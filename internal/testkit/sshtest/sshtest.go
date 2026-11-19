// Package sshtest is an in-process SSH server for tests (spec section 7),
// built on the server side of golang.org/x/crypto/ssh. It listens on a
// random loopback port, accepts exactly one generated client key, really
// runs each exec request with a local "sh -c" in a temp directory and in
// its own process group, streams stdout and stderr, and sends exit-status
// or exit-signal. It honours the signal request by signalling the process
// group and answers its want-reply, serves SFTP (relative paths resolve
// against the temp directory) and has fault switches. It records every
// request it sees, so tests can assert what a client never sends. It is
// test support and is imported only by tests.
//
// Requests it refuses, like a server with "restrict": pty-req, env, shell,
// x11-req, auth-agent-req@openssh.com, every global request and every
// channel type but "session".
package sshtest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// DefaultUser is the login name the server accepts unless Options.User is
// set.
const DefaultUser = "yasshm-test"

// Options are the fault switches. The zero value is a well-behaved server.
type Options struct {
	// User is the only login name accepted (default DefaultUser).
	User string
	// WrongHostKey makes the server present a host key other than the one
	// HostKey reports (the key a client pins).
	WrongHostKey bool
	// RefuseKey makes the server refuse every client key.
	RefuseKey bool
	// DropMidStream closes the TCP connection right after the first
	// stdout bytes of an exec have been sent.
	DropMidStream bool
	// RefuseSignal answers every signal request with failure and delivers
	// nothing.
	RefuseSignal bool
	// SlowBanner delays the server's version banner by this long.
	SlowBanner time.Duration
	// NoExitStatus closes an exec channel after the command ends without
	// sending exit-status or exit-signal.
	NoExitStatus bool
}

// Server is a running test server.
type Server struct {
	// Addr is the loopback host:port; Host and Port are its halves.
	Addr string
	Host string
	Port int
	// HostKey is the key a client should pin. With WrongHostKey the server
	// presents OfferedKey instead.
	HostKey    ssh.PublicKey
	OfferedKey ssh.PublicKey
	// ClientKeyFile is the generated client private key (mode 0600,
	// unencrypted OpenSSH format); ClientKey is its public half.
	ClientKeyFile string
	ClientKey     ssh.PublicKey
	// User is the accepted login name.
	User string
	// Root is the temp directory commands run in and SFTP is rooted at.
	Root string

	opts Options
	cfg  *ssh.ServerConfig
	ln   net.Listener
	done chan struct{}
	wg   sync.WaitGroup

	mu        sync.Mutex
	conns     map[net.Conn]struct{}
	procs     map[int]*exec.Cmd
	pids      []int
	requests  []string
	accepted  int
	closeOnce sync.Once
}

// signals maps the signal names a signal request may carry (OpenSSH's set,
// F10) to the signal; names travel without the SIG prefix.
var signals = map[string]syscall.Signal{
	"TERM": syscall.SIGTERM, "KILL": syscall.SIGKILL, "INT": syscall.SIGINT,
	"HUP": syscall.SIGHUP, "QUIT": syscall.SIGQUIT, "USR1": syscall.SIGUSR1, "USR2": syscall.SIGUSR2,
}

// signalName returns the RFC 4254 name of sig, without the SIG prefix.
func signalName(sig syscall.Signal) string {
	for n, s := range signals {
		if s == sig {
			return n
		}
	}
	switch sig {
	case syscall.SIGPIPE:
		return "PIPE"
	case syscall.SIGALRM:
		return "ALRM"
	case syscall.SIGABRT:
		return "ABRT"
	case syscall.SIGSEGV:
		return "SEGV"
	default:
		return "SIG" + strings.ToUpper(sig.String())
	}
}

