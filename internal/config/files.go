package config

import (
	"crypto/ed25519"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/InfiniteRoomLabs/yasshm/internal/keyfile"
)

// maxKnownHostsBytes bounds a known_hosts file.
const maxKnownHostsBytes = 1 << 20

// checkFiles runs the checks that need the file system.
func checkFiles(refs fileRefs) []Problem {
	var probs []Problem
	for _, r := range refs.privateKeys {
		if msg := checkPrivateKey(r.file); msg != "" {
			probs = append(probs, Problem{Path: r.path, Line: r.line, Message: msg})
		}
	}
	for _, r := range refs.knownHosts {
		if msg := checkKnownHosts(r.file, r.host, r.port); msg != "" {
			probs = append(probs, Problem{Path: r.path, Line: r.line, Message: msg})
		}
	}
	return probs
}

// fileProblem turns an error from opening or checking a file into a message
// that never carries file content.
func fileProblem(err error) string {
	var me *keyfile.ModeError
	var oe *keyfile.OwnerError
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "file does not exist"
	case errors.Is(err, fs.ErrPermission):
		return "file cannot be read (permission denied)"
	case errors.Is(err, keyfile.ErrNotRegular), errors.Is(err, keyfile.ErrTooLarge), errors.As(err, &me), errors.As(err, &oe):
		return err.Error()
	default:
		return "file cannot be read"
	}
}

// checkPrivateKey checks a private key file through keyfile.Read (opened
// once; a regular file, no permission bit for group or other, owned by the
// effective uid, at most keyfile.MaxBytes) and that it holds an unencrypted
// key that parses. The key bytes are cleared after parsing and never leave
// this function; no message carries them.
func checkPrivateKey(file string) string {
	b, err := keyfile.Read(file)
	if err != nil {
		return fileProblem(err)
	}
	defer clear(b)
	if _, err := ssh.ParsePrivateKey(b); err != nil {
		var pm *ssh.PassphraseMissingError
		if errors.As(err, &pm) {
			return "is protected by a passphrase; yasshm needs a key without one"
		}
		return "does not parse as a private key"
	}
	return ""
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

// readKnownHosts reads a regular file through a size limit.
func readKnownHosts(file string) (data []byte, problem string) {
	f, err := os.Open(file) //nolint:gosec // G304: the operator's config names the known_hosts file
	if err != nil {
		return nil, fileProblem(err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, fileProblem(err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fileProblem(keyfile.ErrNotRegular)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxKnownHostsBytes+1))
	if err != nil {
		return nil, fileProblem(err)
	}
	if len(b) > maxKnownHostsBytes {
		return nil, "is larger than 1 MiB"
	}
	return b, ""
}

// checkKnownHosts checks that file is a known_hosts file that pins at least
// one key for host:port that is not revoked. A @cert-authority line is
// refused (SSH certificates are not supported in v0.1, D18). Host patterns
// follow OpenSSH: hashed entries and wildcard patterns such as
// *.example.net are accepted, and a wildcard pins its key for every host it
// matches. Any outcome of the check other than "this key is pinned" fails
// closed.
func checkKnownHosts(file, host string, port int) string {
	data, msg := readKnownHosts(file)
	if msg != "" {
		return msg
	}
	for _, line := range strings.Split(string(data), "\n") {
		if f := strings.Fields(line); len(f) > 0 && f[0] == "@cert-authority" {
			return "holds a @cert-authority line; SSH certificates are not supported in this version"
		}
	}
	cb, err := knownhosts.New(file)
	if err != nil {
		return "does not parse as a known_hosts file"
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	remote := &net.TCPAddr{IP: net.IPv4zero}
	var ke *knownhosts.KeyError
	if err := cb(addr, remote, probeKey); !errors.As(err, &ke) {
		return "cannot be checked for " + addr
	}
	if len(ke.Want) == 0 {
		return "lists no key for " + addr
	}
	for _, k := range ke.Want {
		if cb(addr, remote, k.Key) == nil {
			return ""
		}
	}
	return "lists only revoked keys for " + addr
}
