# GOAL.md -- autonomous build goal

One cloud session, one orchestrator, both repositories. It is started once with the kickoff prompt below and ships the approved roadmap one phase at a time through the pipeline: verify premise -> implement -> four-lane review gate -> ship -> retarget this file -> next phase. The design oracle is `docs/superpowers/specs/2026-11-18-yasshm-design.md` (the "spec"); `CLAUDE.md` holds the conventions and dispatch rules; `docs/progress.md` is where we are.

**Shipped so far:** Phase 0 (Scaffold): the Go module with `version`, the full gate, the `mise` tasks, CI with `sshd` on the runner, and the sandbox facts settled. Next target: **Phase 1 (Config, tenants, policy, audit)**. Full roadmap in *Retarget* at the bottom.

## Kickoff

Start one cloud session with this repo and the operator's infra repo attached, both on the branch `feat/yasshm-mvp`, on the fable model. (If a clone starts on its default branch instead, the prompt still works: it merges the plan in.) Send:

```text
/goal The approved roadmap in yasshm/GOAL.md is shipped through Phase 8 and its attended queue is written under "Blocked on user" and pushed -- or "Blocked on user" names one action only the user can take. First, in each of the two clones: your working branch is the branch you were told to develop on, or the claude/... branch the clone is already on; if there is neither, create and switch to claude/yasshm-mvp. It is never the default branch and never feat/yasshm-mvp. Then bring in the plan: git fetch origin feat/yasshm-mvp, then git merge --no-edit FETCH_HEAD (a no-op if the clone started from that branch; never rebase; if CHANGELOG.md conflicts, keep both sides' entries). Then read yasshm/GOAL.md from "Session start" and follow it. You are the orchestrator: subagents build and review; you dispatch, verify and ship.
```

---

## Session start (at kickoff, and again before every phase)

You are compacted during a run this long. Do not trust what you remember: do these steps from the files, every phase.

1. **Find the clones.** The session starts in the directory above them. This repo is the directory that holds this file. The other git clone beside it is the operator's private infra repository. Use absolute paths and `git -C <dir>` everywhere; never rely on the working directory.
2. **Working branches.** In each clone, the working branch is the one this session was told to develop on, or the `claude/...` branch the clone was already on; if there is neither, create `claude/yasshm-mvp`. It is never the default branch and never `feat/yasshm-mvp`. It must contain the plan: after `git -C <dir> fetch origin feat/yasshm-mvp`, the check `git -C <dir> merge-base --is-ancestor FETCH_HEAD HEAD` succeeds; if it does not, run the merge from the kickoff prompt (a shallow clone that reports unrelated histories needs `git -C <dir> fetch --deepen=200 origin` first). If this session replaces an earlier one (the phase ledger in `docs/progress.md` is behind what a `claude/...` branch on the remote shows), fetch that branch and merge it into your working branch before anything else.
3. **Read the rulebook of a repo before touching it:** `CLAUDE.md` here, and the infra repo's own `CLAUDE.md` and `CONTRIBUTING.md` before any work there (Phase 8 only). The two repos have different rules; neither overrides the other.
4. **Toolchain:** run `scripts/bootstrap.sh` (installs `mise` and the pinned tools; a no-op when current). If `mise` is not on `PATH`, call it as `$HOME/.local/bin/mise`.
5. **Clean and pushed:** `git status --porcelain` is empty in both clones, and each working branch is pushed (`git -C <dir> push -u origin HEAD`). If a push of the working branch is refused, that is a `Blocked on user` item, not something to work around.
6. **Read `## Blocked on user` below.** If it names an action, verify that the action landed; if it did not, stop after one turn with no tool call.

## How a phase lands (one branch per clone, no merges)

A cloud session can count on pushing exactly one branch per repository: its working branch (spec F13). So there are no phase branches and no merges. A phase is a commit range on the working branch:

