# yasshm v0.1 design

**Date:** 2026-11-18
**Status:** Written by the planning session at the owner's direction, from decisions the owner made in that session. The owner signs it off by sending the kickoff prompt in `GOAL.md`; until then the owner may edit any row. Revised 2026-11-19: the build is one orchestrating cloud session that works on one branch per repository (section 9).
**How to read it:** section 2 is locked (do not re-litigate). Sections 4 to 8 are the build oracle. Section 9 is the process. `GOAL.md` names the sections each phase needs; read only those.

yasshm ("Yet Another SSH MCP") is a self-hosted MCP gateway that lets an AI assistant run policy-checked, audited commands on SSH hosts its operator has configured.

## 1. Purpose

**The problem.** The operator wants to hand an assistant real work on their own machines from a phone or a browser, without giving it a shell, a private key or an open inbound port.

**The product.** One static Go binary, run by systemd on a Linux host the operator controls. It serves eight MCP tools over Streamable HTTP. Each request is authenticated at an identity-aware edge and verified again at the origin, checked against policy, and written to an append-only audit log before anything runs. Commands that outlive a tool call become jobs the assistant can poll, page and kill.

**In v0.1:** the eight tools (section 5.8), one tenant, access level `look` (read-only by construction of the target account), job control, SQLite state, JSONL audit, verification of Cloudflare Access tokens, a hardened systemd unit and an example config.

**Not in v0.1:** see D18 and section 10.

**Why build rather than adopt.** The owner decided to build; the name admits the prior art. The gap as understood at planning time (INFERRED, F16): the SSH MCP servers that were looked at are local stdio servers that run beside the client with the user's own keys, which cannot serve a phone or web client, and none combined remote Streamable HTTP, origin-side identity verification, server-side-only config, job control, tenant-scoped state and an audit log. Phase 0 re-checks this in a 15-minute time box. Only if it finds a maintained project that covers every row of section 2 whose source is `owner` does the lead park with a `Blocked on user` note; otherwise it records what it found here and continues.

> **STATE AS OF 2026-11-19** -- The Phase 0 survey (15-minute box, README text only) found no full match, so the build continues. Remote HTTP transports now exist in several SSH MCP servers, so the planning-time claim that they are all local stdio servers is out of date: `gelse/ssh-mcp` serves Streamable HTTP with API keys, a JSON config file and a JSONL audit log, and its README lists the lack of OAuth as a gap; `tufantunc/ssh-mcp` has roles, a read-only profile, background sessions and an audit log, with stdio as its default transport. What none of the projects read has is verification of the edge's identity token at the origin, together with job control, tenant-scoped SQLite state and a fixed eight-tool surface. Whether each project is maintained was not verified.

## 2. Decisions locked (do not re-litigate)

Source `owner` = the owner decided it. Source `planning` = the planning session chose it to satisfy an owner decision; the owner can overturn it before kickoff, the loop cannot.

| # | Decision | Choice | Why | Source |
|---|---|---|---|---|
| D1 | Build | yasshm is its own project | See section 1 | owner |
| D2 | Shape | One static Go binary (CGO off) run by systemd on a host. Not in Kubernetes | The gateway is a way to reach and repair the things it manages, so it must not depend on them. One file to install | owner |
| D3 | Ingress | Public HTTPS through an identity-aware edge: Cloudflare Tunnel plus Access, with Access-managed OAuth. yasshm listens on loopback only and refuses a non-loopback address unless the config says `allow_non_loopback: true` | No open inbound port. The edge does the OAuth dance, so yasshm ships no OAuth server | owner |
| D4 | Origin auth | yasshm verifies the Access application token on every request (signature, issuer, audience, expiry) behind an `Authenticator` interface. There is no unauthenticated mode | An edge can be misconfigured or bypassed from the host itself; "the header is present" is not authentication. The interface lets a second provider be added later | owner (edge), planning (interface, no unauthenticated mode) |
| D5 | Access level | `look` only in v0.1. `full` is a reserved value that the config loader rejects with "not supported in this version". Every tool declares the level it requires and the policy engine compares that with the level the server grants, so enabling `full` later is one validation rule plus tests | Read-only now, an easy path to more later | owner |
| D6 | What `look` means | Commands run as an unprivileged account on the target. The gateway adds guard rails: a command denylist, a path denylist, no PTY, no forwarding, `upload_file` refused. It is not a sandbox, and the docs say so | A gateway cannot prove that a shell command is read-only. The OS account is the boundary | planning |
| D7 | Tenancy | One tenant in v0.1, multi-tenant-ready: the config has a `tenants` list (exactly one accepted), every domain object, store row, output path and audit event carries a tenant id, and every query filters by it. Isolation is tested with two tenants built in test code | Multi-tenant must be an add-on, not a rewrite | owner |
| D8 | Config | One YAML file on the gateway host, read at start, strictly validated (an unknown key is an error). Nothing in it is readable or writable through MCP: tools expose server ids, descriptions, tags and access levels only | The model must not learn hosts, users or key paths, and must not be able to change its own permissions | owner |
| D9 | Tools | Exactly eight: `list_servers`, `run_command`, `read_output`, `list_commands`, `kill_command`, `get_server_info`, `upload_file`, `download_file` | The trimmed v0.1 scope | owner |
| D10 | Long commands | Job control. `run_command` waits a bounded time and then returns a command id; `read_output` pages; `kill_command` stops | A tool call has a time limit and a result has a size limit (F3, F15) | owner (job control), planning (numbers) |
| D11 | State | SQLite through a pure-Go driver (no CGO) for command records. Captured output in files under the data directory. No other datastore | Owner chose SQLite. Pure Go keeps the binary static and the tests hermetic | owner, planning (driver constraint) |
| D12 | Audit | Append-only JSONL file. Audit-before-act: if the "requested" event cannot be written, the action does not happen | Owner chose JSONL. Failing closed is the only safe reading of "audited" | owner, planning (fail closed) |
| D13 | SSH trust | Host keys are pinned in config. No trust on first use. Key-based authentication only. One connection per command, no pooling | A gateway that accepts an unknown host key can be pointed at an impostor | planning |
| D14 | Dependencies | Small and well known: the official MCP Go SDK, `golang.org/x/crypto`, an SFTP library, one pure-Go SQLite driver, one YAML library, one maintained JWT or OIDC library (token verification is never hand-rolled). Anything else needs a justification line in the implementer report and a clean vulnerability scan | Supply chain is part of the attack surface of a tool that holds SSH keys | planning |
| D15 | Tests | Every test that can run without the real edge or a real target is automated and in the gate: unit, integration (in-process SSH server, a real `sshd` process, a local token issuer, real SQLite), end to end (the real binary driven by a real MCP client through a fake edge). Coverage floor 85 percent | Owner: every unit, integration and end-to-end test that is possible to implement | owner, planning (floor) |
| D16 | No live deploy in the loop | The build loop never deploys, never reaches a real target or the real edge, and never needs a secret. Live checks are operator manual tests (`test/manual/MT-*.md`) run after an attended deployment | The build session has no access to the target and no password-manager login | owner |
| D17 | Infra changes | Deployment support lives in the operator's private infra repository, on the build session's working branch there. The session prepares it and validates it statically. It never applies it | Owner | owner |
| D18 | Cut from v0.1 | Tenant self-service, message queue, object storage, web UI, Kubernetes packaging, dashboards, interactive sessions, SSH certificates, a metrics endpoint | The trimmed plan. Section 10 lists what may come back | owner |

