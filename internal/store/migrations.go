package store

// migrations is the ordered, forward-only schema history. Entry i takes the
// database from user_version i to i+1. Never edit or reorder an entry that
// has shipped: append a new one.
var migrations = []string{
	// 1: the commands table (spec 5.6). The primary key and every index
	// start with tenant (spec D7). Times are UTC unix microseconds.
	`CREATE TABLE commands (
		tenant            TEXT    NOT NULL,
		id                TEXT    NOT NULL,
		principal         TEXT    NOT NULL,
		request_id        TEXT    NOT NULL,
		server            TEXT    NOT NULL,
		command           TEXT    NOT NULL,
		status            TEXT    NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'killed', 'timed_out', 'lost', 'error')),
		exit_code         INTEGER,
		signal            TEXT    NOT NULL,
		error_code        TEXT    NOT NULL,
		created_at        INTEGER NOT NULL,
		started_at        INTEGER,
		finished_at       INTEGER,
		kill_requested_at INTEGER,
		stdout_bytes      INTEGER NOT NULL CHECK (stdout_bytes >= 0),
		stderr_bytes      INTEGER NOT NULL CHECK (stderr_bytes >= 0),
		stored_bytes      INTEGER NOT NULL CHECK (stored_bytes >= 0),
		truncated         INTEGER NOT NULL CHECK (truncated IN (0, 1)),
		may_still_run     INTEGER NOT NULL CHECK (may_still_run IN (0, 1)),
		merge_stderr      INTEGER NOT NULL CHECK (merge_stderr IN (0, 1)),
		timeout_seconds   INTEGER NOT NULL CHECK (timeout_seconds > 0),
		CHECK ((status = 'running') = (finished_at IS NULL)),
		PRIMARY KEY (tenant, id)
	) STRICT, WITHOUT ROWID;
	CREATE INDEX commands_by_created ON commands (tenant, created_at DESC, id DESC);
	CREATE INDEX commands_by_finished ON commands (tenant, status, finished_at);`,
}
