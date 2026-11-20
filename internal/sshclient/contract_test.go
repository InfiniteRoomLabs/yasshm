package sshclient

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/leakcheck"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/loopback"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/realsshd"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/sshtest"
)

// scenario is one case of the SSH contract suite: it holds against the
// in-process server and against a real OpenSSH sshd alike.
type scenario struct {
	name string
	run  func(t *testing.T, tg target, c *Client)
}

// countWriter counts bytes and checks they are all one byte value.
type countWriter struct {
	mu    sync.Mutex
	n     int
	want  byte
	wrong int
}

func (w *countWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.n += len(p)
	for _, b := range p {
		if b != w.want {
			w.wrong++
		}
	}
	return len(p), nil
}

// gateWriter blocks its first write until release is closed, then counts.
type gateWriter struct {
	release chan struct{}
	entered chan struct{}
	once    sync.Once
	mu      sync.Mutex
	n       int
}

func (w *gateWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	w.mu.Lock()
	w.n += len(p)
	w.mu.Unlock()
	return len(p), nil
}

func exec1(t *testing.T, tg target, c *Client, command string) (st ExitStatus, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	// Bounded, so a client that leaves stdin open fails here instead of
	// hanging until the test binary's timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var err error
	st, err = c.Exec(ctx, tg.tid, tg.srv, command, &out, &errb)
	if err != nil {
		t.Fatalf("Exec(%q): %v", command, err)
	}
	return st, out.String(), errb.String()
}

// readyCommand prints the pid of the session's shell, then replaces the
// shell (bash on the CI runner, which ignores SIGQUIT and discards a QUIT
// that arrives before its exec) with sh, which prints "ready" and execs
// sleep. A caller that waits for "ready" signals sh or sleep, never bash.
const readyCommand = `echo $$; exec sh -c 'echo ready; exec sleep 30'`

// startReady starts readyCommand and returns once the remote side printed
// "ready", so a signal cannot reach a shell that ignores it.
func startReady(t *testing.T, tg target, c *Client) (cmd *Command, pid int) {
	t.Helper()
	out := newNotifyBuffer()
	cmd, pid = startWithPidOut(t, tg, c, readyCommand, out)
	out.waitFor(t, "ready\n", 10*time.Second)
	return cmd, pid
}

// waitBounded waits at most 10 seconds for cmd: a signal that was accepted
// but never took effect fails in seconds, not after the command's sleep.
func waitBounded(t *testing.T, cmd *Command) (ExitStatus, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := cmd.Wait(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the command did not end within 10 s (a signal accepted but not delivered?): status %+v", st)
	}
	return st, err
}

// startWithPid starts command, which must print one pid as its first line,
// and returns the handle and the pid; the pid is killed in a cleanup.
func startWithPid(t *testing.T, tg target, c *Client, command string) (cmd *Command, pid int) {
	t.Helper()
	return startWithPidOut(t, tg, c, command, newNotifyBuffer())
}