## 3. Verified external facts

Status is CONFIRMED (read in a primary source or observed, on the date shown), INFERRED (believed, not yet checked against a primary source) or TODO. When reality disagrees with a row, reality wins: fix the row and add a `> **STATE AS OF YYYY-MM-DD**` callout in the section that depended on it, in the same commit.

| # | Fact | Status | Source, checked 2026-11-18 | Re-verify in |
|---|---|---|---|---|
| F1 | Claude custom connectors talk to remote MCP servers over Streamable HTTP; the older SSE transport is deprecated. Calls come from the vendor's cloud, so the server needs a public URL | CONFIRMED | `https://claude.com/docs/connectors/building` | Phase 5 |
| F2 | Connector sign-in is OAuth; dynamic client registration is supported; the OAuth callback is `https://claude.ai/api/mcp/auth_callback` | CONFIRMED | same page | Phase 7 (operator guide) |
| F3 | A tool call times out after 300 seconds in the Claude apps, and a tool result is capped at about 150,000 characters | CONFIRMED | same page | Phase 5 |
| F4 | The official Go SDK is `github.com/modelcontextprotocol/go-sdk`, v1.7.0 at planning time, with packages `mcp`, `auth`, `jsonrpc`, `oauthex`; servers are built with `mcp.NewServer` and typed `mcp.AddTool`; it speaks protocol revisions 2024-11-05 through 2026-07-28 | CONFIRMED | the repository's release notes and package docs | Phase 5 |
| F5 | The SDK's Streamable HTTP handler has a stateless mode (`StreamableHTTPOptions.Stateless`), and revision 2026-07-28 is accepted over Streamable HTTP only in that mode | CONFIRMED | v1.7.0 release notes | Phase 5 |
| F6 | How a per-request verified identity reaches a tool handler in the SDK (request context, `RequestExtra`, or a per-request server) | TODO | -- | Phase 5 premise |
| F7 | Cloudflare Access sends the origin an application token that is a JWT signed with RS256 and carrying a `kid`. Identity tokens carry `aud` (the application's AUD tag), `email`, `sub`, `iss` (the team domain URL), `exp`, `iat`, `nbf`, `type` (`app` or `org`). Service tokens carry `common_name` (the client id) and an empty `sub`. The origin must validate the token; the presence of a header proves nothing | CONFIRMED | `https://developers.cloudflare.com/cloudflare-one/access-controls/applications/http-apps/authorization-cookie/application-token/` | Phase 4 |
| F8 | The token arrives in the request header `Cf-Access-Jwt-Assertion`; the signing keys are a JWKS at `https://<team-name>.cloudflareaccess.com/cdn-cgi/access/certs`; keys rotate, so an unknown `kid` means "refetch" | CONFIRMED (header, URL), INFERRED (rotation cadence) | the "Validate JWTs" page in the same docs section | Phase 4 |
| F9 | Access-managed OAuth can be switched on for a self-hosted Access application, so an MCP client can sign in with no OAuth code at the origin; the client must support RFC 8707 | CONFIRMED in vendor docs; the end-to-end flow with a Claude connector is INFERRED (never exercised by us) | Cloudflare One docs, MCP and Access sections | Manual test MT-001 |
| F10 | OpenSSH honours the SSH `signal` channel request since 7.9 for command sessions that are not under a forced command, and delivers it to the session's process group | INFERRED | recalled from OpenSSH release notes and `session.c` | Phase 2 premise, then the real-`sshd` lane |
| F11 | A cloud build sandbox can install the toolchain with `scripts/bootstrap.sh` (mise from `mise.run`, Go from `dl.google.com`), and the template gate runs green there in about 10 seconds | CONFIRMED | observed in the planning sandbox; observed again in the build session on 2026-11-19 (bootstrap about 3 seconds with a warm download cache, gate 8 seconds, `check.sh: all OK`) | -- |
| F12 | The build sandbox has no `ssh` or `sshd` binary. A `docker` client is installed but no daemon is running. `go mod download` reaches the module proxy and `govulncheck` reaches its database. Installing `openssh-server` was refused by the session's permission layer, so whether `sshd` could run there on a loopback port was not observed; the real-`sshd` lane runs in CI (section 7 callout) | CONFIRMED (absence, proxy, vulnerability database, the refusal), not observed (a local `sshd`) | observed in the build session, 2026-11-19 | Phase 2 premise (CI has `sshd`) |
| F13 | In a cloud session the GitHub proxy rejects tag pushes and branch deletions and rejects GraphQL, so `gh pr` and `gh issue` fail; REST works (`gh api repos/...`). The vendor docs say the proxy does not limit which branches a push may update; the operator's experience is that a session can only push branches under a `claude/` prefix. The design needs neither to be true: a session pushes exactly one branch per repository, the one it was started on | CONFIRMED (tags, deletions, GraphQL; a push of the session's own `claude/...` working branch is accepted and CI runs on it), not probed (whether pushes are limited to a prefix) | `https://code.claude.com/docs/en/cloud-environments.md`, "GitHub proxy", re-read 2026-11-19; the push and the check runs observed in the build session, 2026-11-19 | -- |
| F14 | A cloud session with several repositories starts in the directory above the clones and loads no hooks, permission rules or `env` from any repository's `.claude/settings.json`. Plugins declared there do not load; they come from the environment's setup script. Each repository's `CLAUDE.md` still applies | CONFIRMED | `https://code.claude.com/docs/en/settings.md`, "Settings in cloud sessions"; in the build session (2026-11-19) the `agency` plugin's skills and agents were loaded from the environment and the toolchain was not, until `scripts/bootstrap.sh` ran | -- |
| F15 | The edge closes a proxied request whose origin sends nothing for about 100 seconds | INFERRED | recalled Cloudflare proxy read timeout | Phase 5 premise; manual test MT-003 |
| F16 | No open-source project that was found covers the locked scope. The closest ones read (`gelse/ssh-mcp`, `tufantunc/ssh-mcp`, `n0madic/ssh-mcp`, `orvice/ssh-mcp`, `sparksbenjamin/aegis-ssh-mcp`) each lack origin-side verification of an edge identity token, and none combines it with job control, tenant-scoped SQLite state and the fixed eight tools | CONFIRMED for the READMEs that were read; INFERRED for the wider ecosystem (commit dates could not be read, and a few listings were seen only as search results) | time-boxed survey in the build session, 2026-11-19 (section 1 callout) | -- |
| F17 | Go 1.27.4 and golangci-lint 2.15.0 are the current releases and install through mise | CONFIRMED | installed in the planning sandbox | -- |
| F18 | Candidate pure-Go SQLite drivers are `modernc.org/sqlite` and `github.com/ncruces/go-sqlite3`; candidate YAML libraries are `go.yaml.in/yaml` and `github.com/goccy/go-yaml` | CONFIRMED (YAML), INFERRED (SQLite) | YAML, checked 2026-11-19 in the source of `go.yaml.in/yaml/v3` v3.0.6: `Decoder.KnownFields` rejects unknown keys ("field x not found in type"), duplicate keys are rejected by default when decoding into Go values ("mapping key already defined at line N"), every problem is collected into one `TypeError` with its line, and every `yaml.Node` carries `Line` and `Column`. Chosen: `go.yaml.in/yaml/v3` v3.0.6, the YAML organisation's maintained continuation of `gopkg.in/yaml.v3` with a long-stable API (`go.yaml.in/yaml/v4` v4.0.0 was two weeks old). Decoding into `yaml.Node` skips the duplicate-key check, and the library reports lines, not key paths, so the loader walks the node tree itself: that gives `path.to.key` messages, every problem in one pass, and duplicate, unknown-key, anchor and alias checks in one place. `goccy/go-yaml` was not needed | Phase 3 (SQLite): pick one, record why |
| F19 | A cloud session with several repositories can start each clone on a chosen branch, not only on its default branch | CONFIRMED | the operator, in the desktop app, 2026-11-19 (an earlier version of this row said the opposite; that came from a mistake in creating the session). The build session's clones each started on a `claude/...` working branch that was created at the head of the plan branch, so the kickoff merge was a no-op | -- |
| F20 | An idle session's machine is paused with its files and can later be reclaimed; a reopened session gets a fresh machine and a fresh clone, so only pushed commits are safe | CONFIRMED (pause, reclaim, fresh machine), INFERRED (unpushed commits are lost) | `https://code.claude.com/docs/en/cloud-environments.md` and `https://code.claude.com/docs/en/claude-code-on-the-web.md`, read 2026-11-19 | -- |

