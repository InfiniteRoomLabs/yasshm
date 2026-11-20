# Phase 6 implementer report -- End-to-end and hardening

Implementer: general-purpose, opus. Branch `claude/serene-franklin-jv4pu7`, phase base `ce5df4f`. Every commit below was pushed; the head before this report is `9e854ec` (CI `check completed success`).

## Summary

- `test/e2e`: 19 top-level `TestE2E*` scenarios against the real binary, built once per run with the flags of `scripts/build.sh` and started as a child process per scenario (port 0, own data directory, audit log, config, keys), driven with the MCP Go SDK client through `edgetest`. Wall time: 3.5 to 4.2 s locally (20 runs), 5 s in the local gate step, 22 s in CI (about 19 s of it is the cold `go build` on the runner).
- Gate step `e2e` between `sshd` and `cover`; the `test` task leaves `./test/e2e` out. Coverage 94.8 % locally, 94.7 % in CI (floor 85, unchanged; the suite has no statements of its own).
- Section 6: a guarantee -> tests table and a "not guaranteed" -> files table in the spec, checked by `internal/deprule` `TestSecurityModelTable`.
- Hardening: (a) test support moved into the test kit; (b) policy cost cut from 0.25-0.44 s to 0.1-0.5 ms on a typical 64 KiB command, worst case about 0.26 s; (c) full-disk behaviour against the real binary with `RLIMIT_FSIZE` (no mount possible without privileges); (d) `mise run fuzz`: six targets, 30 s each, no crasher.

## Scenarios (test -> the spec sentence it proves)

All in `test/e2e`; the first subtest element is the path class.

