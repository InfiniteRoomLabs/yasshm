package sshclient

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// Outcome says how a command's session ended.
type Outcome uint8

// The outcomes. The zero value means "not known yet".
const (
	// OutcomeExited: the server sent exit-status; Code holds it.
	OutcomeExited Outcome = iota + 1
	// OutcomeSignaled: the server sent exit-signal; Signal names it.
	OutcomeSignaled
	// OutcomeNoStatus: the session ended without exit-status or
	// exit-signal (the connection was lost or closed, or the server sent
	// neither). The command may still be running.
	OutcomeNoStatus
)

// String returns "exited", "signaled", "no_status" or "unknown".
func (o Outcome) String() string {
	switch o {
	case OutcomeExited:
		return "exited"
	case OutcomeSignaled:
		return "signaled"
	case OutcomeNoStatus:
		return "no_status"
	default:
		return "unknown"
	}
}

// StopReason says whether the client stopped the command, and why.
type StopReason uint8

// The stop reasons.
const (
	// StopNone: the command ended on its own (or by a Signal call).
	StopNone StopReason = iota
	// StopDeadline: the Wait context's deadline passed.
	StopDeadline
	// StopCancel: the Wait context was cancelled.
	StopCancel
)

// String returns "none", "deadline" or "cancel".
func (s StopReason) String() string {
	switch s {
	case StopDeadline:
		return "deadline"
	case StopCancel:
		return "cancel"
	default:
		return "none"
	}
}

// ExitStatus is how a command ended.
type ExitStatus struct {
	// Outcome says which of the fields below is meaningful.
	Outcome Outcome
	// Code is the exit code, for OutcomeExited.
	Code int
	// Signal is the signal that ended the command, without the SIG prefix
	// ("TERM"), for OutcomeSignaled.
	Signal string
	// Stopped says whether Wait stopped the command because its context
	// ended.
	Stopped StopReason
	// TermSent and KillSent report which signals the stop sequence sent
	// and the server accepted.
	TermSent, KillSent bool
	// MayStillRun is true when the client cannot tell that the remote
	// command stopped: no exit arrived (after KILL, or at all), or a
	// signal was refused and the session closed. Closing a session does
	// not stop a command (F10), so the job engine records this.
	MayStillRun bool
}

// Command is a running remote command and its connection. Wait, Signal and
// Close are safe to call concurrently and more than once.
//
// A handle is bound to the tenant that started it (Tenant()) and its
// methods take no tenant: whoever holds the handle can wait for, signal or
// close it. The job engine must keep handles keyed by cmd.Tenant() and the
// command id, and look one up only with the caller's tenant (spec 5.2).
type Command struct {
	tenant tenant.TenantID
	server tenant.ServerID
	opts   Options
	client *ssh.Client
	sess   *ssh.Session

	exited  chan struct{} // closed when sess.Wait returned
	waitErr error         // set before exited is closed

	mu          sync.Mutex
	stopCause   error
	termSent    bool
	killSent    bool
	mayStillRun bool
	closed      bool

	stopOnce   sync.Once
	finishOnce sync.Once
	status     ExitStatus
	err        error
}