## 4. Architecture

The model is `docs/architecture/*.c4`; this table is its index. A phase that builds an element removes that element's `#planned` tag in the same commit and regenerates the diagrams (`mise run arch:gen`). A phase that needs an element this table does not have adds the row and the element together.

| FQN | Kind | Go package | Built in |
|---|---|---|---|
| `operator` | actor | -- | -- |
| `assistant` | system, external | -- | -- |
| `edge` | system, external | -- | -- |
| `targets` | system, external | -- | -- |
| `yasshm` | system | module `github.com/InfiniteRoomLabs/yasshm` | -- |
| `yasshm.config` | component | `internal/config` | Phase 1 |
| `yasshm.policy` | component | `internal/policy` (domain types in `internal/tenant`) | Phase 1 |
| `yasshm.audit` | datastore | `internal/audit` | Phase 1 |
| `yasshm.ssh` | component | `internal/sshclient` | Phase 2 |
| `yasshm.jobs` | component | `internal/jobs` | Phase 3 |
| `yasshm.store` | datastore | `internal/store` | Phase 3 |
| `yasshm.outputs` | datastore | `internal/jobs` (output files) | Phase 3 |
| `yasshm.auth` | component | `internal/auth` | Phase 4 |
| `yasshm.tools` | component | `internal/mcpserver` | Phase 5 |
| `yasshm.http` | component | `internal/mcpserver`, `cmd/yasshm` | Phase 5 |

Relationships (all in `model.c4`): `assistant -> edge`, `edge -> yasshm.http`, `yasshm.http -> yasshm.auth`, `yasshm.http -> yasshm.tools`, `yasshm.tools -> yasshm.policy`, `yasshm.tools -> yasshm.jobs`, `yasshm.tools -> yasshm.audit`, `yasshm.jobs -> yasshm.ssh`, `yasshm.jobs -> yasshm.store`, `yasshm.jobs -> yasshm.outputs`, `yasshm.auth -> yasshm.config`, `yasshm.policy -> yasshm.config`, `yasshm.ssh -> yasshm.config`, `yasshm.auth -> edge` (signing keys), `yasshm.ssh -> targets`, `operator -> yasshm.config`, `operator -> yasshm.audit`.