// Start starts a server with the given options and registers Close as a
// cleanup.
func Start(t testing.TB, opts Options) *Server {
	t.Helper()
	if opts.User == "" {
		opts.User = DefaultUser
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	s := &Server{
		User:  opts.User,
		Root:  root,
		opts:  opts,
		done:  make(chan struct{}),
		conns: map[net.Conn]struct{}{},
		procs: map[int]*exec.Cmd{},
	}
	pinned, pinnedSigner := genKey(t)
	s.HostKey = pinned
	hostSigner := pinnedSigner
	if opts.WrongHostKey {
		_, hostSigner = genKey(t)
	}
	s.OfferedKey = hostSigner.PublicKey()
	s.ClientKeyFile = filepath.Join(dir, "id_client")
	s.ClientKey = writeClientKey(t, s.ClientKeyFile)
	s.cfg = &ssh.ServerConfig{
		PublicKeyCallback: s.checkKey,
		ServerVersion:     "SSH-2.0-yasshm-sshtest",
	}
	s.cfg.AddHostKey(hostSigner)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.ln = ln
	s.Addr = ln.Addr().String()
	s.Host = "127.0.0.1"
	s.Port = ln.Addr().(*net.TCPAddr).Port
	s.wg.Add(1)
	go s.serve()
	t.Cleanup(s.Close)
	return s
}

// checkKey accepts exactly the generated client key for the login name.
func (s *Server) checkKey(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	if s.opts.RefuseKey || c.User() != s.User || string(key.Marshal()) != string(s.ClientKey.Marshal()) {
		return nil, errors.New("sshtest: key refused")
	}
	return &ssh.Permissions{}, nil
}

// Requests returns every request and channel the server saw, in order:
// channel request types ("exec", "pty-req", "env", "signal:TERM",
// "subsystem:sftp", ...), "global:<type>" and "channel:<type>" for a
// refused channel type.
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

// Conns returns how many connections are open now.
func (s *Server) Conns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// Accepted returns how many TCP connections the server has accepted.
func (s *Server) Accepted() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accepted
}

// Pids returns the pid of every command the server has started.
func (s *Server) Pids() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.pids...)
}

// Running returns how many started commands have not been reaped.
func (s *Server) Running() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.procs)
}

func (s *Server) record(r string) {
	s.mu.Lock()
	s.requests = append(s.requests, r)
	s.mu.Unlock()
}

// Close stops listening, closes every connection, kills every command's
// process group and waits for every goroutine of the server. It is
// idempotent.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
		_ = s.ln.Close()
		s.mu.Lock()
		for c := range s.conns {
			_ = c.Close()
		}
		for pid := range s.procs {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
		s.mu.Unlock()
		s.wg.Wait()
	})
}

func (s *Server) serve() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		select {
		case <-s.done:
			s.mu.Unlock()
			_ = c.Close()
			return
		default:
		}
		s.conns[c] = struct{}{}
		s.accepted++
		s.wg.Add(1)
		s.mu.Unlock()
		go s.handle(c)
	}
}

func (s *Server) handle(c net.Conn) {
	defer s.wg.Done()
	defer func() {
		_ = c.Close()
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
	}()
	if s.opts.SlowBanner > 0 {
		t := time.NewTimer(s.opts.SlowBanner)
		select {
		case <-t.C:
		case <-s.done:
			t.Stop()
			return
		}
	}
	sconn, chans, reqs, err := ssh.NewServerConn(c, s.cfg)
	if err != nil {
		return
	}
	defer func() { _ = sconn.Close() }()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for r := range reqs {
			s.record("global:" + r.Type)
			if r.WantReply {
				_ = r.Reply(false, nil)
			}
		}
	}()
	for nc := range chans {
		if nc.ChannelType() != "session" {
			s.record("channel:" + nc.ChannelType())
			_ = nc.Reject(ssh.Prohibited, "sshtest: only session channels")
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		s.wg.Add(1)
		go s.session(c, ch, creqs)
	}
}

