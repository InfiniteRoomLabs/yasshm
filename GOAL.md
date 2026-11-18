# GOAL.md -- autonomous next-phase goal

Run with `/goal complete everything in @GOAL.md` (from a session that starts above the clones: `/goal complete everything in @yasshm/GOAL.md`). Each run ships exactly one phase of `yasshm` through the pipeline: verify premise -> implement -> four-lane review gate -> merge -> ship -> retarget this file. The design oracle is `docs/superpowers/specs/2026-11-18-yasshm-design.md` (the "spec"); `CLAUDE.md` holds the conventions and dispatch rules; `docs/progress.md` is where we are.

**Shipped so far:** nothing yet. The planning session cut the template, modelled the architecture, and wrote the spec, this file and the work orders. Next default target: **Phase 0 (Scaffold)**. Full roadmap in *Retarget* at the bottom.

---

## Session start (every run, before stage 1)

This build runs in a cloud session with two repositories. Do these first, every run:

1. **Find the clones.** The session starts in the directory above them. This repo is the directory that holds this file. The other git clone beside it is the operator's private infra repository. Use absolute paths and `git -C <dir>` everywhere; never rely on the working directory.
2. **Read the rulebook of a repo before touching it:** `CLAUDE.md` here, and the infra repo's own `CLAUDE.md` and `CONTRIBUTING.md` before any work there (Phase 8 only). The two repos have different rules; neither overrides the other.
3. **Toolchain:** run `scripts/bootstrap.sh` (installs `mise` and the pinned tools; a no-op when current). If `mise` is not on `PATH`, call it as `$HOME/.local/bin/mise`.
4. **Branches:** both clones must be on `feat/yasshm-mvp`, fast-forwarded to `origin/feat/yasshm-mvp`, with `git status --porcelain` empty. If the harness started a clone on another branch (the default branch, or a working branch of its own), `git -C <dir> fetch origin` and `git -C <dir> switch feat/yasshm-mvp`: all work lands on that branch and on `phase-<n>/<name>` branches, nowhere else. If either clone cannot be brought to that state, reconcile before doing anything else.
5. **Read `## Blocked on user` below.** If it names an action, verify that the action landed; if it did not, stop after one turn with no tool call.

**Hard limits for every phase (spec D16, D17).** The loop never deploys, never applies infrastructure, never reaches a real target or the real edge, never logs in to a password manager, never reads or asks for a secret, never runs a manual test, never pushes a tag, never force-pushes, never deletes a branch, and never pushes to `main` here or to the infra repo's default branch. No done criterion depends on any of those. A step that needs one is written under `## Blocked on user`.

**The integration branch is `feat/yasshm-mvp`.** It exists under that name in both repos. Wherever a work order or a lesson says `main`, read `feat/yasshm-mvp`: phase branches fork from it and merge into it with `--no-ff`.

---

## Goal (current target: Phase 0 -- Scaffold)