Dependency rule: packages depend downward only. `tenant` (types) depends on nothing. `config`, `policy`, `audit`, `sshclient`, `store` depend on `tenant`. `jobs` depends on `sshclient`, `store`, `audit`. `auth` depends on `config`, `tenant`. `mcpserver` depends on all of them. Test support lives in `internal/testkit/...` and is imported only by tests.

## 5. Component design

### 5.1 Config (`yasshm.config`)

One YAML file, path from `--config`. Decoded strictly: unknown keys, duplicate keys, wrong types and missing required keys are errors, reported as one line per problem in the form `path.to.key: message`. All values below are synthetic.

```yaml
version: 1
server:
  listen: 127.0.0.1:8822
  data_dir: /var/lib/yasshm
  audit_log: /var/log/yasshm/audit.jsonl
auth:
  provider: cloudflare-access
  cloudflare_access:
    team_domain: example-team.cloudflareaccess.com
    audience: "REPLACE_WITH_THE_ACCESS_APPLICATION_AUD_TAG"
tenants:
  - id: default
    principals:
      - email: operator@example.net
    identities:
      lab-look:
        user: yasshm-look
        private_key_file: /etc/yasshm/keys/lab-look_ed25519
    servers:
      - id: lab-1
        description: Example lab host
        host: lab-1.example.net
        port: 22
        identity: lab-look
        host_key: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEXAMPLEONLYEXAMPLEONLYEXAMPLEONLYEXAMPLEONLY00"
        access: look
        tags: [lab]
    policy:
      deny_commands: []
      deny_paths: []
    limits: {}
```

Validation rules, each with a test: `version` is 1. `listen` is a loopback address unless `server.allow_non_loopback: true`. Exactly one tenant (the rule is one constant, `MaxTenants = 1`, and tests that need two tenants build the config in code). Tenant, identity and server ids match `^[a-z0-9][a-z0-9-]{0,62}$` and are unique in their scope. Every server names an identity that exists. `access` is `look`; `full` produces "access level full is not supported in this version". `host_key` parses as an OpenSSH public key (alternatively `known_hosts_file`, exactly one of the two). `private_key_file` exists, parses, and is not readable by group or other. Every `deny_commands` entry compiles as an RE2 expression. Every limit is inside its allowed range. `auth.cloudflare_access.certs_url` is an optional override that must be `https` unless its host is loopback (this is what lets the tests point the real binary at a local issuer).

Limits and their defaults (all under `limits`, per tenant): `default_wait_seconds` 30, `max_wait_seconds` 60 (keep it under the shortest timeout between client and gateway, F3 and F15), `default_timeout_seconds` 600, `max_timeout_seconds` 3600, `max_concurrent_commands` 4, `max_concurrent_per_server` 2, `max_command_bytes` 8192, `max_output_bytes` 16777216 per command, `max_result_bytes` 100000 per tool result, `retention_days` 7, `max_data_bytes` 1073741824, `max_upload_bytes` 1048576, `kill_grace_seconds` 5, `connect_timeout_seconds` 10, `shutdown_grace_seconds` 20.

> **STATE AS OF 2026-11-19** -- Built in Phase 1 (`internal/config`). The spec gives the defaults but not the ranges; these are the allowed ranges (inclusive), and the same table is `limitTable` in `internal/config/limits.go`:
>
> | Limit | Default | Min | Max | Why the bounds |
> |---|---|---|---|---|
> | `default_wait_seconds` | 30 | 0 | 290 | 0 returns at once; never above `max_wait_seconds` |
> | `max_wait_seconds` | 60 | 1 | 290 | under the client's 300 s tool-call limit (F3); the edge may cut at about 100 s (F15) |
> | `default_timeout_seconds` | 600 | 1 | 86400 | never above `max_timeout_seconds` |
> | `max_timeout_seconds` | 3600 | 1 | 86400 | a day is the longest job a tool call should start |
> | `max_concurrent_commands` | 4 | 1 | 64 | one connection per command (D13) |
> | `max_concurrent_per_server` | 2 | 1 | 64 | never above `max_concurrent_commands` |
> | `max_command_bytes` | 8192 | 1 | 65536 | far above a typed command, below `ARG_MAX` |
> | `max_output_bytes` | 16777216 | 1024 | 1073741824 | never above `max_data_bytes` |
> | `max_result_bytes` | 100000 | 1024 | 140000 | the client caps a result at about 150,000 characters (F3) |
> | `retention_days` | 7 | 1 | 3650 | |
> | `max_data_bytes` | 1073741824 | 1048576 | 1099511627776 | |
> | `max_upload_bytes` | 1048576 | 1 | 1572864 | the 2 MiB body cap (5.8) less base64's growth |
> | `kill_grace_seconds` | 5 | 1 | 60 | |
> | `connect_timeout_seconds` | 10 | 1 | 120 | |
> | `shutdown_grace_seconds` | 20 | 1 | 300 | |
>
> Cross-field rules: `default_wait_seconds <= max_wait_seconds`, `default_timeout_seconds <= max_timeout_seconds`, `max_concurrent_per_server <= max_concurrent_commands`, `max_output_bytes <= max_data_bytes`.
>
> Choices the loader makes where this section is silent: a `principals` entry is `email` (a user, compared without regard to case) or `common_name` (a service token's client id, F7), exactly one per entry. YAML anchors, aliases and merge keys are rejected, and a file holds exactly one document of at most 1 MiB. `listen` must be a loopback IP literal (a name such as `localhost` is refused, since what it resolves to is not the config's to decide); port 0 is accepted and asks the kernel for a free port. `data_dir`, `audit_log`, `private_key_file` and `known_hosts_file` are absolute, clean paths; `config check` does not look at `data_dir` or `audit_log` on disk. A private key file is refused when its mode has any bit for group or other (stricter than "not readable": a group-writable key is refused too), checked on the mode bits so the check also holds for root, and when it is protected by a passphrase. `host_key` is one line, one key, no `authorized_keys` options and no certificate (D18). A `known_hosts_file` must list a key for the server's `host:port`. A `deny_commands` entry that matches the empty string is refused (it would deny every command). A `deny_paths` glob that contains a slash must be absolute and is matched against the whole cleaned path; one without a slash is matched against the base name. A server's `port` defaults to 22; `description` (at most 256 bytes) and `tags` (ids, at most 16) are optional; `access` is required. Problem paths name list items by index and, when the item has a valid id, the id: `tenants[0](default).servers[1](lab-1).host_key`.