1. **Start commit:** `docs(phase-<n>): start <name>` sets the phase's row in the `docs/progress.md` ledger to in progress. Its SHA is the **phase base**; record it in the ledger.
2. **Work:** the implementer commits on the working branch and pushes after every green commit.
3. **Gate:** the lanes review `git diff <phase base>..HEAD`. One fix commit, `fix(<scope>): apply the review-gate findings`.
4. **Ship commit:** `docs(phase-<n>): ship <name>` carries `CHANGELOG.md`, the ledger row (SHIPPED, with the commit range) and this file retargeted to the next phase. Its body summarises the gate results. Push, and confirm CI is green on it.

**Push the working branch after every green commit.** An idle session's machine can be reclaimed, and a reopened session starts from a fresh clone: only pushed commits survive (spec F20).

## Orchestrator rules

- **You dispatch, verify and ship. You do not write product code.** One implementer per phase, then the four lanes, all as subagents with an explicit `model:`. Your own context is for decisions, not for diffs: read reports from `docs/phases/<n>/reports/`, not from memory.
- **Keep going.** When a phase ships, do not stop and do not wait for a new prompt: go back to *Session start* and run the next approved phase. The run ends only at a park (see *Blocked on user*).
- **Clean up between phases:** `TaskStop` every agent of the phase that just shipped.
- **One phase at a time, in roadmap order.** No phase starts before the previous one has shipped.

