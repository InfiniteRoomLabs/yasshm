# MT-004 -- when the gateway dies mid-command the restarted gateway reads the command as lost

Added in 0.1 for spec section 7; the offline half is `TestE2ELostAfterKill` in `test/e2e` (the gateway killed with SIGKILL, restarted on the same data directory) and `internal/jobs` `TestRecover`. A plain `systemctl restart` is a graceful stop and records the command as `killed` (`TestE2EGracefulShutdown`), so this test ends the gateway the way a crash does.

- **Runner:** a Claude Code session with the yasshm connector registered as an MCP server, plus a human on the gateway host and on the target. Steps 3, 4, 6 and 7 are manual (the kill, the wait for the restart and the process list on the target).
- **Placeholders:** `<server-id>` -- `MT_SERVER_ID`, the id of the real target's server entry. The value lives in the gitignored `mise.local.toml` `[env]` block, is a common word and is caught by the run-time oracle only, and never appears in a committed file. Run value, never recorded or committed: `<command-id>`, the `command_id` of step 2.
- **Live budget:** 2 tool calls (steps 2 and 5), 0 navigations, so `live=2`. No retries, no polling, no loops. Do not call any other tool inside a run. A BLOCKED because the service did not come back is cleared between runs by the human (start it, read the journal), and the next run is a new line; a command left running on the target is ended there by the human (step 7).
- **Automate when:** a throwaway target and a real systemd unit can be run in CI (a container with systemd and an `sshd`). The gateway's crash recovery is automated against the real binary; what blocks this test is the real unit's restart behaviour (`Restart=on-failure` after a SIGKILL) and a real target.
- **Preconditions:** the gateway is deployed with the shipped unit, the connector is signed in, MT-001 passed for this version, you have root on the gateway host; Bash steps run from the repository root; steps 2 to 5 run back to back.

## Steps

### 1. The placeholders are set
Do (Bash): `mise exec -- sh -c 'test -n "$MT_SERVER_ID" && echo set'`
Expect: exactly one line, `set`. No output -> BLOCKED with note `placeholder unset`.
Record: nothing

### 2. Start a long command (live 1)
Do (tool): `run_command {"server": "<server-id>", "command": "sleep 600 # yasshm-mt004-marker", "wait_seconds": 5, "timeout_seconds": 900}`, the result's structured content written verbatim to `<scratch>/MT-004/step2.json`
Expect: `jq -e '.status == "running" and (.command_id | test("^[0-9a-f]{32}$"))' <scratch>/MT-004/step2.json` exits 0. A tool error object (`code` present) -> FAIL, record `kind` only.
Record: `started=running`

### 3. End the gateway the way a crash does
Do (manual): On the gateway host, as root, run `systemctl kill --signal=SIGKILL yasshm`.
Expect: yes or no: did the command finish without an error? No -> BLOCKED with note `kill_failed`.
Record: nothing

### 4. The service comes back
Do (manual): On the gateway host, wait for the unit's restart delay (5 seconds) and run `systemctl is-active yasshm`.
Expect: yes or no: does it print `active`? `activating`, `failed` or anything else -> BLOCKED with note `service_not_back` (the unit restarts a process killed by a signal; if it did not, the start limit may have stopped it, and `systemctl reset-failed yasshm` clears that between runs).
Record: nothing

### 5. The command reads as lost (live 2)
Do (tool): `read_output {"command_id": "<command-id>", "offset": 0, "max_bytes": 100}`, the result's structured content written verbatim to `<scratch>/MT-004/step5.json`
Expect: `jq -e '.status == "lost" and .eof == true and .may_still_run == true' <scratch>/MT-004/step5.json` exits 0. Status `running` or `killed` -> FAIL. A connector error because the sign-in lapsed -> BLOCKED with note `connector_error`.
Record: `status=lost`, `may_still_run=true`

### 6. What the target did
Do (manual): On the target, as any account, run `pgrep -f yasshm-mt004-marker`.
Expect: yes or no: does it print a pid? Either answer passes; it is recorded because "lost" means nobody stopped the remote process, and this is the one place that shows what an `sshd` does when the connection drops.
Record: `remote=running` or `remote=gone`

### 7. Clean up the target
Do (manual): On the target, as the same account that runs the commands, run `pkill -f yasshm-mt004-marker`, then `pgrep -f yasshm-mt004-marker` again.
Expect: yes or no: does the second command print nothing and exit 1? No -> FAIL with note `cleanup_failed` (a process is still running on the target).
Record: nothing

## Result

Append one line to `test/manual/MT-004-crash-marks-running-command-lost.results`:
`ts= test=MT-004 repo= result= version= build= runner= live= failed= started= status= may_still_run= remote= [note=""]`