// startWithPidOut is startWithPid with the caller's output buffer.
func startWithPidOut(t *testing.T, tg target, c *Client, command string, out *notifyBuffer) (cmd *Command, pid int) {
	t.Helper()
	cmd, err := c.Start(context.Background(), tg.tid, tg.srv, command, out, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Close() })
	pid, err = strconv.Atoi(strings.TrimSpace(out.waitLine(t, 10*time.Second)))
	if err != nil || pid <= 1 {
		t.Fatalf("no pid in %q", out.String())
	}
	t.Cleanup(func() {
		if alive(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return cmd, pid
}

var scenarios = []scenario{
	{"happy/echo", func(t *testing.T, tg target, c *Client) {
		st, out, _ := exec1(t, tg, c, "echo ok")
		if st.Outcome != OutcomeExited || st.Code != 0 || out != "ok\n" || st.MayStillRun || st.Stopped != StopNone {
			t.Fatalf("status %+v, out %q", st, out)
		}
	}},
	{"happy/exit codes", func(t *testing.T, tg target, c *Client) {
		for _, code := range []int{0, 1, 3, 255} {
			st, _, _ := exec1(t, tg, c, "exit "+strconv.Itoa(code))
			if st.Outcome != OutcomeExited || st.Code != code {
				t.Fatalf("exit %d: status %+v", code, st)
			}
		}
	}},
	{"happy/stdout and stderr are separate", func(t *testing.T, tg target, c *Client) {
		_, out, errs := exec1(t, tg, c, "echo out; echo err >&2")
		if out != "out\n" || errs != "err\n" {
			t.Fatalf("out %q err %q", out, errs)
		}
	}},
	{"happy/exit by signal", func(t *testing.T, tg target, c *Client) {
		st, _, _ := exec1(t, tg, c, "kill -TERM $$")
		if st.Outcome != OutcomeSignaled || st.Signal != "TERM" || st.MayStillRun {
			t.Fatalf("status %+v", st)
		}
	}},
	{"edge/stdin is closed so a reader gets EOF", func(t *testing.T, tg target, c *Client) {
		st, out, _ := exec1(t, tg, c, "cat; echo eof")
		if st.Code != 0 || out != "eof\n" {
			t.Fatalf("status %+v out %q", st, out)
		}
	}},
	{"edge/the command is sent exactly as given", func(t *testing.T, tg target, c *Client) {
		_, out, _ := exec1(t, tg, c, `printf '%s|' "a b" 'c'"'"'d' $((1+2))`)
		if out != "a b|c'd|3|" {
			t.Fatalf("out %q", out)
		}
	}},
	{"raging/several megabytes on both streams", func(t *testing.T, tg target, c *Client) {
		const n = 3_000_000
		o, e := &countWriter{want: 'o'}, &countWriter{want: 'e'}
		cmd := "head -c 3000000 /dev/zero | tr '\\0' o; head -c 3000000 /dev/zero | tr '\\0' e >&2"
		st, err := c.Exec(context.Background(), tg.tid, tg.srv, cmd, o, e)
		if err != nil || st.Code != 0 {
			t.Fatalf("status %+v err %v", st, err)
		}
		if o.n != n || e.n != n || o.wrong+e.wrong != 0 {
			t.Fatalf("stdout %d stderr %d bytes, %d wrong", o.n, e.n, o.wrong+e.wrong)
		}
	}},
	{"edge/a slow writer applies back-pressure", func(t *testing.T, tg target, c *Client) {
		marker := filepath.Join(tg.dir, "bp-done")
		w := &gateWriter{release: make(chan struct{}), entered: make(chan struct{})}
		cmd, err := c.Start(context.Background(), tg.tid, tg.srv, "head -c 16000000 /dev/zero; touch "+marker, w, nil)
		if err != nil {
			t.Fatal(err)
		}
		<-w.entered
		// With the writer blocked, the SSH window stops the remote side
		// long before 16 MB: the marker must not appear.
		if leakcheck.Poll(500*time.Millisecond, func() bool { _, serr := os.Stat(marker); return serr == nil }) {
			close(w.release)
			_ = cmd.Close()
			t.Fatal("the command finished while the writer was blocked: output was buffered without bound")
		}
		close(w.release)
		st, err := waitBounded(t, cmd)
		if err != nil || st.Code != 0 || w.n != 16_000_000 {
			t.Fatalf("status %+v err %v bytes %d", st, err, w.n)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatal(err)
		}
	}},
	{"happy/a deadline sends TERM and the error is timeout", func(t *testing.T, tg target, c *Client) {
		// Connect outside the deadline, so a slow connect under -race cannot
		// eat the budget.
		cmd, err := c.Start(context.Background(), tg.tid, tg.srv, "sleep 30", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		st, err := cmd.Wait(ctx)
		if !errors.Is(err, CodeTimeout) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v", err)
		}
		if st.Outcome != OutcomeSignaled || st.Signal != "TERM" || !st.TermSent || st.KillSent || st.Stopped != StopDeadline || st.MayStillRun {
			t.Fatalf("status %+v", st)
		}
	}},
	{"raging/a command that traps TERM is ended by KILL after the grace", func(t *testing.T, tg target, c *Client) {
		out := newNotifyBuffer()
		cmd, err := c.Start(context.Background(), tg.tid, tg.srv, `trap "" TERM; echo ready; sleep 30`, out, nil)
		if err != nil {
			t.Fatal(err)
		}
		out.waitLine(t, 10*time.Second)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		began := time.Now()
		st, err := cmd.Wait(ctx)
		if !errors.Is(err, context.Canceled) || CodeOf(err) != CodeInternal {
			t.Fatalf("err = %v", err)
		}
		if st.Outcome != OutcomeSignaled || st.Signal != "KILL" || !st.TermSent || !st.KillSent || st.Stopped != StopCancel {
			t.Fatalf("status %+v", st)
		}
		if el := time.Since(began); el < c.Options().KillGrace {
			t.Fatalf("KILL came after %v, before the grace of %v", el, c.Options().KillGrace)
		}
	}},
	{"happy/TERM reaches a forked child in the process group", func(t *testing.T, tg target, c *Client) {
		cmd, pid := startWithPid(t, tg, c, "sleep 30 & echo $!; wait")
		if !alive(pid) {
			t.Fatalf("child %d is not running", pid)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		st, _ := cmd.Wait(ctx)
		if st.Outcome != OutcomeSignaled || st.Signal != "TERM" || st.KillSent {
			t.Fatalf("status %+v", st)
		}
		if !leakcheck.Poll(5*time.Second, func() bool { return !alive(pid) }) {
			t.Fatalf("child %d survived TERM to its process group", pid)
		}
	}},
	{"corner/a child that left the process group survives TERM and KILL", func(t *testing.T, tg target, c *Client) {
		if _, err := exec.LookPath("setsid"); err != nil {
			if realsshd.Required() {
				t.Fatalf("no setsid binary, so the F10 survivor case cannot be observed")
			}
			t.Logf("no setsid binary here; the survivor case was not observed")
			return
		}
		cmd, pid := startWithPid(t, tg, c, "setsid sleep 30 </dev/null >/dev/null 2>&1 & echo $!; sleep 30")
		// $! is printed before the child has called setsid(); stop only
		// once it leads its own process group.
		if !leakcheck.Poll(5*time.Second, func() bool { return pgid(pid) == pid }) {
			t.Fatalf("pid %d never left the process group (pgid %d)", pid, pgid(pid))
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		st, _ := cmd.Wait(ctx)
		if st.Outcome != OutcomeSignaled || st.Signal != "TERM" {
			t.Fatalf("status %+v", st)
		}
		survived := alive(pid)
		t.Logf("F10 observed (%s): a child that called setsid survived the TERM sent to the session's process group: %v", tg.banner, survived)
		if !survived {
			t.Fatalf("the child %d that left the group did not survive; F10 says it does", pid)
		}
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		if !leakcheck.Poll(5*time.Second, func() bool { return !alive(pid) }) {
			t.Fatalf("could not clean up pid %d", pid)
		}
	}},
	{"corner/a command ended by TERM never reports may still run", func(t *testing.T, tg target, _ *Client) {
		// A server may close the channel before it answers the signal
		// request (sshtest's ExitBeforeSignalReply forces that order); on
		// either server, a command ended by the TERM it was sent is
		// signaled TERM and never "may still run", by Signal or by stop.
		// Bounds of seconds, so a slow reply on a loaded runner is not
		// taken for a silent server (that case has its own tests).
		c := New(Options{ConnectTimeout: 5 * time.Second, KillGrace: 3 * time.Second, KillWait: 5 * time.Second})
		for i := range 10 {
			cmd, _ := startReady(t, tg, c)
			if err := cmd.Signal("TERM"); err != nil {
				t.Fatalf("round %d: Signal(TERM) = %v", i, err)
			}
			st, err := waitBounded(t, cmd)
			if err != nil || st.Outcome != OutcomeSignaled || st.Signal != "TERM" || st.MayStillRun {
				t.Fatalf("round %d: Signal: status %+v err %v", i, st, err)
			}
			cmd2, _ := startReady(t, tg, c)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			st, _ = cmd2.Wait(ctx)
			if st.Outcome != OutcomeSignaled || st.Signal != "TERM" || !st.TermSent || st.KillSent || st.MayStillRun {
				t.Fatalf("round %d: stop: status %+v", i, st)
			}
		}
	}},
	{"happy/the signal request is accepted and delivered", func(t *testing.T, tg target, c *Client) {
		cmd, pid := startReady(t, tg, c)
		if err := cmd.Signal("TERM"); err != nil {
			t.Fatalf("Signal(TERM) = %v", err)
		}
		t.Logf("F10 observed (%s): signal request TERM with want-reply was ACCEPTED", tg.banner)
		st, err := waitBounded(t, cmd)
		if err != nil || st.Outcome != OutcomeSignaled || st.Signal != "TERM" || st.MayStillRun {
			t.Fatalf("status %+v err %v", st, err)
		}
		if alive(pid) {
			t.Fatalf("pid %d still alive", pid)
		}
		for _, name := range []string{"INT", "HUP", "QUIT", "USR1", "USR2", "KILL"} {
			cmd2, _ := startReady(t, tg, c)
			if err := cmd2.Signal(name); err != nil {
				t.Fatalf("Signal(%s) = %v", name, err)
			}
			st, _ := waitBounded(t, cmd2)
			if st.Outcome != OutcomeSignaled || st.Signal != name {
				t.Fatalf("%s: status %+v", name, st)
			}
		}
		t.Logf("F10 observed (%s): INT HUP QUIT USR1 USR2 KILL accepted and delivered", tg.banner)
	}},
	{"corner/closing the session does not stop the command", func(t *testing.T, tg target, c *Client) {
		cmd, pid := startReady(t, tg, c)
		if err := cmd.Close(); err != nil {
			t.Fatal(err)
		}
		st, err := waitBounded(t, cmd)
		if err != nil || !st.MayStillRun || st.Outcome != OutcomeNoStatus {
			t.Fatalf("status %+v err %v", st, err)
		}
		// Observe for a second whether anything on the server side ends
		// the command once its connection is gone.
		died := leakcheck.Poll(time.Second, func() bool { return !alive(pid) })
		t.Logf("F10 observed (%s): after the client closed the connection the command was still running: %v", tg.banner, !died)
		if died {
			t.Fatalf("pid %d ended when the session closed; F10 says closing does not stop a command", pid)
		}
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
	}},
	{"corner/a background child that holds stdout keeps the session open", func(t *testing.T, tg target, c *Client) {
		// The shell exits at once; its child keeps stdout open, so the
		// server reports the exit but does not close the channel. Pinned
		// on both servers for the job engine (spec 5.5 callout).
		cmd, pid := startWithPid(t, tg, c, "sleep 30 & echo $!")
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		st, err := cmd.Wait(ctx)
		t.Logf("A8 observed (%s): outcome %s code %d, TERM accepted %v, KILL accepted %v, may still run %v, child alive %v, err %v", tg.banner, st.Outcome, st.Code, st.TermSent, st.KillSent, st.MayStillRun, alive(pid), CodeOf(err))
		if !errors.Is(err, CodeTimeout) || st.Outcome != OutcomeExited || st.Code != 0 || st.TermSent || st.KillSent || !st.MayStillRun || st.Stopped != StopDeadline {
			t.Fatalf("status %+v err %v", st, err)
		}
	}},
	{"sad/a signal after the command ended is not running", func(t *testing.T, tg target, c *Client) {
		cmd, err := c.Start(context.Background(), tg.tid, tg.srv, "true", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := waitBounded(t, cmd); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Signal("TERM"); !errors.Is(err, ErrNotRunning) {
			t.Fatalf("err = %v", err)
		}
	}},
	{"happy/download with offset and limit", func(t *testing.T, tg target, c *Client) {
		content := make([]byte, 1000)
		for i := range content {
			content[i] = byte('a' + i%26)
		}
		p := filepath.Join(tg.dir, "dl.txt")
		if err := os.WriteFile(p, content, 0o600); err != nil {
			t.Fatal(err)
		}
		cases := []struct {
			name     string
			off, max int64
			from, to int
			eof      bool
		}{
			{"middle", 10, 100, 10, 110, false},
			{"tail", 990, 100, 990, 1000, true},
			{"exact end", 900, 100, 900, 1000, true},
			{"whole", 0, 5000, 0, 1000, true},
			{"past the end", 2000, 10, 0, 0, true},
			{"max zero", 0, 0, 0, 0, false},
		}
		for _, cs := range cases {
			data, size, eof, err := c.Download(context.Background(), tg.tid, tg.srv, p, cs.off, cs.max)
			if err != nil || size != 1000 || eof != cs.eof || !bytes.Equal(data, content[cs.from:cs.to]) {
				t.Fatalf("%s: len %d size %d eof %v err %v", cs.name, len(data), size, eof, err)
			}
		}
	}},
	{"raging/download of several megabytes is bounded by max", func(t *testing.T, tg target, c *Client) {
		content := bytes.Repeat([]byte("0123456789abcdef"), 250_000)
		p := filepath.Join(tg.dir, "big.bin")
		if err := os.WriteFile(p, content, 0o600); err != nil {
			t.Fatal(err)
		}
		data, size, eof, err := c.Download(context.Background(), tg.tid, tg.srv, p, 0, int64(len(content)))
		if err != nil || !eof || size != int64(len(content)) || !bytes.Equal(data, content) {
			t.Fatalf("len %d size %d eof %v err %v", len(data), size, eof, err)
		}
		data, _, eof, err = c.Download(context.Background(), tg.tid, tg.srv, p, 1, 3)
		if err != nil || eof || string(data) != "123" {
			t.Fatalf("data %q eof %v err %v", data, eof, err)
		}
	}},
	{"sad/download of a missing file or a directory", func(t *testing.T, tg target, c *Client) {
		_, _, _, err := c.Download(context.Background(), tg.tid, tg.srv, filepath.Join(tg.dir, "missing"), 0, 10)
		if !errors.Is(err, fs.ErrNotExist) || CodeOf(err) != CodeInternal || strings.Contains(err.Error(), tg.dir) {
			t.Fatalf("missing: err = %v", err)
		}
		_, _, _, err = c.Download(context.Background(), tg.tid, tg.srv, tg.dir, 0, 10)
		if !errors.Is(err, ErrNotRegular) {
			t.Fatalf("directory: err = %v", err)
		}
	}},
	{"raging/download of a FIFO or a device is refused before it is opened", func(t *testing.T, tg target, c *Client) {
		fifo := filepath.Join(tg.dir, "fifo")
		if err := syscall.Mkfifo(fifo, 0o600); err != nil {
			t.Fatal(err)
		}
		// Bounded: opening a FIFO blocks until a writer appears.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, p := range []string{fifo, "/dev/null"} {
			began := time.Now()
			_, _, _, err := c.Download(ctx, tg.tid, tg.srv, p, 0, 10)
			if !errors.Is(err, ErrNotRegular) || time.Since(began) > 5*time.Second {
				t.Fatalf("%s: err = %v after %v", p, err, time.Since(began))
			}
		}
		// Nothing has the FIFO open: a non-blocking open for writing fails
		// with ENXIO when there is no reader.
		if fd, err := syscall.Open(fifo, syscall.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = syscall.Close(fd)
			t.Fatal("something still has the FIFO open for reading")
		}
	}},
	{"edge/download through a symbolic link to a regular file", func(t *testing.T, tg target, c *Client) {
		p := filepath.Join(tg.dir, "real.txt")
		if err := os.WriteFile(p, []byte("linked"), 0o600); err != nil {
			t.Fatal(err)
		}
		l := filepath.Join(tg.dir, "link.txt")
		if err := os.Symlink(p, l); err != nil {
			t.Fatal(err)
		}
		data, size, eof, err := c.Download(context.Background(), tg.tid, tg.srv, l, 0, 100)
		if err != nil || string(data) != "linked" || size != 6 || !eof {
			t.Fatalf("data %q size %d eof %v err %v", data, size, eof, err)
		}
	}},
	{"edge/download of a file that reports size zero reads to EOF", func(t *testing.T, tg target, c *Client) {
		want, err := os.ReadFile("/proc/version")
		if err != nil || len(want) == 0 {
			t.Fatalf("/proc/version: %v", err)
		}
		data, size, eof, err := c.Download(context.Background(), tg.tid, tg.srv, "/proc/version", 0, 1<<16)
		if err != nil || !bytes.Equal(data, want) || !eof || size != int64(len(want)) {
			t.Fatalf("len %d size %d eof %v err %v", len(data), size, eof, err)
		}
		data, size, eof, err = c.Download(context.Background(), tg.tid, tg.srv, "/proc/version", 2, 3)
		if err != nil || !bytes.Equal(data, want[2:5]) || eof || size != 5 {
			t.Fatalf("partial: data %q size %d eof %v err %v", data, size, eof, err)
		}
	}},
	{"happy/upload creates with the mode and refuses to overwrite", func(t *testing.T, tg target, c *Client) {
		srv := tg.full()
		p := filepath.Join(tg.dir, "up.txt")
		if err := c.Upload(context.Background(), tg.tid, srv, p, []byte("first"), 0o640, false); err != nil {
			t.Fatal(err)
		}
		checkFile(t, p, "first", 0o640)
		err := c.Upload(context.Background(), tg.tid, srv, p, []byte("second"), 0o600, false)
		if !errors.Is(err, fs.ErrExist) {
			t.Fatalf("overwrite=false on an existing file: err = %v", err)
		}
		checkFile(t, p, "first", 0o640)
		if err = c.Upload(context.Background(), tg.tid, srv, p, []byte("second"), 0o600, true); err != nil {
			t.Fatal(err)
		}
		checkFile(t, p, "second", 0o600)
		empty := filepath.Join(tg.dir, "empty")
		if err = c.Upload(context.Background(), tg.tid, srv, empty, nil, 0o644, true); err != nil {
			t.Fatal(err)
		}
		checkFile(t, empty, "", 0o644)
		entries, err := os.ReadDir(tg.dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if strings.Contains(e.Name(), ".yasshm-") {
				t.Fatalf("temporary file left behind: %s", e.Name())
			}
		}
	}},
	{"sad/upload into a missing directory", func(t *testing.T, tg target, c *Client) {
		srv := tg.full()
		err := c.Upload(context.Background(), tg.tid, srv, filepath.Join(tg.dir, "no", "such"), []byte("x"), 0o600, false)
		if err == nil || CodeOf(err) != CodeInternal {
			t.Fatalf("err = %v", err)
		}
	}},
	{"edge/upload onto a symbolic link replaces the link, never its target", func(t *testing.T, tg target, c *Client) {
		srv := tg.full()
		target := filepath.Join(tg.dir, "target.txt")
		if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
			t.Fatal(err)
		}
		l := filepath.Join(tg.dir, "link.txt")
		if err := os.Symlink(target, l); err != nil {
			t.Fatal(err)
		}
		if err := c.Upload(context.Background(), tg.tid, srv, l, []byte("new"), 0o600, false); !errors.Is(err, fs.ErrExist) {
			t.Fatalf("overwrite=false: err = %v", err)
		}
		if err := c.Upload(context.Background(), tg.tid, srv, l, []byte("new"), 0o600, true); err != nil {
			t.Fatal(err)
		}
		checkFile(t, target, "target", 0o600)
		fi, err := os.Lstat(l)
		if err != nil || !fi.Mode().IsRegular() {
			t.Fatalf("the link was not replaced by a file: %v %v", fi, err)
		}
		checkFile(t, l, "new", 0o600)
	}},
	{"sad/upload to a server that grants look is refused before connecting", func(t *testing.T, tg target, c *Client) {
		p := filepath.Join(tg.dir, "denied")
		if err := c.Upload(context.Background(), tg.tid, tg.srv, p, []byte("x"), 0o600, false); !errors.Is(err, ErrAccessLevel) {
			t.Fatalf("err = %v", err)
		}
		if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("the file exists: %v", err)
		}
	}},
	{"sad/host key mismatch", func(t *testing.T, tg target, c *Client) {
		srv := *tg.srv
		srv.HostKey = otherHostKey(t)
		_, err := c.Exec(context.Background(), tg.tid, &srv, "true", nil, nil)
		var e *Error
		if !errors.Is(err, CodeHostKeyMismatch) || !errors.As(err, &e) || e.Fingerprint() != ssh.FingerprintSHA256(tg.hostKey) {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(err.Error(), e.Fingerprint()) {
			t.Fatalf("message %q", err.Error())
		}
		noHostIn(t, &srv, err.Error())
	}},
	{"sad/a host key of another type is a mismatch", func(t *testing.T, tg target, c *Client) {
		ek, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		pk, err := ssh.NewPublicKey(&ek.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		srv := *tg.srv
		srv.HostKey = sshtest.KeyLine(pk)
		_, err = c.Exec(context.Background(), tg.tid, &srv, "true", nil, nil)
		if !errors.Is(err, CodeHostKeyMismatch) {
			t.Fatalf("err = %v", err)
		}
		noHostIn(t, &srv, err.Error())
	}},
	{"happy/known_hosts pinning", func(t *testing.T, tg target, c *Client) {
		kh := filepath.Join(t.TempDir(), "known_hosts")
		hostPat := "[" + tg.srv.Host + "]:" + strconv.Itoa(tg.srv.Port)
		other := otherHostKey(t)
		text := hostPat + " " + other + "\n" + hostPat + " " + sshtest.KeyLine(tg.hostKey) + "\n"
		if err := os.WriteFile(kh, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		srv := *tg.srv
		srv.HostKey, srv.KnownHostsFile = "", kh
		if _, err := c.Exec(context.Background(), tg.tid, &srv, "true", nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(kh, []byte(text+"@revoked * "+sshtest.KeyLine(tg.hostKey)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := c.Exec(context.Background(), tg.tid, &srv, "true", nil, nil)
		if !errors.Is(err, CodeHostKeyMismatch) {
			t.Fatalf("revoked key: err = %v", err)
		}
		noHostIn(t, &srv, err.Error())
	}},
	{"sad/refused key", func(t *testing.T, tg target, c *Client) {
		srv := *tg.srv
		srv.Identity.PrivateKeyFile = writeKeyFile(t)
		_, err := c.Exec(context.Background(), tg.tid, &srv, "true", nil, nil)
		if !errors.Is(err, CodeAuthFailed) {
			t.Fatalf("err = %v", err)
		}
		noHostIn(t, &srv, err.Error())
	}},
	{"sad/unreachable port", func(t *testing.T, tg target, c *Client) {
		srv := *tg.srv
		srv.Port = loopback.RefusingPort(t) // reserved for the test, refuses every connection
		_, err := c.Exec(context.Background(), tg.tid, &srv, "true", nil, nil)
		if !errors.Is(err, CodeUnreachable) {
			t.Fatalf("err = %v", err)
		}
		noHostIn(t, &srv, err.Error())
	}},
	{"happy/probe", func(t *testing.T, tg target, c *Client) {
		f, err := c.Probe(context.Background(), tg.tid, tg.srv)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(f.Kernel, "Linux ") || f.Arch == "" || f.User != tg.runsAs || f.Uptime <= 0 {
			t.Fatalf("facts %+v", f)
		}
	}},
}

func checkFile(t *testing.T, p, content string, mode fs.FileMode) {
	t.Helper()
	b, err := os.ReadFile(p) //nolint:gosec // G304: the test reads a file it wrote under its temp directory
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != content || fi.Mode().Perm() != mode {
		t.Fatalf("%s: content %q mode %04o, want %q %04o", filepath.Base(p), b, fi.Mode().Perm(), content, mode)
	}
}

func runContract(t *testing.T, mk func(t *testing.T) target) {
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			leakcheck.Check(t)
			tg := mk(t)
			// Registered after the server, so it runs before the server's
			// own cleanup (which kills survivors) and after the scenario's.
			checkNoLeftovers(t, &tg)
			sc.run(t, tg, testClient())
			waitNoConns(t, tg)
		})
	}
}

// TestContractSSHTest runs the contract suite against the in-process server.
func TestContractSSHTest(t *testing.T) {
	runContract(t, func(t *testing.T) target { return newSSHTest(t, sshtest.Options{}) })
}

// TestRealSSHDContract runs the contract suite against a real OpenSSH sshd.
// Without sshd it logs one line and returns, unless YASSHM_REQUIRE_SSHD=1.
func TestRealSSHDContract(t *testing.T) {
	if !realsshd.Present(t) {
		return
	}
	runContract(t, newReal)
}
