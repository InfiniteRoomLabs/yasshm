package sshclient

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"

	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/sshtest"
)

func TestNewAndOptions(t *testing.T) {
	t.Run("edge/zero options take the defaults", func(t *testing.T) {
		o := New(Options{}).Options()
		if o.ConnectTimeout != DefaultConnectTimeout || o.KillGrace != DefaultKillGrace || o.KillWait != DefaultKillWait {
			t.Fatalf("options %+v", o)
		}
	})
	t.Run("happy/from limits", func(t *testing.T) {
		o := New(OptionsFromLimits(tenant.Limits{ConnectTimeoutSeconds: 7, KillGraceSeconds: 3})).Options()
		if o.ConnectTimeout != 7*time.Second || o.KillGrace != 3*time.Second || o.KillWait != DefaultKillWait {
			t.Fatalf("options %+v", o)
		}
	})
	t.Run("happy/signal names", func(t *testing.T) {
		for _, n := range []string{"TERM", "KILL", "INT", "HUP", "QUIT", "USR1", "USR2"} {
			if !ValidSignal(n) {
				t.Fatalf("%s refused", n)
			}
		}
		for _, n := range []string{"SIGTERM", "STOP", "term", "", "CONT"} {
			if ValidSignal(n) {
				t.Fatalf("%q accepted", n)
			}
		}
	})
	t.Run("happy/strings", func(t *testing.T) {
		if OutcomeExited.String() != "exited" || OutcomeSignaled.String() != "signaled" || OutcomeNoStatus.String() != "no_status" || Outcome(0).String() != "unknown" {
			t.Fatal("Outcome.String")
		}
		if StopNone.String() != "none" || StopDeadline.String() != "deadline" || StopCancel.String() != "cancel" {
			t.Fatal("StopReason.String")
		}
	})
}

func TestErrors(t *testing.T) {
	t.Run("happy/an error matches its code and sentinel", func(t *testing.T) {
		cause := errors.New("dial tcp 192.0.2.1:22: refused")
		e := newErr(CodeUnreachable, "srv-1", "exec", "the TCP connection failed", ErrNotRunning, cause)
		if !errors.Is(e, CodeUnreachable) || errors.Is(e, CodeTimeout) || !errors.Is(e, ErrNotRunning) || errors.Is(e, ErrSignalRefused) || !errors.Is(e, cause) {
			t.Fatal("Is")
		}
		if e.Code() != CodeUnreachable || e.Server() != "srv-1" || e.Fingerprint() != "" {
			t.Fatal("accessors")
		}
		if e.Error() != "sshclient: exec on server srv-1: ssh_unreachable: the TCP connection failed" {
			t.Fatalf("Error() = %q", e.Error())
		}
		if !strings.Contains(e.Detail(), "192.0.2.1") || strings.Contains(e.Error(), "192.0.2.1") {
			t.Fatalf("Detail() = %q", e.Detail())
		}
	})
	t.Run("edge/no message, no cause", func(t *testing.T) {
		e := newErr(CodeInternal, "s", "op", "", nil, nil)
		if e.Error() != "sshclient: op on server s: internal" || e.Detail() != e.Error() || errors.Is(e, ErrNotRunning) {
			t.Fatal(e.Error())
		}
	})
	t.Run("raging/auth-failure text before the host key was checked is not an auth failure", func(t *testing.T) {
		// A peer can put any text in a disconnect message during key
		// exchange; it counts only after our callback accepted its key.
		text := errors.New("ssh: handshake failed: ssh: unable to authenticate, attempted methods [none publickey]")
		if err := handshakeErr(context.Background(), "s", "exec", text, nil, false); !errors.Is(err, CodeUnreachable) {
			t.Fatalf("unverified peer: %v", err)
		}
		if err := handshakeErr(context.Background(), "s", "exec", text, nil, true); !errors.Is(err, CodeAuthFailed) {
			t.Fatalf("verified peer: %v", err)
		}
	})
	t.Run("happy/CodeOf", func(t *testing.T) {
		if CodeOf(nil) != "" || CodeOf(errors.New("x")) != CodeInternal || CodeOf(newErr(CodeTimeout, "", "", "", nil, nil)) != CodeTimeout {
			t.Fatal("CodeOf")
		}
		if CodeTimeout.Error() != "timeout" {
			t.Fatal("Code.Error")
		}
	})
}

