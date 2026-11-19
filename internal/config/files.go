package config

import (
	"errors"
	"io/fs"

	"golang.org/x/crypto/ssh"

	"github.com/InfiniteRoomLabs/yasshm/internal/hostkey"
	"github.com/InfiniteRoomLabs/yasshm/internal/keyfile"
)

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

// checkKnownHosts checks that file is a known_hosts file that pins at least
// one key for host:port that is not revoked, by the rules in
// internal/hostkey that the SSH layer enforces again at connect.
func checkKnownHosts(file, host string, port int) string {
	if _, err := hostkey.KnownHosts(file, host, port); err != nil {
		var he *hostkey.Error
		if errors.As(err, &he) {
			return he.Msg
		}
		return fileProblem(err)
	}
	return ""
}
