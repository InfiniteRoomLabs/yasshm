# TESTING.md -- how yasshm tests

Referenced from `CLAUDE.md` and `README.md`. This is the single source of truth for **how we test**; the concrete steps are the ones `scripts/check.sh` runs. The gate, in order, is `fmt` (gofmt reports nothing), `lint` (golangci-lint, `.golangci.yml`, warnings are errors, `nolint` needs a named linter and a reason), `skipcheck` (no skipped test without an issue link on the same line), `test` (`go test -race -shuffle=on -count=1` with a coverage profile over all packages, written to the gitignored `coverage.out`), `cover` (total at or above the floor, 85 percent, `internal/testkit/...` the only exclusion; it fails if there is no profile, so run `test` first), `vuln` (`govulncheck ./...`), `build` (the release files in `dist/`) and `arch`. Each runs alone as `mise run check -- <step>`. A dirty tree fails the gate. When `sshd` is not installed locally the gate prints a line saying the real-sshd lane runs in CI only. A `cover` result counts only right after `test` on the same tree (the full gate always does that). Test helpers that can skip live only in `_test.go` files or `internal/testkit/`. `mise run fuzz` runs every `Fuzz*` target for 30 seconds each and is not part of the gate. How the rules below apply to this project (the layers, the test support packages, the scenarios) is spec section 7 (`docs/superpowers/specs/2026-11-18-yasshm-design.md`).

## What gets an automated test, and what cannot

Everything that can run without the real edge, a real target host or a secret gets an automated test in the gate: unit, integration and end to end. No test reaches the network, a real host or a credential. What cannot be automated under those limits is a manual test (last section), run by the operator and never by the build loop.

## The green rule -- always run green, never ignore, never silence

Non-negotiable, enforced by tooling, not honor system:

- **Never skip or focus:** no `.skip`, `.only`, `.todo`, `xit`, `t.Skip`, `@pytest.mark.skip` or their equivalents in committed tests without an inline link to a tracked issue. Ban them in the linter **and** in a `check.sh` grep step, not in a review checklist.
- **Never silence:** no blanket ignore pragmas (`@ts-ignore`, `eslint-disable`, `# noqa`, `//nolint`, muted output) without an inline justification linked to an issue.
- **Never ignore:** a failing test blocks the merge. There is no "known-failing" allowance -- intentionally divergent behavior is fixed or locked in by a *passing* test that documents it.
- **Warnings are errors.** Lint and compiler warnings fail the gate.

`mise run check` must be green on a clean tree (`git status --porcelain` empty) before any phase merges. A dirty tree fails the gate, because a green result on a dirty tree describes disk, not HEAD.

## The pyramid

Bias toward the cheapest layer that can catch the bug: unit and functional tests at the base, integration above, end-to-end at the tip. A bug found by an e2e test that a unit test could have caught is a missing unit test.

## Path taxonomy

Tag test names so triage can grep them. A feature is not done until every applicable tag is covered. In Go the tag is the first element of the subtest name (`t.Run("sad/unknown key", ...)`), so `go test -run '/sad/'` selects a class.

| Tag | Covers |
|---|---|
| `happy` | the intended path with valid input |
| `sad` | invalid input, expected failures, error responses |
| `raging` | hostile or malformed input, abuse, limits exceeded |
| `edge` | boundaries: empty, one, maximum, unicode, time zones |
| `corner` | two or more edges at once |

## Coverage

Set a floor and enforce it in the gate (`scripts/check.sh`), not in promises. Excluding files from the measurement is a design decision: flag it in the same commit and keep the excluded set tiny (an entrypoint with one statement, not "everything hard to test"). For yasshm the floor is 85% of statements across all packages, and the only exclusion is the test support code in `internal/testkit/` (spec section 7).

## Manual tests

Live checks the suite must never run are manual tests in `test/manual/`, one `MT-<nnn>-<name>.md` beside its `.results` log, in the agent-ops `manual-test` format: one tool call per step, a `jq -e` or exact-shape expected result, a declared read budget, placeholders resolved from the gitignored `mise.local.toml`. They run at release (release-prep Step 4a) and a FAIL refuses the tag. Each names what blocks its automation; the count of active files is tracked debt. Active: none yet. Planned for Phase 7: MT-001 to MT-005 (spec section 7), each blocked on the same thing -- they need the real edge and a real target, which the build loop never has.
