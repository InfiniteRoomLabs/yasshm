# MT-003 -- a long command returns a command id inside the wait window through the real edge, is paged and killed, and the process is gone on the target

Added in 0.1 for spec section 7 and F15 (the edge closes a request whose origin sends nothing for 125 seconds, and a tunnel buffers a response that is not an event stream; this is observed here for the first time); the offline half is `TestE2ELongCommand`, `TestE2ETimeout` and `TestE2EGracefulShutdown` in `test/e2e` and the real-`sshd` contract suite for the signal (F10).

- **Runner:** a Claude Code session with the yasshm connector registered as an MCP server. Step 7 is manual (the process list is on the target). The runner's client must show each tool result's structured content (`structuredContent`): the `jq` steps read it, and a client that shows only the text blocks (a gateway header and raw output, not JSON) cannot run them, which is BLOCKED with note `no_structured_content`, noticed at the first tool step (the first live call is then spent: `live` says so).
- **Placeholders:** `<server-id>` -- `MT_SERVER_ID`, the id of the real target's server entry. The value lives in the gitignored `mise.local.toml` `[env]` block, is a common word and is caught by the run-time oracle only, and never appears in a committed file. Run values, never recorded and never committed: `<command-id>` (the `command_id` of step 2) and `<next-offset>` (the `next_offset` of step 3), kept in the runner's context.
- **Live budget:** 5 tool calls (steps 2 to 6), 0 navigations, so `live=5`. No retries, no polling, no loops: step 6 waits inside one call with `wait_seconds`. Do not call `list_servers`, `list_commands`, `download_file` or `get_server_info`, and do not call `run_command` again, inside a run. A BLOCKED that needs the connector re-authorised is cleared between runs by the human; a command left running by a failed run is killed by the human on the target, between runs.
- **Automate when:** a stand-in for the edge that reproduces Cloudflare's response buffering and its 125 second read timeout exists, and the target is a real `sshd` that CI can reach. Neither does: what blocks this test is that the thing under test is the real edge's timing and a real target. The gateway's half (paging, kill, the process stopped) is automated.
- **Preconditions:** the gateway is deployed with the default limits (`max_wait_seconds` 60), the connector is signed in, MT-001 passed for this version; Bash steps run from the repository root; steps 2 to 6 run back to back.

## Steps

### 1. The placeholders are set
Do (Bash): `mise exec -- sh -c 'test -n "$MT_SERVER_ID" && echo set'`
Expect: exactly one line, `set`. No output -> BLOCKED with note `placeholder unset`.
Record: nothing

### 2. Start a command that outlives the wait (live 1)
Do (tool): `run_command {"server": "<server-id>", "command": "echo mt003-start; for i in $(seq 1 300); do echo line-$i; sleep 1; done # yasshm-mt003-marker", "wait_seconds": 20, "timeout_seconds": 120}`, the result's structured content written verbatim to `<scratch>/MT-003/step2.json`
Expect: `jq -e '.status == "running" and (.command_id | test("^[0-9a-f]{32}$")) and .eof == false' <scratch>/MT-003/step2.json` exits 0, and the answer came back as a result and not as an error from the edge. A gateway timeout, a 524 or a connector error -> FAIL with kind `edge_timeout` (the answer did not survive the edge inside the wait window). A tool error object (`code` present) -> FAIL, record `kind` only.
Record: `status=running`, `inside_window=yes`

### 3. The first page (live 2)
Do (tool): `read_output {"command_id": "<command-id>", "offset": 0, "max_bytes": 100}`, the result's structured content written verbatim to `<scratch>/MT-003/step3.json`
Expect: `jq -e '(.data | startswith("mt003-start")) and .eof == false and .next_offset > 0 and .offset == 0' <scratch>/MT-003/step3.json` exits 0.
Record: `page1=ok`

### 4. The next page continues where the first ended (live 3)
Do (tool): `read_output {"command_id": "<command-id>", "offset": <next-offset>, "max_bytes": 200}`, the result's structured content written verbatim to `<scratch>/MT-003/step4.json`
Expect: `jq -e --argjson prev <next-offset> '.offset == $prev and (.data | length) > 0 and .next_offset > $prev' <scratch>/MT-003/step4.json` exits 0.
Record: `page2=ok`

### 5. Kill it (live 4)
Do (tool): `kill_command {"command_id": "<command-id>", "signal": "TERM"}`, the result's structured content written verbatim to `<scratch>/MT-003/step5.json`
Expect: `jq -e '.signal_delivered == true' <scratch>/MT-003/step5.json` exits 0. `signal_delivered` false while the status is still `running` -> FAIL (the target did not accept the signal).
Record: `signal=delivered`

### 6. It ends as killed (live 5)
Do (tool): `read_output {"command_id": "<command-id>", "offset": 1000000000, "max_bytes": 100, "wait_seconds": 10}` (an offset past the end is clamped to the end, so the call has nothing to return and waits, inside this one call, until the command ends or 10 seconds pass), the result's structured content written verbatim to `<scratch>/MT-003/step6.json`
Expect: `jq -e '.status == "killed" and .eof == true' <scratch>/MT-003/step6.json` exits 0 (`eof` is true only when the command has ended and the page reaches the end of its output, which is why the offset is past the end). A status of `running` after the wait -> FAIL.
Record: `status=killed`

### 7. The process is gone on the target
Do (manual): On the target, as any account, run `pgrep -f yasshm-mt003-marker`.
Expect: yes or no: does it print nothing and exit 1? Any pid -> FAIL, and the human ends the process on the target before the next run.
Record: `remote=gone`

## Result

Append one line to `test/manual/MT-003-long-command-through-the-edge.results`:
`ts= test=MT-003 repo= result= version= build= runner= live= failed= status= inside_window= page1= page2= signal= remote= [note=""]`