**Hard limits for every phase (spec D16, D17).** The session never deploys, never applies infrastructure, never reaches a real target or the real edge, never logs in to a password manager, never reads or asks for a secret, never runs a manual test, never pushes a tag, never force-pushes, never deletes a branch, and never pushes any branch except its working branch (so never `main` here, never the infra repo's default branch, never `feat/yasshm-mvp`). No done criterion depends on any of those. A step that needs one is written under `## Blocked on user`.

---

## Goal (current target: Phase 1 -- Config, tenants, policy, audit)

> **Goal -- ship Phase 1 (Config, tenants, policy, audit), then go on to Phase 2:**
>
> 1. **Verify the premise before building.** Do *Session start* above. Read spec sections 2 (D5 to D8, D12, D14), 4, 5.1 to 5.4, 5.9 and 6, and `CLAUDE.md`. Confirm, and record each result in spec section 3 and `docs/progress.md`:
>    - `mise run check` prints `check.sh: all OK` on the clean tree you start from, and CI is green on the head.
>    - F18 (YAML half): the implementer picks one of the two candidate YAML libraries after checking, in the library's own source or docs, that it can reject unknown keys, reject duplicate keys and report the position or path of each problem. The choice and the reason go into F18.
>    - The sandbox runs as root, so a file mode alone does not make a write fail. Tests that need an unwritable audit log or an unreadable key use a mechanism that also fails for root (a path under a regular file, a directory in place of the file, `/dev/full` for a failing write) and check key permissions by mode bits, not by trying to open the file.
>    - Anything wrong: fix the spec with a `> **STATE AS OF YYYY-MM-DD**` callout in the affected section, note it in `docs/progress.md`, and continue.
> 2. **Implement.** Make the start commit `docs(phase-1): start config, tenants, policy, audit` and record the phase base. Dispatch ONE implementer (`general-purpose`, `model: "opus"`, work order from `docs/phases/_templates/implementer.md`, pointers to spec sections 2, 4, 5.1 to 5.4, 5.9, 6 and 7). Deliverables, each with an acceptance criterion:
>    - `internal/tenant` (spec 5.2): `TenantID`, `Principal`, `AccessLevel` ordered `none < look < full`, `ServerID`, `CommandID`, and the server type with `IdentityFor(level)` -- it depends on nothing else in the module; the level comparison `required <= granted` exists in exactly one function.
>    - `internal/config` (spec 5.1): strict decoding and every validation rule of 5.1, errors as one line per problem in the form `path.to.key: message`, the limits with their defaults and allowed ranges -- every rule has a passing and a failing test; `access: full` is rejected with the exact message of 5.1; a second tenant is rejected through the one constant `MaxTenants`; a key file readable by group or other is refused; the example config of 5.1 loads when its key path and host key are replaced by ones generated in the test.
>    - `internal/policy` (spec 5.3): `Decide` with the three checks in order and the codes `not_found`, `access_denied`, `policy_denied`; the built-in command denylist and path denylist each in one file -- every built-in pattern has a must-match and a must-not-match case; a server of another tenant gives `not_found`, never `access_denied`; a grant of `full` built in test code is accepted by the policy engine (D5); operator additions from the config are applied.
>    - `internal/audit` (spec 5.4): the JSONL writer (`O_APPEND`, mode 0600, synced after each event, UTC RFC 3339 timestamps) and typed constructors for every event of 5.4 -- audit-before-act is proven: with a log that cannot be written the write returns an error and the guarded action in the test does not run; concurrent writers never interleave a line; an existing log is appended to, never truncated; no event type can carry a token or key material.
>    - `yasshm config check --config <file>` (spec 5.9): exit 0 on a valid file, exit 1 with the error lines on stderr otherwise -- covered by tests through the command function, and by one test that runs it against a file on disk.
>    - Fuzz targets for the config decoder and for the command and path guard rails, with seed corpora that run in the gate as ordinary tests; `mise run fuzz` finds and runs them.
>    - Architecture: `yasshm.config`, `yasshm.policy` and `yasshm.audit` lose `#planned` in the same commits that build them; `mise run arch:gen` output committed.
>    - The dependency rule of spec section 4 holds (`tenant` imports nothing from the module; `config`, `policy`, `audit` import only `tenant`, except that `policy` may take the operator's deny lists as plain values) and a test or lint rule enforces it.
>    - New dependencies are only a YAML library and `golang.org/x/crypto` (D14), each justified in the report; `mise run check -- vuln` clean.
>    - `README.md`, `TESTING.md` and `CLAUDE.md` gotchas updated where the phase changed something; `CHANGELOG.md` has the `[Unreleased]` entries.
>    - Acceptance: `mise run check` green on a clean tree with coverage at or above 85, and CI green on the pushed head.
> 3. **Four-lane gate** -- dispatch code-review, simplification, and security lanes in parallel (read-only), then the QA lane (the only lane that runs the gate); all `general-purpose`, **`model: "fable"`**. Work orders from `docs/phases/_templates/`. Reports to `docs/phases/1/reports/<lane>.md` AND as the lane's final text (AND via `SendMessage` to `team-lead` where the lane has that tool). Commit the three read-only reports before dispatching QA (QA needs a clean tree). Triage, ONE fix commit `fix(config): apply the review-gate findings`, re-run the gate, confirm `git status --porcelain` is empty.
> 4. **Ship** -- make the ship commit `docs(phase-1): ship config, tenants, policy, audit` (body summarises gate results) with `CHANGELOG.md`, `docs/progress.md` and **this file retargeted to Phase 2** (see *Self-advance*); push the working branch; confirm CI green on the ship commit. Then start Phase 2 from *Session start*.
>
> **Phase 1 is done when:** the deliverables above are met, CI green, the gate green locally on a clean tree, `docs/progress.md` + `CHANGELOG.md` updated, **and `GOAL.md` retargeted to Phase 2 and pushed.**
>
> **The run is done when** phases 0 to 8 are marked `SHIPPED` in the roadmap below and the attended queue is written under `## Blocked on user` and pushed. **It is also done when** `## Blocked on user` names one concrete action only the user can take and the lead has committed it, pushed it and stopped -- a parked attended step is a clean exit, not a failure. **Stop only** for that, for a genuine decision you + an advisor skill cannot confidently make, or for a permission the harness will not grant.
>
> **When blocked on the user:** write the action under `## Blocked on user`, commit, push, say so once, and stop **without any further tool calls** (no polling the remote, no status re-prints -- the goal hook treats tool use as progress and re-prompts every few seconds). Never poll for a human.

## Blocked on user

Nothing is parked.

### Self-advance (do this in the ship commit, every phase)

Leave `GOAL.md` pointed at the **next** phase, then run it:
- Update **"Shipped so far"** and strike the shipped row in the **Retarget table** (mark `SHIPPED`, add the commit range + one-line lesson).
- Rewrite the **`## Goal` heading + block** for the next phase: stage 1 is *Session start* plus that phase's premise (the "Re-verify in" column of spec section 3 names the facts each phase must settle); stage 2 names the start commit, implementer model (from the Retarget table), work-order template, and concrete deliverables with acceptance criteria, taken from that phase's row in spec section 9 and the spec sections the row points to; stage 3 is the gate with the reviewer model one tier above; stage 4 is the ship commit + retarget.
- Append anything you learned to **Lessons** (process traps only -- feature notes go in `docs/progress.md` and spec callouts).
- **Advance only within the approved roadmap.** If the next row in *Approved roadmap* exists, rewrite the `## Goal` block for it and continue. If it does not, the roadmap is exhausted: write the maintenance/park block below instead of inventing a phase.
- Any follow-on work the lanes surfaced goes under *Proposed (needs user approval)*, not into the approved roadmap. It runs only after the user approves it (which moves it up into *Approved roadmap*).
- **Next approved phase after Phase 1: Phase 2 (SSH layer).**

### Parking when the roadmap is exhausted

When no approved phase remains, replace the `## Goal` block with a park block: state that the approved roadmap is done, list the *Proposed* rows (each waiting on the user to approve or on an attended input), and set **Done when** to "the user has approved a proposed row (moved it into *Approved roadmap*) and rewritten this block into that phase, or decided none runs." Mark it **Blocked on user**. A run that lands here reads `## Blocked on user`, sees nothing approved changed, and stops after one turn with no tool call.

### Parking before Phase 9 (the attended queue)

Phase 9 is attended and so is everything between Phase 8 and it. When Phase 8 has shipped, do not start Phase 9. Write this list under `## Blocked on user`, in this order, naming the two working branches, and park:

1. Review this repo's working branch and merge it into `main` (a pull request is the usual way; CI runs on it).
2. Tag `v0.1.0` on `main` and push the tag; the release workflow publishes the binaries and `SHA256SUMS`.
3. Review the infra repo's working branch and merge it, following that repo's rules.
4. Deploy, following the infra repo's plan document: apply the edge configuration, converge the host and the target accounts.
5. Run the manual tests `test/manual/MT-001` to `MT-005` and record the results beside them. A failure is a bug report, not a reason for the session to deploy.
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

Added by the planning session (2026-11-18, revised 2026-11-19), specific to this build:

- **There is no `main` in this run.** Where a seed lesson or a generic work order says "merge to `main`" or "diff against `main`", read "ship commit" and "diff against the phase base". The changelog guard only fires on `main`, so nothing blocks a ship commit without a changelog entry: stage `CHANGELOG.md` with it anyway.
- **Only pushed commits are safe.** The machine can be reclaimed while the session is idle (a usage limit, a wait). Push the working branch after every green commit, and never leave a phase's work only on disk.
- **You will be compacted.** The files are the memory: at every phase start redo *Session start* and re-read the current `## Goal` block. Never rebuild the plan from what you recall.
- **Commit the three read-only lane reports before dispatching QA.** They are written uncommitted, and QA must fail a dirty tree.
- **This repo is public and the infra repo is private.** Nothing from the infra repo (host names, addresses, account or tenant ids, vault item names, anything in its plan documents) goes into a file, report or commit message here. Point at "the infra repo's plan document" instead of quoting it.
- **If `SendMessage` is not in a lane's tools, the report file and the lane's final text are the delivery.** Do not wait for a message that cannot arrive.
- **`gh pr` and `gh issue` fail in a cloud session** (GraphQL is rejected). Use REST: `gh api repos/<owner>/<name>/...`.
- **A missing tool is never a silent skip.** If a gate step cannot run in the sandbox, that is a finding: record it, move the step to CI with a callout in the spec, and make QA check the CI run.

Added by the build session (2026-11-19):

- **The harness can refuse a step this file asks for** (Phase 0: installing `openssh-server`). Do not retry it and do not reach the same end another way: take the fallback the spec already has, record the refusal with a callout, and tell the user once.
- **Wait for an agent with one bounded blocking command**, for example a shell loop that ends when the report file or the expected commit exists. Ending the turn to wait makes the goal hook re-prompt.
- **A lane's report reaches the lead as its final text and as the report file.** That is enough; do not wait for a `SendMessage`.
- **Resume the implementer for the fix commit** (`SendMessage` to its agent id with the triaged list) instead of briefing a new agent: it already knows the files.
- **The start commit cannot hold its own SHA.** Write the phase base into the ledger in the ship commit.
- **The sandbox runs as root.** A file mode does not stop root from reading or writing, so a test that needs an unwritable or unreadable file must use something that fails for root too.
- **The lead re-runs the gate itself after the fix commit** and reads CI for that commit. A second QA dispatch is only for a fix that changed behaviour the first QA run did not see.

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

Drafted by the planning session on 2026-11-18 from the owner's decisions (spec section 2). **The owner approves these rows by sending the kickoff prompt**; before that the owner may edit them freely. This is the ceiling: the session runs these and stops. One row per phase; strike and mark `SHIPPED` as they land. "Attended" means the lead should have the user available. Adding a row here requires the user's approval -- the lead may reorder these but never append a new one on its own. The orchestrator itself runs on fable.

| Phase | Where | Implementer -> Reviewers | Effort | Notes |
|---|---|---|---|---|
| ~~**0 Scaffold**~~ | this repo | sonnet -> opus | low | **SHIPPED**, `34d6001` .. the commit `docs(phase-0): ship scaffold`. Lesson: the first coverage step summed one copy of every block per test binary; the review lanes caught it by hand-counting statements, so have QA hand-derive every number a gate step prints |
| **1 Config, tenants, policy, audit** | this repo | opus -> fable | med | Spec 5.1 to 5.4 and `config check`. `look` only, `full` reserved, one tenant, tenant id everywhere, audit-before-act |
| **2 SSH layer** | this repo | opus -> fable | high | Spec 5.5. Pinned host keys, exec, signals, SFTP, the in-process test server and the real-`sshd` lane |
| **3 Jobs, store, outputs** | this repo | opus -> fable | high | Spec 5.6. Lifecycle, paging, limits, retention, restart recovery, SQLite |
| **4 Authenticator** | this repo | opus -> fable | med | Spec 5.7. Access token verification behind the interface, local issuer for tests |
| **5 MCP endpoint and tools** | this repo | opus -> fable | med | Spec 5.8, 5.9. The eight tools, stateless Streamable HTTP, `serve`, the fake edge |
| **6 End-to-end and hardening** | this repo | opus -> fable | high | Spec 6, 7. The real binary through a real MCP client; every scenario; fuzz run; each guarantee mapped to a test |
| **7 Packaging and operator docs** | this repo | sonnet -> opus | med | Spec 7, 8. `deploy/` examples, release build and workflow, operator guide, threat model, manual tests MT-001 to MT-005 (written, never run) |
| **8 Infra deployment support** | the infra repo, on the session's working branch there | opus -> fable | med | Spec 8, D17, and the infra repo's plan document. Prepared and statically validated under that repo's rules; never applied; no secret needed. Reports and the ledger stay in this repo and quote nothing private |
| **9 History scrub** | this repo | opus -> fable | low | **Attended.** Always the final row. `public-readiness` over tree + full history + commit messages; `git filter-repo --replace-text` with the redaction term list; force-push EVERY remote (internal mirrors included; the operator runs the force-pushes, the session cannot); re-clone. This repo has been public since its first commit, so a finding here is also an exposure to rotate, not only a line to rewrite |

**Batch option:** none. One phase at a time, in order, all in the one session.

### Proposed (needs user approval)

Follow-on work the review lanes surfaced mid-run. The lead writes rows here freely; the session does **not** run them. Each stays here until the user promotes it into *Approved roadmap*. When the approved roadmap is exhausted and this list is non-empty, the run parks (see *Parking when the roadmap is exhausted*) rather than promoting one itself.

| Proposed phase | Why (surfaced by) | Effort | Attended? |
|---|---|---|---|
| -- | -- | -- | -- |
