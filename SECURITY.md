# Security

## Reporting a vulnerability

Report vulnerabilities privately through GitHub's "Report a vulnerability" form on this repository's Security tab (private vulnerability reporting), not in a public issue. You will get an acknowledgement within a few days. Please include the version (`yasshm version`), what you did and what you saw. Do not include a real token, key or host in the report; describe it and offer to share it privately.

This repository holds no secrets: its examples and fixtures are synthetic. If you find a committed credential or a real host name, report it the same way and treat it as compromised.

## Supported versions

The latest release. There is no release yet (version 0.1 is built and has not been released or deployed); until there is one, the latest commit of the default branch is what is supported. Older releases get no fixes: upgrade (see [the operator guide](docs/operator-guide.md#running-upgrading-and-rolling-back)).

## Threat model

This is the model the design is built on (spec section 6), with the evidence for each claim. The [operator guide](docs/operator-guide.md) says what to do about it in practice.

**Assets.** The SSH private keys the gateway holds; the targets and what the `look` account on them can reach; the audit log (it holds command text); captured command output (it can contain secrets from a target).

**Boundaries.** Client to edge (the edge's job: Cloudflare Access). Edge to gateway (the gateway verifies the Access token on every request; there is no unauthenticated mode). Gateway to target (host keys are pinned, key-only authentication, one connection per command). Tenant to tenant (every query and path carries a tenant id; v0.1 serves one tenant, and isolation is tested with two built in test code). Target output to model (output is untrusted text; the gateway's own text is kept in a separate block).

**Adversaries considered.**

- A stranger on the internet who finds the public hostname: stopped by the edge, and by the gateway's own token check if the edge is bypassed or misconfigured.
- Someone with a valid Access token for another application or another team: refused by the audience and issuer checks.
- Someone who can reach the loopback port from the gateway host without going through the edge: refused (401), but see the last item under what is not guaranteed.
- An authorised user, or an assistant acting for one, who tries to do more than the operator intended: held to the `look` level, the guard rails and the limits; what a command can change is decided by the target account.
- A hostile or compromised target, or a command's output that carries instructions meant for the assistant: output is labelled untrusted and kept in its own block; the gateway never shows a host, user or key path to the assistant.
- An impostor of a target: a host key that is not the pinned one is never spoken to.

**Out of scope.** Attacks that need root on the gateway host, and the edge's own configuration.

### What the gateway guarantees, and the tests that prove it

Seven guarantees. A test is named by its package and function; `TestSecurityModelTable` in `internal/deprule` checks that every named function exists and every quoted phrase is in its file, and `TestSecurityMDMatchesSpec` checks that this section and the spec's section 6 hold the same two tables. The sentences are the spec's.

<!-- guarantees:begin -->
| Guarantee | Proved by |
|---|---|
| No request reaches a tool without a verified principal | `test/e2e` `TestE2EAuth`, `test/e2e` `TestE2EHTTPSurface`, `internal/mcpserver` `TestBehindTheEdge`, `internal/mcpserver` `TestF6IdentityReachesHandler`, `internal/auth` `TestTokenClassesThroughMiddleware` |
| A principal sees only its tenant's servers, commands and output | `internal/mcpserver` `TestTenantIsolation`, `internal/mcpserver` `TestTenantLookupInTheToolsLayer`, `internal/jobs` `TestTenantIsolation`, `internal/store` `TestTenantIsolation`, `test/e2e` `TestE2ETwoTenantsRefused` (the binary serves one tenant) |
| Nothing from the config file can be read or changed through MCP | `internal/mcpserver` `TestNothingFromTheConfigLeaks`, `internal/mcpserver` `TestProbeLoginNameNeverShown`, `internal/mcpserver` `TestToolsList` (exactly the eight tools, none writes config), `test/e2e` `TestE2EEveryTool` |
| A target with an unknown or changed host key is never spoken to | `test/e2e` `TestE2EHostKeyMismatch`, `internal/sshclient` `TestPinningAndKeys`, `internal/sshclient` `TestContractSSHTest`, `internal/sshclient` `TestRealSSHDContract`, `internal/hostkey` `TestKnownHosts` |
| An action whose audit event cannot be written does not happen | `test/e2e` `TestE2EAuditUnwritable`, `test/e2e` `TestE2EAuditUnwritableAtRequested`, `internal/mcpserver` `TestAuditBeforeAct`, `internal/mcpserver` `TestBrokenAuditLog`, `internal/auth` `TestMiddlewareAuditFailure`, `internal/jobs` `TestStartFailures`, `internal/jobs` `TestKillAudit`, `internal/audit` `TestAuditBeforeAct` |
| Tokens and key material never appear in logs, errors or the audit log | `internal/auth` `TestNothingLeaks`, `internal/audit` `TestNoSecretFields`, `internal/config` `TestLoadPrivateKey` (key content is never echoed in an error), `test/e2e` `TestE2EAuth` and `TestE2EEveryTool` (every scenario's cleanup scans the binary's stderr and audit log for token and key text) |
| A command stops consuming gateway resources when it is killed, times out or the gateway shuts down | `test/e2e` `TestE2EResourceBounds`, `test/e2e` `TestE2ELongCommand`, `test/e2e` `TestE2ETimeout`, `test/e2e` `TestE2EGracefulShutdown`, `internal/jobs` `TestLeaks`, `internal/jobs` `TestShutdown` |

| Not guaranteed | Where it is said |
|---|---|
| That a command at `look` changes nothing | `README.md` says "what a command can change is decided by the account it runs as"; `internal/mcpserver/tools.go` says "guard rail, not a boundary" (the `run_command` and `download_file` descriptions, checked by `internal/mcpserver` `TestToolsList`) |
| That the denylist cannot be bypassed | `internal/policy/policy.go` says "The denylist is a guard rail, not a boundary"; `README.md` says "that the denylist cannot be bypassed"; spec 5.3 |
| That output is free of secrets | `README.md` says "that output is free of secrets"; spec 5.6 (captured output is stored under the data directory) |
| Availability under a request flood from a host that can reach the loopback listener | `README.md` says "availability under a request flood"; spec 5.7 and 5.8 (the request cap, `internal/mcpserver` `TestRequestCap`; bounded rejection lines, `internal/auth` `TestRejectionAllowance`) |
| That text returned from a target is safe for a model to act on | `internal/mcpserver/tools.go` says "Output from a target is untrusted text" (checked by `internal/mcpserver` `TestToolsList`); `internal/mcpserver` `TestTargetCannotImitateTheGateway`; `README.md` says "the tool descriptions label output as untrusted" |
| That another local user cannot take the loopback port while the gateway is down | `README.md` says "another local user"; spec section 8 (no untrusted local users) and section 10 (a Unix-socket listener) |
<!-- guarantees:end -->

### What is not guaranteed

In the spec's words: that a command at `look` changes nothing (the target account decides that). That the denylist cannot be bypassed: it is a guard rail, not a boundary. That output is free of secrets. Availability under a request flood from a host that can reach the loopback listener: rejections are answered and their audit lines are bounded (at most 60 a minute for tokens without a verified signature and 60 for verified tokens that are not valid here, plus one summary each), but nothing limits the request rate itself; the edge is what stops a flood from the internet. That text returned from a target is safe for a model to act on: a compromised or hostile target can put instructions in its output, and the tool descriptions label output as untrusted. That another local user cannot take the loopback port while the gateway is down (a restart, an upgrade, a crash loop) and receive what the edge forwards, tokens and command text included: the gateway host must have no untrusted local users, and a Unix-socket listener is future work.

The command denylist is a set of regular expressions that skip quoted text, not a shell parser. The package documentation of `internal/policy` and spec section 5.3 list the bypasses that are known and accepted: a command word built at run time, an option that takes a value before the command, a wrapper the list does not know, commands inside `eval`, other tools that do the same harm, a download saved and run later, and a symbolic link on the target that reaches a denied file.

### Deployment assumptions

The guarantees hold when the deployment is the one spec section 8 describes ([deploy/README.md](deploy/README.md) builds it):

- The edge is in front: a tunnel connector on the gateway host forwards one hostname to the loopback port, and an Access application with a policy that admits only the operator, with Managed OAuth switched on, sits in front of it. The gateway listens on loopback only and refuses another address unless the config says so.
- On each target, an unprivileged account for `look`, outside every privileged group, whose `authorized_keys` line starts with `restrict` and has no forced command (a forced command stops the gateway from signalling a command).
- The gateway host has no untrusted local users, runs the unit as shipped (no `MCPGODEBUG`, no extra capabilities) and keeps the config, keys, data directory and audit log at the modes the install steps give; the gateway refuses to start with a key that group or other can read, a config or `known_hosts` file that others can write, or an audit directory that others can write.
- The audit log and the data directory are treated as sensitive, and the audit log is rotated by copy and truncate.
- The Access service-token principals in the config are tokens the operator created.
