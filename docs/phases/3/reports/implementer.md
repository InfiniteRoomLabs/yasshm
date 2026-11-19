# Phase 3 -- implementer report (jobs, store, outputs)

Branch `claude/serene-franklin-jv4pu7`, phase base `4886053`. The run was interrupted once by a container restart after the store commit had been pushed; nothing of `internal/jobs` had been written yet, so the work went on from the pushed head with nothing lost.

## Result

- Gate: `mise run check` -> `check.sh: all OK` on the clean tree at `3821ff1` (the last code commit before this report), `cover: total 94.7% (floor 85%, internal/testkit excluded)`. `git status --porcelain` empty before and after.
- CI: `check completed success` on `edb7723` (engine) and on `3821ff1` (docs head), read with `gh api repos/InfiniteRoomLabs/yasshm/commits/<sha>/check-runs`. The real-`sshd` lane was untouched by this phase and stays green there.
- Fuzz: `mise run fuzz` ran every target for 30 seconds, all PASS, no crasher; `FuzzPage` (the new target) was also run alone: 650854 execs in 30 s, PASS.
- `mise run check -- vuln`: `No vulnerabilities found.` `mise run build`: static binaries for linux/amd64 and arm64 as before.

## Packages and exported API

### `internal/store` (imports only `tenant` from the module)

