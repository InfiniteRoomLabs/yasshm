# Progress

Living status doc. Read first, update at every phase boundary. Last updated: 2026-11-18.

## Current state

- Repo cut from `InfiniteRoomLabs/template-repo` and planned. No product code yet.
- Integration branch: `feat/yasshm-mvp`. It exists under the same name in the operator's private infra repo. Nothing has been merged to `main`.
- Planning artifacts are in place: the design spec (`docs/superpowers/specs/2026-11-18-yasshm-design.md`), `GOAL.md` (targeting Phase 0), the five work orders (`docs/phases/_templates/`), and the LikeC4 model of the planned system (every component is tagged `#planned` until its phase builds it).
- Toolchain: Go 1.27.4 and golangci-lint 2.15.0 are pinned in `mise.toml` next to the LikeC4 tools. `scripts/bootstrap.sh` installs everything on a bare machine. The gate (`mise run check`) runs the `arch` step only; Phase 0 adds the Go steps.
- CI runs the gate on pushes to `main`, `feat/**` and `phase-*/**` and on pull requests.
- No credentials are wired and none are needed: the build loop never deploys and never touches a secret (spec D16).
- Spec status: section 2 is locked from the owner's decisions; section 3 still has INFERRED and TODO rows, each with the phase that must settle it.

## Phase ledger

| Phase | Status | Branch / merge | Notes |
|---|---|---|---|
| 0 Scaffold | not started | `phase-0/scaffold` | Next |
| 1 Config, tenants, policy, audit | not started | `phase-1/config-policy-audit` | |
| 2 SSH layer | not started | `phase-2/ssh-layer` | |
| 3 Jobs, store, outputs | not started | `phase-3/job-engine` | |
| 4 Authenticator | not started | `phase-4/authenticator` | |
| 5 MCP endpoint and tools | not started | `phase-5/mcp-tools` | |
| 6 End-to-end and hardening | not started | `phase-6/e2e-hardening` | |
| 7 Packaging and operator docs | not started | `phase-7/packaging-docs` | |
| 8 Infra deployment support | not started | `phase-8/infra-deploy` (infra repo) | Prepared and validated, never applied |
| 9 History scrub | not started | `phase-9/history-scrub` | Attended |

## Discoveries

- 2026-11-18 (planning): a cloud session with several repositories starts above the clones and loads no hooks or settings from either repo, so the toolchain is installed by `scripts/bootstrap.sh` at the start of every run, not by a session hook (spec F14).
- 2026-11-18 (planning): the planning sandbox had no `ssh` or `sshd` binary and no running Docker daemon. Phase 0 decides how the real-`sshd` test lane runs (spec F12, section 7).
- 2026-11-18 (planning): the edge may cut a request whose origin is silent for about 100 seconds, so the default ceiling for a synchronous wait is 60 seconds, not the client's 300 (spec F15, still INFERRED).

## Next action

Start a cloud session with this repo and the infra repo, both on `feat/yasshm-mvp`, and run `/goal complete everything in @yasshm/GOAL.md`.

## How to resume in a fresh session

1. Read this file, then `GOAL.md` (its *Session start* section first), then `CLAUDE.md`.
2. `git status --porcelain` must be empty and `git log --oneline -5` should match the ledger above. If not, reconcile before starting.
3. Read only the spec sections the current phase names.
4. Start the goal.
