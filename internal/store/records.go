package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// Status is a command record's status (spec 5.6).
type Status string

// The statuses.
const (
	// StatusRunning: the command was accepted and has not finished.
	StatusRunning Status = "running"
	// StatusSucceeded: the command exited 0.
	StatusSucceeded Status = "succeeded"
	// StatusFailed: the command exited non-zero, or a signal nobody here
	// sent ended it.
	StatusFailed Status = "failed"
	// StatusKilled: the command ended after a kill request or a shutdown.
	StatusKilled Status = "killed"
	// StatusTimedOut: the command passed its timeout and was stopped.
	StatusTimedOut Status = "timed_out"
	// StatusLost: the gateway died while the command ran (set only by
	// RecoverAllTenants).
	StatusLost Status = "lost"
	// StatusError: the gateway could not run the command.
	StatusError Status = "error"
)

// transitions is the whole state machine: the only status a record can
// leave is running, and the statuses it can go to. lost is reached only
// through RecoverAllTenants; Finish refuses it.
var transitions = map[Status][]Status{
	StatusRunning: {StatusSucceeded, StatusFailed, StatusKilled, StatusTimedOut, StatusError, StatusLost},
}

// CanTransition reports whether a record in status from may go to status to.
func CanTransition(from, to Status) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// Valid reports whether s is one of the seven statuses.
func (s Status) Valid() bool {
	return s == StatusRunning || CanTransition(StatusRunning, s)
}

// Finished reports whether s is a final status.
func (s Status) Finished() bool { return s.Valid() && s != StatusRunning }

// Record is one command record (spec 5.6).
type Record struct {
	// Tenant and ID are the record's key.
	Tenant tenant.TenantID
	ID     tenant.CommandID
	// Principal is the caller that started it, as "kind:subject".
	Principal string
	// RequestID is the request that started it, for audit correlation.
	RequestID string
	// Server is the server it runs on.
	Server tenant.ServerID
	// Command is the full command text (the operator's audit trail needs
	// it; tools show only the start of it).
	Command string
	// Status is the record's status.
	Status Status
	// ExitCode is set when the command exited with a code.
	ExitCode *int
	// Signal names the signal that ended it, without SIG.
	Signal string
	// ErrorCode is the error code for StatusError (and for a lost
	// connection).
	ErrorCode string
	// CreatedAt, StartedAt, FinishedAt and KillRequestedAt are UTC; a zero
	// value means "not yet".
	CreatedAt, StartedAt, FinishedAt, KillRequestedAt time.Time
	// StdoutBytes and StderrBytes count the bytes received per stream.
	StdoutBytes, StderrBytes int64
	// StoredBytes counts the bytes stored on disk (at most
	// max_output_bytes).
	StoredBytes int64
	// Truncated is set when output passed max_output_bytes.
	Truncated bool
	// MayStillRun is set when the gateway cannot tell that the remote
	// process stopped (a refused signal, no exit status).
	MayStillRun bool
	// MergeStderr says whether stderr was written into stdout.
	MergeStderr bool
	// TimeoutSeconds is the command's timeout.
	TimeoutSeconds int64
}

// Final is how a running record finishes.
type Final struct {
	Status                   Status
	ExitCode                 *int
	Signal                   string
	ErrorCode                string
	FinishedAt               time.Time
	StdoutBytes, StderrBytes int64
	StoredBytes              int64
	Truncated                bool
	MayStillRun              bool
}

// columns is the column list every SELECT returns, in scan order.
const columns = `tenant, id, principal, request_id, server, command, status, exit_code, signal, error_code,
	created_at, started_at, finished_at, kill_requested_at, stdout_bytes, stderr_bytes, stored_bytes,
	truncated, may_still_run, merge_stderr, timeout_seconds`

type scanner interface{ Scan(dest ...any) error }

