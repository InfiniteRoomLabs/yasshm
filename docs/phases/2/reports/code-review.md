# Phase 2 (SSH layer) -- code-review lane

Range `2300f0e..dab8dcc`. Read-only review; nothing was run. Checked against `x/crypto` v0.59.0 and `pkg/sftp` v1.13.12 in the module cache.

## Verdict: REQUEST CHANGES

3 BLOCKING, 7 ADVISORY. The design is sound and the callouts are honest; the blockers are small and local.

## BLOCKING

1. **Unbounded waits on a stalled or vanished server.** `internal/sshclient/command.go:270` -- `sess.SendRequest("signal", true, ...)` blocks on the reply with no bound (`x/crypto` `channel.go:694`). It is called from `stop` (`:210`, `:218`) and `Signal` (`:290`). When the target drops off the network or `sshd` hangs, a deadline fires, TERM is written and `Wait` then blocks until TCP gives up (about 15 minutes with unacked data; forever on a stalled peer): the grace, KILL and close are never reached, and the job keeps its slot long past its timeout. Same class in `Start`: `client.go:225` clears the conn deadline and nothing watches `ctx` afterwards, so `client.NewSession()` (`command.go:128`) and the want-reply `exec` (`:142`) are unbounded too, and the caller has no handle to close. Fix: give `sendSignal` a bound (`t := time.AfterFunc(bound, func() { cmd.closeConn(true) }); defer t.Stop()`; KillGrace for TERM, KillWait for KILL and `Signal`); in `Start` keep a `context.AfterFunc` on a ctx-plus-ConnectTimeout context that closes the client until `sess.Start` returns, mapped with `ctxErr` or `ssh_unreachable`. Tests need two `sshtest` switches (never answer `signal`; accept auth, then never answer channel-open or `exec`).

2. **The host name and port reach `Error()`.** `client.go:130` appends `hostkey.Error.Msg`, and three of those messages end in `host:port` (`internal/hostkey/hostkey.go:158`, `:161`, `:173`). A `known_hosts` file edited after start (it is re-read at every connect) gives "... lists no key for <host>:<port>" in a string documented as safe for a tool result (D8, package doc, 5.5 callout, changelog). `client_test.go:248-254` exercises it and line 279 only checks for `/`. Fix: a fixed message in `pin` ("the pinned known_hosts file cannot be used"), `he` kept as the cause only; assert `!strings.Contains(err.Error(), srv.Host)` in every `TestPinningAndKeys` case. While there, drop the uid numbers of `keyfile.OwnerError` from the message at `client.go:147`.

3. **`Download` opens before it knows what the path is.** `sftp.go:98` opens, then `fstat`s. On a FIFO the server's `open(2)` blocks: the call hangs until `ctx` ends (forever without a deadline) and leaves a blocked `sftp-server` on the target, since closing does not kill it (F10). Opening a device node can have side effects. `download_file` is a v0.1 `look` tool. Fix: `s.sc.Stat(remotePath)` first and return `ErrNotRegular` unless regular, keeping the `fstat` after open. Add FIFO and `/dev/null` cases to the contract suite (both servers).

## ADVISORY

4. `command.go:339-341`: the `default` branch is reached only when `sess.Wait` returns a copy error, which `x/crypto` does only when the exit status was 0 (`session.go:414-417`). So "exit 0, an output writer returned an error" is reported as `no_status`, `ssh_unreachable`, `MayStillRun`. Report `exited` code 0 with an `internal` error ("an output writer failed"). Document in `Start` (`:116`) that a writer must not fail or block forever: a failed writer stops draining, the window fills and the command stalls; a writer blocked forever keeps `Wait` and `Close` blocked even after the connection is closed.

5. `client.go:242`: the text is right for v0.59.0 (`client_auth.go:154`), but a peer's disconnect message is echoed into the same error string, so an impostor can produce `ssh_auth_failed` before its host key was ever checked. Set a flag when `pinned.Check` returns nil and require it for this case. The fallback (`ssh_unreachable`) is safe.

6. `sftp.go:110-114` trusts `st_size`: procfs and sysfs files report 0, so they download as empty with `eof=true`. Read up to `max` until EOF when size is 0, or record the limitation in the callout.

7. `sftp.go:126-133,186`: "a partial file it created is removed" is false when the failure is the connection closing (context end, lost link): `Remove` fails too and, without overwrite, the partial file stays at the final path. Correct the doc, callout and changelog.

8. Tests. `contract_test.go:179-182` and `client_test.go:333-335` put connect plus run under 300 ms and 100 ms deadlines and assert a run outcome: under `-race` on a loaded runner the connect can eat the budget (use `Start` with a background context, then `Wait` with the deadline). `client_test.go:213-225` dials 192.0.2.1 (GOAL: nothing beyond loopback) and accepts either code. `contract_test.go:227-230` returns early without `setsid`: a skip that `skipcheck` cannot see and the `sshd` step counts as PASS; fail when `realsshd.Required()`. `client_test.go:538` is misnamed and never reaches the no-subsystem branch. Nothing covers findings 1 to 4, an upload onto a symlink, or a context ending mid-transfer.

9. `sshtest` fidelity. Beyond the missing stall switches: `sshtest.go:407` (`WaitDelay`) ends the channel 2 s after the shell exits even when a background child still holds stdout; real `sshd` keeps the channel open until the pipe closes, so `Wait` runs to the deadline and TERM is then refused (no pid). Phase 3 will meet this; add a contract scenario so both servers pin it.

10. Nits. `sftp.go:91,108,140` say "the remotePath" (a rename leaked into messages). `probe.go:98`: `s < 1e10` overflows `int64` nanoseconds (bound it below 9e9). `command.go:210-223`: when TERM is refused, skip the grace and KILL wait. `hostkey.go:165-173`: a file with only certificate-typed lines reports "lists only revoked keys".

## The implementer's choices

1 right (messages byte-identical; `O_NONBLOCK` and ignoring certificate-typed lines are the only behaviour changes, both stricter). 2 right; Phase 3 must key handles by `cmd.Tenant()`. 3 right. 4 right. 5 right. 6 right. 7 acceptable with finding 5. 8 right, minus finding 7. 9 right. 10 right, and bounded.

Open questions: no sixth code -- Phase 3 should branch on `ExitStatus.Stopped` and `errors.Is(err, context.Canceled)`. Keep `KillWait` a constant; a config key means spec 5.1, `limits.go` and `TestParseLimits` churn for no operator value.

## Checked and found sound

Callouts (5.5, 7, F10, section 4): fair records, no locked decision redesigned; the "never the host" and "partial file removed" sentences become true with findings 2 and 7. Lifecycle: double `Close`, `Wait` twice, `Signal` after exit, `Close` during `Wait`, one stop sequence, timers stopped, connect deadline applied then cleared, every dial error path closes the conn, the keepalive probe is bounded. No PTY, env, agent or forwarding; stdin EOF; back-pressure real (one channel window). Certificates refused at load, in `HostKeyAlgorithms` and in the callback. Gate: the `sshd` step cannot pass on zero scenarios, fails under `YASSHM_REQUIRE_SSHD=1` without `sshd`, CI sets it; shell is correct under `set -euo pipefail`. `deprule`, `#planned`, README, TESTING and CLAUDE.md match the code.
