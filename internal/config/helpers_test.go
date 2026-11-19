package config

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// keys holds material generated for one test: a private key file written to
// a temp dir with mode 0600, and a host key line.
type keys struct {
	dir      string
	keyFile  string
	keyPEM   []byte
	hostKey  string
	hostPub  ssh.PublicKey
	otherPub ssh.PublicKey
}

func newEd25519(t testing.TB) (ed25519.PrivateKey, ssh.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return priv, sp
}

func genKeys(t testing.TB) *keys {
	t.Helper()
	dir := t.TempDir()
	priv, _ := newEd25519(t)
	block, err := ssh.MarshalPrivateKey(priv, "test key")
	if err != nil {
		t.Fatal(err)
	}
	k := &keys{dir: dir, keyFile: filepath.Join(dir, "lab-look_ed25519"), keyPEM: pem.EncodeToMemory(block)}
	writeFile(t, k.keyFile, k.keyPEM, 0o600)
	_, k.hostPub = newEd25519(t)
	_, k.otherPub = newEd25519(t)
	k.hostKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k.hostPub)))
	return k
}

func writeFile(t testing.TB, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil { //nolint:gosec // G703: test paths come from t.TempDir
		t.Fatal(err)
	}
	// WriteFile's mode is filtered by the umask; set it exactly.
	chmod(t, path, mode)
}

// chmod sets an exact mode; tests use loose modes on purpose to check that
// the loader refuses them.
func chmod(t testing.TB, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

const baseTemplate = `version: 1
server:
  listen: 127.0.0.1:8822
  data_dir: /var/lib/yasshm
  audit_log: /var/log/yasshm/audit.jsonl
auth:
  provider: cloudflare-access
  cloudflare_access:
    team_domain: example-team.cloudflareaccess.com
    audience: "EXAMPLE-AUDIENCE-TAG"
tenants:
  - id: default
    principals:
      - email: operator@example.net
    identities:
      lab-look:
        user: yasshm-look
        private_key_file: KEYFILE
    servers:
      - id: lab-1
        description: Example lab host
        host: lab-1.example.net
        port: 22
        identity: lab-look
        host_key: "HOSTKEY"
        access: look
        tags: [lab]
    policy:
      deny_commands: []
      deny_paths: []
    limits: {}
`

// base returns the valid base config with this test's key file and host key.
func (k *keys) base() string {
	return strings.NewReplacer("KEYFILE", k.keyFile, "HOSTKEY", k.hostKey).Replace(baseTemplate)
}

// edit replaces old with repl in s exactly once and fails the test if old is
// not there, so a case cannot silently test the base config.
func edit(t testing.TB, s, old, repl string) string {
	t.Helper()
	if strings.Count(s, old) != 1 {
		t.Fatalf("edit: %q occurs %d times in the config, want exactly 1", old, strings.Count(s, old))
	}
	return strings.Replace(s, old, repl, 1)
}

// problems returns the problem lines of err, or fails when err is not an
// *Error.
func problems(t testing.TB, err error) []string {
	t.Helper()
	var ce *Error
	if !errors.As(err, &ce) {
		t.Fatalf("error %v (%T) is not a *config.Error", err, err)
	}
	out := make([]string, len(ce.Problems))
	for i, p := range ce.Problems {
		out[i] = p.Path + ": " + p.Message
	}
	return out
}

// wantProblem fails unless err carries a problem whose "path: message" form
// contains want.
func wantProblem(t testing.TB, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("got no error, want a problem containing %q", want)
	}
	for _, p := range problems(t, err) {
		if strings.Contains(p, want) {
			return
		}
	}
	t.Fatalf("no problem contains %q; got:\n%v", want, err)
}

func writeConfig(t testing.TB, dir, text string) string {
	t.Helper()
	p := filepath.Join(dir, "config.yaml")
	writeFile(t, p, []byte(text), 0o600)
	return p
}

func sshAuthorized(pk ssh.PublicKey) []byte { return ssh.MarshalAuthorizedKey(pk) }

func sshMarshalWithPassphrase(priv ed25519.PrivateKey) ([]byte, error) {
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "test key", []byte("synthetic-passphrase"))
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(block), nil
}
