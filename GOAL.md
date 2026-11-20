# GOAL.md -- autonomous build goal

One cloud session, one orchestrator, both repositories. It is started once with the kickoff prompt below and ships the approved roadmap one phase at a time through the pipeline: verify premise -> implement -> four-lane review gate -> ship -> retarget this file -> next phase. The design oracle is `docs/superpowers/specs/2026-11-18-yasshm-design.md` (the "spec"); `CLAUDE.md` holds the conventions and dispatch rules; `docs/progress.md` is where we are.

**Shipped so far:** Phase 0 (Scaffold): the Go module with `version`, the full gate, the `mise` tasks, CI with `sshd` on the runner, and the sandbox facts settled. Phase 1 (Config, tenants, policy, audit): `internal/tenant`, `internal/config`, `internal/policy`, `internal/audit`, `internal/keyfile` and `yasshm config check`. Phase 2 (SSH layer): `internal/sshclient`, `internal/hostkey`, the in-process and real-`sshd` test servers and the contract suite. Phase 3 (Jobs, store, outputs): `internal/store` (SQLite) and `internal/jobs` (the engine, the output files, the janitor). Phase 4 (Authenticator): `internal/auth` (Access token verification, the key set, the guard middleware) and the local issuer test kit. Phase 5 (MCP endpoint and tools): `internal/mcpserver` (the eight tools on stateless Streamable HTTP, `/healthz`), `yasshm serve` and the fake edge. Next target: **Phase 6 (End-to-end and hardening)**. Full roadmap in *Retarget* at the bottom.

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

## Goal (current target: Phase 6 -- End-to-end and hardening)

