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
- `mise run build` is reproducible: the build date is `SOURCE_DATE_EPOCH` or the commit time, and a `VERSION` override is validated
- CI ensures `sshd` is installed on the runner and fails if it is not; `scripts/bootstrap.sh` and the gate report whether `sshd` is present locally (the real-sshd lane is CI-only, spec section 7)
- `internal/tenant`: the shared domain types (`TenantID`, `ServerID`, `CommandID`, `Principal`, `AccessLevel` ordered none < look < full, `Server` with `IdentityFor`, `Identity`, `PrincipalMatch`, `Policy`, `Limits`); `tenant.Permits` is the one place access levels are compared
- `internal/config`: strict loading of the YAML config (spec 5.1) with every problem reported in one pass as `path.to.key: message (line N)`, the limits table with defaults, allowed ranges and cross-field rules, `MaxTenants = 1`, and private key and known_hosts checks that never echo key material
- `internal/policy`: `Engine.Decide` with the `not_found`, `access_denied` and `policy_denied` checks in order, the built-in command and path denylists with a named rule per entry, and the operator's `deny_commands` and `deny_paths`
- `internal/audit`: the append-only JSONL audit log (`O_APPEND`, mode 0600, one write and one sync per event, UTC timestamps), a typed constructor for every event of spec 5.4, and `Logger.Before` for audit-before-act
- `yasshm config check --config <file>`: exit 0 when valid, 1 with the problem lines, 2 for a usage error
- Fuzz targets for the config decoder and the command and path guard rails; `internal/deprule` enforces the package dependency rule of spec section 4; `internal/testkit/configtest` writes synthetic configs for tests
- Dependencies: `go.yaml.in/yaml/v3` v3.0.6 and `golang.org/x/crypto` v0.59.0

### Changed

- `GOAL.md` targets Phase 1; `docs/progress.md` records Phase 0 as shipped
- A dirty working tree now fails the gate (`scripts/check.sh` exits 1) instead of printing a banner
- `TESTING.md`, `CLAUDE.md` and `README.md` describe the gate as it now is
- Template placeholders replaced with the project's name and description in `README.md`, `CLAUDE.md`, `AGENTS.md`, `TESTING.md`, `docs/progress.md` and the architecture workspace; the "Using this template" checklist is done and removed
- `CLAUDE.md` is now the yasshm rulebook: one working branch per repository with a phase as a commit range on it, the two-repository rules, the hard limits, and the cloud-session gotchas
- CI runs the gate on pushes to every branch, not only `main` and pull requests