| Test | Spec 7 scenario / sentence | What it asserts |
|---|---|---|
| `TestE2EEveryTool` | every tool through the fake edge; D8; spec 6 "Tokens and key material never appear in logs" | all eight tools' structured answers from the binary; `download_file` of a real text file and a binary file (base64); `upload_file` `access_denied` rule `access-level` with nothing sent; `tool.called` for every tool with tenant and principal; `list_servers` shows no host, user, key path, config or data path; exit 0; no `eyJ` (JOSE header) and no line of the private key in the binary's stderr or audit log |
| `TestE2EAuth` | no token, bad token, unmapped principal; spec 6 "No request reaches a tool without a verified principal" | 401 `{"error":"unauthorized"}` (reason `missing_token`), 401 (`bad_signature`), 403 `{"error":"forbidden"}` (`unmapped_principal`), one `auth.rejected` each with no tenant; the MCP client cannot connect; no `tool.called`, no request or connection on the target |
| `TestE2EHTTPSurface` | "A request that goes around it must get 401"; 5.8 batch refusal | around the edge 401; a forged `alg: none` token around the edge 401; a JSON-RPC batch 400 `{"error":"batch requests are not supported"}` with a valid token; unknown path 404 `{"error":"not found"}`; `GET /mcp` 405 |
| `TestE2EHealthz` | `/healthz` | 200 `{"status":"ok","version":"0.0.0-e2e"}`; 503 `{"status":"degraded","detail":"key set not loaded",...}` when `certs_url` is a `loopback.RefusingPort` at start, and an MCP request is 503 then |
| `TestE2ELongCommand` | a command that outlives `wait_seconds`, is paged and is killed | `running` after at least `wait_seconds` (1); 10+ forward pages of 256 bytes rebuild the output exactly; `offset: -15` reads the tail; `kill_command` KILL delivered -> `killed`; `list_commands status=killed`; audit `requested, started, kill_requested, finished` |
| `TestE2ETimeout` | a command that times out | `timeout_seconds: 1` -> `timed_out` with its output; `command.finished` status `timed_out` |
| `TestE2EOutputLimit` | output beyond `max_output_bytes` | `max_output_bytes: 1024`, the command writes 500,001 bytes and still exits on its own (`succeeded`): `truncated`, `stdout_bytes` 500001, kept 1024 bytes; `read_output` size 1024, truncated |
| `TestE2EBusy` | a `busy` rejection | `max_concurrent_per_server: 1`: second command `busy` "a limit is reached: max_concurrent_per_server", nothing sent; the slot frees after the kill |
| `TestE2EPolicyDenied` | a denylisted command | built-in `sudo` (also `"sudo"` quoted), an operator `deny_commands` rule, the `shadow` path rule: `policy_denied` with the rule, a `policy.denied` event; the target saw no request and no connection |
| `TestE2EHostKeyMismatch` | a host key mismatch; spec 6 "never spoken to" | `run_command` and `download_file` `host_key_mismatch`; probe `host_key_ok: false`; the impostor's request log is empty |
| `TestE2EUploadDenied` | `upload_file` denied | text and base64 content: `access_denied` rule `access-level`, two `policy.denied`, no `file.uploaded`, no request on the target |
| `TestE2EAuditUnwritable` | an unwritable audit log blocks the action; spec 6 "An action whose audit event cannot be written does not happen" | per acting tool (7 parallel gateways): `RLIMIT_FSIZE` lowered on the running binary so `auth.accepted` fits and `tool.called` does not -> `internal` "the audit log could not be written, so nothing was done", target untouched, the next request refused by the guard, last complete line `auth.accepted`, exit 0, "gateway.stopped cannot be written" logged |
| `TestE2ELostAfterKill` | killed mid-command, restarted gateway reports `lost` | `SIGKILL` (exit -1, no `gateway.stopped`); a restart on the same data directory reads `lost` with the output `before-the-kill\n` and eof; `list_commands status=lost`; `command.finished` status `lost` |
| `TestE2EGracefulShutdown` | graceful shutdown | `SIGTERM` exit 0; a `read_output` waiting on the command answers within 15 s (status `killed`, or `internal` "shutting down, retry later" when it had not started waiting, see choices); `command.finished` `killed`; first event `gateway.started` with the bound address, last `gateway.stopped` reason `signal`, no unterminated line; a restart lists the command `killed` |
| `TestE2EConfigCheck` | `config check` on valid and invalid files | valid: exit 0, `config ok: 1 tenant, 1 server`; non-loopback listen, unknown key, `access: full`, a bad limit, a missing key file, a YAML anchor, a missing file: exit 1 with the error lines; no `--config` exit 2; `version` prints the stamped metadata |
| `TestE2ETwoTenantsRefused` | two tenants (as far as the binary can prove it) | `config check` and `serve` exit 1 with "tenants: at most 1 tenant is supported in this version (MaxTenants), got 2"; `serve` never opens the audit log |
| `TestE2ESecondGateway` | a second gateway on the same data directory | exit 1, `start-up failed` reason "the data directory is in use by another gateway"; the first still runs a command |
| `TestE2EResourceBounds` | leak checks; spec 6 "A command stops consuming gateway resources" | 200 mixed calls on 4 workers plus a killed command: descriptors return to idle; RSS ceiling 256 MB; threads grow by at most 16 |
| `TestE2EFileSizeLimit` | hardening (c), full disk | the output file stops at the limit: status `error` code `internal`, output up to the limit readable to eof, `command.finished` `error`, the gateway keeps serving |

Around every scenario: the child runs in its own process group with `Pdeathsig`; cleanup sends `SIGTERM` and fails the test if it is not gone within 30 s (then kills the group), then checks with `kill(-pgid, 0)` that nothing of its group is left, and attaches the child's stderr to the log of a failed test; the fixture checks that no target has a running command after the scenario.

## Guarantee table

In the spec, section 6, between `<!-- guarantees:begin -->` and `<!-- guarantees:end -->`; checked by `internal/deprule` `TestSecurityModelTable` (every guarantee sentence has a row, every named test exists in its package, every quoted phrase is in its file, and a missing test or phrase is flagged by its own sad subtests).

