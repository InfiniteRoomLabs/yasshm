package realsshd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// TestRealSSHDLogin proves the CI loop: start sshd as the current user, log
// in with x/crypto/ssh against the pinned host key and run one command.
func TestRealSSHDLogin(t *testing.T) {
	if !Present(t) {
		return
	}
	t.Run("happy/echo ok over a real sshd", func(t *testing.T) {
		s := Start(t)
		cfg := &ssh.ClientConfig{
			User:            s.User,
			Auth:            []ssh.AuthMethod{ssh.PublicKeys(s.Signer(t))},
			HostKeyCallback: ssh.FixedHostKey(s.HostKey),
			Timeout:         5 * time.Second,
		}
		c, err := ssh.Dial("tcp", s.Addr, cfg)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer func() { _ = c.Close() }()
		sess, err := c.NewSession()
		if err != nil {
			t.Fatalf("session: %v", err)
		}
		defer func() { _ = sess.Close() }()
		var out bytes.Buffer
		sess.Stdout = &out
		if err := sess.Run("echo ok"); err != nil {
			t.Fatalf("run: %v", err)
		}
		if got := strings.TrimSpace(out.String()); got != "ok" {
			t.Fatalf("output = %q, want ok", got)
		}
		t.Logf("realsshd: server version %s", string(c.ServerVersion()))
	})
}

func TestPresentWithoutRequirement(t *testing.T) {
	t.Run("edge/Required follows the environment", func(t *testing.T) {
		t.Setenv(RequireEnv, "0")
		if Required() {
			t.Fatal("Required with 0")
		}
		t.Setenv(RequireEnv, "1")
		if !Required() {
			t.Fatal("not Required with 1")
		}
	})
}
