// Package jobs is the job engine (spec 5.6): it starts commands on SSH
// targets through internal/sshclient, keeps their records in internal/store,
// captures their output into files under the data directory, and writes the
// command events of the audit log (spec 5.4) through internal/audit.
//
// A command runs under a context owned by the engine, not by the request that
// started it, so a caller that goes away does not stop it (D10). Start does
// not wait; Wait, Get, List, ReadOutput and Kill work on the record later.
// There is no queue: when a concurrency limit is reached Start returns busy
// and names the limit.
//
// Audit-before-act (D12): command.requested is written before the SSH
// session starts and command.kill_requested before the signal is sent; when
// the write fails nothing is done.
//
// Tenancy (D7): every method takes the caller's tenant.TenantID; a command of
// another tenant is not_found. Live command handles are kept by (tenant, id)
// and the handle's own tenant is checked again. Ids are validated against
// strict patterns before they reach SQL or a path.
//
// Output files are data_dir/tenants/<tenant>/commands/<id>/stdout and
// stderr, directories 0700 and files 0600 (the "outputs" datastore of the
// architecture model lives in this package).
//
// It imports from this module only tenant, sshclient, store and audit.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/InfiniteRoomLabs/yasshm/internal/audit"
	"github.com/InfiniteRoomLabs/yasshm/internal/sshclient"
	"github.com/InfiniteRoomLabs/yasshm/internal/store"
	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// Record is a command record (spec 5.6).
type Record = store.Record

// Status is a record's status.
type Status = store.Status

// The statuses, re-exported from store.
const (
	StatusRunning   = store.StatusRunning
	StatusSucceeded = store.StatusSucceeded
	StatusFailed    = store.StatusFailed
	StatusKilled    = store.StatusKilled
	StatusTimedOut  = store.StatusTimedOut
	StatusLost      = store.StatusLost
	StatusError     = store.StatusError
)

// DefaultJanitorInterval is how often the janitor runs when Config leaves it
// zero.
const DefaultJanitorInterval = time.Hour

// Auditor is the part of *audit.Logger the engine uses.
type Auditor interface {
	// Before writes ev and runs act only if the write succeeded.
	Before(ev audit.Event, act func() error) error
	// Write appends ev.
	Write(ev audit.Event) error
}

// Config builds an Engine. It is built from tenant values, not from the
// config package, so tests build tenants in code.
type Config struct {
	// DataDir holds the database and the output files. It must exist and
	// be private to the gateway's user (0700).
	DataDir string
	// Tenants are the tenants with their servers and limits.
	Tenants []tenant.Tenant
	// Audit receives the command events.
	Audit Auditor
	// Now is the clock (default time.Now). Times are stored in UTC.
	Now func() time.Time
	// Rand is the source of command ids (default crypto/rand.Reader).
	Rand io.Reader
	// OnError receives errors the engine cannot return to a caller (an
	// audit write after the fact, a record update in a goroutine, a
	// janitor deletion). Default: discard. The error may name paths and
	// hosts: operator logs only.
	OnError func(error)
	// JanitorInterval is how often the janitor runs (default
	// DefaultJanitorInterval). The janitor also runs once at start.
	JanitorInterval time.Duration
	// SSHOptions turns a tenant's limits into SSH client options (default
	// sshclient.OptionsFromLimits).
	SSHOptions func(tenant.Limits) sshclient.Options
}

// tenantState is one tenant's configuration and its concurrency counters
// (guarded by Engine.mu).
type tenantState struct {
	t         tenant.Tenant
	client    *sshclient.Client
	running   int64
	perServer map[tenant.ServerID]int64
}

type key struct {
	tid tenant.TenantID
	id  tenant.CommandID
}

// job is a running command.
type job struct {
	tid    tenant.TenantID
	id     tenant.CommandID
	server tenant.ServerID
	actor  audit.Actor
	dir    string
	merge  bool
	h      *sshclient.Command
	cap    *capture
	ctx    context.Context
	cancel context.CancelCauseFunc
	stopT  context.CancelFunc
	start  time.Time
	done   chan struct{}

	// killMu is held by Kill from setting killRequested until it knows
	// whether the signal reached a running command, and by run before it
	// reads the flags, so a kill that races a natural exit is not counted.
	killMu sync.Mutex

	mu            sync.Mutex
	killRequested bool
	mayStillRun   bool
}

