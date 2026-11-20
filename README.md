# yasshm

Yet Another SSH MCP: a self-hosted MCP gateway that lets an AI assistant run policy-checked, audited commands on SSH hosts you have configured.

MIT licensed. Built by [Infinite Room Labs](https://github.com/InfiniteRoomLabs).

## Status

Under construction. The binary has `serve --config <file>`, which serves the eight MCP tools over Streamable HTTP on a loopback address behind the origin's token check, `version` and `config check --config <file>`, which validates a config file strictly (every problem reported at once, one line each) and checks its private key and known_hosts files without connecting anywhere. Built so far: the tenant model (`internal/tenant`), the config loader (`internal/config`), the policy engine with its command and path guard rails (`internal/policy`), the append-only audit log (`internal/audit`), the SSH layer (`internal/sshclient`: pinned host keys, commands stopped with the SSH signal request, SFTP, a fixed probe), tested against an in-process server and, in CI, a real OpenSSH `sshd`, and the job engine (`internal/jobs` with the SQLite record store `internal/store`: commands that outlive a call, paged output, kill, limits, recovery after a restart, retention), and the authenticator (`internal/auth`: verifies the Cloudflare Access token on every request against a cached, rate-limited key set and maps it to a tenant), tested against a local token issuer, and the MCP endpoint (`internal/mcpserver`: the eight tools, `/mcp` and `/healthz`), tested through the SDK's own client and a stand-in edge. End-to-end tests and the deployment examples come next, so there is still nothing to deploy. The design is in [docs/superpowers/specs/2026-11-18-yasshm-design.md](docs/superpowers/specs/2026-11-18-yasshm-design.md), the roadmap is in [GOAL.md](GOAL.md), and the current state is in [docs/progress.md](docs/progress.md). The plan is on the `feat/yasshm-mvp` branch; the build happens on a `claude/...` working branch until the first release.

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

## Run it locally

`serve` needs a config whose `listen` is a loopback address, an existing data directory with mode 0700 and an audit log path, all owned by the user running it. Every request to `/mcp` must carry a Cloudflare Access token for the configured team domain and audience, so a local smoke test can only reach `/healthz`; the test suites drive the tools through a local token issuer (`internal/testkit/accesstest`) and a stand-in edge (`internal/testkit/edgetest`). A sketch with synthetic values only:

```sh
install -d -m 0700 /tmp/yasshm-data
# The version is what the binary was stamped with: the short commit for a
# `mise run build` binary (for example "1d3b63c"), "dev" for `go run`.
# config.yaml as in spec section 5.1, with
#   listen: 127.0.0.1:8822, data_dir: /tmp/yasshm-data, audit_log: /tmp/yasshm-audit.jsonl
yasshm config check --config config.yaml
yasshm serve --config config.yaml 2>serve.log &
curl -s 'http://127.0.0.1:8822/healthz'   # {"status":"ok","version":"<the build's version>"} once the key set is loaded
curl -s -o /dev/null -w '%{http_code}\n' -X POST 'http://127.0.0.1:8822/mcp'   # 401: no token
kill -TERM %1                            # graceful: gateway.stopped in the audit log
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
