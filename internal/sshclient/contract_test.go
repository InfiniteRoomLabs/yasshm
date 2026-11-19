package sshclient

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
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
	var err error
	st, err = c.Exec(context.Background(), tg.tid, tg.srv, command, &out, &errb)
	if err != nil {
		t.Fatalf("Exec(%q): %v", command, err)
	}
	return st, out.String(), errb.String()
}

// startWithPid starts command, which must print one pid as its first line,
// and returns the handle, the pid and a cancel for the Wait context.
func startWithPid(t *testing.T, tg target, c *Client, command string) (*Command, int, *notifyBuffer) {
	t.Helper()
	out := newNotifyBuffer()
	cmd, err := c.Start(context.Background(), tg.tid, tg.srv, command, out, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Close() })
	pid, err := strconv.Atoi(strings.TrimSpace(out.waitLine(t, 10*time.Second)))
	if err != nil || pid <= 1 {
		t.Fatalf("no pid in %q", out.String())
	}
	t.Cleanup(func() {
		if alive(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return cmd, pid, out
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
		if poll(500*time.Millisecond, func() bool { _, serr := os.Stat(marker); return serr == nil }) {
			close(w.release)
			_ = cmd.Close()
			t.Fatal("the command finished while the writer was blocked: output was buffered without bound")
		}
		close(w.release)
		st, err := cmd.Wait(context.Background())
		if err != nil || st.Code != 0 || w.n != 16_000_000 {
			t.Fatalf("status %+v err %v bytes %d", st, err, w.n)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatal(err)
		}
	}},
	{"happy/a deadline sends TERM and the error is timeout", func(t *testing.T, tg target, c *Client) {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		st, err := c.Exec(ctx, tg.tid, tg.srv, "sleep 300", nil, nil)
		if !errors.Is(err, CodeTimeout) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v", err)
		}
		if st.Outcome != OutcomeSignaled || st.Signal != "TERM" || !st.TermSent || st.KillSent || st.Stopped != StopDeadline || st.MayStillRun {
			t.Fatalf("status %+v", st)
		}
	}},
	{"raging/a command that traps TERM is ended by KILL after the grace", func(t *testing.T, tg target, c *Client) {
		out := newNotifyBuffer()
		cmd, err := c.Start(context.Background(), tg.tid, tg.srv, `trap "" TERM; echo ready; sleep 300`, out, nil)
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
		cmd, pid, _ := startWithPid(t, tg, c, "sleep 300 & echo $!; wait")
		if !alive(pid) {
			t.Fatalf("child %d is not running", pid)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		st, _ := cmd.Wait(ctx)
		if st.Outcome != OutcomeSignaled || st.Signal != "TERM" || st.KillSent {
			t.Fatalf("status %+v", st)
		}
		if !poll(5*time.Second, func() bool { return !alive(pid) }) {
			t.Fatalf("child %d survived TERM to its process group", pid)
		}
	}},
	{"corner/a child that left the process group survives TERM and KILL", func(t *testing.T, tg target, c *Client) {
		if _, err := exec.LookPath("setsid"); err != nil {
			t.Logf("no setsid binary here; the survivor case was not observed")
			return
		}
		cmd, pid, _ := startWithPid(t, tg, c, "setsid sleep 300 </dev/null >/dev/null 2>&1 & echo $!; sleep 300")
		// $! is printed before the child has called setsid(); stop only
		// once it leads its own process group.
		if !poll(5*time.Second, func() bool { return pgid(pid) == pid }) {
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
		if !poll(5*time.Second, func() bool { return !alive(pid) }) {
			t.Fatalf("could not clean up pid %d", pid)
		}
	}},
	{"happy/the signal request is accepted and delivered", func(t *testing.T, tg target, c *Client) {
		cmd, pid, _ := startWithPid(t, tg, c, "echo $$; exec sleep 300")
		if err := cmd.Signal("TERM"); err != nil {
			t.Fatalf("Signal(TERM) = %v", err)
		}
		t.Logf("F10 observed (%s): signal request TERM with want-reply was ACCEPTED", tg.banner)
		st, err := cmd.Wait(context.Background())
		if err != nil || st.Outcome != OutcomeSignaled || st.Signal != "TERM" || st.MayStillRun {
			t.Fatalf("status %+v err %v", st, err)
		}
		if alive(pid) {
			t.Fatalf("pid %d still alive", pid)
		}
		for _, name := range []string{"INT", "HUP", "QUIT", "USR1", "USR2", "KILL"} {
			cmd2, _, _ := startWithPid(t, tg, c, "echo $$; exec sleep 300")
			if err := cmd2.Signal(name); err != nil {
				t.Fatalf("Signal(%s) = %v", name, err)
			}
			st, _ := cmd2.Wait(context.Background())
			if st.Outcome != OutcomeSignaled || st.Signal != name {
				t.Fatalf("%s: status %+v", name, st)
			}
		}
		t.Logf("F10 observed (%s): INT HUP QUIT USR1 USR2 KILL accepted and delivered", tg.banner)
	}},
	{"sad/a signal after the command ended is not running", func(t *testing.T, tg target, c *Client) {
		cmd, err := c.Start(context.Background(), tg.tid, tg.srv, "true", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cmd.Wait(context.Background()); err != nil {
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
	{"happy/upload creates with the mode and refuses to overwrite", func(t *testing.T, tg target, c *Client) {
		p := filepath.Join(tg.dir, "up.txt")
		if err := c.Upload(context.Background(), tg.tid, tg.srv, p, []byte("first"), 0o640, false); err != nil {
			t.Fatal(err)
		}
		checkFile(t, p, "first", 0o640)
		err := c.Upload(context.Background(), tg.tid, tg.srv, p, []byte("second"), 0o600, false)
		if !errors.Is(err, fs.ErrExist) {
			t.Fatalf("overwrite=false on an existing file: err = %v", err)
		}
		checkFile(t, p, "first", 0o640)
		if err = c.Upload(context.Background(), tg.tid, tg.srv, p, []byte("second"), 0o600, true); err != nil {
			t.Fatal(err)
		}
		checkFile(t, p, "second", 0o600)
		empty := filepath.Join(tg.dir, "empty")
		if err = c.Upload(context.Background(), tg.tid, tg.srv, empty, nil, 0o644, true); err != nil {
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
		err := c.Upload(context.Background(), tg.tid, tg.srv, filepath.Join(tg.dir, "no", "such"), []byte("x"), 0o600, false)
		if err == nil || CodeOf(err) != CodeInternal {
			t.Fatalf("err = %v", err)
		}
	}},
	{"sad/host key mismatch", func(t *testing.T, tg target, c *Client) {
		srv := *tg.srv
		srv.HostKey, _ = otherHostKey(t)
		_, err := c.Exec(context.Background(), tg.tid, &srv, "true", nil, nil)
		var e *Error
		if !errors.Is(err, CodeHostKeyMismatch) || !errors.As(err, &e) || e.Fingerprint() != ssh.FingerprintSHA256(tg.hostKey) {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(err.Error(), e.Fingerprint()) || strings.Contains(err.Error(), tg.srv.Host) {
			t.Fatalf("message %q", err.Error())
		}
	}},
	{"happy/known_hosts pinning", func(t *testing.T, tg target, c *Client) {
		kh := filepath.Join(t.TempDir(), "known_hosts")
		hostPat := "[" + tg.srv.Host + "]:" + strconv.Itoa(tg.srv.Port)
		other, _ := otherHostKey(t)
		text := hostPat + " " + other + "\n" + hostPat + " " + keyLine(tg.hostKey) + "\n"
		if err := os.WriteFile(kh, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		srv := *tg.srv
		srv.HostKey, srv.KnownHostsFile = "", kh
		if _, err := c.Exec(context.Background(), tg.tid, &srv, "true", nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(kh, []byte(text+"@revoked * "+keyLine(tg.hostKey)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Exec(context.Background(), tg.tid, &srv, "true", nil, nil); !errors.Is(err, CodeHostKeyMismatch) {
			t.Fatalf("revoked key: err = %v", err)
		}
	}},
	{"sad/refused key", func(t *testing.T, tg target, c *Client) {
		srv := *tg.srv
		srv.Identity.PrivateKeyFile = writeKeyFile(t)
		_, err := c.Exec(context.Background(), tg.tid, &srv, "true", nil, nil)
		if !errors.Is(err, CodeAuthFailed) {
			t.Fatalf("err = %v", err)
		}
	}},
	{"sad/unreachable port", func(t *testing.T, tg target, c *Client) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		if cerr := l.Close(); cerr != nil {
			t.Fatal(cerr)
		}
		srv := *tg.srv
		srv.Port = port
		_, err = c.Exec(context.Background(), tg.tid, &srv, "true", nil, nil)
		if !errors.Is(err, CodeUnreachable) || strings.Contains(err.Error(), "127.0.0.1") {
			t.Fatalf("err = %v", err)
		}
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
			checkLeaks(t)
			tg := mk(t)
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

var _ io.Writer = (*countWriter)(nil)
