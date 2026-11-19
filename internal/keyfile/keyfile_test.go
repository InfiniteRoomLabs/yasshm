package keyfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func write(t *testing.T, data []byte, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRead(t *testing.T) {
	t.Run("happy/owner-only file is read", func(t *testing.T) {
		b, err := Read(write(t, []byte("key bytes"), 0o600))
		if err != nil || string(b) != "key bytes" {
			t.Fatalf("got %q, %v", b, err)
		}
	})
	t.Run("edge/exactly MaxBytes", func(t *testing.T) {
		if b, err := Read(write(t, make([]byte, MaxBytes), 0o600)); err != nil || len(b) != MaxBytes {
			t.Fatalf("len %d, %v", len(b), err)
		}
	})
	t.Run("sad/one byte over MaxBytes", func(t *testing.T) {
		if _, err := Read(write(t, make([]byte, MaxBytes+1), 0o600)); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("err = %v", err)
		}
	})
	for _, mode := range []os.FileMode{0o640, 0o604, 0o620, 0o601, 0o644} {
		t.Run("sad/mode "+mode.String(), func(t *testing.T) {
			_, err := Read(write(t, []byte("x"), mode))
			var me *ModeError
			if !errors.As(err, &me) || me.Perm != mode {
				t.Fatalf("err = %v", err)
			}
		})
	}
	t.Run("sad/missing", func(t *testing.T) {
		if _, err := Read(filepath.Join(t.TempDir(), "nope")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("sad/a directory", func(t *testing.T) {
		if _, err := Read(t.TempDir()); !errors.Is(err, ErrNotRegular) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("raging/a FIFO does not block and is refused", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "fifo")
		if err := syscall.Mkfifo(p, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(p); !errors.Is(err, ErrNotRegular) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("sad/owned by another uid", func(t *testing.T) {
		orig := euid
		t.Cleanup(func() { euid = orig })
		euid = func() int { return os.Geteuid() + 1 }
		_, err := Read(write(t, []byte("x"), 0o600))
		var oe *OwnerError
		if !errors.As(err, &oe) || oe.UID != os.Geteuid() || oe.Want != os.Geteuid()+1 {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("corner/a symbolic link is checked on its target", func(t *testing.T) {
		target := write(t, []byte("x"), 0o644)
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		var me *ModeError
		if _, err := Read(link); !errors.As(err, &me) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("happy/error texts", func(t *testing.T) {
		if (&ModeError{Perm: 0o640}).Error() != "is accessible by group or other (mode 0640); it must be 0600 or stricter" ||
			(&OwnerError{UID: 1, Want: 2}).Error() != "is owned by uid 1, not by the gateway's uid 2" {
			t.Fatal("unexpected error text")
		}
	})
}
