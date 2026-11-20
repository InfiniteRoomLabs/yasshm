package sshtest

import (
	"bytes"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func dial(t *testing.T, s *Server) *ssh.Client {
	t.Helper()
	b, err := os.ReadFile(s.ClientKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	sg, err := ssh.ParsePrivateKey(b)
	if err != nil {
		t.Fatal(err)
	}
	c, err := ssh.Dial("tcp", s.Addr, &ssh.ClientConfig{
		User:            s.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(sg)},
		HostKeyCallback: ssh.FixedHostKey(s.HostKey),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// serverGoroutines returns the stacks of goroutines running this
// package's server code.
func serverGoroutines() []string {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	var out []string
	for _, g := range strings.Split(string(buf[:n]), "\n\n") {
		if strings.Contains(g, "internal/testkit/sshtest.(*Server)") && !strings.Contains(g, "testing.tRunner") {
			out = append(out, g)
		}
	}
	return out
}

func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	i := bytes.LastIndexByte(b, ')')
	return i > 0 && i+2 < len(b) && b[i+2] != 'Z'
}

func TestExecReplyFirst(t *testing.T) {
	t.Run("edge/the exec reply comes before a fast command's exit and close", func(t *testing.T) {
		s := Start(t, Options{})
		c := dial(t, s)
		for i := range 300 {
			sess, err := c.NewSession()
			if err != nil {
				t.Fatal(err)
			}
			if err := sess.Run("true"); err != nil {
				t.Fatalf("run %d: %v", i, err)
			}
			_ = sess.Close()
		}
	})
}

func TestServer(t *testing.T) {
	t.Run("happy/exec, refused requests are recorded, Close kills the children", func(t *testing.T) {
		before := len(serverGoroutines())
		s := Start(t, Options{})
		c := dial(t, s)
		sess, err := c.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		if err := sess.RequestPty("xterm", 24, 80, ssh.TerminalModes{}); err == nil {
			t.Fatal("pty-req accepted")
		}
		if err := sess.Setenv("A", "B"); err == nil {
			t.Fatal("env accepted")
		}
		if ok, _, _ := c.SendRequest("tcpip-forward", true, nil); ok {
			t.Fatal("forwarding accepted")
		}
		if err := sess.Start("sleep 30"); err != nil {
			t.Fatal(err)
		}
		// The server registers the process before it answers exec, so
		// Start returning means it is counted.
		pids := s.Pids()
		if len(pids) != 1 || !alive(pids[0]) || s.Conns() != 1 {
			t.Fatalf("pids %v conns %d", pids, s.Conns())
		}
		got := strings.Join(s.Requests(), ",")
		for _, want := range []string{"pty-req", "env", "global:tcpip-forward", "exec"} {
			if !strings.Contains(got, want) {
				t.Fatalf("requests %s lack %s", got, want)
			}
		}
		s.Close()
		s.Close()
		_ = c.Close()
		if alive(pids[0]) || s.Running() != 0 || s.Conns() != 0 {
			t.Fatalf("after Close: pid alive %v running %d conns %d", alive(pids[0]), s.Running(), s.Conns())
		}
		// A goroutine past wg.Done may still be returning: poll briefly.
		g := serverGoroutines()
		for deadline := time.Now().Add(2 * time.Second); len(g) > before && time.Now().Before(deadline); g = serverGoroutines() {
			runtime.Gosched()
		}
		if len(g) > before {
			t.Fatalf("%d server goroutines survived Close:\n%s", len(g)-before, strings.Join(g, "\n\n"))
		}
	})
	t.Run("happy/exit-signal names", func(t *testing.T) {
		for name, sig := range signals {
			if signalName(sig) != name {
				t.Fatalf("%s -> %s", name, signalName(sig))
			}
		}
		if signalName(syscall.SIGPIPE) != "PIPE" || signalName(syscall.SIGALRM) != "ALRM" || signalName(syscall.SIGABRT) != "ABRT" || signalName(syscall.SIGSEGV) != "SEGV" {
			t.Fatal("extra names")
		}
		if !strings.HasPrefix(signalName(syscall.SIGWINCH), "SIG") {
			t.Fatal(signalName(syscall.SIGWINCH))
		}
	})
	t.Run("sad/a second exec and an unknown subsystem are refused", func(t *testing.T) {
		s := Start(t, Options{})
		c := dial(t, s)
		defer func() { _ = c.Close() }()
		sess, err := c.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		if err := sess.RequestSubsystem("nope"); err == nil {
			t.Fatal("unknown subsystem accepted")
		}
		if err := sess.Run("true"); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Dial("tcp", "127.0.0.1:1"); err == nil {
			t.Fatal("direct-tcpip accepted")
		}
		if !strings.Contains(strings.Join(s.Requests(), ","), "channel:direct-tcpip") {
			t.Fatalf("requests %v", s.Requests())
		}
	})
}
