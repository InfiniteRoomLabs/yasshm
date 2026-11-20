# yasshm

Yet Another SSH MCP: a self-hosted MCP gateway that lets an AI assistant run policy-checked, audited commands on SSH hosts you have configured.

MIT licensed. Built by [Infinite Room Labs](https://github.com/InfiniteRoomLabs).

## Status

**Version 0.1 is built. It has not been released or deployed.** The code, the tests and the install material are in this repository and the gate is green; no release has been cut, no gateway has run against a real edge or a real target, and the manual tests that would show that (`test/manual/`) have not been run. Treat it as release-candidate software until they have.

The binary has `serve --config <file>` (the gateway), `config check --config <file>` (strict validation of a config and the key files it names, without connecting anywhere) and `version`. The design is in [docs/superpowers/specs/2026-11-18-yasshm-design.md](docs/superpowers/specs/2026-11-18-yasshm-design.md), the roadmap in [GOAL.md](GOAL.md) and the current state in [docs/progress.md](docs/progress.md). The plan is on the `feat/yasshm-mvp` branch; the build happens on a `claude/...` working branch until the first release.

## Documentation

- [docs/operator-guide.md](docs/operator-guide.md): what yasshm is and is not, the config reference, adding a server, the audit log, running and upgrading, connecting a Claude custom connector, troubleshooting by error code.
- [deploy/README.md](deploy/README.md): the install steps (user, directories and modes, the binary and its checksum, the key, the target account, the unit, the edge), with [the unit](deploy/yasshm.service) and [an example config](deploy/config.example.yaml).
- [SECURITY.md](SECURITY.md): the threat model, what is and is not guaranteed and the test behind each guarantee, and how to report a vulnerability.
- [TESTING.md](TESTING.md): how the project is tested, and the operator's manual tests in [test/manual](test/manual).
- [The design spec](docs/superpowers/specs/2026-11-18-yasshm-design.md) and [the architecture diagrams](docs/architecture/README.md).

## What v0.1 does

- One static Go binary, run by systemd on a host you control, listening on loopback only.
- Reached through an identity-aware edge (Cloudflare Tunnel plus Access); yasshm verifies the edge's signed token on every request.
- Eight MCP tools over Streamable HTTP: `list_servers`, `run_command`, `read_output`, `list_commands`, `kill_command`, `get_server_info`, `upload_file`, `download_file`.
- Read-only by construction: commands run as an unprivileged account on the target, behind a denylist. Write access is designed in and switched off.
- Job control for commands that outlive a tool call, and an append-only audit log of everything asked and done.

The eight tools:

| Tool | Needs | What it does |
|---|---|---|
| `list_servers` | a signed-in caller | Lists your tenant's servers: id, description, tags, access level. |
| `get_server_info` | `look` | Probes a server (reachable, host key check, kernel, architecture, OS, uptime). |
| `run_command` | `look` | Runs a shell command as the server's unprivileged account, waits a bounded time, returns its status and first page of output. |
| `read_output` | `look` | Pages through a command's output. |
| `list_commands` | `look` | Lists commands, newest first. |
| `kill_command` | `look` | Sends TERM, INT or KILL to a running command. |
| `download_file` | `look` | Reads a regular file over SFTP, behind a path denylist. |
| `upload_file` | `full` | Refused in v0.1: no server can grant `full`. |

The denylist is a guard rail, not a sandbox: what a command can change is decided by the account it runs as. Read the security model in the spec (section 6) before pointing this at a machine you care about.

What v0.1 does not guarantee (spec section 6): that a command at `look` changes nothing (the target account decides that); that the denylist cannot be bypassed; that output is free of secrets (captured output is stored under the data directory and returned to the assistant as it is, so whatever a command prints from the target, a secret included, goes both places); availability under a request flood from a host that can reach the loopback listener (rejections are answered and their audit lines are bounded: at most 60 a minute for tokens without a verified signature and 60 for verified tokens that are not valid here, plus one summary line each; but nothing limits the request rate; the edge stops a flood from the internet); that text returned from a target is safe for a model to act on (a hostile target can put instructions in its output; the tool descriptions label output as untrusted); and that another local user cannot take the loopback port while the gateway is down and receive what the edge forwards (run it on a host with no untrusted local users; a Unix-socket listener is future work).

## Develop

```sh
git clone https://github.com/InfiniteRoomLabs/yasshm.git
cd yasshm
scripts/bootstrap.sh   # installs mise if needed, then the pinned toolchain
mise run check
```

## Quick start

This runs the gateway on your own machine, on loopback, with a made-up target. It shows the build, the config check and the health endpoint; it cannot show a tool call, because every request to `/mcp` must carry a Cloudflare Access token for your team domain and audience, which only the real edge issues. A tool call is exercised by the test suites (through a local token issuer and a stand-in edge) and, for real, by the manual tests.

You need `scripts/bootstrap.sh` run once (it installs `mise` and the pinned Go), and `ssh-keygen` for the throwaway keys.

```sh
mise run build                       # dist/yasshm_<version>_linux_{amd64,arm64} and dist/SHA256SUMS
B=$(ls dist/yasshm_*_linux_amd64)    # use the _arm64 file on an arm64 machine
$B version

T=/tmp/yasshm-try
mkdir -p $T/data $T/logs $T/keys && chmod 700 $T/data $T/logs
ssh-keygen -q -t ed25519 -N '' -f $T/keys/lab-look_ed25519
ssh-keygen -q -t ed25519 -N '' -f $T/fake-host                  # any public key line will do for a local try
sed -e "s|/etc/yasshm/keys|$T/keys|" -e "s|/var/lib/yasshm|$T/data|" -e "s|/var/log/yasshm|$T/logs|" \
    -e "s|\"ssh-ed25519 REPLACE_WITH_THE_TARGETS_HOST_KEY_LINE_FROM_ssh-keyscan_OR_THE_HOST\"|\"$(cut -d' ' -f1,2 $T/fake-host.pub)\"|" \
    deploy/config.example.yaml > $T/config.yaml
chmod 600 $T/config.yaml

$B config check --config $T/config.yaml     # config ok: 1 tenant, 1 server
$B serve --config $T/config.yaml 2>$T/serve.log &
curl -s http://127.0.0.1:8822/healthz       # see below
curl -s -o /dev/null -w '%{http_code}\n' -X POST http://127.0.0.1:8822/mcp   # 401: no token
kill -TERM %1                               # graceful stop; gateway.stopped is the last audit event
cat $T/serve.log $T/logs/audit.jsonl
```

With the example's made-up team domain the gateway cannot fetch Access's signing keys (it tries `https://example-team.cloudflareaccess.com/cdn-cgi/access/certs`), so `/healthz` answers `{"status":"degraded","detail":"key set not loaded","version":"..."}` with status 503 and `/mcp` refuses every request. With your real team domain and the Access application's AUD tag in the config, and a host that can reach the key set, `/healthz` answers `{"status":"ok","version":"..."}`. The version is what the binary was stamped with: the short commit for a `mise run build` binary, the tag for a release, `dev` for `go run`. To go on, follow [deploy/README.md](deploy/README.md).

## Architecture

The architecture is modelled as code with [LikeC4](https://likec4.dev) in `docs/architecture/`. The landscape view is rendered in [docs/architecture/README.md](docs/architecture/README.md); `mise run arch:dev` opens the interactive version. Components tagged `#planned` are designed but not built yet.

## Contributing

- Toolchain via [mise](https://mise.jdx.dev/): `scripts/bootstrap.sh` or `mise install`. Go and golangci-lint are pinned in `mise.toml`; Node and pnpm are there only for LikeC4.
- The gate: `mise run check` runs `fmt lint skipcheck test sshd e2e deploy docs cover vuln build arch` and fails on a dirty tree; `mise run check -- <step>` runs one step. The `e2e` step builds the real binary and runs the end-to-end suite (`test/e2e`) against it. The `deploy` step tests the unit and the example config and runs `systemd-analyze verify` where it exists; the `docs` step checks the Markdown links, ASCII and wrapping. `mise run build` writes `dist/yasshm_<version>_linux_{amd64,arm64}` and `dist/SHA256SUMS`. See `TESTING.md` for the testing rules.
- Commits: [Conventional Commits](https://www.conventionalcommits.org/), imperative mood, scoped.
- If you use Claude Code, install the [agent-ops marketplace](https://github.com/InfiniteRoomLabs/agent-ops) -- it provides the changelog guard that keeps `CHANGELOG.md` honest and the goal loop this repo is built with (`GOAL.md`).
- `scripts/redaction-check.sh` (a pre-commit hygiene check) is optional for outside contributors; it no-ops when `REDACTION_TERMS_RESOLVER` is not set.
- Security reports: see `SECURITY.md`.
