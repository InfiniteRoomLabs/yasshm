package jobs

import (
	"bytes"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// checkPage asserts the properties of page for one input.
func checkPage(t *testing.T, offset, size, limit int64) {
	t.Helper()
	start, next := page(offset, size, limit)
	sz := max(size, 0)
	if start < 0 || next < 0 {
		t.Fatalf("page(%d, %d, %d) = %d, %d: negative", offset, size, limit, start, next)
	}
	if start > sz || next > sz {
		t.Fatalf("page(%d, %d, %d) = %d, %d: beyond size", offset, size, limit, start, next)
	}
	if next < start || next-start > max(limit, 0) {
		t.Fatalf("page(%d, %d, %d) = %d, %d: length %d", offset, size, limit, start, next, next-start)
	}
	if next-start < min(max(limit, 0), sz-start) {
		t.Fatalf("page(%d, %d, %d) = %d, %d: short read", offset, size, limit, start, next)
	}
	if offset < 0 {
		from := int64(0)
		if offset >= -sz {
			from = sz + offset
		}
		s2, n2 := page(from, size, limit)
		if s2 != start || n2 != next {
			t.Fatalf("page(%d, %d, %d) = %d, %d but from the start %d gives %d, %d", offset, size, limit, start, next, from, s2, n2)
		}
	}
}

func TestPage(t *testing.T) {
	cases := []struct {
		name                string
		offset, size, limit int64
		wantStart, wantNext int64
	}{
		{"happy/from the start", 0, 10, 4, 0, 4},
		{"happy/in the middle", 3, 10, 4, 3, 7},
		{"happy/from the end", -4, 10, 100, 6, 10},
		{"edge/limit past the end", 8, 10, 100, 8, 10},
		{"edge/offset at size", 10, 10, 5, 10, 10},
		{"edge/offset beyond size", 50, 10, 5, 10, 10},
		{"edge/negative beyond the start clamps to 0", -50, 10, 3, 0, 3},
		{"edge/empty stream", -1, 0, 5, 0, 0},
		{"edge/zero limit", 2, 10, 0, 2, 2},
		{"corner/minimum int offset", math.MinInt64, 10, 5, 0, 5},
		{"corner/maximum values", math.MaxInt64, math.MaxInt64, math.MaxInt64, math.MaxInt64, math.MaxInt64},
		{"raging/negative size and limit", 3, -5, -5, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, n := page(c.offset, c.size, c.limit)
			if s != c.wantStart || n != c.wantNext {
				t.Fatalf("page(%d, %d, %d) = %d, %d; want %d, %d", c.offset, c.size, c.limit, s, n, c.wantStart, c.wantNext)
			}
			checkPage(t, c.offset, c.size, c.limit)
		})
	}
}

func FuzzPage(f *testing.F) {
	for _, s := range [][3]int64{{0, 0, 0}, {-1, 10, 3}, {5, 3, 1}, {math.MinInt64, 1, 1}, {math.MaxInt64, math.MaxInt64, 1}, {-7, 7, math.MaxInt64}} {
		f.Add(s[0], s[1], s[2])
	}
	f.Fuzz(func(t *testing.T, offset, size, limit int64) {
		checkPage(t, offset, size, limit)
	})
}

// memFile is an in-memory output file.
type memFile struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	err    error
	closed bool
}

func (m *memFile) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return 0, m.err
	}
	return m.buf.Write(p)
}

func (m *memFile) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *memFile) String() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.buf.String()
}

