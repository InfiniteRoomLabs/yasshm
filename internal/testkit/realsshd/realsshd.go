// Package realsshd starts a real OpenSSH sshd as the current user on a random
// loopback port, with a generated host key and a generated client key whose
// authorized_keys line starts with "restrict" and has no forced command
// (spec sections 7 and 8). It is test support and is imported only by tests.
//
// The build sandbox has no sshd; CI has one (spec section 7 callout). A test
// that needs sshd calls Present first: when sshd is missing it logs one line
// and returns false, unless YASSHM_REQUIRE_SSHD=1, in which case the test
// fails. Nothing here skips a test.
package realsshd

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// RequireEnv is the environment variable that turns a missing sshd into a
// test failure. CI sets it to "1".
const RequireEnv = "YASSHM_REQUIRE_SSHD"

// fallbackPath is where Debian and Ubuntu install sshd; it is often not on a
// normal user's PATH.
const fallbackPath = "/usr/sbin/sshd"

// startTimeout bounds how long Start waits for sshd to answer.
const startTimeout = 15 * time.Second

// Path returns the absolute path of the sshd binary, or "" when there is
// none. sshd refuses to start from a relative path (it re-executes itself).
func Path() string {
	if p, err := exec.LookPath("sshd"); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
	}
	if fi, err := os.Stat(fallbackPath); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
		return fallbackPath
	}
	return ""
}

// Available reports whether an sshd binary is installed.
func Available() bool { return Path() != "" }

// Required reports whether YASSHM_REQUIRE_SSHD=1 is set.
func Required() bool { return os.Getenv(RequireEnv) == "1" }

// Present reports whether a real-sshd test can run. When sshd is missing it
// fails the test if Required, and otherwise logs one line and returns false;
// the caller then returns without running its scenarios.
func Present(t testing.TB) bool {
	t.Helper()
	if Available() {
		return true
	}
	if Required() {
		t.Fatalf("realsshd: %s=1 but no sshd binary was found on PATH or at %s", RequireEnv, fallbackPath)
	}
	t.Logf("realsshd: no sshd here; this real-sshd test runs in CI only (spec section 7)")
	return false
}

// Server is a running sshd.
type Server struct {
	// Addr is the loopback host:port sshd listens on.
	Addr string
	// Host and Port are Addr split.
	Host string
	Port int
	// HostKey is sshd's host public key.
	HostKey ssh.PublicKey
	// ClientKeyFile is the path of the client's private key (mode 0600,
	// unencrypted OpenSSH format).
	ClientKeyFile string
	// User is the login name (the current user).
	User string
	// Banner is the version line sshd sent, e.g. "SSH-2.0-OpenSSH_9.6p1 ...".
	Banner string
	// Dir is the temp directory that holds the config and keys.
	Dir string

	cmd    *exec.Cmd
	stderr *lockedBuffer
	exited chan struct{}
}

// lockedBuffer collects sshd's stderr; the process writes it while the test
// may read it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Log returns what sshd has written to stderr so far (its -e log).
func (s *Server) Log() string { return s.stderr.String() }

// Signer parses the client private key into a signer.
func (s *Server) Signer(t testing.TB) ssh.Signer {
	t.Helper()
	b, err := os.ReadFile(s.ClientKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	sg, err := ssh.ParsePrivateKey(b)
	if err != nil {
		t.Fatal(err)
	}
	return sg
}

// AuthorizedKey returns the client's public key in authorized_keys form
// ("type base64").
func (s *Server) AuthorizedKey(t testing.TB) string {
	t.Helper()
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.Signer(t).PublicKey())))
}

// HostKeyLine returns the host key in authorized_keys form ("type base64").
func (s *Server) HostKeyLine() string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.HostKey)))
}

