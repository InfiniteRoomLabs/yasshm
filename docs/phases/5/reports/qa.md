# Phase 5 QA -- MCP endpoint and tools

Verdict: **NEEDS WORK** -- 2 blocking, 5 advisory. Head `35d1fe8` (code head `a842d66`), 2026-11-20. Both blocking defects of the read-only lanes are reproduced by running code.

## QA lane incident (mine, not the product's)

My first hand smoke script passed `--noproxy *` unquoted. The shell expanded `*` to the file names of the session scratch directory and `curl` took them as URLs. One of those names is also a registered public domain: a parked page answered about 55 plain-HTTP requests, 11 of them `POST`s carrying the fixture's token header and synthetic JSON-RPC bodies (`lab-1`, `echo hello`). The other names got no connection. The token was signed by a key generated in my fixture process and accepted only by that throwaway loopback gateway; both were stopped within three minutes and the key no longer exists. No real credential, host or config value left the machine. The run was repeated with globbing off, loopback only (check 3). Check 7 holds for the product and was broken once by this lane.

## Findings

1. **BLOCKING -- a JSON-RPC batch is served** (`internal/mcpserver/gateway.go:365-369`; security finding 1). Expected: one request, one tool call, inside the request cap and `max_result_bytes`. Observed with raw `net/http` against the real handler, array body of 50 `tools/call`, request cap set to 1, `max_result_bytes` 4096:
   - No `MCP-Protocol-Version` header, and `2025-03-26`: 200, 50 answers, 50 `tool.called`, run concurrently. `2025-06-18` and `2025-11-25`: 400 "JSON-RPC batching is not supported", no `tool.called`.
   - 50 `run_command` of `sleep 2`: 2 succeeded, 48 `busy`, at most 2 sessions on `sshtest`, 2.1 s. The engine's limits hold.
   - 50 `get_server_info`: 50 reachable, 50 SSH connections accepted, 38 to 42 open at once, 0.3 s, on one request slot.
   - 50 `read_output` pages: one HTTP body of 97,692 bytes against the 4096 bound.
   - 50 `read_output` with `wait_seconds` 3: 50 waiters answered together after 3.16 s, on a cap of 1.
2. **BLOCKING -- error results do not validate against the declared output schema** (`internal/mcpserver/call.go:297-307`; code-review finding 1). With `jsonschema-go` v0.4.3 and the schemas `tools/list` returned: the success result of each of the eight tools validates; the error result of each of the eight does not ("unexpected additional properties [code message]", plus `rule` on denials).
3. **ADVISORY -- `list_commands` can hide every command** (`internal/mcpserver/tool_commands.go:382-401`; code-review finding 5, reproduced and wider). `max_result_bytes` 4096, a 31-character server id, three commands of 200 unprintable astral characters: expected one command per page; observed "no commands" with a cursor, page after page. Cause: the retry with the smaller limit adds `next_cursor` to the base cost, so the entry that fitted no longer does. With the id `srv-1` they fit with 2 bytes to spare.
4. **ADVISORY -- nothing fails when `get_server_info` returns the probe's login name** (`internal/mcpserver/leak_test.go:59-66`, `CLAUDE.md:141`). The mutant survived in two forms (a `user` fact; the name appended to `os`). The probe runs `id -un`, on `sshtest` the test process's user, not the configured marker user, so the gotcha's claim is false. Fix: assert the current OS user's name is absent from the result.
5. **ADVISORY -- the tenant lookup in `mcpserver` is not tested by itself** (`internal/mcpserver/tool_servers.go:129`). A lookup in every tenant survived: `policy.Decide` (`internal/policy/policy.go:204`) and `sshclient` (`client.go:103`) still refuse.
6. **ADVISORY -- `download_file` corrupts a split character without saying so** (`internal/mcpserver/tool_files.go:137`; code-review finding 6, by running): one byte of a 3-byte character returns `encoding: text`, `data` U+FFFD, `next_offset` 1, with no `lossy` field.
7. **ADVISORY -- `README.md:40`** shows `"version":"dev"`; the binary from `mise run build` answers `"version":"35d1fe8"`.

## Checks