func TestCapture(t *testing.T) {
	t.Run("happy/merged streams keep arrival order in one file", func(t *testing.T) {
		out := &memFile{}
		c := newCapture([2]io.WriteCloser{out, nil}, true, 100, nil)
		for _, w := range []struct {
			i int
			s string
		}{{outIdx, "a"}, {errIdx, "B"}, {outIdx, "c"}, {errIdx, "D"}} {
			if n, err := c.writer(w.i).Write([]byte(w.s)); n != 1 || err != nil {
				t.Fatal(n, err)
			}
		}
		snap := c.snapshot()
		if out.String() != "aBcD" || snap.sizes != [2]int64{4, 0} || snap.received != [2]int64{2, 2} || snap.truncated {
			t.Fatalf("%q %+v", out.String(), snap)
		}
	})
	t.Run("happy/separate streams go to separate files", func(t *testing.T) {
		out, errf := &memFile{}, &memFile{}
		c := newCapture([2]io.WriteCloser{out, errf}, false, 100, nil)
		_, _ = c.writer(outIdx).Write([]byte("out"))
		_, _ = c.writer(errIdx).Write([]byte("err!"))
		if snap := c.snapshot(); out.String() != "out" || errf.String() != "err!" || snap.sizes != [2]int64{3, 4} || snap.stored != 7 {
			t.Fatalf("%q %q %+v", out.String(), errf.String(), snap)
		}
	})
	t.Run("edge/truncation at the exact byte boundary keeps accepting", func(t *testing.T) {
		out, errf := &memFile{}, &memFile{}
		c := newCapture([2]io.WriteCloser{out, errf}, false, 10, nil)
		if n, err := c.writer(outIdx).Write([]byte("0123456")); n != 7 || err != nil {
			t.Fatal(n, err)
		}
		if c.snapshot().truncated {
			t.Fatal("truncated below the limit")
		}
		if n, err := c.writer(errIdx).Write([]byte("789")); n != 3 || err != nil {
			t.Fatal(n, err)
		}
		if c.snapshot().truncated {
			t.Fatal("truncated at exactly the limit")
		}
		if n, err := c.writer(errIdx).Write([]byte("X")); n != 1 || err != nil {
			t.Fatal(n, err)
		}
		for range 1000 {
			if n, err := c.writer(outIdx).Write(bytes.Repeat([]byte("y"), 1000)); n != 1000 || err != nil {
				t.Fatal(n, err)
			}
		}
		snap := c.snapshot()
		if !snap.truncated || snap.stored != 10 || out.String() != "0123456" || errf.String() != "789" ||
			snap.received != [2]int64{7 + 1000*1000, 4} {
			t.Fatalf("%+v %q %q", snap, out.String(), errf.String())
		}
	})
	t.Run("edge/a write that straddles the limit stores the part that fits", func(t *testing.T) {
		out := &memFile{}
		c := newCapture([2]io.WriteCloser{out, nil}, true, 5, nil)
		_, _ = c.writer(outIdx).Write([]byte("abcdefgh"))
		if snap := c.snapshot(); out.String() != "abcde" || !snap.truncated || snap.stored != 5 {
			t.Fatalf("%q %+v", out.String(), snap)
		}
	})
	t.Run("sad/a disk error is reported once and never returned to the SSH layer", func(t *testing.T) {
		full, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		var calls int
		var got error
		c := newCapture([2]io.WriteCloser{full, nil}, true, 100, func(e error) { calls++; got = e })
		for range 3 {
			if n, werr := c.writer(outIdx).Write([]byte("data")); n != 4 || werr != nil {
				t.Fatal(n, werr)
			}
		}
		snap := c.snapshot()
		if calls != 1 || got == nil || snap.diskErr == nil || snap.received[outIdx] != 12 {
			t.Fatalf("calls %d err %v %+v", calls, got, snap)
		}
		if err := c.close(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("sad/a short write counts as a disk error", func(t *testing.T) {
		c := newCapture([2]io.WriteCloser{shortFile{}, nil}, true, 100, nil)
		_, _ = c.writer(outIdx).Write([]byte("abcd"))
		if snap := c.snapshot(); !errors.Is(snap.diskErr, io.ErrShortWrite) || snap.stored != 2 {
			t.Fatalf("%+v", snap)
		}
	})
	t.Run("happy/every change wakes a waiter, and close wakes it too", func(t *testing.T) {
		out := &memFile{}
		c := newCapture([2]io.WriteCloser{out, nil}, true, 100, nil)
		ch := c.snapshot().changed
		_, _ = c.writer(outIdx).Write([]byte("x"))
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("not woken by a write")
		}
		ch = c.snapshot().changed
		_, _ = c.writer(outIdx).Write(nil) // an empty write changes nothing
		select {
		case <-ch:
			t.Fatal("woken by an empty write")
		default:
		}
		if err := c.close(); err != nil || !out.closed {
			t.Fatal(err)
		}
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("not woken by close")
		}
		_, _ = c.writer(outIdx).Write([]byte("late"))
		if snap := c.snapshot(); out.String() != "x" || snap.received[outIdx] != 5 {
			t.Fatalf("%q %+v", out.String(), snap)
		}
		if err := c.close(); err != nil {
			t.Fatal("second close", err)
		}
	})
	t.Run("corner/concurrent writers under -race never lose a count", func(t *testing.T) {
		out, errf := &memFile{}, &memFile{}
		c := newCapture([2]io.WriteCloser{out, errf}, false, 5000, nil)
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 100 {
					_, _ = c.writer(i % 2).Write([]byte("0123456789"))
				}
			}()
		}
		wg.Wait()
		snap := c.snapshot()
		if snap.received != [2]int64{4000, 4000} || snap.stored != 5000 || !snap.truncated || int64(len(out.String())+len(errf.String())) != 5000 {
			t.Fatalf("%+v", snap)
		}
	})
}