// pendingEnd is a command whose end could not be written to the store (a
// full disk, a locked database): this engine reports it as finished, and the
// janitor retries the write.
type pendingEnd struct {
	f *store.Final
}

// errShutdown and errDisk are the causes the engine cancels a command's
// context with.
var (
	errShutdown = errors.New("the engine is shutting down")
	errDisk     = errors.New("an output file could not be written")
)

// Engine is the job engine. It is safe for concurrent use.
type Engine struct {
	cfg        Config
	st         *store.Store
	tenants    map[tenant.TenantID]*tenantState
	root       context.Context
	cancelRoot context.CancelCauseFunc
	openOutput openOutputFunc

	mu      sync.Mutex
	hk      hooks // guarded by mu; read with e.hooks()
	jobs    map[key]*job
	pending map[key]*pendingEnd
	closing bool
	started bool
	wg      sync.WaitGroup // one per reserved slot: Start in flight or a running job

	storeClosed  atomic.Bool
	janitorStop  chan struct{}
	janitorDone  chan struct{}
	wake         chan struct{}      // one slot: a command finished, run the janitor
	settleReq    chan chan struct{} // tests: answered once a pending wake's pass has run
	sweepMu      sync.Mutex         // one janitor pass at a time
	shutdownDone chan struct{}
	shutdownErr  error
}

// hooks are the store writes that can fail for lack of space, and a point
// in Kill, which tests replace.
type hooks struct {
	finishRow func(ctx context.Context, tid tenant.TenantID, id tenant.CommandID, f *store.Final) error
	deleteRow func(ctx context.Context, tid tenant.TenantID, id tenant.CommandID) error
	// beforeSignal runs in Kill between marking the kill and sending the
	// signal (nil in production).
	beforeSignal func(j *job)
}

func (e *Engine) hooks() hooks {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.hk
}

// New opens the store in cfg.DataDir, marks every command left running by an
// earlier gateway lost (Recover), removes output directories that have no
// record, and starts the janitor. Call Shutdown to stop it.
func New(ctx context.Context, cfg Config) (*Engine, error) {
	if cfg.Audit == nil {
		return nil, errors.New("jobs: Config.Audit is required")
	}
	if !filepath.IsAbs(cfg.DataDir) {
		return nil, errors.New("jobs: Config.DataDir must be an absolute path")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Rand == nil {
		cfg.Rand = rand.Reader
	}
	if cfg.OnError == nil {
		cfg.OnError = func(error) {}
	}
	if cfg.JanitorInterval <= 0 {
		cfg.JanitorInterval = DefaultJanitorInterval
	}
	if cfg.SSHOptions == nil {
		cfg.SSHOptions = sshclient.OptionsFromLimits
	}
	tenants := make(map[tenant.TenantID]*tenantState, len(cfg.Tenants))
	for i := range cfg.Tenants {
		t := cfg.Tenants[i]
		if !store.ValidTenantID(t.ID) {
			return nil, fmt.Errorf("jobs: tenant %d has an invalid id", i)
		}
		if _, dup := tenants[t.ID]; dup {
			return nil, fmt.Errorf("jobs: tenant %q is listed twice", t.ID)
		}
		tenants[t.ID] = &tenantState{t: t, client: sshclient.New(cfg.SSHOptions(t.Limits)), perServer: map[tenant.ServerID]int64{}}
	}
	st, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		return nil, err
	}
	root, cancel := context.WithCancelCause(context.Background())
	e := &Engine{
		cfg: cfg, st: st, tenants: tenants, root: root, cancelRoot: cancel, openOutput: defaultOpenOutput,
		jobs: map[key]*job{}, pending: map[key]*pendingEnd{}, janitorStop: make(chan struct{}), janitorDone: make(chan struct{}),
		wake: make(chan struct{}, 1), settleReq: make(chan chan struct{}), shutdownDone: make(chan struct{}),
	}
	e.hk = hooks{finishRow: st.Finish, deleteRow: st.DeleteFinished}
	if _, err := e.Recover(ctx); err != nil {
		cancel(nil)
		_ = st.Close(ctx)
		return nil, err
	}
	e.sweepOrphans(ctx)
	go e.janitor()
	return e, nil
}

func (e *Engine) now() time.Time { return e.cfg.Now().UTC() }

