// Package sshclient is yasshm's SSH layer (spec 5.5): it runs a command on a
// target, signals it, transfers a file over SFTP and probes a server, with
// one connection per call.
//
// Trust and transport: the host key callback accepts exactly the server's
// pinned host_key, or a non-revoked plain key that the pinned known_hosts
// file lists for host:port (the rules of internal/hostkey, which the config
// loader also checks) -- never a certificate, never a CA, never a key on
// first use. The private key is read through internal/keyfile at every
// connect (type, mode and owner re-checked), parsed, and its bytes cleared.
// No PTY, no environment, no agent and no port forwarding is ever requested,
// and stdin is closed at once, so a command that reads stdin gets EOF.
//
// Tenancy: every entry point takes the caller's tenant.TenantID and the
// *tenant.Server and refuses a server of another tenant (ErrWrongTenant).
//
// Stopping a command: closing an SSH session does not stop the remote
// command (F10). A command is stopped with the SSH signal request, which
// OpenSSH delivers to the command's process group; Command.Wait does that
// when its context ends (TERM, the kill grace, KILL), and only then closes
// the connection. When no exit arrives, ExitStatus.MayStillRun says so.
//
// Errors are *Error values with one of five codes (Code); their messages
// carry the server id, never the host name, key material or command text.
//
// It imports from this module only tenant, keyfile and hostkey.
package sshclient

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/InfiniteRoomLabs/yasshm/internal/hostkey"
	"github.com/InfiniteRoomLabs/yasshm/internal/keyfile"
	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// Defaults for a zero Options field; they match the config defaults of
// spec 5.1 (connect_timeout_seconds 10, kill_grace_seconds 5).
const (
	DefaultConnectTimeout = 10 * time.Second
	DefaultKillGrace      = 5 * time.Second
	DefaultKillWait       = 2 * time.Second
)

// Options are the timings a Client uses.
type Options struct {
	// ConnectTimeout bounds the TCP dial plus the SSH handshake and
	// authentication (connect_timeout_seconds).
	ConnectTimeout time.Duration
	// KillGrace is how long Wait waits after TERM before it sends KILL
	// (kill_grace_seconds).
	KillGrace time.Duration
	// KillWait is how long Wait waits after KILL for the exit before it
	// closes the connection and reports that the command may still run.
	KillWait time.Duration
}

// OptionsFromLimits returns the Options for a tenant's limits.
func OptionsFromLimits(l tenant.Limits) Options {
	return Options{
		ConnectTimeout: time.Duration(l.ConnectTimeoutSeconds) * time.Second,
		KillGrace:      time.Duration(l.KillGraceSeconds) * time.Second,
	}
}

// Client runs SSH operations with fixed Options. It holds no connection and
// is safe for concurrent use.
type Client struct {
	opts Options
}

// New returns a Client. A zero or negative field of o takes its default.
func New(o Options) *Client {
	if o.ConnectTimeout <= 0 {
		o.ConnectTimeout = DefaultConnectTimeout
	}
	if o.KillGrace <= 0 {
		o.KillGrace = DefaultKillGrace
	}
	if o.KillWait <= 0 {
		o.KillWait = DefaultKillWait
	}
	return &Client{opts: o}
}

// Options returns the client's effective options.
func (c *Client) Options() Options { return c.opts }

// checkServer refuses a nil server, an empty tenant and a server of another
// tenant.
func checkServer(tid tenant.TenantID, srv *tenant.Server, op string) error {
	switch {
	case srv == nil:
		return newErr(CodeInternal, "", op, "no server", ErrInvalidArgument, nil)
	case tid == "" || srv.Tenant != tid:
		return newErr(CodeInternal, srv.ID, op, ErrWrongTenant.Error(), ErrWrongTenant, nil)
	}
	return nil
}

// pin builds the accepted host key set for srv. A problem with the pinned
// key or the known_hosts content is CodeHostKeyMismatch (no key can be
// accepted, so the target is not spoken to); a known_hosts file that cannot
// be read is CodeInternal.
func pin(srv *tenant.Server, port int, op string) (*hostkey.Pinned, error) {
	var (
		p   *hostkey.Pinned
		err error
	)
	switch {
	case srv.HostKey != "" && srv.KnownHostsFile != "":
		return nil, newErr(CodeInternal, srv.ID, op, "the server pins both a host key and a known_hosts file", ErrInvalidArgument, nil)
	case srv.HostKey != "":
		p, err = hostkey.Fixed(srv.HostKey)
	case srv.KnownHostsFile != "":
		p, err = hostkey.KnownHosts(srv.KnownHostsFile, srv.Host, port)
	default:
		return nil, newErr(CodeHostKeyMismatch, srv.ID, op, "the server pins no host key", nil, nil)
	}
	if err != nil {
		var he *hostkey.Error
		if errors.As(err, &he) {
			return nil, newErr(CodeHostKeyMismatch, srv.ID, op, "the pinned host key cannot be used: "+he.Msg, nil, err)
		}
		return nil, newErr(CodeInternal, srv.ID, op, "the known_hosts file cannot be read", nil, err)
	}
	return p, nil
}

