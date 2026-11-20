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
	placeholderHostKey = `"ssh-ed25519 REPLACE_WITH_THE_TARGETS_HOST_KEY_LINE_FROM_ssh-keyscan_OR_THE_HOST"`
	placeholderKeyFile = "/etc/yasshm/keys/lab-look_ed25519"
)

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
		filled := strings.NewReplacer(placeholderHostKey, `"`+hostKey+`"`, placeholderKeyFile, keyFile).Replace(string(raw))
		code, out, errs := run(filled)
		if code != 0 || out != "config ok: 1 tenant, 1 server\n" || errs != "" {
			t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errs)
		}
	})
	t.Run("sad/as shipped, config check fails on the placeholders", func(t *testing.T) {
		code, _, errs := run(string(raw))
		if code != 1 || !strings.Contains(errs, "host_key") || !strings.Contains(errs, "private_key_file") {
			t.Fatalf("exit %d, stderr %q", code, errs)
		}
	})
}

// TestExampleIsASCIIAndSynthetic: the example names only reserved example
// domains and carries no real-looking value.
func TestExampleIsASCIIAndSynthetic(t *testing.T) {
	raw, err := os.ReadFile("config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(string(raw), "\n") {
		for _, r := range line {
			if r > 126 {
				t.Fatalf("line %d holds a non-ASCII character %U", i+1, r)
			}
		}
	}
	for _, want := range []string{"example.net", "example-team.cloudflareaccess.com", "REPLACE_WITH_THE_ACCESS_APPLICATION_AUD_TAG"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the example no longer holds %q", want)
		}
	}
}
