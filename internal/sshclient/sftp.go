package sshclient

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// posixRename is the OpenSSH SFTP extension for a rename that replaces the
// target atomically.
const posixRename = "posix-rename@openssh.com"

// sftpSession is one connection with its SFTP client; ctx ending closes it.
type sftpSession struct {
	conn *ssh.Client
	sc   *sftp.Client
	stop func() bool
}

func (s *sftpSession) close() {
	s.stop()
	_ = s.sc.Close()
	_ = s.conn.Close()
}

// openSFTP connects and starts the SFTP subsystem.
func (c *Client) openSFTP(ctx context.Context, tid tenant.TenantID, srv *tenant.Server, level tenant.AccessLevel, op string) (*sftpSession, error) {
	conn, err := c.dial(ctx, tid, srv, level, op)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	sc, err := sftp.NewClient(conn)
	if err != nil {
		stop()
		_ = conn.Close()
		if cerr := ctx.Err(); cerr != nil {
			return nil, ctxErr(srv.ID, op, cerr)
		}
		return nil, newErr(CodeInternal, srv.ID, op, "the server did not start the SFTP subsystem", nil, err)
	}
	return &sftpSession{conn: conn, sc: sc, stop: stop}, nil
}

// fileErr maps an SFTP error (nil stays nil). The cause keeps fs.ErrNotExist,
// fs.ErrPermission and fs.ErrExist matchable; the message names no path.
func fileErr(ctx context.Context, server tenant.ServerID, op, what string, err error) error {
	if err == nil {
		return nil
	}
	if cerr := ctx.Err(); cerr != nil {
		return ctxErr(server, op, cerr)
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	msg := what + " failed"
	switch {
	case errors.Is(err, fs.ErrNotExist):
		msg = what + ": no such file"
	case errors.Is(err, fs.ErrPermission):
		msg = what + ": permission denied"
	case errors.Is(err, fs.ErrExist):
		msg = what + ": the file exists"
	}
	return newErr(CodeInternal, server, op, msg, nil, err)
}

// Download reads at most max bytes of the regular file at path on srv,
// starting at offset, over SFTP. It returns the bytes, the file's size when
// it was opened, and whether the read reached that size. It never reads
// more than max bytes, whatever the file's size or growth. The path is
// stat'ed (following a symbolic link) before it is opened, so a FIFO, a
// device or a directory is refused with ErrNotRegular without being opened;
// the open file is checked again. A file that reports a size of zero or
// less (procfs, sysfs) is read until EOF, at most max bytes; size is then
// offset plus the bytes read, and eof says whether EOF was reached. A
// missing file matches fs.ErrNotExist.
//
// SFTP calls are bounded only by ctx: a caller must pass a deadline.
func (c *Client) Download(ctx context.Context, tid tenant.TenantID, srv *tenant.Server, remotePath string, offset, maxBytes int64) (data []byte, size int64, eof bool, err error) {
	const op = "download"
	if cerr := checkServer(tid, srv, op); cerr != nil {
		return nil, 0, false, cerr
	}
	if remotePath == "" || offset < 0 || maxBytes < 0 {
		return nil, 0, false, newErr(CodeInternal, srv.ID, op, "the path must be set and offset and max must not be negative", ErrInvalidArgument, nil)
	}
	s, err := c.openSFTP(ctx, tid, srv, tenant.AccessLook, op)
	if err != nil {
		return nil, 0, false, err
	}
	defer s.close()
	notRegular := newErr(CodeInternal, srv.ID, op, "the path "+ErrNotRegular.Error(), ErrNotRegular, nil)
	// Stat first: opening a FIFO blocks the server and opening a device can
	// have side effects.
	pre, err := s.sc.Stat(remotePath)
	if err != nil {
		return nil, 0, false, fileErr(ctx, srv.ID, op, "stat", err)
	}
	if !pre.Mode().IsRegular() {
		return nil, 0, false, notRegular
	}
	f, err := s.sc.Open(remotePath)
	if err != nil {
		return nil, 0, false, fileErr(ctx, srv.ID, op, "open", err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, 0, false, fileErr(ctx, srv.ID, op, "stat", err)
	}
	if !fi.Mode().IsRegular() {
		return nil, 0, false, notRegular
	}
	size = fi.Size()
	sized := size > 0
	n := maxBytes
	if sized {
		if offset >= size {
			return []byte{}, size, true, nil
		}
		n = min(maxBytes, size-offset)
	}
	buf := make([]byte, n)
	k, err := io.ReadFull(io.NewSectionReader(f, offset, n), buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, 0, false, fileErr(ctx, srv.ID, op, "read", err)
	}
	if cerr := ctx.Err(); cerr != nil {
		return nil, 0, false, ctxErr(srv.ID, op, cerr)
	}
	if !sized {
		return buf[:k], offset + int64(k), k < len(buf), nil
	}
	return buf[:k], size, offset+int64(k) >= size || k < len(buf), nil
}

// Upload writes data to path on srv over SFTP with permission bits mode
// (at most 0777). Without overwrite it creates the file exclusively
// (O_CREATE|O_EXCL) and an existing file matches fs.ErrExist. With
// overwrite it writes a temporary file in the same directory and renames
// it over path when the server supports posix-rename (readers never see a
// partial file; a symbolic link at path is replaced, not followed), and
// otherwise truncates and writes path directly. When a write fails on a
// working connection, the file it created is removed; when the connection
// is gone (ctx ended, link lost) the removal cannot run either, and a
// partial file (at path without overwrite, or the hidden temporary file)
// stays. No v0.1 tool reaches it (spec D5); it refuses a server that does
// not grant tenant.AccessFull (ErrAccessLevel) and connects with
// IdentityFor(full).
//
// SFTP calls are bounded only by ctx: a caller must pass a deadline.
func (c *Client) Upload(ctx context.Context, tid tenant.TenantID, srv *tenant.Server, remotePath string, data []byte, mode fs.FileMode, overwrite bool) error {
	const op = "upload"
	if err := checkServer(tid, srv, op); err != nil {
		return err
	}
	if remotePath == "" || mode&^fs.ModePerm != 0 {
		return newErr(CodeInternal, srv.ID, op, "the path must be set and the mode must be permission bits only", ErrInvalidArgument, nil)
	}
	s, err := c.openSFTP(ctx, tid, srv, tenant.AccessFull, op)
	if err != nil {
		return err
	}
	defer s.close()
	if !overwrite {
		return fileErr(ctx, srv.ID, op, "create", writeNew(s.sc, remotePath, data, mode))
	}
	if _, ok := s.sc.HasExtension(posixRename); ok {
		tmp := tempName(remotePath)
		if werr := writeNew(s.sc, tmp, data, mode); werr != nil {
			return fileErr(ctx, srv.ID, op, "create", werr)
		}
		if rerr := s.sc.PosixRename(tmp, remotePath); rerr != nil {
			_ = s.sc.Remove(tmp)
			return fileErr(ctx, srv.ID, op, "rename", rerr)
		}
		return nil
	}
	f, err := s.sc.OpenFile(remotePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return fileErr(ctx, srv.ID, op, "open", err)
	}
	return fileErr(ctx, srv.ID, op, "write", fill(f, data, mode))
}

// writeNew creates path exclusively, sets mode, writes data and closes it;
// on failure it removes what it created.
func writeNew(sc *sftp.Client, p string, data []byte, mode fs.FileMode) error {
	f, err := sc.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		// SFTP v3 has no "file exists" status, so OpenSSH answers a failed
		// exclusive create with a generic failure; look before naming it.
		if !errors.Is(err, fs.ErrExist) {
			if _, serr := sc.Lstat(p); serr == nil {
				return &fs.PathError{Op: "create", Path: p, Err: fs.ErrExist}
			}
		}
		return err
	}
	if err := fill(f, data, mode); err != nil {
		_ = sc.Remove(p)
		return err
	}
	return nil
}

// fill sets the mode first (so the content is never readable with wider
// bits), writes data and closes f. It returns nil only when all three
// succeeded.
func fill(f *sftp.File, data []byte, mode fs.FileMode) error {
	err := f.Chmod(mode)
	if err == nil {
		_, err = f.Write(data)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// tempName returns a random hidden name next to p. crypto/rand.Read never
// fails (since Go 1.24 it aborts the process instead).
func tempName(p string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return path.Join(path.Dir(p), "."+path.Base(p)+".yasshm-"+hex.EncodeToString(b[:]))
}
