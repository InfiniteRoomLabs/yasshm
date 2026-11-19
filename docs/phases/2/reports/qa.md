# Phase 2 (SSH layer) -- QA lane

Range `2300f0e..5860501` (code head `3f2d8fb`). Throwaway tests and mutations ran in a clone outside the repo (deleted).

## Verdict: NEEDS WORK

4 BLOCKING, 5 ADVISORY. Gate and CI are green and every named invariant's mutation is caught; the blockers are behaviours no test exercises.

## Gate and CI evidence

`mise run check` on `5860501`, `git status --porcelain` empty before and after:

```
== sshd ==
check.sh: sshd is not installed here; the real-sshd lane runs in CI only (spec section 7)
== cover ==
cover: total 94.9% (floor 85%, internal/testkit excluded)
== vuln ==
No vulnerabilities found.
check.sh: all OK
```

Recomputed by hand: 1410 of 1486 statements.

CI for head `5860501`: run `44631648629`, job `135939540471`, `success` . From the job log: `YASSHM_REQUIRE_SSHD: 1` in the step's env; `realsshd: /usr/sbin/sshd listening on 127.0.0.1:<port> as user runner (version: SSH-2.0-OpenSSH_10.2p1 Ubuntu-2ubuntu3.8)`; 25 `--- PASS: TestRealSSHDContract/...` lines (counted; 0 FAIL); `sshd: 26 real-sshd scenarios passed` (25 plus `TestRealSSHDLogin/happy/echo_ok_over_a_real_sshd`); `cover: total 95.0%`. The four `F10 observed` lines: setsid child survived `TERM` (true); `TERM` with want-reply ACCEPTED; `INT HUP QUIT USR1 USR2 KILL` accepted and delivered; command still running after the client closed (true).

## BLOCKING

1. **A stalled server hangs `Exec` past its deadline (reproduced).** `internal/sshclient/command.go:270`, `:128`, `:142`. A server built on `x/crypto/ssh` authenticates, then stalls; `Exec` has a 1 s deadline, grace 300 ms, kill wait 1 s. Expected: return within 3.3 s. Observed: no return after 12 s in all three cases (never answers `signal`; never answers `exec`; never answers channel-open); each returned only when I closed the server's TCP connection. The `exec` case then reported `internal: the server refused the exec request`, not `timeout`.
2. **Host and port in `Error()` (reproduced).** `client.go:130` with `internal/hostkey/hostkey.go:161`, `:173`. A pinned `known_hosts` that no longer lists the server gives `sshclient: exec on server srv-1: host_key_mismatch: the pinned host key cannot be used: lists no key for 127.0.0.1:34335`; a file with only a revoked key gives `... lists only revoked keys for 127.0.0.1:34335`. Expected: neither host nor port.
3. **`Download` of a FIFO hangs (reproduced).** `sftp.go:98`. Without a deadline: no return after 5 s; it returned `ErrNotRegular` only after I opened the FIFO for writing. With a 1 s deadline it returns `timeout` at 1.0 s. `/dev/null` gives `ErrNotRegular`.
4. **A host key of another type is `ssh_unreachable`.** `client.go:246`. Pinned ECDSA, server offers ed25519. Expected (spec 5.5: an unknown or changed key fails with `host_key_mismatch` and is audited): `host_key_mismatch`. Observed: `ssh_unreachable: the SSH handshake failed`, no fingerprint, cause "no common algorithm for host key"; 0 requests sent, so it fails closed. A changed key of the same type gives `host_key_mismatch` with the fingerprint. The security lane rated this advisory (A2); measured, it is a wrong code in a mapping the spec makes part of the contract. No test covers it.

## ADVISORY

5. **The `sshd` step passes when the contract half did not run.** `scripts/check.sh:94-95` needs one PASS line of any `TestRealSSHD*/` subtest. Step logic run on captured output: zero scenario lines -> exit 1 (correct); login subtest only, contract suite absent -> `sshd: 1 real-sshd scenarios passed`, exit 0; one of 25 scenarios -> exit 0. Count `TestRealSSHDContract/` lines against the in-process suite's count.
6. **Mutation survivors (28 run, 4 survived).** The early `checkServer` in `Download` and `Upload` (`sftp.go:87`, `:136`; `dial` repeats it); `mayStillRun` in `closeConn` (`command.go:256-258`; `finish` sets it for every `no_status`); `nc.SetDeadline` (`client.go:199-201`; the `AfterFunc` suffices). Two more are killed only by a hang until the test timeout (10 minutes in the gate): stdin left open, and a refused `Signal` that does not close the session. 
7. **"No process left behind" is not asserted for real `sshd`.** `internal/testkit/realsshd/realsshd.go:316-328` stops the listener only. For `sshtest`, `Close` holds it (mutation killed). Locally, 20 passes left no child process and no `/tmp` entry.
8. **`client_test.go:213-225` does not test what it names here.** It returned in 0.00 s (no route to 192.0.2.1): no cancel happens during a dial in the sandbox. It is the only non-loopback dial.
9. **Docs.** `docs/progress.md:11` lists the gate without `sshd` (ship commit). `CHANGELOG.md` and `errors.go:53-57` say "never the host", false until finding 2 is fixed.

## Deliverables (GOAL stage 2)

Met, with evidence: the five codes; every `Client` method takes a `TenantID` (tenant-check mutants killed); one table of 25 scenarios run against both servers by `runContract`, covering every item of the GOAL list; no PTY, env, agent or forwarding (asserted on `sshtest.Requests()` in `TestConnectRules`; env and PTY mutants killed); stdin closed (scenario on both servers); `deprule` fails on a throwaway import of `config`, `policy`, `audit` or `testkit`; `go list -deps ./cmd/...` has no `testkit`; `restrict` key line, no forced command; `YASSHM_REQUIRE_SSHD=1` without `sshd` fails the step and both tests; `yasshm.ssh` lost `#planned` in `92aae78`, `arch` green; placeholder grep empty; docs ASCII-only. Not met: findings 1 to 4.

## Fidelity (derived first, then observed on `sshtest`)

- Exit 0, 1, 127, 255: `exited` with that code; `kill -KILL $$`: `signaled KILL`, `MayStillRun` false. As derived.
- 1 s deadline, grace 300 ms, `TERM` trapped: derived `TERM` at 1.000 s, `KILL` at 1.300 s; observed 1.000 s, 1.301 s, total 1.302 s; `signaled KILL`, `timeout`.
- 8 MiB on each stream at once, writer taking 64 KiB per 10 ms: 8388608 and 8388608 bytes, 0 wrong, 1.46 s; 1.7 MiB and 1.1 MiB in flight when the producers finished (window 2 MiB); heap peak 5.9 MiB above base.
- `Download`, 1000-byte file, 12 boundary cases (offset 0, size-1, size, above; max 0, 1, size-1, size, above): all as derived.
- `Upload` under umask 077: no overwrite onto an existing file -> `fs.ErrExist`, file unchanged; overwrite -> new content, 0640; new file -> 0640; no temp file left.

## Flakiness, limits

`go test -race -shuffle=on -count=20` of the three packages: 0 failures; one pass 6.3 s wall. The deadline scenarios under 8 busy loops on 4 CPUs: 100 of 100. Nothing deployed, no real host, no credential.