1. **Gate.** `mise run check`: `cover: total 94.7% (floor 85%, internal/testkit excluded)`, `No vulnerabilities found.`, `check.sh: all OK`, exit 0; `git status --porcelain` empty before and after. CI `check completed success` on `35d1fe8` and `a842d66`.
2. **Deliverables.**
   - Eight tools and no others: `TestToolsList` (8 by hand too). Met, except finding 2.
   - Call path: `TestF6IdentityReachesHandler`, `TestTenantIsolation`, `TestAuditBeforeAct`. Met.
   - Tool behaviour: one `Test<Tool>` per tool in `tools_test.go`, plus `TestRunCommandWaitClamped`, `TestRunCommandBusy`. Met, except findings 3 and 6.
   - Result size: `TestResultSize` at 4096 and 140000. Met for one call; not for a batch.
   - D8: `TestNothingFromTheConfigLeaks` reaches results, errors, schemas, headers and logs; not the login name (finding 4).
   - HTTP surface: `TestHTTPSurface`, `TestRequestCap`, `TestDerivedLimits`, `TestBehindTheEdge`. Met, except finding 1.
   - `serve`: `TestServe` (start, a call through `edgetest`, `/healthz`, second gateway refused, shutdown, `gateway.started` and `gateway.stopped`), `TestServeSignal`. Met.
   - Maximum wait 110: `internal/config/limits.go:26,31`, `config_test.go:340`, spec lines 153-154 and 169. Met.
   - `edgetest` (`TestEdge`), architecture, dependency rule, static build (15.5 MB amd64): gate. Met.
3. **Real binary** (`dist/yasshm_35d1fe8_linux_amd64`, issuer and `sshtest` from a fixture in my clone, `curl`): `config check` exit 0; `/healthz` 200; `/mcp` without or with a bad token 401 `{"error":"unauthorized"}`; another path 404; `initialize`, `tools/list` (8), `list_servers`, `run_command` (`hello\n`, eof), `read_output`, `sudo id` (`policy_denied`, rule `sudo`), a `sleep 60` returned `running` after `wait_seconds` 1, `kill_command` (TERM delivered, then `killed`), `get_server_info` (no login name), `upload_file` (`access_denied`). `SIGTERM`: exit 0, the running command ended `killed`, the port then refused. Audit: 39 events, first `gateway.started`, last `gateway.stopped` (signal). Stderr: 7 JSON lines, no token, command text, key path, target address or audience. Matches the spec and README, except finding 7.
4. **Fidelity.**
   - (a) 20 bytes with invalid UTF-8 and split characters, `max_bytes` 1, 2, 3, 4, 4096: every byte covered, `lossy` exactly on pages with a replaced byte, no stall. Valid text at 3 to 5: identical, never lossy.
   - (b) 200 multi-byte characters (2, 3 and 4 bytes, `<`, unprintable astral), 7 commands each: largest body 3582, every command once, cursor always advanced. Finding 3 lies just beyond.
   - (c) 300 KiB of random bytes, `max_bytes` unset: 9 pages, largest body 97,806 of 100,000, identical after reassembly.
   - (d) Largest bodies at 4096 / 140000: `run_command` 2050 / 137,954; `read_output` 1968 / 137,868; `download_file` 1934 / 137,846; `list_servers` 408 / 136,856; `get_server_info` 2990 / 5638; `list_commands` 3026 / 137,188; error results 189 to 331.
5. **Mutations** (8, in my clone): killed -- skip `policy.Decide` (8 tests), skip `tool.called` (`TestAuditBeforeAct`, `TestHTTPSurface`), no `read_output` clamp (`TestResultSize`), no guard (5 tests), `upload_file` at `look` (`TestToolsList`), no request cap (`TestRequestCap`). Survived -- tenant lookup (finding 5), login name (finding 4).
6. **Flakiness.** `-race -shuffle=on -count=8` on the four packages: all `ok`; no test process or child left.
7. **Limits (D16).** The product reached only loopback and used no credential; see the incident above.
8. **Docs.** ASCII-only; the placeholder grep prints nothing; `yasshm.tools` and `yasshm.http` are not `#planned`; `arch` green; changelog entries present; the 5.1 callout matches the code. The 5.8 callout (spec line 306) is false for a batch (finding 1).