func TestTenantChecks(t *testing.T) {
	checkLeaks(t)
	tg := newSSHTest(t, sshtest.Options{})
	c := testClient()
	ctx := context.Background()
	other := tenant.TenantID("t2")
	calls := map[string]func(tenant.TenantID, *tenant.Server) error{
		"exec": func(tid tenant.TenantID, s *tenant.Server) error {
			_, err := c.Exec(ctx, tid, s, "true", nil, nil)
			return err
		},
		"download": func(tid tenant.TenantID, s *tenant.Server) error {
			_, _, _, err := c.Download(ctx, tid, s, "/x", 0, 1)
			return err
		},
		"upload": func(tid tenant.TenantID, s *tenant.Server) error {
			return c.Upload(ctx, tid, s, "/x", nil, 0o600, false)
		},
		"probe": func(tid tenant.TenantID, s *tenant.Server) error {
			_, err := c.Probe(ctx, tid, s)
			return err
		},
	}
	for name, call := range calls {
		t.Run("raging/"+name+" refuses another tenant's server", func(t *testing.T) {
			if err := call(other, tg.srv); !errors.Is(err, ErrWrongTenant) || CodeOf(err) != CodeInternal {
				t.Fatalf("err = %v", err)
			}
		})
		t.Run("sad/"+name+" refuses an empty tenant", func(t *testing.T) {
			if err := call("", tg.srv); !errors.Is(err, ErrWrongTenant) {
				t.Fatalf("err = %v", err)
			}
		})
		t.Run("sad/"+name+" refuses a nil server", func(t *testing.T) {
			if err := call(testTenant, nil); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	t.Run("happy/nothing reached the server", func(t *testing.T) {
		if tg.st.Accepted() != 0 {
			t.Fatalf("%d connections accepted", tg.st.Accepted())
		}
	})
}

func TestConnectRules(t *testing.T) {
	ctx := context.Background()
	t.Run("happy/the client never asks for a PTY, env, agent or forwarding", func(t *testing.T) {
		checkLeaks(t)
		tg := newSSHTest(t, sshtest.Options{})
		c := testClient()
		if _, err := c.Exec(ctx, tg.tid, tg.srv, "true", nil, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Probe(ctx, tg.tid, tg.srv); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := c.Download(ctx, tg.tid, tg.srv, filepath.Join(tg.dir, "none"), 0, 1); !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
		waitNoConns(t, tg)
		for _, r := range tg.st.Requests() {
			if r != "exec" && r != "subsystem:sftp" {
				t.Fatalf("the client sent %q (all: %v)", r, tg.st.Requests())
			}
		}
		if tg.st.Accepted() != 3 {
			t.Fatalf("one connection per call: %d accepted", tg.st.Accepted())
		}
	})
	t.Run("sad/the server presents another host key", func(t *testing.T) {
		checkLeaks(t)
		tg := newSSHTest(t, sshtest.Options{WrongHostKey: true})
		_, err := testClient().Exec(ctx, tg.tid, tg.srv, "true", nil, nil)
		var e *Error
		if !errors.As(err, &e) || e.Code() != CodeHostKeyMismatch || !strings.HasPrefix(e.Fingerprint(), "SHA256:") {
			t.Fatalf("err = %v", err)
		}
		if len(tg.st.Requests()) != 0 {
			t.Fatalf("requests sent to an unpinned server: %v", tg.st.Requests())
		}
		noHostIn(t, tg.srv, err.Error())
		waitNoConns(t, tg)
	})
	t.Run("sad/the server refuses the key", func(t *testing.T) {
		checkLeaks(t)
		tg := newSSHTest(t, sshtest.Options{RefuseKey: true})
		if _, err := testClient().Exec(ctx, tg.tid, tg.srv, "true", nil, nil); !errors.Is(err, CodeAuthFailed) {
			t.Fatalf("err = %v", err)
		}
		waitNoConns(t, tg)
	})
	t.Run("raging/a slow banner hits the connect timeout", func(t *testing.T) {
		checkLeaks(t)
		tg := newSSHTest(t, sshtest.Options{SlowBanner: 10 * time.Second})
		c := New(Options{ConnectTimeout: 200 * time.Millisecond})
		began := time.Now()
		_, err := c.Exec(ctx, tg.tid, tg.srv, "true", nil, nil)
		if !errors.Is(err, CodeUnreachable) {
			t.Fatalf("err = %v", err)
		}
		if el := time.Since(began); el > 5*time.Second {
			t.Fatalf("connect took %v", el)
		}
	})
	t.Run("sad/the caller's deadline during the handshake is timeout", func(t *testing.T) {
		checkLeaks(t)
		tg := newSSHTest(t, sshtest.Options{SlowBanner: 10 * time.Second})
		dctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer cancel()
		_, err := testClient().Exec(dctx, tg.tid, tg.srv, "true", nil, nil)
		if !errors.Is(err, CodeTimeout) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("sad/an already cancelled context", func(t *testing.T) {
		checkLeaks(t)
		tg := newSSHTest(t, sshtest.Options{})
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		_, err := testClient().Exec(cctx, tg.tid, tg.srv, "true", nil, nil)
		if !errors.Is(err, context.Canceled) || tg.st.Accepted() != 0 {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("sad/a cancel during the handshake with a server that never speaks", func(t *testing.T) {
		checkLeaks(t)
		srv := silentServer(t)
		c := New(Options{ConnectTimeout: 10 * time.Second})
		cctx, cancel := context.WithCancel(ctx)
		time.AfterFunc(100*time.Millisecond, cancel)
		began := time.Now()
		_, err := c.Exec(cctx, testTenant, srv, "true", nil, nil)
		if !errors.Is(err, context.Canceled) || time.Since(began) > 5*time.Second {
			t.Fatalf("err = %v after %v", err, time.Since(began))
		}
		noHostIn(t, srv, err.Error())
		dctx, dcancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer dcancel()
		if _, err := c.Exec(dctx, testTenant, srv, "true", nil, nil); !errors.Is(err, CodeTimeout) {
			t.Fatalf("deadline: err = %v", err)
		}
		if _, err := New(Options{ConnectTimeout: 100 * time.Millisecond}).Exec(ctx, testTenant, srv, "true", nil, nil); !errors.Is(err, CodeUnreachable) {
			t.Fatalf("connect timeout: err = %v", err)
		}
	})
	t.Run("sad/a closed loopback port", func(t *testing.T) {
		srv := silentServer(t)
		srv.Port = closedPort(t)
		_, err := testClient().Exec(ctx, testTenant, srv, "true", nil, nil)
		if !errors.Is(err, CodeUnreachable) {
			t.Fatalf("err = %v", err)
		}
		noHostIn(t, srv, err.Error())
	})
}

// silentServer returns a server on a loopback listener that accepts TCP
// connections and never sends a byte.
func silentServer(t *testing.T) *tenant.Server {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var (
		mu    sync.Mutex
		conns []net.Conn
		wg    sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = l.Close()
		wg.Wait()
		mu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
	})
	return &tenant.Server{
		Tenant: testTenant, ID: "silent", Host: "127.0.0.1", Port: l.Addr().(*net.TCPAddr).Port,
		HostKey: otherHostKey(t), Access: tenant.AccessLook,
		Identity: tenant.Identity{User: "someone", PrivateKeyFile: writeKeyFile(t)},
	}
}

// closedPort returns a loopback port nothing listens on.
func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if cerr := l.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	return port
}

// failWriter fails every write.
type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("writer failed") }

func TestPinningAndKeys(t *testing.T) {
	ctx := context.Background()
	tg := newSSHTest(t, sshtest.Options{})
	c := testClient()
	cases := []struct {
		name string
		edit func(s *tenant.Server)
		code Code
		text string
	}{
		{"sad/no pinned key", func(s *tenant.Server) { s.HostKey = "" }, CodeHostKeyMismatch, "pins no host key"},
		{"sad/both a key and a file", func(s *tenant.Server) { s.KnownHostsFile = "/nonexistent" }, CodeInternal, "both"},
		{"raging/a bad pinned key", func(s *tenant.Server) { s.HostKey = "garbage" }, CodeHostKeyMismatch, "cannot be used"},
		{"sad/a missing known_hosts file", func(s *tenant.Server) { s.HostKey, s.KnownHostsFile = "", filepath.Join(t.TempDir(), "kh") }, CodeInternal, "cannot be read"},
		{"sad/a known_hosts file without the host", func(s *tenant.Server) {
			p := filepath.Join(t.TempDir(), "kh")
			if err := os.WriteFile(p, []byte("other.example.net "+otherHostKey(t)+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			s.HostKey, s.KnownHostsFile = "", p
		}, CodeHostKeyMismatch, "has no usable key for this server"},
		{"sad/a private key path that is a directory", func(s *tenant.Server) { s.Identity.PrivateKeyFile = t.TempDir() }, CodeInternal, "is not a regular file"},
		{"sad/a missing private key", func(s *tenant.Server) { s.Identity.PrivateKeyFile = filepath.Join(t.TempDir(), "nokey") }, CodeInternal, "cannot be read"},
		{"raging/a private key readable by others", func(s *tenant.Server) {
			p := writeKeyFile(t)
			if err := os.Chmod(p, 0o644); err != nil { //nolint:gosec // G302: the test needs a key that others can read
				t.Fatal(err)
			}
			s.Identity.PrivateKeyFile = p
		}, CodeInternal, "accessible by group or other"},
		{"raging/a private key that does not parse", func(s *tenant.Server) {
			p := filepath.Join(t.TempDir(), "bad")
			if err := os.WriteFile(p, []byte("not a key"), 0o600); err != nil {
				t.Fatal(err)
			}
			s.Identity.PrivateKeyFile = p
		}, CodeInternal, "does not parse"},
	}
	for _, cs := range cases {
		t.Run(cs.name, func(t *testing.T) {
			srv := *tg.srv
			cs.edit(&srv)
			_, err := c.Exec(ctx, tg.tid, &srv, "true", nil, nil)
			if CodeOf(err) != cs.code || !strings.Contains(err.Error(), cs.text) {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(err.Error(), "/") || strings.Contains(err.Error(), "0644") {
				t.Fatalf("a path or mode in the message: %q", err.Error())
			}
			noHostIn(t, &srv, err.Error())
		})
	}
	t.Run("edge/port zero means 22", func(t *testing.T) {
		srv := *tg.srv
		srv.Host, srv.Port = "127.0.0.1", 0
		// Nothing listens on 22 here, or something that is not this server
		// does: either way the call fails without reaching tg.
		before := tg.st.Accepted()
		if _, err := New(Options{ConnectTimeout: 500 * time.Millisecond}).Exec(ctx, tg.tid, &srv, "true", nil, nil); err == nil {
			t.Fatal("connected")
		}
		if tg.st.Accepted() != before {
			t.Fatal("reached the test server")
		}
	})
	if tg.st.Accepted() != 0 {
		t.Fatalf("the cases above must not reach the server: %d", tg.st.Accepted())
	}
}

func TestCommandFaults(t *testing.T) {
	ctx := context.Background()
	t.Run("sad/a refused signal closes the session and says it may still run", func(t *testing.T) {
		checkLeaks(t)
		tg := newSSHTest(t, sshtest.Options{RefuseSignal: true})
		c := testClient()
		cmd, err := c.Start(ctx, tg.tid, tg.srv, "sleep 300", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if cmd.Tenant() != testTenant || cmd.Server() != "srv-1" {
			t.Fatal("accessors")
		}
		err = cmd.Signal("TERM")
		if !errors.Is(err, ErrSignalRefused) || CodeOf(err) != CodeInternal {
			t.Fatalf("err = %v", err)
		}
		select {
		case <-cmd.Done():
		case <-time.After(10 * time.Second):
			t.Fatal("a refused signal did not close the session")
		}
		st, err := cmd.Wait(ctx)
		if err != nil || st.Outcome != OutcomeNoStatus || !st.MayStillRun {
			t.Fatalf("status %+v err %v", st, err)
		}
		if err := cmd.Signal("TERM"); !errors.Is(err, ErrNotRunning) {
			t.Fatalf("after close: %v", err)
		}
		waitNoConns(t, tg)
	})
	t.Run("raging/refused TERM and KILL end with a closed session that may still run", func(t *testing.T) {
		checkLeaks(t)
		tg := newSSHTest(t, sshtest.Options{RefuseSignal: true})
		c := New(Options{ConnectTimeout: 5 * time.Second, KillGrace: 2 * time.Second, KillWait: 2 * time.Second})
		cmd, err := c.Start(ctx, tg.tid, tg.srv, "sleep 300", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		dctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		began := time.Now()
		st, err := cmd.Wait(dctx)
		// Refused signals skip their waits: no grace, no kill wait.
		if el := time.Since(began); el > 1500*time.Millisecond {
			t.Fatalf("the stop took %v; a refused signal must not wait", el)
		}
		if !errors.Is(err, CodeTimeout) || st.Outcome != OutcomeNoStatus || !st.MayStillRun || st.TermSent || st.KillSent || st.Stopped != StopDeadline {
			t.Fatalf("status %+v err %v", st, err)
		}
		if tg.st.Running() != 1 {
			t.Fatalf("the remote command should still run: %d", tg.st.Running())
		}
		waitNoConns(t, tg)
	})
	for _, cs := range []struct {
		name string
		opts sshtest.Options
		code Code
	}{
		{"raging/a server that never answers signal", sshtest.Options{SilentSignal: true}, CodeTimeout},
		{"raging/a server that never answers exec", sshtest.Options{SilentExec: true}, CodeTimeout},
		{"raging/a server that never answers a channel open", sshtest.Options{SilentChannelOpen: true}, CodeTimeout},
	} {
		t.Run(cs.name, func(t *testing.T) {
			checkLeaks(t)
			tg := newSSHTest(t, cs.opts)
			c := New(Options{ConnectTimeout: 5 * time.Second, KillGrace: 300 * time.Millisecond, KillWait: 300 * time.Millisecond})
			o := c.Options()
			const deadline = time.Second
			dctx, cancel := context.WithTimeout(ctx, deadline)
			defer cancel()
			began := time.Now()
			st, err := c.Exec(dctx, tg.tid, tg.srv, "sleep 300", nil, nil)
			if el, limit := time.Since(began), deadline+o.KillGrace+o.KillWait+2*time.Second; el > limit {
				t.Fatalf("Exec returned after %v, past %v", el, limit)
			}
			if !errors.Is(err, cs.code) || !st.MayStillRun || st.Outcome != OutcomeNoStatus || st.Stopped != StopDeadline {
				t.Fatalf("status %+v err %v", st, err)
			}
			noHostIn(t, tg.srv, err.Error())
			waitNoConns(t, tg)
		})
	}
	t.Run("raging/a caller's Signal to a server that never answers", func(t *testing.T) {
		checkLeaks(t)
		tg := newSSHTest(t, sshtest.Options{SilentSignal: true})
		c := New(Options{ConnectTimeout: 5 * time.Second, KillGrace: 300 * time.Millisecond, KillWait: 300 * time.Millisecond})
		cmd, err := c.Start(ctx, tg.tid, tg.srv, "sleep 300", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		began := time.Now()
		err = cmd.Signal("TERM")
		if !errors.Is(err, CodeUnreachable) || time.Since(began) > 3*time.Second {
			t.Fatalf("err = %v after %v", err, time.Since(began))
		}
		wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		st, err := cmd.Wait(wctx)
		if err != nil || !st.MayStillRun {
			t.Fatalf("status %+v err %v", st, err)
		}
		waitNoConns(t, tg)
	})
	t.Run("edge/the connect timeout bounds a silent exec without a caller deadline", func(t *testing.T) {
		checkLeaks(t)
		tg := newSSHTest(t, sshtest.Options{SilentExec: true})
		c := New(Options{ConnectTimeout: 300 * time.Millisecond})
		st, err := c.Exec(ctx, tg.tid, tg.srv, "true", nil, nil)
		if !errors.Is(err, CodeUnreachable) || !st.MayStillRun || st.Stopped != StopNone {
			t.Fatalf("status %+v err %v", st, err)
		}
	})
	t.Run("sad/an output writer that fails after an exit 0", func(t *testing.T) {
		checkLeaks(t)
		tg := newSSHTest(t, sshtest.Options{})
		wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		st, err := testClient().Exec(wctx, tg.tid, tg.srv, "echo x", failWriter{}, nil)
		if st.Outcome != OutcomeExited || st.Code != 0 || st.MayStillRun || CodeOf(err) != CodeInternal || !strings.Contains(err.Error(), "output writer") {
			t.Fatalf("status %+v err %v", st, err)
		}
	})
	t.Run("raging/a signal name from the server that is not a name", func(t *testing.T) {
		for in, want := range map[string]string{"TERM": "TERM", "SIGKILL": "KILL", "RT+1": "RT+1", "": "UNKNOWN", "term": "UNKNOWN", "A\x1b[2J": "UNKNOWN", strings.Repeat("A", 17): "UNKNOWN"} {
			if got := cleanSignal(in); got != want {
				t.Fatalf("%q -> %q, want %q", in, got, want)
			}
		}
	})
	t.Run("edge/no exit status", func(t *testing.T) {
		checkLeaks(t)
		tg := newSSHTest(t, sshtest.Options{NoExitStatus: true})
		st, err := testClient().Exec(ctx, tg.tid, tg.srv, "true", nil, nil)
		if err != nil || st.Outcome != OutcomeNoStatus || !st.MayStillRun {
			t.Fatalf("status %+v err %v", st, err)
		}
		waitNoConns(t, tg)
	})
	t.Run("raging/the connection drops mid-stream", func(t *testing.T) {
		checkLeaks(t)
		tg := newSSHTest(t, sshtest.Options{DropMidStream: true})
		out := newNotifyBuffer()
		st, err := testClient().Exec(ctx, tg.tid, tg.srv, "echo first; sleep 300", out, nil)
		if !errors.Is(err, CodeUnreachable) || st.Outcome != OutcomeNoStatus || !st.MayStillRun {
			t.Fatalf("status %+v err %v", st, err)
		}
		if out.String() != "first\n" {
			t.Fatalf("output %q", out.String())
		}
	})
	t.Run("sad/an unknown signal name is refused locally", func(t *testing.T) {
		checkLeaks(t)
		tg := newSSHTest(t, sshtest.Options{})
		cmd, err := testClient().Start(ctx, tg.tid, tg.srv, "sleep 300", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range []string{"STOP", "SIGTERM", ""} {
			if err := cmd.Signal(n); !errors.Is(err, ErrUnknownSignal) {
				t.Fatalf("%q: %v", n, err)
			}
		}
		for _, r := range tg.st.Requests() {
			if strings.HasPrefix(r, "signal") {
				t.Fatalf("sent %s", r)
			}
		}
		if err := cmd.Close(); err != nil {
			t.Fatal(err)
		}
		st, _ := cmd.Wait(ctx)
		if !st.MayStillRun {
			t.Fatalf("closing does not stop a command: %+v", st)
		}
		waitNoConns(t, tg)
	})
	t.Run("corner/concurrent Signal and Wait", func(t *testing.T) {
		checkLeaks(t)
		tg := newSSHTest(t, sshtest.Options{})
		cmd, err := testClient().Start(ctx, tg.tid, tg.srv, "sleep 300", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		type res struct {
			st  ExitStatus
			err error
		}
		results := make(chan res, 3)
		for range 3 {
			go func() {
				st, err := cmd.Wait(ctx)
				results <- res{st, err}
			}()
		}
		if err := cmd.Signal("KILL"); err != nil {
			t.Fatal(err)
		}
		for range 3 {
			r := <-results
			if r.err != nil || r.st.Outcome != OutcomeSignaled || r.st.Signal != "KILL" {
				t.Fatalf("%+v", r)
			}
		}
		if err := cmd.Close(); err != nil {
			t.Fatal(err)
		}
		waitNoConns(t, tg)
	})
	t.Run("corner/two Waits with ending contexts run one stop sequence", func(t *testing.T) {
		checkLeaks(t)
		tg := newSSHTest(t, sshtest.Options{})
		cmd, err := testClient().Start(ctx, tg.tid, tg.srv, "sleep 300", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		done := make(chan ExitStatus, 2)
		for range 2 {
			go func() {
				st, _ := cmd.Wait(cctx)
				done <- st
			}()
		}
		a, b := <-done, <-done
		if a != b || a.Signal != "TERM" {
			t.Fatalf("%+v %+v", a, b)
		}
		n := 0
		for _, r := range tg.st.Requests() {
			if r == "signal:TERM" {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("%d TERM requests", n)
		}
	})
	t.Run("raging/the probe output is bounded", func(t *testing.T) {
		b := &limitedBuffer{max: 4}
		if n, err := b.Write([]byte("abcdef")); n != 6 || err != nil {
			t.Fatal(n, err)
		}
		_, _ = b.Write([]byte("gh"))
		if b.String() != "abcd" {
			t.Fatal(b.String())
		}
	})
}

func TestProbeParsing(t *testing.T) {
	t.Run("happy/all facts", func(t *testing.T) {
		f := parseFacts("kernel=Linux 6.8.0\narch=x86_64\nos=\"Ubuntu 24.04 LTS\"\nuptime=12.50 3.1\nuser=look\n")
		if f.Kernel != "Linux 6.8.0" || f.Arch != "x86_64" || f.OS != "Ubuntu 24.04 LTS" || f.User != "look" {
			t.Fatalf("%+v", f)
		}
		if f.Uptime != 0 {
			t.Fatalf("a malformed uptime parsed: %v", f.Uptime)
		}
	})
	t.Run("edge/uptime and missing values", func(t *testing.T) {
		f := parseFacts("uptime=12.5\r\nos=\nnoise\nother=1\n")
		if f.Uptime != 12500*time.Millisecond || f.OS != "" || f.Kernel != "" {
			t.Fatalf("%+v", f)
		}
	})
	t.Run("raging/hostile values", func(t *testing.T) {
		f := parseFacts("kernel=" + strings.Repeat("é", 300) + "\narch=a\x1b[31mb\xff\nuptime=-5\n")
		if len(f.Kernel) > maxFactBytes || !strings.HasPrefix(f.Kernel, "é") || f.Arch != "a[31mb�" || f.Uptime != 0 {
			t.Fatalf("%q %q %v", f.Kernel, f.Arch, f.Uptime)
		}
	})
	t.Run("sad/the probe fails when the command does not exit 0", func(t *testing.T) {
		checkLeaks(t)
		tg := newSSHTest(t, sshtest.Options{NoExitStatus: true})
		if _, err := testClient().Probe(context.Background(), tg.tid, tg.srv); CodeOf(err) != CodeInternal || !strings.Contains(err.Error(), "no_status") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("sad/the probe passes on a connect error", func(t *testing.T) {
		tg := newSSHTest(t, sshtest.Options{RefuseKey: true})
		if _, err := testClient().Probe(context.Background(), tg.tid, tg.srv); !errors.Is(err, CodeAuthFailed) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestSFTPFaults(t *testing.T) {
	ctx := context.Background()
	t.Run("sad/invalid arguments", func(t *testing.T) {
		tg := newSSHTest(t, sshtest.Options{})
		c := testClient()
		for _, a := range []struct {
			p        string
			off, max int64
		}{{"", 0, 1}, {"/x", -1, 1}, {"/x", 0, -1}} {
			if _, _, _, err := c.Download(ctx, tg.tid, tg.srv, a.p, a.off, a.max); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("%+v: %v", a, err)
			}
		}
		for _, m := range []fs.FileMode{0o1777, fs.ModeSetuid | 0o755, fs.ModeDir | 0o755} {
			if err := c.Upload(ctx, tg.tid, tg.full(), "/x", nil, m, true); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("%v: %v", m, err)
			}
		}
		if err := c.Upload(ctx, tg.tid, tg.full(), "", nil, 0o600, true); !errors.Is(err, ErrInvalidArgument) {
			t.Fatal(err)
		}
		if tg.st.Accepted() != 0 {
			t.Fatal("reached the server")
		}
	})
	t.Run("sad/connect errors", func(t *testing.T) {
		checkLeaks(t)
		tg := newSSHTest(t, sshtest.Options{RefuseKey: true})
		c := testClient()
		if _, _, _, err := c.Download(ctx, tg.tid, tg.srv, "/x", 0, 1); !errors.Is(err, CodeAuthFailed) {
			t.Fatal(err)
		}
		if err := c.Upload(ctx, tg.tid, tg.full(), "/x", nil, 0o600, true); !errors.Is(err, CodeAuthFailed) {
			t.Fatal(err)
		}
	})
	t.Run("sad/a context cancelled before the SFTP session", func(t *testing.T) {
		checkLeaks(t)
		tg := newSSHTest(t, sshtest.Options{})
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if _, _, _, err := testClient().Download(cctx, tg.tid, tg.srv, "/x", 0, 1); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
	t.Run("sad/the server refuses the sftp subsystem", func(t *testing.T) {
		checkLeaks(t)
		tg := newSSHTest(t, sshtest.Options{NoSFTP: true})
		_, _, _, err := testClient().Download(ctx, tg.tid, tg.srv, "/x", 0, 1)
		if CodeOf(err) != CodeInternal || !strings.Contains(err.Error(), "SFTP subsystem") {
			t.Fatalf("err = %v", err)
		}
		waitNoConns(t, tg)
	})
	t.Run("raging/another tenant with invalid arguments is still another tenant", func(t *testing.T) {
		tg := newSSHTest(t, sshtest.Options{})
		c := testClient()
		if _, _, _, err := c.Download(ctx, "t2", tg.srv, "", -1, -1); !errors.Is(err, ErrWrongTenant) {
			t.Fatalf("download: %v", err)
		}
		if err := c.Upload(ctx, "t2", tg.full(), "", nil, 0o7777, true); !errors.Is(err, ErrWrongTenant) {
			t.Fatalf("upload: %v", err)
		}
	})
	t.Run("corner/a context that ends mid-transfer", func(t *testing.T) {
		checkLeaks(t)
		tg := newSSHTest(t, sshtest.Options{})
		c := testClient()
		big := make([]byte, 64<<20)
		p := filepath.Join(tg.dir, "big")
		if err := os.WriteFile(p, big, 0o600); err != nil {
			t.Fatal(err)
		}
		// Connect first, then let the deadline fall inside the transfer:
		// the connect alone takes a few milliseconds.
		dctx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
		defer cancel()
		if _, _, _, err := c.Download(dctx, tg.tid, tg.srv, p, 0, int64(len(big))); !errors.Is(err, CodeTimeout) {
			t.Fatalf("download: err = %v", err)
		}
		uctx, ucancel := context.WithTimeout(ctx, 150*time.Millisecond)
		defer ucancel()
		if err := c.Upload(uctx, tg.tid, tg.full(), filepath.Join(tg.dir, "up"), big, 0o600, true); !errors.Is(err, CodeTimeout) {
			t.Fatalf("upload: err = %v", err)
		}
		waitNoConns(t, tg)
	})
	t.Run("corner/overwrite without posix-rename writes in place", func(t *testing.T) {
		checkLeaks(t)
		if err := sftp.SetSFTPExtensions("statvfs@openssh.com"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := sftp.SetSFTPExtensions("hardlink@openssh.com", "posix-rename@openssh.com", "statvfs@openssh.com"); err != nil {
				t.Fatal(err)
			}
		})
		tg := newSSHTest(t, sshtest.Options{})
		c := testClient()
		p := filepath.Join(tg.dir, "inplace")
		if err := os.WriteFile(p, []byte("old content"), 0o600); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if uerr := c.Upload(ctx, tg.tid, tg.full(), p, []byte("new"), 0o640, true); uerr != nil {
			t.Fatal(uerr)
		}
		checkFile(t, p, "new", 0o640)
		fi2, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(fi, fi2) {
			t.Fatal("the file was replaced, not written in place")
		}
		if err := c.Upload(ctx, tg.tid, tg.full(), filepath.Join(tg.dir, "nodir", "x"), []byte("x"), 0o600, true); err == nil {
			t.Fatal("wrote into a missing directory")
		}
	})
	t.Run("sad/posix-rename onto a directory fails and leaves no temp file", func(t *testing.T) {
		checkLeaks(t)
		tg := newSSHTest(t, sshtest.Options{})
		d := filepath.Join(tg.dir, "adir")
		if err := os.MkdirAll(filepath.Join(d, "child"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := testClient().Upload(ctx, tg.tid, tg.full(), d, []byte("x"), 0o600, true); err == nil {
			t.Fatal("renamed over a non-empty directory")
		}
		entries, err := os.ReadDir(tg.dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Fatalf("left behind: %v", entries)
		}
	})
	t.Run("sad/an exclusive create leaves an existing file alone", func(t *testing.T) {
		checkLeaks(t)
		tg := newSSHTest(t, sshtest.Options{})
		p := filepath.Join(tg.dir, "keep")
		if err := os.WriteFile(p, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := testClient().Upload(ctx, tg.tid, tg.full(), p, []byte("x"), 0o600, false); !errors.Is(err, fs.ErrExist) {
			t.Fatal(err)
		}
		checkFile(t, p, "keep", 0o600)
	})
}