// session handles one session channel's requests.
func (s *Server) session(c net.Conn, ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer s.wg.Done()
	var (
		mu      sync.Mutex
		started bool
		pid     int
		ended   bool
	)
	for req := range reqs {
		switch req.Type {
		case "exec":
			s.record("exec")
			var p struct{ Command string }
			mu.Lock()
			ok := !started && ssh.Unmarshal(req.Payload, &p) == nil
			if ok {
				started = true
				var err error
				pid, err = s.run(c, ch, p.Command, func() {
					mu.Lock()
					ended = true
					mu.Unlock()
				})
				ok = err == nil
			}
			mu.Unlock()
			_ = req.Reply(ok, nil)
			if !ok {
				_ = ch.Close()
			}
		case "subsystem":
			var p struct{ Name string }
			_ = ssh.Unmarshal(req.Payload, &p)
			s.record("subsystem:" + p.Name)
			mu.Lock()
			ok := !started && p.Name == "sftp"
			started = started || ok
			mu.Unlock()
			_ = req.Reply(ok, nil)
			if ok {
				s.wg.Add(1)
				go s.serveSFTP(ch)
			}
		case "signal":
			var p struct{ Signal string }
			_ = ssh.Unmarshal(req.Payload, &p)
			s.record("signal:" + p.Signal)
			sig, known := signals[p.Signal]
			mu.Lock()
			ok := known && !s.opts.RefuseSignal && pid > 0 && !ended
			if ok {
				ok = syscall.Kill(-pid, sig) == nil
			}
			mu.Unlock()
			if req.WantReply {
				_ = req.Reply(ok, nil)
			}
		default:
			s.record(req.Type)
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

// dropWriter closes the TCP connection after the first write.
type dropWriter struct {
	w    io.Writer
	conn net.Conn
	once sync.Once
}

func (d *dropWriter) Write(p []byte) (int, error) {
	n, err := d.w.Write(p)
	d.once.Do(func() { _ = d.conn.Close() })
	return n, err
}

// run starts command with sh -c in Root, in its own process group, and a
// goroutine that reaps it and reports its exit on ch.
func (s *Server) run(c net.Conn, ch ssh.Channel, command string, ended func()) (int, error) {
	cmd := exec.Command("sh", "-c", command) //nolint:gosec // G204: the test server's job is to run the command it was sent
	cmd.Dir = s.Root
	cmd.Stdin = ch
	var stdout io.Writer = ch
	if s.opts.DropMidStream {
		stdout = &dropWriter{w: ch, conn: c}
	}
	cmd.Stdout = stdout
	cmd.Stderr = ch.Stderr()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.done:
		return 0, errors.New("sshtest: closed")
	default:
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	s.procs[pid] = cmd
	s.pids = append(s.pids, pid)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		err := cmd.Wait()
		s.mu.Lock()
		delete(s.procs, pid)
		s.mu.Unlock()
		ended()
		s.reportExit(ch, cmd, err)
	}()
	return pid, nil
}

// reportExit sends exit-status or exit-signal (unless NoExitStatus) and
// closes the channel.
func (s *Server) reportExit(ch ssh.Channel, cmd *exec.Cmd, err error) {
	defer func() { _ = ch.Close() }()
	_ = ch.CloseWrite()
	if s.opts.NoExitStatus || cmd.ProcessState == nil {
		return
	}
	ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	switch {
	case ok && ws.Signaled():
		_, _ = ch.SendRequest("exit-signal", false, ssh.Marshal(struct {
			Signal     string
			CoreDumped bool
			Error      string
			Lang       string
		}{Signal: signalName(ws.Signal())}))
	default:
		_ = err                                                                                                                // the exit code is in ProcessState either way
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(cmd.ProcessState.ExitCode())})) //nolint:gosec // G115: an exit code is 0..255 here
	}
}

// serveSFTP serves the SFTP subsystem on ch until the client ends it.
func (s *Server) serveSFTP(ch ssh.Channel) {
	defer s.wg.Done()
	defer func() { _ = ch.Close() }()
	srv, err := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(s.Root))
	if err != nil {
		return
	}
	_ = srv.Serve()
	_ = srv.Close()
}

// genKey returns a new ed25519 host key.
func genKey(t testing.TB) (ssh.PublicKey, ssh.Signer) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sg, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return sg.PublicKey(), sg
}

// writeClientKey writes a new ed25519 private key to path (0600) and
// returns its public half.
func writeClientKey(t testing.TB, path string) ssh.PublicKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	if werr := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); werr != nil {
		t.Fatal(werr)
	}
	pk, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return pk
}