| Guarantee | Proved by |
|---|---|
| No request reaches a tool without a verified principal | `test/e2e` `TestE2EAuth`, `TestE2EHTTPSurface`; `internal/mcpserver` `TestBehindTheEdge`, `TestF6IdentityReachesHandler`; `internal/auth` `TestTokenClassesThroughMiddleware` |
| A principal sees only its tenant's servers, commands and output | `internal/mcpserver` `TestTenantIsolation`, `TestTenantLookupInTheToolsLayer`; `internal/jobs` `TestTenantIsolation`; `internal/store` `TestTenantIsolation`; `test/e2e` `TestE2ETwoTenantsRefused` |
| Nothing from the config file can be read or changed through MCP | `internal/mcpserver` `TestNothingFromTheConfigLeaks`, `TestProbeLoginNameNeverShown`, `TestToolsList`; `test/e2e` `TestE2EEveryTool` |
| A target with an unknown or changed host key is never spoken to | `test/e2e` `TestE2EHostKeyMismatch`; `internal/sshclient` `TestPinningAndKeys`, `TestContractSSHTest`, `TestRealSSHDContract`; `internal/hostkey` `TestKnownHosts` |
| An action whose audit event cannot be written does not happen | `test/e2e` `TestE2EAuditUnwritable`; `internal/mcpserver` `TestAuditBeforeAct`, `TestBrokenAuditLog`; `internal/auth` `TestMiddlewareAuditFailure`; `internal/jobs` `TestStartFailures`, `TestKillAudit`; `internal/audit` `TestAuditBeforeAct` |
| Tokens and key material never appear in logs, errors or the audit log | `internal/auth` `TestNothingLeaks`; `internal/audit` `TestNoSecretFields`; `test/e2e` `TestE2EEveryTool` |
| A command stops consuming gateway resources when it is killed, times out or the gateway shuts down | `test/e2e` `TestE2EResourceBounds`, `TestE2ELongCommand`, `TestE2ETimeout`, `TestE2EGracefulShutdown`; `internal/jobs` `TestLeaks`, `TestShutdown` |

| Not guaranteed | Where it is said |
|---|---|
| A command at `look` changes nothing | `README.md`; the `run_command` and `download_file` descriptions ("guard rail, not a boundary", checked by `TestToolsList`); spec D6 |
| The denylist cannot be bypassed | `internal/policy` package doc; `README.md`; spec 5.3 |
| Output is free of secrets | `README.md` (new in this phase); spec 5.6 |
| Availability under a request flood | `README.md` (new in this phase); spec 5.7, 5.8 (`TestRequestCap`, `TestRejectionAllowance`) |
| Text from a target is safe for a model | tool descriptions ("Output from a target is untrusted text", `TestToolsList`); `TestTargetCannotImitateTheGateway`; `README.md` |

## Section 6 review (where the code enforces or documents each item)

- **No request reaches a tool without a verified principal.** `internal/mcpserver` (`gateway.go`): `/mcp` goes through the concurrency cap, `auth.Guard` (verifies the token, maps it to a principal, writes `auth.accepted` before the next handler) and `auth.Require`; the per-request MCP server is built from the principal in the request context and the tool handlers read it from that closure only; a missing principal is `internal`. There is no unauthenticated mode (D4); `/healthz` is the only unauthenticated path and holds status and version only. Holds.
- **A principal sees only its tenant's servers, commands and output.** Server lookup in the tools layer inside the principal's tenant (`not_found` otherwise, before policy or SSH); `internal/store` scopes every statement by tenant; `internal/jobs` keeps per-tenant state and output paths under `tenants/<id>/commands/<id>`. On the binary only one tenant can exist, so the isolation tests are in-process (spec 7 callout). Holds.
- **Nothing from the config file can be read or changed through MCP.** The eight tools only (D9); `list_servers` and `get_server_info` expose id, description, tags, access; the probe's login name is never shown; no tool writes config. Holds.
- **A target with an unknown or changed host key is never spoken to.** `internal/sshclient` with `internal/hostkey`: the pinned key is the only accepted key, checked in the handshake before authentication; no trust on first use, no certificates. The e2e impostor saw no request at all. Holds.
- **An action whose audit event cannot be written does not happen.** `audit.Logger.Before`, refusing every write after the first failure; the guard writes `auth.accepted` first (503 if it fails), every tool writes `tool.called` before acting, the engine writes `command.requested` and `command.kill_requested` before the SSH action, file tools write `requested` first. Holds on the binary with a real kernel write failure. Note (not a defect): `read_output` and `kill_command` resolve the command id before `tool.called`, so a lookup of an unknown id is answered `not_found` without an audit line; it reads one row of the caller's own tenant and acts on nothing.
- **Tokens and key material never appear in logs, errors or the audit log.** Typed audit constructors with no token or key field (`TestNoSecretFields`); fixed log messages; `keyfile` never echoes key bytes; the e2e scan of stderr and audit found neither. Holds.
- **A command stops consuming gateway resources when it is killed, times out or the gateway shuts down.** `internal/jobs` (kill, timeout and shutdown close the SSH connection after TERM/KILL and release slots and files); the e2e burst returns to the idle descriptor count. Holds.
- **Not guaranteed, documented as such.** Found and fixed in this phase: "output is free of secrets" and "availability under a request flood" were said only in the spec, nowhere an operator or the model reads; `README.md` now lists all five items (a docs change, checked by the table test). The other three were already in the tool descriptions, the policy package doc and the README. Nothing to list for the lead beyond the open questions below.

