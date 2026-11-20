# Phase 7 simplification lane

Range `038cdc0..7a7a41a`. Propose only; nothing run but `git` and reads.

## Proposals

1. **APPLY-RECOMMENDED -- the "not guaranteed" list is written four times.** `README.md:44`, `SECURITY.md:57-61`, `docs/operator-guide.md:25-35`, and again in part at `docs/operator-guide.md:336`. The guide's "What yasshm is not" is the owner (the operator acts on it). Change `SECURITY.md:59` to one sentence: "The table above is the list; [What yasshm is not](docs/operator-guide.md#what-yasshm-is-not) says what to do about each." Keep `SECURITY.md:61` (the accepted bypasses, which belong in a threat model). Cut `README.md:44` to one sentence of short clauses that keeps the six phrases the spec 6 table quotes ("that the denylist cannot be bypassed", "that output is free of secrets", "availability under a request flood", "the tool descriptions label output as untrusted", "another local user"; "decided by the account it runs as" is on line 42), drop the 60-a-minute detail, and link the guide. Also fix `operator-guide.md:27`: it says each item "is checked against `README.md` ... by a test", but no test reads the guide's list. Nothing is lost because each fact stays once in the guide. `TestSecurityModelTable` fails if a quoted README phrase goes missing. Saves about 350 words. Risk: low.

2. **APPLY-RECOMMENDED -- upgrade, rollback and uninstall are written twice.** `deploy/README.md:150-167` and `docs/operator-guide.md:156-173`. The guide owns them: they come after the install, and `SECURITY.md:11` already links its anchor. First move the facts only `deploy/README.md` has into the guide's Uninstall paragraph: the `rm` and `daemon-reload` commands, keep `/var/log/yasshm`, and `userdel yasshm` last. Then replace the two `deploy/README.md` sections with one line: "Upgrade, rollback and uninstall: [operator guide](../docs/operator-guide.md#running-upgrading-and-rolling-back)." `TestDocsLinks` checks the anchor. Saves about 16 lines. Risk: low.

3. **APPLY-RECOMMENDED -- one helper for reading the guide's tables.** `guideBlock` is copied word for word in `internal/audit/guide_test.go:22` and `internal/mcpserver/guide_test.go:16`, and written inline again in `internal/config/guide_test.go:21-35` and `internal/store/guide_test.go:14-22`. `firstCells` is in three places (audit:192, mcpserver:34, store:24-29). Put `Block(t, name)` and `FirstCells(block)` in `internal/testkit/guidedoc`, as the gotcha "Shared test support is in `internal/testkit`" asks, and move `TestGuideHelpers` there. Every assertion stays as it is. Saves about 60 lines. Risk: low (`internal/testkit` is outside the coverage count). Optional follow-on: add `startup-reasons` markers to the guide's start-up table so `internal/cli/guide_test.go:39-52` can use the helper and drop its `"| \`the "` prefix check.

4. **APPLY-RECOMMENDED -- the ASCII check runs twice.** `deploy/unit_test.go:158` (`TestUnitIsASCII`) and the loop at `deploy/example_test.go:83-89` repeat `TestDocsASCII`, which already covers `.service` and `.yaml` (`docs_test.go:179`). Delete both, keep the half of `TestExampleIsASCIIAndSynthetic` that checks for synthetic values, and rename it `TestExampleIsSynthetic`. The `deploy` step's `-lt 5` count still passes, because six `Test(Unit|Example)` tests remain. One difference: `TestDocsASCII` accepts `\r` and the unit test refused it. If that matters, add `\r` to `nonASCII`'s refusals, which tightens the check for every doc. Saves about 18 lines. Risk: low.

5. **APPLY-RECOMMENDED -- one function for the shared shape of `deploy` and `docs` in `scripts/check.sh`.** Lines 162-180 and 210-225 do the same thing: run `go test` into a temp log, print the log and fail on error, then count `--- PASS` lines against a minimum. Add `go_test_step <step> <min> <pass-regex> <go test args...>` and print the same messages it prints now. Optional, and stronger: take the minimum from `go test -list`, as `run_e2e` does at line 122, instead of the fixed 5 and 4. Saves about 15 lines. Risk: low (shell only; the gate's output does not change).

6. **OPTIONAL -- the gate step list is described in three places.** `README.md:92` names the steps and also describes `e2e`, `deploy` and `docs`; `TESTING.md` owns those descriptions. Cut `README.md:92` to the step names, the `-- <step>` form and "see `TESTING.md`". Saves one long line. Risk: low.

7. **OPTIONAL -- small repeats inside the operator docs.**
   - The service-principal warning is at both `operator-guide.md:121` (its last two sentences) and `:339`. Keep it at `:339` and link to it from `:121`.
   - The `/healthz` answers are at `deploy/README.md:134`, `README.md:83` and `operator-guide.md:305`, which owns them. At `deploy/README.md:134`, link to [HTTP statuses](../docs/operator-guide.md#http-statuses) instead of quoting both bodies.
   - `operator-guide.md:103` explains `restrict` and the forced command again, after `deploy/README.md:78-79`. Shorten it to "starts with `restrict`, no forced command (why: deploy/README step 4)".

   Saves about 6 lines. Risk: low.

8. **DO-NOT-APPLY -- dropping the re-runs in `deploy` and `docs`.** The `test` step already runs `./deploy`, `internal/deprule` and `internal/config`, so the two steps run those tests a second time. Removing the re-runs would take away the per-step PASS counts and named output, which changes what the gate reports.

9. **DO-NOT-APPLY -- removing default directives from the unit.** `Type=simple` and `KillSignal=SIGTERM` are defaults, but `TestUnitDirectives` pins `KillSignal`, and both lines state intent next to the comments about the graceful stop.

## Left alone, on purpose

- **Unit comments** (`deploy/yasshm.service`): each one says why (fail-closed paths, EPERM rather than SIGSYS, the `IPAddressDeny` template), not what the directive is.
- **Example config comments**: `TestExampleLimitComments` parses their "Default n, range a to b." form.
- **Manual-test headers**: the Runner, Placeholders, Live budget, Automate when and Preconditions bullets, step 1 and the Result line are the `manual-test` format, and `TestManualTests` checks them. The repeated sentence about `mise.local.toml` is borderline, but it is per file and short.
- **Marker blocks and the audit sample block**: tests generate or compare them, so they cannot be shortened by hand.
- **`workflows_test.go`**: one parser, with no duplication.
- **The README quick start's explanation of `degraded`**: that is the result a reader of that section actually sees.
