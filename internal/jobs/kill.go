package jobs

import (
	"context"
	"errors"

	"github.com/InfiniteRoomLabs/yasshm/internal/audit"
	"github.com/InfiniteRoomLabs/yasshm/internal/sshclient"
	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// The signals Kill sends (spec 5.8, kill_command).
const (
	SignalTerm = "TERM"
	SignalInt  = "INT"
	SignalKill = "KILL"
)

// Kill sends sig (TERM when empty, INT or KILL) to the tenant's running
// command id and reports whether the server accepted the signal. It writes
// command.kill_requested first and sends nothing if that write fails
// (ErrAuditFailed). A refused or unanswered signal is not an error: the SSH
// layer closes the session and the record says the remote process may still
// run. On a finished command Kill does nothing and returns the record with
// delivered false (idempotent). A command of another tenant is not_found.
func (e *Engine) Kill(ctx context.Context, tid tenant.TenantID, by audit.Actor, id tenant.CommandID, sig string) (rec Record, delivered bool, err error) {
	if sig == "" {
		sig = SignalTerm
	}
	if sig != SignalTerm && sig != SignalInt && sig != SignalKill {
		return Record{}, false, invalid("signal must be TERM, INT or KILL")
	}
	r, err := e.Get(ctx, tid, id)
	if err != nil {
		return Record{}, false, err
	}
	if by.Principal.Tenant != tid {
		return Record{}, false, invalid("the principal does not belong to the tenant")
	}
	j := e.live(tid, id)
	if r.Status != StatusRunning || j == nil {
		return r, false, nil
	}
	bg := context.WithoutCancel(ctx)
	var acted bool
	aerr := e.cfg.Audit.Before(audit.CommandKillRequested(by, id, sig), func() error {
		acted = true
		j.mu.Lock()
		j.killRequested = true
		j.mu.Unlock()
		e.report(e.st.MarkKillRequested(bg, tid, id, e.now()))
		serr := j.h.Signal(sig)
		switch {
		case serr == nil:
			delivered = true
		case errors.Is(serr, sshclient.ErrNotRunning):
			// It ended on its own before the signal: not a kill.
			j.mu.Lock()
			j.killRequested = false
			j.mu.Unlock()
		default:
			// Refused or unanswered: the session is closed and the
			// remote process may still run (spec 5.5).
			j.mu.Lock()
			j.mayStillRun = true
			j.mu.Unlock()
			e.report(e.st.MarkMayStillRun(bg, tid, id))
		}
		return nil
	})
	if !acted {
		return r, false, auditFailed(aerr)
	}
	r, err = e.Get(bg, tid, id)
	return r, delivered, err
}
