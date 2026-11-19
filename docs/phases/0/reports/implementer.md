# Phase 0 implementer report

## Files
Created: `go.mod`, `cmd/yasshm/main.go`, `internal/cli/{cli.go,cli_test.go}`, `internal/testkit/doc.go`, `.golangci.yml`, `scripts/build.sh`, `scripts/fuzz.sh`. Changed: `scripts/check.sh`, `scripts/bootstrap.sh`, `mise.toml`, `.github/workflows/ci.yml`, `README.md`, `TESTING.md`, `CLAUDE.md`, `CHANGELOG.md`.

## Results
- Tests: 1 test function, 6 subtests (happy/, sad/, edge/), all pass with `-race -shuffle=on`. `-race` works here (gcc present); the `test` task sets CGO_ENABLED=1 for the test binary only.
- Coverage total 85.7 percent (floor 85; `main` is the one uncovered statement). The margin is thin; Phase 1 code will raise it.
- Gate tail: `check.sh: sshd is not installed here; the real-sshd lane runs in CI only (spec section 7)` then `check.sh: all OK` on a clean tree.
- amd64 binary: `yasshm 036f624` / `commit 036f6243...` / `built 2026-11-19T20:27:00Z`; `dist/SHA256SUMS` lists both files.
- CI check-run on head `036f624`: `check completed success`.

## Bite proofs (not committed)
(a) skipcheck:
```
== skipcheck ==
skipcheck: skipped tests without an issue link on the same line:
internal/cli/tmp_test.go:3:func TestTmp(t *testing.T){ t.Skip("x") }
[check] ERROR task failed
```
(b) cover with floor 86:
```
cover: total 85.7% (floor 86%, internal/testkit excluded)
cover: 85.7% is below the floor of 86%
[check] ERROR task failed
```
(c) dirty tree, single step `fmt` with an untracked file:
```
!! FAIL: the tree is DIRTY -- a result on a dirty tree describes disk, not HEAD:
?? stray.txt
[check] ERROR task failed
```
(d) cover alone with no profile: `cover: no coverage profile at .../coverage.out -- run the test step first (mise run check -- test)` and exit 1.

## Tools and dependencies
- No `go.mod` dependencies.
- `govulncheck` v1.10.0 via the mise `go:` backend (spec section 7 `vuln` step). It reaches the vuln database from the sandbox.

## Spec notes
- No spec callout was needed; the sshd callout already existed.
- The skipcheck patterns are `.Skip(`, `.Skipf(`, `.SkipNow(` and `testing.Short()`; Go has no focus construct, so "focused" has no pattern.
- `COVER_FLOOR` sits after the arg parsing near the top of `scripts/check.sh`, not on the first line.
- The `test`, `lint` and `build` gate steps call `mise run <task>`, so the tasks are the single source of the commands.
- `mise run test` and `mise run lint` do not fail on a dirty tree; only `mise run check` does.
