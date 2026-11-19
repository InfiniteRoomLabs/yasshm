package config

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"strconv"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// maxKeyFileBytes bounds a private key file; real keys are a few KiB.
const maxKeyFileBytes = 64 << 10

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

// statRegular stats file and describes why it cannot be used, or returns its
// info.
func statRegular(file string) (fi fs.FileInfo, problem string) {
	fi, err := os.Stat(file)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, "file does not exist"
	case errors.Is(err, fs.ErrPermission):
		return nil, "file cannot be read (permission denied)"
	case err != nil:
		return nil, "file cannot be read"
	case !fi.Mode().IsRegular():
		return nil, "is not a regular file"
	}
	return fi, ""
}

// checkPrivateKey checks a private key file: a regular file, no permission
// bits for group or other (checked on the mode bits, not by trying to open
// it, so the check holds when the gateway runs as root), and an unencrypted
// key that parses. The key bytes are cleared after parsing and never leave
// this function; no message carries them.
func checkPrivateKey(file string) string {
	fi, msg := statRegular(file)
	if msg != "" {
		return msg
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Sprintf("is accessible by group or other (mode %04o); it must be 0600 or stricter", perm)
	}
	if fi.Size() > maxKeyFileBytes {
		return "is too large to be a private key"
	}
	b, err := os.ReadFile(file) //nolint:gosec // G304: the operator's config names the key file
	if err != nil {
		return "file cannot be read"
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

// checkKnownHosts checks that file is a known_hosts file that lists at least
// one key for host:port.
func checkKnownHosts(file, host string, port int) string {
	if _, msg := statRegular(file); msg != "" {
		return msg
	}
	cb, err := knownhosts.New(file)
	if err != nil {
		return "does not parse as a known_hosts file"
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	err = cb(addr, &net.TCPAddr{IP: net.IPv4zero}, probeKey)
	var ke *knownhosts.KeyError
	if errors.As(err, &ke) && len(ke.Want) == 0 {
		return "lists no key for " + addr
	}
	return ""
}
