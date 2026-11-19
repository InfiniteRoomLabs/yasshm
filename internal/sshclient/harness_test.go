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
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/realsshd"
	"github.com/InfiniteRoomLabs/yasshm/internal/testkit/sshtest"
)

const testTenant tenant.TenantID = "t1"

// target is one SSH server the contract suite runs against.
type target struct {
	name string
	real bool
	tid  tenant.TenantID
	srv  *tenant.Server
	// dir is an absolute directory on the target (the same machine) for
	// SFTP files and pid files.
	dir  string
	user string
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
}

func keyLine(pk ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk)))
}

// newSSHTest starts an in-process server and the tenant.Server for it.
func newSSHTest(t *testing.T, o sshtest.Options) target {
	t.Helper()
	s := sshtest.Start(t, o)
	return target{
		name: "sshtest",
		tid:  testTenant,
		srv: &tenant.Server{
			Tenant:   testTenant,
			ID:       "srv-1",
			Host:     s.Host,
			Port:     s.Port,
			Identity: tenant.Identity{ID: "look", User: s.User, PrivateKeyFile: s.ClientKeyFile},
			HostKey:  keyLine(s.HostKey),
			Access:   tenant.AccessLook,
		},
		dir:     s.Root,
		user:    s.User,
		runsAs:  currentUser(t),
		hostKey: s.OfferedKey,
		conns:   s.Conns,
		st:      s,
		banner:  "SSH-2.0-yasshm-sshtest",
	}
}

// newReal starts a real sshd and the tenant.Server for it.
func newReal(t *testing.T) target {
	t.Helper()
	s := realsshd.Start(t)
	return target{
		name: "realsshd",
		real: true,
		tid:  testTenant,
		srv: &tenant.Server{
			Tenant:   testTenant,
			ID:       "srv-real",
			Host:     s.Host,
			Port:     s.Port,
			Identity: tenant.Identity{ID: "look", User: s.User, PrivateKeyFile: s.ClientKeyFile},
			HostKey:  s.HostKeyLine(),
			Access:   tenant.AccessLook,
		},
		dir:     t.TempDir(),
		user:    s.User,
		runsAs:  s.User,
		hostKey: s.HostKey,
		banner:  s.Banner,
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
	if !poll(5*time.Second, func() bool { return tg.conns() == 0 }) {
		t.Errorf("%d connections still open on the server", tg.conns())
	}
}

// poll calls cond every 10ms until it is true or d passes.
func poll(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond) // the polling interval of a deadline-bounded poll
	}
}

// goroutines returns the stacks, by goroutine id, of the goroutines that
// belong to this module, x/crypto/ssh or pkg/sftp and are not test
// goroutines.
func goroutines() map[string]string {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	out := map[string]string{}
	for _, g := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(g, "testing.tRunner") || strings.Contains(g, "testing.(*T).Run") || strings.Contains(g, "testing.runCleanup") {
			continue
		}
		if !strings.Contains(g, "InfiniteRoomLabs/yasshm/") && !strings.Contains(g, "golang.org/x/crypto/ssh") && !strings.Contains(g, "github.com/pkg/sftp") {
			continue
		}
		head, _, _ := strings.Cut(g, "\n")
		out[strings.Fields(head + " x x")[1]] = g
	}
	return out
}

// checkLeaks registers a cleanup, run after every later cleanup (the
// servers' Close included), that fails when a goroutine of the client or
// server outlived the test.
func checkLeaks(t *testing.T) {
	t.Helper()
	base := goroutines()
	t.Cleanup(func() {
		var leaked []string
		ok := poll(5*time.Second, func() bool {
			leaked = leaked[:0]
			for id, g := range goroutines() {
				if _, old := base[id]; !old {
					leaked = append(leaked, g)
				}
			}
			return len(leaked) == 0
		})
		if !ok {
			t.Errorf("%d goroutines leaked:\n%s", len(leaked), strings.Join(leaked, "\n\n"))
		}
	})
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
func otherHostKey(t *testing.T) (string, ssh.PublicKey) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pk, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return keyLine(pk), pk
}
