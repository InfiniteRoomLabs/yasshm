# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

- Design spec for v0.1 (`docs/superpowers/specs/2026-11-18-yasshm-design.md`): locked decisions, verified external facts, component design, security model, testing and release design, deployment contract, and the phase plan
- `GOAL.md` targeting Phase 0, with a ten-row roadmap, the kickoff prompt and session-start checklist for one orchestrating cloud session that holds two repositories, and the hard limits of the build (no deploy, no real host, no secret)
- Phase work orders for the four-lane review gate (`docs/phases/_templates/{implementer,code-review,simplify,security,qa}.md`)
- LikeC4 model of the planned gateway (`docs/architecture/`): the MCP client, the identity-aware edge, the SSH targets, and the ten yasshm components and datastores, each tagged `#planned` until its phase builds it
- `scripts/bootstrap.sh`: installs mise and the pinned toolchain on a bare machine; idempotent
- Go 1.27.4 and golangci-lint 2.15.0 pinned in `mise.toml`

### Changed

- Template placeholders replaced with the project's name and description in `README.md`, `CLAUDE.md`, `AGENTS.md`, `TESTING.md`, `docs/progress.md` and the architecture workspace; the "Using this template" checklist is done and removed
- `CLAUDE.md` is now the yasshm rulebook: one working branch per repository with a phase as a commit range on it, the two-repository rules, the hard limits, and the cloud-session gotchas
- CI runs the gate on pushes to every branch, not only `main` and pull requests
