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
- Go module `github.com/InfiniteRoomLabs/yasshm` with `cmd/yasshm` with `version` and `help` subcommands (version, commit and build date injected at build time); standard library only
- The full gate in `scripts/check.sh`: `fmt`, `lint` (`.golangci.yml`), `skipcheck`, `test` (`-race -shuffle=on`, coverage profile), `cover` (floor 85 percent, `internal/testkit` excluded), `vuln` (govulncheck pinned in `mise.toml`), `build`, `arch`
- `mise run build` (`scripts/build.sh`: linux amd64 and arm64, `CGO_ENABLED=0`, `-trimpath`, `dist/SHA256SUMS`), `mise run test`, `mise run lint`, `mise run fuzz` (`scripts/fuzz.sh`)
- CI ensures `sshd` is installed on the runner and fails if it is not; `scripts/bootstrap.sh` and the gate report whether `sshd` is present locally (the real-sshd lane is CI-only, spec section 7)

### Changed

- A dirty working tree now fails the gate (`scripts/check.sh` exits 1) instead of printing a banner
- `TESTING.md`, `CLAUDE.md` and `README.md` describe the gate as it now is
- Template placeholders replaced with the project's name and description in `README.md`, `CLAUDE.md`, `AGENTS.md`, `TESTING.md`, `docs/progress.md` and the architecture workspace; the "Using this template" checklist is done and removed
- `CLAUDE.md` is now the yasshm rulebook: one working branch per repository with a phase as a commit range on it, the two-repository rules, the hard limits, and the cloud-session gotchas
- CI runs the gate on pushes to every branch, not only `main` and pull requests