type shortFile struct{}

func (shortFile) Write(p []byte) (int, error) { return len(p) / 2, nil }
func (shortFile) Close() error                { return nil }

func TestOutputPaths(t *testing.T) {
	dir := t.TempDir()
	t.Run("happy/the layout of spec 5.6", func(t *testing.T) {
		p, err := commandDir(dir, "tenant-a", "0123456789abcdef0123456789abcdef")
		if err != nil {
			t.Fatal(err)
		}
		if p != filepath.Join(dir, "tenants", "tenant-a", "commands", "0123456789abcdef0123456789abcdef") {
			t.Fatal(p)
		}
	})
	t.Run("raging/hostile ids never reach a path", func(t *testing.T) {
		good := tenant.CommandID("0123456789abcdef0123456789abcdef")
		for _, tid := range []tenant.TenantID{"", "..", "../x", "a/b", "A", "a\x00b", ".", "a b", tenant.TenantID(strings.Repeat("a", 64))} {
			if _, err := commandDir(dir, tid, good); CodeOf(err) != CodeInvalidArgument {
				t.Errorf("tenant %q: %v", tid, err)
			}
		}
		for _, id := range []tenant.CommandID{"", "..", "../../../../etc", "0123456789abcdef0123456789abcde/", "0123456789ABCDEF0123456789ABCDEF", "0123456789abcdef0123456789abcdef0"} {
			if _, err := commandDir(dir, "tenant-a", id); CodeOf(err) != CodeInvalidArgument {
				t.Errorf("id %q: %v", id, err)
			}
		}
	})
	t.Run("happy/files are created 0600 and exclusively, directories 0700", func(t *testing.T) {
		d := filepath.Join(dir, "a", "b")
		if err := mkdirPrivate(d); err != nil {
			t.Fatal(err)
		}
		f, err := createOutput(filepath.Join(d, "stdout"))
		if err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
		if fi, _ := os.Stat(filepath.Join(d, "stdout")); fi.Mode().Perm() != 0o600 {
			t.Fatal(fi.Mode())
		}
		if fi, _ := os.Stat(d); fi.Mode().Perm() != 0o700 {
			t.Fatal(fi.Mode())
		}
		if _, err := createOutput(filepath.Join(d, "stdout")); !errors.Is(err, os.ErrExist) {
			t.Fatalf("second create: %v", err)
		}
	})
	t.Run("raging/a symbolic link in place of an output file is refused", func(t *testing.T) {
		d := filepath.Join(dir, "link")
		if err := mkdirPrivate(d); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("/etc/passwd", filepath.Join(d, "stdout")); err != nil {
			t.Fatal(err)
		}
		if _, err := createOutput(filepath.Join(d, "stdout")); err == nil {
			t.Fatal("created through a link")
		}
		if _, err := readFileRange(filepath.Join(d, "stdout"), 0, 10); err == nil {
			t.Fatal("read through a link")
		}
		if _, err := fileSize(filepath.Join(d, "stdout")); err == nil {
			t.Fatal("sized a link")
		}
	})
	t.Run("sad/a directory under a regular file cannot be created", func(t *testing.T) {
		f := filepath.Join(dir, "plain")
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := mkdirPrivate(filepath.Join(f, "sub")); err == nil {
			t.Fatal("created under a file")
		}
		if err := mkdirPrivate(f); err == nil {
			t.Fatal("a file accepted as a directory")
		}
	})
	t.Run("edge/a missing file reads empty and sizes zero", func(t *testing.T) {
		b, err := readFileRange(filepath.Join(dir, "none"), 0, 5)
		if err != nil || len(b) != 0 {
			t.Fatal(b, err)
		}
		if n, err := fileSize(filepath.Join(dir, "none")); n != 0 || err != nil {
			t.Fatal(n, err)
		}
		if b, err := readFileRange(filepath.Join(dir, "plain"), 3, 3); err != nil || len(b) != 0 {
			t.Fatal(b, err)
		}
		if _, err := readFileRange(filepath.Join(dir, "plain", "x"), 0, 3); err == nil {
			t.Fatal("read under a file")
		}
		if _, err := fileSize(filepath.Join(dir, "plain", "x")); err == nil {
			t.Fatal("sized under a file")
		}
	})
}