## Hardening

- **(a) Shared test support.** `internal/testkit/audittest` (`FailLog`: `Open`, `Detached`, `SetFail`, `Write`, `Before`, `Close`, `Lines`, `Line`), `internal/testkit/tenanttest` (`Limits`, `SSHOptions`, `Server`), `internal/testkit/syncbuf` (`Buffer`, replacing five copies in `auth`, `cli`, `cmd/yasshm`, `mcpserver`), `accesstest.Issuer.For(who)` (used by `edgetest` and the endpoint harness). No behaviour change: same values; `tenanttest.Server` sets a description and a tag that the job engine ignores. -362/+118 lines in the moved tests.
- **(b) Policy cost.** `BenchmarkDecideMaxCommand` on allowed 64 KiB commands. Before: one-line 246 ms, many-lines 380 ms, quotes 382 ms, separators 442 ms, arguments 299 ms. `BenchmarkCommandRules`: every rule built on the command-start prefix costs 25 to 110 ms (rm-root the largest), because Go's `regexp` runs these as an NFA (no DFA), cost = input x program size. Cut: each built-in rule has `Needs` (literals every match contains) and is skipped when none is present; decisions unchanged (fuzz invariant + `TestCommandRuleNeeds` on every table case). After: one-line 0.35 ms, many-lines 0.42 ms, quotes 0.13 ms, separators 0.5 ms, arguments 87 ms, every-literal (worst case, no rule skippable) 260 ms. Bound recorded in the spec 5.3 callout; the command is length-checked before any rule and the request cap bounds concurrency.
- **(c) Full disk.** A `tmpfs` or loop mount needs privileges; a user namespace could grant them but the gateway would have to run inside it and the runner's userns policy was not checked. Used instead: `prlimit(2)` lowers `RLIMIT_FSIZE` of the running binary (no privilege, binds root, Go ignores `SIGXFSZ` so writes fail with `EFBIG`). `TestE2EFileSizeLimit` (output file past 128 KiB) and `TestE2EAuditUnwritable` (audit log). Store write failures stay injected in-process (`TestFullDisk`). The limit covers every regular file of the process, so after it the database cannot grow either (seen as "job engine shutdown incomplete" in the audit scenario's logs, exit still 0).
- **(d) Fuzz.** `mise run fuzz`, 30 s each, all PASS, no crasher, nothing to commit: `FuzzResolve` 793,620 execs; `FuzzParse` 1,276,951; `FuzzPage` 468,173; `FuzzFitText` 357,178; `FuzzCommandGuardRail` 210,744 (with the new prefilter invariant); `FuzzPathGuardRail` 2,978,218.

## Leak-check numbers (`TestE2EResourceBounds`, sandbox)

Burst outcomes: `list_servers:ok 19`, `run_command:ok 19`, `run_command:policy_denied 18`, `run_command:invalid_argument 18`, `read_output:ok 18`, `read_output:not_found 18`, `list_commands:ok 18`, `get_server_info:ok 18`, `download_file:ok 18`, `download_file:not_found 18`, `upload_file:access_denied 18`, plus one killed command. Descriptors: idle 14, peak 23 to 24, after 14 (closed idle keep-alives, polled up to 20 s, returned at once). RSS 21.9 MB -> 23.9 MB; threads 9-10 -> 11.

## Gate, repeat runs, CI

Local gate on the clean tree at `9e854ec`:

```
check.sh: sshd is not installed here; the real-sshd lane runs in CI only (spec section 7)
== e2e ==
e2e: 19 of 19 end-to-end scenarios passed against the real binary in 5s (uid 0)
== cover ==
cover: total 94.8% (floor 85%, internal/testkit excluded)
== vuln ==
No vulnerabilities found.
== build ==
== arch ==
check.sh: all OK
```

