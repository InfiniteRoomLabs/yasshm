# MT-002 -- on the real target the look account is unprivileged, a denylisted command is refused, and both are in the audit log

Added in 0.1 for spec section 7 (the `look` level is an unprivileged account; the denylist is a guard rail); the offline half is `TestE2EPolicyDenied` and `TestE2EEveryTool` in `test/e2e` and the policy tables in `internal/policy`, which cannot say anything about a real target's accounts.

- **Runner:** a Claude Code session with the yasshm connector registered as an MCP server. Step 6 is manual (the audit log is on the gateway host). The runner's client must show each tool result's structured content (`structuredContent`): the `jq` steps read it, and a client that shows only the text blocks (a gateway header and raw output, not JSON) cannot run them, which is BLOCKED with note `no_structured_content`, noticed at the first tool step (the first live call is then spent: `live` says so).
- **Placeholders:** `<server-id>` -- `MT_SERVER_ID`, the id of the real target's server entry. The value lives in the gitignored `mise.local.toml` `[env]` block, is a common word and is caught by the run-time oracle only (it cannot be listed in the redaction term list), and never appears in a committed file.
- **Live budget:** 4 tool calls (steps 2 to 5), 0 navigations, so `live=4`. No retries, no polling, no loops. Do not call `list_servers`, `read_output`, `kill_command`, `list_commands`, `download_file` or `get_server_info` inside a run. Step 3 is refused by the gateway before it reaches the target; it is counted because it is a live call. A BLOCKED that needs the connector re-authorised is cleared between runs by the human.
- **Automate when:** a throwaway target account with the same shape as the real one (an unprivileged login, outside every privileged group, no `sudo` rights) can be provisioned in CI. The gateway's own refusal half is already automated; what blocks this test is that the thing under test is the real target's account configuration, which the build never reaches.
- **Preconditions:** the gateway is deployed, the connector is signed in, and the target server `<server-id>` is configured with the unprivileged account, whose gateway key line is in the root-owned file `/etc/ssh/authorized_keys/<account name>` that `deploy/README.md` step 4 sets up; MT-001 passed for this version; Bash steps run from the repository root; steps 2 to 5 run back to back.

## Steps

### 1. The placeholders are set
Do (Bash): `mise exec -- sh -c 'test -n "$MT_SERVER_ID" && echo set'`
Expect: exactly one line, `set`. No output -> BLOCKED with note `placeholder unset`.
Record: nothing

### 2. The account is not root and holds no privileged group (live 1)
Do (tool): `run_command {"server": "<server-id>", "command": "id -u; id -Gn", "wait_seconds": 20}`, the result's structured content written verbatim to `<scratch>/MT-002/step2.json`
Expect: `jq -e '.status == "succeeded" and .exit_code == 0 and ((.stdout | split("\n"))[0] != "0") and ([((.stdout | split("\n"))[1] | split(" "))[] | select(. == "root" or . == "sudo" or . == "wheel" or . == "adm" or . == "docker" or . == "lxd" or . == "libvirt" or . == "kvm" or . == "disk" or . == "shadow")] | length == 0)' <scratch>/MT-002/step2.json` exits 0. A tool error object (`code` present) -> FAIL, record `kind` only; a connector or authorization error -> BLOCKED with note `connector_error`.
Record: `uid=nonzero`, `groups=unprivileged`

### 3. A denylisted command is refused (live 2)
Do (tool): `run_command {"server": "<server-id>", "command": "sudo -n true", "wait_seconds": 5}`, the result's single text block written verbatim to `<scratch>/MT-002/step3.json`
Expect: `jq -e '.code == "policy_denied" and .rule == "sudo"' <scratch>/MT-002/step3.json` exits 0. A success result (the command ran) -> FAIL, and the unprivileged account on the target needs a look too.
Record: `denied=policy_denied`, `rule=sudo`

### 4. The account cannot write a system file (live 3)
Do (tool): `run_command {"server": "<server-id>", "command": "test -w /etc/passwd && echo writable || echo not-writable", "wait_seconds": 20}`, the result's structured content written verbatim to `<scratch>/MT-002/step4.json`
Expect: `jq -e '.status == "succeeded" and (.stdout | rtrimstr("\n")) == "not-writable"' <scratch>/MT-002/step4.json` exits 0. `writable` -> FAIL.
Record: `etc_passwd=not-writable`

### 5. The account cannot change which keys log in as it (live 4)
Do (tool): `run_command {"server": "<server-id>", "command": "if test -w \"/etc/ssh/authorized_keys/$(id -un)\" || test -w /etc/ssh/authorized_keys; then echo writable; else echo not-writable; fi", "wait_seconds": 20}`, the result's structured content written verbatim to `<scratch>/MT-002/step5.json`
Expect: `jq -e '.status == "succeeded" and (.stdout | rtrimstr("\n")) == "not-writable"' <scratch>/MT-002/step5.json` exits 0. `writable` -> FAIL: the assistant could add a key or remove `restrict`, opening a login that never passes the edge, the token check or the audit log. A file that does not exist is also `not-writable`, so a target set up the old way (the key line in the account's own `~/.ssh/authorized_keys`) is not caught here: the precondition says where the file must be.
Record: `keyfile=not-writable`

### 6. All of it is in the audit log
Do (manual): On the gateway host, run `tail -n 80 /var/log/yasshm/audit.jsonl` and read the lines from the last five minutes.
Expect: yes or no: is there a `command.requested` event for each of the three commands that ran (`id -u; id -Gn`, the `/etc/passwd` test and the key file test), a `policy.denied` event with `"rule":"sudo"` for `sudo -n true` and no `command.requested` event for it, and a `command.finished` event with `"status":"succeeded"` for each of the three that ran? A no -> FAIL.
Record: `audited=yes`

## Result

Append one line to `test/manual/MT-002-look-account-unprivileged.results`:
`ts= test=MT-002 repo= result= version= build= runner= live= failed= uid= groups= denied= rule= etc_passwd= keyfile= audited= [note=""]`