// loadSigner reads the identity's private key through keyfile (which
// re-checks the file), parses it and clears the bytes. No message carries
// the path or the key.
func loadSigner(file string, server tenant.ServerID, op string) (ssh.Signer, error) {
	b, err := keyfile.Read(file)
	if err != nil {
		msg := "the identity's private key file cannot be read"
		var me *keyfile.ModeError
		var oe *keyfile.OwnerError
		if errors.Is(err, keyfile.ErrNotRegular) || errors.Is(err, keyfile.ErrTooLarge) || errors.As(err, &me) || errors.As(err, &oe) {
			msg = "the identity's private key file " + err.Error()
		}
		return nil, newErr(CodeInternal, server, op, msg, nil, nil)
	}
	defer clear(b)
	sg, err := ssh.ParsePrivateKey(b)
	if err != nil {
		return nil, newErr(CodeInternal, server, op, "the identity's private key does not parse", nil, nil)
	}
	return sg, nil
}

// dial opens one authenticated SSH connection to srv for an operation that
// needs level. The connect timeout covers the dial, the handshake and
// authentication; ctx can end it earlier.
func (c *Client) dial(ctx context.Context, tid tenant.TenantID, srv *tenant.Server, level tenant.AccessLevel, op string) (*ssh.Client, error) {
	if err := checkServer(tid, srv, op); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, ctxErr(srv.ID, op, err)
	}
	port := srv.Port
	if port == 0 {
		port = 22
	}
	pinned, err := pin(srv, port, op)
	if err != nil {
		return nil, err
	}
	id := srv.IdentityFor(level)
	signer, err := loadSigner(id.PrivateKeyFile, srv.ID, op)
	if err != nil {
		return nil, err
	}
	addr := net.JoinHostPort(srv.Host, strconv.Itoa(port))
	deadline := time.Now().Add(c.opts.ConnectTimeout)
	dctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	var d net.Dialer
	nc, err := d.DialContext(dctx, "tcp", addr)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, ctxErr(srv.ID, op, cerr)
		}
		if pastDeadline(ctx) {
			return nil, ctxErr(srv.ID, op, context.DeadlineExceeded)
		}
		return nil, newErr(CodeUnreachable, srv.ID, op, "the TCP connection failed", nil, err)
	}
	// The handshake has no context; closing the conn is how ctx ends it.
	stop := context.AfterFunc(dctx, func() { _ = nc.Close() })
	if dl, ok := dctx.Deadline(); ok {
		_ = nc.SetDeadline(dl)
	}
	var mismatch *hostkey.MismatchError
	cfg := &ssh.ClientConfig{
		User: id.User,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			cerr := pinned.Check(key)
			if cerr != nil {
				errors.As(cerr, &mismatch)
			}
			return cerr
		},
		HostKeyAlgorithms: pinned.Algorithms(),
	}
	sc, chans, reqs, err := ssh.NewClientConn(nc, addr, cfg)
	stopped := stop()
	if err == nil && !stopped {
		_ = sc.Close()
		err = errors.New("the context ended during the handshake")
	}
	if err != nil {
		_ = nc.Close()
		return nil, handshakeErr(ctx, srv.ID, op, err, mismatch)
	}
	_ = nc.SetDeadline(time.Time{})
	return ssh.NewClient(sc, chans, reqs), nil
}

// handshakeErr classifies a failed handshake.
func handshakeErr(ctx context.Context, server tenant.ServerID, op string, err error, mismatch *hostkey.MismatchError) error {
	switch {
	case mismatch != nil:
		e := newErr(CodeHostKeyMismatch, server, op, mismatch.Error(), nil, err)
		e.fingerprint = mismatch.Fingerprint
		return e
	case ctx.Err() != nil:
		return ctxErr(server, op, ctx.Err())
	case pastDeadline(ctx):
		// The conn deadline, set to the caller's deadline, can fire a moment
		// before the context's own timer marks it done.
		return ctxErr(server, op, context.DeadlineExceeded)
	case strings.Contains(err.Error(), "ssh: unable to authenticate"):
		// x/crypto has no typed client-side auth error; this text is pinned
		// by a test against both servers.
		return newErr(CodeAuthFailed, server, op, "the server refused the identity's key", nil, err)
	default:
		return newErr(CodeUnreachable, server, op, "the SSH handshake failed", nil, err)
	}
}

// pastDeadline reports whether ctx has a deadline that has passed.
func pastDeadline(ctx context.Context) bool {
	d, ok := ctx.Deadline()
	return ok && !time.Now().Before(d)
}

// signalNames are the signal names OpenSSH accepts in a signal request
// (F10), without the SIG prefix.
var signalNames = map[string]bool{"TERM": true, "KILL": true, "INT": true, "HUP": true, "QUIT": true, "USR1": true, "USR2": true}

// ValidSignal reports whether name is a signal name this package sends:
// TERM, KILL, INT, HUP, QUIT, USR1 or USR2, without the SIG prefix.
func ValidSignal(name string) bool { return signalNames[name] }

// trimSig removes a SIG prefix from a signal name a server sent.
func trimSig(name string) string { return strings.TrimPrefix(name, "SIG") }
