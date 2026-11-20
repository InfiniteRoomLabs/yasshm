# Phase 6 QA report -- end-to-end and hardening

Verdict: **NEEDS WORK** -- 2 blocking, 2 advisory.

Subject: `97be900` (code head `9e854ec`, base `ce5df4f`). Experiments ran in a private clone, now deleted; the repo tree stayed clean.

## Findings

**1. BLOCKING -- security finding 1 reproduced: forged-signature rejections write audit lines without bound** (`internal/auth/middleware.go:124`, `:197-204`).

- Test: the `internal/auth` fixture, the real `NewGuard` and `audit.Open` log, 2000 requests each. The forged token uses public data only: header `{"alg":"RS256","kid":<the served kid>}`, 256 random signature bytes.
- Expected from "their audit lines are bounded" (`README.md:21`, spec line 330): at most 60 lines and a summary per minute.

| Token | Audit lines | Bytes | Time under `-race` | Without |
|---|---|---|---|---|
| forged signature | 2000 (`bad_signature`) | 309,785 | 912 ms | 496 ms |
| malformed | 61 (60 + `suppressed`) | 9,525 | 42 ms | 22 ms |

- About 155 bytes and one sync (248 us) per request: one sequential caller writes roughly 4000 lines and 0.6 MB a second on this disk (computed).
- The code follows spec 5.7 revision (e) (`bad_signature` is always written); it contradicts the Phase 6 sentences in section 6 and the README. Change the code and 5.7, or withdraw the sentences.

**2. BLOCKING -- `TestE2EOutputLimit` is flaky** (`test/e2e/commands_test.go:125-129`, same assumption at `:132`).

- Expected by the test: `stdout` is 1024 `a`. Observed: `run = status succeeded truncated true stdout_bytes 500001 stdout 1015 bytes`. 1015 = 1024 - 9, and 9 is `finished\n`, which the command writes to stderr.
- Cause: `max_output_bytes` is one allowance for both streams in arrival order (`internal/jobs/output.go:130-137`, spec 5.6), so stderr can take its 9 bytes first. The product is within the spec; the assertion is not.
- Frequency: once in 31 full-suite runs (under mutation (h), which does not touch output); 4 of 150 with `go test -race -count=150 -run 'TestE2EOutputLimit$' ./test/e2e` under 6 busy loops on 4 CPUs (a targeted repeat, outside the 20 suite runs).
- Fix: drop the stderr write, or assert that stdout plus stderr kept is 1024 and stdout is all `a`.

**3. ADVISORY -- the `e2e` step does not notice a removed scenario** (`scripts/check.sh:121`, `:135`). With `TestE2EBusy` renamed to a non-test the step printed `e2e: 18 of 18 end-to-end scenarios passed` (exit 1 came only from the dirty clone) and `internal/deprule` stayed green. Nine scenarios are in no checked table: `Healthz`, `OutputLimit`, `Busy`, `PolicyDenied`, `UploadDenied`, `LostAfterKill`, `ConfigCheck`, `SecondGateway`, `FileSizeLimit`. Fix: have `deprule` check the spec 7 callout's scenario -> test map.

**4. ADVISORY -- mutation (e) survives the suite.** `TestE2EAuditUnwritable` fails `tool.called`, so `internal/mcpserver/call.go:181` returns before the engine and `command.requested` never fails. `internal/jobs/engine_test.go:202` covers it in-process (read, not run against the mutant).

## Evidence

**1. Gate and CI.** `mise run check` on `97be900`, 67 s, `git status --porcelain` empty before and after:

```
check.sh: sshd is not installed here; the real-sshd lane runs in CI only (spec section 7)
e2e: 19 of 19 end-to-end scenarios passed against the real binary in 4s (uid 0)
cover: total 94.7% (floor 85%, internal/testkit excluded)
check.sh: all OK
```

CI: `check completed success` on `97be900` (job 136149969744) and `9e854ec`. Job log of `97be900`: `sshd: 32 of 32 contract scenarios passed against real sshd, and the login test`; `e2e: 19 of 19 end-to-end scenarios passed against the real binary in 27s (uid 1001)`; `cover: total 94.7%`.

