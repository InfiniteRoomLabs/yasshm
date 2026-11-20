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
| Code | `cmd/yasshm/`, `internal/<package>/` (the MCP endpoint is `internal/mcpserver`, `serve` is `internal/cli/serve.go`), test support in `internal/testkit/` (`edgetest` is the stand-in edge), end-to-end tests in `test/e2e/` |
| Operator manual tests (written in Phase 7, never run by the loop) | `test/manual/` |
| Install material and the operator's docs | `deploy/` (unit, example config, install steps), `docs/operator-guide.md`, `SECURITY.md`, `.github/workflows/release.yml` |
| Harness bootstrap (marketplaces, plugins, MCP allowlist) | `.claude/settings.json`, `.mcp.json` |
| Codex brief | `AGENTS.md` |

## Commands you'll want

```bash
scripts/bootstrap.sh                  # mise + pinned toolchain on a bare machine; no-op when current
mise run check                        # the gate: fmt lint skipcheck test sshd e2e deploy docs cover vuln build arch, then fails on a dirty tree; `mise run check -- <step>` for one
mise run build                        # dist/yasshm_<version>_linux_{amd64,arm64} + dist/SHA256SUMS
mise run test                         # go test -race with coverage.out (the gate's test step uses it)
mise run lint                         # golangci-lint
mise run fuzz                         # every Fuzz target for 30 seconds each; passes when there are none
mise exec -- go version               # any Go command goes through mise
yasshm serve --config <file>          # the gateway; JSON logs on stderr, SIGTERM for a graceful stop
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
- A real-sshd test is named `TestRealSSHD*` and starts with `if !realsshd.Present(t) { return }` -- never `t.Skip` (`skipcheck` forbids it). The `sshd` step runs those tests with `TestContractSSHTest` and fails unless the real-`sshd` contract PASS count equals the in-process one: a new scenario must pass on both servers.
- x/crypto waits for a want-reply answer (`signal`, `exec`, a channel open) with no timeout. Every such wait in `sshclient` is armed with a timer or context that closes the connection; a new one needs the same, and a test against an `sshtest` `Silent*` switch.
- A background child that keeps stdout open keeps an SSH session open on real `sshd` (the exit-status arrives, the channel does not close) and, since Phase 2's review gate, on `sshtest` too: do not "fix" `sshtest` back to cutting the channel when the shell exits.
- `govet` runs `shadow` and `gocritic` runs `sloppyReassign`: an inner `if err := ...` that shadows an outer `err` fails lint, and so does `if err = ...`; name the inner one (`werr`, `cerr`).
- A non-interactive bash (the CI runner's login shell) ignores `SIGQUIT` and drops a `QUIT` that arrives before its `exec`: a test that signals a command waits until the command printed a marker from a process that is not the login shell (`readyCommand` in `internal/sshclient/contract_test.go`), and every contract `Wait` is bounded to 10 seconds.
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
- A new built-in denylist entry in `internal/policy` needs must-match and must-not-match cases in `commandCases` or `pathCases`; the table test fails without them. A new command rule also needs `Needs` (literals every match contains): the rule is skipped when none is in the command, and `TestCommandRuleNeeds` fails without them.
- `TestParseLimits` holds its own literal copy of the limits table and the four cross-field pairs: changing a default, a bound or a pair means changing `limits.go`, the test and the spec 5.1 callout together.
- The command rails are regular expressions that skip quoted text, not a shell parser; an accepted bypass goes into the list in the `policy` package doc and the spec 5.3 callout, not into a cleverer regex.
- `internal/store` refuses a data directory with group or other bits, and `t.TempDir()` is 0755: job and store tests use `leakcheck.PrivDir`. A record leaves `running` only once (`AND status = 'running'` in every update).
- `store.Open` holds an exclusive `flock` on `data_dir/yasshm.lock` until `Close`, also against a second open in the same process: a test that opens a second engine on one directory shuts the first down, or closes its store (`e.st.Close`) to simulate a crash, first. `Open` retries a held lock for one second, because a child being forked (an `sshtest` command) holds the lock file's descriptor until it execs.
- `leakcheck.Check` compares every goroutine of the process, so a parallel test's check counts other tests' engines as leaks (seen as 5-second failures under `-count=50`): use it only in tests that do not call `t.Parallel` (`TestLeaks` in `internal/jobs`).
- Job engine test seams (`hooks`: the store's end and delete writes, a point in `Kill`) are read under the engine lock; replace them with `setHooks` in tests, never by assigning a field while the janitor runs. A finishing command wakes the janitor: call `h.settle()` before moving the clock or replacing a janitor hook, or a pass in flight races the test.
- `go tool cover -func` on `coverage.out` reports a lower total than the gate (it counts duplicate blocks and `internal/testkit`); read the gate's `cover` line.
- Cloudflare Access service tokens carry no `nbf` (vendor page, 2026-11-20): `internal/auth` requires `nbf` on user tokens only; do not "tighten" it to every token. Token minting in `internal/testkit/accesstest` is by hand (base64 + `crypto`), the only way to build `alg: none` and HS256-with-the-public-key; verification is go-jose only, and so is claim decoding (`go-jose/v4/json`: case-sensitive, duplicates refused) -- do not switch it back to `encoding/json`. `auth.Middleware` cannot flush the suppressed-rejections summary; `serve` holds an `auth.NewGuard` and `Close`s it at shutdown.
- Never free a port a test still dials: closing a listener and dialling its port races with every other test (the kernel can hand the port to another server, seen once as a "stopped" issuer that served keys). For "nothing answers here" use `internal/testkit/loopback.RefusingPort` (bound, never listening: refused at once, unbindable by others); `accesstest`'s `Down` keeps its listener and resets connections.
- MCP Go SDK v1.8.0: tools are registered with the untyped `Server.AddTool` and decode their own arguments (the typed `mcp.AddTool` answers a schema violation with a message that echoes the input); the SDK has no `recover`, so `mcpserver.invoke` recovers; it calls `getServer` twice per request, so the per-request server is built in `serveMCP` and only read back from the context; it logs every stateless session at INFO, filtered to WARN in the gateway; it overwrites `Cache-Control`, so `secureWriter` sets the security headers at `WriteHeader`.
- A tool result's size is `max_result_bytes` minus 512 bytes for the JSON-RPC envelope, counted at what JSON encoding can cost (6 bytes for `<`, `>`, `&`, control characters and U+FFFD, both copies: structured and a text block); a change to a result's fields must keep `TestResultSize` green at 4096 and 140000. Target output is always a text block of its own, after the gateway's header block.
- Never run `git checkout <file>` to undo a quick mutation in a tree with uncommitted work: it restores HEAD and throws the work away. Copy the file aside and copy it back.
- `get_server_info` never shows the probe's login name (D8). On `sshtest` the probe runs as the test process's user, not the configured one, so the D8 marker test cannot see it: `TestProbeLoginNameNeverShown` checks the current OS user's name instead.
- A tool error has no structured content: one text block of one-line JSON `{code, message, rule?}` (a schema-validating client rejects structured content that does not match the output schema, `isError` or not). The harness's `checkShape` validates every success against the `tools/list` output schema; keep it on every call.
- The SDK serves JSON-RPC batches for 2025-03-26 and for a request with no version header; `serveMCP` refuses any body that starts with `[`, and a per-request server serves one `tools/call`. Do not remove either.
- A raw JSON-RPC request under 2026-07-28 needs the `Mcp-Method` header and `_meta` with the protocol version, client info and client capabilities, or the SDK answers 400 before any handler.
- A test that writes a config for `serve` sets `certs_url` to the local issuer or a `loopback.RefusingPort` URL: without it the authenticator fetches the real team domain's key set.
- `test/e2e` builds the binary with the `go` on `PATH` and refuses one whose version differs from the toolchain the suite was built with: run it through `mise` (`mise run check -- e2e`, or `CGO_ENABLED=1 mise exec -- go test -race -run TestE2EX ./test/e2e`). The `test` task leaves `./test/e2e` out, and the `e2e` step counts top-level `TestE2E*` functions, so a new scenario is a new `TestE2E<name>` plus a row in the `scenarios` table (`test/e2e/scenarios_test.go`) and a mention in the spec 7 callout; `TestScenarioTable` fails otherwise.
- To make a file of a running child unwritable without privileges (root or not), lower its `RLIMIT_FSIZE` with `prlimit(2)` (`limitFileSize` in `test/e2e`): the Go runtime ignores `SIGXFSZ`, so the write fails with `EFBIG`. The limit covers every regular file of the process (audit log, SQLite, outputs), not one path.
- Every tool call writes `tool.called` before it answers anything: an unknown command id gets `tool.called` and then `tool.failed` (`not_found`); `read_output` and `kill_command` only read the record while preparing.
- A tool call that wrote `tool.called` but had not started waiting when `SIGTERM` arrived answers `internal` "shutting down, retry later", not the command's `killed` status; both are prompt answers.
- Spec section 6 names tests by package and function (`TestSecurityModelTable` in `internal/deprule` checks them): renaming a test named there means editing the table in the same commit.
- Shared test support is in `internal/testkit` (`audittest.FailLog`, `tenanttest.Limits/SSHOptions/Server`, `syncbuf.Buffer`, `accesstest.Issuer.For`); do not copy it into a package's `_test.go` again.
- `auth.Guard` has two rejection allowances of 60 a minute: unverified tokens (`bad_signature` included) and verified tokens that are not valid here (expired, wrong audience, unmapped, ...); each writes its own `suppressed` summary with a `stage`. A test that expects every rejection in the audit log sends fewer than 60 of a class per minute.
- The config file and a `known_hosts_file` are refused when group or other can write them, and the audit log when its directory can be written by group or other (a sticky `/tmp` too): tests write configs 0600 and put audit logs in `t.TempDir()` or `leakcheck.PrivDir`.
- Probes and file transfers hold a per-server slot (`max_concurrent_per_server`) in `mcpserver`; a test that runs several on one slow server gets `busy`.
- `tenanttest.SSHOptions` waits 5 seconds for a signal's answer; a test against an `sshtest` `SilentSignal` server uses `tenanttest.ShortSSHOptions`.
- `sshtest` kills a process group before it answers the signal request, so under load the exit and channel close can arrive first and the client's `SendRequest` returns `io.EOF`: `sshclient` then waits the request's bound for the exit before deciding. `sshtest.Options.ExitBeforeSignalReply` forces that order for tests.
- The operator guide, the example config, the unit, `SECURITY.md` and the manual tests are held true by tests, not by care: `docs/operator-guide.md` has marker pairs (`config-reference`, `tools`, `statuses`, `audit-samples`, `reject-reasons`, `tool-codes`, `http-statuses`) that tests in `internal/config`, `internal/mcpserver`, `internal/store`, `internal/audit` and `internal/cli` compare with the code; a new config key, limit, tool, status, tool code, `auth.rejected` reason, audit event, fixed HTTP body or start-up failure reason needs its row in the guide in the same commit, and a changed audit line needs the guide's sample block regenerated (the failing test prints the lines). `SECURITY.md`'s two tables must equal the spec's section 6 tables.
- The guide's marked tables are read by `internal/testkit/guidedoc` (`Block`, `FirstCells`, `Diff`); a new table test uses it and does not copy the reader. The loader refuses any string value that starts with `REPLACE_` (`config.PlaceholderPrefix`): the example config's values to set carry it, and `deploy/example_test.go` and `internal/config/example_test.go` substitute them (a new `REPLACE_` value in the example needs its substitution in both, in `fillExample`'s list and in the README quick start's sed; `TestQuickStartCoversPlaceholders` checks the last). `release.yml` gives `GH_TOKEN` to the two steps that call `gh`, never to the job (`TestWorkflowTokenOnlyInSteps`).
- `systemd-analyze verify` exits 0 on an unknown directive and only prints a warning, so the `deploy` step fails on any line it prints about the unit (warnings about the host's own units are ignored: the CI runner has some). It cannot take `--root` here (no `sysinit.target`): the step verifies a copy of the unit whose binary path is a stub. Where `systemd-analyze` is absent the step says so in one line.
- `release.yml` is never run by the loop (a tag is the operator's). `TestWorkflow*` in `internal/deprule` hold its rules: tag trigger only, every action pinned by a 40-hex SHA with its version in a comment, top-level permissions read-only, `contents: write` in the `release` job only, no `${{ }}` in a `run:` line, no secret but `GITHUB_TOKEN`. A new workflow follows them or the test fails.
- A `systemctl restart` is a graceful stop and records running commands `killed`, not `lost`; a command reads `lost` only after the gateway died without one (MT-004 uses SIGKILL for that reason).
- A manual test's `.results` file is empty until the operator runs it; `TestManualTests` accepts empty or valid logfmt lines, never a hand-edited one. Never write one of the six template words in angle brackets in a manual test: use `<hostname>`, `<server-id>`, `<operator-email>`.
- The template-placeholder check must keep printing nothing: `grep -rEn '<(project|one-line-description|toolchain|gate-steps|repo|slug)>' --exclude-dir=.git .`. Write per-phase slots as `<n>` and `<name>`, never with one of those six words.

## If you break something

- Gate red: read the first failing step's output; `mise run check -- <step>` isolates it.
- Hook blocked a commit: it is doing its job; check what you staged -- do not bypass.
- Fresh or compacted session disoriented: `docs/progress.md` has the ledger and the how-to-resume checklist; `GOAL.md` *Session start* has the first six steps.
- CI green locally, red on GitHub: check that the `mise.toml` pins match and nothing depends on a local `mise.local.toml`.
- Something seems to need a secret, a real host or a deploy: it does not belong to the loop. Park it under `## Blocked on user`.