> **Goal -- don't stop until Phase 0 (Scaffold) ships:**
>
> 1. **Verify the premise before building.** Do *Session start* above. Read spec sections 2, 3, 7 and 9 and `CLAUDE.md`. Confirm, and record each result in spec section 3 and `docs/progress.md`:
>    - `mise run check` prints `check.sh: all OK` on the clean tree you start from (F11).
>    - The agent-ops `agency` plugin is loaded (its skills appear in the session's skill list; the environment's setup script installs it). If it is not, its changelog and version guards are absent: apply the same rules by hand and say so in `docs/progress.md`.
>    - F12: can the sandbox install and run a real `sshd` (`apt-get install -y openssh-server`, then start it as the current user on a loopback port with a temporary config and key, and log in with a Go or `ssh` client)? Can `govulncheck` reach its database and `go mod download` reach the module proxy? Is a Docker daemon available (only Phase 8 cares)?
>    - F13: pushing a phase branch works and CI runs on it; read the result with `gh api repos/InfiniteRoomLabs/yasshm/commits/<sha>/check-runs`.
>    - F16: 15 minutes, no more, of search-before-build. Park only on the condition in spec section 1.
>    - Anything wrong: fix the spec with a `> **STATE AS OF YYYY-MM-DD**` callout in the affected section, note it in `docs/progress.md`, and continue.
> 2. **Implement** on branch `phase-0/scaffold` by dispatching ONE implementer (`general-purpose`, `model: "sonnet"`, work order from `docs/phases/_templates/implementer.md`, pointers to spec sections 2 (D2, D14, D15), 7 and 9). Deliverables, each with an acceptance criterion:
>    - Go module `github.com/InfiniteRoomLabs/yasshm` with `cmd/yasshm` and a `version` subcommand whose values are injected at build time -- `mise run build` writes `dist/yasshm_<version>_linux_amd64` and `_arm64` (`CGO_ENABLED=0`, `-trimpath`) plus `dist/SHA256SUMS`, and the amd64 binary prints a version, a commit and a build date; a unit test covers the command.
>    - The gate of spec section 7 in `scripts/check.sh`: steps `fmt lint skipcheck test cover vuln build arch`, with `.golangci.yml` (warnings are errors; a `nolint` needs a named linter and a reason) -- each step runs alone with `mise run check -- <step>`; `usage lint scripts/check.sh` is clean.
>    - `skipcheck` and `cover` are proven to bite -- the implementer report shows the output of a temporary skipped test failing `skipcheck` and of a floor one point above the measured total failing `cover`; neither temporary change is committed. The floor is 85 and excludes only `internal/testkit/...`.
>    - A dirty tree fails the gate (the banner in `scripts/check.sh` becomes `exit 1`) -- shown in the report.
>    - `mise` tasks `build`, `test`, `lint`, `fuzz` (the last runs every fuzz target for 30 seconds and passes when there are none yet).
>    - The real-`sshd` decision of spec section 7, taken from the F12 result: either `scripts/bootstrap.sh` checks for the binary and CI installs it, or the CI-only arrangement is written into the spec with a callout. The lane itself arrives in Phase 2.
>    - `TESTING.md`, `CLAUDE.md` (commands, gotchas) and `README.md` describe the gate as it now is; `CHANGELOG.md` has the `[Unreleased]` entries.
>    - Acceptance: `mise run check` green on a clean tree, and CI green on `phase-0/scaffold`.
> 3. **Four-lane gate** -- dispatch code-review, simplification, and security lanes in parallel (read-only), then the QA lane (the only lane that runs the gate); all `general-purpose`, **`model: "opus"`**. Work orders from `docs/phases/_templates/`. Reports to `docs/phases/0/reports/<lane>.md` AND via `SendMessage` to `team-lead` AND as the lane's final text. Commit the three read-only reports before dispatching QA (QA needs a clean tree). Triage, ONE fix commit `fix(scaffold): apply the review-gate findings`, re-run the gate, confirm `git status --porcelain` is empty.
> 4. **Ship** -- merge `phase-0/scaffold` into `feat/yasshm-mvp` with `--no-ff` (body summarises gate results); update `CHANGELOG.md` + `docs/progress.md`; push `feat/yasshm-mvp`; confirm CI green on the merge commit. Then **retarget THIS file to Phase 1** (see *Self-advance*) and commit it with the docs.
>
> **Done when:** the Phase 0 deliverables above are met, CI green, the gate green locally on a clean tree, `docs/progress.md` + `CHANGELOG.md` updated, **and `GOAL.md` retargeted to Phase 1.** **Also done when** the `## Blocked on user` section below names one concrete action only the user can take and the lead has committed it and stopped -- a parked attended step is a clean exit, not a failure. **Stop only** for that, for a genuine decision you + an advisor skill cannot confidently make, or for a permission the harness will not grant.
>
> **When blocked on the user:** write the action under `## Blocked on user`, commit, push, say so once, and stop **without any further tool calls** (no polling the remote, no status re-prints -- the goal hook treats tool use as progress and re-prompts every few seconds). Never poll for a human.

## Blocked on user

Nothing is parked.

### Self-advance (do this as the last Ship step, every run)

Leave `GOAL.md` pointed at the **next** phase so the following `/goal` run is paste-ready:
- Update **"Shipped so far"** and strike the shipped row in the **Retarget table** (mark `SHIPPED`, add merge sha + one-line lesson).
- Rewrite the **`## Goal` heading + block** for the next phase: stage 1 is *Session start* plus that phase's premise (the "Re-verify in" column of spec section 3 names the facts each phase must settle); stage 2 names the branch, implementer model (from the Retarget table), work-order template, and concrete deliverables with acceptance criteria, taken from that phase's row in spec section 9 and the spec sections the row points to; stage 3 is the gate with the reviewer model one tier above; stage 4 is ship + retarget.
- Append anything you learned to **Lessons** (process traps only -- feature notes go in `docs/progress.md` and spec callouts).
- **Advance only within the approved roadmap.** If the next row in *Approved roadmap* exists, rewrite the `## Goal` block for it. If it does not, the roadmap is exhausted: write the maintenance/park block below instead of inventing a phase.
- Any follow-on work the lanes surfaced this run goes under *Proposed (needs user approval)*, not into the approved roadmap. It runs only after the user approves it (which moves it up into *Approved roadmap*).
- **Next approved phase after Phase 0: Phase 1 (Config, tenants, policy, audit).**

### Parking when the roadmap is exhausted