// Start connects to srv, starts command exactly as given (the remote login
// shell interprets it) and returns its handle. Both outputs are streamed to
// stdout and stderr as they arrive (nil discards); a slow writer holds back
// the SSH window, so at most one window per stream is buffered. ctx and the
// connect timeout bound everything up to the server's answer to the exec
// request (a server that never answers is ssh_unreachable, or timeout when
// ctx ended); Wait's context bounds the run. The caller must call Wait or
// Close.
//
// A writer must not fail and must not block forever. A writer that returns
// an error stops being fed: the rest of that stream is not drained, the SSH
// window fills and the remote command stalls until it is stopped; if the
// command still exits 0, Wait reports it exited with code 0 and an internal
// error saying an output writer failed. A writer that blocks forever keeps
// Wait and Close blocked, even after the connection is closed.
func (c *Client) Start(ctx context.Context, tid tenant.TenantID, srv *tenant.Server, command string, stdout, stderr io.Writer) (*Command, error) {
	const op = "exec"
	client, err := c.dial(ctx, tid, srv, tenant.AccessLook, op)
	if err != nil {
		return nil, err
	}
	// Until the exec reply arrives nothing else bounds the channel open or
	// the exec request: keep ctx plus the connect timeout armed to close the
	// connection.
	wctx, cancel := context.WithTimeout(ctx, c.opts.ConnectTimeout)
	defer cancel()
	stop := context.AfterFunc(wctx, func() { _ = client.Close() })
	silent := func(cause error) error {
		_ = client.Close()
		var e *Error
		switch {
		case ctx.Err() != nil:
			e = ctxErr(srv.ID, op, ctx.Err())
		case pastDeadline(ctx):
			e = ctxErr(srv.ID, op, context.DeadlineExceeded)
		default:
			e = newErr(CodeUnreachable, srv.ID, op, "the server did not answer in time", nil, cause)
		}
		e.mayRun = true
		return e
	}
	sess, err := client.NewSession()
	if err != nil {
		if !stop() {
			return nil, silent(err)
		}
		_ = client.Close()
		return nil, newErr(CodeUnreachable, srv.ID, op, "the server did not open a session", nil, err)
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	sess.Stdout = stdout
	sess.Stderr = stderr
	// Stdin stays nil: x/crypto then sends EOF at once.
	err = sess.Start(command)
	if !stop() {
		// The timer fired: whatever Start returned, the connection is closed.
		return nil, silent(nil)
	}
	if err != nil {
		_ = sess.Close()
		_ = client.Close()
		// x/crypto's error repeats the command text; it is not kept.
		return nil, newErr(CodeInternal, srv.ID, op, "the server refused the exec request", nil, nil)
	}
	cmd := &Command{
		tenant: tid,
		server: srv.ID,
		opts:   c.opts,
		client: client,
		sess:   sess,
		exited: make(chan struct{}),
	}
	go func() {
		cmd.waitErr = sess.Wait()
		close(cmd.exited)
	}()
	return cmd, nil
}

// Exec runs command on srv and waits for it (Start, then Wait with the same
// context). A non-zero exit is not an error; see Wait for the rest. When
// Start gave up on a server that stopped answering, the status is
// no_status with MayStillRun set: the exec request may have arrived.
func (c *Client) Exec(ctx context.Context, tid tenant.TenantID, srv *tenant.Server, command string, stdout, stderr io.Writer) (ExitStatus, error) {
	cmd, err := c.Start(ctx, tid, srv, command, stdout, stderr)
	if err != nil {
		var e *Error
		if errors.As(err, &e) && e.mayRun {
			st := ExitStatus{Outcome: OutcomeNoStatus, MayStillRun: true}
			switch {
			case errors.Is(err, context.DeadlineExceeded):
				st.Stopped = StopDeadline
			case errors.Is(err, context.Canceled):
				st.Stopped = StopCancel
			}
			return st, err
		}
		return ExitStatus{}, err
	}
	return cmd.Wait(ctx)
}

// Tenant returns the tenant the command runs for.
func (cmd *Command) Tenant() tenant.TenantID { return cmd.tenant }

// Server returns the id of the server the command runs on.
func (cmd *Command) Server() tenant.ServerID { return cmd.server }

// Done is closed when the command's session has ended.
func (cmd *Command) Done() <-chan struct{} { return cmd.exited }

// Wait waits for the command to end and returns its status; it closes the
// connection. When ctx ends first it stops the command: TERM, then up to
// KillGrace for the exit, then KILL, then up to KillWait, and only then
// closes the connection; the status says how it ended. The error is nil
// for a command that ended on its own (whatever its exit code); it matches
// CodeTimeout when ctx's deadline passed, context.Canceled when ctx was
// cancelled, and CodeUnreachable when the connection was lost.
func (cmd *Command) Wait(ctx context.Context) (ExitStatus, error) {
	select {
	case <-cmd.exited:
	case <-ctx.Done():
		if !cmd.hasExited() {
			cmd.stop(ctx.Err())
		}
		<-cmd.exited
	}
	return cmd.finish()
}

// stop runs the TERM -> grace -> KILL -> wait -> close sequence once. Every
// step is bounded: an unanswered signal request closes the connection.
func (cmd *Command) stop(cause error) {
	cmd.stopOnce.Do(func() {
		cmd.mu.Lock()
		cmd.stopCause = cause
		cmd.mu.Unlock()
		if cmd.hasExited() {
			return
		}
		// A refused or unanswered signal skips its wait: nothing was sent.
		if ok, _, _ := cmd.sendSignal("TERM", cmd.opts.KillGrace); ok {
			cmd.mu.Lock()
			cmd.termSent = true
			cmd.mu.Unlock()
			if cmd.waitExit(cmd.opts.KillGrace) {
				return
			}
		}
		if cmd.isClosed() {
			return
		}
		if ok, _, _ := cmd.sendSignal("KILL", cmd.opts.KillWait); ok {
			cmd.mu.Lock()
			cmd.killSent = true
			cmd.mu.Unlock()
			if cmd.waitExit(cmd.opts.KillWait) {
				return
			}
		}
		cmd.closeConn(true)
	})
}

// isClosed reports whether the connection has been closed by this side.
func (cmd *Command) isClosed() bool {
	cmd.mu.Lock()
	defer cmd.mu.Unlock()
	return cmd.closed
}

// hasExited reports whether the session has ended.
func (cmd *Command) hasExited() bool {
	select {
	case <-cmd.exited:
		return true
	default:
		return false
	}
}

// waitExit waits up to d for the session to end.
func (cmd *Command) waitExit(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-cmd.exited:
		return true
	case <-t.C:
		return false
	}
}

// closeConn closes the session's connection once. mayStillRun records that
// the command was not seen to end.
func (cmd *Command) closeConn(mayStillRun bool) {
	cmd.mu.Lock()
	if mayStillRun && !cmd.hasExited() {
		cmd.mayStillRun = true
	}
	already := cmd.closed
	cmd.closed = true
	cmd.mu.Unlock()
	if !already {
		_ = cmd.client.Close()
	}
}

