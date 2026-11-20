# Phase 5 simplification review

Range `cf21613..9c6daff`; line numbers at `9c6daff`. Nothing was run. Every APPLY item leaves the wire, the audit log and the log lines byte-identical.

## APPLY-RECOMMENDED

1. **One form for argument checks** -- `tool_commands.go:176-187, 280-289, 342-351, 447-452`, `tool_files.go:68-75`. Each `switch` runs every check twice (`case checkX(..) != nil: p.err = checkX(..)`). After: `p.err = cmp.Or(checkServerID(a.Server, true), checkNonNegative(a.WaitSeconds, "wait_seconds"), argIf(cond, "fixed message"))` with `func argIf(bad bool, msg string) error`. Same first-failure order; the checks are pure, so evaluating all of them changes nothing. -18 lines and ten duplicate calls. Risk: low. Leave `prepareUpload`'s switch: it also decodes.

2. **`invoke` writes `tool.called` in one place** -- `call.go:135-150`. The `newCall` failure branch repeats the audit write and the `fail`. After: `var p plan; c, terr := g.newCall(a); if terr != nil { p.err = terr } else { p = d.prepare(ctx, g, c, raw) }`, then the existing path. A zero `plan` gives the same event (server `""`, empty args) and `toolError` returns a `*ToolError` unchanged. -6 lines. Risk: low (assign `terr` only when non-nil).

3. **One source per pattern** -- the server id expression is at `args.go:17`, in the message at `args.go:106` and at `tools.go:188`; the command id at `args.go:18`, `tools.go:210, 224`; the mode at `args.go:23`, `tools.go:238`. After: three string constants feeding `regexp.MustCompile`, the message and the schema `Pattern`. Same strings; validator and schema can no longer drift. Risk: low.

4. **One page header** -- `tool_commands.go:240-249` and `319-328` build the same `; <stream> bytes <a>..<b> of <n>[, lossy][, eof]` line. After: `pageHead(status, stream, from, to, size, lossy, eof)`; `runPage` passes `"stdout", 0`. -8 lines. Risk: low; `TestRunCommand` pins the text.

5. **Small cuts**, each low risk:
   - `output.go:61-63`: dead; `utf8.DecodeRune` never returns size 0 for non-empty input. -3.
   - `output.go:18`: two invisible literal runes (U+2028, U+2029) -> Go unicode escapes.
   - `call.go:215`: `auditFailed`'s unused `error` parameter.
   - `call.go:327`: let `fit` clamp to `c.payloadBudget()`; drop the four `min(payload, c.payloadBudget())` (`tool_commands.go:229, 312`, `tool_files.go:113, 117`).
   - `harness_test.go:302, 310-312`: `tokenTransport.host` is never set. -4.
   - `harness_test.go:445-446`: stale comment (`events` takes a match function).
   - `harness_test.go:393-401, 412-420`: the same strict decode twice -> one helper. -7.
   - `tools_test.go:531-536, 588-593`: the same stage loop -> `h.stages(t, event, path)`. -8.

## OPTIONAL

6. **Shared test support in `internal/testkit`** -- `mcpserver/harness_test.go:33-93` copies `jobs/harness_test.go:23-81` (`testLimits`, `testSSH`, `serverFor`, `failAudit`). A locked buffer exists five times, three new in this phase (`mcpserver/harness_test.go:134`, `cli/serve_test.go:32`, `cmd/yasshm/main_test.go:152`). The "`@` means a user" token choice is at `edgetest.go:113-118` and `harness_test.go:291-296`; four helpers read the audit log. After: `testkit/audittest` (`FailLog`, `Read`), one `SyncBuffer`, `(*accesstest.Issuer).For(who)`. About -100 lines. Risk: low to medium (touches Phase 3 and 4 tests). Best at the start of Phase 6, before `test/e2e` adds a copy.

7. **`serve.go` trims** -- `watchSignals` (`61-77`): two `select`s in sequence instead of a counter, -7. `stopped` (`131, 294, 300`): closing a closed listener returns an error that is already ignored, -3. `errorKind` (`234-239`): `Serve` cannot return `ErrServerClosed` before `shutdown` runs, so it always says `accept`, -6. Risk: low.

8. **Allowed keys from the argument type** -- the eight key lists passed to `decodeArgs` repeat the structs' `json` tags, which the schemas are built from. After: `decodeArgs[T]` reads the tags by reflection (about ten lines). No lines saved; it removes a silent drift between schema and decoder. Risk: low to medium.

9. **One "entries while they fit" loop** -- `tool_servers.go:88-104` and `tool_commands.go:382-398` share the cost arithmetic. After: one helper returning how many entries fit. -12 lines. Risk: medium (`TestResultSize` protects that formula).

10. **`TestRequestCap` waits on an event** -- `http_test.go:307` holds the slot with `sleep 3`. After: `sleep 30`, ended through `h.e.Kill` once the 503 is asserted. -3 s per run. Risk: low.

## DO-NOT-APPLY

11. Typed `mcp.AddTool`: SDK v1.8.0 `mcp/server.go:405` answers `validating "arguments": %v`, which echoes input and is not a fixed text.
12. `Decoder.DisallowUnknownFields` in `decodeArgs`: `encoding/json` matches keys case-insensitively and accepts duplicates (`TestDecodeArgs` pins both). `strings.ToValidUTF8` in `fitText`: no budget, no held-back split character.
13. A closer table or `errors.Join` in `serve`: `shutdown` and `close` differ in order, deadline (grace plus 5 s against 30 s) and concurrency, and every start failure has its own fixed reason. About eight lines saved for a changed shutdown.
14. Merging `CommandStatus` and `CommandEntry`: the entry embeds the status; nothing is parallel.

## Left alone

- The tenant id on every call, `upload_file`, exported constants and result types, `edgetest` modes, `Config` options: by design.
- Unreachable marshal-error branches (`call.go:285-288, 298-301, 311-314`, `gateway.go:351-355`): lint wants the error handled either way.
- `isText`, `shrinkInfo`, the retry in `listPage`: compact and pinned by the size tests.
- Comments: they carry sources and choices; only the stale one in item 5 changes.
- 100 commands per limit in `TestResultSize`: slow, but it is the largest-result claim.
