package jobs

import (
	"errors"

	"github.com/InfiniteRoomLabs/yasshm/internal/sshclient"
)

// Code is a stable error code the tools map into a tool result (spec 5.8).
// A Code is an error, so errors.Is(err, CodeBusy) matches any *Error with
// that code.
type Code string

// The engine's own codes. The SSH codes of sshclient (ssh_unreachable,
// ssh_auth_failed, host_key_mismatch, timeout, internal) are passed through
// unchanged.
const (
	// CodeInvalidArgument: an argument is malformed or out of range.
	CodeInvalidArgument Code = "invalid_argument"
	// CodeNotFound: no such server or command in the caller's tenant.
	CodeNotFound Code = "not_found"
	// CodeBusy: a concurrency limit is reached (Limit names it).
	CodeBusy Code = "busy"
	// CodeInternal: anything else.
	CodeInternal Code = Code(sshclient.CodeInternal)
)

// Error returns the code itself.
func (c Code) Error() string { return string(c) }

// The names of the two concurrency limits, as Error.Limit reports them.
const (
	LimitPerTenant = "max_concurrent_commands"
	LimitPerServer = "max_concurrent_per_server"
)

// Sentinels an *Error can also match with errors.Is.
var (
	// ErrShuttingDown: the engine is shutting down or has shut down and
	// accepts no new work (code internal).
	ErrShuttingDown = errors.New("the job engine is shutting down")
	// ErrAuditFailed: the audit event that must come before the action
	// could not be written, so the action did not happen (code internal,
	// spec D12).
	ErrAuditFailed = errors.New("the audit event could not be written")
)

// Error is every error the engine returns. Its message is fixed text: it
// never names a host, a user, a key path, a file path or another tenant's
// ids, so it may reach a tool result. The cause is reachable with Unwrap for
// operator logs.
type Error struct {
	code     Code
	msg      string
	limit    string
	sentinel error
	cause    error
}

func newError(code Code, msg string) *Error { return &Error{code: code, msg: msg} }

// Code returns the error's code.
func (e *Error) Code() Code { return e.code }

// Limit names the limit for CodeBusy (LimitPerTenant or LimitPerServer).
func (e *Error) Limit() string { return e.limit }

// Error renders "jobs: <code>: <message>".
func (e *Error) Error() string {
	s := "jobs: " + string(e.code)
	if e.msg != "" {
		s += ": " + e.msg
	}
	return s
}

// Unwrap returns the cause (an sshclient error, a store error), which can
// name the host: operator logs only.
func (e *Error) Unwrap() error { return e.cause }

// Is matches the error's code and its sentinel.
func (e *Error) Is(target error) bool {
	if c, ok := target.(Code); ok {
		return c == e.code
	}
	return e.sentinel != nil && target == e.sentinel
}

// CodeOf returns the code of err: an engine *Error's code, an sshclient
// error's code, CodeInternal for any other error, "" for nil.
func CodeOf(err error) Code {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.code
	}
	var se *sshclient.Error
	if errors.As(err, &se) {
		return Code(se.Code())
	}
	return CodeInternal
}

func invalid(msg string) *Error  { return newError(CodeInvalidArgument, msg) }
func notFound(msg string) *Error { return newError(CodeNotFound, msg) }

func internal(msg string, cause error) *Error {
	return &Error{code: CodeInternal, msg: msg, cause: cause}
}

func busy(limit string) *Error {
	return &Error{code: CodeBusy, msg: "the limit " + limit + " is reached; try again when a command ends", limit: limit}
}

func shuttingDown() *Error {
	return &Error{code: CodeInternal, msg: "the gateway is shutting down", sentinel: ErrShuttingDown}
}

func auditFailed(cause error) *Error {
	return &Error{code: CodeInternal, msg: "the audit event could not be written, so nothing was done", sentinel: ErrAuditFailed, cause: cause}
}

// sshError wraps an SSH layer error with its own code and fixed text.
func sshError(err error) *Error {
	code := Code(sshclient.CodeOf(err))
	return &Error{code: code, msg: "the SSH operation failed", cause: err}
}
