# yasshm -- Project Instructions for Claude

yasshm ("Yet Another SSH MCP") is a self-hosted MCP gateway that lets an AI assistant run policy-checked, audited commands on SSH hosts its operator has configured.

Cut from `InfiniteRoomLabs/template-repo`. This file is the living rulebook: when you find a gotcha, write it into **Gotchas** below in the same commit -- just enough that future you knows it is not a bug.

## How this repo runs

- It runs on the `/goal` treadmill (agent-ops `agency` plugin), adapted to **one cloud session that runs the whole roadmap**. The session is started once with the kickoff prompt in `GOAL.md`. Its lead is an orchestrator on the fable model: it dispatches, verifies and ships, and subagents build and review. When a phase ships, the lead retargets `GOAL.md` and goes straight on to the next phase.
- The planning is done: the spec, `GOAL.md` and the work orders were written on 2026-11-18 and revised on 2026-11-19. Do not run `/new-goal-loop` again.
- The build session holds **two repositories**: this one (public) and the operator's infra repository (private). The session is started with both clones on the plan branch `feat/yasshm-mvp` and works on its own working branch in each. `GOAL.md` *Session start* is the checklist; do it at kickoff and again before every phase.

## Read-first

- **`docs/progress.md`** -- living status. Always read before starting work; update at every phase boundary.
- **`GOAL.md`** -- the current autonomous phase goal + full roadmap. Read its *Session start* section first.
- **The spec** (`docs/superpowers/specs/2026-11-18-yasshm-design.md`) -- section 2 is locked (do not re-litigate); sections 4 to 8 are the build oracle; read only the sections the current phase needs.
- **`docs/architecture/`** -- the LikeC4 model. Read it before designing anything structural.

## Hard limits (they override everything else here)

