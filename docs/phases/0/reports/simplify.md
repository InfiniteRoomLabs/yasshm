# Phase 0 -- simplification lane

Range `34d6001..93674e8`. Propose-only; nothing run but `git` and reads. Little to cut: the diff is small and mostly purposeful.

## Proposals

1. **APPLY-RECOMMENDED** -- `scripts/check.sh:62`: `awk -v floor="$COVER_FLOOR" '...'` never reads `floor`. Drop the `-v floor=...`. Dead plumbing; output identical. Risk: none.
2. **APPLY-RECOMMENDED** -- `scripts/check.sh:18` and `:37-40`: `{ git ls-files X; git ls-files --others --exclude-standard X; } | sort -u` -> `git ls-files --cached --others --exclude-standard X`. One call returns the same set (cached and untracked are disjoint, so no dedupe needed). Messages unchanged; only the listing order of offenders could differ. Risk: low.
3. **APPLY-RECOMMENDED** -- `scripts/check.sh:126-127`: `steps` is now a fixed non-empty array, so the bash-3.2 empty-array guard and its comment are dead. `for s in "${steps[@]}"; do run_step "$s"; done`. Same order, same exits. Risk: none.
4. **APPLY-RECOMMENDED** (stale text, not code) -- `mise.toml:11` "Phase 0 wires these into scripts/check.sh." is now done: cut the sentence. `mise.toml:25` the `check` description says "then a dirty-tree banner"; it now fails, so make it "then fails on a dirty tree". No runtime effect; the old text is wrong.
5. **OPTIONAL** -- `scripts/check.sh`: add `cd "$repo_root"` once after line 6 (as `build.sh`/`fuzz.sh` already do) and drop the seven `(cd "$repo_root" && ...)` subshells. Paths in messages are already absolute. Risk: low.
6. **OPTIONAL** -- `scripts/fuzz.sh:11`: `[ -n "$file" ] || continue` cannot fire (the sed output never has empty lines; no matches means no iterations). Remove it. Risk: none.
7. **OPTIONAL** -- `scripts/bootstrap.sh:28`: `&& [ -n "$sshd_path" ]` is redundant (both branches print a path on success). It also uses `ls /usr/sbin/sshd` where `check.sh:132` uses `-x`; aligning on `-x` is a tiny consistency gain. Message text unchanged.
8. **OPTIONAL** -- `.golangci.yml`: `run.tests: true` (line 5), `nolintlint.allow-unused: false` (27) and `exclusions.generated: lax` (48) are v2 defaults. Kept they document intent; cut they shorten the file. Lint results are the same either way.
9. **OPTIONAL** -- `internal/cli/cli_test.go`: `wantNoOut` can be replaced by `len(tt.wantOut) == 0` (true for every case that sets it; the fail-stdout case leaves `out` empty anyway). Pick the writer once (`var stdout io.Writer = &out; if tt.failStdout { stdout = failWriter{} }`) and use `exitOK`/`exitUsage`/`exitError` instead of the literals. Same coverage, one field fewer.
10. **OPTIONAL** -- `README.md` Contributing line: repeats the full step list that `TESTING.md` owns, which `CLAUDE.md` *Commands* also repeats. Point to `TESTING.md` instead (`mise run check` -- see `TESTING.md`). That leaves two copies, not three.
11. **DO-NOT-APPLY** -- merging the two awk calls in `run_cover` into one. That saves a line but mixes the pass/fail decision with the computation. Readability goes down.

## Deliberately left alone

- `.golangci.yml` `run.timeout`, `max-issues-per-linter: 0`, `max-same-issues: 0`, the `govet` fieldalignment disable, and the revive rule list. None of these are defaults (v2 defaults: no timeout, 50, 3), so removing any of them would change behaviour.
- The `mise.toml` `test`/`lint`/`build` tasks against `check.sh`. `check.sh` already delegates to them, so the logic exists once.
- The `exitOK`/`exitError`/`exitUsage` constants and the exported-var comments in `cli.go`. revive's `exported` rule needs the comments.
- `internal/testkit/doc.go`, the empty-fuzz pass, and the `sshd` notice lines. These are required by design.
- The `CLAUDE.md` gotchas. Each one carries a reason, not a restatement.
- The CI `sshd` step. Its re-check after install is the fail-if-missing contract.
- The template-era `run_arch` comment. It is outside this phase's diff.