func (e *Engine) report(err error) {
	if err != nil {
		e.cfg.OnError(err)
	}
}

// usable returns an error once the store is closed.
func (e *Engine) usable() error {
	if e.storeClosed.Load() {
		return shuttingDown()
	}
	return nil
}

// newID returns 128 random bits as 32 lower-case hex digits.
func (e *Engine) newID() (tenant.CommandID, error) {
	var b [16]byte
	if _, err := io.ReadFull(e.cfg.Rand, b[:]); err != nil {
		return "", internal("could not generate a command id", err)
	}
	return tenant.CommandID(hex.EncodeToString(b[:])), nil
}

func (e *Engine) tenant(tid tenant.TenantID) (*tenantState, error) {
	ts, ok := e.tenants[tid]
	if !ok || !store.ValidTenantID(tid) {
		return nil, notFound("no such command or server")
	}
	return ts, nil
}

// StartOptions are the caller's choices for one command.
type StartOptions struct {
	// TimeoutSeconds is the command's timeout; 0 takes the tenant's
	// default_timeout_seconds, and more than max_timeout_seconds is
	// invalid_argument.
	TimeoutSeconds int64
	// MergeStderr writes stderr into stdout in arrival order; nil means
	// true (spec 5.6).
	MergeStderr *bool
}

// Start starts command on the tenant's server sid and returns its record
// without waiting for it (use Wait). The order is fixed: validate against the
// tenant's limits, reserve the concurrency slots (busy, naming the limit,
// when one is full; there is no queue), insert the running record, write
// command.requested (if that fails the record becomes error, nothing is sent
// to the target and the error matches ErrAuditFailed), create the output
// files, connect and start. When the connection or the start fails the record
// becomes error with the SSH code, command.finished is written, and the
// record is returned with an error carrying that code. On success
// command.started is written and the command runs under a context owned by
// the engine: ctx bounds only this call's database insert.
//
// by is the caller: its principal must belong to tid, and its request id is
// kept for the audit events of the command's life.
func (e *Engine) Start(ctx context.Context, tid tenant.TenantID, by audit.Actor, sid tenant.ServerID, command string, opts StartOptions) (Record, error) { //nolint:contextcheck // the command runs under the engine's context, not the request's (D10)
	ts, err := e.tenant(tid)
	if err != nil {
		return Record{}, err
	}
	if by.Principal.Tenant != tid {
		return Record{}, invalid("the principal does not belong to the tenant")
	}
	srv, ok := ts.t.Server(sid)
	if !ok || !store.ValidServerID(sid) {
		return Record{}, notFound("no such server")
	}
	lim := ts.t.Limits
	switch {
	case command == "":
		return Record{}, invalid("the command is empty")
	case int64(len(command)) > lim.MaxCommandBytes:
		return Record{}, invalid("the command is longer than max_command_bytes")
	case !utf8.ValidString(command) || strings.ContainsRune(command, 0):
		return Record{}, invalid("the command is not valid UTF-8 text without NUL")
	}
	timeout := opts.TimeoutSeconds
	if timeout == 0 {
		timeout = lim.DefaultTimeoutSeconds
	}
	if timeout <= 0 || timeout > lim.MaxTimeoutSeconds {
		return Record{}, invalid("timeout_seconds is out of range")
	}
	merge := opts.MergeStderr == nil || *opts.MergeStderr
	id, err := e.newID()
	if err != nil {
		return Record{}, err
	}
	used, err := e.usedBytes(ctx, tid)
	if err != nil {
		return Record{}, internal("could not read the tenant's usage", err)
	}
	if used >= lim.MaxDataBytes {
		return Record{}, busy(LimitData)
	}
	release, err := e.reserve(ts, sid)
	if err != nil {
		return Record{}, err
	}
	rec := Record{
		Tenant: tid, ID: id, Principal: by.Principal.String(), RequestID: by.RequestID, Server: sid, Command: command,
		Status: StatusRunning, CreatedAt: e.now(), MergeStderr: merge, TimeoutSeconds: timeout,
	}
	if err := e.st.Insert(ctx, tid, &rec); err != nil {
		release()
		return Record{}, internal("could not record the command", err)
	}
	bg := context.WithoutCancel(ctx)
	dir, _ := commandDir(e.cfg.DataDir, tid, id) // both ids were validated above
	cctx, cancel := context.WithCancelCause(e.root)
	cctx, stopT := context.WithTimeout(cctx, time.Duration(timeout)*time.Second)
	j := &job{tid: tid, id: id, server: sid, actor: by, dir: dir, merge: merge, ctx: cctx, cancel: cancel, stopT: stopT, done: make(chan struct{})}

	var acted bool
	var setupErr, startErr error
	aerr := e.cfg.Audit.Before(audit.CommandRequested(by, sid, id, command, timeout), func() error {
		acted = true
		setupErr = e.createOutputs(j, lim.MaxOutputBytes)
		if setupErr != nil {
			return setupErr
		}
		j.h, startErr = ts.client.Start(cctx, tid, srv, command, j.cap.writer(outIdx), j.cap.writer(errIdx))
		return startErr
	})
	fail := func(code Code, audited bool) Record {
		snap := snapshot{}
		if j.cap != nil {
			e.report(j.cap.close())
			snap = j.cap.snapshot()
		}
		f := e.final(&snap, StatusError, string(code))
		// A start the server never answered may have reached it (spec
		// 5.5 callout): the command may be running.
		var se *sshclient.Error
		f.MayStillRun = errors.As(startErr, &se) && se.MayStillRun()
		e.recordEnd(bg, j, f, rec.CreatedAt, audited)
		cancel(nil)
		stopT()
		release()
		e.wakeJanitor()
		r, gerr := e.Get(bg, tid, id)
		e.report(gerr)
		return r
	}
	switch {
	case !acted:
		fail(CodeInternal, false)
		return Record{}, auditFailed(aerr)
	case setupErr != nil:
		r := fail(CodeInternal, true)
		return r, internal("could not create the output files", setupErr)
	case startErr != nil:
		r := fail(Code(sshclient.CodeOf(startErr)), true)
		return r, sshError(startErr)
	}
	// The handle is live (Kill, Shutdown) before the two synced writes.
	j.start = e.now()
	e.mu.Lock()
	e.jobs[key{tid, id}] = j
	e.mu.Unlock()
	e.report(e.st.MarkStarted(bg, tid, id, j.start))
	e.report(e.cfg.Audit.Write(audit.CommandStarted(by, sid, id)))
	go e.run(j, release)
	return e.Get(bg, tid, id)
}

