package sshclient

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/leakcheck"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/realsshd"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/sshtest"
)

const testTenant tenant.TenantID = "t1"

// target is one SSH server the contract suite runs against.
type target struct {
	tid tenant.TenantID
	srv *tenant.Server
	// dir is an absolute directory on the target (the same machine) for
	// SFTP files and pid files.
	dir string
	// runsAs is the account commands run as on the target.
	runsAs string
	// hostKey is the key the server presents.
	hostKey ssh.PublicKey
	// conns returns the server's open connection count, or nil when the
	// server cannot tell (real sshd).
	conns func() int
	// st is the in-process server, nil for real sshd.
	st *sshtest.Server
	// banner is the server's version line.
	banner string
	// leftovers returns the pids of processes the server's sessions started
	// that still run.
	leftovers func() []int
}

// full returns a copy of the target's server that grants full access, for
// Upload.
func (tg target) full() *tenant.Server {
	srv := *tg.srv
	srv.Access = tenant.AccessFull
	return &srv
}

// newSSHTest starts an in-process server and the tenant.Server for it.
func newSSHTest(t *testing.T, o sshtest.Options) target {
	t.Helper()
	s := sshtest.Start(t, o)
	return target{
		tid: testTenant,
		srv: &tenant.Server{
			Tenant:   testTenant,
			ID:       "srv-1",
			Host:     s.Host,
			Port:     s.Port,
			Identity: tenant.Identity{ID: "look", User: s.User, PrivateKeyFile: s.ClientKeyFile},
			HostKey:  sshtest.KeyLine(s.HostKey),
			Access:   tenant.AccessLook,
		},
		dir:     s.Root,
		runsAs:  currentUser(t),
		hostKey: s.OfferedKey,
		conns:   s.Conns,
		st:      s,
		banner:  "SSH-2.0-yasshm-sshtest",
		leftovers: func() []int {
			var out []int
			for _, pid := range s.Pids() {
				if alive(pid) {
					out = append(out, pid)
				}
			}
			return out
		},
	}
}

// newReal starts a real sshd and the tenant.Server for it.
func newReal(t *testing.T) target {
	t.Helper()
	s := realsshd.Start(t)
	return target{
		tid: testTenant,
		srv: &tenant.Server{
			Tenant:   testTenant,
			ID:       "srv-real",
			Host:     s.Host,
			Port:     s.Port,
			Identity: tenant.Identity{ID: "look", User: s.User, PrivateKeyFile: s.ClientKeyFile},
			HostKey:  sshtest.KeyLine(s.HostKey),
			Access:   tenant.AccessLook,
		},
		dir:       t.TempDir(),
		runsAs:    s.User,
		hostKey:   s.HostKey,
		banner:    s.Banner,
		leftovers: s.Leftovers,
	}
}

// testClient has short timings so the suite stays fast.
func testClient() *Client {
	return New(Options{ConnectTimeout: 5 * time.Second, KillGrace: 300 * time.Millisecond, KillWait: time.Second})
}

// waitNoConns waits until the in-process server has no open connection.
func waitNoConns(t *testing.T, tg target) {
	t.Helper()
	if tg.conns == nil {
		return
	}
	if !leakcheck.Poll(5*time.Second, func() bool { return tg.conns() == 0 }) {
		t.Errorf("%d connections still open on the server", tg.conns())
	}
}

// currentUser returns the login name of the test process.
func currentUser(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	return u.Username
}

// pgid returns the process group of pid, or -1.
func pgid(pid int) int {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return -1
	}
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return -1
	}
	f := strings.Fields(string(b[i+1:]))
	if len(f) < 3 {
		return -1
	}
	n, err := strconv.Atoi(f[2])
	if err != nil {
		return -1
	}
	return n
}

// alive reports whether pid is a live (not zombie) process.
func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	i := bytes.LastIndexByte(b, ')')
	if i < 0 || i+2 >= len(b) {
		return false
	}
	return b[i+2] != 'Z' && b[i+2] != 'X'
}

// notifyBuffer is a writer that a test can wait on.
type notifyBuffer struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	wake chan struct{}
}

func newNotifyBuffer() *notifyBuffer { return &notifyBuffer{wake: make(chan struct{}, 1)} }

func (b *notifyBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	n, err := b.buf.Write(p)
	b.mu.Unlock()
	select {
	case b.wake <- struct{}{}:
	default:
	}
	return n, err
}

func (b *notifyBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitFor waits until the buffer holds substr.
func (b *notifyBuffer) waitFor(t *testing.T, substr string, d time.Duration) {
	t.Helper()
	timer := time.NewTimer(d)
	defer timer.Stop()
	for !strings.Contains(b.String(), substr) {
		select {
		case <-b.wake:
		case <-timer.C:
			t.Fatalf("no %q in the output within %v (have %q)", substr, d, b.String())
		}
	}
}

// waitLine waits until the buffer holds a complete line and returns the
// first one.
func (b *notifyBuffer) waitLine(t *testing.T, d time.Duration) string {
	t.Helper()
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		if s := b.String(); strings.Contains(s, "\n") {
			l, _, _ := strings.Cut(s, "\n")
			return l
		}
		select {
		case <-b.wake:
		case <-timer.C:
			t.Fatalf("no line of output within %v (have %q)", d, b.String())
		}
	}
}

// writeKeyFile writes a new ed25519 private key to a 0600 file.
func writeKeyFile(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "id_other")
	if err := os.WriteFile(p, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// otherHostKey returns a new public key in authorized_keys form.
func otherHostKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pk, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return sshtest.KeyLine(pk)
}

// checkNoLeftovers registers a cleanup, run after every later cleanup, that
// fails when a process started by the target's sessions still runs.
func checkNoLeftovers(t *testing.T, tg *target) {
	t.Helper()
	t.Cleanup(func() {
		if tg.leftovers == nil {
			return
		}
		var left []int
		if !leakcheck.Poll(5*time.Second, func() bool { left = tg.leftovers(); return len(left) == 0 }) {
			t.Errorf("processes left behind: %v", left)
		}
	})
}

// noHostIn fails when msg names the server's host, port, login user or key
// path.
func noHostIn(t *testing.T, srv *tenant.Server, msg string) {
	t.Helper()
	for _, s := range []string{srv.Host, ":" + strconv.Itoa(srv.Port), strconv.Itoa(srv.Port), srv.Identity.User, srv.Identity.PrivateKeyFile, srv.KnownHostsFile} {
		if s != "" && strings.Contains(msg, s) {
			t.Fatalf("message %q names %q", msg, s)
		}
	}
}
