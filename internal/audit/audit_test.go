package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

var (
	alice = tenant.Principal{Tenant: "default", Kind: tenant.KindUser, Subject: "operator@example.net"}
	actor = Actor{RequestID: "req-1", Principal: alice}
	fixed = time.Date(2026, 11, 19, 21, 30, 0, 123000000, time.FixedZone("UTC+2", 2*3600))
)

func clock() time.Time { return fixed }

func openLog(t *testing.T) (l *Logger, path string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "audit.jsonl")
	l, err := Open(path, WithClock(clock))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, path
}

func readLines(t *testing.T, p string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(p) //nolint:gosec // G304: test path from t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("line %q is not one JSON object: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	if len(b) > 0 && b[len(b)-1] != '\n' {
		t.Fatalf("log does not end with a newline: %q", b)
	}
	return out
}

func TestOpen(t *testing.T) {
	t.Run("happy/creates the log with mode 0600", func(t *testing.T) {
		_, p := openLog(t)
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode %04o, want 0600", fi.Mode().Perm())
		}
	})
	t.Run("happy/appends to an existing log, never truncates", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "audit.jsonl")
		prior := `{"ts":"2026-11-18T00:00:00Z","event":"gateway.started"}` + "\n"
		writeMode(t, p, prior, 0o600)
		l, err := Open(p, WithClock(clock))
		if err != nil {
			t.Fatal(err)
		}
		if err := l.Write(GatewayStopped("test")); err != nil {
			t.Fatal(err)
		}
		_ = l.Close()
		b, _ := os.ReadFile(p) //nolint:gosec // G304: test path from t.TempDir
		if !strings.HasPrefix(string(b), prior) || len(readLines(t, p)) != 2 {
			t.Fatalf("log = %q", b)
		}
	})
	t.Run("edge/an existing 0400 log is accepted", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "audit.jsonl")
		writeMode(t, p, "", 0o400)
		l, err := Open(p)
		if err != nil {
			t.Fatal(err)
		}
		_ = l.Close()
	})
	for _, mode := range []os.FileMode{0o640, 0o604, 0o620, 0o644, 0o666} {
		t.Run("sad/refuses an existing log with mode "+mode.String(), func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "audit.jsonl")
			writeMode(t, p, "", mode)
			_, err := Open(p)
			if err == nil || !strings.Contains(err.Error(), "accessible by group or other") {
				t.Fatalf("err = %v", err)
			}
		})
	}
	t.Run("sad/a directory in place of the log fails to open", func(t *testing.T) {
		_, err := Open(t.TempDir())
		if err == nil || !errors.Is(err, syscall.EISDIR) {
			t.Fatalf("err = %v, want EISDIR", err)
		}
	})
	t.Run("sad/a path under a regular file fails to open", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "file")
		writeMode(t, f, "x", 0o600)
		_, err := Open(filepath.Join(f, "audit.jsonl"))
		if err == nil || !errors.Is(err, syscall.ENOTDIR) {
			t.Fatalf("err = %v, want ENOTDIR", err)
		}
	})
	t.Run("raging/a symbolic link is refused", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "real.jsonl")
		writeMode(t, target, "", 0o600)
		link := filepath.Join(dir, "audit.jsonl")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(link); err == nil || !errors.Is(err, syscall.ELOOP) {
			t.Fatalf("err = %v, want ELOOP", err)
		}
	})
	t.Run("raging/a character device is refused", func(t *testing.T) {
		_, err := Open("/dev/null")
		if err == nil {
			t.Fatal("opened /dev/null as an audit log")
		}
	})
}

