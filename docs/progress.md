# Progress

Living status doc. Read first, update at every phase boundary. Last updated: 2026-11-19.

## Current state

- Repo cut from `InfiniteRoomLabs/template-repo` and planned. No product code yet.
- The plan lives on the branch `feat/yasshm-mvp` here, and the deployment plan lives on a branch of that name in the operator's private infra repo. Nothing has been merged to `main`.
- The build is one cloud session that holds both repositories and runs the whole roadmap, with a fable orchestrator as lead. Each clone starts on its default branch; the session merges the plan branch into its own working branch at kickoff and does all its work there. A phase is a commit range on that branch (`GOAL.md`, *How a phase lands*).
- Planning artifacts are in place: the design spec (`docs/superpowers/specs/2026-11-18-yasshm-design.md`), `GOAL.md` (targeting Phase 0, with the kickoff prompt), the five work orders (`docs/phases/_templates/`), and the LikeC4 model of the planned system (every component is tagged `#planned` until its phase builds it).
- Toolchain: Go 1.27.4 and golangci-lint 2.15.0 are pinned in `mise.toml` next to the LikeC4 tools. `scripts/bootstrap.sh` installs everything on a bare machine. The gate (`mise run check`) runs the `arch` step only; Phase 0 adds the Go steps.
- CI runs the gate on pushes to every branch and on pull requests.
- No credentials are wired and none are needed: the build never deploys and never touches a secret (spec D16).
- Spec status: section 2 is locked from the owner's decisions; section 3 still has INFERRED and TODO rows, each with the phase that must settle it.

## Working branches

Filled in by the session at kickoff. Record this repo's working branch name here. For the infra repo write only "see the infra clone" (its branch name stays out of this public repo).

- This repo: not started
- Infra repo: not started

## Phase ledger

A phase is the commit range from its start commit (the phase base) to its ship commit.

| Phase | Status | Phase base .. ship commit | Notes |
|---|---|---|---|
| 0 Scaffold | not started | -- | Next |
| 1 Config, tenants, policy, audit | not started | -- | |
| 2 SSH layer | not started | -- | |
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
- 2026-11-19 (planning): a session with several repositories cannot pick a branch per repository, so the build cannot start on the plan branch. The plan was reworked: one session, one working branch per clone, no phase branches, no merges (spec F13, F19, F20, section 9).

## Next action

Send the kickoff prompt in `GOAL.md` (section *Kickoff*) to a new cloud session that has this repo and the infra repo attached, on the fable model.

## How to resume in a fresh or compacted session

1. Read this file, then `GOAL.md` (its *Session start* section first), then `CLAUDE.md`.
2. `git status --porcelain` must be empty and `git log --oneline -5` should match the ledger above. If not, reconcile before starting.
3. Read only the spec sections the current phase names.
4. Continue the goal from the current `## Goal` block.
