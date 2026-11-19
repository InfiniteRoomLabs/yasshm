package jobs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/InfiniteRoomLabs/yasshm/internal/store"
	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// The output streams.
const (
	StreamStdout = "stdout"
	StreamStderr = "stderr"
)

// stream indexes.
const (
	outIdx = 0
	errIdx = 1
)

func streamIndex(s string) (int, bool) {
	switch s {
	case StreamStdout:
		return outIdx, true
	case StreamStderr:
		return errIdx, true
	default:
		return 0, false
	}
}

// tenantsDir is the directory under the data directory that holds the
// output files: tenants/<tenant>/commands/<id>/{stdout,stderr}.
const tenantsDir = "tenants"

// commandDir returns the output directory of a command. Both ids are checked
// against their strict patterns first, so no "..", separator or other byte
// outside [a-z0-9-] can reach the path.
func commandDir(dataDir string, tid tenant.TenantID, id tenant.CommandID) (string, error) {
	if !store.ValidTenantID(tid) || !store.ValidCommandID(id) {
		return "", invalid("malformed tenant or command id")
	}
	return filepath.Join(dataDir, tenantsDir, string(tid), "commands", string(id)), nil
}

// mkdirPrivate creates dir and its missing parents with mode 0700 and checks
// that the last element is a directory with no group or other bits.
func mkdirPrivate(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() || fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("output directory is not a private directory")
	}
	return nil
}

// createOutput creates one output file: new (O_EXCL), not through a link
// (O_NOFOLLOW), append-only, mode 0600.
func createOutput(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600) //nolint:gosec // G304: path is built from validated ids by commandDir
}

// openOutputFunc creates one of a command's output files; a
// test replaces it to inject a failing file.
type openOutputFunc func(path string) (io.WriteCloser, error)

func defaultOpenOutput(path string) (io.WriteCloser, error) { return createOutput(path) }

// capture is the sink both output streams of one command are written to. It
// counts the bytes received per stream, stores at most limit bytes in all,
// never blocks the SSH layer beyond a local file write, and never returns an
// error to it: a full quota sets truncated and keeps accepting (so the
// remote side is drained), and a disk error is recorded, reported once
// through onDiskErr (which ends the command) and the rest is discarded.
type capture struct {
	mu        sync.Mutex
	files     [2]io.WriteCloser // files[errIdx] is nil when merged
	merge     bool
	limit     int64
	stored    int64    // bytes stored in all
	sizes     [2]int64 // bytes stored per file
	received  [2]int64 // bytes received per stream
	truncated bool
	diskErr   error
	closed    bool
	changed   chan struct{} // closed and replaced on every change
	onDiskErr func(error)
}

func newCapture(files [2]io.WriteCloser, merge bool, limit int64, onDiskErr func(error)) *capture {
	return &capture{files: files, merge: merge, limit: limit, changed: make(chan struct{}), onDiskErr: onDiskErr}
}

// streamWriter is the io.Writer of one stream.
type streamWriter struct {
	c   *capture
	idx int
}

// Write stores what fits and always reports the whole of p as written.
func (w streamWriter) Write(p []byte) (int, error) {
	w.c.write(w.idx, p)
	return len(p), nil
}

func (c *capture) writer(idx int) io.Writer { return streamWriter{c: c, idx: idx} }

func (c *capture) write(idx int, p []byte) {
	if len(p) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.received[idx] += int64(len(p))
	if c.diskErr != nil || c.closed {
		return
	}
	room := c.limit - c.stored
	if int64(len(p)) > room {
		c.truncated = true
		if room < 0 {
			room = 0
		}
		p = p[:room]
	}
	if len(p) > 0 {
		fi := idx
		if c.merge {
			fi = outIdx
		}
		n, err := c.files[fi].Write(p)
		if n < 0 {
			n = 0
		}
		c.stored += int64(n)
		c.sizes[fi] += int64(n)
		if err == nil && n < len(p) {
			err = io.ErrShortWrite
		}
		if err != nil {
			c.diskErr = err
			if c.onDiskErr != nil {
				c.onDiskErr(err)
			}
		}
	}
	c.notifyLocked()
}

func (c *capture) notifyLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
}

// snapshot is the capture's state at one moment.
type snapshot struct {
	sizes     [2]int64
	received  [2]int64
	stored    int64
	truncated bool
	diskErr   error
	changed   <-chan struct{}
}

func (c *capture) snapshot() snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return snapshot{sizes: c.sizes, received: c.received, stored: c.stored, truncated: c.truncated, diskErr: c.diskErr, changed: c.changed}
}

// close closes the files; later writes are counted and discarded. It wakes
// every waiting reader.
func (c *capture) close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	var errs []error
	for _, f := range c.files {
		if f != nil {
			errs = append(errs, f.Close())
		}
	}
	c.notifyLocked()
	return errors.Join(errs...)
}

// page is the paging arithmetic of ReadOutput, in one pure function. For a
// stream of size bytes, a read at offset (>= 0 from the start, < 0 from the
// end, clamped to [0, size]) of at most limit bytes covers [start, next).
// It never returns a negative value or one beyond size, and next-start is at
// most limit. A negative size or limit counts as zero.
func page(offset, size, limit int64) (start, next int64) {
	if size < 0 {
		size = 0
	}
	if limit < 0 {
		limit = 0
	}
	switch {
	case offset >= 0:
		start = min(offset, size)
	case offset < -size: // also catches math.MinInt64 without negating it
		start = 0
	default:
		start = size + offset
	}
	return start, start + min(limit, size-start)
}

// readFileRange reads [start, next) of the output file at path. A file that
// does not exist yet reads as empty.
func readFileRange(path string, start, next int64) ([]byte, error) {
	if next <= start {
		return []byte{}, nil
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0) //nolint:gosec // G304: path is built from validated ids by commandDir
	if errors.Is(err, os.ErrNotExist) {
		return []byte{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, next-start)
	n, err := f.ReadAt(buf, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:n], nil
}

// fileSize returns the size of the output file at path, 0 when it does not
// exist.
func fileSize(path string) (int64, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !fi.Mode().IsRegular() {
		return 0, fmt.Errorf("output file is not a regular file")
	}
	return fi.Size(), nil
}
