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
// the log ambiguous. The gateway fails closed until it is restarted, and
// Open ends an unterminated last line first, so a line left half-written by
// a crash cannot swallow the next event.
//
// Open refuses a symbolic link with O_NOFOLLOW, which covers the last
// element of the path only: a link in a parent directory is followed. That
// is enough because whoever can change the gateway's directories is root on
// the gateway host, which is outside the threat model (spec section 6).
package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// ErrClosed is returned by Write after Close.
var ErrClosed = errors.New("audit: log is closed")

// ErrNoName is returned by Write for an Event that no constructor built.
var ErrNoName = errors.New("audit: event has no name")

// euid is the effective uid that must own the log; a variable so tests can
// check a mismatch without root.
var euid = os.Geteuid

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
// appending. It refuses a path that is a symbolic link or is not a regular
// file (checked before opening, so a FIFO cannot block it, and again on the
// open file), whose mode gives group or other any access, or that is not
// owned by the effective uid, and a log whose directory group or other can
// write. Mode and owner are read from the open file,
// so the checks also hold for root. If the existing log does not end in a
// newline, Open writes one.
func Open(path string, opts ...Option) (*Logger, error) {
	// Whoever can write the log's directory can unlink or replace the log
	// while the gateway keeps writing to the old file.
	if di, err := os.Stat(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("audit: directory of %s: %w", path, err)
	} else if perm := di.Mode().Perm(); perm&0o022 != 0 {
		return nil, fmt.Errorf("audit: the directory of %s is writable by group or other (mode %04o)", path, perm)
	}
	if fi, err := os.Lstat(path); err == nil && !fi.Mode().IsRegular() && fi.Mode()&os.ModeSymlink == 0 {
		return nil, fmt.Errorf("audit: %s is not a regular file", path)
	}
	// The path comes from the operator's config; opening it is the point.
	// O_RDWR so the last byte can be read; O_NONBLOCK so a FIFO swapped in
	// after the check cannot block.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600) //nolint:gosec // G304: the audit log path is the operator's config
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
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != euid() {
		_ = f.Close()
		return nil, fmt.Errorf("audit: %s is not owned by the gateway's uid %d", path, euid())
	}
	if err := terminateLastLine(f, fi.Size()); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("audit: %s: %w", path, err)
	}
	return newLogger(f, opts...), nil
}

// terminateLastLine appends a newline when the file's last byte is not one.
func terminateLastLine(f *os.File, size int64) error {
	if size == 0 {
		return nil
	}
	last := make([]byte, 1)
	if _, err := f.ReadAt(last, size-1); err != nil {
		return fmt.Errorf("read last byte: %w", err)
	}
	if last[0] == '\n' {
		return nil
	}
	if _, err := f.Write([]byte{'\n'}); err != nil {
		return fmt.Errorf("end the last line: %w", err)
	}
	return f.Sync()
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
	if rec.Event == "" {
		return ErrNoName
	}
	rec.TS = l.now().UTC().Format(time.RFC3339Nano)
	// No HTML escaping, so & < > stay greppable; JSON string escaping still
	// keeps every newline inside the one line. Encode adds the newline.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(rec); err != nil {
		return fmt.Errorf("audit: encode %s: %w", rec.Event, err)
	}
	line := buf.Bytes()
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
