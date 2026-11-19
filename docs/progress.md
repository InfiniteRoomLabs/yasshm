# Progress

Living status doc. Read first, update at every phase boundary. Last updated: 2026-11-19.

## Current state

- Repo cut from `InfiniteRoomLabs/template-repo` and planned. Phase 0 (Scaffold) and Phase 1 (Config, tenants, policy, audit) have shipped: the full gate; the tenant types, the strict config loader, the policy engine with its guard rails, the append-only audit log, the checked key-file reader, and `yasshm config check`. Nothing connects to anything yet; Phase 2 (the SSH layer) is next.
- The plan lives on the branch `feat/yasshm-mvp` here, and the deployment plan lives on a branch of that name in the operator's private infra repo. Nothing has been merged to `main`.
- The build is one cloud session that holds both repositories and runs the whole roadmap, with a fable orchestrator as lead. It is started with both clones on the plan branch, makes its own working branch in each, and does all its work there. A phase is a commit range on that branch (`GOAL.md`, *How a phase lands*).
- Planning artifacts are in place: the design spec (`docs/superpowers/specs/2026-11-18-yasshm-design.md`), `GOAL.md` (targeting Phase 0, with the kickoff prompt), the five work orders (`docs/phases/_templates/`), and the LikeC4 model of the planned system (every component is tagged `#planned` until its phase builds it).
- Toolchain: Go 1.27.4 and golangci-lint 2.15.0 are pinned in `mise.toml` next to the LikeC4 tools. `scripts/bootstrap.sh` installs everything on a bare machine. The gate (`mise run check`) runs `fmt lint skipcheck test cover vuln build arch` and fails on a dirty tree. govulncheck 1.10.0 is pinned there too.
- CI runs the gate on pushes to every branch and on pull requests.
- No credentials are wired and none are needed: the build never deploys and never touches a secret (spec D16).
- Spec status: section 2 is locked from the owner's decisions; section 3 still has INFERRED and TODO rows, each with the phase that must settle it.

## Working branches

Filled in by the session at kickoff. Record this repo's working branch name here. For the infra repo write only "see the infra clone" (its branch name stays out of this public repo).

- This repo: `claude/serene-franklin-jv4pu7` (started there; it was created at the head of the plan branch)
- Infra repo: see the infra clone

## Phase ledger

A phase is the commit range from its start commit (the phase base) to its ship commit.

| Phase | Status | Phase base .. ship commit | Notes |
|---|---|---|---|
| 0 Scaffold | SHIPPED | `34d6001` .. the commit `docs(phase-0): ship scaffold` | Gate: code review REQUEST CHANGES (1 blocking), security PASS, QA NEEDS WORK on the same blocker; fixed in `27e6c67`; gate and CI green after it. Coverage 95.2 percent |
| 1 Config, tenants, policy, audit | SHIPPED | `6cc1f31` .. the commit `docs(phase-1): ship config, tenants, policy, audit` | Gate: code review REQUEST CHANGES (2 blocking), security BLOCK (1 blocking), QA NEEDS WORK (reproduced all three, found a fourth); fixed in `c92a45f`; the lead's probe of that fix found a regression, fixed in `9422125`; gate and CI green after it. Coverage 95.9 percent. YAML library: `go.yaml.in/yaml/v3` (F18) |
| 2 SSH layer | in progress | base: the commit `docs(phase-2): start ssh layer` | Premise: gate and CI green on the Phase 1 ship commit; F10 read in the sources; the real-`sshd` lane is CI-only |
| 3 Jobs, store, outputs | not started | -- | |
| 4 Authenticator | not started | -- | |
| 5 MCP endpoint and tools | not started | -- | |
| 6 End-to-end and hardening | not started | -- | |
| 7 Packaging and operator docs | not started | -- | |
| 8 Infra deployment support | not started | -- (in the infra clone) | Prepared and validated, never applied |
| 9 History scrub | not started | -- | Attended |

## Discoveries

- 2026-11-18 (planning): a cloud session with several repositories starts above the clones and loads no hooks or settings from either repo, so the toolchain is installed by `scripts/bootstrap.sh` at the start of every phase, not by a session hook (spec F14).
- 2026-11-18 (planning): the planning sandbox had no `ssh` or `sshd` binary and no running Docker daemon. Phase 0 decides how the real-`sshd` test lane runs (spec F12, section 7).
- 2026-11-18 (planning): the edge may cut a request whose origin is silent for about 100 seconds, so the default ceiling for a synchronous wait is 60 seconds, not the client's 300 (spec F15, still INFERRED).
- 2026-11-19 (planning): the build was reworked from one run per phase on shared branches to one session that runs the whole roadmap on one working branch per clone, with no phase branches and no merges. Reasons: a session can count on pushing only its own working branch (spec F13), and an idle session's disk is not kept (spec F20). A session can start each clone on a chosen branch (spec F19), so it starts on the plan branch.

