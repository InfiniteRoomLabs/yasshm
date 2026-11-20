# Phase 5 security review -- MCP endpoint and tools

Verdict: **BLOCK** -- 1 blocking, 6 advisory.

Range `cf21613..a842d66` (the head moved one test-only commit past `9c6daff`). Read-only lane: only `git`, `grep`, `go list`, `go mod verify` and `govulncheck` were run, so finding 1 is established by reading the code, not by a test. SDK lines are in `go-sdk@v1.8.0` in the module cache.

## Blocking

**1. BLOCKING -- a JSON-RPC batch runs many tool calls in one HTTP request, past the concurrency cap and the result-size bound.**

- Evidence: `internal/mcpserver/gateway.go:365-370` hands the body to the SDK untouched. SDK `mcp/streamable.go:1439-1453` accepts an array body for any protocol version below 2025-06-18, and a request with no `MCP-Protocol-Version` header counts as 2025-03-26 (line 1447); the refusal of batches in JSON mode is commented out (1455-1460). `mcp/server.go:2004` marks every call but `initialize` async, so the entries run concurrently, and `mcp/streamable.go:1120` answers with one array of all results. No test or spec line covers a batch.
- Effect, for any authenticated principal, with one request holding one slot of `limit` (`gateway.go:302-314`): about 20,000 `tools/call` entries fit in 2 MiB. `get_server_info` and `download_file` have no cap but the request cap, so one request opens thousands of concurrent SSH connections with the gateway's key; `read_output` with `wait_seconds` gives thousands of waiters. Every result is buffered until the last one ends, each up to `max_result_bytes` in several copies: memory reaches gigabytes, the process can be killed, running commands become `lost` and every tenant is down. The HTTP body is N times `max_result_bytes`, against the 5.8 callout ("the whole HTTP body fits"). Identity, policy and audit still hold per call.
- Fix: in `serveMCP` (behind the guard) read the capped body, answer 400 with a fixed JSON body when its first non-space byte is `[`, then restore `r.Body`. Second layer: the per-request server serves one `tools/call` (an atomic flag in the `newServer` closure; a later call answers `invalid_argument`). Tests: a batch without a version header and with `2025-03-26`, both refused with no `tool.called` written. Record the rule in the 5.8 callout.

## Advisory

**2. ADVISORY -- unvalidated argument values reach the audit log, up to 2 MiB per request.** Each `prepare` builds `plan.server` and `plan.args` before validating (`tool_files.go:67,163`, `tool_commands.go:171-174,276,341,443`) and `invoke` writes `tool.called` whatever `p.err` says (`call.go:146`); `fail` repeats `server` in `tool.failed` (`call.go:150`). So `server` (any bytes), `path`, `mode`, `stream`, `status`, `signal`, `cursor` and `command_id` are recorded as sent: up to 96 KiB, 2 MiB for `upload_file`, each synced. One principal fills the disk about 250 times faster than with valid calls, and a full disk fails the gateway closed for everyone. Fix: record `server` only when it matches the id pattern, and cap the others before `ToolCalled` (path 4096 bytes, the short ones 64) with a marker that the value was cut.

**3. ADVISORY -- the write timeout does not cover the connect ahead of a wait.** `WriteTimeoutFor` (`gateway.go:209-217`) takes `max_wait_seconds` alone, but `run_command` first connects and waits for the exec reply (up to twice `connect_timeout_seconds`, `sshclient/command.go:139-146`) and then waits (`tool_commands.go:202-211`). Defaults: 20 + 60 s against 75 s; with the largest limits 240 + 110 against 165. A target that stalls the handshake makes the answer miss the deadline: the command runs and the caller never gets its id. Fix: use `2 x connect + max_wait` for that term; test it in `TestDerivedLimits`.

**4. ADVISORY -- one principal can hold every request slot.** The cap is global (`gateway.go:198-204`): 24 `read_output` requests with `wait_seconds` 110 on one running command give every other caller 503 for 110 seconds, repeatedly. Section 6 lists only the unauthenticated flood as not guaranteed. Fix: a per-principal share of the slots, or add this case to that list.

