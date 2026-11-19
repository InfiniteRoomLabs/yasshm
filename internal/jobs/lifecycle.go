package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/InfiniteRoomLabs/yasshm/internal/audit"
	"github.com/InfiniteRoomLabs/yasshm/internal/store"
	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// Recover marks every record left running by an earlier gateway lost, with
// finished_at set and the output already on disk counted (it stays
// readable), and writes one command.finished event per record. New runs it
// before the engine accepts work; once a command has been started here it
// refuses (a record running now belongs to this engine). It returns how many
// records it marked.
func (e *Engine) Recover(ctx context.Context) (int, error) {
	e.mu.Lock()
	started := e.started
	e.mu.Unlock()
	if started {
		return 0, internal("recover runs only before the first command starts", nil)
	}
	recs, err := e.st.RecoverAllTenants(ctx, e.now(), func(r *store.Record) (int64, int64) {
		dir, derr := commandDir(e.cfg.DataDir, r.Tenant, r.ID)
		if derr != nil {
			return 0, 0
		}
		so, err1 := fileSize(filepath.Join(dir, StreamStdout))
		se, err2 := fileSize(filepath.Join(dir, StreamStderr))
		e.report(errors.Join(err1, err2))
		return so, se
	})
	if err != nil {
		return 0, internal("could not recover the command records", err)
	}
	for i := range recs {
		r := &recs[i]
		f := &store.Final{Status: StatusLost, FinishedAt: r.FinishedAt, StdoutBytes: r.StdoutBytes, StderrBytes: r.StderrBytes, StoredBytes: r.StoredBytes, MayStillRun: true}
		since := r.StartedAt
		if since.IsZero() {
			since = r.CreatedAt
		}
		e.report(e.cfg.Audit.Write(audit.CommandFinished(actorOf(r), r.Server, r.ID, finishEvent(f, since))))
	}
	return len(recs), nil
}

// actorOf rebuilds the audit actor of a stored record.
func actorOf(r *Record) audit.Actor {
	p := tenant.Principal{Tenant: r.Tenant}
	kind, subject, _ := strings.Cut(r.Principal, ":")
	switch kind {
	case tenant.KindUser.String():
		p.Kind = tenant.KindUser
	case tenant.KindService.String():
		p.Kind = tenant.KindService
	}
	p.Subject = subject
	return audit.Actor{RequestID: r.RequestID, Principal: p}
}

// Shutdown stops the engine: Start refuses new work at once (ErrShuttingDown),
// every running command is ended through the SSH layer's stop sequence (TERM,
// the kill grace, KILL) and marked killed, and Shutdown waits up to the
// largest shutdown_grace_seconds of the tenants (or until ctx ends) for them.
// Then it closes the connections that are left (their records say the remote
// process may still run), waits for every engine goroutine, stops the janitor
// and closes the store. After it returns no goroutine, connection or file of
// the engine remains. A second call waits for the first and returns its
// result.
func (e *Engine) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	if e.closing {
		e.mu.Unlock()
		select {
		case <-e.shutdownDone:
			return e.shutdownErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	e.closing = true
	e.mu.Unlock()

	e.cancelRoot(errShutdown)
	close(e.janitorStop)
	<-e.janitorDone

	var grace int64
	for _, ts := range e.tenants {
		grace = max(grace, ts.t.Limits.ShutdownGraceSeconds)
	}
	all := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(all)
	}()
	t := time.NewTimer(time.Duration(grace) * time.Second)
	select {
	case <-all:
	case <-t.C:
	case <-ctx.Done():
	}
	t.Stop()
	select {
	case <-all:
	default:
		// Past the grace: close what is left. Wait returns once its
		// connection is closed (no exit: may_still_run).
		e.mu.Lock()
		left := make([]*job, 0, len(e.jobs))
		for _, j := range e.jobs {
			left = append(left, j)
		}
		e.mu.Unlock()
		for _, j := range left {
			e.report(j.h.Close())
		}
		<-all
	}
	e.storeClosed.Store(true)
	e.shutdownErr = e.st.Close(context.WithoutCancel(ctx))
	close(e.shutdownDone)
	return e.shutdownErr
}