// reserve takes one slot of the tenant and one of the server, or returns
// busy naming the full limit. The returned function gives them back; it is
// safe to call more than once.
func (e *Engine) reserve(ts *tenantState, sid tenant.ServerID) (func(), error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closing {
		return nil, shuttingDown()
	}
	if ts.running >= ts.t.Limits.MaxConcurrentCommands {
		return nil, busy(LimitPerTenant)
	}
	if ts.perServer[sid] >= ts.t.Limits.MaxConcurrentPerServer {
		return nil, busy(LimitPerServer)
	}
	ts.running++
	ts.perServer[sid]++
	e.started = true
	e.wg.Add(1)
	var once sync.Once
	return func() {
		once.Do(func() {
			e.mu.Lock()
			ts.running--
			ts.perServer[sid]--
			if ts.perServer[sid] == 0 {
				delete(ts.perServer, sid)
			}
			e.mu.Unlock()
			e.wg.Done()
		})
	}, nil
}

// createOutputs creates the command's output directory and files and its
// capture writer.
func (e *Engine) createOutputs(j *job, limit int64) error {
	if err := mkdirPrivate(j.dir); err != nil {
		return err
	}
	var files [2]io.WriteCloser
	out, err := e.openOutput(filepath.Join(j.dir, StreamStdout))
	if err != nil {
		return err
	}
	files[outIdx] = out
	if !j.merge {
		errf, err := e.openOutput(filepath.Join(j.dir, StreamStderr))
		if err != nil {
			_ = out.Close()
			return err
		}
		files[errIdx] = errf
	}
	j.cap = newCapture(files, j.merge, limit, func(error) { j.cancel(errDisk) })
	return nil
}

// final builds the Final of a record from a capture snapshot.
func (e *Engine) final(snap *snapshot, status Status, code string) *store.Final {
	return &store.Final{
		Status: status, ErrorCode: code, FinishedAt: e.now(),
		StdoutBytes: snap.received[outIdx], StderrBytes: snap.received[errIdx], StoredBytes: snap.stored, Truncated: snap.truncated,
	}
}

