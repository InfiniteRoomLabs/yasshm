// Package keyfile reads an SSH private key file safely: it opens the path
// once, checks the open file (a regular file, no permission bit for group or
// other, owned by the effective uid) and reads it through a size limit. The
// config loader uses it to validate a key, and the SSH layer uses it again
// when it reads the key to connect, so the checks hold at use and not only
// at start. It imports nothing from this module.
//
// The checks read mode bits and ownership, never "can I open it", so they
// hold when the gateway runs as root. The caller owns the returned bytes and
// should clear them when done.
package keyfile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

// MaxBytes bounds a private key file; real keys are a few KiB.
const MaxBytes = 64 << 10

// ErrNotRegular means the path is not a regular file.
var ErrNotRegular = errors.New("is not a regular file")

// ErrTooLarge means the file is larger than MaxBytes.
var ErrTooLarge = errors.New("is too large to be a private key")

// ModeError means the file's mode gives group or other a permission bit.
type ModeError struct {
	// Perm is the file's permission bits.
	Perm os.FileMode
}

// Error describes the mode problem.
func (e *ModeError) Error() string {
	return fmt.Sprintf("is accessible by group or other (mode %04o); it must be 0600 or stricter", uint32(e.Perm))
}

// OwnerError means the file is not owned by the effective uid.
type OwnerError struct {
	// UID owns the file; Want is the effective uid.
	UID, Want int
}

// Error describes the owner problem.
func (e *OwnerError) Error() string {
	return fmt.Sprintf("is owned by uid %d, not by the gateway's uid %d", e.UID, e.Want)
}

// euid is the effective uid the owner must match; a variable so tests can
// check the mismatch without root.
var euid = os.Geteuid

// Read opens path once and returns its content after the checks. Errors from
// opening the file wrap the os error (errors.Is works with fs.ErrNotExist and
// fs.ErrPermission); the check errors are ErrNotRegular, ErrTooLarge,
// *ModeError and *OwnerError. No error carries file content.
func Read(path string) ([]byte, error) {
	// O_NONBLOCK keeps a FIFO from blocking the open; it changes nothing for
	// a regular file.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // G304: the operator's config names the key file
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, ErrNotRegular
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return nil, &ModeError{Perm: perm}
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok {
		return nil, errors.New("cannot read the file's owner")
	} else if want := euid(); int(st.Uid) != want {
		return nil, &OwnerError{UID: int(st.Uid), Want: want}
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil {
		clear(b)
		return nil, err
	}
	if len(b) > MaxBytes {
		clear(b)
		return nil, ErrTooLarge
	}
	return b, nil
}
