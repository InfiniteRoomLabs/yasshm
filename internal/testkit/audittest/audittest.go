// Package audittest wraps the real audit log for tests: FailLog writes
// through an audit.Logger and can be told to fail a named event, the way a
// full disk would fail it, which is how audit-before-act (spec D12) is
// tested event by event; Lines reads the log back. It is test support and
// is imported only by tests.
package audittest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/InfiniteRoomLabs/yasshm/internal/audit"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/leakcheck"
)

// ErrInjected is what Write returns for an event told to fail.
var ErrInjected = errors.New("audit write failed (test)")

// FailLog is the real audit logger with switches that make named events
// fail. It is safe for concurrent use.
type FailLog struct {
	// Path is the log file.
	Path string

	l    *audit.Logger
	mu   sync.Mutex
	fail map[string]bool
}

// Open opens a new audit log in a private temporary directory and closes
// it when the test ends.
func Open(t testing.TB) *FailLog {
	t.Helper()
	p := filepath.Join(leakcheck.PrivDir(t), "audit.jsonl")
	l, err := audit.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return &FailLog{Path: p, l: l, fail: map[string]bool{}}
}

// Detached returns a FailLog with no file behind it, for a configuration
// that must name an audit log but never writes to it; writing to it fails
// the way an injected failure does.
func Detached() *FailLog { return &FailLog{fail: map[string]bool{}} }

// SetFail makes events named name fail (on) or be written again (off).
func (a *FailLog) SetFail(name string, on bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.fail[name] = on
}

// Write writes ev unless its name is set to fail.
func (a *FailLog) Write(ev audit.Event) error {
	a.mu.Lock()
	f := a.fail[ev.Name()]
	a.mu.Unlock()
	if f || a.l == nil {
		return ErrInjected
	}
	return a.l.Write(ev)
}

// Before writes ev and runs act only if the write succeeded.
func (a *FailLog) Before(ev audit.Event, act func() error) error {
	if err := a.Write(ev); err != nil {
		return err
	}
	return act()
}

// Close closes the underlying logger, so every later write fails for real
// (the logger refuses writes after Close).
func (a *FailLog) Close() error {
	if a.l == nil {
		return nil
	}
	return a.l.Close()
}

// Line is the part of an audit event the tests read.
type Line struct {
	Event     string          `json:"event"`
	RequestID string          `json:"request_id"`
	Tenant    string          `json:"tenant"`
	Principal string          `json:"principal"`
	Tool      string          `json:"tool"`
	Server    string          `json:"server"`
	CommandID string          `json:"command_id"`
	Code      string          `json:"code"`
	Rule      string          `json:"rule"`
	Stage     string          `json:"stage"`
	Path      string          `json:"path"`
	Command   string          `json:"command"`
	Status    string          `json:"status"`
	ErrorCode string          `json:"error_code"`
	Signal    string          `json:"signal"`
	Args      json.RawMessage `json:"args"`
}

// Lines returns every event in the log so far.
func (a *FailLog) Lines(t testing.TB) []Line {
	t.Helper()
	b, err := os.ReadFile(a.Path)
	if err != nil {
		t.Fatal(err)
	}
	var out []Line
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 1<<22)
	for sc.Scan() {
		var l Line
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatal(err)
		}
		out = append(out, l)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
