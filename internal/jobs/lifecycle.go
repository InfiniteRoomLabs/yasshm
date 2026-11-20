package jobs

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
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
	// The engine lock is held across the check and the update, so a first
	// Start cannot insert a record that this call then marks lost.
	e.mu.Lock()
	if e.started {
		e.mu.Unlock()
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
	e.mu.Unlock()
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
	e.report(e.flushPending(context.WithoutCancel(ctx)))
	e.storeClosed.Store(true)
	e.shutdownErr = e.st.Close(context.WithoutCancel(ctx))
	close(e.shutdownDone)
	return e.shutdownErr
}

// janitor runs Sweep at start, every JanitorInterval and whenever a command
// finishes, until Shutdown. A finishing command wakes it, so a tenant's data
// passes max_data_bytes by at most max_concurrent_commands x
// max_output_bytes between passes.
func (e *Engine) janitor() {
	defer close(e.janitorDone)
	t := time.NewTicker(e.cfg.JanitorInterval)
	defer t.Stop()
	for {
		e.report(e.Sweep(e.root))
		if !e.janitorWait(t.C) {
			return
		}
	}
}

// janitorWait waits for the next reason to run a pass and reports false at
// Shutdown. A settle request (tests) runs the pass a pending wake asks for,
// then answers, so every command that finished before the request has had
// its pass.
func (e *Engine) janitorWait(tick <-chan time.Time) bool {
	for {
		select {
		case <-e.janitorStop:
			return false
		case <-tick:
			return true
		case <-e.wake:
			return true
		case reply := <-e.settleReq:
			select {
			case <-e.wake:
				e.report(e.Sweep(e.root))
			default:
			}
			close(reply)
		}
	}
}

// flushPending retries the store writes of command ends this engine could
// not record; a record that has already finished is dropped from the list.
func (e *Engine) flushPending(ctx context.Context) error {
	e.mu.Lock()
	keys := slices.Collect(maps.Keys(e.pending))
	e.mu.Unlock()
	var errs []error
	for _, k := range keys {
		e.mu.Lock()
		p := e.pending[k]
		e.mu.Unlock()
		err := e.hooks().finishRow(ctx, k.tid, k.id, p.f)
		if err != nil && !errors.Is(err, store.ErrNotRunning) && !errors.Is(err, store.ErrNotFound) {
			errs = append(errs, fmt.Errorf("jobs: janitor: record the end of command %s: %w", k.id, err))
			continue
		}
		e.mu.Lock()
		delete(e.pending, k)
		e.mu.Unlock()
	}
	return errors.Join(errs...)
}

// janitorBatch is how many records the janitor reads at once.
const janitorBatch = 100

// Sweep is one janitor pass (the janitor runs it at start, every
// JanitorInterval and when a command finishes). Per tenant it deletes the
// finished commands whose finished_at is older than retention_days, then,
// while the tenant's usage (stored output, command text and store.RowCost
// per record, plus what running commands have stored) exceeds
// max_data_bytes, the oldest finished commands first. It deletes the record
// first and then the output directory; when the record cannot be deleted (a
// full disk), it deletes the directory first to free space and tries the
// record again. It never touches a running command or another tenant's data.
// Then it removes output directories that have no record, and retries the
// store writes of command ends that failed. Passes run one at a time: a call
// waits for a pass in flight (the janitor's or another caller's), so two
// passes never work on the same records at once.
func (e *Engine) Sweep(ctx context.Context) error {
	e.sweepMu.Lock()
	defer e.sweepMu.Unlock()
	if err := e.usable(); err != nil {
		return err
	}
	var errs []error
	for _, tid := range slices.Sorted(maps.Keys(e.tenants)) {
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
		used, err := e.usedBytes(ctx, tid)
		if err != nil {
			errs = append(errs, err)
			continue
		}
	quota:
		for used > lim.MaxDataBytes {
			recs, err := e.st.OldestFinished(ctx, tid, janitorBatch)
			if err != nil {
				errs = append(errs, err)
				break
			}
			if len(recs) == 0 {
				break
			}
			for i := range recs {
				if used <= lim.MaxDataBytes || !e.deleteAll(ctx, tid, recs[i:i+1], &errs) {
					break quota
				}
				used -= recs[i].StoredBytes + int64(len(recs[i].Command)) + store.RowCost
			}
		}
	}
	e.sweepOrphans(ctx)
	errs = append(errs, e.flushPending(ctx))
	return errors.Join(errs...)
}

// usedBytes is the tenant's usage against max_data_bytes: what the store
// counts (finished output, command text, a fixed cost per record), then the
// output running commands have stored so far. The store is read first, so a
// command that finishes between the two reads is counted at most once.
func (e *Engine) usedBytes(ctx context.Context, tid tenant.TenantID) (int64, error) {
	if err := e.usable(); err != nil {
		return 0, err
	}
	n, err := e.st.UsedBytes(ctx, tid)
	if err != nil {
		return 0, err
	}
	return n + e.liveStored(tid), nil
}

// deleteAll deletes each finished record and then its output directory. A
// record that cannot be deleted gets its directory deleted first (freeing
// space on a full disk) and one more try. It returns false when a deletion
// failed.
func (e *Engine) deleteAll(ctx context.Context, tid tenant.TenantID, recs []Record, errs *[]error) bool {
	for i := range recs {
		id := recs[i].ID
		dir, derr := commandDir(e.cfg.DataDir, tid, id)
		err := e.hooks().deleteRow(ctx, tid, id)
		if err != nil && derr == nil && !errors.Is(err, store.ErrNotFound) && !errors.Is(err, store.ErrRunning) {
			if rerr := os.RemoveAll(dir); rerr != nil {
				*errs = append(*errs, fmt.Errorf("jobs: janitor: delete output %s: %w", id, rerr))
			}
			err = e.hooks().deleteRow(ctx, tid, id)
		}
		if err != nil {
			*errs = append(*errs, fmt.Errorf("jobs: janitor: delete record %s: %w", id, err))
			return false
		}
		if derr != nil {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			*errs = append(*errs, fmt.Errorf("jobs: janitor: delete output %s: %w", id, err))
		}
	}
	return true
}

// liveStored returns the bytes stored so far by the tenant's running
// commands. The jobs are copied out under the engine lock and read outside
// it, so the engine lock is never held while a capture lock is awaited.
func (e *Engine) liveStored(tid tenant.TenantID) int64 {
	e.mu.Lock()
	var js []*job
	for k, j := range e.jobs {
		if k.tid == tid {
			js = append(js, j)
		}
	}
	e.mu.Unlock()
	var n int64
	for _, j := range js {
		if !j.recorded.Load() {
			n += j.cap.snapshot().stored
		}
	}
	return n
}

// sweepOrphans removes output directories that have no record (left when a
// gateway died between deleting a record and its files, or when a directory
// could not be deleted). It runs in New and in every Sweep: a record is always
// inserted before its directory is created, so a running command's directory
// is never taken. Names that are not valid ids are left alone and reported.
func (e *Engine) sweepOrphans(ctx context.Context) {
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