func finishEvent(f *store.Final, since time.Time) audit.Finish {
	return audit.Finish{
		Status: string(f.Status), ExitCode: f.ExitCode, Signal: f.Signal, ErrorCode: f.ErrorCode,
		StdoutBytes: f.StdoutBytes, StderrBytes: f.StderrBytes, Truncated: f.Truncated, Duration: f.FinishedAt.Sub(since),
	}
}

// run waits for a started command, records how it ended and frees its
// slots. The end becomes visible to Wait and to waiting readers (done is
// closed) only after the record, the command.finished event and the slot
// release, so a caller that sees a command finish can start the next one.
func (e *Engine) run(j *job, release func()) {
	st, werr := j.h.Wait(j.ctx)
	e.report(j.cap.close())
	snap := j.cap.snapshot()
	j.killMu.Lock() // a Kill in flight settles its flags first
	j.mu.Lock()
	killed, may := j.killRequested, j.mayStillRun
	j.mu.Unlock()
	j.killMu.Unlock()
	status, code := classify(&st, werr, context.Cause(j.ctx), snap.diskErr != nil, killed)
	f := e.final(&snap, status, code)
	switch st.Outcome {
	case sshclient.OutcomeExited:
		c := st.Code
		f.ExitCode = &c
	case sshclient.OutcomeSignaled:
		f.Signal = st.Signal
	}
	f.MayStillRun = st.MayStillRun || may
	e.recordEnd(e.root, j, f, j.start, true)
	e.mu.Lock()
	delete(e.jobs, key{j.tid, j.id})
	e.mu.Unlock()
	j.cancel(nil)
	j.stopT()
	release()
	e.wakeJanitor()
	close(j.done)
}

// finishBackoff are the pauses between the attempts to write a command's
// end; a test shortens them.
var finishBackoff = []time.Duration{20 * time.Millisecond, 80 * time.Millisecond, 320 * time.Millisecond}

// recordEnd writes a command's end to the store, retrying a failed write a
// few times. A record that has already finished (another engine recovered
// it) keeps its status and gets no second command.finished. A write that
// still fails is kept as pending: this engine reports the command finished,
// and the janitor retries the write.
func (e *Engine) recordEnd(ctx context.Context, j *job, f *store.Final, since time.Time, audited bool) {
	bg := context.WithoutCancel(ctx)
	var err error
	for i := 0; ; i++ {
		err = e.hooks().finishRow(bg, j.tid, j.id, f)
		if err == nil || errors.Is(err, store.ErrNotRunning) || errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrInvalid) || i >= len(finishBackoff) {
			break
		}
		time.Sleep(finishBackoff[i]) // a bounded backoff before retrying a failed write
	}
	switch {
	case errors.Is(err, store.ErrNotRunning):
		e.report(fmt.Errorf("jobs: the record of command %s had already finished: %w", j.id, err))
		return
	case err != nil:
		e.report(fmt.Errorf("jobs: record the end of command %s (kept in memory, retried by the janitor): %w", j.id, err))
		e.mu.Lock()
		e.pending[key{j.tid, j.id}] = &pendingEnd{f: f}
		e.mu.Unlock()
	}
	if audited {
		e.report(e.cfg.Audit.Write(audit.CommandFinished(j.actor, j.server, j.id, finishEvent(f, since))))
	}
}

// wakeJanitor asks the janitor for a pass without blocking.
func (e *Engine) wakeJanitor() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// classify maps how a command ended to its status and error code. It is the
// whole of the mapping of spec 5.6 and the 5.5 callout, in this order (a
// timeout wins over a kill request):
//   - an output file that could not be written: error, internal;
//   - ended because the engine shut down: killed;
//   - ended by the command's timeout (Stopped is deadline): timed_out, even
//     when the shell's exit code arrived and a background child kept the
//     session open (the record then also says may_still_run);
//   - ended after a kill request: killed;
//   - the connection was lost: error, ssh_unreachable;
//   - exit 0: succeeded; another exit code: failed; a signal nobody here
//     sent: failed (the signal is recorded);
//   - no status at all: error, ssh_unreachable.
func classify(st *sshclient.ExitStatus, err, cause error, diskErr, killRequested bool) (status Status, code string) {
	switch {
	case diskErr:
		return StatusError, string(CodeInternal)
	case errors.Is(cause, errShutdown) && (st.Stopped == sshclient.StopCancel || errors.Is(err, context.Canceled)):
		return StatusKilled, ""
	case st.Stopped == sshclient.StopDeadline || errors.Is(err, sshclient.CodeTimeout):
		return StatusTimedOut, ""
	case killRequested:
		return StatusKilled, ""
	case err != nil:
		return StatusError, string(sshclient.CodeOf(err))
	case st.Outcome == sshclient.OutcomeExited && st.Code == 0:
		return StatusSucceeded, ""
	case st.Outcome == sshclient.OutcomeExited || st.Outcome == sshclient.OutcomeSignaled:
		return StatusFailed, ""
	default:
		return StatusError, string(sshclient.CodeUnreachable)
	}
}