func scanRecord(sc scanner) (Record, error) {
	var (
		r                           Record
		exit                        sql.NullInt64
		created, started, fin, kill sql.NullInt64
		trunc, may, merge           int
	)
	err := sc.Scan(&r.Tenant, &r.ID, &r.Principal, &r.RequestID, &r.Server, &r.Command, &r.Status, &exit, &r.Signal, &r.ErrorCode,
		&created, &started, &fin, &kill, &r.StdoutBytes, &r.StderrBytes, &r.StoredBytes, &trunc, &may, &merge, &r.TimeoutSeconds)
	if err != nil {
		return Record{}, err
	}
	if exit.Valid {
		c := int(exit.Int64)
		r.ExitCode = &c
	}
	r.CreatedAt, r.StartedAt, r.FinishedAt, r.KillRequestedAt = fromMicros(created), fromMicros(started), fromMicros(fin), fromMicros(kill)
	r.Truncated, r.MayStillRun, r.MergeStderr = trunc != 0, may != 0, merge != 0
	return r, nil
}

func checkKey(tid tenant.TenantID, id tenant.CommandID) error {
	if !ValidTenantID(tid) || !ValidCommandID(id) {
		return ErrInvalid
	}
	return nil
}

func nullInt(p *int) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*p), Valid: true}
}

