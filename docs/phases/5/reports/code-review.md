# Phase 5 code review -- MCP endpoint and tools

Range `cf21613..9c6daff`. Nothing was run. Verdict: **REQUEST CHANGES** -- 1 blocking, 10 advisory.

## Findings

1. **BLOCKING -- error results break the declared output schema, and real clients reject them** (`internal/mcpserver/call.go:297-307`, choice 5). Every tool declares an `outputSchema` inferred from its success type (`additionalProperties: false`, required fields), and every error result carries `structuredContent` `{code, message, rule?}`, which cannot validate against it. The MCP specification says a server with an output schema MUST return structured results that conform, and has no exemption for `isError`. The TypeScript SDK client validates `structuredContent` whenever it is present, also when `isError` is true (only a missing one is excused for errors). I read that code on this machine in the Claude Code CLI bundle and in a bundled `@modelcontextprotocol/sdk`. So after `tools/list` every error answer becomes a protocol error "Structured content does not match the tool's output schema", and the model never sees the code, message or rule. The Go SDK client validates nothing, so no test sees it. Fix: error results carry text only (keep `error <code>: <message> (rule <r>)`, or make the text the JSON object), as the SDK's typed path does; `wantErr` parses the text; the 5.8 callout is corrected; a test validates each tool's success result against its `OutputSchema` and asserts an error result has no structured content.

2. **ADVISORY -- `run_command` takes start time plus the wait, and only the wait is budgeted** (`tool_commands.go:193-211`, `gateway.go:209-217`). `jobs.Start` connects and waits for the exec reply (up to twice `connect_timeout_seconds`, which may be 120) before the wait begins. `WriteTimeoutFor` allows `max_wait_seconds` + 15 s, and the 110 s cap leaves 15 s under the edge's 125 s. A slow connect and a full wait lose the answer while the command runs on. Fix: count the wait from the start of the call (`wait - time.Since(start)`), and make the write timeout cover `2 * connect` as well.

3. **ADVISORY -- a client that goes away stops the wait only on the 2026-07-28 protocol** (`gateway.go:164-171`; 5.8 callout "Transport"). `PropagateRequestCancellation` applies to that protocol only. For 2025-11-25 and older the SDK gives handlers a context that never ends, and `session.Close` waits for them, so the request and its slot stay until the wait or transfer deadline expires. Fix: pass `r.Context()` into `newServer` and end the handler's context when it ends (the server is per request already); then the SDK option is not needed. Correct the callout.

4. **ADVISORY -- shutdown does not wait for handlers after the forced close** (`internal/cli/serve.go:273-296`, choice 8). Running both shutdowns together is right, but probes and transfers (connect + 20 or 30 s) are not ended by the engine, and the default grace + 5 s is shorter. After `srv.Close()` nothing waits for them, so `tool.failed` or `file.*` can follow `gateway.stopped` or hit a closed log. Fix: a gateway context cancelled at shutdown for probes and transfers, and a wait for in-flight calls before `guard.Close`.

5. **ADVISORY -- `list_commands` can answer "no commands" with a cursor past commands it never showed** (`tool_commands.go:399-409`). When the first entry alone is over the budget, `fits` is 0, the retry is skipped, and the page's `next_cursor` is returned. Reachable near `max_result_bytes` 4096 with a preview of 200 unprintable astral characters (about 3.4 KB after `%q` and JSON escaping). Fix: answer `internal` with `msgTooLarge`; never an empty page with a cursor.

6. **ADVISORY -- `download_file` can corrupt a character silently** (`tool_files.go:137-153`). A page that is only an incomplete UTF-8 sequence (`max_bytes` 1 to 3) passes `isText` (the trimmed prefix is empty), `fitText` replaces it with U+FFFD, and the result has no `lossy` field. Fix: `isText` returns false when `end == 0 && len(data) > 0`.

7. **ADVISORY -- unvalidated arguments land in typed audit fields** (`tool_commands.go:171-173`, `276`, `341`, `443`; `tool_files.go:67`, `163`). `plan.server` and `command_id` are set before their pattern checks, so `tool.called` and `tool.failed` can carry arbitrary text there; `upload_file` decodes up to 2 MiB, so `server`, `path` or `mode` can be megabytes per event. Fix: set the ids only after validation, as `prepareServerInfo` does, and bound `path`, `mode` and `cursor` before auditing.

8. **ADVISORY -- a caller that went away is recorded as a gateway fault** (`call.go:203-211`, `tool_servers.go:162-164`): `tool.failed` with `internal` plus a WARN line. Fix: when the call's context has ended, skip the WARN.

9. **ADVISORY -- tests.** `subscriptions/listen` is refused only under the 2025-11-25 header, where the SDK opens no stream: add the 2026-07-28 case (`http_test.go:200-208`). No test has a request waiting when shutdown starts. `New` accepts `max_result_bytes` from 1024 while pages stop advancing below about 2600; require 4096 (`gateway.go:148`).

10. **ADVISORY -- model and source nits.** `serve` belongs to `yasshm.http`, loads the config, owns the engine and writes `gateway.started` and `gateway.stopped`: add `http -> config`, `http -> jobs`, `http -> audit` to `model.c4` and the section 4 list; the section 4 row should name `internal/cli`. `output.go:18` holds the literal invisible runes U+2028 and U+2029: write them as escapes.

11. **ADVISORY -- a signal during start-up** exits 1 with a wrong reason ("the job engine cannot start", "the listen address cannot be bound"), because `ctx` is already cancelled (`serve.go:158-195`). Check `ctx.Err()` first.

## Checked and sound

- **Call path.** Tenant, decode and validate, target in the caller's tenant, `tool.called`, policy, action. `tool.called` is written for every call that reaches a tool, including bad arguments, unknown servers, a missing principal and a panic. Not audited as tool calls: an unknown tool name, malformed JSON-RPC, a refused method (`auth.accepted` only) and a 503 from the cap. A fair reading of 5.4. No action can run with a nil server. `read_output` and `kill_command` read the record in the caller's tenant first.
- **No policy decision** for `list_servers` and `list_commands` without a server: sound for D5 and 5.3 (tenant-scoped, one level). A second level or per-principal grants would need a filter there.
- **Callouts.** D9, D8 and D12 are intact. Rule names `config:deny_*[n]` reveal an index only (Phase 1). The 110 s maximum, the 4096 minimum and `tool.failed` are fair additions.
- **Sizes.** `jsonCost` is an upper bound for `encoding/json`; `fit` measures and `invoke` measures again. Followed by hand: a split character and invalid bytes across two pages neither stall nor skip. `run_command` and `read_output` share the clamp and the page budget.
- **Errors.** Every path ends in one of the ten codes with fixed text; no panic value or wrapped text escapes.
- **HTTP.** Routing is exact. `ReadTimeout` does not cut a long wait (`net/http` clears the read deadline once the body is read). The slot is released by `defer`. Per-request servers share only read-only tool definitions. Only the tools capability is advertised, so choice 11 is safe.
- **`serve`.** Order, exit codes, a failed start and logs are as described.

## The 17 choices

Right: 1, 2, 3, 4, 6, 7, 9, 10, 11, 12, 13, 14, 15, 17. Choice 16 is right to defer: `policy` checks the length before any expression, so the cost is bounded by `max_command_bytes`. Choice 8 is right with finding 4. Choice 5 is wrong: finding 1.