The first gate run on `9e854ec` failed once in `internal/jobs` `TestStatuses/happy/kill_delivers_TERM_and_the_record_is_killed` (delivered false, record `may_still_run`): not reproduced in 30 isolated runs nor in the next gate or the `-count=2` run. Most likely the sshtest signal reply arrived after `KillWait` (1 s in the test SSH options, unchanged by the refactor) while every package ran under `-race` at once; it predates this phase. Listed for the lead below.

- `test/e2e` 20 runs in a row (`go test -race -shuffle=on -count=1 ./test/e2e`, separate invocations): 20 of 20 passed in 91 s total, 3.5 to 4.1 s each.
- Whole module `go test -race -shuffle=on -count=2 ./...`: all 19 test packages ok (92 s).
- CI on `9e854ec`: `check completed success` (job 136140771865), runner uid 1001: `e2e: 19 of 19 end-to-end scenarios passed against the real binary in 22s (uid 1001)`, every `--- PASS: TestE2E*`, `sshd: 32 of 32 contract scenarios passed against real sshd, and the login test`, `cover: total 94.7%`, `check.sh: all OK`. Earlier heads `b4573a5` and `001b5fd` were green too (18 of 18, uid 1001).

## Commits

```
9e854ec docs: record the end-to-end layer in the changelog and gotchas
509b550 test: name the tests behind each section 6 guarantee
3d22a94 perf(policy): skip a command rule whose literals are absent
001b5fd refactor(testkit): share the audit, tenant and buffer test support
b4573a5 chore(gate): run the end-to-end suite in its own e2e step
78af53a test(e2e): drive the real binary through the fake edge
```

`git status --porcelain` before this report: empty.

## Spec discrepancies and deliberate choices

1. **Two tenants on the binary are impossible** (D7, `MaxTenants = 1`, no test-only flag): isolation stays in-process; `TestE2ETwoTenantsRefused` proves the refusal; spec 7 callout.
2. **Unwritable audit log**: `RLIMIT_FSIZE` via `prlimit(2)` on the running child, not a FIFO or `/dev/full` (the audit log refuses non-regular files at open). `list_servers` is checked only at the guard (its `tool.called` is too close in length to `auth.accepted` for the limit to separate them). A broken log makes the guard answer 503 for every later request, so each tool gets its own gateway.
3. **Shutdown answer**: a read that wrote `tool.called` but had not started waiting when the engine began shutting down answers `internal` "shutting down, retry later" (the spec 5.8 mapping of `jobs.ErrShuttingDown`), not `killed`; the scenario accepts either and requires the answer within 15 s. Seen 1 time in 3 early runs.
4. **The suite's build is lazy** (inside `TestMain`'s temp directory, once per process), so `go test -list` and the `sshd` step's `./...` run build nothing; the build uses the `go` on `PATH` and refuses a version that differs from `runtime.Version()` (no deprecated `runtime.GOROOT`).
5. **`stdout_bytes` counts what the command wrote**, not what was kept (500001 with 1024 kept): the scenario asserts that, matching spec 5.6.
6. **The e2e step reports its wall time and uid**; it counts top-level `TestE2E*` with `go test -list` (19).
7. **Spec edits** (callouts dated 2026-11-20): 5.3 (policy cost and `Needs`), 6 (the two tables, with markers), 7 (scenario -> test map, two tenants, the audit and full-disk method, the gate step, the new test kits).
8. `docs/progress.md` still lists the gate steps without `e2e` (its current-state bullet); left for the lead with the ledger, as instructed.

## Open questions for the lead

- The `internal/jobs` kill flake above: raise `tenanttest.SSHOptions` `KillWait` (slower `Silent*` tests) or leave it; it was seen once in about ten full test runs today.
- `TestE2EResourceBounds` RSS ceiling is generous (256 MB against about 24 MB observed); tighten if wanted.
- The SDK's `MCPGODEBUG` crash on a malformed value remains for Phase 7 (the suite strips `MCPGODEBUG`, `GODEBUG` and `GOTRACEBACK` from the child's environment).
