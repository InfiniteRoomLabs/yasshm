# Phase 7 implementer report

Branch `claude/serene-franklin-jv4pu7`, phase base `038cdc0`. Work commits: `cf2af7b`, `a47117d`, `934c583`, `bacdf45` (a claim about the AUD tag's location removed), `026dbfb` (gate fix, below); this report is the commit after them. No product code was changed.

## Files created or changed

Created: `deploy/yasshm.service`, `deploy/config.example.yaml`, `deploy/README.md`, `deploy/unit_test.go`, `deploy/example_test.go`, `.github/workflows/release.yml`, `docs/operator-guide.md`, `test/manual/MT-001-connector-signs-in.md`, `MT-002-look-account-unprivileged.md`, `MT-003-long-command-through-the-edge.md`, `MT-004-crash-marks-running-command-lost.md`, `MT-005-public-hostname-refuses-unsigned-requests.md` (each with an empty `.results` beside it), and the tests `internal/config/example_test.go`, `internal/config/guide_test.go`, `internal/audit/guide_test.go`, `internal/mcpserver/guide_test.go`, `internal/store/guide_test.go`, `internal/cli/guide_test.go`, `internal/deprule/workflows_test.go`, `internal/deprule/docs_test.go`, `internal/deprule/manual_test.go`.

Changed: `SECURITY.md` (replaced), `README.md` (status, documentation links, tools table, quick start), `TESTING.md` (gate steps, new section, "Active: MT-001 to MT-005"), `CLAUDE.md` (step list, key locations, gotchas), `CHANGELOG.md`, `scripts/check.sh` (steps `deploy` and `docs`), `internal/deprule/guarantees_test.go` (`TestSecurityMDMatchesSpec`), the spec (callouts in F2, F9, section 7 and section 8). `GOAL.md` and the ledger in `docs/progress.md` were not touched. `mise.local.toml` was already in `.gitignore`.

## How each doc is kept from drifting

| Document | Test |
|---|---|
| `deploy/yasshm.service` | `deploy` `TestUnitDirectives`, `TestUnitRestartAndStop`, `TestUnitNoEnvironment`, `TestUnitNoIPAddressDeny`; `internal/config` `TestUnitStopTimeout` (`TimeoutStopSec` against the largest `shutdown_grace_seconds` + 5 s + margin); `systemd-analyze verify` in the `deploy` step |
| `deploy/config.example.yaml` | `internal/config` `TestExampleLoads` (real loader, generated key and host key; refused as shipped), `TestExampleLimitComments` (default and range in each comment equal `limitTable`; the four pairs and the sum rule are stated), `TestExampleHasEveryKey` (key set walked from the AST of `decode.go`); `deploy` `TestExampleConfigCheck` (through `cli.Run config check`) |
| config reference table in the guide | `internal/config` `TestGuideConfigReference` (same key walk; limit defaults and ranges; required keys checked by deleting each from the example) |
| audit sample lines | `internal/audit` `TestGuideAuditSamples` (writes each event through the logger with a fixed clock and compares with the guide, also that every `Event*` constant has a sample), `TestGuideRejectReasons` |
| tool, code, status, HTTP body, start-up reason tables | `internal/mcpserver` `TestGuideTools`, `TestGuideToolCodes`, `TestGuideHTTPBodies`; `internal/store` `TestGuideStatuses`; `internal/cli` `TestGuideStartUpReasons` |
| `SECURITY.md` | `internal/deprule` `TestSecurityMDMatchesSpec` (tables equal spec section 6, which `TestSecurityModelTable` already checks against the code) |
| `release.yml` and `ci.yml` | `internal/deprule` `TestWorkflowActionsPinned`, `TestWorkflowPermissions`, `TestWorkflowTriggers`, `TestWorkflowNoInterpolationInRun` |
| manual tests | `internal/deprule` `TestManualTests` (five files, header bullets, step shape, live budget equals the number of live steps, placeholders declared with `MT_*`, named tests exist, results file empty or valid logfmt, `TESTING.md` says Active) |
| all Markdown, YAML, unit, shell, TOML | `internal/deprule` `TestDocsLinks` (relative links and anchors; a broken link and a broken anchor fail, shown by a sad subtest), `TestDocsASCII`, `TestDocsPlaceholders`, `TestDocsNotWrapped` (`docs/phases` excluded) |

The `deploy` step also fails when `systemd-analyze verify` prints a line about the unit, because it exits 0 on an unknown directive (checked here by misspelling one). **CI found a problem on the first push** (`934c583`, job failed at `deploy`): on the runner, verify also printed `CPUAccounting= has been removed` warnings for the host's own units, which are not ours; the step now fails only on lines that name `yasshm.service` (`026dbfb`).

## `systemd-analyze verify`

systemd 255 (255.4-1ubuntu8.18) exists in the sandbox. `--root` does not work here (`Unit sysinit.target not found`), so the step verifies a copy of the unit with the binary path pointed at a stub:

```text
== deploy ==
deploy: systemd-analyze verify passed on deploy/yasshm.service (systemd 255 (255.4-1ubuntu8.18))
```

The tool prints nothing for the shipped unit. Where it is absent the step prints one line and skips it (the CI runner is ubuntu-latest and has it; not verified).

## Vendor facts read (2026-11-20)

CONFIRMED in `https://claude.com/docs/connectors/building`: Streamable HTTP is the transport and SSE is being deprecated; DCR; redirect URI `https://claude.ai/api/mcp/auth_callback` for the hosted surfaces and a loopback redirect for Claude Code; 240 s tool-call timeout and about 150,000 characters (claude.ai and Desktop). CONFIRMED in `.../building/testing`: a connector is added by URL under Customize, Connectors; a server on a machine needs a public URL, a tunnel is the recommended pattern; a firewall or WAF that drops Anthropic's traffic is a known failure. CONFIRMED in Cloudflare's Managed OAuth page: Access acts as the OAuth authorization server for non-browser clients; enabled per self-hosted application (Zero Trust > Access controls > Applications > Edit > Advanced settings > Managed OAuth); the client must support RFC 8707; tokens are opaque; the origin must validate `Cf-Access-Jwt-Assertion`; redirect URIs are limited by the application's allowed-URI setting; not to be enabled when running your own OAuth server behind Access. CONFIRMED in Cloudflare's Tunnel page: `cloudflared` makes outbound-only connections. INFERRED and said so in the guide: that the Claude callback must be in the allowed redirect URIs, that the AUD tag location is on the vendor's "Validate JWTs" page (the page was not re-read; the spec's F7 and F8 read it on 2026-11-19), the whole connector flow (F9, stays INFERRED until MT-001), and the 125 s edge timeout (spec F15, not re-read). The Cloudflare "published application route" pages were not found, so the guide says "route one public hostname" and points at the Tunnel page, with no clicks.

