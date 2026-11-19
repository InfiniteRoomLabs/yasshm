# Phase 0 code-review lane

Range `34d6001..93674e8` (5 commits) on `claude/serene-franklin-jv4pu7`. Read-only; no gate, tests or linters run.

## Verdict: REQUEST CHANGES

All GOAL deliverables are present: module and `version` command, the eight gate steps in order, `.golangci.yml`, the `build`/`test`/`lint`/`fuzz` tasks, dirty-tree failure, the CI sshd step, the docs, and the changelog. The CI workflow is fine: actions are SHA-pinned, it has `contents: read`, and the sshd step installs `sshd` when missing and then fails if it is still absent. The golangci v2 config is valid, and `nolintlint` requires a named linter and a reason. The docs are ASCII-only, and the placeholder grep prints nothing. One arithmetic bug in `cover` will break the gate in Phase 1.

## Findings

1. **BLOCKING -- `cover` counts each duplicate block as a separate block, so it does not merge them.** `scripts/check.sh:62-67`. With `-coverpkg=./...` (`mise.toml` task `test`), every test binary instruments every package, and `go test` appends each binary's profile to `coverage.out` without merging, so the same block appears once per binary. The awk adds `$2` for every line, so a block that one binary covers and the others do not counts as partly uncovered. The reported 85.7 percent shows this already: `Run` has 12 statements, all covered, and `main` has 1, which is 12/13 = 92.3 percent. The reported figure is 12/14, so the `main` block was counted twice. Once Phase 1 adds several tested packages, the measured total will fall toward the real total divided by the number of test binaries, and the gate will fail even though nothing is wrong. Fix: merge by block key before summing, for example `{ k=$1; n[k]=$2; if ($3>0) c[k]=1 } END { for (k in n) { s+=n[k]; if (k in c) v+=n[k] } ... }`. Alternatively, run `grep -v '/internal/testkit/'` on the profile and read the total from `go tool cover -func`, which merges duplicate blocks. Add a check.sh comment explaining why.

2. **ADVISORY -- the boundary is rounded before it is compared.** `scripts/check.sh:66,73`. `printf "%.1f"` comes before the `>=` test, so a true 84.95 prints `85.0` and passes. Fix: compare the unrounded value and round only for display.

3. **ADVISORY -- `cover` run alone accepts a stale profile.** `scripts/check.sh:57`. `mise run check -- cover` reads whatever `coverage.out` is on disk, which may come from another HEAD. Fix: fail when `coverage.out` is older than HEAD's commit time, or say in `TESTING.md` that only `test` followed by `cover` counts.

4. **ADVISORY -- the build is not reproducible.** `scripts/build.sh:13`. `Date` is the wall clock, so `SHA256SUMS` changes on every build of the same commit. Fix: `date="${SOURCE_DATE_EPOCH:+...}"`, or default to the commit time with `git log -1 --format=%cd --date=format:%Y-%m-%dT%H:%M:%SZ` and `TZ=UTC`.

5. **ADVISORY -- `fuzz.sh` scans untracked trees.** `scripts/fuzz.sh:17`. `grep -r .` walks the gitignored `.worktrees/` and `node_modules/`. A worktree's fuzz target resolves to a package in another module, so `go test` fails. `2>/dev/null` together with process substitution also hides grep errors, which turns them into a vacuous "no Fuzz targets". Fix: get the files from `git ls-files '*_test.go'` and pass them to `grep -Eo`, and treat a grep exit status above 1 as an error.

6. **ADVISORY -- `skipcheck` gaps.** `scripts/check.sh:38-40`. Three cases are not caught: a method value (`skip := t.Skip; skip()`), a selector split across lines (`t.` then a newline, then `Skip(`), and a helper that calls Skip from a non-`_test.go` file outside `internal/testkit/`. Fix: match `\.Skip(f|Now)?\b` without the paren, and say in `TESTING.md` that test helpers live only in `_test.go` files or `internal/testkit/`. No false positive is likely from the current pattern.

7. **ADVISORY -- the task description is stale.** `mise.toml:25`. The `check` description still says "then a dirty-tree banner", but the gate now fails on a dirty tree. Fix: "...then fails on a dirty tree".

8. **ADVISORY -- CLI edge cases.** `internal/cli/cli.go:42-46,65`. `help`, `-h` and `--help` return exit 2 with "unknown command". A failed stdout write returns 1 and prints no diagnostic. In `cli_test.go`, `happy/version` does not assert that stderr is empty. Fix: treat `help`, `-h` and `--help` as usage on stdout with exit 0, write `yasshm: <err>` to stderr on a write failure, and assert that stderr is empty in the happy case.
