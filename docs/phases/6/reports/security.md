# Phase 6 security review -- end-to-end and hardening

Verdict: **BLOCK** -- 1 blocking, 7 advisory.

Scope: the whole product at `a5ea5c9`. Read-only lane, so every finding is established by reading the code, not by a test.

## Blocking

**1. BLOCKING -- an unauthenticated caller writes unbounded audit lines; `README.md:21` and spec section 6 say they are bounded.**

- Evidence: `internal/auth/middleware.go:124` suppresses only `preSignature()` reasons (`:197-204`); `bad_signature` is not one. `internal/auth/auth.go:265-271`: any RS256-shaped token naming a current `kid` reaches `jws.Verify` and fails there, and the `kid` is public (the team's key set, F8). `middleware_test.go:202` pins "always written". A holder of a token the team signed for another application (`wrong_audience`) or of an old one (`expired`) has the same lever.
- Effect: adversary 1 gets one synced line per request with no allowance. Real requests queue behind those syncs (one mutex, `internal/audit/audit.go:148-184`), and a full disk breaks the logger, after which every request is 503 until a restart. The README sentence added in this phase ("their audit lines are bounded") is false.
- Fix: add `ReasonBadSignature` to `preSignature()` (a failed signature is not a verified one) and give post-signature rejections their own per-minute allowance with the same `suppressed` summary; update `TestRejectionAllowance`. At least correct the README and section 6.

## Advisory

**2. The loopback port can be taken by another local user while the gateway is down** (restart, upgrade, crash loop). `serve.go:199` binds an unprivileged TCP port; whoever binds it first receives what the edge forwards: tokens and command text. A captured token is replayable against the real gateway until `exp` (no `jti` tracking), and the squatter can answer the model. Fix: add it to "Not guaranteed" and to section 8 (no untrusted local users on the gateway host); list a Unix-socket listener in section 10.

**3. Audit log growth has no bound and no documented owner.** Every authenticated request writes `auth.accepted` and `tool.called` with up to `max_command_bytes` of text (`tool_commands.go:176`), limited only by sync speed. Spec 5.4 puts rotation out of scope; neither README nor `SECURITY.md` gives it to the operator. The logger keeps one `O_APPEND` descriptor (`audit.go:91`) and never reopens, so only copy-and-truncate rotation works; `SIGHUP` is not handled (`serve.go:46`), so a rotation hook that sends it kills the gateway with no `gateway.stopped`. Fix: Phase 7 operator docs (copytruncate, a file system apart from `data_dir`); ignore `SIGHUP`.

**4. Probes and transfers are outside the per-server limits.** `tool_servers.go:156` and `tool_files.go:109` dial directly; the only bound is the principal's share of the request cap (`gateway.go:182`: 18 at the defaults, 198 at the largest config) concurrent SSH connections to one target. Fix: a per-server slot for both, answered `busy`.

**5. The per-principal share is taken after the body is read.** `gateway.go:396` reads up to 2 MiB before `hold` at `:408`, so one principal can fill every slot for `ReadTimeout` (30 s) with slow bodies. Fix: `hold` first.

**6. Trust files are not checked for write access by others.** The private key and data directory are; `known_hosts` (`internal/hostkey/hostkey.go:111`, re-read at each connect), the config (`internal/config/config.go:202`) and the audit log's directory (`audit.go:85-91`: whoever can write it can unlink the log while the gateway keeps writing) are not. Fix: refuse a group- or other-writable `known_hosts` and config on the open file's mode bits; the Phase 7 example makes the log directory 0700.

**7. `go.mod` says `go 1.27`, `mise.toml` pins 1.27.4.** A build outside mise accepts 1.27.0. `scripts/build.sh` inherits `GOFLAGS`. Fix: `go 1.27.4`; `GOFLAGS=` in `build.sh`.

**8. `certs_url` is a test seam in the shipped config.** `internal/config/rules.go:106-121` accepts http to loopback: whoever binds that port is the issuer. Nothing is logged when it is set. Fix: a WARN at start-up and a line in the operator docs.

## Checked, no finding

**Section 6 guarantees (item 1).** Each has one first layer, and the named tests reach it:

| Guarantee | Enforced at | Breaks if |
|---|---|---|
| Verified principal | `gateway.go:266` (`limit` -> `Guard.Wrap` -> `Require`) | a route added outside `mcpH` |
| Tenant only | `call.go:226`, `tool_servers.go:129` (lookup inside `c.t`); every engine call passes `c.t.ID` | a tool taking a tenant argument |
| No config through MCP | result types `tool_servers.go:26-67`; fixed messages `call.go:41-59` | a new result field |
| Host key | `sshclient/client.go:230-239` | a second dial path |
| Audit before act | `call.go:181`; then `jobs/engine.go:379`, `kill.go:48`, `tool_files.go:104,201` | an action placed before `p.act` |
| No token or key in logs | typed audit constructors; fixed log messages | logging an error's `Detail()` |
| Command stops | `jobs/engine.go:373-374`, `lifecycle.go:96-131` | a command run under a request context |

`TestE2EAuditUnwritable` fails exactly `tool.called` (the limit lets `auth.accepted` fit), so the tools layer is tested, not only the guard. Implementer report line 68 is wrong in the safe direction: an unknown command id does get `tool.called` and `tool.failed` (`call.go:181-186`).

**Seams (item 2).** (a) One principal from the guard's context; `jobs.Start` (`engine.go:324`), `policy.Decide` (`policy.go:204`) and `sshclient` (`client.go:103`) each re-check the tenant. (b) One Go string `a.Command` goes to `tool.called`, `Decide`, `command.requested`, the row and `sess.Start` (`command.go:180`); policy trims white space for matching only, which fails closed. (c) `policy.CleanPath` is applied in `checkPath` and in `tool_files.go:82`; SFTP opens that string. (d) One request id per request; `command.finished` carries the starter's. (f) `enter()` refuses after `Gateway.Shutdown`, `engine.reserve` after `closing`; every failed audit write is logged, `gateway.stopped` included. A handler that outlives a timed-out shutdown can write a refusal after `gateway.stopped`; it cannot act.

**(e) Limits:**

| Limit | Enforced at |
|---|---|
| `default_wait_seconds` | `tool_commands.go:192` |
| `max_wait_seconds` | `tool_commands.go:196,325`; `jobs/engine.go:703`; `jobs/read.go:68` |
| `default_timeout_seconds` | `jobs/engine.go:342` |
| `max_timeout_seconds` | `tool_commands.go:183`; `jobs/engine.go:344` |
| `max_concurrent_commands` | `jobs/engine.go:439` |
| `max_concurrent_per_server` | `jobs/engine.go:442` (commands only, finding 4) |
| `max_command_bytes` | `policy.go:258`; `jobs/engine.go:335` |
| `max_output_bytes` | `jobs/output.go:130` |
| `max_result_bytes` | `call.go:211,234`; `jobs/read.go:69` |
| `retention_days` | `jobs/lifecycle.go:227` |
| `max_data_bytes` | `jobs/engine.go:356`; `jobs/lifecycle.go:244` |
| `max_upload_bytes` | `tool_files.go:180` |
| `kill_grace_seconds` | `sshclient/command.go:268-272` |
| `connect_timeout_seconds` | `sshclient/client.go:206`, `command.go:146`; `tool_files.go:90`, `tool_servers.go:153` |
| `shutdown_grace_seconds` | `serve.go:281`; `jobs/lifecycle.go:102` |

**Prefilter (item 3).** No command found. Every built-in pattern is a concatenation that contains its word group or fixed piece; both branches of `write-to-device` contain `/dev/`, both of `rm-root` contain `rm`; no pattern is case-insensitive; quotes and backslashes sit around the word, never inside it. Operator rules have no `Needs` and always run. `mayMatch` and the expression see the same string.

**Files (item 4).** Database, lock, outputs and audit log are created 0600 and directories 0700 with explicit modes, so a permissive umask widens nothing; existing files are checked on the open descriptor. No temp file, Unix socket, child process or environment read in product code; `argv` holds the config path only.

**Resources (item 5).** Defaults: 24 request slots of a few MiB each (the 2 MiB body and its copies), 4 commands with one SSH window each. Before the guard a caller costs headers only (32 KiB); connections are unlimited, as section 6 says. Records and output are bounded by quota and retention; the audit log is not (finding 3).

**Test kits (item 6).** `go list -deps ./cmd/...` holds no `internal/testkit`, `test/e2e` or `testing`. No build tag or test flag in product code. The suite dials loopback only: `example.net` appears as a redirect target the fetcher must refuse, and every `serve` config sets a local `certs_url`. CI uses no secret; actions are pinned by commit.

**Dependencies (item 7).** `go mod verify`: all verified. `govulncheck ./...`: none. Both builds use `CGO_ENABLED=0 -trimpath`. Modules in the binary:

- `go-jose/go-jose/v4` v4.1.9
- `google/jsonschema-go` v0.4.3
- `modelcontextprotocol/go-sdk` v1.8.0 -- imports `os/exec` (a client transport, never built here)
- `pkg/sftp` v1.13.12
- `kr/fs` v0.1.0 -- old, tiny, via sftp
- `go.yaml.in/yaml/v3` v3.0.6
- `golang.org/x/crypto` v0.59.0
- `golang.org/x/oauth2` v0.35.0, `x/sync` v0.24.0, `x/time` v0.15.0 -- via the SDK, unused paths
- `golang.org/x/sys` v0.50.0
- `segmentio/encoding` v0.5.4, `segmentio/asm` v1.1.3 -- `unsafe`; the SDK parses every authenticated body with it
- `yosida95/uritemplate/v3` v3.0.2 -- via the SDK
- `modernc.org/sqlite` v1.62.0, `libc` v1.80.1, `memory` v1.12.1, `mathutil` v1.7.1 -- `unsafe`; `libc` imports `os/exec`
- `dustin/go-humanize` v1.1.0, `google/uuid` v1.6.0 -- via `libc`
- `remyoudompheng/bigfft` pseudo-version of 2023-01-29 -- via `mathutil`

**Hygiene (item 8).** Tree and all 91 commits: only `example.net`, `example-team.cloudflareaccess.com`, documentation and loopback addresses; no token; the one `PRIVATE KEY` match is a marker a test writes. Nothing real found.