When no approved phase remains, replace the `## Goal` block with a park block: state that the approved roadmap is done, list the *Proposed* rows (each waiting on the user to approve or on an attended input), and set **Done when** to "the user has approved a proposed row (moved it into *Approved roadmap*) and rewritten this block into that phase, or decided none runs." Mark it **Blocked on user**. A `/goal` run that lands here reads `## Blocked on user`, sees nothing approved changed, and stops after one turn with no tool call.

### Parking before Phase 9 (the attended queue)

Phase 9 is attended and so is everything between Phase 8 and it. When Phase 8 has shipped, do not start Phase 9. Write this list under `## Blocked on user`, in this order, and park:

1. Review and merge `feat/yasshm-mvp` into `main` in this repo (a pull request is the usual way; CI runs on it).
2. Tag `v0.1.0` on `main` and push the tag; the release workflow publishes the binaries and `SHA256SUMS`.
3. Review and merge `feat/yasshm-mvp` in the infra repo, following that repo's rules.
4. Deploy, following the infra repo's plan document: apply the edge configuration, converge the host and the target accounts.
5. Run the manual tests `test/manual/MT-001` to `MT-005` and record the results beside them. A failure is a bug report, not a reason for the loop to deploy.
6. Then start Phase 9 with the operator present.

---

## Lessons from prior runs (read before starting)

- **Verify the premise before implementing.** Autonomous runs regularly discover the goal's own premise wrong (missing features, mislabeled components, stale counts). The oracle's actual behavior wins over the spec; fix the spec in the same run with a `STATE AS OF` callout.
- **Model tiers are pinned in agent definitions, not inherited.** Omitting `model:` on an `Agent` call silently uses whatever the agent file pins (often haiku). Pass `model:` explicitly on every dispatch; reviewers one tier above the implementer (sonnet -> opus, opus -> fable). Never use haiku where a permission prompt could occur -- it has no auto mode and stalls the run.
- **Check the agent's `tools:` list before writing its prompt.** Curated review agents often lack `Bash` or `SendMessage` and will appear to end silently. `general-purpose` (tools `*`) works for every lane.
- **Tell agents HOW to deliver.** A named background agent's final text does not reach the lead by itself. Instruct it to call `SendMessage` to **`team-lead`** (not `main`) with the full report in `message` (not `summary`), AND to write the report to `docs/phases/<n>/reports/<lane>.md`.
- **Only the QA lane runs the gate.** Concurrent gate runs in one tree collide on build/coverage outputs and produce spurious reds. Code-review, simplification, and security lanes are read-only (`git`, `grep`, read).
- **A green HEAD says nothing about a dirty working tree.** Verify `git status --porcelain` is empty before reporting a gate result or claiming a fix landed.
- **The changelog guard is a Claude hook, not a git hook.** Commits on `main` are blocked unless `CHANGELOG.md` is staged; write the changelog entry with the commit, not after. The version guard requires `git add` and `git commit` as **separate tool calls** (no `-a`/`-am`, no `&&` chaining).
- **Bash `cd` persists across tool calls.** Use absolute paths or `git -C <dir>`; never rely on the cwd a previous call left behind.
- **The docs that describe a scrub leak the thing they scrubbed.** A goal's acceptance criterion or a lane report that quotes the real value defeats the scrub, and the redaction check passes if the term list does not know the value yet. Add the project's secret-shaped identifiers to the redaction term list before the first commit that could mention them, and write "the real <thing>" instead of the value everywhere.
- **Lane reports quote what they flag.** Grep every report for the redaction terms and for template placeholders before committing it.

Added by the planning session (2026-11-18), specific to this build:

- **`main` means `feat/yasshm-mvp` here.** The changelog guard only fires on `main`, so on the integration branch nothing blocks a merge without a changelog entry. Stage `CHANGELOG.md` with every merge anyway; the rule is the same, only the enforcement is missing.
- **Commit the three read-only lane reports before dispatching QA.** They are written uncommitted, and QA must fail a dirty tree.
- **This repo is public and the infra repo is private.** Nothing from the infra repo (host names, addresses, account or tenant ids, vault item names, anything in its plan documents) goes into a file, report or commit message here. Point at "the infra repo's plan document" instead of quoting it.
- **If `SendMessage` is not in a lane's tools, the report file and the lane's final text are the delivery.** Do not wait for a message that cannot arrive.
- **`gh pr` and `gh issue` fail in a cloud session** (GraphQL is rejected). Use REST: `gh api repos/<owner>/<name>/...`.
- **A missing tool is never a silent skip.** If a gate step cannot run in the sandbox, that is a finding: record it, move the step to CI with a callout in the spec, and make QA check the CI run.

## Reference

