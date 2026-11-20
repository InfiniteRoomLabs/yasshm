# Phase 5 implementer report -- MCP endpoint and tools

Phase base `cf21613`. Head at the time of writing: `1f68528` (CI green), plus this report's commit. Branch `claude/serene-franklin-jv4pu7`.

## Summary

- `internal/mcpserver`: the eight tools of spec 5.8 on stateless Streamable HTTP with plain JSON responses, one call path for every tool, results sized to `max_result_bytes` on the wire, and the HTTP surface (`/mcp`, `/healthz`, 404, caps, timeouts, security headers).
- `yasshm serve --config <file>` (`internal/cli/serve.go`), tested in-process and as a child process stopped by `SIGTERM`.
- `internal/testkit/edgetest`: the stand-in edge.
- Config: the maximum wait is 110 seconds; the minimum of `max_result_bytes` is 4096.
- Audit: new event `tool.failed`.
- Architecture: `yasshm.tools` and `yasshm.http` built, `tools -> ssh` added, labels corrected, diagrams regenerated.
- F6 settled first (CONFIRMED in spec row F6), F5 observations added.
- Gate green on the clean tree, coverage 94.7 percent, CI green, `-count=20` of the two phase packages and `-count=3` of the module green, every fuzz target PASS.

## F6: how the verified principal reaches a tool handler

Mechanism: `Gateway.serveMCP` runs behind `auth.Guard` and `auth.Require`. It reads the principal and request id the guard put in the request's context, builds one `*mcp.Server` for this request whose eight tool handlers capture an `audit.Actor{RequestID, Principal}` in a closure, and hands the request to the SDK handler with that server in the context. The SDK's `getServer` (`serverFromContext`) only reads it back; the SDK calls `getServer` twice per request (`ServeHTTP` for protocol versions, `serveStateless` to connect), so building there would build twice.

Evidence (`internal/mcpserver/f6_test.go`, `TestF6IdentityReachesHandler`, against the real v1.8.0 handler and client, for the 2026-07-28 protocol and for 2025-11-25):