**2. Deliverables (`GOAL.md` stage 2).** All exist; two are met in part.

- Binary and harness: met. `harness_test.go:113-116` has the flags of `scripts/build.sh` (`CGO_ENABLED=0`, `-trimpath`, `-s -w`, three `-X`); the suite imports test kits only and refuses a non-loopback URL (`:520`, `:569`).
- Spec 7 scenarios: 19 tests, mapped as the spec 7 callout says, none missing or weaker than its sentence; two tenants in-process plus `TestE2ETwoTenantsRefused`, as the goal allows. Met in part: finding 2.
- Guarantee table: met. Renaming `TestE2EHostKeyMismatch` fails `TestSecurityModelTable` (`test/e2e has no test TestE2EHostKeyMismatch`).
- `e2e` step: a forced failure gives `e2e: the end-to-end suite failed`; `test` leaves the package out; `usage lint` is clean. Met in part: finding 3.
- Phase 5 leftovers, leak checks, coverage, no new dependency (`go.mod`, `go.sum` unchanged): met.

**3. Mutations** (`go test -race ./test/e2e` after each):

| | Mutation | Caught by |
|---|---|---|
| a | guard not mounted | 17 of 19 scenarios |
| b | no `Decide` for `run_command` | `TestE2EPolicyDenied`, `TestE2EResourceBounds` |
| c | any host key accepted | `TestE2EHostKeyMismatch` |
| d | `upload_file` at `look` | `TestE2EUploadDenied`, `TestE2EEveryTool` |
| e | failed `command.requested` write ignored | **survived** (finding 4) |
| f | `Recover` marks nothing | `TestE2ELostAfterKill` |
| g | no `gateway.stopped` | `TestE2EGracefulShutdown`, `TestE2EAuditUnwritable` |
| h | batch refusal removed | `TestE2EHTTPSurface` |

(d) used `look(...)`, which also drops `WritesFiles`; `Requires` alone is an equivalent mutant (`internal/policy/policy.go:209` forces `full`; read, not run).

**4. Stability.** The suite in 20 separate invocations: 20 of 20, 97 s, 4.6 to 5.4 s each. Afterwards no `yasshm` or `e2e.test` process (so no listener) and no `yasshm-e2e-*` directory. `mise run check -- test` once: green, 53 s. `internal/jobs` 15 times under 4 busy loops on 4 CPUs: 15 of 15, 176 s; the `TestStatuses` failure did not reproduce.

**5. Fidelity.** My own scenario against the real binary, expectations written first, passed on its first run. Two commands back to back; the first killed with `KILL` (`killed`); the second paged to the end in 4000-byte pages (58,012 bytes, `succeeded`) while a second session downloaded a 204,800-byte random file in 30,000-byte pages; both reassembled byte for byte. A third command, then `SIGTERM`: exit 0. Audit log, 88 events over 32 request ids: every request id begins `auth.accepted`, `tool.called`; the killed command reads `requested, started, kill_requested, finished(killed)`; first event `gateway.started`, last `gateway.stopped` (signal).

**6. Prefilter.** `FuzzCommandGuardRail` for 60 s: 271,062 executions, no crasher. Differential: the 256 table cases in seven forms (plain, double-quoted, single-quoted, leading backslash, `env X=1`, after `;`, inside `$( )`), 1792 commands, decided with the prefilter and with every `Needs` emptied: 933 denied, 859 allowed, **0 differences**.

**7. Limits.** The suite listens on `127.0.0.1:0`; targets, issuer and edge are on loopback. My tooling: no `curl` or `wget`; Go tests with `httptest` recorders or the suite's fixture; `gh api` for CI. No credential. What I only read is marked.

**8. Docs.** `README.md:56`, `TESTING.md:3` and `CLAUDE.md:83` list `e2e`; the changelog has the Phase 6 entries; the section 6 tables and the section 7 callout match the code, except "bounded" (finding 1). Tracked `.md` files are ASCII; the placeholder grep prints nothing.