### 5.2 Tenant model and access levels (`internal/tenant`)

Types: `TenantID`, `Principal{Tenant TenantID, Kind (user or service), Subject string}`, `AccessLevel` (ordered: `none < look < full`), `ServerID`, `CommandID`. Rules that make later growth cheap:

- No function below `mcpserver` takes a server, command or path without a `TenantID`. There is no "current tenant" global.
- A server grants one maximum level. A tool requires one level. The only comparison is `required <= granted`, in one function.
- The identity used for a connection comes from `Server.IdentityFor(level)`, which returns the server's single identity in v0.1. That function is the seam for a per-level identity later.
- A token whose principal maps to no tenant is rejected with HTTP 403 and audited.

### 5.3 Policy (`yasshm.policy`)

`Decide(principal, server, tool, args) -> Decision{Allow bool, Code, Rule}`. Checks in order: the server belongs to the principal's tenant (else `not_found`, never `access_denied`, so ids do not leak across tenants); the tool's required level is within the grant (else `access_denied`); the arguments pass the guard rails (else `policy_denied` with the rule that matched).

Command guard rails: valid UTF-8, no NUL, at most `max_command_bytes`, and no match against the denylist. The denylist is RE2 expressions matched unanchored against the whole command string: a built-in list (privilege escalation such as `sudo`, `su`, `doas`, `pkexec`; power and init control; filesystem creation and raw-device writes; recursive deletes of `/`; pipe-to-shell downloads; a fork bomb) plus the operator's `deny_commands`. The built-in list lives in one file, and every pattern has a must-match and a must-not-match test case. Path guard rails for file transfer: the path is absolute, is cleaned (`path.Clean`), and matches no entry of the built-in `deny_paths` globs (shadow files, private keys, common secret file names) or the operator's additions.

The denylist is a guard rail, not a boundary. The docs, the tool descriptions and `SECURITY.md` say so in those words.

### 5.4 Audit (`yasshm.audit`)

One JSON object per line, appended with `O_APPEND`, file mode 0600, synced after each event. Fields: `ts` (RFC 3339, UTC), `event`, `request_id`, `tenant`, `principal`, `tool`, `server`, `command_id`, `decision`, `code`, `rule`, and event-specific fields. Events: `auth.accepted`, `auth.rejected` (with a reason, never the token), `tool.called` (every tool call, arguments included except file content), `policy.denied`, `command.requested` (written before the SSH session starts, carries the full command text), `command.started`, `command.finished` (status, exit code, byte counts, duration), `command.kill_requested`, `file.downloaded`, `file.uploaded`, `gateway.started`, `gateway.stopped`.

Audit-before-act (D12): the handler writes `command.requested`, `command.kill_requested` or the file event's "requested" form first; if the write or sync fails, the tool returns `internal` and nothing else happens. The audit log is sensitive (it holds command text); it never holds output, tokens or key material. Rotation is out of scope (section 10).

### 5.5 SSH client (`yasshm.ssh`)

Built on `golang.org/x/crypto/ssh`. One connection per command. Host key callback is a fixed key from config or a `known_hosts` file; an unknown or changed key fails with `host_key_mismatch` and is audited. Authentication is one private key per identity. No PTY, no agent forwarding, no port forwarding, no environment requests.

- `Exec(ctx, server, command, stdout, stderr) -> ExitStatus`: starts the command exactly as given (the remote login shell interprets it), streams both outputs to the writers, returns the exit code or the signal that ended it. Cancelling `ctx` sends `TERM`, waits `kill_grace_seconds`, sends `KILL`, then closes the session.
- `Signal(session, sig)`: the SSH `signal` request (F10). Reports whether the server accepted it. If it did not, the session is closed and the caller records that the remote process may still be running.
- `Download(ctx, server, path, offset, max) -> data, size, eof` and `Upload(ctx, server, path, data, mode, overwrite)`: SFTP subsystem. `Upload` is built and tested here even though no v0.1 tool call can reach it (D5).
- `Probe(ctx, server)`: a fixed, built-in set of read-only commands for `get_server_info` (kernel, architecture, OS name, uptime, login name). The text is constant, never caller-supplied.

Error mapping is part of the contract: `ssh_unreachable` (dial or timeout), `ssh_auth_failed`, `host_key_mismatch`, `timeout`, `internal`.

### 5.6 Jobs, store and outputs (`yasshm.jobs`, `yasshm.store`, `yasshm.outputs`)

A command record: `id` (random, 128 bits, URL-safe), `tenant`, `principal`, `server`, `command`, `status`, `exit_code`, `signal`, `error_code`, `created_at`, `started_at`, `finished_at`, `stdout_bytes`, `stderr_bytes`, `truncated`, `timeout_seconds`, `kill_requested_at`.

Statuses: `running`, `succeeded` (exit 0), `failed` (non-zero exit), `killed`, `timed_out`, `lost` (the gateway died while it ran), `error` (the gateway could not run it). There is no queue: when a concurrency limit is reached, `run_command` returns `busy` and says which limit.

Engine API: `Start(ctx, tenant, principal, server, command, opts) -> record`, `Wait(ctx, tenant, id, d) -> record`, `Get`, `List(tenant, filter, cursor)`, `Kill(ctx, tenant, id, sig)`, `ReadOutput(tenant, id, stream, offset, max, wait) -> data, next, eof`, `Recover()`, `Shutdown(ctx)`. Every call takes the tenant and a record of another tenant is `not_found`.

Output: two append-only files per command under `data_dir/tenants/<tenant>/commands/<id>/` (`stdout`, `stderr`), directories 0700 and files 0600. With `merge_stderr` (the default) both streams are written to `stdout` in arrival order. When a command passes `max_output_bytes` the engine stops storing, keeps draining so the remote side does not block, and sets `truncated`. A reader with `wait_seconds` blocks until there are bytes past its offset, the command ends, or the wait expires.