> **Goal -- ship Phase 6 (End-to-end and hardening), then go on to Phase 7:**
>
> 1. **Verify the premise before building.** Do *Session start* above. Read spec sections 2, 5.8, 5.9, 6 and 7 (the end-to-end scenario list, fuzzing, test names), `TESTING.md` and `CLAUDE.md`. Confirm, and record each result in spec section 3 and `docs/progress.md`:
>    - `mise run check` prints `check.sh: all OK` on the clean tree you start from, and CI is green on the head.
>    - The real binary can be built once per test run and started on a random loopback port with a generated config, the local issuer (`accesstest`), the in-process SSH server (`sshtest`) and the fake edge (`edgetest`), from a test, in the sandbox (as root) and in CI (as a normal user).
>    - Anything wrong: fix the spec with a `> **STATE AS OF YYYY-MM-DD**` callout in the affected section, note it in `docs/progress.md`, and continue.
> 2. **Implement.** Make the start commit `docs(phase-6): start end-to-end and hardening` and record the phase base. Dispatch ONE implementer (`general-purpose`, `model: "opus"`, work order from `docs/phases/_templates/implementer.md`, pointers to spec sections 5.8, 5.9, 6 and 7). Deliverables, each with an acceptance criterion:
>    - `test/e2e`: the suite builds the real binary once (`CGO_ENABLED=0`, the same flags as `scripts/build.sh`), writes a config and keys into a temp directory, starts `yasshm serve` as a child process on a random loopback port, and drives it with the MCP Go SDK's client through the fake edge. No test in it calls an internal package's logic instead of the binary.
>    - Every scenario of spec section 7 exists and passes against the real binary, each named with its path class: every tool through the fake edge; no token, a bad token and an unmapped principal; two tenants cannot see each other's servers, commands or output (the binary accepts one tenant from YAML, so this scenario needs a stated, tested way to start it with two -- a test-only build or flag is not allowed in the shipped binary; if the loader's one-tenant rule makes it impossible against the real binary, the scenario runs against the real handler wiring in-process and the spec says so in a callout); a command that outlives `wait_seconds`, is paged and is killed; a command that times out; output beyond `max_output_bytes`; a `busy` rejection; a denylisted command; a host key mismatch; `upload_file` denied; an unwritable audit log blocks the action; the gateway is killed mid-command with `SIGKILL` and the restarted gateway reports `lost`; graceful shutdown on `SIGTERM` (running commands end `killed`, exit code 0, `gateway.stopped` written); `config check` on valid and invalid files; `/healthz`; a second gateway on the same data directory refuses to start.
>    - What Phase 5 left for this one: the test support that `internal/mcpserver` tests copied from `internal/jobs` moves into `internal/testkit` once (simplification item 6 of the Phase 5 gate); the policy engine's cost on a command of the maximum allowed size is measured and, if it can be cut without changing a decision, cut; the SDK reads an environment variable at start that can crash the process when malformed, which Phase 7's unit and guide must cover; the full-disk behaviour is tried against a size-limited directory if the runner allows one without privileges.
>    - Drive the binary only with Go clients bound to loopback addresses. No `curl`, no shell globbing near a URL: in the Phase 5 gate a test script sent requests to a public host by accident.
>    - Each guarantee of spec section 6 names the test that proves it: a table in the spec (section 6 or a callout under it) maps every "guarantees v0.1 makes" sentence to at least one test by name, and a test fails if a named test does not exist.
>    - Leak checks around the binary: after each scenario and after shutdown, the child process is gone, no process started by the test SSH server is left, the number of open file descriptors of the gateway (read from `/proc/<pid>/fd` while it runs) returns to its idle value after a burst of calls, and goroutine counts in the in-process suites stay flat.
>    - `mise run fuzz` is run for every target (30 seconds each) and any crasher is fixed and committed as a seed; the result is in the report.
>    - A review of spec section 6 against the code, written into the implementer report: for each guarantee and each "not guaranteed" item, where the code enforces or documents it; anything found is fixed in this phase or listed for the lead.
>    - The gate runs the end-to-end suite (a step `e2e` after `sshd`, or inside `test` if it stays fast; say which and keep `usage lint` clean); its wall time is reported; it does not need the network beyond loopback and passes as root and as a normal user.
>    - Coverage: the end-to-end tests do not count toward the floor unless the binary is built with coverage and merged (not required); the floor stays 85 on the unit and integration tests.
>    - No new dependency. `README.md`, `TESTING.md` and `CLAUDE.md` gotchas updated; `CHANGELOG.md` has the `[Unreleased]` entries. No architecture change is expected; if the model is wrong anywhere, it is corrected here.
>    - Acceptance: `mise run check` green on a clean tree with coverage at or above 85, the end-to-end suite green twenty times in a row, and CI green on the pushed head.
> 3. **Four-lane gate** -- dispatch code-review, simplification, and security lanes in parallel (read-only), then the QA lane (the only lane that runs the gate); all `general-purpose`, **`model: "fable"`**. Work orders from `docs/phases/_templates/`. Reports to `docs/phases/6/reports/<lane>.md`; a lane's final text is its verdict, its finding count and that path. Wait for each lane's completion notice, then commit the three read-only reports before dispatching QA. The security lane reviews the whole product against section 6, not only this phase's diff. QA gets a budget (at most eight mutations of product code that an end-to-end scenario must catch, at most twenty repeat runs). Triage, ONE fix commit `fix(e2e): apply the review-gate findings`, re-run the gate, confirm `git status --porcelain` is empty.
> 4. **Ship** -- make the ship commit `docs(phase-6): ship end-to-end and hardening` (body summarises gate results) with `CHANGELOG.md`, `docs/progress.md` and **this file retargeted to Phase 7** (see *Self-advance*); push the working branch; confirm CI green on the ship commit. Then start Phase 7 from *Session start*.
>
> **Phase 6 is done when:** the deliverables above are met, CI green, the gate green locally on a clean tree, `docs/progress.md` + `CHANGELOG.md` updated, **and `GOAL.md` retargeted to Phase 7 and pushed.**
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
- **Next approved phase after Phase 6: Phase 7 (Packaging and operator docs).**

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
- **Commit a lane's report only after the lane's completion notice.** A lane can rewrite its file after the first copy appears; a report committed early leaves the tree dirty under QA's gate run (Phase 1). The report file existing is not the notice: in Phase 2 the QA lane trimmed its file after the lead had committed it. Wait for the lane's final message, then commit.
- **Ask a lane for a short final text** (verdict, finding count, report path). The lead reads the report file; the same report again as a message doubles the cost.
- **Ask QA for mutations.** Changing a bound, deleting a check or removing a lock and seeing whether a test fails found tests that could not fail (Phase 1). Name the invariants worth mutating in the QA work order.
- **Probe a fix that changes matching or parsing.** The Phase 1 fix commit passed the gate and still opened a bypass the first review never saw, because the fix was new code. After a fix commit that changes how input is matched, the lead writes a throwaway test with hostile inputs, runs it, deletes it, and sends failures back as a second fix commit. "ONE fix commit" is the aim, not a reason to ship a known hole.
- **A CI-only test lane works if it is proven first.** In Phase 2 the implementer's first pushed commit was the smallest real-`sshd` test, so the loop (push, read the job log over REST) was known to work before anything depended on it; the whole real-`sshd` half then took two round trips.
- **A container restart stops every agent but keeps the disk.** In Phase 3 the machine restarted mid-phase: the clones, the pushed commits and the scratchpad were intact, and the implementer was resumed from its transcript with `SendMessage` and a note of what the disk held. Check `git status` and the remote head first; do not start a new agent before trying the resume.
- **Give QA a budget.** The Phase 3 QA lane ran 85 mutations and took 55 minutes. Name at most eight mutations and at most twenty repeat runs in the work order, and say which invariants matter.
- **A flake seen once is a finding.** The lead's re-gate failed once and passed afterwards; reading the test showed a real race (two janitor passes). Do not ship on "it passed the second time": find the cause or have the implementer prove there is none.
- **A test that simulates "nothing listens here" must hold the port.** Closing a listener frees the address, and another test or process can take it at once (Phase 4). The test kit has `loopback.RefusingPort` and a test issuer that stays bound while down; use them.
- **CI can fail on a commit that changed only documents.** The Phase 3 ship commit went red on a real-`sshd` scenario that had passed a dozen times (a signal lost while bash was replacing itself). Read the job log before assuming a flake is harmless, and fix the cause before starting the next phase.
- **Dispatch the review lanes only after the implementer's completion notice too.** In Phase 5 the implementer was still running its last gate when the lanes started writing report files, and its gate failed on the dirty tree.
- **A lane that drives the binary by hand can leave loopback by accident.** The Phase 5 QA lane ran `curl` with an unquoted `*` from a directory of scratch files; the shell turned file names into URLs and one was a real public domain. Nothing real was sent, but the rule "nothing beyond loopback" was broken once. Work orders now say: Go clients bound to loopback only, a private empty working directory per lane, no `curl`.
- **Re-gate only after the implementer's completion notice.** In Phase 2 the lead started its own gate as soon as the fix commit appeared, while the implementer was still running gates of its own; one of the implementer's runs then measured 68.1 percent coverage with a package's profile missing. Two gates in one tree collide on `coverage.out`, whoever runs them.
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
| ~~**1 Config, tenants, policy, audit**~~ | this repo | opus -> fable | med | **SHIPPED**, `6cc1f31` .. the commit `docs(phase-1): ship config, tenants, policy, audit`. Lesson: the blockers were all fail-open paths on input nobody had tried (a null value, a zero value, a certificate-authority line), and QA found tests that could not fail by mutating the code; ask every QA lane for mutations |
| ~~**2 SSH layer**~~ | this repo | opus -> fable | high | **SHIPPED**, `2300f0e` .. the commit `docs(phase-2): ship ssh layer`. Lesson: every blocker was a behaviour no test exercised (a server that stops answering, a FIFO, a host key of another type), and QA found them by building the hostile peer; have the implementer build fault switches for "the other side goes silent" from the start |
| ~~**3 Jobs, store, outputs**~~ | this repo | opus -> fable | high | **SHIPPED**, `4886053` .. the commit `docs(phase-3): ship jobs, store, outputs`. Lesson: QA ran the unmutated tests fifty times and found the gate was flaky (7 of 50) because the engine reported a command finished before it had released its slot; and the fix for the quota added a background pass that raced a test. Repeat-run every concurrent package in QA and again after the fix commit |
| ~~**4 Authenticator**~~ | this repo | opus -> fable | med | **SHIPPED**, `64101fd` .. the commit `docs(phase-4): ship authenticator`. Lesson: the review found no blocker, and the lead's repeat runs still found two flaky tests (a test server that freed its port while "down", and an engine that dropped a live handle before freeing its slot). Repeat-run the whole module after every fix commit, not only the phase's packages |
| ~~**5 MCP endpoint and tools**~~ | this repo | opus -> fable | med | **SHIPPED**, `cf21613` .. the commit `docs(phase-5): ship mcp endpoint and tools`. Lesson: both blockers lived where the SDK's own test client is lenient (it does not validate a result against the output schema, and it never sends a batch), so the suite was green while a real client would have failed; review the wire behaviour against the protocol text and against what other clients do, not only against the client used in tests |
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
