# MT-005 -- the public hostname refuses a request that has not signed in

Added in 0.1 for spec section 7 and D3 and D4 (the edge is the first lock, the gateway's own token check the second); the offline half is `TestE2EAuth`, `TestE2EHTTPSurface` and `internal/mcpserver` `TestBehindTheEdge`, which prove the second lock with a stand-in edge and cannot say anything about the first.

- **Runner:** human, or a Claude Code session with Bash and network access. Every step is Bash.
- **Placeholders:** `<hostname>` -- `MT_HOSTNAME`, the gateway's public hostname. The value lives in the gitignored `mise.local.toml` `[env]` block, is added to the redaction term list before the first commit that could mention it, and never appears in a committed file.
- **Live budget:** 2 live calls (steps 2 and 4), both navigations (a request to the public edge, which it logs as activity), so `live=2`. No retries, no polling, no loops. The request carries no token and no cookie on purpose: do not sign in, and do not call any MCP tool, inside a run. A BLOCKED because the hostname does not resolve is cleared between runs by the human.
- **Automate when:** a test Cloudflare account and a throwaway tunnel can be created from CI. What blocks this test is that it needs the real edge and its Access application, which the build never reaches.
- **Preconditions:** the gateway is deployed and reachable through the tunnel (MT-001 passed for this version); `curl` is installed; Bash steps run from the repository root; steps 2 to 5 run back to back.

## Steps

### 1. The placeholders are set
Do (Bash): `mise exec -- sh -c 'test -n "$MT_HOSTNAME" && mkdir -p <scratch>/MT-005 && echo set'`
Expect: exactly one line, `set`. No output -> BLOCKED with note `placeholder unset`.
Record: nothing

### 2. An unsigned MCP request is refused (live 1, navigation 1)
Do (Bash): `mise exec -- sh -c 'curl -sS --max-time 20 -o <scratch>/MT-005/mcp.body -w "%{http_code}\n" -X POST "https://$MT_HOSTNAME/mcp"'`
Expect: exactly one line matching `^(302|401|403)$`. A `200` or any other 2xx -> FAIL. A name-resolution or connection failure -> BLOCKED with note `unreachable`.
Record: `mcp_status=refused`

### 3. The edge answered, not the gateway
Do (Bash): `grep -cF '{"error":"unauthorized"}' <scratch>/MT-005/mcp.body`
Expect: exactly one line, `0`. A `1` means the request got through the edge and only the gateway's own check stopped it: the edge is misconfigured -> FAIL with note `edge_passed_unsigned` (the second lock held, the first did not).
Record: `answered_by=edge`

### 4. An unsigned health request is refused too (live 2, navigation 2)
Do (Bash): `mise exec -- sh -c 'curl -sS --max-time 20 -o <scratch>/MT-005/health.body -w "%{http_code}\n" "https://$MT_HOSTNAME/healthz"'`
Expect: exactly one line matching `^(302|401|403)$`. A `200` -> FAIL: the health endpoint is reachable without signing in.
Record: `health_status=refused`

### 5. The scratch files are removed
Do (Bash): `rm -r <scratch>/MT-005`
Expect: no output and exit 0.
Record: nothing

## Result

Append one line to `test/manual/MT-005-public-hostname-refuses-unsigned-requests.results`:
`ts= test=MT-005 repo= result= version= build= runner= live= failed= mcp_status= answered_by= health_status= [note=""]`