- 2026-11-19 (build, Phase 0 premise): the gate is green on the clean tree the session started from (8 seconds), CI is green on the pushed working branch, and pushing the working branch works. The `agency` plugin is loaded (its skills and agents are listed). Whether its changelog and version guard hooks fire in this session was not observed, so their rules are applied by hand: `CHANGELOG.md` is staged with every ship commit, and `git add` and `git commit` are separate tool calls.
- 2026-11-19 (build, Phase 0 premise): the module proxy and the vulnerability database are reachable. There is no Docker daemon. There is no `sshd`, and the session's permission layer refused to install `openssh-server`; the lead did not work around it, so the real-`sshd` lane is CI-only (spec section 7 callout, F12). Adding `openssh-server` to the cloud environment's setup script would give a later session the lane locally.
- 2026-11-19 (build, Phase 0 premise): `scripts/redaction-check.sh` finds the plugin's resolver but the resolver has no term list in this session, so the check prints "no redaction terms configured" and passes. Hygiene is therefore held by rule and by the security lane, not by that script: nothing from the infra clone is quoted here, and fixtures are synthetic.
- 2026-11-19 (build, Phase 0 gate): the first `cover` step summed every line of the profile, and `-coverpkg=./...` writes one copy of each block per test binary, so the total would have fallen as packages were added (a second tested package measured 45.2 percent where the truth was 93.3). It now merges blocks by key. The code-review and QA lanes both found it by counting statements by hand.
- 2026-11-19 (build, Phase 0 gate): advisories that were not applied, each for a reason. Pinning the mise version in CI: `scripts/bootstrap.sh` installs the current mise locally too, so a pin in one place would only make the two differ; pin both together if the operator wants it. Flagging `//go:build` tags on test files in `skipcheck`: no test file has a tag yet; Phase 2 must not use a build tag to keep the real-`sshd` tests out of the local gate without the gate saying so. The remaining simplification options (one `cd` in `check.sh`, default-valued lint settings, the step list repeated in `README.md`) change nothing observable and were left.
- 2026-11-19 (build, Phase 1 gate): four blockers, all fixed in `c92a45f`: a principal with a null value was dropped silently, leaving a tenant nobody maps to; the policy engine allowed zero and out-of-range values; a `known_hosts` file with a certificate-authority line counted as a pinned key; the limit ranges were tested against the table under test. The guard rails were widened and narrowed where the lanes showed cheap bypasses and wrong denials; what a guard rail does not catch is listed in the spec 5.3 callout.
- 2026-11-19 (build, Phase 1 gate): the fix commit made command matching skip quoted text, and the lead's own probe of it found that a quote character in a shell comment or a here-doc body then hid every later line from the command rules (a comment with an apostrophe, then `sudo id` on the next line, was allowed). `9422125` stops quote skipping at a newline, so every line start is a command start again, and adds a fuzz property for it. This is why there are two fix commits in this phase. One probe case is still allowed and is left as a guard-rail limit (spec D6): a command substitution in backticks that holds an unmatched quote, followed by a separator.
- 2026-11-19 (build, Phase 1 gate): not applied, each for a reason. The larger `decode.go` refactor (simplification items 5 and 6): better at a phase start than in a fix commit. A closed set or server-side generation for audit request ids: Phase 5 owns request ids. A test against the `scripts/build.sh` binary: Phase 6 end-to-end tests run the real binary.
- 2026-11-19 (build, Phase 2 premise, done early): F10 was checked against the OpenSSH 7.9 release note and `session.c`. The `signal` request is honoured for command sessions that are not subsystems and not under a forced command, for a fixed list of signal names, and is delivered to the process group. The Go client's `Session.Signal` sends it without asking for a reply, so the SSH layer sends the request itself with want-reply set to learn whether the server accepted it. No kill was found on channel close, so closing is never relied on to stop a command.

## Next action

The build session is running. The current target is in `GOAL.md` under `## Goal`; the ledger above says how far it got.

## How to resume in a fresh or compacted session

1. Read this file, then `GOAL.md` (its *Session start* section first), then `CLAUDE.md`.
2. `git status --porcelain` must be empty and `git log --oneline -5` should match the ledger above. If not, reconcile before starting.
3. Read only the spec sections the current phase names.
4. Continue the goal from the current `## Goal` block.