Lifecycle: a command that passes its timeout is ended as in `Exec` and marked `timed_out`. `Recover()` runs at start and marks every `running` record `lost` (the output captured so far stays readable). `Shutdown` stops accepting work, ends running commands, marks them `killed` and waits up to `shutdown_grace_seconds`. A janitor deletes finished commands older than `retention_days` and, above `max_data_bytes`, the oldest finished ones first. No goroutine outlives `Shutdown` (tests assert it).

Store: SQLite, WAL mode, one writer. Schema versioned with an embedded, forward-only migration list; the binary refuses a database newer than it understands. Every table that holds tenant data has a `tenant` column in its primary key or first index.

### 5.7 Authenticator (`yasshm.auth`)

`Authenticator.Authenticate(*http.Request) -> (tenant.Principal, error)`. One implementation in v0.1, for Cloudflare Access (F7, F8): read `Cf-Access-Jwt-Assertion`; verify with a maintained library that the algorithm is RS256, the `kid` is in the JWKS, the signature is valid, `iss` equals `https://<team_domain>`, `aud` contains the configured audience, and `exp`, `nbf`, `iat` hold with 30 seconds of leeway; map `email` (user) or `common_name` (service token) to a principal through the tenant's `principals` list. The JWKS is fetched at start, cached, refreshed periodically, and refetched when a token names an unknown `kid`, at most once per minute so a stream of bad tokens cannot make the gateway hammer the edge. If the JWKS cannot be fetched at start the gateway starts and rejects every request until it can (and says so in the log and on `/healthz`).

HTTP results: no header or a bad token is 401; a valid token with no mapped principal is 403; both are audited with a reason and neither echoes the token. HTTP middleware puts the principal in the request context; nothing behind it runs without one.

### 5.8 MCP endpoint and tools (`yasshm.http`, `yasshm.tools`)

HTTP surface: `POST /mcp` (Streamable HTTP, stateless, F5), `GET /healthz` (no authentication; returns status and version, nothing else), everything else 404. Request bodies are capped at 2 MiB, header reads time out at 10 seconds, and the write timeout exceeds `max_wait_seconds`. The response mode (plain JSON or an event stream) is chosen in Phase 5 against F15; the requirement is that a tool call survives a buffering reverse proxy for `max_wait_seconds`.

Every tool returns structured content and a text rendering. Failures the model should read are tool results with `isError: true` and a stable `code`: `invalid_argument`, `not_found`, `access_denied`, `policy_denied`, `busy`, `ssh_unreachable`, `ssh_auth_failed`, `host_key_mismatch`, `timeout`, `internal`. Tool descriptions state the access level, the guard-rail caveat, and that output from a target is untrusted text.

| Tool | Requires | Input | Output | Notes |
|---|---|---|---|---|
| `list_servers` | a principal | none | `servers[]` of `id`, `description`, `tags`, `access` | The caller's tenant only. Read-only hint |
| `get_server_info` | `look` | `server` | config-side fields plus `reachable`, `latency_ms`, `host_key_ok`, `facts` | Runs the fixed probe of 5.5 |
| `run_command` | `look` | `server`, `command`, `wait_seconds`, `timeout_seconds`, `merge_stderr`, `max_bytes` | `command_id`, `status`, `exit_code`, `stdout`, `stderr`, `next_offset`, `truncated`, byte counts | Waits up to `wait_seconds` (capped), then returns with `status: running`. The output fields equal `read_output` at offset 0. Not read-only; destructive and open-world hints set |
| `read_output` | `look` | `command_id`, `stream`, `offset` (negative counts from the end), `max_bytes`, `wait_seconds` | `status`, `exit_code`, `data`, `next_offset`, `eof`, `lossy` | `data` is text; bytes that are not valid UTF-8 are replaced and `lossy` is set |
| `list_commands` | `look` | `server`, `status`, `limit` (default 20, at most 100), `cursor` | `commands[]` (id, server, first 200 characters of the command, status, exit code, times, byte counts), `next_cursor` | Newest first |
| `kill_command` | `look` | `command_id`, `signal` (`TERM` default, `INT`, `KILL`) | `status`, `signal_delivered` | Idempotent on a finished command |
| `download_file` | `look` | `server`, `path`, `offset`, `max_bytes` | `size`, `data` or `data_base64`, `next_offset`, `eof` | SFTP read behind the path guard rails |
| `upload_file` | `full` | `server`, `path`, `content` or `content_base64`, `mode`, `overwrite` | `bytes_written` | In v0.1 every call returns `access_denied`, because no server can grant `full` |

### 5.9 Command line

`yasshm serve --config <file>`, `yasshm config check --config <file>` (exit 0 when valid, 1 with the error lines otherwise; meant for a systemd `ExecStartPre` and for config-management validation hooks), `yasshm version` (version, commit, build date, injected at build time). Logs are structured JSON on stderr (`log/slog`) and never contain tokens, key material, command text or command output.

## 6. Security model

**Assets:** the SSH private keys, the targets, the audit log, captured output (it can contain secrets from a target).

