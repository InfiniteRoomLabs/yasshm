# GOAL.md -- autonomous build goal

One cloud session, one orchestrator, both repositories. It is started once with the kickoff prompt below and ships the approved roadmap one phase at a time through the pipeline: verify premise -> implement -> four-lane review gate -> ship -> retarget this file -> next phase. The design oracle is `docs/superpowers/specs/2026-11-18-yasshm-design.md` (the "spec"); `CLAUDE.md` holds the conventions and dispatch rules; `docs/progress.md` is where we are.

**Shipped so far:** every approved phase, 0 to 8. This repo holds the gateway (config, policy, audit, the SSH layer, the job engine, the authenticator, the MCP endpoint with its eight tools, `yasshm serve`), its tests (unit, integration against an in-process and a real `sshd`, end to end against the real binary), the deployment examples, the release workflow, the operator guide, the threat model and the manual tests. The infra clone holds the deployment support, prepared and statically checked, never applied. **The run is parked**: everything left is attended and listed under *Blocked on user*. Full roadmap in *Retarget* at the bottom.

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

## Goal (parked: the approved roadmap is shipped through Phase 8)

> **The approved roadmap is done.** Phases 0 to 8 are `SHIPPED` in the roadmap below. Phase 9 (the history scrub) is attended, and so is everything between Phase 8 and it: the session does not merge, tag, deploy, apply, run a manual test or rewrite history.
>
> **What waits on the user** is the ordered list under `## Blocked on user`. The rows under *Proposed (needs user approval)* wait on the user too; the session runs none of them until the user moves one into *Approved roadmap* and rewrites this block for it.
>
> **Done when:** the user has worked through the list under `## Blocked on user` and started Phase 9 with the operator present, or has approved a proposed row (moved it into *Approved roadmap*) and rewritten this block into that phase, or has decided that nothing more runs.
>
> A run that lands here reads `## Blocked on user`, sees that nothing approved has changed, and stops after one turn with no tool call.

## Blocked on user

**Parked on 2026-11-20 after Phase 8.** The session did none of the steps below and cannot: they need the operator. In this order:

0. **Repository settings, before the first tag.** Add a ruleset that limits creating, updating and deleting tags matching `v*` to the repository admin; protect `main` (no force push, no deletion, pull request required); turn on immutable releases if the repository offers it; switch on private vulnerability reporting (`SECURITY.md` relies on it). The release workflow's own check that a tag is on `main` prevents mistakes, not misuse.
1. **Review this repo's working branch `claude/serene-franklin-jv4pu7` and merge it into `main`** (a pull request is the usual way; CI runs on it). `docs/progress.md` has the ledger and what each review gate found; `docs/phases/<n>/reports/` has the reports.
2. **Tag `v0.1.0` on `main` and push the tag.** The release workflow publishes the two binaries, the unit, the example config and `SHA256SUMS`. It has never run: watch its first run.
3. **Review the infra repo's working branch (the infra clone's working branch) and merge it, following that repo's rules.** Its plan document holds the attended sequence, in order. Two decisions in it are yours before anything is switched on: where the first target lives (the gateway host with the containment the playbook implements, or a separate machine), and the audit of what the unprivileged account can read there (the playbook refuses to continue while a root-equivalent file is readable by others, and as that host is built today it will refuse until you change that).
4. **Deploy, following the infra repo's plan document:** apply the edge configuration, converge the host and the target account. The unit has only been checked with `systemd-analyze verify`; its first start under a real systemd is here, with the journal open.
5. **Run the manual tests `test/manual/MT-001` to `MT-005`** and record the results beside them. A failure is a bug report, not a reason for the session to deploy.
6. **Then start Phase 9 (the history scrub) with the operator present.**

For your information, no action needed to go on:

- One incident in a review lane's own tooling during Phase 5 sent test-only requests to a public host by mistake; nothing real was in them. `docs/progress.md` has the account (2026-11-20, Phase 5 gate).
- The real-`sshd` tests ran only in CI, because the build sandbox has no `sshd` and the session's permission layer refused the install. Adding `openssh-server` to the cloud environment's setup script lets a later session run them locally.
- Three rows wait under *Proposed (needs user approval)*: release provenance, a tighter unit after the first real start, and a Unix-socket listener.

### Self-advance (do this in the ship commit, every phase)