// janitor runs Sweep at start and every JanitorInterval until Shutdown.
func (e *Engine) janitor() {
	defer close(e.janitorDone)
	t := time.NewTicker(e.cfg.JanitorInterval)
	defer t.Stop()
	for {
		e.report(e.Sweep(e.root))
		select {
		case <-e.janitorStop:
			return
		case <-t.C:
		}
	}
}

// janitorBatch is how many records the janitor reads at once.
const janitorBatch = 100

// Sweep is one janitor pass (the janitor runs it at start and every
// JanitorInterval). Per tenant it deletes the finished commands whose
// finished_at is older than retention_days, then, while the tenant's stored
// output exceeds max_data_bytes, the oldest finished commands first. It
// deletes the record first and then the output directory, and never touches
// a running command or another tenant's data.
func (e *Engine) Sweep(ctx context.Context) error {
	if err := e.usable(); err != nil {
		return err
	}
	var errs []error
	for _, tid := range e.sortedTenants() {
		lim := e.tenants[tid].t.Limits
		cutoff := e.now().Add(-time.Duration(lim.RetentionDays) * 24 * time.Hour)
		for {
			recs, err := e.st.FinishedBefore(ctx, tid, cutoff, janitorBatch)
			if err != nil {
				errs = append(errs, err)
				break
			}
			if !e.deleteAll(ctx, tid, recs, &errs) || len(recs) < janitorBatch {
				break
			}
		}
		used, err := e.st.StoredBytes(ctx, tid)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		used += e.liveStored(tid)
		for used > lim.MaxDataBytes {
			recs, err := e.st.OldestFinished(ctx, tid, janitorBatch)
			if err != nil {
				errs = append(errs, err)
				break
			}
			if len(recs) == 0 {
				break
			}
			var n int
			for i := range recs {
				if used <= lim.MaxDataBytes {
					break
				}
				if !e.deleteAll(ctx, tid, recs[i:i+1], &errs) {
					break
				}
				used -= recs[i].StoredBytes
				n++
			}
			if n < len(recs) && used > lim.MaxDataBytes {
				break // a deletion failed; try again next pass
			}
		}
	}
	return errors.Join(errs...)
}

// deleteAll deletes each finished record and then its output directory. It
// returns false when a deletion failed.
func (e *Engine) deleteAll(ctx context.Context, tid tenant.TenantID, recs []Record, errs *[]error) bool {
	for i := range recs {
		id := recs[i].ID
		if err := e.st.DeleteFinished(ctx, tid, id); err != nil {
			*errs = append(*errs, fmt.Errorf("jobs: janitor: delete record %s: %w", id, err))
			return false
		}
		dir, err := commandDir(e.cfg.DataDir, tid, id)
		if err != nil {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			*errs = append(*errs, fmt.Errorf("jobs: janitor: delete output %s: %w", id, err))
		}
	}
	return true
}

// liveStored returns the bytes stored so far by the tenant's running
// commands.
func (e *Engine) liveStored(tid tenant.TenantID) int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	var n int64
	for k, j := range e.jobs {
		if k.tid == tid {
			n += j.cap.snapshot().stored
		}
	}
	return n
}

// sweepOrphans removes output directories that have no record (left when a
// gateway died between deleting a record and its files). It runs once in
// New, before any command starts; names that are not valid ids are left
// alone and reported.
func (e *Engine) sweepOrphans() {
	ctx := e.root
	base := filepath.Join(e.cfg.DataDir, tenantsDir)
	tdirs, err := os.ReadDir(base)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			e.report(err)
		}
		return
	}
	for _, td := range tdirs {
		tid := tenant.TenantID(td.Name())
		if !td.IsDir() || !store.ValidTenantID(tid) {
			e.report(fmt.Errorf("jobs: unexpected entry in the output directory: %q", td.Name()))
			continue
		}
		cdirs, err := os.ReadDir(filepath.Join(base, td.Name(), "commands"))
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				e.report(err)
			}
			continue
		}
		for _, cd := range cdirs {
			id := tenant.CommandID(cd.Name())
			if !cd.IsDir() || !store.ValidCommandID(id) {
				e.report(fmt.Errorf("jobs: unexpected entry in the output directory: %q", cd.Name()))
				continue
			}
			ok, err := e.st.Exists(ctx, tid, id)
			if err != nil || ok {
				e.report(err)
				continue
			}
			dir, _ := commandDir(e.cfg.DataDir, tid, id)
			e.report(os.RemoveAll(dir))
		}
	}
}