func writeMode(t *testing.T, p, data string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func TestWrite(t *testing.T) {
	t.Run("happy/one line per event with a UTC RFC 3339 timestamp", func(t *testing.T) {
		l, p := openLog(t)
		if err := l.Write(GatewayStarted("v0.1.0", "127.0.0.1:8822")); err != nil {
			t.Fatal(err)
		}
		lines := readLines(t, p)
		if len(lines) != 1 {
			t.Fatalf("got %d lines", len(lines))
		}
		if lines[0]["ts"] != "2026-11-19T19:30:00.123Z" || lines[0]["event"] != EventGatewayStarted ||
			lines[0]["version"] != "v0.1.0" || lines[0]["listen"] != "127.0.0.1:8822" {
			t.Fatalf("line = %v", lines[0])
		}
		if _, err := time.Parse(time.RFC3339, lines[0]["ts"].(string)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("raging/newlines in command text stay inside one line", func(t *testing.T) {
		l, p := openLog(t)
		if err := l.Write(CommandRequested(actor, "lab-1", "c1", "ls\nreboot\r\n", 60)); err != nil {
			t.Fatal(err)
		}
		lines := readLines(t, p)
		if len(lines) != 1 || lines[0]["command"] != "ls\nreboot\r\n" {
			t.Fatalf("lines = %v", lines)
		}
	})
	t.Run("corner/concurrent writers never interleave a line", func(t *testing.T) {
		l, p := openLog(t)
		const writers, each = 32, 50
		long := strings.Repeat("x", 8192)
		var wg sync.WaitGroup
		errs := make(chan error, writers*each)
		for w := range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range each {
					errs <- l.Write(CommandRequested(actor, "lab-1", tenant.CommandID(strings.Repeat("c", w+1)), long, int64(i)))
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		lines := readLines(t, p)
		if len(lines) != writers*each {
			t.Fatalf("got %d lines, want %d", len(lines), writers*each)
		}
		for _, m := range lines {
			if m["command"] != long {
				t.Fatal("a line was torn")
			}
		}
	})
	t.Run("sad/write after close", func(t *testing.T) {
		l, _ := openLog(t)
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
		if err := l.Write(GatewayStopped("x")); !errors.Is(err, ErrClosed) {
			t.Fatalf("err = %v", err)
		}
		if err := l.Close(); err != nil {
			t.Fatalf("second Close = %v", err)
		}
	})
}

// fakeFile fails on demand.
type fakeFile struct {
	buf               bytes.Buffer
	writeErr, syncErr error
	short             bool
	syncs             int
}

func (f *fakeFile) Write(b []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	if f.short {
		return len(b) - 1, nil
	}
	return f.buf.Write(b)
}
func (f *fakeFile) Sync() error  { f.syncs++; return f.syncErr }
func (f *fakeFile) Close() error { return nil }

func TestAuditBeforeAct(t *testing.T) {
	ev := CommandRequested(actor, "lab-1", "c1", "uptime", 60)
	t.Run("happy/the action runs after a successful write", func(t *testing.T) {
		l, p := openLog(t)
		ran := false
		if err := l.Before(ev, func() error { ran = true; return nil }); err != nil || !ran {
			t.Fatalf("err = %v, ran = %v", err, ran)
		}
		if len(readLines(t, p)) != 1 {
			t.Fatal("event not written before the action")
		}
	})
	t.Run("happy/the action's own error is returned", func(t *testing.T) {
		l, _ := openLog(t)
		boom := errors.New("boom")
		if err := l.Before(ev, func() error { return boom }); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("sad/a write that fails (ENOSPC on /dev/full) blocks the action", func(t *testing.T) {
		f, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		l := newLogger(f)
		t.Cleanup(func() { _ = l.Close() })
		ran := false
		err = l.Before(ev, func() error { ran = true; return nil })
		if err == nil || !errors.Is(err, syscall.ENOSPC) {
			t.Fatalf("err = %v, want ENOSPC", err)
		}
		if ran {
			t.Fatal("the guarded action ran although the audit write failed")
		}
		t.Run("corner/the logger stays failed", func(t *testing.T) {
			if err := l.Before(ev, func() error { ran = true; return nil }); err == nil || ran {
				t.Fatalf("second Before: err = %v, ran = %v", err, ran)
			}
		})
	})
	t.Run("sad/a log that cannot be opened blocks the action", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "file")
		writeMode(t, f, "", 0o600)
		ran := false
		act := func() error { ran = true; return nil }
		l, err := Open(filepath.Join(f, "audit.jsonl"))
		if err == nil {
			err = l.Before(ev, act)
		}
		if err == nil || ran {
			t.Fatalf("err = %v, ran = %v", err, ran)
		}
	})
	t.Run("sad/a sync that fails blocks the action", func(t *testing.T) {
		ff := &fakeFile{syncErr: syscall.EIO}
		l := newLogger(ff, WithClock(clock))
		ran := false
		if err := l.Before(ev, func() error { ran = true; return nil }); !errors.Is(err, syscall.EIO) || ran {
			t.Fatalf("err = %v, ran = %v", err, ran)
		}
	})
	t.Run("raging/a short write blocks the action", func(t *testing.T) {
		ff := &fakeFile{short: true}
		l := newLogger(ff, WithClock(clock))
		ran := false
		if err := l.Before(ev, func() error { ran = true; return nil }); err == nil || ran {
			t.Fatalf("err = %v, ran = %v", err, ran)
		}
	})
	t.Run("edge/each event is synced once", func(t *testing.T) {
		ff := &fakeFile{}
		l := newLogger(ff, WithClock(clock))
		for range 3 {
			if err := l.Write(ev); err != nil {
				t.Fatal(err)
			}
		}
		if ff.syncs != 3 || strings.Count(ff.buf.String(), "\n") != 3 {
			t.Fatalf("syncs = %d, lines = %d", ff.syncs, strings.Count(ff.buf.String(), "\n"))
		}
	})
	t.Run("edge/the default clock is UTC", func(t *testing.T) {
		ff := &fakeFile{}
		l := newLogger(ff)
		if err := l.Write(ev); err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(ff.buf.Bytes(), &m); err != nil || !strings.HasSuffix(m["ts"].(string), "Z") {
			t.Fatalf("ts = %v, %v", m["ts"], err)
		}
	})
}