// Insert adds a new running record. Its status must be running and its
// tenant, id and server valid; an existing id in the tenant is ErrExists.
func (s *Store) Insert(ctx context.Context, tid tenant.TenantID, r *Record) error {
	if err := checkKey(tid, r.ID); err != nil {
		return err
	}
	if r.Tenant != tid || r.Status != StatusRunning || !ValidServerID(r.Server) || r.CreatedAt.IsZero() || r.TimeoutSeconds <= 0 {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO commands (`+columns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tid, r.ID, r.Principal, r.RequestID, r.Server, r.Command, r.Status, nullInt(r.ExitCode), r.Signal, r.ErrorCode,
		micros(r.CreatedAt), micros(r.StartedAt), micros(r.FinishedAt), micros(r.KillRequestedAt),
		r.StdoutBytes, r.StderrBytes, r.StoredBytes, boolInt(r.Truncated), boolInt(r.MayStillRun), boolInt(r.MergeStderr), r.TimeoutSeconds)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return ErrExists
		}
		return fmt.Errorf("store: insert: %w", err)
	}
	return nil
}

// Get returns the tenant's record with id; another tenant's record, or none,
// is ErrNotFound.
func (s *Store) Get(ctx context.Context, tid tenant.TenantID, id tenant.CommandID) (Record, error) {
	if err := checkKey(tid, id); err != nil {
		return Record{}, ErrNotFound
	}
	r, err := scanRecord(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM commands WHERE tenant = ? AND id = ?`, tid, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("store: get: %w", err)
	}
	return r, nil
}

// Exists reports whether the tenant has a record with id.
func (s *Store) Exists(ctx context.Context, tid tenant.TenantID, id tenant.CommandID) (bool, error) {
	if err := checkKey(tid, id); err != nil {
		return false, nil
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM commands WHERE tenant = ? AND id = ?`, tid, id).Scan(&n); err != nil {
		return false, fmt.Errorf("store: exists: %w", err)
	}
	return n > 0, nil
}

// updateRunning runs query, an UPDATE whose last two parameters are the
// tenant and id and whose WHERE clause keeps only a running record, and maps
// "no row changed" to ErrNotFound or ErrNotRunning.
func (s *Store) updateRunning(ctx context.Context, tid tenant.TenantID, id tenant.CommandID, query string, args ...any) error {
	if err := checkKey(tid, id); err != nil {
		return ErrNotFound
	}
	res, err := s.db.ExecContext(ctx, query, append(args, tid, id)...)
	if err != nil {
		return fmt.Errorf("store: update: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update: %w", err)
	}
	if n == 1 {
		return nil
	}
	ok, err := s.Exists(ctx, tid, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return ErrNotRunning
}

// MarkStarted records when a running command started on the target.
func (s *Store) MarkStarted(ctx context.Context, tid tenant.TenantID, id tenant.CommandID, at time.Time) error {
	return s.updateRunning(ctx, tid, id, `UPDATE commands SET started_at = ? WHERE status = 'running' AND tenant = ? AND id = ?`, micros(at))
}

// MarkKillRequested records a kill request on a running command.
func (s *Store) MarkKillRequested(ctx context.Context, tid tenant.TenantID, id tenant.CommandID, at time.Time) error {
	return s.updateRunning(ctx, tid, id, `UPDATE commands SET kill_requested_at = ? WHERE status = 'running' AND tenant = ? AND id = ?`, micros(at))
}

// MarkMayStillRun records that the gateway can no longer tell whether the
// running command's remote process stopped.
func (s *Store) MarkMayStillRun(ctx context.Context, tid tenant.TenantID, id tenant.CommandID) error {
	return s.updateRunning(ctx, tid, id, `UPDATE commands SET may_still_run = 1 WHERE status = 'running' AND tenant = ? AND id = ?`)
}

// Finish moves a running record to a final status (not lost: see
// RecoverAllTenants). A record that has already finished is ErrNotRunning
// and is not changed.
func (s *Store) Finish(ctx context.Context, tid tenant.TenantID, id tenant.CommandID, f *Final) error {
	if !CanTransition(StatusRunning, f.Status) || f.Status == StatusLost || f.FinishedAt.IsZero() {
		return ErrInvalid
	}
	return s.updateRunning(ctx, tid, id, `UPDATE commands SET status = ?, exit_code = ?, signal = ?, error_code = ?, finished_at = ?,
		stdout_bytes = ?, stderr_bytes = ?, stored_bytes = ?, truncated = ?, may_still_run = ?
		WHERE status = 'running' AND tenant = ? AND id = ?`,
		f.Status, nullInt(f.ExitCode), f.Signal, f.ErrorCode, micros(f.FinishedAt),
		f.StdoutBytes, f.StderrBytes, f.StoredBytes, boolInt(f.Truncated), boolInt(f.MayStillRun))
}

// Sizes reports the output a running record has on disk, for
// RecoverAllTenants: the bytes stored per stream.
type Sizes func(r *Record) (stdout, stderr int64)

// RecoverAllTenants marks every running record of every tenant lost with
// finished_at set to at and may_still_run set (nobody stopped the remote
// process), records the output sizes that sizes reports for it
// as its byte counts (the gateway that received the output is gone, so what
// is on disk is all that is known), and returns the records as they now are.
// It is the one method that spans tenants; the job engine runs it once at
// start, before it accepts work, and uses the result only to write one audit
// event per record. sizes may be nil (no output).
func (s *Store) RecoverAllTenants(ctx context.Context, at time.Time, sizes Sizes) ([]Record, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: recover: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT `+columns+` FROM commands WHERE status = 'running' ORDER BY tenant, created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("store: recover: %w", err)
	}
	out, err := collect(rows)
	if err != nil {
		return nil, fmt.Errorf("store: recover: %w", err)
	}
	for i := range out {
		r := &out[i]
		var so, se int64
		if sizes != nil {
			so, se = sizes(r)
		}
		r.Status, r.FinishedAt, r.MayStillRun = StatusLost, at.UTC().Truncate(time.Microsecond), true
		r.StdoutBytes, r.StderrBytes, r.StoredBytes = max(so, 0), max(se, 0), max(so, 0)+max(se, 0)
		if _, err := tx.ExecContext(ctx, `UPDATE commands SET status = 'lost', finished_at = ?, stdout_bytes = ?, stderr_bytes = ?, stored_bytes = ?, may_still_run = 1
			WHERE status = 'running' AND tenant = ? AND id = ?`,
			micros(at), r.StdoutBytes, r.StderrBytes, r.StoredBytes, r.Tenant, r.ID); err != nil {
			return nil, fmt.Errorf("store: recover: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: recover: %w", err)
	}
	return out, nil
}

// MaxListLimit is the most records List returns at once.
const MaxListLimit = 100

// Filter selects records for List. Empty fields select everything.
type Filter struct {
	// Server keeps records of this server.
	Server tenant.ServerID
	// Status keeps records with this status.
	Status Status
	// Limit is the most records to return, 1 to MaxListLimit.
	Limit int
	// After continues a listing after the record with this position.
	After *Position
}

// Position is a place in the newest-first order: a record's creation time
// and id.
type Position struct {
	CreatedAt time.Time
	ID        tenant.CommandID
}

// List returns the tenant's records newest first (by created_at, then id),
// filtered by f. It returns at most f.Limit records and whether more follow.
func (s *Store) List(ctx context.Context, tid tenant.TenantID, f Filter) ([]Record, bool, error) {
	if !ValidTenantID(tid) || f.Limit < 1 || f.Limit > MaxListLimit ||
		(f.Server != "" && !ValidServerID(f.Server)) || (f.Status != "" && !f.Status.Valid()) ||
		(f.After != nil && (!ValidCommandID(f.After.ID) || f.After.CreatedAt.IsZero())) {
		return nil, false, ErrInvalid
	}
	var hasAfter int
	var afterAt sql.NullInt64
	var afterID tenant.CommandID
	if f.After != nil {
		hasAfter, afterAt, afterID = 1, micros(f.After.CreatedAt), f.After.ID
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM commands
		WHERE tenant = ?1
		AND (?2 = '' OR server = ?2)
		AND (?3 = '' OR status = ?3)
		AND (?4 = 0 OR created_at < ?5 OR (created_at = ?5 AND id < ?6))
		ORDER BY created_at DESC, id DESC
		LIMIT ?7`,
		tid, f.Server, f.Status, hasAfter, afterAt, afterID, f.Limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("store: list: %w", err)
	}
	out, err := collect(rows)
	if err != nil {
		return nil, false, fmt.Errorf("store: list: %w", err)
	}
	more := len(out) > f.Limit
	if more {
		out = out[:f.Limit]
	}
	return out, more, nil
}

func collect(rows *sql.Rows) ([]Record, error) {
	defer func() { _ = rows.Close() }()
	var out []Record
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FinishedBefore returns up to limit of the tenant's finished records whose
// finished_at is before t, oldest first. Running records are never returned.
func (s *Store) FinishedBefore(ctx context.Context, tid tenant.TenantID, t time.Time, limit int) ([]Record, error) {
	if !ValidTenantID(tid) || limit < 1 {
		return nil, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM commands
		WHERE tenant = ? AND status != 'running' AND finished_at < ?
		ORDER BY finished_at, id LIMIT ?`, tid, micros(t), limit)
	if err != nil {
		return nil, fmt.Errorf("store: finished: %w", err)
	}
	out, err := collect(rows)
	if err != nil {
		return nil, fmt.Errorf("store: finished: %w", err)
	}
	return out, nil
}

// OldestFinished returns up to limit of the tenant's finished records,
// oldest finished first. Running records are never returned.
func (s *Store) OldestFinished(ctx context.Context, tid tenant.TenantID, limit int) ([]Record, error) {
	return s.FinishedBefore(ctx, tid, time.UnixMicro(1<<62), limit)
}

// StoredBytes returns the sum of stored_bytes over the tenant's records.
// A running record counts what was stored when it last changed (0 until it
// finishes); the engine adds its live counts.
func (s *Store) StoredBytes(ctx context.Context, tid tenant.TenantID) (int64, error) {
	if !ValidTenantID(tid) {
		return 0, ErrInvalid
	}
	var n int64
	if err := s.db.QueryRowContext(ctx, `SELECT coalesce(sum(stored_bytes), 0) FROM commands WHERE tenant = ?`, tid).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: stored bytes: %w", err)
	}
	return n, nil
}

// DeleteFinished deletes the tenant's finished record with id. A running
// record is never deleted (ErrRunning); a missing one is ErrNotFound.
func (s *Store) DeleteFinished(ctx context.Context, tid tenant.TenantID, id tenant.CommandID) error {
	if err := checkKey(tid, id); err != nil {
		return ErrNotFound
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM commands WHERE tenant = ? AND id = ? AND status != 'running'`, tid, id)
	if err != nil {
		return fmt.Errorf("store: delete: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete: %w", err)
	}
	if n == 1 {
		return nil
	}
	ok, err := s.Exists(ctx, tid, id)
	if err != nil {
		return err
	}
	if ok {
		return ErrRunning
	}
	return ErrNotFound
}
