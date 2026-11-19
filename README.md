# yasshm

Yet Another SSH MCP: a self-hosted MCP gateway that lets an AI assistant run policy-checked, audited commands on SSH hosts you have configured.

MIT licensed. Built by [Infinite Room Labs](https://github.com/InfiniteRoomLabs).

## Status

Under construction. The binary has `version` and `config check --config <file>`, which validates a config file strictly (every problem reported at once, one line each) and checks its private key and known_hosts files without connecting anywhere. Built so far: the tenant model (`internal/tenant`), the config loader (`internal/config`), the policy engine with its command and path guard rails (`internal/policy`), the append-only audit log (`internal/audit`) the SSH layer (`internal/sshclient`: pinned host keys, commands stopped with the SSH signal request, SFTP, a fixed probe), tested against an in-process server and, in CI, a real OpenSSH `sshd`, and the job engine (`internal/jobs` with the SQLite record store `internal/store`: commands that outlive a call, paged output, kill, limits, recovery after a restart, retention). Authentication and the MCP endpoint come next, so there is still nothing to deploy. The design is in [docs/superpowers/specs/2026-11-18-yasshm-design.md](docs/superpowers/specs/2026-11-18-yasshm-design.md), the roadmap is in [GOAL.md](GOAL.md), and the current state is in [docs/progress.md](docs/progress.md). The plan is on the `feat/yasshm-mvp` branch; the build happens on a `claude/...` working branch until the first release.

## What v0.1 will be

- One static Go binary, run by systemd on a host you control, listening on loopback only.
- Reached through an identity-aware edge (Cloudflare Tunnel plus Access); yasshm verifies the edge's signed token on every request.
- Eight MCP tools over Streamable HTTP: `list_servers`, `run_command`, `read_output`, `list_commands`, `kill_command`, `get_server_info`, `upload_file`, `download_file`.
- Read-only by construction: commands run as an unprivileged account on the target, behind a denylist. Write access is designed in and switched off.
- Job control for commands that outlive a tool call, and an append-only audit log of everything asked and done.

The denylist is a guard rail, not a sandbox: what a command can change is decided by the account it runs as. Read the security model in the spec (section 6) before pointing this at a machine you care about.

## Develop

```sh
git clone https://github.com/InfiniteRoomLabs/yasshm.git
cd yasshm
scripts/bootstrap.sh   # installs mise if needed, then the pinned toolchain
mise run check
```

## Architecture

The architecture is modelled as code with [LikeC4](https://likec4.dev) in `docs/architecture/`. The landscape view is rendered in [docs/architecture/README.md](docs/architecture/README.md); `mise run arch:dev` opens the interactive version. Components tagged `#planned` are designed but not built yet.

## Contributing

- Toolchain via [mise](https://mise.jdx.dev/): `scripts/bootstrap.sh` or `mise install`. Go and golangci-lint are pinned in `mise.toml`; Node and pnpm are there only for LikeC4.
- The gate: `mise run check` runs `fmt lint skipcheck test sshd cover vuln build arch` and fails on a dirty tree; `mise run check -- <step>` runs one step. `mise run build` writes `dist/yasshm_<version>_linux_{amd64,arm64}` and `dist/SHA256SUMS`. See `TESTING.md` for the testing rules.
- Commits: [Conventional Commits](https://www.conventionalcommits.org/), imperative mood, scoped.
- If you use Claude Code, install the [agent-ops marketplace](https://github.com/InfiniteRoomLabs/agent-ops) -- it provides the changelog guard that keeps `CHANGELOG.md` honest and the goal loop this repo is built with (`GOAL.md`).
- `scripts/redaction-check.sh` (a pre-commit hygiene check) is optional for outside contributors; it no-ops when `REDACTION_TERMS_RESOLVER` is not set.
- Security reports: see `SECURITY.md`.
