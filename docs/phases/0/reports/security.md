# Phase 0 security report

Range `34d6001..93674e8`, branch `claude/serene-franklin-jv4pu7`. Read-only review; no gate, tests or builds run.

**Verdict: PASS** (no blocking findings; four advisories).

## Checked and clean

- **Supply chain:** `go list -m all` prints only `github.com/InfiniteRoomLabs/yasshm` (no dependencies). `mise.toml:7-15` pins every tool, govulncheck at `1.10.0` via the `go:` backend; `@latest` appears only in comments. `ci.yml:20,21,27` pin actions by full SHA, `permissions: contents: read` (line 12), triggers are `pull_request`/`push`/`workflow_call` (no `pull_request_target`), and the new sshd step (`ci.yml:34-38`) has no `${{ }}` in its shell. `bootstrap.sh:17` still installs mise only from `https://mise.run`.
- **Build:** `build.sh:23-24` uses `CGO_ENABLED=0` and `-trimpath`; `-ldflags "$ldflags"` is quoted as one argument; `rm -rf dist` then `sha256sum yasshm_*` inside `dist/` covers exactly the two built files.
- **Shell:** all four scripts have `set -euo pipefail`; no `eval`; no temp files. `fmt`'s `bad=$(... | xargs gofmt -l)` propagates failure (pipefail, `local` declared separately). The dirty-tree check now exits 1.
- **Lint config:** `gosec` enabled, no `exclusions.rules` or presets; `nolintlint` has `require-specific: true`, `require-explanation: true`.
- **Hygiene:** diff, implementer report and commit messages contain no host, IP, domain, tenant/account id, vault item, token, key or personal name; only the public org and the session/co-author trailers. `.gitignore:35,37` cover `dist/` and `coverage*.out`. Nothing reads or needs a secret (D16).

## Findings

1. **ADVISORY** -- `.golangci.yml:49` `exclusions: generated: lax`. Lax mode treats any file containing a phrase like "do not edit" or "code generated" in a comment as generated and drops every linter on it, `gosec` included, so a hand-written file can silently leave the security lint. Fix: `generated: strict` (only the standard `// Code generated ... DO NOT EDIT.` header).
2. **ADVISORY** -- `check.sh:36-43` skipcheck. (a) A method value (`skip := t.Skip; skip()`) or a skip in a non-`_test.go` helper outside `internal/testkit` is not matched. (b) `xargs` splits file names on whitespace and `|| true` masks grep exit 2, so an unreadable or space-named file passes silently. Fix: `git ls-files -z ... | xargs -0r grep ...`, treat grep exit status >1 as failure, and widen the pattern to `\.(Skip|Skipf|SkipNow)\b` and `testing\.Short\b`. Also consider flagging `//go:build ignore`-style tags on `_test.go` files, which silence a whole file.
3. **ADVISORY** -- `build.sh:11,15,20`. `VERSION` from the environment is spliced into the ldflags string and the output path unvalidated: Go splits `-ldflags` on spaces, so `VERSION='1 -X <pkg>.Commit=x'` forges metadata or injects linker flags, and a `/` or `..` writes outside `dist/`. Operator-controlled, so low risk. Fix: reject `VERSION` not matching `^[0-9A-Za-z][0-9A-Za-z._+-]*$`.
4. **ADVISORY (pre-existing)** -- `ci.yml:21-23` mise-action has no `version:`, so CI installs whatever mise is current. Fix: pin `version:` to the local mise version.

## Not applicable in Phase 0

- Tokens/OAuth: no MCP server or auth code yet.
- Tenancy: no tenants, policy or data model yet.
- SSH transport: no SSH client, key handling or host-key checks yet; the sshd step only installs a package.
- Audit: no audit log yet.
