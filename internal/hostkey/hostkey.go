// Package hostkey holds the host key pinning rules that the config loader
// checks at start and the SSH layer enforces at connect (spec D13, 5.1 and
// 5.5): a pinned host key is one plain key in authorized_keys form, and a
// pinned known_hosts file must have no @cert-authority line and must list at
// least one key for the server's host:port that is not @revoked. A Pinned
// set accepts exactly those keys; never a certificate, never a CA, never a
// key on first use. It imports nothing from this module.
//
// Every message is about the file or the key line, never their content: no
// error carries key material beyond a SHA256 fingerprint.
package hostkey

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// MaxKnownHostsBytes bounds a known_hosts file.
const MaxKnownHostsBytes = 1 << 20

// Error is a problem with a pinned key line or a known_hosts file. Msg is
// the text the config loader prints after the key path; it never carries
// file content.
type Error struct {
	// Msg describes the problem, e.g. "lists no key for host:22".
	Msg string
}

// Error returns Msg.
func (e *Error) Error() string { return e.Msg }

// MismatchError means the server offered a host key that is not pinned.
type MismatchError struct {
	// Fingerprint is the SHA256 fingerprint of the key the server offered
	// ("SHA256:..."); it is the only thing about the key that is kept.
	Fingerprint string
	// Certificate is true when the server offered a certificate.
	Certificate bool
}

// Error says that the key is not pinned, with its fingerprint.
func (e *MismatchError) Error() string {
	if e.Certificate {
		return "the server offered a host certificate (" + e.Fingerprint + "); certificates are not accepted"
	}
	return "the server offered host key " + e.Fingerprint + ", which is not pinned"
}

// Pinned is the set of host keys that are accepted for one host:port.
type Pinned struct {
	keys []ssh.PublicKey
}

// ParseKey parses a pinned host key in authorized_keys form ("type base64
// [comment]"): one line, one key, no options and no certificate. Errors are
// *Error.
func ParseKey(line string) (ssh.PublicKey, error) {
	if strings.ContainsAny(line, "\r\n") {
		return nil, &Error{Msg: "must be one line"}
	}
	pk, _, opts, rest, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return nil, &Error{Msg: "does not parse as an OpenSSH public key (\"type base64 [comment]\")"}
	}
	if len(opts) > 0 {
		return nil, &Error{Msg: "must not carry authorized_keys options"}
	}
	if strings.TrimSpace(string(rest)) != "" {
		return nil, &Error{Msg: "must hold exactly one key"}
	}
	if _, isCert := pk.(*ssh.Certificate); isCert {
		return nil, &Error{Msg: "SSH certificates are not supported in this version"}
	}
	return pk, nil
}

// Fixed pins the one key in line (see ParseKey).
func Fixed(line string) (*Pinned, error) {
	pk, err := ParseKey(line)
	if err != nil {
		return nil, err
	}
	return &Pinned{keys: []ssh.PublicKey{pk}}, nil
}

// probeKey is a fixed public key that no known_hosts file lists; it is used
// to ask a known_hosts callback which keys it holds for a host.
var probeKey = func() ssh.PublicKey {
	pk, err := ssh.NewPublicKey(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public())
	if err != nil {
		panic(err)
	}
	return pk
}()

// readFile opens file once (O_NONBLOCK, so a FIFO cannot block it), checks
// that it is a regular file and reads it through the size limit. Open and
// read errors are the os errors, unwrapped by this package.
func readFile(file string) ([]byte, error) {
	f, err := os.OpenFile(file, os.O_RDONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // G304: the operator's config names the known_hosts file
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, &Error{Msg: "is not a regular file"}
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxKnownHostsBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxKnownHostsBytes {
		return nil, &Error{Msg: "is larger than 1 MiB"}
	}
	return b, nil
}

// KnownHosts reads file and pins the keys it lists for host:port that are
// not revoked. A @cert-authority line is refused (SSH certificates are not
// supported in v0.1, D18). Host patterns follow OpenSSH: hashed entries and
// wildcard patterns such as *.example.net are accepted, and a wildcard pins
// its key for every host it matches. Any outcome other than "at least one
// key is pinned" fails closed. A problem with the content is an *Error; a
// failure to open or read the file is the os error.
func KnownHosts(file, host string, port int) (*Pinned, error) {
	data, err := readFile(file)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if f := strings.Fields(line); len(f) > 0 && f[0] == "@cert-authority" {
			return nil, &Error{Msg: "holds a @cert-authority line; SSH certificates are not supported in this version"}
		}
	}
	cb, err := knownhosts.New(file)
	if err != nil {
		return nil, &Error{Msg: "does not parse as a known_hosts file"}
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	remote := &net.TCPAddr{IP: net.IPv4zero}
	var ke *knownhosts.KeyError
	if err := cb(addr, remote, probeKey); !errors.As(err, &ke) {
		return nil, &Error{Msg: "cannot be checked for " + addr}
	}
	if len(ke.Want) == 0 {
		return nil, &Error{Msg: "lists no key for " + addr}
	}
	p := &Pinned{}
	plain := 0
	for _, k := range ke.Want {
		if _, isCert := k.Key.(*ssh.Certificate); isCert {
			continue
		}
		plain++
		if cb(addr, remote, k.Key) == nil {
			p.keys = append(p.keys, k.Key)
		}
	}
	if plain == 0 {
		return nil, &Error{Msg: "lists only certificates for " + addr + "; SSH certificates are not supported in this version"}
	}
	if len(p.keys) == 0 {
		return nil, &Error{Msg: "lists only revoked keys for " + addr}
	}
	return p, nil
}

// Algorithms returns the host key algorithms that can verify a pinned key,
// for ssh.ClientConfig.HostKeyAlgorithms: the key types of the pinned keys
// (the SHA-2 signature forms for an RSA key), and never a certificate
// algorithm, so the handshake never negotiates a certificate.
func (p *Pinned) Algorithms() []string {
	var out []string
	seen := map[string]bool{}
	add := func(a string) {
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	for _, k := range p.keys {
		if k.Type() == ssh.KeyAlgoRSA {
			add(ssh.KeyAlgoRSASHA512)
			add(ssh.KeyAlgoRSASHA256)
			continue
		}
		add(k.Type())
	}
	return out
}

// Check reports whether key is pinned; if not, it returns a *MismatchError.
// A certificate is never pinned. The SSH layer calls it from its host key
// callback.
func (p *Pinned) Check(key ssh.PublicKey) error {
	if key == nil {
		return &MismatchError{Fingerprint: "none"}
	}
	if _, isCert := key.(*ssh.Certificate); isCert {
		return &MismatchError{Fingerprint: ssh.FingerprintSHA256(key), Certificate: true}
	}
	blob := key.Marshal()
	for _, k := range p.keys {
		if bytes.Equal(k.Marshal(), blob) {
			return nil
		}
	}
	return &MismatchError{Fingerprint: ssh.FingerprintSHA256(key)}
}

// String describes the set for logs: the fingerprints of the pinned keys.
func (p *Pinned) String() string {
	fps := make([]string, 0, len(p.keys))
	for _, k := range p.keys {
		fps = append(fps, ssh.FingerprintSHA256(k))
	}
	return fmt.Sprintf("pinned(%s)", strings.Join(fps, ", "))
}
