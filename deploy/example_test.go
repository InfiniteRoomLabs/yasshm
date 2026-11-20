package deploy

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/InfiniteRoomLabs/yasshm/internal/cli"
)

// The two values an operator replaces by hand in config.example.yaml.
const (
	placeholderHostKey = `"ssh-ed25519 REPLACE_WITH_THE_HOST_KEY_READ_ON_THE_TARGET"`
	placeholderKeyFile = "/etc/yasshm/keys/lab-look_ed25519"
)

// The other REPLACE_ values of the example and what stands in for them.
var placeholderValues = [][2]string{
	{"REPLACE_WITH_YOUR_TEAM.cloudflareaccess.com", "example-team.cloudflareaccess.com"},
	{"REPLACE_WITH_THE_ACCESS_APPLICATION_AUD_TAG", "an-example-audience"},
	{"REPLACE_WITH_YOUR_EMAIL@example.net", "operator@example.net"},
	{"REPLACE_WITH_THE_TARGET_HOST.example.net", "lab-1.example.net"},
}

// TestExampleConfigCheck runs `yasshm config check` (the command the unit's
// ExecStartPre runs) on the example after a generated key file and a real host
// key line replace the two placeholders, and on the example as shipped.
func TestExampleConfigCheck(t *testing.T) {
	raw, err := os.ReadFile("config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "test key")
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dir, "lab-look_ed25519")
	if werr := os.WriteFile(keyFile, pem.EncodeToMemory(block), 0o600); werr != nil {
		t.Fatal(werr)
	}
	hostPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	hostKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostPub)))

	run := func(text string) (int, string, string) {
		cfg := filepath.Join(t.TempDir(), "config.yaml")
		if werr := os.WriteFile(cfg, []byte(text), 0o600); werr != nil { //nolint:gosec // G703: the path is under t.TempDir
			t.Fatal(werr)
		}
		var out, errb bytes.Buffer
		code := cli.Run([]string{"config", "check", "--config", cfg}, &out, &errb)
		return code, out.String(), errb.String()
	}

	t.Run("happy/filled in, config check passes", func(t *testing.T) {
		pairs := []string{placeholderHostKey, `"` + hostKey + `"`, placeholderKeyFile, keyFile}
		for _, p := range placeholderValues {
			pairs = append(pairs, p[0], p[1])
		}
		filled := strings.NewReplacer(pairs...).Replace(string(raw))
		code, out, errs := run(filled)
		if code != 0 || out != "config ok: 1 tenant, 1 server\n" || errs != "" {
			t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errs)
		}
	})
	t.Run("sad/as shipped, config check fails on the placeholders", func(t *testing.T) {
		code, _, errs := run(string(raw))
		if code != 1 || !strings.Contains(errs, "host_key") || !strings.Contains(errs, "private_key_file") || strings.Count(errs, "replace the placeholder value") != len(placeholderValues) {
			t.Fatalf("exit %d, stderr %q", code, errs)
		}
	})
}

// TestExampleIsSynthetic: the example names only reserved example domains and
// marks every value the operator must set with REPLACE_ (the loader refuses
// those). ASCII is checked for every file by TestDocsASCII.
func TestExampleIsSynthetic(t *testing.T) {
	raw, err := os.ReadFile("config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"example.net", "REPLACE_WITH_YOUR_TEAM.cloudflareaccess.com", "REPLACE_WITH_THE_ACCESS_APPLICATION_AUD_TAG",
		"REPLACE_WITH_YOUR_EMAIL@example.net", "REPLACE_WITH_THE_TARGET_HOST.example.net"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the example no longer holds %q", want)
		}
	}
}

// TestQuickStartCoversPlaceholders: the README quick start's sed replaces every
// value the loader refuses (each REPLACE_ value and the host key) and puts the
// key-set URL on a loopback port, so nothing in it leaves the machine.
func TestQuickStartCoversPlaceholders(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(b)
	for _, p := range placeholderValues {
		if !strings.Contains(readme, p[0]) {
			t.Errorf("the quick start does not replace %s", p[0])
		}
	}
	hostKey := strings.Trim(placeholderHostKey, `"`)
	if !strings.Contains(readme, hostKey) {
		t.Errorf("the quick start does not replace the host key placeholder %q", hostKey)
	}
	if !strings.Contains(readme, "certs_url: https://example.net/certs") || !strings.Contains(readme, "certs_url: http://127.0.0.1:1/certs") {
		t.Error("the quick start does not point certs_url at a loopback port")
	}
	ex, err := os.ReadFile("config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ex), "\n    # certs_url: https://example.net/certs\n") {
		t.Error("the example's certs_url line is not the one the quick start's sed matches")
	}
}