**5. ADVISORY -- control characters and ANSI escapes pass through undocumented, and the text rendering can be imitated.** `fitText` (`output.go:51-77`) replaces only invalid UTF-8; control bytes are JSON-escaped, so the framing is safe, but 5.8 does not say they pass. The text block puts target output right after the gateway's status line and around the `--- stderr bytes ... ---` divider (`tool_commands.go:240-255`, `tool_files.go:120-124`), so a target can print a fake divider or status line. Fix: say both in the 5.8 callout and in the descriptions ("the structured fields are authoritative").

**6. ADVISORY -- the SDK reads `MCPGODEBUG` at init.** `internal/mcpgodebug/mcpgodebug.go:24` parses the environment variable (a malformed value panics before `main`), and `allowsessionsinstateless=1` changes `DELETE` and session-id handling (`mcp/streamable.go:280`), still behind the guard. Fix: note it in the 5.8 callout; the Phase 8 unit passes no environment.

**7. ADVISORY -- a result that suggests an action.** `msgFileExists` (`call.go:49`) tells the model to set `overwrite`. Unreachable at `look`; reword it to the fact alone before `full` exists.

## Checked, no finding

1. **Principal before any tool.** `Handler` (`gateway.go:241-255`) matches `r.URL.Path` exactly: `/mcp/`, `//mcp`, `/MCP`, `/mcp;x` and an encoded slash are 404; an absolute URI and `%6dcp` reach `/mcp`, which is behind `limit` -> `Guard.Wrap` -> `Require` for every method. `Host` and override headers are never read. The SDK, the body read and the 375 KB server build run after the guard. `/healthz` gives status, a fixed detail and the version. Version unauthenticated: yes, acceptable -- the spec requires it, the source is public, and it is reachable only on loopback or behind the edge. No CORS header, and only the token header is read (`auth.go:235`): a cross-site `POST` or a rebinding page has no ambient credential and gets 401.
2. **Identity (F6).** The actor comes only from the guard's context into the closure (`gateway.go:365-370,403-419`); no non-test code reads `Extra`, `TokenInfo` or a request header. `AddTool` does not mutate the shared `*mcp.Tool`; stateless mode stores no session.
3. **Tenants.** Every engine and SSH call takes `c.t.ID`; another tenant's server, command or cursor gets the same `not_found` and text as a missing one (`TestTenantIsolation`); audit events take the tenant from the actor.
4. **D8.** Messages are fixed (`call.go:41-58`); SDK error bodies echo only the caller's input; facts exclude the login name; `TestNothingFromTheConfigLeaks` covers results, schemas, headers and logs.
5. **Policy and audit.** One `a.Command` string goes to audit, policy and `jobs.Start`; SFTP gets `policy.CleanPath`, as policy applies it. `upload_file` is stopped by descriptor, policy and the SSH layer. A broken log: 503 at the guard, `internal` in `invoke`, `errAudit` before a transfer. `ToolArgs` has no content or output field.
6. **Limits.** `max_command_bytes` is checked before any regex (`policy.go:232`): 8192 bytes per call by default. The SDK caps JSON depth at 1000. The unauthenticated flood is in section 6's list.
7. **Output.** Findings 5 and 7; no target text reaches a description or an error message.
8. **Process.** The loader enforces a loopback IP literal; no `pprof` or `expvar`; log lines carry codes and counts; a failed start closes the listener.
9. **Supply chain.** `go mod verify` and `govulncheck` clean; `go.sum` additions only. Linked but unused: `x/oauth2` (no init), `uritemplate`. `segmentio/asm` and `segmentio/encoding` carry assembly and `unsafe`, and parse bodies only after the guard. CI and scripts unchanged.
10. **Hygiene.** Diff, report and commit messages hold only `example.net` names, `127.0.0.1` and synthetic identities.
