// Package store keeps the command records of the job engine in SQLite (spec
// 5.6), through the pure-Go driver modernc.org/sqlite (no CGO, spec D11 and
// D14).
//
// One *sql.DB with one open connection is the only writer. The database runs
// in WAL mode with a busy timeout, foreign keys on and synchronous=FULL. The
// schema is versioned with PRAGMA user_version and an ordered, forward-only
// migration list compiled into the binary; a database newer than the binary
// is refused before anything is written to it.
//
// Tenancy (spec D7): the commands table has (tenant, id) as its primary key
// and every index starts with tenant. Every method takes a tenant.TenantID
// first and every statement filters by it, so a record of another tenant is
// ErrNotFound. The only statement that spans tenants is RecoverAllTenants,
// which the engine runs once at start; it is named as such.
//
// Times are stored as UTC unix microseconds (INTEGER); a zero time.Time is
// stored as NULL.
//
// It imports only tenant from this module.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	// The pure-Go SQLite driver (spec F18); it registers "sqlite".
	_ "modernc.org/sqlite"

	"github.com/InfiniteRoomLabs/yasshm/internal/tenant"
)

// FileName is the database's name inside the data directory.
const FileName = "yasshm.db"

// busyTimeoutMillis is how long a statement waits for a lock held by another
// connection (another process, or a second Store in tests) before it fails.
const busyTimeoutMillis = 5000

// Errors a caller can match with errors.Is.
var (
	// ErrNotFound: no record with that id in that tenant.
	ErrNotFound = errors.New("store: record not found")
	// ErrNotRunning: the record has already finished; a finished record
	// never changes status again.
	ErrNotRunning = errors.New("store: record is not running")
	// ErrRunning: the record is still running, so it cannot be deleted.
	ErrRunning = errors.New("store: record is running")
	// ErrTooNew: the database was written by a newer binary.
	ErrTooNew = errors.New("store: database schema is newer than this binary")
	// ErrUnsafe: the data directory or database file is not private to
	// the gateway's user.
	ErrUnsafe = errors.New("store: unsafe permissions")
	// ErrInvalid: a tenant id, command id or field is not valid.
	ErrInvalid = errors.New("store: invalid argument")
	// ErrExists: a record with that id already exists in the tenant.
	ErrExists = errors.New("store: record already exists")
)

// tenantPattern is the form of a tenant id (spec 5.1, the config's id
// pattern). commandPattern is the form of a command id: 128 random bits as
// 32 lower-case hex digits.
var (
	tenantPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	commandPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	serverPattern  = tenantPattern
)

// ValidTenantID reports whether tid has the form of a tenant id. Only a
// valid id is ever joined into a path or used in SQL.
func ValidTenantID(tid tenant.TenantID) bool { return tenantPattern.MatchString(string(tid)) }

// ValidCommandID reports whether id has the form of a command id (32 lower
// case hex digits).
func ValidCommandID(id tenant.CommandID) bool { return commandPattern.MatchString(string(id)) }

// ValidServerID reports whether sid has the form of a server id.
func ValidServerID(sid tenant.ServerID) bool { return serverPattern.MatchString(string(sid)) }

// Store is the command record store. It is safe for concurrent use.
type Store struct {
	db *sql.DB
}

// Open opens (creating it if needed) the database in dataDir and applies the
// migrations the binary knows. dataDir must exist, be a directory owned by
// the effective uid and give group and other no access (0700 or stricter).
// The database file is created with mode 0600, and an existing one must give
// group and other no access either. A database whose schema version is
// higher than the binary knows is refused with ErrTooNew before anything is
// written to it.
func Open(ctx context.Context, dataDir string) (*Store, error) {
	if err := CheckPrivateDir(dataDir); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, FileName)
	if err := ensureFile(path); err != nil {
		return nil, err
	}
	// Connection settings that are not writes go in the DSN, so a
	// connection the pool replaces gets them too: the busy timeout,
	// foreign keys, and synchronous=FULL (each commit is synced, also the
	// WAL; records are few and small, and a finished record must survive
	// a power loss as the audit event that names it does). journal_mode is
	// persistent and is a write, so it is set only after the version check.
	q := url.Values{}
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeoutMillis))
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(FULL)")
	q.Set("_txlock", "immediate")
	u := url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	// One connection: one writer, and no reader that sees a different
	// snapshot from the writer in the same process.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// CheckPrivateDir returns an error matching ErrUnsafe unless dir is an
// existing directory owned by the effective uid whose mode gives group and
// other no access.
func CheckPrivateDir(dir string) error {
	fi, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("store: data directory: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%w: the data directory is not a directory", ErrUnsafe)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: the data directory gives group or other access (want 0700)", ErrUnsafe)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("%w: the data directory is not owned by the gateway's user", ErrUnsafe)
	}
	return nil
}

// ensureFile creates the database file with mode 0600 if it does not exist
// and checks an existing one: a regular file, not a symbolic link, owned by
// the effective uid, with no group or other bits. SQLite gives the -wal and
// -shm files it creates the mode of the database file.
func ensureFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600) //nolint:gosec // G304: path is the data directory joined with a constant name
	if err != nil {
		return fmt.Errorf("store: database file: %w", err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("store: database file: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: the database is not a regular file", ErrUnsafe)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: the database file gives group or other access (want 0600)", ErrUnsafe)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("%w: the database file is not owned by the gateway's user", ErrUnsafe)
	}
	return nil
}

// Close checkpoints the WAL into the database file and closes it. ctx bounds
// the checkpoint; the database is closed whatever happens to it.
func (s *Store) Close(ctx context.Context) error {
	_, cerr := s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	err := s.db.Close()
	if cerr != nil && err == nil {
		err = fmt.Errorf("store: checkpoint: %w", cerr)
	}
	return err
}

// Version returns the database's schema version (PRAGMA user_version).
func (s *Store) Version(ctx context.Context) (int, error) {
	var v int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("store: read schema version: %w", err)
	}
	return v, nil
}

// SchemaVersion is the schema version this binary writes.
func SchemaVersion() int { return len(migrations) }

// migrate refuses a newer database, sets WAL mode and applies the missing
// migrations in one transaction.
func (s *Store) migrate(ctx context.Context) error {
	v, err := s.Version(ctx)
	if err != nil {
		return err
	}
	if v > len(migrations) || v < 0 {
		return fmt.Errorf("%w: the database has schema version %d and this binary knows versions up to %d; use a newer yasshm or another data directory", ErrTooNew, v, len(migrations))
	}
	var mode string
	if werr := s.db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode); werr != nil {
		return fmt.Errorf("store: set WAL mode: %w", werr)
	}
	if mode != "wal" {
		return fmt.Errorf("store: the database refused WAL mode (journal mode %q)", mode)
	}
	if v == len(migrations) {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// Read the version again inside the write transaction, so two
	// processes opening a new database cannot both apply a migration.
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	for i := v; i < len(migrations); i++ {
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			return fmt.Errorf("store: migration %d: %w", i+1, err)
		}
	}
	// PRAGMA takes no bound parameter; the value is the length of a
	// compiled-in list.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", len(migrations))); err != nil {
		return fmt.Errorf("store: set schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit migration: %w", err)
	}
	return nil
}

// micros converts t to UTC unix microseconds; the zero time is NULL.
func micros(t time.Time) sql.NullInt64 {
	if t.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UTC().UnixMicro(), Valid: true}
}

// fromMicros converts a stored time back; NULL is the zero time.
func fromMicros(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return time.UnixMicro(v.Int64).UTC()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
