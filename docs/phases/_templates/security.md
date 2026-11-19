# Work order: security lane

Dispatch: `Agent(subagent_type: "general-purpose", model: "<one tier above the implementer>", name: "phase-<n>-security")`. Runs in parallel with the code-review and simplification lanes. Read-only (vulnerability scanners that do not write to the tree are allowed).

---

You are the **security lane** of a four-lane review gate for Phase <n> in `<absolute path of the clone>` (`git diff <phase base sha>..HEAD` on branch `<working branch>`). **READ-ONLY:** do not modify files, do not commit, and do NOT run `mise run check`, tests, or builds. You may run `git`, `grep`, `mise exec -- go list -m all`, and `mise exec -- govulncheck ./...`, and read files. Threat context: yasshm holds SSH private keys and runs commands on other machines on behalf of an AI model, and this repository is public.

## Context

- Spec: `docs/superpowers/specs/2026-11-18-yasshm-design.md` sections 2 (D3 to D8, D12 to D14), 6, and <the component sections this phase builds>. Conventions: `CLAUDE.md`.

## Check, with evidence

1. **Secrets never leak:** tokens, private keys, the `Cf-Access-Jwt-Assertion` header and key file contents must not appear in logs at any level, error strings, panics, tool results, the audit log, test fixtures, golden files, or doc examples. Command text belongs in the audit log only; command output belongs in the output store only. Grep logging calls, error formatting, and `String()`/`MarshalJSON` methods on credential-bearing types.
2. **Credential and data storage:** restrictive file permissions (keys and audit log 0600, data directory 0700), a key readable by group or other is refused, atomic writes, no world-readable defaults, no secrets in committed config or examples.
3. **Authentication and tenancy:** no handler runs without a verified principal; the token is verified with a maintained library and RS256 only (no `none`, no HS256 with the public key); issuer, audience and time claims are all checked; an unknown `kid` cannot make the gateway refetch keys without a limit; every store query and output path is scoped by tenant id; another tenant's id returns `not_found`.
4. **Transport and SSH:** TLS never disabled; host key checking never disabled and never trust-on-first-use; no agent or port forwarding, no PTY; timeouts set on dials, sessions and HTTP; request and response sizes bounded.
5. **Trust boundaries:** all external input (tool arguments, HTTP headers, config, SSH output, SFTP paths) is validated or decoded into typed structures; no path traversal from caller-supplied paths; the gateway never runs a caller-supplied string in a local shell; output from a target is treated as untrusted text; nothing from the config file is reachable through a tool; audit-before-act holds (spec D12).
6. **Supply chain:** `go.mod`/`go.sum` changed only by intended additions; every new dependency listed with its justification (spec D14); vulnerability scan clean; CI workflows pin actions by SHA and use least-privilege permissions; the release path cannot be triggered from an unprotected ref.
7. **Hygiene (every repo, every remote):** no real tenant/account IDs, internal hostnames/IPs, vault item names, or personal names anywhere in the diff (fixtures, reports, and the goal file included), and nothing copied from the private infra repo. If one is already in history, say so: the fix is a history rewrite, and the lead must record it for the final history-scrub phase.

## Deliver

Verdict **PASS** or **BLOCK**, findings numbered and tagged **BLOCKING** (must fix before the phase ships) / **ADVISORY**, each with `file:line`, the evidence, and the concrete fix. Write the report to `docs/phases/<n>/reports/security.md` (do not commit), send it with `SendMessage` to `team-lead` (full report in `message`, not `summary`), AND return it as your final text.