## Spec discrepancies, product gaps, deliberate choices

1. **Spec 7, MT-004 is wrong as written** ("the service is restarted mid-command and the command reads `lost`"): `systemctl restart` is a graceful stop and records `killed`. MT-004 ends the gateway with `systemctl kill --signal=SIGKILL yasshm` so the restart finds a `running` record. Fixed with a STATE AS OF 2026-11-20 callout in spec section 7.
2. **The results-file test accepts empty or valid logfmt**, not strictly empty: a strict "empty" assertion would fail the first time the operator runs a test and commits the line, which is what the file is for. The loop never runs them, and the files are empty now.
3. **Unit paths:** explicit `ReadWritePaths` and not `StateDirectory`/`LogsDirectory` (reason in the unit's header and the spec 8 callout). `SystemCallErrorNumber=EPERM` so a call the filter missed does not kill the gateway.
4. **The unit has never been started.** `systemd-analyze verify` proves syntax, not that `@system-service` minus `@privileged @resources`, `MemoryDenyWriteExecute`, `ProtectProc=invisible` and `ProcSubset=pid` leave the Go binary everything it needs. First start under real systemd is attended; if the service fails there, the journal will say which call or path, and the lead should list it in `Blocked on user`. Not in the manual tests (spec lists five).
5. **`release.yml` is untested live** (a tag is the operator's). It uses `gh release create --verify-tag --generate-notes`, checks the tag is on the default branch with `git merge-base --is-ancestor` against `origin/<default>` (the checkout has `fetch-depth: 0`), takes `VERSION` from the tag without the `v`, and a tag with a hyphen becomes a prerelease. The action SHAs are the ones in `ci.yml`. Release files are therefore named `yasshm_0.1.0_linux_amd64` (no `v`).
6. No product bug found. One operational fact the guide states: a `lost` command cannot be killed by the gateway (no live handle; `Kill` returns delivered false with no audit event); the operator must end it on the target. That is as designed, not a defect.
7. `TestWorkflowNoInterpolationInRun`'s sad subtest only shows the parser sees `${{`; the real assertion is on the shipped workflows.
8. The placeholder test and the doc tests read `git ls-files --cached --others --exclude-standard`, so a new untracked file is checked before `git add`.

## Gate tail (clean tree, head `934c583`)

```text
== deploy ==
deploy: systemd-analyze verify passed on deploy/yasshm.service (systemd 255 (255.4-1ubuntu8.18))
== docs ==
docs: 4 documentation checks passed
== cover ==
cover: total 94.7% (floor 85%, internal/testkit excluded)
== vuln ==
No vulnerabilities found.
== build ==
build: dist/yasshm_934c583_linux_amd64
build: dist/yasshm_934c583_linux_arm64
build: dist/SHA256SUMS
== arch ==
(validate, format check and diagram staleness passed)
check.sh: all OK
```

## What the quick start actually printed here

Run against a config made from the example with a generated key: `config check` printed `config ok: 1 tenant, 1 server`; the example as shipped printed the two placeholder problems (`private_key_file`, `host_key`) and exited 1; `serve` logged `config loaded`, the `certs_url` warning, `listening` (write_timeout_seconds 95 with the defaults), `readiness ready:false status:"key set not loaded"`, the SIGHUP warning, `shutting down` and `stopped`, and the audit log held `gateway.started` and `gateway.stopped`. `/healthz` and `/mcp` were not requested from the shell (no `curl` here); their answers in the docs come from the code and `TestE2EHealthz`.

## Not verified and left out

Nothing was left out for lack of verification except dashboard clicks (see vendor facts). The guide's claim that Managed OAuth needs the Claude callback in the allowed URIs is marked INFERRED.

## CI, log and status

CI (`check`, includes the real-`sshd` lane and the new `deploy` and `docs` steps) on `7a7a41a`: completed success. `934c583` failed at `deploy` as described above and was fixed by `026dbfb`. CI does not run `release.yml`.

`git log --oneline 038cdc0..7a7a41a`:

```text
7a7a41a docs(phase-7): add the implementer report
026dbfb fix(gate): ignore systemd-analyze warnings about the host's own units
bacdf45 docs: do not claim where the dashboard shows the AUD tag
934c583 docs: add the operator guide, the install steps, the threat model and the quick start
a47117d test(manual): add MT-001 to MT-005 and the check of their format
cf2af7b feat(deploy): add the hardened unit, the example config, the release workflow and their checks
```

One more commit follows this section (the CI line itself). `git status --porcelain` was empty after the gate on `7a7a41a`.
