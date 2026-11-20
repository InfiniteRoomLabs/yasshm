# Phase 3 -- code-review lane

**Verdict: REQUEST CHANGES** -- 1 BLOCKING, 6 ADVISORY. Reviewed `4886053..1b251fa` by reading only; no gate, test or build was run.

## Findings

1. **BLOCKING -- a start the server never answered is recorded without `may_still_run`.** `internal/jobs/engine.go:371-373` (through `fail`, `:344-363`). `sshclient.Start` gives up on an unanswered `exec` request with an error whose unexported `mayRun` is set (`internal/sshclient/command.go:160`, `errors.go:72`); only `Exec` turns that into `MayStillRun`. The engine calls `Start`, cannot see the flag, and finishes the record as `error` with `may_still_run` false although the command may be running on the target. That contradicts the 5.5 callout ("the request may have arrived") and the GOAL bullet on a missing exit status. Fix: export the flag (`(*sshclient.Error).MayStillRun()`), set `f.MayStillRun` in `fail`, and add an engine test against `sshtest.Options{SilentExec: true}` with a short connect timeout (the jobs tests use `Silent*` only for signals).

2. **ADVISORY -- a kill racing a natural exit can still be recorded `killed`.** `internal/jobs/kill.go:48-60`: `killRequested` is set, a synced database write and the signal round trip follow, and the flag is cleared only after `ErrNotRunning`. If the command exits by itself in that window, `run` (`engine.go:463-466`) reads the flag as true and records `killed` with exit code 0. Choice 6 claims the opposite. Fix: a per-job mutex that `Kill` holds from setting the flag to correcting it and that `run` takes after `Wait` returns, before it reads the flags (bounded by `KillWait`).

3. **ADVISORY -- a failed final write leaves a record `running` until the next restart.** `engine.go:477-479` and `:353`: when `Finish` fails (disk full, or `SQLITE_BUSY` after 5 s because another tool holds the file) the error is reported, the slot is freed and the job forgotten. The record then says `running`, `ReadOutput` never reports `eof` and returns at once even with a wait (`read.go:111`, a busy loop for a polling client), and `Kill` does nothing. Fix: retry `Finish` a few times with a short backoff.

4. **ADVISORY -- `command.finished` is written even when the record refused the change.** `engine.go:477-480`: after `ErrNotRunning` the audit log still gets a second, contradicting `command.finished` (in the restart test: `lost`, then `killed`, for one id). Skip the event on `ErrNotRunning`. Related: nothing stops two gateway processes from opening one data directory, and the second one's `Recover` marks the first one's live commands `lost`. Recommend an exclusive `flock` on a lock file in `store.Open`, now or as a named Phase 5 item.

5. **ADVISORY -- a started command is missing from the live map while two synced writes run.** `engine.go:375-380`: `MarkStarted` and `command.started` come before `e.jobs[...] = j`. In that window `Kill` on the id (visible through `List`) returns "running, not delivered" with no event and no signal, and `Shutdown` past its grace does not close that connection. Fix: register the job right after `client.Start` succeeds.

6. **ADVISORY -- the migration can stamp a newer database down.** `internal/store/store.go:234-246`: the version is re-read inside the transaction but not re-checked; if a newer binary migrated between the two reads, the loop does nothing and `user_version` is set back. Fix: return `ErrTooNew` when the re-read value is larger than `len(migrations)`.

7. **ADVISORY -- janitor accounting.** `internal/jobs/lifecycle.go:215-224`: the row goes first, so a failed `RemoveAll` leaves a directory no pass revisits until the next start, outside the quota; calling the orphan sweep from `Sweep` is safe (a row always exists before its directory). `:176-181`: a command finishing between `StoredBytes` and `liveStored` is counted twice and can cost one extra old record. `liveStored` (`:232-241`) holds the engine lock while waiting for a capture lock held across a file write; copy the jobs out first.

## Checked and found sound

Every exit of `Start` after the slot is reserved releases it once, closes the files, cancels both contexts and leaves a final record. `classify` matches 5.6 and the 5.5 callouts: disk error, shutdown (only when the stop was a cancel), deadline (exit code kept, `may_still_run` in the background-child case), kill, lost connection, foreign signal, no status. Timeout beats a kill request: reasonable, but say so in the callout. A final record cannot change, including through a late finish of an earlier engine. No query runs while a transaction or open rows hold the single connection; final writes use contexts detached from the caller. The capture has one lock, exact counts at the truncation boundary, and never blocks or fails; the snapshot with its `changed` channel cannot miss a wake-up. `page`, `eof`, merged `stderr`, a read during a janitor delete, the cursor, file modes and flags are right. The orphan sweep runs inside `New`, before any `Start`. Audit-before-act holds for both events. Error text is fixed. `deprule`, the `#planned` tags, the model's relationships, changelog and guides match the code. Tests synchronise on output or deadline-bounded polls, hold as root and as a user, and reach every status through the engine.

## The implementer's choices

1, 2, 3, 4, 5, 7, 10, 11, 12, 13: right. 6: right in intent, not guaranteed (finding 2). 8: right; with a broken audit log every attempt adds an `error` row holding the command text until retention. 9: right; the check is not atomic with a concurrent first `Start`, so Phase 5 must not call `Recover` itself.

The `STATE AS OF` callouts in 5.6 and F18 are fair readings: they fill gaps in section 5 and change no locked decision (D7, D10, D11, D12, D14 hold as written).

## Open questions

- **Engine in the binary before Phase 5:** no. Leave `cmd/yasshm` alone; linking it now adds size and no behaviour or test.
- **`ErrShuttingDown`:** keep the code `internal` (the code set of 5.8 is closed); Phase 5 matches the sentinel with `errors.Is` and returns a fixed "shutting down, retry later" message.