- the closure carries the guard's principal;
- `req.Extra.Header` is the client's raw header set (the test's client-set header comes through verbatim), and `req.Extra.TokenInfo` is nil: neither is used;
- observed as well: the handler's `ctx` carries the guard's context values in stateless mode (the SDK connects the per-request session with the HTTP request's context); the gateway does not rely on it;
- a request without a principal never reaches the SDK (`auth.Require` answers 500).

Cost (`BenchmarkNewServer`): 0.41 ms and 375 KB allocated per request (1067 allocations), schemas computed once at start. Recorded in the 5.8 callout; correctness wins.

## Exported API

`internal/mcpserver`:

- `type Config struct { Version string; Tenants []tenant.Tenant; Policy *policy.Engine; Jobs *jobs.Engine; Audit Auditor; Guard *auth.Guard; Health Health; SSHOptions func(tenant.Limits) sshclient.Options; Logger *slog.Logger; MaxInFlight int }`
- `func New(cfg Config) (*Gateway, error)`; `(*Gateway).Handler() http.Handler`; `(*Gateway).HTTPServer() *http.Server`; `(*Gateway).WriteTimeout() time.Duration`
- `type Auditor interface { Write(audit.Event) error }`; `type Health interface { Ready() bool; Status() string }` (`*auth.CloudflareAccess` is one)
- `func MaxInFlightFor([]tenant.Tenant) int`, `func WriteTimeoutFor([]tenant.Tenant) time.Duration`, `func Policy() map[string]policy.Tool`
- Constants: the ten codes (`CodeInvalidArgument` ... `CodeInternal`) and `var Codes`; the eight tool names (`ToolListServers` ...); `PathMCP`, `PathHealthz`; `MaxBodyBytes`, `MaxHeaderBytes`, `ReadHeaderTimeout`, `ReadTimeout`, `IdleTimeout`, `WriteMargin`, `RetryAfterSeconds`
- Result types: `ToolError`, `ServerEntry`, `ListServersResult`, `Facts`, `ServerInfoResult`, `CommandStatus`, `RunResult`, `ReadResult`, `CommandEntry`, `ListCommandsResult`, `KillResult`, `DownloadResult`, `UploadResult`

`internal/cli`: `func Serve(ctx context.Context, args []string, stdout, stderr io.Writer) int` (and `Run` dispatches `serve` with signal handling).

`internal/audit`: `const EventToolFailed = "tool.failed"`, `func ToolFailed(a Actor, tool string, server tenant.ServerID, code string) Event`.

`internal/testkit/edgetest`: `New(t, origin, issuer, who) *Edge` with `URL`, `As(who)`, `SetMode(ModeToken | ModeNoToken | ModeBadToken)`, `Close`; `PublicHost = "yasshm.example.net"`.

## The tool table as built

Policy descriptors (`mcpserver.Policy()`): every tool requires `look` except `upload_file` (`full`, `WritesFiles`); `run_command` checks `InputCommand`, `download_file` and `upload_file` check `InputPath`. `list_servers`, and `list_commands` without `server`, take no policy decision (a verified principal is enough; only the principal's tenant is read); `read_output` and `kill_command` read the record in the caller's tenant first and decide against its server.

| Tool | Hints (readOnly, destructive, idempotent, openWorld) | Arguments (defaults, caps) | Result fields | Codes it can return |
|---|---|---|---|---|
| `list_servers` | true, false, true, false | none | `servers[]` (`id`, `description`, `tags`, `access`), `truncated` | `invalid_argument`, `internal` |
| `get_server_info` | true, false, true, true | `server` | `id`, `description`, `tags`, `access`, `reachable`, `latency_ms`, `host_key_ok`, `facts` (`kernel`, `arch`, `os`, `uptime_seconds`; never the login name), `error_code`, `truncated` | `invalid_argument`, `not_found`, `internal` (a failed probe is a success with `reachable` false) |
| `run_command` | false, true, false, true | `server`, `command`, `wait_seconds` (default `default_wait_seconds`, clamped to `max_wait_seconds`), `timeout_seconds` (0/absent = default, above `max_timeout_seconds` refused), `merge_stderr` (true), `max_bytes` (clamped to the result budget) | `command_id`, `server`, `status`, `exit_code`, `signal`, `error_code`, `may_still_run`, `merge_stderr`, `stdout`, `stderr`, `next_offset`, `stderr_next_offset`, `eof`, `lossy`, `truncated`, `stdout_bytes`, `stderr_bytes` | `invalid_argument`, `not_found`, `policy_denied`, `busy`, `ssh_unreachable`, `ssh_auth_failed`, `host_key_mismatch`, `timeout`, `internal` |
| `read_output` | true, false, true, false | `command_id`, `stream` (stdout), `offset` (negative from the end), `max_bytes`, `wait_seconds` (0, clamped) | command status fields, `stream`, `offset`, `data`, `next_offset`, `size`, `eof`, `lossy`, `truncated` | `invalid_argument`, `not_found`, `internal` |
| `list_commands` | true, false, true, false | `server`, `status`, `limit` (1..100, default 20), `cursor` | `commands[]` (status fields, `command` = first 200 characters, `command_truncated`, `created_at`, `started_at`, `finished_at`, byte counts), `next_cursor` | `invalid_argument`, `not_found`, `internal` |
| `kill_command` | false, true, true, true | `command_id`, `signal` (`TERM` default, `INT`, `KILL`) | status fields, `signal_delivered` | `invalid_argument`, `not_found`, `internal` |
| `download_file` | true, false, true, true | `server`, `path`, `offset` (0), `max_bytes` (clamped) | `server`, `path` (cleaned), `offset`, `size`, `encoding` (`text` or `base64`), `data` or `data_base64`, `next_offset`, `eof` | `invalid_argument`, `not_found`, `access_denied`, `policy_denied`, SSH codes, `internal` |
| `upload_file` | false, true, false, true | `server`, `path`, exactly one of `content` and `content_base64`, `mode` (octal, default `0644`, at most `0777`), `overwrite` (false); `max_upload_bytes` checked before anything is sent | `server`, `path`, `bytes_written` | `access_denied` (every v0.1 server), `invalid_argument`, `not_found`, `policy_denied`, SSH codes, `internal` |

Every tool can also answer `internal` for an audit write that failed ("the audit log could not be written, so nothing was done") and for "shutting down, retry later" (`jobs.ErrShuttingDown`). Errors are `isError: true` with structured `{code, message, rule?}` and the same text; `wantErr` in the tests asserts every code seen is one of the ten.

## HTTP surface and timeouts

- `/mcp` (exact path, any method): concurrency cap -> `auth.Guard.Wrap` -> `auth.Require` -> `serveMCP` -> SDK handler with `Stateless`, `JSONResponse`, `DisableLocalhostProtection`, `MaxRequestBodyBytes` 2 MiB, `PropagateRequestCancellation`. `GET`/`DELETE` with a token: the SDK's 405.
- `/healthz` (`GET`, `HEAD`): `{"status":"ok","version":"..."}` 200, or `{"status":"degraded","detail":"key set not loaded","version":"..."}` 503; another method 405.
- Anything else: 404 `{"error":"not found"}`.
- Every response: `Cache-Control: no-store`, `X-Content-Type-Options: nosniff`; no CORS header. `http.MaxBytesReader` 2 MiB in front of everything.
- `http.Server`: `ReadHeaderTimeout` 10 s, `ReadTimeout` 30 s, `WriteTimeout` = max(`max_wait_seconds`, connect + 20 s probe, connect + 30 s transfer) + 15 s (defaults: 60 s), `IdleTimeout` 120 s, `MaxHeaderBytes` 32 KiB.
- Concurrency cap: 8 + 4 x `max_concurrent_commands` per tenant (default 24, at most 1024), in front of the guard; over it 503 `{"error":"busy"}` with `Retry-After: 5`, one log line a minute, never audited.
- MCP methods allowed: `initialize`, `notifications/initialized`, `notifications/cancelled`, `ping`, `server/discover`, `tools/list`, `tools/call`; others "method not found".

## Tests

New in this phase: 32 top-level tests, fuzz targets and benchmarks in `internal/mcpserver`, 3 new top-level tests in `internal/cli` (`TestServe`, `TestServeStartFailures`, `TestWatchSignals`), 1 in `internal/testkit/edgetest`, 1 in `cmd/yasshm` (`TestServeSignal`), plus `tool.failed` in the audit table and the limits rows in `TestParseLimits`. One verbose run of `internal/mcpserver`, `internal/cli`, `internal/testkit/edgetest` and `cmd/yasshm`: 279 passing tests and subtests (62 `happy/`, 91 `sad/`, 62 `raging/`, 18 `edge/`, 2 `corner/`, the rest top-level). Highlights: each tool's happy/sad/raging cases; the two-tenant isolation through every tool (same server id on different targets, a tenant B cursor used by tenant A); audit-before-act per event (`tool.called`, `command.requested`, `command.kill_requested`, `file.downloaded`, `file.uploaded`, `policy.denied`) plus a really closed audit log (guard answers 503, no connection to the target); the D8 marker test (mutation-checked: putting the host into a description fails it); the largest result of every tool measured on the wire at 4096 and 140000; `busy`; denylisted commands; host key mismatch; unreachable via `loopback.RefusingPort`; a command outliving the wait, paged and killed; a client that goes away mid-wait does not stop the command (D10); `tools/list` names, schemas and hints; protocol errors for unknown tools and malformed JSON-RPC; a recovered panic; `serve` in-process with a second gateway refused and shutdown ending a running command `killed`; `serve` as a child process stopped by `SIGTERM`.

Coverage: `cover: total 94.7% (floor 85%, internal/testkit excluded)`.

Repeat runs:

- `CGO_ENABLED=1 mise exec -- go test -race -shuffle=on -count=20 ./internal/mcpserver/ ./internal/cli/`: `ok internal/mcpserver 732.705s`, `ok internal/cli 24.273s`.
- `CGO_ENABLED=1 mise exec -- go test -race -shuffle=on -count=3 ./...`: all 18 packages with tests `ok`, exit 0.

Fuzz (`mise run fuzz`, 30 s each): `FuzzResolve`, `FuzzParse`, `FuzzPage`, `FuzzFitText` (new), `FuzzCommandGuardRail`, `FuzzPathGuardRail`: all PASS, no crasher, nothing written to the tree.

Tail of `mise run check` on the clean tree at `1f68528`:

```text
== sshd ==
check.sh: sshd is not installed here; the real-sshd lane runs in CI only (spec section 7)
== cover ==
cover: total 94.7% (floor 85%, internal/testkit excluded)
== vuln ==
No vulnerabilities found.
== build ==
build: dist/yasshm_1f68528_linux_amd64
build: dist/yasshm_1f68528_linux_arm64
build: dist/SHA256SUMS
== arch ==
check.sh: all OK
```

CI (`gh api repos/InfiniteRoomLabs/yasshm/commits/<sha>/check-runs`): `1f68528` `check completed success`; also green on `9c06b3c`, `7094de2`, `8b004c0`.

## Dependency (D14)

`github.com/modelcontextprotocol/go-sdk` v1.8.0 (latest release; `v1.9.0-pre.1` not used): the official SDK named by D14. Modules it brings into the build graph of `./cmd/...` (`go list -deps`), none there before:

- `github.com/google/jsonschema-go` v0.4.3 -- JSON Schema inference and validation; the gateway also imports it directly to build the tools' schemas.
- `github.com/segmentio/encoding` v0.5.4 -- the SDK's internal JSON encoder.
- `github.com/segmentio/asm` v1.1.3 -- assembly helpers used by segmentio/encoding.
- `github.com/yosida95/uritemplate/v3` v3.0.2 -- URI templates for MCP resources (unused by the gateway, linked by the SDK).
- `golang.org/x/oauth2` v0.35.0 -- the SDK's auth helpers (unused here; the edge does OAuth).
- `golang.org/x/sync` v0.24.0 -- errgroup and friends inside the SDK.
- `golang.org/x/time` v0.15.0 -- rate limiting inside the SDK.

`github.com/golang-jwt/jwt/v5` and `github.com/google/go-cmp` are in the SDK's `go.mod` but not in the build graph. `mise run check -- vuln`: "No vulnerabilities found."

## Binary sizes

`mise run build`, static (`statically linked`), `-trimpath`:

| | Before (`541ceea`, same code as `cf21613`) | After (`1f68528`) |
|---|---|---|
| linux/amd64 | 4,210,848 | 15,483,040 |
| linux/arm64 | 3,932,320 | 14,549,152 |

The jump is the job engine, the store and `modernc.org/sqlite` linked into the binary for the first time (F18 measured SQLite alone at about +4.9 MB), the authenticator, and the SDK with its modules.

## Spec discrepancies, ambiguities and deliberate choices

1. **Maximum wait** (premise): `default_wait_seconds` and `max_wait_seconds` maximum 110 (was 290); limits, `TestParseLimits` and the 5.1 callout changed together.
2. **`max_result_bytes` minimum raised to 4096** (was 1024): a result carries a status, offsets and a header; 1 KiB minus the envelope left no room. Same three places.
3. **New audit event `tool.failed`** (5.4 callout): an error answer that is not a policy denial would otherwise leave only `tool.called`; it is how a recovered panic is "audited as internal" without inventing a policy decision.
4. **Untyped `Server.AddTool`** instead of typed `mcp.AddTool`: the typed path answers a schema violation with text that echoes the input and is not one of the ten codes; the gateway decodes strictly itself (exact keys, no duplicates, fixed messages).
5. **Output schema = success shape**; an error's structured content `{code, message, rule?}` does not follow it. A client may validate structured content against the output schema; the Go SDK client validates neither. Open question for review: whether a client in the field validates error results too.
6. **`get_server_info` never shows the probe's login name**: it is the configured user (D8). `reachable` means the SSH handshake completed: true with `host_key_ok` false for a mismatch, true and true for a refused key, false otherwise.
7. **Binding before `gateway.started`**: the event names the address actually bound (port 0); a failed write closes the listener before any request is accepted.
8. **Shutdown order**: `http.Server.Shutdown` and `engine.Shutdown` run together, so requests waiting on commands answer at once when their commands are killed, instead of the HTTP shutdown waiting out `wait_seconds`.
9. **Concurrency cap in front of the guard**: it bounds unauthenticated work too; it is a request count, not a rate (section 6 already says rate limiting is the edge's job).
10. **`/healthz` answers 503 while degraded** (spec says "status and version"); a method other than `GET`/`HEAD` is 405, not 404.
11. **MCP methods outside the tools set are refused** (notably `subscriptions/listen`, which would hold a request open).
12. **`run_command` with `merge_stderr` false**: stderr gets what stdout leaves of the page budget, so stdout always equals `read_output` at offset 0 and stderr does when it fits.
13. **`download_file` is text only when the bytes are valid UTF-8 without NUL**; a NUL would cost six bytes per byte as text.
14. **A character split by the page end** is held for the next page unless it is all the page holds (then replaced, so a reader with `max_bytes` 1 still progresses).
15. **Command text over `max_command_bytes`** is `policy_denied` (rule `command-too-long`) as the policy engine decides; arguments over 96 KiB (2 MiB for `upload_file`) are `invalid_argument` before policy.
16. **Observed, not fixed (Phase 1 code)**: `policy.Decide` on a 64 KiB command takes about 270 ms without the race detector (about 6 s under `-race`), roughly linear; only an authenticated caller can send one and the default `max_command_bytes` is 8192. Worth a look in Phase 6 hardening.
17. The README's smoke test can only reach `/healthz` and a 401 on `/mcp`: every tool needs a real Access token, which only the test kits mint.

## `git log --oneline cf21613..HEAD` (before this report)

```text
1f68528 test(mcp): measure the per-request server build
7094de2 test(mcp): clamp the wait against a one-second maximum
8b004c0 docs: record the MCP endpoint, serve and edgetest as built
1833393 docs(arch): build yasshm.tools and yasshm.http in the model
339dd99 feat(cli): add yasshm serve
143f215 test(mcp): prove the HTTP surface, result sizes and that no config leaks
9c06b3c feat(mcp): add the eight tools and their one call path
bdfe575 test(mcp): prove how the verified principal reaches a tool handler (F6)
```

`git status --porcelain` before writing this report: empty.
