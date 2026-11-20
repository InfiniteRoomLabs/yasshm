package hostkey

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func edKey(t *testing.T) (ssh.PublicKey, ssh.Signer) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pk, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	sg, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return pk, sg
}

func line(pk ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk)))
}

func certFor(t *testing.T, pk ssh.PublicKey) *ssh.Certificate {
	t.Helper()
	_, ca := edKey(t)
	c := &ssh.Certificate{Key: pk, CertType: ssh.HostCert, ValidPrincipals: []string{"h"}, ValidBefore: ssh.CertTimeInfinity}
	if err := c.SignCert(rand.Reader, ca); err != nil {
		t.Fatal(err)
	}
	return c
}

func writeKH(t *testing.T, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func msgOf(t *testing.T, err error) string {
	t.Helper()
	var he *Error
	if !errors.As(err, &he) {
		t.Fatalf("error %v (%T) is not a *hostkey.Error", err, err)
	}
	return he.Msg
}

func TestParseKey(t *testing.T) {
	pk, _ := edKey(t)
	cases := []struct {
		name, in, msg string
	}{
		{"happy/plain key", line(pk), ""},
		{"happy/with a comment", line(pk) + " host@example.net", ""},
		{"sad/two lines", line(pk) + "\n" + line(pk), "must be one line"},
		{"sad/carriage return", line(pk) + "\r", "must be one line"},
		{"sad/garbage", "not a key", "does not parse as an OpenSSH public key (\"type base64 [comment]\")"},
		{"sad/options", "restrict " + line(pk), "must not carry authorized_keys options"},
		{"raging/certificate", strings.TrimSpace(string(ssh.MarshalAuthorizedKey(certFor(t, pk)))), "SSH certificates are not supported in this version"},
		{"edge/empty", "", "does not parse as an OpenSSH public key (\"type base64 [comment]\")"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseKey(c.in)
			if c.msg == "" {
				if err != nil || line(got) != line(pk) {
					t.Fatalf("ParseKey = %v, %v", got, err)
				}
				return
			}
			if m := msgOf(t, err); m != c.msg {
				t.Fatalf("msg = %q, want %q", m, c.msg)
			}
			if err.Error() != c.msg {
				t.Fatalf("Error() = %q", err.Error())
			}
		})
	}
	t.Run("sad/trailing second key", func(t *testing.T) {
		// ParseAuthorizedKey reads one line; a second key on the same line is
		// part of the comment, so the line is accepted as one key.
		if _, err := ParseKey(line(pk) + " " + line(pk)); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestFixed(t *testing.T) {
	pk, _ := edKey(t)
	other, _ := edKey(t)
	t.Run("happy/accepts the pinned key", func(t *testing.T) {
		p, err := Fixed(line(pk))
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Check(pk); err != nil {
			t.Fatal(err)
		}
		if got := p.Algorithms(); len(got) != 1 || got[0] != ssh.KeyAlgoED25519 {
			t.Fatalf("Algorithms = %v", got)
		}
		if len(p.keys) != 1 || !strings.Contains(p.String(), ssh.FingerprintSHA256(pk)) {
			t.Fatalf("keys/String = %v %s", p.keys, p)
		}
	})
	t.Run("sad/refuses another key with its fingerprint", func(t *testing.T) {
		p, err := Fixed(line(pk))
		if err != nil {
			t.Fatal(err)
		}
		err = p.Check(other)
		var me *MismatchError
		if !errors.As(err, &me) || me.Fingerprint != ssh.FingerprintSHA256(other) || me.Certificate {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(err.Error(), ssh.FingerprintSHA256(other)) || strings.Contains(err.Error(), line(other)[12:]) {
			t.Fatalf("message = %q", err.Error())
		}
	})
	t.Run("raging/refuses a certificate for the pinned key", func(t *testing.T) {
		p, err := Fixed(line(pk))
		if err != nil {
			t.Fatal(err)
		}
		err = p.Check(certFor(t, pk))
		var me *MismatchError
		if !errors.As(err, &me) || !me.Certificate || !strings.Contains(err.Error(), "certificate") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("edge/nil key", func(t *testing.T) {
		p, err := Fixed(line(pk))
		if err != nil {
			t.Fatal(err)
		}
		var me *MismatchError
		if err := p.Check(nil); !errors.As(err, &me) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("sad/bad line", func(t *testing.T) {
		if _, err := Fixed("x"); err == nil {
			t.Fatal("accepted")
		}
	})
}

func TestAlgorithms(t *testing.T) {
	ed, _ := edKey(t)
	rk, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rpk, err := ssh.NewPublicKey(&rk.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	ek, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	epk, err := ssh.NewPublicKey(&ek.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("happy/rsa uses the sha2 forms", func(t *testing.T) {
		p := &Pinned{keys: []ssh.PublicKey{rpk}}
		got := strings.Join(p.Algorithms(), ",")
		if got != ssh.KeyAlgoRSASHA512+","+ssh.KeyAlgoRSASHA256 {
			t.Fatalf("got %s", got)
		}
	})
	t.Run("corner/several types, deduplicated, no certificate algorithm", func(t *testing.T) {
		ed2, _ := edKey(t)
		p := &Pinned{keys: []ssh.PublicKey{ed, epk, rpk, ed2, rpk}}
		got := p.Algorithms()
		want := []string{ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("got %v, want %v", got, want)
		}
		for _, a := range got {
			if strings.Contains(a, "cert") {
				t.Fatalf("certificate algorithm %s", a)
			}
		}
	})
}

func TestKnownHosts(t *testing.T) {
	a, _ := edKey(t)
	b, _ := edKey(t)
	c, _ := edKey(t)
	const host = "lab-1.example.net"
	hashed := knownhosts.HashHostname(knownhosts.Normalize(host + ":2222"))
	cases := []struct {
		name, text string
		port       int
		msg        string
		accept     []ssh.PublicKey
		refuse     []ssh.PublicKey
	}{
		{"happy/one key", host + " " + line(a) + "\n", 22, "", []ssh.PublicKey{a}, []ssh.PublicKey{b}},
		{"happy/two keys, one revoked", host + " " + line(a) + "\n" + host + " " + line(b) + "\n@revoked * " + line(b) + "\n", 22, "", []ssh.PublicKey{a}, []ssh.PublicKey{b}},
		{"happy/hashed entry with a port", hashed + " " + line(a) + "\n", 2222, "", []ssh.PublicKey{a}, nil},
		{"happy/wildcard", "*.example.net " + line(a) + "\n", 22, "", []ssh.PublicKey{a}, nil},
		{"happy/other hosts' keys are not pinned", host + " " + line(a) + "\nother.example.net " + line(c) + "\n", 22, "", []ssh.PublicKey{a}, []ssh.PublicKey{c}},
		{"sad/no key for the host", "other.example.net " + line(a) + "\n", 22, "lists no key for lab-1.example.net:22", nil, nil},
		{"sad/wrong port", host + " " + line(a) + "\n", 2222, "lists no key for lab-1.example.net:2222", nil, nil},
		{"sad/only revoked", host + " " + line(a) + "\n@revoked * " + line(a) + "\n", 22, "lists only revoked keys for lab-1.example.net:22", nil, nil},
		{"raging/cert authority", "@cert-authority *.example.net " + line(a) + "\n" + host + " " + line(b) + "\n", 22, "holds a @cert-authority line; SSH certificates are not supported in this version", nil, nil},
		{"raging/only a certificate for the host", host + " " + line(certFor(t, a)) + "\n", 22, "lists only certificates for lab-1.example.net:22; SSH certificates are not supported in this version", nil, nil},
		{"raging/not a known_hosts file", "garbage\n", 22, "does not parse as a known_hosts file", nil, nil},
		{"edge/empty file", "", 22, "lists no key for lab-1.example.net:22", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := KnownHosts(writeKH(t, tc.text), host, tc.port)
			if tc.msg != "" {
				if m := msgOf(t, err); m != tc.msg {
					t.Fatalf("msg = %q, want %q", m, tc.msg)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, k := range tc.accept {
				if err := p.Check(k); err != nil {
					t.Fatalf("pinned key refused: %v", err)
				}
			}
			for _, k := range tc.refuse {
				if err := p.Check(k); err == nil {
					t.Fatal("unpinned key accepted")
				}
			}
		})
	}
	t.Run("sad/missing file", func(t *testing.T) {
		_, err := KnownHosts(filepath.Join(t.TempDir(), "nope"), host, 22)
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("sad/a directory", func(t *testing.T) {
		_, err := KnownHosts(t.TempDir(), host, 22)
		if m := msgOf(t, err); m != "is not a regular file" {
			t.Fatalf("msg = %q", m)
		}
	})
	t.Run("raging/a file group or other can write", func(t *testing.T) {
		p := writeKH(t, host+" "+strings.TrimSpace(string(ssh.MarshalAuthorizedKey(probeKey)))+"\n")
		for _, mode := range []os.FileMode{0o620, 0o602} {
			if err := os.Chmod(p, mode); err != nil {
				t.Fatal(err)
			}
			_, err := KnownHosts(p, host, 22)
			if m := msgOf(t, err); m != "is writable by group or other" {
				t.Fatalf("mode %04o: msg = %q", mode, m)
			}
		}
	})
	t.Run("raging/too large", func(t *testing.T) {
		_, err := KnownHosts(writeKH(t, strings.Repeat("#", MaxKnownHostsBytes+1)), host, 22)
		if m := msgOf(t, err); m != "is larger than 1 MiB" {
			t.Fatalf("msg = %q", m)
		}
	})
	t.Run("edge/exactly the size limit", func(t *testing.T) {
		text := host + " " + line(a) + "\n"
		pad := MaxKnownHostsBytes - len(text)
		text += strings.Repeat(strings.Repeat("#", 1023)+"\n", pad/1024) + strings.Repeat("#", pad%1024)
		if _, err := KnownHosts(writeKH(t, text), host, 22); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("sad/path under a regular file", func(t *testing.T) {
		f := writeKH(t, "")
		if _, err := KnownHosts(filepath.Join(f, "x"), host, 22); err == nil {
			t.Fatal("accepted")
		}
	})
}
