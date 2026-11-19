// Package configtest writes synthetic yasshm config files for tests: a
// generated ed25519 private key (mode 0600) and host key, example.net hosts,
// and nothing real. It is test support (spec section 7) and is imported only
// by tests.
package configtest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Audience is the synthetic Access audience in configs this package writes.
const Audience = "SYNTHETIC-AUDIENCE-TAG"

// Files describes a config written by Write.
type Files struct {
	// Dir is the temp directory that holds everything.
	Dir string
	// Config is the path of the config file.
	Config string
	// KeyFile is the path of the generated private key.
	KeyFile string
	// HostKey is the generated host key in authorized_keys form.
	HostKey string
}

// Write writes a valid one-tenant config with two servers into a new temp
// directory.
func Write(t testing.TB) Files {
	t.Helper()
	f := Files{Dir: t.TempDir()}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	f.KeyFile = filepath.Join(f.Dir, "id_test")
	writeFile(t, f.KeyFile, pem.EncodeToMemory(block))
	hostPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := ssh.NewPublicKey(hostPub)
	if err != nil {
		t.Fatal(err)
	}
	f.HostKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp)))
	text := fmt.Sprintf(`version: 1
server:
  listen: 127.0.0.1:8822
  data_dir: /var/lib/yasshm
  audit_log: /var/log/yasshm/audit.jsonl
auth:
  provider: cloudflare-access
  cloudflare_access:
    team_domain: example-team.cloudflareaccess.com
    audience: %s
tenants:
  - id: default
    principals: [{email: operator@example.net}]
    identities:
      lab-look: {user: yasshm-look, private_key_file: %s}
    servers:
      - {id: lab-1, host: lab-1.example.net, identity: lab-look, host_key: %q, access: look}
      - {id: lab-2, host: lab-2.example.net, identity: lab-look, host_key: %q, access: look}
`, Audience, f.KeyFile, f.HostKey, f.HostKey)
	f.Config = filepath.Join(f.Dir, "config.yaml")
	writeFile(t, f.Config, []byte(text))
	return f
}

// Rewrite writes a variant of the config text, made by replacing old with
// repl once, to a new file and returns its path. It fails the test if old is
// not in the config.
func (f Files) Rewrite(t testing.TB, old, repl string) string {
	t.Helper()
	b, err := os.ReadFile(f.Config)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), old) {
		t.Fatalf("configtest: %q is not in the config", old)
	}
	p := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, p, []byte(strings.Replace(string(b), old, repl, 1)))
	return p
}

func writeFile(t testing.TB, p string, data []byte) {
	t.Helper()
	if err := os.WriteFile(p, data, 0o600); err != nil { //nolint:gosec // G703: test paths from t.TempDir
		t.Fatal(err)
	}
}