// sendSignal sends the signal request with want-reply set and reports
// whether the server accepted it. x/crypto's Session.Signal sends it
// without asking for a reply, so it cannot report acceptance (F10). A
// server that does not answer within bound gets its connection closed
// (silent is then true): x/crypto waits for the reply without a timeout.
func (cmd *Command) sendSignal(name string, bound time.Duration) (ok, silent bool, err error) {
	var fired atomic.Bool
	t := time.AfterFunc(bound, func() {
		fired.Store(true)
		cmd.closeConn(true)
	})
	ok, err = cmd.sess.SendRequest("signal", true, ssh.Marshal(struct{ Signal string }{name}))
	t.Stop()
	return ok && !fired.Load(), fired.Load(), err
}

// Signal sends signal name (TERM, KILL, INT, HUP, QUIT, USR1 or USR2) to
// the command's process group and returns nil when the server accepted it.
// When the server refuses, the session is closed (the command may still be
// running; Wait then reports MayStillRun) and the error matches
// ErrSignalRefused. A server that does not answer within KillWait gets its
// connection closed and the error is CodeUnreachable (the command may still
// be running). A command that already ended gives ErrNotRunning.
func (cmd *Command) Signal(name string) error {
	const op = "signal"
	if !ValidSignal(name) {
		return newErr(CodeInternal, cmd.server, op, ErrUnknownSignal.Error(), ErrUnknownSignal, nil)
	}
	cmd.mu.Lock()
	closed := cmd.closed
	cmd.mu.Unlock()
	if closed || cmd.hasExited() {
		return newErr(CodeInternal, cmd.server, op, ErrNotRunning.Error(), ErrNotRunning, nil)
	}
	ok, silent, err := cmd.sendSignal(name, cmd.opts.KillWait)
	switch {
	case silent:
		return newErr(CodeUnreachable, cmd.server, op, "the server did not answer the signal request", nil, err)
	case err != nil && cmd.hasExited():
		return newErr(CodeInternal, cmd.server, op, ErrNotRunning.Error(), ErrNotRunning, err)
	case err != nil:
		return newErr(CodeUnreachable, cmd.server, op, "the connection was lost", nil, err)
	case !ok:
		cmd.closeConn(true)
		return newErr(CodeInternal, cmd.server, op, ErrSignalRefused.Error(), ErrSignalRefused, nil)
	}
	return nil
}

// Close ends the handle: it closes the connection (without stopping the
// command, which may then still run; use Signal or a Wait context for
// that) and waits for the session goroutine. It is a no-op after Wait.
func (cmd *Command) Close() error {
	cmd.closeConn(true)
	<-cmd.exited
	_, _ = cmd.finish()
	return nil
}

// finish builds the status once, after the session ended, and closes the
// connection.
func (cmd *Command) finish() (ExitStatus, error) {
	cmd.finishOnce.Do(func() {
		cmd.mu.Lock()
		st := ExitStatus{TermSent: cmd.termSent, KillSent: cmd.killSent, MayStillRun: cmd.mayStillRun}
		cause := cmd.stopCause
		closedByUs := cmd.closed
		cmd.mu.Unlock()
		var (
			ee *ssh.ExitError
			em *ssh.ExitMissingError
		)
		lost, writerFailed := false, false
		switch err := cmd.waitErr; {
		case err == nil:
			st.Outcome = OutcomeExited
		case errors.As(err, &ee):
			if sig := ee.Signal(); sig != "" {
				st.Outcome, st.Signal = OutcomeSignaled, cleanSignal(sig)
			} else {
				st.Outcome, st.Code = OutcomeExited, ee.ExitStatus()
			}
		case errors.As(err, &em):
			st.Outcome = OutcomeNoStatus
			lost = !closedByUs && !cmd.alive()
		default:
			// x/crypto returns another error only for exit status 0 with a
			// failed output copy (session.go, Wait): a writer failed.
			st.Outcome = OutcomeExited
			writerFailed = true
		}
		if st.Outcome == OutcomeNoStatus {
			st.MayStillRun = true
		}
		switch {
		case errors.Is(cause, context.DeadlineExceeded):
			st.Stopped = StopDeadline
		case cause != nil:
			st.Stopped = StopCancel
		}
		cmd.status = st
		switch {
		case cause != nil:
			cmd.err = ctxErr(cmd.server, "exec", cause)
		case lost:
			cmd.err = newErr(CodeUnreachable, cmd.server, "exec", "the connection was lost", nil, cmd.waitErr)
		case writerFailed:
			cmd.err = newErr(CodeInternal, cmd.server, "exec", "an output writer failed", nil, cmd.waitErr)
		}
		cmd.closeConn(false)
	})
	return cmd.status, cmd.err
}

// alive asks the server for a reply to a harmless global request, to tell
// a session the server ended without a status (alive) from a lost
// connection. A server that does not answer within KillWait counts as lost.
func (cmd *Command) alive() bool {
	t := time.AfterFunc(cmd.opts.KillWait, func() { _ = cmd.client.Close() })
	defer t.Stop()
	_, _, err := cmd.client.SendRequest("keepalive@openssh.com", true, nil)
	return err == nil
}
