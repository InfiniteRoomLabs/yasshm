// Package audit writes the append-only JSONL audit log (spec 5.4).
//
// Each event is one JSON object on one line, written with one write call and
// then synced. The log is opened with O_APPEND and mode 0600 and is never
// truncated. Events are built only by the typed constructors in event.go, and
// none of them can carry a token, key material, file content or command
// output; command text appears only in command.requested and in the
// arguments of tool.called.
//
// Audit-before-act (spec D12): a handler writes the "requested" event and
// does the action only if that write succeeded. Logger.Before is that
// pattern:
//
//	err := log.Before(audit.CommandRequested(actor, srv, id, cmd, timeout), func() error {
//		return start(cmd)
//	})
//
// After any failed write or sync the Logger refuses every later write: a
// failed write may have left a partial line, and appending to it would make
// the log ambiguous. The gateway fails closed until it is restarted.
package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"
	"time"
)

// ErrClosed is returned by Write after Close.
var ErrClosed = errors.New("audit: log is closed")

// file is what the Logger writes to; *os.File in production.
type file interface {
	io.Writer
	Sync() error
	Close() error
}

// Logger appends events to the audit log. It is safe for concurrent use.
type Logger struct {
	mu     sync.Mutex
	f      file
	now    func() time.Time
	broken error
	closed bool
}

// Option configures Open.
type Option func(*Logger)

// WithClock sets the clock that stamps events; the default is time.Now.
// Timestamps are always written in UTC.
func WithClock(now func() time.Time) Option {
	return func(l *Logger) { l.now = now }
}

// Open opens (creating it with mode 0600 if needed) the audit log at path for
// appending. It refuses a path that is a symbolic link, is not a regular
// file, or whose mode gives group or other any access; the mode is read from
// the open file, so the check also holds for root.
func Open(path string, opts ...Option) (*Logger, error) {
	// The path comes from the operator's config; opening it is the point.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600) //nolint:gosec // G304: the audit log path is the operator's config
	if err != nil {
		return nil, fmt.Errorf("audit: open %s: %w", path, err)
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("audit: stat %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("audit: %s is not a regular file", path)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		_ = f.Close()
		return nil, fmt.Errorf("audit: %s is accessible by group or other (mode %04o); it must be 0600 or stricter", path, perm)
	}
	return newLogger(f, opts...), nil
}

func newLogger(f file, opts ...Option) *Logger {
	l := &Logger{f: f, now: time.Now}
	for _, o := range opts {
		o(l)
	}
	return l
}

// Write stamps ev with the current UTC time and appends it as one line with
// one write call, then syncs the file. It returns an error, and the Logger
// refuses later writes, if the write or the sync fails.
func (l *Logger) Write(ev Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return ErrClosed
	}
	if l.broken != nil {
		return fmt.Errorf("audit: log unusable after an earlier failure: %w", l.broken)
	}
	rec := ev.rec
	rec.TS = l.now().UTC().Format(time.RFC3339Nano)
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("audit: encode %s: %w", rec.Event, err)
	}
	line = append(line, '\n')
	n, err := l.f.Write(line)
	if err == nil && n != len(line) {
		err = io.ErrShortWrite
	}
	if err != nil {
		l.broken = err
		return fmt.Errorf("audit: write: %w", err)
	}
	if err := l.f.Sync(); err != nil {
		l.broken = err
		return fmt.Errorf("audit: sync: %w", err)
	}
	return nil
}

// Before writes ev and, only if that succeeded, runs act and returns its
// error. If the write fails, act does not run and the write error is
// returned (spec D12).
func (l *Logger) Before(ev Event, act func() error) error {
	if err := l.Write(ev); err != nil {
		return err
	}
	return act()
}

// Close closes the log. Later writes return ErrClosed.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	return l.f.Close()
}