Leave `GOAL.md` pointed at the **next** phase, then run it:
- Update **"Shipped so far"** and strike the shipped row in the **Retarget table** (mark `SHIPPED`, add the commit range + one-line lesson).
- Rewrite the **`## Goal` heading + block** for the next phase: stage 1 is *Session start* plus that phase's premise (the "Re-verify in" column of spec section 3 names the facts each phase must settle); stage 2 names the start commit, implementer model (from the Retarget table), work-order template, and concrete deliverables with acceptance criteria, taken from that phase's row in spec section 9 and the spec sections the row points to; stage 3 is the gate with the reviewer model one tier above; stage 4 is the ship commit + retarget.
- Append anything you learned to **Lessons** (process traps only -- feature notes go in `docs/progress.md` and spec callouts).
- **Advance only within the approved roadmap.** If the next row in *Approved roadmap* exists, rewrite the `## Goal` block for it and continue. If it does not, the roadmap is exhausted: write the maintenance/park block below instead of inventing a phase.
- Any follow-on work the lanes surfaced goes under *Proposed (needs user approval)*, not into the approved roadmap. It runs only after the user approves it (which moves it up into *Approved roadmap*).
- **Next approved phase after Phase 8: none.** Phase 9 is attended: when Phase 8 has shipped, park as *Parking before Phase 9* says.

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
- **Do not accept "raise the timeout" until the timings say so.** In Phase 6 a test that failed once under load was put down to a 300 ms bound three times; the failing run's own timestamps (finished 8 ms after the deadline) ruled that out and pointed at a race in product code. Ask for the evidence of the failing run before approving a bound change.
- **A lane report is a document the gate may check.** In Phase 7 the new link check failed on a lane report the lead had just committed, and the head was red until the fix commit. Reports under `docs/phases/` are now outside the link check; still read a report for placeholder words and private text before committing it.
- **An agent that is polling CI looks idle.** Its transcript is quiet and no test process runs while a shell loop sleeps. Before dispatching lanes or running the gate, check for its `gh api` and `sleep` processes too, or wait for its final message.
- **Reports about a private repository are written twice.** In Phase 8 each lane gave its full findings to the lead as its final text and wrote a role-only copy for this public repo; the lead grepped each copy for private terms and read it before committing. The implementer wrote no file here at all; the lead wrote that summary.
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
| ~~**6 End-to-end and hardening**~~ | this repo | opus -> fable | high | **SHIPPED**, `ce5df4f` .. the commit `docs(phase-6): ship end-to-end and hardening`. Lesson: a whole-product security review found what five per-phase reviews had passed (a rejection class left outside a bound), and chasing one flaky test to its cause found a real race in the SSH stop sequence instead of the slow timer everyone had assumed; when an implementer says the planned fix would not fix the cause, let it stop and look again |
| ~~**7 Packaging and operator docs**~~ | this repo | sonnet -> opus | med | **SHIPPED**, `038cdc0` .. the commit `docs(phase-7): ship packaging and operator docs`. Lesson: every blocker was in a document an operator would copy (an account that could edit its own key file, a release path with no word on who may push tags, a manual test that would fail on a correct product); review documents as if they were code that runs on someone's host, and make the gate check them (links, tables against the code, samples from real output) |
| ~~**8 Infra deployment support**~~ | the infra repo, on the session's working branch there | opus -> fable | med | **SHIPPED**, see the infra clone for the range; here `4426f21` .. the commit `docs(phase-8): ship infra deployment support`. Prepared and statically validated; never applied; no secret read. Lesson: "unprivileged" is a property of the host, not of the account: an account in no group was still an administrator on the first target because of a setting outside the change, and a target account on the gateway host broke the product's own deployment assumption. Review what the account can reach on the real target before reviewing how it logs in |
| **9 History scrub** | this repo | opus -> fable | low | **Attended.** Always the final row. `public-readiness` over tree + full history + commit messages; `git filter-repo --replace-text` with the redaction term list; force-push EVERY remote (internal mirrors included; the operator runs the force-pushes, the session cannot); re-clone. This repo has been public since its first commit, so a finding here is also an exposure to rotate, not only a line to rewrite |

**Batch option:** none. One phase at a time, in order, all in the one session.

### Proposed (needs user approval)

Follow-on work the review lanes surfaced mid-run. The lead writes rows here freely; the session does **not** run them. Each stays here until the user promotes it into *Approved roadmap*. When the approved roadmap is exhausted and this list is non-empty, the run parks (see *Parking when the roadmap is exhausted*) rather than promoting one itself.

| Proposed phase | Why (surfaced by) | Effort | Attended? |
|---|---|---|---|
| Release provenance | Security lane, Phase 7: the release carries checksums but no proof of origin. Add build provenance attestations to the release job (a new pinned action, `id-token` and `attestations` permissions) and commit a `mise.lock` so tool downloads are checksum-pinned | low | no, but the action's commit SHA must be checked by someone who can read that repository |
| Tighter unit | Security lane, Phase 7: `PrivateUsers=yes` and dropping `AF_UNIX` from the unit, once the unit has started under a real systemd and MT-004 has passed | low | yes (needs the deployed host) |
| Unix-socket listener | Security lane, Phase 6: another local user could take the loopback port while the gateway is down; a socket in a directory only the service and the edge connector can reach closes that | med | no |
