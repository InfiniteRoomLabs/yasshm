# Phase 3 -- QA lane

**Verdict: NEEDS WORK** -- 3 BLOCKING, 5 ADVISORY. Head `b04af3c` (code `3821ff1`). Experiments ran in a clone outside the repo (deleted).

## Gate and CI

`mise run check` on the clean tree: exit 0, `git status --porcelain` empty before and after.

```
== cover ==
cover: total 94.7% (floor 85%, internal/testkit excluded)
== vuln ==
No vulnerabilities found.
check.sh: all OK
```

Coverage by hand from `coverage.out`: 2311 of 2440 statements, 94.71%. CI: `check completed success` on `b04af3c` and `3821ff1`; both job logs say `sshd: 32 of 32 contract scenarios passed against real sshd, and the login test`. `dist/` binaries are statically linked (`file`, `ldd`).

## Findings

1. **BLOCKING -- an unanswered `exec` leaves `may_still_run` false** (`internal/jobs/engine.go:344-373`). Code-review finding 1 reproduced with `sshtest.Options{SilentExec: true}` and a 700 ms connect timeout. Expected `may_still_run` true (the server logged `[exec]`); observed `error`, `ssh_unreachable`, `may_still_run=false`.

2. **BLOCKING -- `max_data_bytes` is not enforced between janitor passes, and a full disk stops every tenant** (`internal/jobs/lifecycle.go:135-147`, `engine.go:323`, `:477`). Reproduced security finding 1. Quota 1 MiB, 20 commands of `head -c 1048576 /dev/zero`: 20971520 bytes on disk, no `Start` refused; one `Sweep` left 1048576. On a 6 MiB tmpfs holding the data directory and the audit log (`max_output_bytes` 4 MiB): the second command filled it; then every `Start` of both tenants returned `internal` (`store: insert: database or disk is full`); three ended commands stayed `running` for good because `Finish` failed and is not retried; `Kill` said `delivered=true` and the record stayed `running`; `Sweep` failed too (`delete record ...: database or disk is full`), so the janitor cannot free a full disk. The audit latch was not reached.

3. **BLOCKING -- a command is reported finished before its slot is free and its `command.finished` event is written; the gate is flaky** (`engine.go:477-487`: `Finish`, the synced audit write, `close(j.done)`, then `release()`; `Wait` returns as soon as `Get` sees the final record, `:584-586`). Expected: once `Wait` reports the end, the next `Start` is accepted. Observed with `max_concurrent_per_server` 1 and a caller that waits, then starts: `busy` in round 70 while the previous record was `succeeded`. Unmutated `internal/jobs` at HEAD failed 7 of 50 separate runs of `go test -race -shuffle=on -count=1` (5 of 30, and 2 of 20 with `-cpu 8`): `engine_test.go:344` four times and `:317` once (`slots still reserved`), `:156` once (no `command.finished` yet), `:151` once (finding 4). `-run 'TestStartValidation|TestBusy' -count=150` failed 14 times. One `-count=15` run of both packages passed (73 s); `GOMAXPROCS=1` passed. Fix: make the end visible last.

4. **ADVISORY -- `sshtest` answers `exec` after starting the command** (`internal/testkit/sshtest/sshtest.go:351-363`). A fast command can close the channel first; the client reports `internal`. Seen 3 times in about 140 package runs (`read_test.go:270`, `:197`, `engine_test.go:151`).

5. **ADVISORY -- a kill racing a natural exit is recorded `killed`** (`kill.go:48-60`). Reproduced code-review advisory 2. With the database lock held during the kill: `killed`, exit code 0, `delivered=false`. With no lock, `sleep 0.15` killed at about 150 ms: 2 of 60 ended `killed` with exit code 0, one also `may_still_run=true`.

6. **ADVISORY -- a live record can become `lost` and then cannot be killed** (`lifecycle.go:24-30`, `kill.go:41`, `store.go:100`). Reproduced security advisory 2. A second engine on the same directory: the first engine's `Get` says `lost`, `Kill` with `KILL` returns `delivered=false` and no error on both engines, the remote process keeps running, the slot stays reserved. Exported `Recover` raced against a first `Start`: 6 of 150 rounds marked the new command `lost`.

7. **ADVISORY -- surviving mutations (85 tried, 74 killed; all sixteen requested ones killed).** Real gaps: `DeleteFinished` and `MarkKillRequested` without `tenant = ?` (`store_test.go:423` deletes a running record, so the status guard answers, not the tenant); the slot not released when `Insert` fails (`engine.go:324`); the janitor ignoring live bytes (`lifecycle.go:181`); kill-before-timeout order in `classify`; `shutdown_grace_seconds` replaced by 0 or by hours (`lifecycle.go:105`) -- the tests only use the context. Four more survivors are equivalent (redundant guards). The slot leaks on the audit and connect paths were caught only by a test timeout.

8. **ADVISORY -- `README.md:9`** lost a comma: "audit log (`internal/audit`) the SSH layer".

## Deliverables (GOAL stage 2)

Met with evidence: store (WAL, newer schema refused before any write, 0600 files including `-wal` and `-shm` in a 0700 directory); audit-before-act; ids; `busy` names the limit and nothing queues; output modes; `mise run fuzz` runs `FuzzPage`; restart; janitor; tenant isolation; `deprule` (a throwaway import of `config` in `store` and of `policy` in `jobs` failed `TestDependencyRule`); static build; `#planned` gone from `jobs`, `store`, `outputs`; docs ASCII-only; placeholder grep empty. Not met: may-still-run for an unanswered `exec` (1); a gate that is green every time (3). Unproven: the `shutdown_grace_seconds` wait (7).

Statuses: `running` in `TestReadOutput/edge/Wait returns the running record`; `succeeded`, `failed`, `killed`, `timed_out`, `error` in `TestStatuses`; `lost` in `TestRecover/happy`.

## Fidelity (expectation written first; all matched)

(a) 1000 bytes, 7 offsets by 4 limits: 28 of 28 `start`, `next`, `eof` and data equal. (b) 10 lines: 11 calls, 60 bytes in order, no empty return, `eof` only on the last. (c) `trap '' TERM`, timeout 1: `timed_out`, signal `KILL`, no exit code, `may_still_run` false, 1.29 s; events requested, started, finished. (d) 3 MiB against 1 MiB: stored 1048576, `truncated`, received 3145728 and 5, exit 0 in 38 ms. (e) merged: one file with `out`, `err`, `out2` in order; separate: two files. (f) six cross-tenant calls: `jobs: not_found: no such command`, identical to a missing id. (g) 250 commands: pages 100, 100, 50, newest first, no duplicate or gap; tenant a's cursor in tenant b: empty.

## Restart

A test binary (engine, two `sleep 300` commands, third start `busy`) was killed with `SIGKILL`. A new engine on the directory: both records `lost` with `may_still_run`, output readable with `eof`, two new starts accepted and the third `busy`, a fresh command `succeeded`.

## Other

No test child or temp directory after a clean pass. Tests hold as root and as the CI user. Nothing deployed, no real host, no credential.