**Boundaries:** client to edge (the edge's job), edge to gateway (D4), gateway to target (D13), tenant to tenant (D7), and target output to model.

**Guarantees v0.1 makes, each with a test:** no request reaches a tool without a verified principal. A principal sees only its tenant's servers, commands and output. Nothing from the config file can be read or changed through MCP. A target with an unknown or changed host key is never spoken to. An action whose audit event cannot be written does not happen. Tokens and key material never appear in logs, errors or the audit log. A command stops consuming gateway resources when it is killed, times out or the gateway shuts down.

**Not guaranteed, and documented as such:** that a command at `look` changes nothing (the target account decides that). That the denylist cannot be bypassed. That output is free of secrets. That text returned from a target is safe for a model to act on: a compromised or hostile target can put instructions in its output, and tool descriptions label output as untrusted.

**Out of scope:** attacks that need root on the gateway host, and the edge's own configuration.

## 7. Testing, CI and release design

`TESTING.md` holds the rules (green rule, pyramid, path taxonomy). This section is how they apply here.

**The gate** is `mise run check`. Phase 0 makes it run these steps, each also runnable alone as `mise run check -- <step>`: `fmt` (gofmt reports nothing), `lint` (golangci-lint, warnings are errors, every `nolint` needs a specific linter and a reason), `skipcheck` (no skipped or focused test without an issue link on the same line), `test` (`go test -race -shuffle=on -count=1 ./...` with a coverage profile over all packages), `cover` (total at or above the floor, 85), `vuln` (`govulncheck ./...`), `build` (`CGO_ENABLED=0`, linux/amd64 and linux/arm64), `arch`. A dirty tree fails the gate.

**Layers.** Unit tests sit beside the code. Integration tests exercise each seam with the real thing on the other side: the SSH client against a server, the store against SQLite on disk, the authenticator against a JWKS endpoint, the job engine against all three. End-to-end tests (`test/e2e`) build the real binary once, start it on a random loopback port with a generated config, and drive it with the MCP Go SDK client.

**Test support** (`internal/testkit`, excluded from the coverage measurement; that exclusion is this design decision and no other is allowed without a line here):

- `sshtest`: an in-process SSH server on a random loopback port. By default it really runs the command with a local `sh -c` in a temp directory, honours `signal` requests, serves SFTP from a temp root, and has switches for fault injection (wrong host key, refused key, dropped connection mid-stream, refused `signal`, slow banner).
- `realsshd`: starts a real OpenSSH `sshd` as the current user on a random loopback port with a generated host key and `authorized_keys`. The SSH contract suite (exec, exit codes, signals, timeouts, large output, SFTP both ways, host key mismatch) runs against both `sshtest` and `realsshd`. This lane is what turns F10 from INFERRED into CONFIRMED.
- `accesstest`: a local issuer that serves a JWKS and mints tokens: valid, expired, not yet valid, wrong audience, wrong issuer, unknown `kid`, tampered, `alg` none, HS256 signed with the public key, rotated key.
- `edgetest`: a reverse proxy that adds the token header, standing in for the edge. A request that goes around it must get 401.

**The real-`sshd` lane is not optional.** Phase 0 finds out whether the sandbox can install and run `sshd` (F12). If it can, the lane is part of the local gate and `scripts/bootstrap.sh` checks for the binary. If it cannot, the lane runs in CI only, the local gate prints a line saying so, the QA lane must see that CI step green before it can say PASS, and this paragraph gets a STATE AS OF callout. In neither case does a test skip silently.

> **STATE AS OF 2026-11-19** -- The build sandbox has no `sshd`, and the session's permission layer refused `apt-get install -y openssh-server`; the lead did not work around the refusal. So the CI-only arrangement applies: the real-`sshd` lane runs in CI, where the workflow makes sure `sshd` is installed and fails if it is not; the local gate prints one line saying the lane ran in CI only; the QA lane must see that CI step green before it can say PASS. `scripts/bootstrap.sh` reports whether `sshd` is present and does not fail without it. If the operator adds `openssh-server` to the cloud environment's setup script, a later session gets the lane locally with no code change. No Docker daemon runs in the sandbox either (a client is installed); only Phase 8 could have used one.

**End-to-end scenarios (Phase 6), each tagged with its path class:** every tool through the fake edge; no token, bad token and unmapped principal; two tenants cannot see each other's servers, commands or output (config built in code); a command that outlives `wait_seconds`, is paged and is killed; a command that times out; output beyond `max_output_bytes`; a `busy` rejection; a denylisted command; a host key mismatch; `upload_file` denied; an unwritable audit log blocks the action; the gateway is killed mid-command and the restarted gateway reports `lost`; graceful shutdown; `config check` on valid and invalid files.

**Fuzzing.** Native Go fuzz targets for the config decoder, the command and path guard rails, the claims-to-principal mapping and the output paging arithmetic. Seed corpora run in the gate as ordinary tests; `mise run fuzz` runs each target for 30 seconds and is run in Phase 6, with any crasher committed as a seed.

**Test names** carry the taxonomy as the first subtest path element (`happy/`, `sad/`, `raging/`, `edge/`, `corner/`), so `go test -run '/sad/'` selects a class.

**CI** (`.github/workflows/ci.yml`) runs the same gate on every push to any branch and on pull requests. Read its result with REST (F13): `gh api repos/InfiniteRoomLabs/yasshm/commits/<sha>/check-runs`.

**Release.** `mise run build` writes `dist/yasshm_<version>_linux_<arch>` and `dist/SHA256SUMS`, built with `-trimpath` and version metadata. A `release.yml` workflow (Phase 7) builds the same files on a `v*` tag and attaches them to a GitHub release, with SHA-pinned actions and write permission only in that job. Pushing the tag is an attended step (F13, and because a release is the operator's call).

**Manual tests** (`test/manual/MT-<nnn>-<name>.md`, written in Phase 7, run by the operator after an attended deployment, never by the loop): MT-001 a Claude connector signs in through the real edge and `list_servers` answers; MT-002 on the real target the `look` account is unprivileged, a denylisted command is refused and both are in the audit log; MT-003 a long command returns a command id inside the wait window through the real edge, is paged, is killed, and the process is gone on the target; MT-004 the service is restarted mid-command and the command reads `lost`; MT-005 the public hostname refuses a request that has not signed in. A failed manual test refuses the release tag.

## 8. Deployment contract

What yasshm needs from the machine it runs on. Phase 7 ships these as examples under `deploy/`; the private infra repository turns them into infrastructure as code (Phase 8). Nothing in this section names a real host.

- A system user and group `yasshm` with no login shell.
- The binary at `/usr/local/bin/yasshm`, installed from a release file whose checksum was verified.
- `/etc/yasshm/config.yaml` (readable by the service, not by others) and `/etc/yasshm/keys/` (private keys owned by the service user, mode 0600; the service refuses a key that group or other can read).
- `/var/lib/yasshm` (mode 0700) for the database and output, `/var/log/yasshm/` for the audit log.
- A systemd unit: `ExecStartPre=yasshm config check`, `ExecStart=yasshm serve`, `Restart=on-failure`, and hardening (`NoNewPrivileges`, `ProtectSystem=strict` with the two writable paths, `ProtectHome`, `PrivateTmp`, an empty capability bounding set, address families limited to INET, INET6 and UNIX).
- The edge connector running on the same host, forwarding one public hostname to the loopback port, with an Access application in front of it that has managed OAuth switched on and a policy that admits the operator's identity. The application's AUD tag and the team domain go into the config.
- On each target: an unprivileged account for `look`, outside every privileged group, whose `authorized_keys` line for the gateway's public key starts with `restrict` and has no forced command (a forced command disables `signal`, F10).

## 9. Process

**The loop.** One cloud session, started once, runs the whole approved roadmap. Its lead is an orchestrator: for each phase it verifies the premise, opens the phase with a start commit, dispatches one implementer, runs the four-lane gate (code review, simplification and security in parallel and read-only, then QA, the only lane that runs the gate), triages into one fix commit, re-gates, closes the phase with a ship commit, retargets `GOAL.md`, and goes on to the next phase. It writes no product code itself. `GOAL.md` and `CLAUDE.md` carry the rules; this section fixes what is specific to yasshm.

**Branch model.** The plan lives on `feat/yasshm-mvp` in both repositories. The session is started with both clones on that branch (F19) and makes its working branch from it; if a clone starts elsewhere, the kickoff merges the plan branch in. The only push the session can count on is to its own working branch (F13), so it never pushes to the plan branch. There are no phase branches and no merges: a phase is a commit range on the working branch, from its start commit (`docs(phase-<n>): start <name>`) to its ship commit (`docs(phase-<n>): ship <name>`), and the review lanes diff that range. The working branch is pushed after every green commit, because an idle session's machine can be reclaimed (F20). Nothing else is ever pushed: not `main`, not the infra repository's default branch, not the plan branch, no tag, no force-push, no deletion. Merging the working branches, tagging a release, deploying and the history scrub are the operator's, with the lead parked under `Blocked on user`.

**Phases.** `GOAL.md` holds the live table and its status. The plan each row comes from:

| Phase | Builds | Architecture delta | Acceptance beyond "gate green on a clean tree" |
|---|---|---|---|
| 0 Scaffold | Go module, `cmd/yasshm` with `version`, the full gate of section 7, `mise` tasks (`build`, `test`, `lint`, `fuzz`), dirty tree becomes a failure, sandbox facts F12 and F16 settled | none | Each gate step runs alone; a skipped test without an issue link fails `skipcheck`; a total under the floor fails `cover`; CI is green on the pushed head |
| 1 Config, tenants, policy, audit | 5.1 to 5.4, `yasshm config check` | `yasshm.config`, `yasshm.policy`, `yasshm.audit` lose `#planned` | Every validation rule and every denylist pattern has a passing and a failing case; `full` is rejected by the loader and accepted by the policy engine in a code-built grant; audit-before-act proven with an unwritable file; fuzz targets for decoder and guard rails |
| 2 SSH layer | 5.5, `testkit/sshtest`, `testkit/realsshd`, the SSH contract suite | `yasshm.ssh` loses `#planned` | The contract suite passes against both servers (or the CI-only arrangement of section 7 is in place); F10 is CONFIRMED or corrected; no test needs the network |
| 3 Jobs, store, outputs | 5.6 | `yasshm.jobs`, `yasshm.store`, `yasshm.outputs` lose `#planned` | Every status is reachable in a test; restart recovery, truncation, retention, concurrency limits and tenant filtering are proven; no goroutine leaks |
| 4 Authenticator | 5.7, `testkit/accesstest` | `yasshm.auth` loses `#planned` | Every token class of `accesstest` has a test with the expected status code and audit event; a token never appears in any log or error |
| 5 MCP endpoint and tools | 5.8, 5.9 `serve`, `testkit/edgetest` | `yasshm.tools`, `yasshm.http` lose `#planned` | Each tool has happy, sad and raging cases through the real handler; F6 and F15 are settled; nothing from the config leaks through any tool (asserted on the serialized results) |
| 6 End-to-end and hardening | `test/e2e`, the scenarios of section 7, fuzz run, goroutine and file-descriptor leak checks, a review of section 6 against the code | none | Every scenario exists and passes against the real binary; each guarantee in section 6 names the test that proves it |
| 7 Packaging and operator docs | `deploy/` (unit, example config), `mise run build` release files, `release.yml`, the operator guide, `SECURITY.md` threat model, `test/manual/MT-001` to `MT-005`, the README | none | `yasshm config check` passes on the shipped example config with a generated key; `systemd-analyze verify` passes on the unit where the tool exists; the manual tests are in the agent-ops format and none has been run by the loop |
| 8 Infra deployment support | In the private infra repository, on the session's working branch there: everything section 8 needs as infrastructure as code, following that repository's own plan document and rules | none here | That repository's static checks that need no secret and no cluster pass; nothing was applied; its plan document lists the attended steps in order |
| 9 History scrub | Attended, always last | none | See `GOAL.md` |

**Models.** The orchestrator runs on fable. Implementer and reviewers per phase are in the `GOAL.md` roadmap. The rule: reviewers are one tier above the implementer. Phases that touch a trust boundary or concurrency (1 to 6, and 8) use the higher pair (opus implements, fable reviews); 0 and 7 use sonnet and opus.

**What the loop can never do** (D16, D17): deploy, apply infrastructure, reach a real target or the real edge, log in to a password manager, read or request a secret, run a manual test, push a tag, push any branch other than its working branch, or merge into a default branch. No phase's done criteria may depend on any of them.

**Done for the MVP:** phases 0 to 8 shipped on the two working branches, CI green, and the lead parked with the operator's ordered list: review and merge both working branches, tag `v0.1.0`, deploy, run MT-001 to MT-005, then Phase 9.

## 10. Future work

Not approved. A row moves into the roadmap only when the owner says so.

- Access level `full`: a privileged identity per server through `IdentityFor`, `upload_file` enabled, a per-level denylist, an idempotency key on `run_command`.
- More than one tenant: lift `MaxTenants`, add tenant administration, per-tenant quotas.
- A second authenticator (generic OIDC), and config reload without a restart.
- A UNIX-socket listener as an alternative to the loopback port.
- Interactive sessions, jump hosts, connection pooling, SSH certificates from a CA.
- A metrics endpoint, audit rotation and shipping, rate limits per principal.
- An allowlist mode for commands.