The build loop **never** deploys, applies infrastructure, reaches a real target host or the real edge, logs in to a password manager, reads or asks for a secret, runs a manual test, pushes a tag, force-pushes, deletes a branch, or pushes any branch except its working branch (so never `main` here, never the infra repo's default branch, never the plan branch `feat/yasshm-mvp`). No done criterion depends on any of those. When a step needs one, write it under `## Blocked on user` in `GOAL.md`, commit, push, say so once and stop.

Everything that can be tested without those things must be tested, automatically, in the gate (spec D15).

## Two repositories, one working branch in each

- **The plan branch is `feat/yasshm-mvp`** in both repos. It holds the plan and nothing else; the session starts from it (or, if a clone started elsewhere, merges it in at kickoff) and never pushes to it.
- **The working branch** of a clone is the branch the session was told to develop on, or the `claude/...` branch the clone was already on; if there is neither, the session creates `claude/yasshm-mvp`. All work lands there and it is the only branch the session pushes. There are no phase branches and no merges: a phase is the commit range from `docs(phase-<n>): start <name>` to `docs(phase-<n>): ship <name>`. `GOAL.md` *How a phase lands* has the steps.
- **Push the working branch after every green commit.** The session's machine can be reclaimed while it is idle; a reopened session starts from a fresh clone.
- Work in the infra repo only in Phase 8, on the working branch there, under that repo's own `CLAUDE.md` and `CONTRIBUTING.md`. Read them first; they differ from this file (secret handling, changelog, layout).
- **This repo is public, the infra repo is private.** Nothing from the infra repo -- host names, addresses, account or tenant ids, vault item names, the text of its plan documents -- goes into a file, report, commit message or branch name here. Refer to "the infra repo's plan document".

## Toolchain

- **Everything through `mise`:** `scripts/bootstrap.sh` (installs `mise` if missing, then the pinned tools), then `mise run check` is the gate. Pin real versions in `mise.toml [tools]`; never bare tool invocations (version drift). Go 1.27.4 and golangci-lint 2.15.0 are pinned there; Node and pnpm are there only for LikeC4.
- **Go:** `mise run <task>` or `mise exec -- go ...`, never a bare `go`. `CGO_ENABLED=0` always (spec D2).
- **Python:** `uv run` only. **JS:** `pnpm` only (`pnpm dlx`, never `npx`).
- **Secrets:** this build has none and needs none; there is no `fnox.toml`. If one ever exists: a **gitignored** `fnox.toml` maps env vars to vault items; run anything that needs them as `fnox exec -- <cmd>`. No `.env`/`.envrc`, never export a secret in a shell, never echo a value.

## Working conventions

- **Phases, not tasks.** One GOAL block = one phase = one commit range on the working branch (start commit to ship commit) = one gate. This replaces the template's one-branch-and-one-merge-per-phase, because a cloud session can count on pushing only one branch per repository.
- **Four-lane review gate before every ship commit:** code review, simplification, security (parallel, read-only), then QA (the only lane that runs `mise run check`). Work orders in `docs/phases/_templates/`; reports to `docs/phases/<n>/reports/<lane>.md` and via `SendMessage` to `team-lead`. Commit the three read-only reports before dispatching QA. Triage, ONE fix commit, re-gate, ship commit.
- **Model rules (non-negotiable):** pass `model:` explicitly on every dispatch; reviewers one tier above the implementer (sonnet -> opus, opus -> fable); haiku only for read-only sweeps that cannot hit a permission prompt. Check an agent's `tools:` before writing its prompt; `general-purpose` has everything.
- **Clean up agents** with `TaskStop` once they are no longer needed: review lanes after triage, implementers after their phase ships, QA after its verdict is acted on.
- **Green rule:** see `TESTING.md`. Tests first where feasible; no skipped/focused/silenced tests without an issue link; warnings are errors; `go test -race -shuffle=on -count=1` always; coverage floor **85%** enforced by the gate, not by promises.
- **Inferred vs confirmed:** facts about the outside world are CONFIRMED / INFERRED / TODO (spec section 3). When reality disagrees with the spec, reality wins -- add a `> **STATE AS OF YYYY-MM-DD**` callout in the affected spec section in the same commit.
- **Commits:** conventional, imperative, scoped (`feat(<scope>): ...`, `fix: ...`, `docs: ...`, `chore(ci): ...`). **Stage and commit in separate tool calls** (version guard); never `-a`/`-am`, never `--no-verify`. `CHANGELOG.md` is staged with every phase's ship commit (the changelog guard only enforces this on `main`; the rule is the same). Keep a Changelog, `[Unreleased]` on top. Co-author trailer per harness rules.
- **Public-repo hygiene on every commit:** no vault item names, internal IPs/domains, real tenant/account IDs, tokens, or personal correspondents. Fixtures synthetic (`example.net`, documentation address ranges, keys generated in the test). Run `scripts/redaction-check.sh` before committing. A leak found in history is fixed by `git filter-repo` and a force-push to every remote, never by a working-tree edit, and only with the operator present. Never write a secret-shaped value into a goal, report, or commit message -- say "the real <thing>".
- **Docs are ASCII-only and never hard-wrapped.** No smart quotes, em dashes, or arrows: use `--` and `->`. Diagrams are Mermaid.
- **Remotes:** this repo may dual-push (GitHub + an internal mirror). `git remote -v` before pushing a WIP branch; internal hostnames never appear in tracked files.

## Architecture (LikeC4)

The architecture is modelled as code in `docs/architecture/*.c4` and is part of the deliverable, not a side artifact. The LikeC4 **workspace** is the repo root; the **project** is scoped by `docs/architecture/likec4.config.json`.

- Read the model before designing. Where the `likec4` MCP server from `.mcp.json` is loaded, use `read-project-summary`, `search-element`, `read-element`, `find-relationships` instead of grepping `.c4` files. In a cloud session with two repositories `.mcp.json` is not loaded: read the `.c4` files directly and use the `mise run arch:*` tasks.
- The whole planned system is already in the model. An element tagged **`#planned`** is designed (spec section 4) but not built. The phase that builds it removes the tag **in the same commit** and regenerates the diagrams. A phase that needs an element the model lacks adds the element and the spec row together.
- A structural change to the code -- a new component, a new datastore, a relationship that did not exist -- updates `docs/architecture/*.c4` **in the same commit**.
- `mise run check` runs `likec4 validate`, `likec4 format --check`, and a staleness check on the committed Mermaid in `docs/architecture/generated/` (and the landscape view spliced into `docs/architecture/README.md`). Regenerate with `mise run arch:gen` and commit the result.
- The gate proves the model is well-formed and the diagrams are current. It does **not** prove the model is true -- that is the code-review lane's job.

## Key locations

| Concern | Path |
|---|---|
| Gate, scripts, CI | `mise.toml`, `scripts/` (`check.sh`, `bootstrap.sh`, `arch-gen.sh`, `redaction-check.sh`), `.github/workflows/ci.yml` |
| Process | `GOAL.md`, `docs/progress.md`, `docs/phases/_templates/`, `docs/phases/<n>/reports/` |
| Design spec | `docs/superpowers/specs/2026-11-18-yasshm-design.md` |
| Architecture model / project config / generated diagrams | `docs/architecture/*.c4`, `docs/architecture/likec4.config.json`, `docs/architecture/generated/` |
| Testing rules | `TESTING.md` |
| Build, fuzz, lint config | `scripts/build.sh`, `scripts/fuzz.sh`, `.golangci.yml` |
| Code | `cmd/yasshm/`, `internal/<package>/`, test support in `internal/testkit/`, end-to-end tests in `test/e2e/` |
| Operator manual tests (written in Phase 7, never run by the loop) | `test/manual/` |
| Harness bootstrap (marketplaces, plugins, MCP allowlist) | `.claude/settings.json`, `.mcp.json` |
| Codex brief | `AGENTS.md` |

## Commands you'll want

```bash
scripts/bootstrap.sh                  # mise + pinned toolchain on a bare machine; no-op when current
mise run check                        # the gate: fmt lint skipcheck test sshd cover vuln build arch, then fails on a dirty tree; `mise run check -- <step>` for one
mise run build                        # dist/yasshm_<version>_linux_{amd64,arm64} + dist/SHA256SUMS
mise run test                         # go test -race with coverage.out (the gate's test step uses it)
mise run lint                         # golangci-lint
mise run fuzz                         # every Fuzz target for 30 seconds each; passes when there are none
mise exec -- go version               # any Go command goes through mise
mise run arch:dev                     # live diagram browser at http://localhost:5173
mise run arch:validate                # LikeC4 syntax + layout-drift check
mise run arch:fmt                     # format .c4 sources in place
mise run arch:gen                     # regenerate committed Mermaid diagrams + README splice
mise exec -- scripts/redaction-check.sh   # staged index; --range <phase base sha>..HEAD for a phase
mise exec -- usage lint scripts/check.sh   # validate a #USAGE spec
gh api repos/InfiniteRoomLabs/yasshm/commits/<sha>/check-runs   # CI result without GraphQL
```

## Gotchas

- `#USAGE` directives take **no space** after `#`. `usage` 6.0.0 silently ignores `# USAGE`, and the script then runs with every `usage_*` variable unset.
- The changelog guard and version guard are Claude hooks from agent-ops, not git hooks. If a commit is blocked, read the message and fix the staging -- do not bypass.
- `.claude/` is committed through a `.gitignore` allowlist (`settings.json` and `.gitignore` only). `git add .claude/anything-else` is silently ignored.
- `mise run check` fails (exit 1) on a dirty tree, after `all` and after a single step: commit or stash before gating, so green describes HEAD. The gate itself never dirties the tree (`coverage.out` and `dist/` are gitignored).
- `-race` needs CGO, so the `test` task sets `CGO_ENABLED=1` for the test binary only; `build` is always `CGO_ENABLED=0`.
- `cover` reads `coverage.out` and fails when it is missing: run `mise run check -- test` first. The floor is `COVER_FLOOR` at the top of `scripts/check.sh`.
- The sandbox has no `sshd`: `scripts/bootstrap.sh` and the gate's `sshd` step print one line saying the real-sshd lane runs in CI only; CI installs `openssh-server`, sets `YASSHM_REQUIRE_SSHD=1` and fails if it is absent. Develop the real-sshd half blind and read the CI job log over REST (`gh api repos/InfiniteRoomLabs/yasshm/actions/jobs/<id>/logs`); the `sshd` step prints `sshd -e` output of a failing test and the `F10 observed` lines.
- A real-sshd test is named `TestRealSSHD*` and starts with `if !realsshd.Present(t) { return }` -- never `t.Skip` (`skipcheck` forbids it). The `sshd` step selects tests by that name and counts their PASS lines.
- `govet` runs `shadow` and `gocritic` runs `sloppyReassign`: an inner `if err := ...` that shadows an outer `err` fails lint, and so does `if err = ...`; name the inner one (`werr`, `cerr`).
- A shell's `$!` is printed before the background child has run `setsid`; a test that needs the child out of the group polls its pgid in `/proc/<pid>/stat` first.
- SFTP version 3 has no "file exists" status: OpenSSH answers a failed `O_EXCL` create with a generic failure, so `sshclient.Upload` checks with `lstat` before reporting `fs.ErrExist`. `pkg/sftp`'s server renames over an existing file on a plain rename; only `O_EXCL` gives no-clobber on both servers.
- The CI runner's `sshd` (OpenSSH 10.2p1 on 2026-11-19) runs as the runner's user with `UsePAM no`, `StrictModes no`, `PidFile none` and an absolute-path config in a temp dir, and must be started by its absolute path (it re-executes itself).
- In a cloud session the redaction check has no term list and passes as a no-op, so hygiene is held by rule and by the security lane.
- `cover` merges duplicate profile blocks by key because `-coverpkg=./...` writes one copy per test binary.
- govulncheck is pinned in `mise.toml [tools]` through the `go:` backend (`"go:golang.org/x/vuln/cmd/govulncheck"`); bump it to a released version, never `@latest`.
- LikeC4 is pinned in `mise.toml [env] LIKEC4_VERSION`, not `[tools]` (it is not in the mise registry; every call is `pnpm dlx`). Bump only to a release at least 7 days old -- the pnpm release-age gate refuses younger ones and the failure reads as a resolution error, not a LikeC4 error.
- `.mcp.json` uses `${CLAUDE_PROJECT_DIR}`, not `${workspaceFolder}`: Claude Code does not expand the VS Code variable and passes it through literally.
- The first `mise run check` on a clean machine downloads ~120 MB of LikeC4; it is cached after that. There is no SVG export -- diagrams are committed as Mermaid so GitHub renders them; PNG needs Playwright and is not in the gate.
- The VS Code LikeC4 extension registers its own `likec4` MCP server, so you may see two. Both work; pick one.
- **Cloud session with two repositories:** it starts in the directory above the clones and loads no hooks, permission rules, `env` or MCP servers from either repo's `.claude/settings.json`; plugins come from the environment's setup script (spec F14). So nothing bootstraps itself: run `scripts/bootstrap.sh`, and use absolute paths or `git -C <dir>`.
- **An idle cloud session loses its disk eventually.** The machine is paused, then reclaimed; reopening the session gives a fresh clone. Anything not pushed is gone (spec F20).
- `mise` is installed to `$HOME/.local/bin` and the installer does not edit shell profiles. If `mise` is "not found", call `$HOME/.local/bin/mise`.
- **GitHub from a cloud session:** tag pushes and branch deletions are rejected, and so is GraphQL, so `gh pr` and `gh issue` fail. Use REST (`gh api repos/...`). Whether a push to a branch outside `claude/` is accepted is unsettled (spec F13); do not find out by trying, push only the working branch.
- The sandbox runs tests as root (a CI runner does not), so a 0400 file is still writable and readable there. Write tests that hold for both: make a write fail with `/dev/full` (ENOSPC) and an open fail with a path under a regular file or a directory in place of the file; check key permissions on mode bits.
- `go.yaml.in/yaml/v3` skips its duplicate-key check when decoding into a `yaml.Node`, and reports lines, not key paths. `internal/config` walks the node tree itself for both; do not switch it to `Decode` into structs.
- A new built-in denylist entry in `internal/policy` needs must-match and must-not-match cases in `commandCases` or `pathCases`; the table test fails without them.
- `TestParseLimits` holds its own literal copy of the limits table and the four cross-field pairs: changing a default, a bound or a pair means changing `limits.go`, the test and the spec 5.1 callout together.
- The command rails are regular expressions that skip quoted text, not a shell parser; an accepted bypass goes into the list in the `policy` package doc and the spec 5.3 callout, not into a cleverer regex.
- The template-placeholder check must keep printing nothing: `grep -rEn '<(project|one-line-description|toolchain|gate-steps|repo|slug)>' --exclude-dir=.git .`. Write per-phase slots as `<n>` and `<name>`, never with one of those six words.

## If you break something

- Gate red: read the first failing step's output; `mise run check -- <step>` isolates it.
- Hook blocked a commit: it is doing its job; check what you staged -- do not bypass.
- Fresh or compacted session disoriented: `docs/progress.md` has the ledger and the how-to-resume checklist; `GOAL.md` *Session start* has the first six steps.
- CI green locally, red on GitHub: check that the `mise.toml` pins match and nothing depends on a local `mise.local.toml`.
- Something seems to need a secret, a real host or a deploy: it does not belong to the loop. Park it under `## Blocked on user`.
