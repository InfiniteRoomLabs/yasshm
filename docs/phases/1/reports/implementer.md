# Phase 1 implementer report -- Config, tenants, policy, audit

Phase base `6cc1f31`. Branch `claude/serene-franklin-jv4pu7`. Every deliverable of `GOAL.md` stage 2 is done.

## Packages and API

- `internal/tenant` (`tenant.go`): `TenantID`, `ServerID`, `CommandID`, `IdentityID`, `PrincipalKind` (`KindUser`, `KindService`), `Principal{Tenant, Kind, Subject}`, `AccessLevel` (`AccessNone < AccessLook < AccessFull`, zero value none, `ParseAccessLevel`, text marshalling), `Permits(granted, required)` (the only `required <= granted`), `Identity`, `Server` with `IdentityFor(level)`, `PrincipalMatch`, `Policy`, `Limits`, `Tenant` with `Server(id)` and `Admits`.
- `internal/config` (`config.go`, `decode.go`, `rules.go`, `files.go`, `limits.go`): `Load(path)`, `Parse(data)` (no file system checks), `Config{Version, Gateway, Auth, Tenants}`, `Config.Tenant`, `Config.Server(tenant, id)`, `Error{Problems}`, `Problem{Path, Line, Message}`, `DefaultLimits`, `MaxTenants = 1`, `MaxFileBytes`.
- `internal/policy` (`policy.go`, `commands.go`, `paths.go`): `NewEngine(tenants)`, `Engine.Decide(principal, *Server, Tool, Args) Decision{Allow, Code, Rule}`, `Tool{Name, Requires, Input, WritesFiles}`, `Args{Command, Path}`, codes `not_found`/`access_denied`/`policy_denied`, `CleanPath`.
- `internal/audit` (`audit.go`, `event.go`): `Open(path, WithClock)`, `Logger.Write`, `Logger.Before(ev, act)`, `Close`, a constructor per event of 5.4, `Actor`, `ToolArgs`, `Finish`, `RejectReason` (closed set).
- `internal/cli`: `yasshm config check --config <file>` (0 / 1 / 2). `internal/testkit/configtest`: synthetic config fixture. `internal/deprule`: dependency-rule test.

## Tests (passing, including subtests)

tenant 54, config 311, policy 596, audit 52, cli 33, cmd/yasshm 5 (runs the real `main` in a child process against files on disk), deprule 7. Every package has `happy/`, `sad/`, `edge/`, `corner/` and `raging/` cases where they apply. No `t.Skip`.

## Gate

Coverage total 96.1 percent. Tail of `mise run check` on `5ffce62`:

```
cover: total 96.1% (floor 85%, internal/testkit excluded)
check.sh: sshd is not installed here; the real-sshd lane runs in CI only (spec section 7)
check.sh: all OK
```

`mise run fuzz`: FuzzParse, FuzzCommandGuardRail, FuzzPathGuardRail, 30 s each, all PASS. An earlier run found `"/\xd1/.."` (invalid bytes that cleaning drops): the guard rail correctly refuses it, the fuzz property was too strict, it was fixed and the input is committed as a seed in `internal/policy/testdata/fuzz/`.

CI: `5ffce62` -> `check completed success`. CI on `d8a1301` failed: a test opened a 0400 audit log for writing, which only root can do; fixed in `5ffce62` (uses 0200).

## Commits (`git log --oneline 6cc1f31..HEAD`, before this report)

```
5ffce62 fix(audit): make the owner-only mode test hold for a non-root runner
d8a1301 docs: describe the Phase 1 packages, config check and the new gotchas
ec393d5 feat(cli): add yasshm config check --config <file>
f0bcfaa feat(audit): add the append-only JSONL audit log with audit-before-act
ba3e796 feat(policy): decide tool calls with tenant, level and guard-rail checks
a6b9444 feat(config): load and validate the YAML config strictly
52f87db feat(tenant): add the tenant domain types and the dependency rule
```

`git status --porcelain`: empty.

## Dependencies

- `go.yaml.in/yaml/v3` v3.0.6: the one YAML library (D14). Checked in its source: `KnownFields`, duplicate-key rejection, line-numbered `TypeError`, `Node.Line/Column`. Chosen over v4.0.0 (two weeks old) for API stability; F18 records it.
- `golang.org/x/crypto` v0.59.0: `ssh.ParseAuthorizedKey`, `ParsePrivateKey`, `knownhosts`.
- Nothing else. `govulncheck`: no vulnerabilities.

## Dependency rule

Enforced by a test, not depguard: `internal/deprule` parses the imports of `tenant`, `config`, `policy`, `audit` and fails on any module import outside the rule (tests may add `internal/testkit`). `policy` does not import `config`.

## Limits ranges (also in spec 5.1 callout)

default_wait 0..290, max_wait 1..290, default_timeout 1..86400, max_timeout 1..86400, max_concurrent_commands 1..64, max_concurrent_per_server 1..64, max_command_bytes 1..65536, max_output_bytes 1024..1 GiB, max_result_bytes 1024..140000, retention_days 1..3650, max_data_bytes 1 MiB..1 TiB, max_upload_bytes 1..1572864, kill_grace 1..60, connect_timeout 1..120, shutdown_grace 1..300. Pairs: default_wait <= max_wait, default_timeout <= max_timeout, per_server <= concurrent, max_output <= max_data.

## Denylist rule names

Commands: `sudo`, `su`, `doas`, `pkexec`, `run0`, `power-control`, `init-runlevel`, `systemctl-power`, `fs-create`, `dd-to-device`, `write-to-device`, `rm-root`, `rm-no-preserve-root`, `pipe-to-shell`, `download-substitution`, `fork-bomb`. Paths: `shadow`, `sudoers`, `ssh-host-key`, `ssh-private-key`, `key-file`, `env-file`, `credential-file`, `cloud-credentials`, `proc-secrets`. Operator entries: `config:deny_commands[<n>]`, `config:deny_paths[<n>]`.

## Spec discrepancies and deliberate choices (each in a STATE AS OF 2026-11-19 callout)

- 5.1: ranges table; `principals` entries are `email` or `common_name`; anchors, aliases, merge keys and multiple documents refused; files at most 1 MiB; `listen` must be a loopback IP literal (not `localhost`), port 0 allowed; paths absolute and clean; key file refused for any group/other bit (stricter than "readable"); passphrase keys refused; `host_key` one line, no options, no certificate; `known_hosts_file` must list `host:port`; a `deny_commands` entry matching the empty string refused; error path form `tenants[0](default).servers[1](lab-1).host_key`, sorted by line. The `server` mapping is `Config.Gateway` (a field named `Server` would clash with the lookup method).
- 5.3: command rules match words in command position, so `grep sudo log` is allowed and `ls; sudo id` is not; matching is on the trimmed command; `WritesFiles` forces `full`; empty/NUL/UTF-8/length checks are `policy_denied` with named rules; path globs without a slash match the base name.
- 5.4: file "requested" form is `stage: requested` on the same event name; the logger refuses all writes after a failure; `O_NOFOLLOW`; RFC 3339 with fractional seconds.
- Sandbox runs as root, CI does not: tests use `/dev/full`, a path under a file and a directory for failures, and mode bits for key checks (CLAUDE.md gotcha).