// live returns the running job of the tenant with id, or nil. The handle's
// own tenant is checked again.
func (e *Engine) live(tid tenant.TenantID, id tenant.CommandID) *job {
	e.mu.Lock()
	j := e.jobs[key{tid, id}]
	e.mu.Unlock()
	if j == nil || j.tid != tid || j.h.Tenant() != tid {
		return nil
	}
	return j
}

func checkID(id tenant.CommandID) error {
	if !store.ValidCommandID(id) {
		return invalid("malformed command id")
	}
	return nil
}

// Get returns the tenant's record with id. A running command's byte counts
// are live. A command of another tenant is not_found.
func (e *Engine) Get(ctx context.Context, tid tenant.TenantID, id tenant.CommandID) (Record, error) {
	if _, err := e.tenant(tid); err != nil {
		return Record{}, err
	}
	if err := checkID(id); err != nil {
		return Record{}, err
	}
	if err := e.usable(); err != nil {
		return Record{}, err
	}
	r, err := e.st.Get(ctx, tid, id)
	if errors.Is(err, store.ErrNotFound) {
		return Record{}, notFound("no such command")
	}
	if err != nil {
		return Record{}, internal("could not read the command record", err)
	}
	e.overlay(&r)
	return r, nil
}

// overlay replaces a running record's counters with the live ones, or its
// end with the one this engine could not write yet.
func (e *Engine) overlay(r *Record) {
	if r.Status != StatusRunning {
		return
	}
	e.mu.Lock()
	p := e.pending[key{r.Tenant, r.ID}]
	e.mu.Unlock()
	if p != nil {
		f := p.f
		r.Status, r.ExitCode, r.Signal, r.ErrorCode, r.FinishedAt = f.Status, f.ExitCode, f.Signal, f.ErrorCode, f.FinishedAt.UTC().Truncate(time.Microsecond)
		r.StdoutBytes, r.StderrBytes, r.StoredBytes, r.Truncated, r.MayStillRun = f.StdoutBytes, f.StderrBytes, f.StoredBytes, f.Truncated, r.MayStillRun || f.MayStillRun
		return
	}
	j := e.live(r.Tenant, r.ID)
	if j == nil {
		return
	}
	snap := j.cap.snapshot()
	r.StdoutBytes, r.StderrBytes, r.StoredBytes, r.Truncated = snap.received[outIdx], snap.received[errIdx], snap.stored, snap.truncated
	j.mu.Lock()
	r.MayStillRun = r.MayStillRun || j.mayStillRun
	j.mu.Unlock()
}

// Wait waits up to d (at most the tenant's max_wait_seconds) for the
// tenant's command id to finish, or until ctx ends, and returns the record as
// it is then, finished or not. A command counts as finished once the engine
// is done with it: its record, its command.finished event and its slots.
func (e *Engine) Wait(ctx context.Context, tid tenant.TenantID, id tenant.CommandID, d time.Duration) (Record, error) {
	ts, err := e.tenant(tid)
	if err != nil {
		return Record{}, err
	}
	d = min(d, time.Duration(ts.t.Limits.MaxWaitSeconds)*time.Second)
	j := e.live(tid, id) // before the record: a live job is waited for whatever the record says
	r, err := e.Get(ctx, tid, id)
	if err != nil || j == nil || d <= 0 {
		return r, err
	}
	t := time.NewTimer(d)
	select {
	case <-j.done:
	case <-t.C:
	case <-ctx.Done():
	}
	t.Stop()
	return e.Get(context.WithoutCancel(ctx), tid, id)
}
