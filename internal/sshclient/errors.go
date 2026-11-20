package sshclient

import (
	"context"
	"errors"

	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// Code is one of the five SSH error codes of spec 5.5. A Code is an error,
// so errors.Is(err, CodeTimeout) matches any *Error with that code.
type Code string

// The error codes (spec 5.5 and 5.8).
const (
	// CodeUnreachable: the TCP dial or the SSH handshake failed or timed
	// out, or the connection was lost.
	CodeUnreachable Code = "ssh_unreachable"
	// CodeAuthFailed: the server refused the identity's key.
	CodeAuthFailed Code = "ssh_auth_failed"
	// CodeHostKeyMismatch: the server's host key is not pinned, or no key
	// can be pinned for it.
	CodeHostKeyMismatch Code = "host_key_mismatch"
	// CodeTimeout: the caller's deadline passed.
	CodeTimeout Code = "timeout"
	// CodeInternal: anything else.
	CodeInternal Code = "internal"
)

// Error returns the code itself.
func (c Code) Error() string { return string(c) }

// Sentinels that an *Error can also match with errors.Is. Their code is
// CodeInternal unless documented otherwise.
var (
	// ErrWrongTenant: the server does not belong to the tenant the caller
	// named (defense in depth; the policy engine checks it first).
	ErrWrongTenant = errors.New("the server does not belong to the tenant")
	// ErrSignalRefused: the server answered the signal request with
	// failure. The session is closed, and the command may still be running.
	ErrSignalRefused = errors.New("the server refused the signal request")
	// ErrUnknownSignal: the signal name is not one OpenSSH accepts.
	ErrUnknownSignal = errors.New("the signal name is not supported")
	// ErrNotRunning: the command has already ended or its session is closed.
	ErrNotRunning = errors.New("the command is not running")
	// ErrNotRegular: the remote path is not a regular file.
	ErrNotRegular = errors.New("is not a regular file")
	// ErrAccessLevel: the server does not grant the access level the
	// operation needs (Upload needs full).
	ErrAccessLevel = errors.New("the server does not grant the access level this operation needs")
	// ErrInvalidArgument: an argument is out of range (a negative offset, a
	// mode with bits outside 0777, an empty path).
	ErrInvalidArgument = errors.New("invalid argument")
)

// Error is every error this package returns. Its message is fixed text
// that names the operation and the server id: never the host name or port,
// the login user, a uid or file mode, a key or key path, a gateway path or
// the command text, so it may reach a tool result. The cause, which may name
// the host and paths, is reachable only through Unwrap and Detail, for
// operator logs.
type Error struct {
	code        Code
	server      tenant.ServerID
	op          string
	msg         string
	fingerprint string
	kind        error
	cause       error
	// mayRun: the exec request may have reached the server (Start gave up
	// on a server that stopped answering); Exec reports MayStillRun.
	mayRun bool
}

// Error renders "sshclient: <op> on server <id>: <code>[: <message>]".
func (e *Error) Error() string {
	s := "sshclient: " + e.op + " on server " + string(e.server) + ": " + string(e.code)
	if e.msg != "" {
		s += ": " + e.msg
	}
	return s
}

// Code returns the error's code.
func (e *Error) Code() Code { return e.code }

// MayStillRun reports whether the command may be running on the target
// although the operation failed: Start gave up on a server that stopped
// answering after the exec request was sent. The job engine records it.
func (e *Error) MayStillRun() bool { return e.mayRun }

// Server returns the id of the server the operation was for.
func (e *Error) Server() tenant.ServerID { return e.server }

// Fingerprint returns the SHA256 fingerprint of the host key the server
// offered, for CodeHostKeyMismatch; otherwise "".
func (e *Error) Fingerprint() string { return e.fingerprint }

// Is matches the error's Code and its sentinel, if it has one.
func (e *Error) Is(target error) bool {
	if c, ok := target.(Code); ok {
		return c == e.code
	}
	return e.kind != nil && target == e.kind
}

// Unwrap returns the underlying cause (for errors.Is with fs.ErrNotExist,
// context.DeadlineExceeded and the like). Its text may name the host.
func (e *Error) Unwrap() error { return e.cause }

// Detail returns the error with its cause, for operator logs only: the
// cause can name the host and port (spec D8).
func (e *Error) Detail() string {
	if e.cause == nil {
		return e.Error()
	}
	return e.Error() + " (" + e.cause.Error() + ")"
}

// CodeOf returns the code of err: the *Error's code, or CodeInternal for
// any other non-nil error, or "" for nil.
func CodeOf(err error) Code {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.code
	}
	return CodeInternal
}

// newErr builds an *Error.
func newErr(code Code, server tenant.ServerID, op, msg string, kind, cause error) *Error {
	return &Error{code: code, server: server, op: op, msg: msg, kind: kind, cause: cause}
}

// ctxErr maps a context error: a passed deadline is CodeTimeout, a
// cancellation is CodeInternal matching context.Canceled.
func ctxErr(server tenant.ServerID, op string, err error) *Error {
	if errors.Is(err, context.DeadlineExceeded) {
		return newErr(CodeTimeout, server, op, "the deadline passed", nil, err)
	}
	return newErr(CodeInternal, server, op, "cancelled", nil, err)
}
