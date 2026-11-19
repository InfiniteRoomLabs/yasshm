# Phase 2 (SSH layer) -- implementer report

Phase base: `2300f0e` (`docs(phase-2): start ssh layer`). Head at the time of this report: `3f2d8fb` (this report's commit comes after it). Branch: `claude/serene-franklin-jv4pu7`, pushed after every green commit.

## Verdict

Every deliverable of the GOAL Phase 2 contract is built. The gate is green on the clean committed tree (coverage 94.9 percent), CI is green on the head, and the CI job log shows all 25 real-`sshd` contract scenarios and the realsshd login test passing against OpenSSH 10.2p1. F10 is CONFIRMED by observation. The real-`sshd` half took 2 CI round trips to go green (the first proved the bare loop, the second ran the full suite and passed); 2 more pushes after that confirmed added scenarios and the docs.

## Packages and exported API

`internal/hostkey` (new leaf, imports nothing from the module; shared by `config` and `sshclient`):
- `ParseKey(line) (ssh.PublicKey, error)`, `Fixed(line) (*Pinned, error)`, `KnownHosts(file, host, port) (*Pinned, error)`
- `(*Pinned) Keys`, `Algorithms`, `Callback`, `Check`, `String`
- `Error{Msg}` (config's messages, byte for byte), `MismatchError{Fingerprint, Certificate}`, `MaxKnownHostsBytes`

`internal/sshclient` (imports `tenant`, `keyfile`, `hostkey` from the module; in the `deprule` table):
- `Options{ConnectTimeout, KillGrace, KillWait}`, `OptionsFromLimits(tenant.Limits)`, `New(Options) *Client`, `(*Client) Options()`
- `(*Client) Start(ctx, tid, srv, command, stdout, stderr) (*Command, error)`; `(*Command) Wait(ctx) (ExitStatus, error)`, `Signal(name) error`, `Close() error`, `Done()`, `Tenant()`, `Server()`
- `(*Client) Exec(ctx, tid, srv, command, stdout, stderr) (ExitStatus, error)`
- `(*Client) Download(ctx, tid, srv, path, offset, max) (data, size, eof, error)`
- `(*Client) Upload(ctx, tid, srv, path, data, mode, overwrite) error`
- `(*Client) Probe(ctx, tid, srv) (Facts, error)`, `ProbeCommand` (constant), `Facts{Kernel, Arch, OS, Uptime, User}`
- `ExitStatus{Outcome, Code, Signal, Stopped, TermSent, KillSent, MayStillRun}`, `Outcome` (`exited`, `signaled`, `no_status`), `StopReason` (`none`, `deadline`, `cancel`)
- Errors: `Code` (an `error`: `CodeUnreachable`, `CodeAuthFailed`, `CodeHostKeyMismatch`, `CodeTimeout`, `CodeInternal`), `*Error` with `Code()`, `Server()`, `Fingerprint()`, `Detail()` (operator logs only), `Is`, `Unwrap`; `CodeOf(err)`; sentinels `ErrWrongTenant`, `ErrSignalRefused`, `ErrUnknownSignal`, `ErrNotRunning`, `ErrNotRegular`, `ErrInvalidArgument`; `ValidSignal(name)`; `DefaultConnectTimeout`, `DefaultKillGrace`, `DefaultKillWait`

`internal/testkit/sshtest`: `Start(t, Options) *Server`; `Options{User, WrongHostKey, RefuseKey, DropMidStream, RefuseSignal, SlowBanner, NoExitStatus}`; `Server{Addr, Host, Port, HostKey, OfferedKey, ClientKeyFile, ClientKey, User, Root}` with `Requests`, `Conns`, `Accepted`, `Pids`, `Running`, `Close`.

`internal/testkit/realsshd`: `Path`, `Available`, `Required`, `Present(t)`, `Start(t) *Server`; `Server{Addr, Host, Port, HostKey, ClientKeyFile, User, Banner, Dir}` with `Log`, `Signer`, `AuthorizedKey`, `HostKeyLine`; `RequireEnv = "YASSHM_REQUIRE_SSHD"`.

Gate: new step `sshd` between `test` and `cover` in `scripts/check.sh` (`usage lint` clean); the old trailing notice is removed (the step prints it). `.github/workflows/ci.yml` sets `YASSHM_REQUIRE_SSHD: "1"` on the gate step.

## Tests

- `internal/sshclient`: 10 test functions, 80 passing subtests locally (the contract suite of 25 scenarios against `sshtest`, plus tenant checks, connect rules, pinning and key cases, command faults, probe parsing, SFTP faults, errors). `TestRealSSHDContract` runs the same 25 scenarios against real `sshd` (CI).
- `internal/hostkey`: 4 test functions, 32 subtests. `internal/testkit/sshtest`: 3 subtests (Close kills every child, no server goroutine survives, refused requests recorded). `internal/testkit/realsshd`: `TestRealSSHDLogin` (CI) and one env test.
- Every `sshclient` test registers `checkLeaks` (goroutine stacks of this module, x/crypto/ssh and pkg/sftp, compared before and after with a 5 s poll, run after the servers' Close) and ends with `waitNoConns` on the in-process server. Process-group cases kill any survivor by pid in a cleanup.
- Stress: `-race -shuffle=on -count=10` of the SSH packages and `-count=100` of `sshtest` are green.

Coverage: total 94.9 percent (floor 85, `internal/testkit` excluded). `internal/sshclient` alone is 92.1 percent from the in-process half (the local gate has no `sshd`).

## The tail of `mise run check` (clean tree, head `3f2d8fb`)

```
== fmt ==
== lint ==
== skipcheck ==
== test ==
== sshd ==
check.sh: sshd is not installed here; the real-sshd lane runs in CI only (spec section 7)
== cover ==
cover: total 94.9% (floor 85%, internal/testkit excluded)
== vuln ==
No vulnerabilities found.
== build ==
== arch ==
check.sh: all OK
```

## CI evidence

Head `3f2d8fb`: run `44629872928`, job `135933341973`, check `check` completed `success`. The runner's `sshd` is `/usr/sbin/sshd`, version `SSH-2.0-OpenSSH_10.2p1 Ubuntu-2ubuntu3.8`, run as user `runner`. From the `== sshd ==` step of that job log:

```
realsshd: /usr/sbin/sshd listening on 127.0.0.1:<port> as user runner (version: SSH-2.0-OpenSSH_10.2p1 Ubuntu-2ubuntu3.8)
F10 observed (SSH-2.0-OpenSSH_10.2p1 Ubuntu-2ubuntu3.8): a child that called setsid survived the TERM sent to the session's process group: true
F10 observed (SSH-2.0-OpenSSH_10.2p1 Ubuntu-2ubuntu3.8): signal request TERM with want-reply was ACCEPTED
F10 observed (SSH-2.0-OpenSSH_10.2p1 Ubuntu-2ubuntu3.8): INT HUP QUIT USR1 USR2 KILL accepted and delivered
F10 observed (SSH-2.0-OpenSSH_10.2p1 Ubuntu-2ubuntu3.8): after the client closed the connection the command was still running: true
--- PASS: TestRealSSHDContract
    --- PASS: TestRealSSHDContract/happy/echo
    --- PASS: TestRealSSHDContract/happy/a_deadline_sends_TERM_and_the_error_is_timeout
    --- PASS: TestRealSSHDContract/raging/a_command_that_traps_TERM_is_ended_by_KILL_after_the_grace
    --- PASS: TestRealSSHDContract/happy/TERM_reaches_a_forked_child_in_the_process_group
    --- PASS: TestRealSSHDContract/corner/a_child_that_left_the_process_group_survives_TERM_and_KILL
    --- PASS: TestRealSSHDContract/happy/the_signal_request_is_accepted_and_delivered
    --- PASS: TestRealSSHDContract/corner/closing_the_session_does_not_stop_the_command
    --- PASS: TestRealSSHDContract/raging/several_megabytes_on_both_streams
    --- PASS: TestRealSSHDContract/edge/a_slow_writer_applies_back-pressure
    --- PASS: TestRealSSHDContract/happy/download_with_offset_and_limit
    --- PASS: TestRealSSHDContract/happy/upload_creates_with_the_mode_and_refuses_to_overwrite
    --- PASS: TestRealSSHDContract/sad/host_key_mismatch
    --- PASS: TestRealSSHDContract/sad/refused_key
    --- PASS: TestRealSSHDContract/sad/unreachable_port
    (and the remaining scenarios, 25 in all)
--- PASS: TestRealSSHDLogin
sshd: 26 real-sshd scenarios passed
```

Earlier runs: `44625699491` (first push `90a24e1`: the bare loop, `TestRealSSHDLogin` passed), `44628614466` (`92aae78`: full suite, 24 scenarios passed), `44629427495` (`336ea41`: 25 scenarios with the closed-session case).

## F10

CONFIRMED by observation (spec section 3 row updated). On OpenSSH 10.2p1 run as a non-root user with a `restrict` key: the `signal` request with want-reply is answered with success for `TERM`, `INT`, `HUP`, `QUIT`, `USR1`, `USR2`, `KILL`, and each ends the command by that signal; `TERM` ends the command and a forked child in its group; a child that called `setsid` survives (cleaned up by pid); a command that ignores `TERM` is ended by `KILL` after the grace; a command is still running a second after the client closed the connection. Not observed: a root login. From memory of `session.c` (INFERRED, marked so in the row), a root session runs in the monitor process, which refuses signalling. The gateway's `look` account is never root.

## `git log --oneline 2300f0e..HEAD` (before this report's commit)

```
3f2d8fb docs: record F10 as observed and document the SSH layer and the sshd step
336ea41 fix(ssh): classify a passed caller deadline as timeout during connect
71d29e3 test(ssh): observe that a closed session leaves the command running
92aae78 feat(ssh): add the SSH client, the in-process test server and the contract suite
99687d3 refactor(config): move the host key pinning rules into internal/hostkey
90a24e1 test(ssh): add realsshd and prove the CI-only real-sshd loop
```

`git status --porcelain`: empty.

## Dependency (D14)

`github.com/pkg/sftp` v1.13.12, released 2026-10-23 and actively maintained. It is the de facto Go SFTP library, built on `golang.org/x/crypto/ssh`, with a client (`OpenFile` with `O_EXCL`, `Chmod` on a handle, `HasExtension`, `PosixRename`) and a server side (`sftp.NewServer` with `WithServerWorkingDirectory`) that `sshtest` uses. Its only new runtime requirement is `github.com/kr/fs` v0.1.0. Its go.mod also lists `stretchr/testify` v1.11.1, `davecgh/go-spew`, `pmezard/go-difflib` and `gopkg.in/yaml.v3` v3.0.1, but those serve its own tests and are not in this module's build. `golang.org/x/crypto` and `golang.org/x/sys` stay at this module's versions (v0.59.0, v0.50.0). `mise run check -- vuln` reports no vulnerabilities.

## Spec discrepancies and deliberate choices

These are recorded in the spec: the 5.5 callout, the section 7 callout extension, the F10 row and the section 4 dependency rule.

1. `internal/hostkey` is a new leaf package, and `config` keeps its messages byte for byte (its tests are unchanged apart from one constant's new home). Section 4 (table row and dependency rule) now names it. `config` now opens `known_hosts` with `O_NONBLOCK` (a FIFO cannot block the loader).
2. The API is methods on a `Client` built from `Options`, not package functions: the kill grace, kill wait and connect timeout need a home, and tests set them in fractions of a second. `Signal` is a method of the `Command` handle. Section 5.5 wrote it as `Signal(session, sig)`. A `Command`'s methods take no tenant because the handle is bound to the tenant that started it. Every `Client` method takes the `TenantID` and the `*tenant.Server`.
3. `KillWait` (default 2 s) is the wait after `KILL` before closing. The spec says "wait briefly" and the config has no key for it.
4. A cancelled context (not a deadline) returns code `internal`, matching `context.Canceled`. The five codes have no "cancelled"; Phase 3 should test `errors.Is(err, context.Canceled)` first.
5. `HostKeyAlgorithms` is always the set of pinned plain key types, not only when a single key is pinned, so a certificate algorithm is never offered. The callback also refuses a certificate.
6. At connect: a pin that cannot accept any key is `host_key_mismatch`. A `known_hosts` file that cannot be read, a bad private key and a wrong tenant are `internal`. Running out of the connect timeout is `ssh_unreachable` (5.5: "dial or timeout"); the caller's deadline is `timeout`.
7. `ssh_auth_failed` is recognised by x/crypto's text `ssh: unable to authenticate`, because there is no typed client-side error. Tests against both servers pin that text.
8. `Upload`: without `overwrite` it uses `O_CREAT|O_EXCL` on the final path, which is not atomic for readers. A temp file plus no-clobber rename is not portable: `pkg/sftp`'s server renames over the target, while OpenSSH's plain rename refuses. SFTP v3 has no "exists" status, so a failed exclusive create is checked with `lstat` before it is reported as `fs.ErrExist`. With `overwrite` it uses a temp file plus `posix-rename` when the server offers it, and otherwise writes in place. The mode is set before the content. It uses `IdentityFor(full)`.
9. `MayStillRun` is true for every `no_status` outcome, not only after `KILL`: the client cannot tell that the command stopped.
10. A lost connection and "the server closed the channel without a status" look the same in x/crypto (`ExitMissingError`). The client tells them apart with a `keepalive@openssh.com` global request, bounded by `KillWait`.

## Process notes

- In commit `71d29e3` I ran `git add` and `git commit` in one shell call, against the rule that they are separate calls. The commit's content is as intended, and it was not amended. Every other commit used separate calls.
- I did not edit `GOAL.md` or the ledger in `docs/progress.md`. Recording F10 in `docs/progress.md` (GOAL stage 1) is left to the lead.

## Open questions for the lead

- Should a cancelled context have its own code? Phase 3 maps shutdown to `killed` either way.
- Should `KillWait` be a config limit? It is fixed at 2 s now.
