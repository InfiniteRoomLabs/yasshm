# Phase 3 -- simplification lane

Range `4886053..1b251fa`. Read-only: nothing was run. Every proposal keeps statuses, codes, messages, schema, audit events, paths, modes, paging and exported signatures. Line counts are net estimates.

## Proposals

1. **APPLY-RECOMMENDED -- share the leak-check scaffolding.** `internal/jobs/harness_test.go:295-355` (`poll`, `goroutines`, `checkLeaks`) copies `internal/sshclient/harness_test.go:137-200`; `privDir` is in `jobs/harness_test.go:286` and `store/store_test.go:28`; `keyLine` is in both harnesses. After: `internal/testkit/leakcheck` with `Check(t)` and `Poll(d, cond)` (the sshclient filter, which also matches `pkg/sftp`, is the superset), `PrivDir(t)` beside it, `(*sshtest.Server).HostKeyLine()`. Test-only; `deprule` already lets tests import `internal/testkit`; coverage is unaffected. About -70 lines. Risk: low; it touches Phase 2 test files.

2. **APPLY-RECOMMENDED -- `job.mu` and two bools -> two `atomic.Bool`.** `kill.go:48-50,58-60,64-66`, `engine.go:135-137,463-465,576-578`. Before: `j.mu.Lock(); j.killRequested = true; j.mu.Unlock()`. After: `j.killRequested.Store(true)`. The flags are never written together and `Kill` already drops the lock between them, so no reader loses a consistent pair. -9 lines, one mutex. Risk: low.

3. **APPLY-RECOMMENDED -- standard-library one-liners.** `engine.go:600-608`: `sortedTenants` -> `slices.Sorted(maps.Keys(e.tenants))` at its one caller (`lifecycle.go:163`). `store/records.go:45-52`: the `CanTransition` loop -> `slices.Contains(transitions[from], to)`. `slices` is already used in `internal/config`. -12 lines. Risk: none.

4. **APPLY-RECOMMENDED -- the quota loop in `Sweep`.** `lifecycle.go:182-205`: label the outer loop, `break quota` at the two inner breaks, drop the counter `n` and the trailing `if n < len(recs) && used > lim.MaxDataBytes`. Each inner break already ends the outer loop (quota met: its condition is false; deletion failed: the trailing test is true), and a full batch goes on to the next fetch as now. -4 lines. Risk: low.

5. **OPTIONAL -- the `ReadOutput` wait loop.** `read.go:78-131`: hoist `done` out of the loop (a nil channel takes `default`, so the `if j != nil` wrapper goes); `finished || (j == nil && !finished)` is `finished || j == nil`; replace the timer, `expired` and `stop` with `wctx, cancel := context.WithTimeout(ctx, req.Wait)` and the test `wctx.Err() != nil` (with `Wait <= 0` it is born expired). Same pages on output, finish, expiry and context end (`read_test.go:116-158`). -10 lines. Risk: low to medium; the loop is subtle.

6. **OPTIONAL -- the `Shutdown` grace wait.** `lifecycle.go:105-127`: `gctx` from `context.WithTimeout(ctx, grace)`, then one `select { case <-all: case <-gctx.Done(): /* close what is left */ <-all }` replaces the timer and the second select. When `all` is closed `e.jobs` is empty (a job leaves the map before it releases its slot), so a lost race closes nothing. -7 lines. Risk: low.

7. **OPTIONAL -- `janitorStop` -> `e.root.Done()`.** `lifecycle.go:93,142`, `engine.go:163,212`: `Shutdown` cancels the root on the line before it closes `janitorStop`. -3 lines, one field. Risk: low.

8. **OPTIONAL -- one rows-affected helper in `store`.** `records.go:217-235` and `430-448` repeat exec, `RowsAffected`, `n == 1`, `Exists`. After: `execOne(ctx, op, tid, id, query, args...) (changed, exists bool, err error)`; `op` keeps the `store: update:` and `store: delete:` prefixes. -8 lines. Risk: low.

9. **OPTIONAL -- small inlines.** `checkID` (`engine.go:535`, one caller); `defaultOpenOutput` (`output.go:79`: `createOutput` can return `io.WriteCloser`); `newError` (`errors.go:60`, two callers); the two `if j.cap != nil` blocks in `fail` (`engine.go:345-351`) as one; `setUserVersion` into `bump` (`engine_test.go:564-588`). -20 lines. Risk: none.

10. **DO-NOT-APPLY -- `sync.OnceValue` for `Shutdown`.** A second caller would stop honouring its own context (`engine_test.go:500`).

11. **DO-NOT-APPLY -- `sync.Cond` for `capture.changed`.** A `Cond` cannot sit in a `select` with the done channel, the timer and the context.

12. **DO-NOT-APPLY -- merge `store.CheckPrivateDir`, `ensureFile` and `jobs.mkdirPrivate`.** Different calls (`Stat`, `fstat`, `Lstat`), an owner check in two of three, distinct fixed messages: observable.

## Left alone, and why

- `Record` and `Status` are aliases, not copies. `store.Final` (keeps `Finish` off the immutable columns) and `ListFilter` beside `store.Filter` (opaque cursor, default limit) are parallel on purpose.
- SQL: `columns`, `scanRecord`, `collect` and `updateRunning` already hold the shared parts; `List` is one static statement.
- `Start`'s `fail` closure and `classify` are already the one failure path and the one status mapping. The `acted` flag stays: `Before` returns the write error and the act's error alike.
- Repeated id and tenant checks (`tenant`, `live`, `commandDir`): defence in depth for D7.
- The seams named in the work order. Comments: those read carry a source or a deliberate choice.