// Start starts sshd and registers its cleanup. It fails the test when sshd
// is missing; call Present first. sshd's own log (-e) is written into the
// test log when the test fails.
func Start(t testing.TB) *Server {
	t.Helper()
	bin := Path()
	if bin == "" {
		t.Fatalf("realsshd: no sshd binary")
	}
	u, err := user.Current()
	if err != nil {
		t.Fatalf("realsshd: current user: %v", err)
	}
	dir := t.TempDir()
	hostKeyFile := filepath.Join(dir, "ssh_host_ed25519_key")
	hostPub := writeKey(t, hostKeyFile)
	clientKeyFile := filepath.Join(dir, "id_client")
	clientPub := writeKey(t, clientKeyFile)
	authFile := filepath.Join(dir, "authorized_keys")
	line := "restrict " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(clientPub))) + " yasshm-test\n"
	if err := os.WriteFile(authFile, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		s, err := launch(t, bin, dir, hostKeyFile, authFile, u.Username)
		if err == nil {
			s.HostKey = hostPub
			s.ClientKeyFile = clientKeyFile
			s.User = u.Username
			s.Dir = dir
			t.Logf("realsshd: %s listening on %s as user %s (version: %s)", bin, s.Addr, s.User, s.Banner)
			return s
		}
		lastErr = err
	}
	t.Fatalf("realsshd: sshd did not start: %v", lastErr)
	return nil
}

// launch starts one sshd on a fresh port. A port race with another process
// shows up as an early exit, and Start tries again.
func launch(t testing.TB, bin, dir, hostKeyFile, authFile, login string) (*Server, error) {
	t.Helper()
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	cfgFile := filepath.Join(dir, "sshd_config")
	cfg := strings.Join([]string{
		"Port " + strconv.Itoa(port),
		"ListenAddress 127.0.0.1",
		"HostKey " + hostKeyFile,
		"AuthorizedKeysFile " + authFile,
		"PidFile none",
		"UsePAM no",
		"StrictModes no",
		"PubkeyAuthentication yes",
		"PasswordAuthentication no",
		"KbdInteractiveAuthentication no",
		"AllowUsers " + login,
		"Subsystem sftp internal-sftp",
		"LogLevel VERBOSE",
		"",
	}, "\n")
	if werr := os.WriteFile(cfgFile, []byte(cfg), 0o600); werr != nil {
		return nil, werr
	}
	stderr := &lockedBuffer{}
	cmd := exec.Command(bin, "-D", "-e", "-f", cfgFile) //nolint:gosec // G204: test support runs the sshd binary it found, with a config it wrote
	cmd.Stdout = stderr
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err = cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", bin, err)
	}
	s := &Server{
		Addr:   net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		Host:   "127.0.0.1",
		Port:   port,
		cmd:    cmd,
		stderr: stderr,
		exited: make(chan struct{}),
	}
	go func() {
		_ = cmd.Wait()
		close(s.exited)
	}()
	banner, err := s.waitReady()
	if err != nil {
		s.stop()
		t.Logf("realsshd: attempt on port %d failed: %v\n--- sshd -e log ---\n%s", port, err, s.Log())
		return nil, err
	}
	s.Banner = banner
	t.Cleanup(func() {
		s.stop()
		if t.Failed() {
			t.Logf("--- sshd -e log (%s) ---\n%s", s.Addr, s.Log())
		}
	})
	return s, nil
}

// waitReady polls until sshd sends its version banner or exits.
func (s *Server) waitReady() (string, error) {
	deadline := time.Now().Add(startTimeout)
	for {
		select {
		case <-s.exited:
			return "", errors.New("sshd exited before it answered")
		default:
		}
		if banner, err := readBanner(s.Addr); err == nil {
			return banner, nil
		}
		if time.Now().After(deadline) {
			return "", errors.New("sshd did not answer in time")
		}
		select {
		case <-s.exited:
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// readBanner dials addr and reads the server's identification line.
func readBanner(addr string) (string, error) {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return "", err
	}
	defer func() { _ = c.Close() }()
	if derr := c.SetDeadline(time.Now().Add(2 * time.Second)); derr != nil {
		return "", derr
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "SSH-") {
		return "", fmt.Errorf("unexpected banner %q", line)
	}
	return line, nil
}

// stop ends sshd (TERM, then KILL after a second) and waits for it.
func (s *Server) stop() {
	select {
	case <-s.exited:
		return
	default:
	}
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-s.exited:
	case <-time.After(time.Second):
		_ = s.cmd.Process.Kill()
		<-s.exited
	}
}

// freePort asks the kernel for a free loopback port.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		return 0, err
	}
	return port, nil
}

// writeKey generates an ed25519 key, writes the private half to path (mode
// 0600, OpenSSH format) and returns the public half.
func writeKey(t testing.TB, path string) ssh.PublicKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	pk, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return pk
}
