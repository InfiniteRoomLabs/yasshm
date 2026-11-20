# Phase 6 simplification lane

Range `ce5df4f..a5ea5c9`. Read-only review: nothing was run (no gate, tests or builds), so no proposal has been tried and the line counts are estimates. Every proposal keeps the scenarios, their assertions, the gate output, every policy decision and the guarantee table; a changed failure message is named.

## Proposals

**1. APPLY-RECOMMENDED -- `gateway.stop` owns the exit-0 check.** `test/e2e/harness_test.go:459`. All ten callers repeat the same three lines (`tools_test.go:139`, `lifecycle_test.go:63`, ...).
- Before: `if code := g.stop(); code != 0 { t.Fatalf("exit %d", code) }`
- After: `g.stop(t)`; `stop(t *testing.T)` does the `Fatalf`.
- Preserving: same signal, wait and failure text. The timeout `Fatalf` moves from the parent's `g.t` to the calling subtest's `t`.
- About -20 lines. Risk: low.

**2. APPLY-RECOMMENDED -- the fixture owns start, ready, connect.** 14 sites repeat `g := f.start(t); g.waitReady(t); c := connect(t, g.edge(t))`: `commands_test.go:19`, `97`, `121`, `143`, `174`, `217`, `252`; `audit_test.go:80`, `161`; `lifecycle_test.go:23`, `40`, `80`, `160`; `tools_test.go:20`.
- After: `g, c := f.serve(t)`, a six-line helper beside `start`. `auth_test.go` and `leak_test.go` keep `start` (they need the edge, or no client).
- Preserving: same calls in the same order. About -21 lines net. Risk: low.

**3. APPLY-RECOMMENDED -- one helper for "is this event in the audit log".** Five hand-rolled loops: `commands_test.go:105-111` and `194-201`, `audit_test.go:180-187`, `lifecycle_test.go:53-60` and `147-150`. Four ask for a `command.finished` with an id and a status.
- After: `f.finished(t, id, status) bool`, built on the existing `f.events` (`harness_test.go:856`); the policy loop becomes `len(f.events(...)) == 0`.
- Preserving: same predicates, same fatal text; `lifecycle_test.go:143` reads the log twice instead of once. About -18 lines. Risk: low.

**4. APPLY-RECOMMENDED -- drop `rewrite`, use `configtest`.** `test/e2e/lifecycle_test.go:199-215` copies `configtest.Files.Rewrite` (`internal/testkit/configtest/configtest.go:86`).
- After, at `lifecycle_test.go:247`: `p := configtest.Files{Config: f.config}.Rewrite(t, tc.old, tc.repl)`.
- Preserving: identical logic; the "not in the config" message gains a `configtest:` prefix. It also makes `doc.go:11` true (it lists `configtest`, which the suite does not import).
- -17 lines. Risk: low.

**5. OPTIONAL -- read the audit log through `audittest`.** `test/e2e/harness_test.go:810-853`. `auditLine` is `audittest.Line` plus `Reason` and `Listen`; `auditLog` is `FailLog.Lines` plus the unterminated-last-line rule. A `CLAUDE.md` gotcha forbids such copies.
- After: add the two fields and `audittest.Read(t, path) (lines []Line, rest string)` (the e2e body, moved); `Lines` calls it and fails on a non-empty `rest`; `f.auditLog` becomes a one-line wrapper; the 12 mentions of `auditLine` become `audittest.Line`. `TestEndToEndImports` already allows `internal/testkit`.
- Preserving for a well-formed log. Differences: `Lines` would accept an empty line and lose the scanner's 4 MiB line cap.
- About -30 lines net. Risk: medium (nine in-process callers of `Lines`). Lower-risk variant: leave `Lines` untouched, about -20.

**6. OPTIONAL -- `-race` on the `go test -list` call.** `scripts/check.sh:121`.
- After: `CGO_ENABLED=1 mise exec -- go test -race -list '^TestE2E' ./test/e2e`.
- Preserving: same list, same output. The run on line 127 builds with `-race`; the list call builds a second, non-race copy of the dependencies. The saving is inferred, not measured. Risk: low.

**7. OPTIONAL -- two small ones.** `leak_test.go:177-183`: the hand-rolled poll becomes `leakcheck.Poll(20*time.Second, func() bool { closeIdle(); after = fdCount(t, g.pid); return after <= idle })` (a 10 ms interval instead of 50 ms; -4 lines). `audittest.ErrInjected` (`audittest.go:23`) has no user outside its package: unexport it.

**8. DO-NOT-APPLY -- one function for the `sshd` and `e2e` steps.** `scripts/check.sh:84-92` and `117-131` share only the temp file, the trap and run-capture-fail, about 8 lines. The counting differs in kind: a ratio from one log of indented subtest lines, against a `-list` count of top-level lines. A shared function saves about 5 lines, and the sandbox cannot run the `sshd` branch.

**9. DO-NOT-APPLY -- derive `Needs` from the patterns.** `internal/policy/commands.go:136-179`. The standard library has no required-literal extraction (`LiteralPrefix` gives a leading literal only, and every rule starts with a group). Writing one over `regexp/syntax` is more code than the 15 lists it replaces. Three rules also choose a literal that is not the command word (`of=`, `/dev/`, `()`). `TestCommandRuleNeeds` and the fuzz invariant already catch drift.

## Left alone

- **Guarantee checker** (`internal/deprule/guarantees_test.go`): the table is already one row per line and `parseTables` is 30 lines; another format would change the table.
- **The empty-`Needs` branch of `mayMatch`** (`commands.go:32`): production never reaches it (operator rules do not call `mayMatch`, `policy.go:241`), but it makes a built-in rule without literals run its expression instead of being skipped.
- **Result structs and client helpers** (`harness_test.go:552-744`): they mirror the `internal/mcpserver` harness, which the import rule keeps out of the suite.
- **`fixture.write`**: `configtest.Write` has fixed hosts and no targets.
- **One gateway each** for `TestE2ETimeout`, `TestE2EOutputLimit` and `TestE2EBusy`: merging them changes which scenarios exist, and the suite takes 4 s.
- **Comments**: the long ones (`Needs`, `limitFileSize`, `run_e2e`) carry evidence or a choice. Nothing to cut.
