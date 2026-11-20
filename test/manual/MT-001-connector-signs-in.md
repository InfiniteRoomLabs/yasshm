# MT-001 -- a Claude connector signs in through the real edge and list_servers answers

Added in 0.1 for spec section 7 and F9 (the end-to-end flow of a Claude custom connector through Cloudflare Access with Managed OAuth is INFERRED until this passes); the offline half is `TestE2EAuth` and `TestE2EEveryTool` in `test/e2e`, which drive the real binary through a stand-in edge.

- **Runner:** a Claude Code session with the yasshm connector registered as an MCP server (a remote HTTP server at the public URL), plus a human. Steps 2 and 4 are manual (the sign-in in a browser, and a look at the audit log on the gateway host); step 3 is the one tool step. The runner's client must show each tool result's structured content (`structuredContent`): the `jq` steps read it, and a client that shows only the text blocks (a gateway header and raw output, not JSON) cannot run them, which is BLOCKED with note `no_structured_content`, noticed at the first tool step (the first live call is then spent: `live` says so).
- **Placeholders:** `<hostname>` -- `MT_HOSTNAME`, the gateway's public hostname; `<server-id>` -- `MT_SERVER_ID`, the id of a configured server; `<operator-email>` -- `MT_OPERATOR_EMAIL`, the Access identity that signs in. Values live in the gitignored `mise.local.toml` `[env]` block, are added to the redaction term list before the first commit that could mention them (`<server-id>` is a common word and is caught by the run-time oracle only), and never appear in a committed file.
- **Live budget:** 1 tool call (step 3) and 1 navigation (the sign-in of step 2, done by the human and counted in `live`), so `live=2`. No retries, no polling, no loops. Do not call `run_command`, `read_output`, `kill_command`, `download_file` or `get_server_info` inside a run: they are not the thing under test. A BLOCKED that needs the browser sign-in repeated (an expired Access session, an MFA prompt) is cleared between runs by the human, and the next run is a new line.
- **Automate when:** a headless MCP client can complete Access's Managed OAuth sign-in against a test application without a human, and Anthropic's connector sign-in can be scripted. Neither exists, which is what blocks this test: it needs the real edge, the real Access application and Claude's own connector. The origin half (token verification, tenant mapping, the tools) is already automated.
- **Preconditions:** the gateway is deployed and `/healthz` answers `ok` on its host; the Access application has Managed OAuth on and a policy that admits `<operator-email>`; the connector is not yet added in this Claude account, or was removed; Bash steps run from the repository root; steps 2 to 4 run back to back (an Access session can expire between them, which is BLOCKED).

## Steps

### 1. The placeholders are set
Do (Bash): `mise exec -- sh -c 'test -n "$MT_HOSTNAME" && test -n "$MT_SERVER_ID" && test -n "$MT_OPERATOR_EMAIL" && echo set'`
Expect: exactly one line, `set`. No output or a non-zero exit -> BLOCKED with note `placeholder unset`.
Record: nothing

### 2. Add the connector and sign in (live 1, navigation 1)
Do (manual): In Claude, go to Customize, Connectors and add a custom connector with the URL `https://<hostname>/mcp`, signing in as `<operator-email>` in the browser when asked.
Expect: yes or no: does the connector show Connected? No, an Access error page, or a prompt for a second factor -> BLOCKED with note `signin_incomplete`. A connector that shows Connected and then loses its tools is a FAIL in step 3, not here.
Record: `signin=connected`

### 3. list_servers answers (live 2)
Do (tool): `list_servers {}`, the result's structured content written verbatim to `<scratch>/MT-001/step3.json`
Expect: `mise exec -- sh -c 'jq -e --arg id "$MT_SERVER_ID" "(.servers | length) >= 1 and any(.servers[]; .id == \$id and .access == \"look\")" <scratch>/MT-001/step3.json'` exits 0. A tool error object (`code` present) -> FAIL, record `kind` only. A transport error or an authorization error before any result -> BLOCKED with note `connector_error`.
Record: `servers=ge1`, `access=look`

### 4. The audit log shows the sign-in and the call
Do (manual): On the gateway host, run `tail -n 40 /var/log/yasshm/audit.jsonl` and read the lines from the last two minutes.
Expect: yes or no: is there an `auth.accepted` event with the principal `user:<operator-email>`, followed by a `tool.called` event with `"tool":"list_servers"`, and no `auth.rejected` event with the reason `unmapped_principal` or `wrong_audience` in that stretch? A no -> FAIL.
Record: `audited=yes`

## Result

Append one line to `test/manual/MT-001-connector-signs-in.results`:
`ts= test=MT-001 repo= result= version= build= runner= live= failed= signin= servers= access= audited= [note=""]`