- `Open(ctx, dataDir) (*Store, error)`, `(*Store).Close(ctx)`, `Version(ctx)`, `SchemaVersion()`, `CheckPrivateDir(dir)`, `FileName` (`yasshm.db`).
- Records: `Insert(ctx, tid, *Record)`, `Get(ctx, tid, id)`, `Exists(ctx, tid, id)`, `MarkStarted`, `MarkKillRequested`, `MarkMayStillRun`, `Finish(ctx, tid, id, *Final)`, `List(ctx, tid, Filter) ([]Record, more, error)` with `Filter{Server, Status, Limit, After *Position}`, `FinishedBefore(ctx, tid, t, limit)`, `OldestFinished(ctx, tid, limit)`, `StoredBytes(ctx, tid)`, `DeleteFinished(ctx, tid, id)`.
- Cross-tenant, named as such: `RecoverAllTenants(ctx, at, Sizes)` (the engine runs it once at start; the janitor's enumeration walks the output directory, not the database).
- Types: `Status` with `Valid`, `Finished`; `CanTransition(from, to)`; `Record`, `Final`, `Position`, `Sizes`, `MaxListLimit` (100); `ValidTenantID`, `ValidCommandID`, `ValidServerID`.
- Errors (`errors.Is`): `ErrNotFound`, `ErrNotRunning`, `ErrRunning`, `ErrTooNew`, `ErrUnsafe`, `ErrInvalid`, `ErrExists`.

### `internal/jobs` (imports only `tenant`, `sshclient`, `store`, `audit`)

- `New(ctx, Config) (*Engine, error)` with `Config{DataDir, Tenants, Audit Auditor, Now, Rand, OnError, JanitorInterval, SSHOptions}`; `Auditor` is the `Before`/`Write` half of `*audit.Logger`.
- `Start(ctx, tid, by audit.Actor, sid, command, StartOptions{TimeoutSeconds, MergeStderr *bool}) (Record, error)`, `Wait(ctx, tid, id, d)`, `Get(ctx, tid, id)`, `List(ctx, tid, ListFilter{Server, Status, Limit, Cursor}) (ListPage{Records, NextCursor}, error)`, `Kill(ctx, tid, by, id, sig) (Record, delivered bool, error)`, `ReadOutput(ctx, tid, id, ReadRequest{Stream, Offset, MaxBytes, Wait}) (Output{Data, Start, Next, Size, EOF, Record}, error)`, `Recover(ctx) (int, error)`, `Shutdown(ctx)`, `Sweep(ctx)` (one janitor pass).
- Errors: `*Error` with `Code()`, `Limit()`, `Unwrap()`; `Code` values `invalid_argument`, `not_found`, `busy`, `internal` plus the SSH codes passed through; `CodeOf(err)`; sentinels `ErrShuttingDown`, `ErrAuditFailed`; `LimitPerTenant`, `LimitPerServer`. Messages are fixed text (no host, user, key path, file path or other tenant's ids); the cause is in `Unwrap` for operator logs.
- Re-exports: `Record`, `Status` and the seven status constants; `StreamStdout`, `StreamStderr`; `SignalTerm`, `SignalInt`, `SignalKill`; `DefaultListLimit` (20), `DefaultJanitorInterval` (1 h).

## Schema (migration 1, `PRAGMA user_version` = 1)

`commands` (STRICT, WITHOUT ROWID): `tenant`, `id`, `principal`, `request_id`, `server`, `command`, `status` (CHECK in the seven), `exit_code`, `signal`, `error_code`, `created_at`, `started_at`, `finished_at`, `kill_requested_at` (UTC unix microseconds, NULL = not yet), `stdout_bytes`, `stderr_bytes` (received), `stored_bytes` (on disk), `truncated`, `may_still_run`, `merge_stderr` (0/1), `timeout_seconds` (> 0); CHECK `(status = 'running') = (finished_at IS NULL)`; PRIMARY KEY `(tenant, id)`. Indexes `commands_by_created (tenant, created_at DESC, id DESC)` and `commands_by_finished (tenant, status, finished_at)`. Every statement has `tenant = ?` in its WHERE clause or key, except `RecoverAllTenants`.

Connection: one `*sql.DB`, `SetMaxOpenConns(1)`; DSN `_pragma=busy_timeout(5000)`, `foreign_keys(1)`, `synchronous(FULL)`, `_txlock=immediate`; `journal_mode=WAL` set after the version check (it is a write). `Close` runs `wal_checkpoint(TRUNCATE)`.

## State machine

| From | To | Where |
|---|---|---|
| `running` | `succeeded` | exit 0 |
| `running` | `failed` | non-zero exit, or a signal the gateway did not send (signal recorded) |
| `running` | `killed` | after `Kill`, or ended by `Shutdown` |
| `running` | `timed_out` | the command's timeout (exit code kept when the shell exited and a background child held the session; `may_still_run`) |
| `running` | `error` | audit failure before start (`internal`), output files not creatable (`internal`), connect or start failure (the SSH code), disk error while writing (`internal`), lost connection or no status (`ssh_unreachable`) |
| `running` | `lost` | only `RecoverAllTenants` at engine start (`may_still_run` set, byte counts from disk) |
| any final | -- | never; every UPDATE has `AND status = 'running'` and checks the affected rows |

The mapping from `sshclient.ExitStatus` lives in one function, `classify` in `internal/jobs/engine.go`; it branches on the disk-error flag, the cancel cause (`errShutdown`), `Stopped == StopDeadline` / `CodeTimeout`, the kill flag, the error code and the outcome.

## Tests

| Package | Top-level tests | Passing tests and subtests (`-v`) |
|---|---|---|
| `internal/store` | 7 | 38 |
| `internal/jobs` | 18 (incl. `FuzzPage`) | 113 |
| `internal/deprule` | 2 | 15 (now covers `store` and `jobs`) |

Every status is reached through the real engine against `sshtest`: succeeded, failed (code), failed (signal `USR1`), timed_out, timed_out with a background child (`may_still_run`, exit 0 kept), killed (TERM delivered), killed after a refused signal (`RefuseSignal`, `may_still_run`), error/`ssh_unreachable` (`NoExitStatus`, `DropMidStream`, a closed server), error/`host_key_mismatch`, error/`ssh_auth_failed`, error/`internal` (audit failure, output files, `/dev/full`), lost (restart). Also: busy for both limits naming the limit, another tenant not affected, slots free after success, kill, audit failure, connect failure, output failure and id failure (`noSlots` would fail a leaked slot); truncation with 3 000 000 bytes against a 1000-byte limit (the command exits 0, so the remote side never blocked); paging while writing with no gap or repeat; a waiting reader woken by output, by the finish, by expiry and by context end; Wait with time up; restart recovery (engine dropped without Shutdown, a second engine on the same directory: `lost`, output readable with `eof`, slots free, new commands run, the first engine's late finish cannot change the record); a database newer than the binary (store and `New`); migrations from an empty file and idempotent reopen; WAL, `-wal`/`-shm` 0600; Shutdown (killed, twice safe, Start and Get after it are `ErrShuttingDown`, no goroutine, connection, remote process or file under the data directory left); Shutdown past the grace (`SilentSignal`: connections closed, `killed` with `may_still_run`); a concurrent second Shutdown honouring its context; 24 concurrent Starts with Kill/ReadOutput/List/Get under `-race`; two tenants that cannot Get, List, Kill, Wait or ReadOutput each other's commands, a tenant-a cursor in tenant b listing nothing, disjoint paths; hostile ids (`..`, separators, SQL text, upper case) as `invalid_argument` through the public API and in `commandDir`; the janitor's retention (record and directory gone, running and other-tenant untouched), quota (oldest finished first, per tenant), start-up orphan sweep, and its goroutine on a 1 ms interval with an injected clock. Subtest names start with the path class.

Coverage: gate total 94.7 percent (was 95.7 at the Phase 2 ship; this phase's packages are 92.8 percent `jobs` and 90.7 percent `store` measured alone).

## Gate tail (`mise run check` at `3821ff1`, LikeC4 log lines removed)

```
== sshd ==
check.sh: sshd is not installed here; the real-sshd lane runs in CI only (spec section 7)
== cover ==
cover: total 94.7% (floor 85%, internal/testkit excluded)
== vuln ==
No vulnerabilities found.
== build ==
[build] $ scripts/build.sh
build: dist/yasshm_3821ff1_linux_amd64
build: dist/yasshm_3821ff1_linux_arm64
build: dist/SHA256SUMS
== arch ==
check.sh: all OK
```

## Commits (`git log --oneline 4886053..HEAD` before this report)

```
3821ff1 docs: record the Phase 3 engine and store in the spec and guides
edb7723 feat(jobs): add the job engine, output files and janitor
c4f40ef feat(store): add the SQLite command record store
```

`git status --porcelain`: empty.

## Dependency (D14)

`modernc.org/sqlite` v1.62.0 (released 2026-11-07). Compared with `github.com/ncruces/go-sqlite3` v0.37.0 (same date) in a scratch module: both build with `CGO_ENABLED=0` for linux/amd64 and linux/arm64 into statically linked binaries, both return `wal`, both pass a 50-goroutine insert test under `-race`. Size added to a stripped probe (`database/sql` alone 1.76 MB amd64): modernc +4.9 MB amd64 / +4.8 MB arm64, ncruces +6.4 MB / +6.1 MB. Chosen for the smaller binary, the widest use, SQLite's own code (translated from C) and plain `database/sql` with `_pragma` DSN options per connection. Cost: more modules. Transitive modules in the build graph: `modernc.org/libc` v1.80.1, `modernc.org/mathutil` v1.7.1, `modernc.org/memory` v1.12.1, `github.com/dustin/go-humanize` v1.1.0, `github.com/google/uuid` v1.6.0, `github.com/remyoudompheng/bigfft` v0.0.0-20230129092748-24d4a6f8daec, `golang.org/x/sys` (already present). `go.mod` also lists `github.com/mattn/go-isatty` v0.0.24 and `github.com/ncruces/go-strftime` v1.1.0 as indirect requirements (not compiled into the binary on linux). govulncheck clean. Recorded in spec F18 (now CONFIRMED).

Binary size: the shipped binary is unchanged in this phase (4 210 848 bytes amd64, 3 932 320 arm64 before and after), because `cmd/yasshm` does not import `jobs` or `store` yet; expect about +4.9 MB when Phase 5 wires the engine in.

## Spec discrepancies, ambiguities and choices

1. **Engine signatures.** `Start` and `Kill` take an `audit.Actor` (principal plus request id) instead of a bare principal, because the command events need the request id; `Kill` takes it before the id. `ReadOutput` takes a request struct and returns `start` and `size` too. Spec 5.6 callout.
2. **Lost records say `may_still_run`.** Nobody stopped the remote process when the gateway died. Their byte counts are the sizes on disk. Spec 5.6 callout.
3. **Extra record fields:** `request_id`, `stored_bytes`, `may_still_run`, `merge_stderr`. `stdout_bytes`/`stderr_bytes` are bytes received (as in the audit `Finish`), `stored_bytes` is what is on disk (used by the quota).
4. **`synchronous=FULL`**, not NORMAL: few writes, and a finished record should survive power loss like its synced audit event.
5. **Command ids are lower hex** (`^[0-9a-f]{32}$`). A malformed id is `invalid_argument`; a well-formed id of another tenant is `not_found`; an unknown tenant is `not_found`.
6. **A refused or unanswered kill signal** is not an error: `delivered` false, the session closes (SSH layer), the record says `may_still_run` and ends `killed`. A signal that races a natural exit (`ErrNotRunning`) does not count as a kill.
7. **Kill after a finished command** is not audited (nothing is done). A kill whose audit write fails sends nothing and returns `ErrAuditFailed`.
8. **Audit failure on `command.requested`:** the record exists as `error`/`internal` (so the operator sees the attempt in `list_commands`), no output directory is created, the target is never contacted; no `command.finished` is written for it (the logger is failing anyway).
9. **`Recover` is exported** as the spec lists it, runs inside `New`, and refuses after the first `Start` (a running record then belongs to this engine).
10. **Shutdown grace** is the largest `shutdown_grace_seconds` across tenants; `ctx` can cut it short. After it, remaining connections are closed and those records say `may_still_run`.
11. **Janitor quota** counts records plus the live stored bytes of running commands; it deletes only finished records. Interval 1 hour by default (not in the spec; not a config key).
12. **Architecture:** `jobs`, `store`, `outputs` lost `#planned`; the `jobs` description no longer says "queue" (there is none); a `jobs -> audit` relationship was added (the engine writes the command events), in `model.c4` and the section 4 relationship list. The existing `jobs -> ssh 'runs commands and transfers files'` still says file transfers go through jobs; they will go from the tools to the SSH layer in Phase 5 -- left for that phase to correct.
13. **Commit trailer:** the work order asked for a `Claude Fable 5.2` co-author line; the harness attribution for this session names `Claude Opus 5.5` (the model that wrote the commits), and `CLAUDE.md` says the trailer follows the harness, so the commits carry that one. The `Claude-Session` line is as asked.

## Open questions for the lead

- Should the shipped binary carry the engine before Phase 5 (it would add about 4.9 MB now with nothing calling it)? I left `cmd/yasshm` alone.
- Phase 5 must map `jobs.CodeOf` to tool codes; `ErrShuttingDown` is `internal` today -- a tool might prefer a distinct message.