- **Spec:** `docs/superpowers/specs/2026-11-18-yasshm-design.md`. Section 2 is locked; sections 4 to 8 are the build oracle; section 9 is this process.
- **Conventions + dispatch rules:** `CLAUDE.md`. **Status:** `docs/progress.md`. **Testing rules:** `TESTING.md`.
- **Work orders:** `docs/phases/_templates/{implementer,code-review,simplify,security,qa}.md`. Phase artifacts: `docs/phases/<n>/reports/`.
- **Oracle / ground truth** (read the primary source, not a summary of it):
  - MCP specification, Streamable HTTP transport and tool results: `https://modelcontextprotocol.io/specification/`
  - MCP Go SDK source, docs and release notes: `https://github.com/modelcontextprotocol/go-sdk`
  - Claude custom connector requirements: `https://claude.com/docs/connectors/building`
  - Cloudflare Access application tokens and their validation: `https://developers.cloudflare.com/cloudflare-one/access-controls/applications/http-apps/authorization-cookie/application-token/` and the "Validate JWTs" page beside it
  - OpenSSH release notes and manual pages (`sshd`, `sshd_config`, `authorized_keys`): `https://www.openssh.com/`
  - SSH connection protocol (channels, `exec`, `signal`, exit status): RFC 4254
  - The infra repo's own `CLAUDE.md`, `CONTRIBUTING.md` and its yasshm plan document under `docs/plans/` (Phase 8 only)
- **Toolchain:** everything through `mise run ...` / `mise exec -- ...`. `mise run check` is the gate.
- **Exemplars to mirror:** within this repo, the most recently shipped phase (before Phase 1 there is none; follow Effective Go and the standard library's own style). Outside: the MCP Go SDK's `examples/` for server shape.

## Retarget / roadmap

### Approved roadmap

Drafted by the planning session on 2026-11-18 from the owner's decisions (spec section 2). **The owner approves these rows by starting the loop on this branch**; before that first run the owner may edit them freely. This is the ceiling: the loop runs these and stops. One row per phase; strike and mark `SHIPPED` as they land. "Attended" means the lead should have the user available. Adding a row here requires the user's approval -- the lead may reorder these but never append a new one on its own.

| Phase | Branch | Implementer -> Reviewers | Effort | Notes |
|---|---|---|---|---|
| **0 Scaffold** | `phase-0/scaffold` | sonnet -> opus | low | Go module, `version` command, the full gate, `mise` tasks, sandbox facts settled. Spec 7, 9 |
| **1 Config, tenants, policy, audit** | `phase-1/config-policy-audit` | opus -> fable | med | Spec 5.1 to 5.4 and `config check`. `look` only, `full` reserved, one tenant, tenant id everywhere, audit-before-act |
| **2 SSH layer** | `phase-2/ssh-layer` | opus -> fable | high | Spec 5.5. Pinned host keys, exec, signals, SFTP, the in-process test server and the real-`sshd` lane |
| **3 Jobs, store, outputs** | `phase-3/job-engine` | opus -> fable | high | Spec 5.6. Lifecycle, paging, limits, retention, restart recovery, SQLite |
| **4 Authenticator** | `phase-4/authenticator` | opus -> fable | med | Spec 5.7. Access token verification behind the interface, local issuer for tests |
| **5 MCP endpoint and tools** | `phase-5/mcp-tools` | opus -> fable | med | Spec 5.8, 5.9. The eight tools, stateless Streamable HTTP, `serve`, the fake edge |
| **6 End-to-end and hardening** | `phase-6/e2e-hardening` | opus -> fable | high | Spec 6, 7. The real binary through a real MCP client; every scenario; fuzz run; each guarantee mapped to a test |
| **7 Packaging and operator docs** | `phase-7/packaging-docs` | sonnet -> opus | med | Spec 7, 8. `deploy/` examples, release build and workflow, operator guide, threat model, manual tests MT-001 to MT-005 (written, never run) |
| **8 Infra deployment support** | `phase-8/infra-deploy` in the infra repo, forked from and merged into its `feat/yasshm-mvp` | opus -> fable | med | Spec 8, D17, and the infra repo's plan document. Prepared and statically validated under that repo's rules; never applied; no secret needed. Reports and the ledger stay in this repo and quote nothing private |
| **9 History scrub** | `phase-9/history-scrub` | opus -> fable | low | **Attended.** Always the final row. `public-readiness` over tree + full history + commit messages; `git filter-repo --replace-text` with the redaction term list; force-push EVERY remote (internal mirrors included); re-clone. This repo has been public since its first commit, so a finding here is also an exposure to rotate, not only a line to rewrite |

**Batch option:** none. One phase per run, each in a fresh context.

### Proposed (needs user approval)

Follow-on work the review lanes surfaced mid-run. The lead writes rows here freely; the loop does **not** run them. Each stays here until the user promotes it into *Approved roadmap*. When the approved roadmap is exhausted and this list is non-empty, the run parks (see *Parking when the roadmap is exhausted*) rather than promoting one itself.

| Proposed phase | Why (surfaced by) | Effort | Attended? |
|---|---|---|---|
| -- | -- | -- | -- |
