# AGENTS.md -- yasshm (for Codex)

Read `~/.codex/AGENTS.md` (global) and `./CLAUDE.md` (this repo) first. `CLAUDE.md` is the authoritative brief and applies to you too.

## What this repo is

yasshm ("Yet Another SSH MCP") is a self-hosted MCP gateway that lets an AI assistant run policy-checked, audited commands on SSH hosts its operator has configured.

## Read-first

- `docs/progress.md` -- living status. Read before doing anything.
- `GOAL.md` -- the current autonomous phase goal + roadmap. Its *Session start* section and the hard limits apply to you as well.
- `docs/superpowers/specs/2026-11-18-yasshm-design.md` -- the design spec. Section 2 is locked.
- `docs/architecture/*.c4` -- the LikeC4 architecture model.
- `TESTING.md` -- the green rule. Non-negotiable.

## Harness differences that affect you

- The GOAL treadmill is driven by Claude Code's `/goal` Stop hook and the agent-ops `agency` plugin's hooks (changelog guard, version guard). Those are Claude-Code-only. You do not get them, so enforce the same rules by hand: conventional scoped commits, `CHANGELOG.md` updated on every merge into the integration branch `feat/yasshm-mvp` and on every `main` commit, never `--no-verify`.
- The `likec4` MCP server in `.mcp.json` is Claude-Code-only. Read `docs/architecture/*.c4` directly and use the CLI for everything else: `mise run arch:validate`, `mise run arch:gen`, `mise run arch:dev`. The rule is the same as for Claude: a structural change to the code updates the model in the same commit, and `mise run check` fails if the committed diagrams in `docs/architecture/generated/` are stale.
- This build has no secrets and needs none. Never deploy, never reach a real host, never read, echo, or log a secret value.

Keep this AGENTS.md and `CLAUDE.md` in sync.
