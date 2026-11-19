# Phase 0 QA lane

Head `d492a66` on `claude/serene-franklin-jv4pu7`, base `34d6001`. All tooling was run through `mise`. Every throwaway change was removed, and `git status --porcelain` was empty before and after each run, apart from this report.

## Verdict: NEEDS WORK

Every deliverable exists, and the gate and CI are green. But the code-review lane's BLOCKING finding about `cover` is real. I reproduced it independently, and it makes the gate fail on correct code as soon as a second package has tests.

## Evidence

- `mise run check` on a clean tree exits 0. Its tail: `cover: total 85.7% (floor 85%, internal/testkit excluded)`, `No vulnerabilities found.`, `check.sh: sshd is not installed here; the real-sshd lane runs in CI only (spec section 7)`, `check.sh: all OK`.
- CI on `d492a66`: run 44609610663 completed with success. Steps `Ensure sshd is installed` and `Run mise run check` both succeeded. The job log (readable over REST) shows `/usr/sbin/sshd` was already present, plus `cover: total 85.7%` and `check.sh: all OK`.
- Each of the eight steps run alone (`mise run check -- <step>`) exits 0. An unknown step exits 2. `mise exec -- usage lint scripts/check.sh` prints `No issues found.`
- `mise run build` writes `dist/yasshm_d492a66_linux_{amd64,arm64}` and `SHA256SUMS`, and `sha256sum -c` says OK for both. The binaries are static ELF, and `go version -m` shows `CGO_ENABLED=0 -trimpath=true`. `version` prints `yasshm d492a66 / commit d492a66b... / built 2026-11-19T20:32:46Z` and exits 0.
- `mise run fuzz` passes with "no Fuzz targets". `mise run test` and `mise run lint` (0 issues) both pass.
- Bite proofs I reproduced:
  - A temporary `t.Skip("temporary")` test fails `skipcheck` (exit 1).
  - `COVER_FLOOR=86.7` gives `85.7% is below the floor of 86.7%` (exit 1).
  - An untracked file fails `all` and a single step with `!! FAIL: the tree is DIRTY` (exit 1).
  - A missing or empty `coverage.out` fails `cover` (exit 1).
- `fmt` fidelity: for one misformatted temporary file, `gofmt -l` and the `fmt` step list the same file, and the step exits 1.
- `lint` fidelity: a bare `//nolint` gives `nolintlint: should mention specific linter`. `//nolint:gosec` with no reason gives `whyNoLint: include an explanation` (gocritic). Both exit 1.
- Docs:
  - The placeholder grep prints nothing.
  - The ASCII grep over tracked `.md/.sh/.yml/.toml` files prints nothing.
  - The `bootstrap`, `arch:validate|fmt|gen` and `gh api` commands work as written, and `arch:gen` leaves no diff.
- Limits (D16): nothing was deployed, no real host was reached, and no credential was used. The `sshd` CI-only decision is written into a spec callout (section 7) and `docs/progress.md`, and the CI step is green.

## Findings

1. **BLOCKING -- `cover` sums duplicate profile blocks.** `scripts/check.sh:62-67`, confirmed.
   - Hand count: `Run` has 12 statements, all covered (`cli.go:37-53`), and `main` has 1. The true total is 12/13 = 92.3%.
   - Raw `coverage.out` lists `main.go:12.2,13.1 1 0` twice: once from the `cmd/yasshm` binary and once from the `-coverpkg=./...` cli binary. The awk therefore computes 12/14 = 85.7%.
   - `go tool cover -func` reports 92.3%, and a merged-by-key awk gives 12/13.
   - Throwaway demo: I added `internal/zzdemo` with one fully tested function. The gate printed `cover: total 45.2%` and failed. The true value is 93.3% (14/15 from both `go tool cover -func` and the merged awk). Phase 1 would fail on correct code.
   - Fix: merge by block key, or filter `internal/testkit` and read `go tool cover -func`.
2. **ADVISORY -- `skipcheck` evasions pass.** `scripts/check.sh:38-40`.
   - `skip := t.Skip; skip("x")` in a `_test.go` file: the step prints no offender, and `go test -v` shows `--- SKIP`.
   - `t.Skip` in a non-test `.go` file outside `internal/testkit/` (I used `internal/zzhelp/h.go`) is also not caught.
   - The same call in `internal/testkit/skiphelper.go` is caught.
   - In both evading runs the run failed only because of the dirty-tree check. Once committed, those files would pass.
3. **ADVISORY -- rounding happens before the comparison.** `scripts/check.sh:66,73`. `%.1f` is applied before the `>=` test, so a true 84.95% passes as 85.0%.
4. **ADVISORY -- stale task description.** `mise.toml:25` still says "then a dirty-tree banner"; the gate now exits 1 on a dirty tree.
5. **ADVISORY -- two commands do not work as written.** `CLAUDE.md:93-94` lists `scripts/redaction-check.sh` and `usage lint scripts/check.sh` as bare commands. Both fail with `usage: No such file or directory/command not found` when mise is not activated in the shell. They pass as `mise exec -- ...`. The redaction check is unconfigured here (it does nothing), so that commit hygiene step is unverified.
6. **ADVISORY -- `cli_test.go` gaps.** `internal/cli/cli_test.go:34-35,24`.
   - Present: happy, sad and edge cases. Missing: raging and corner cases.
   - `sad/stdout write fails` asserts only the exit code. `happy/version` does not assert that stderr is empty.
   - Mutations I checked by reasoning would be caught: dropping the write-error check, wrong exit codes, and usage text written to stdout.
   - Would not be caught: version text also written to stderr, and a missing diagnostic on a write failure.
7. **ADVISORY -- the build is not reproducible.** `scripts/build.sh:13` uses the wall clock as `Date`, so `SHA256SUMS` differs between builds of the same commit.

## Required before PASS

Fix finding 1. Re-run the gate on a clean tree and confirm `cover` prints the merged total (92.3% at this head), and see CI green on the fix commit.
