# Phase 2 -- security lane

Range `2300f0e..dab8dcc` on `claude/serene-franklin-jv4pu7`. Read-only review: sources, `golang.org/x/crypto` v0.59.0 and `github.com/pkg/sftp` v1.13.12 in the module cache, `go list`, `govulncheck`. No test, gate or build was run.

## Verdict: BLOCK

2 blocking, 7 advisory. Both blockers are small and local.

## Blocking

**B1. A target that stops answering holds the gateway past every deadline.** Four waits have neither a timer nor the context:

- `internal/sshclient/command.go:128` `client.NewSession()` and `:142` `sess.Start(command)`. `dial` clears the connection deadline (`client.go:225`) and `Start` does not use `ctx` after `dial` returns. x/crypto blocks on `<-ch.msg` with no timeout (`ssh/mux.go:380` for the channel open, `ssh/channel.go:694` for the exec reply).
- `command.go:271` `sendSignal`, called from `stop` (`:210`, `:218`) and `Signal` (`:290`): a want-reply request, same `channel.go:694` wait. In `stop` the `KillGrace` timer starts only after the TERM reply arrives, so a missing reply means no KILL, no close, and `Wait` never returns.

A hostile or compromised target that completes authentication and then goes quiet pins the goroutines, the socket and the caller forever. An honest target that loses power mid-command does the same until the kernel gives up retransmitting (about 15 minutes). This breaks the section 6 guarantee "a command stops consuming gateway resources when it is killed, times out or the gateway shuts down". No test covers it: `sshtest` has no switch for a server that ignores a request.

Fix: in `Start`, keep `context.AfterFunc(ctx, client.Close)` armed until the exec reply and map the failure with `ctxErr`. In `sendSignal`, arm `time.AfterFunc(d, func() { cmd.closeConn(true) })` (`KillGrace` for TERM, `KillWait` for KILL and for the public `Signal`) so the pending request ends with EOF and the status says `MayStillRun`. Add `sshtest` switches (ignore signal, ignore exec, ignore channel open) and a test for each.

**B2. `Error()` can carry the target's host name and port (D8, section 6).** `internal/hostkey/hostkey.go:158`, `:161` and `:173` put `host:port` into `Error.Msg`; `internal/sshclient/client.go:130` appends that `Msg` to the public message. The file is re-read at every connect, so the path is live at run time (a line removed or an `@revoked` line added after start). `contract_test.go` ("known_hosts pinning", the revoked half) already produces such an error and asserts only the code; `client_test.go` "a known_hosts file without the host" checks only for `/`. The type's own doc (`errors.go:54`) says the message never names the host and may reach a tool result.

Fix: in `pin`, use fixed public text ("the pinned known_hosts file has no usable key for this server") and leave the `hostkey` error as the cause (it already is). Assert in both tests that `Error()` contains neither `srv.Host` nor the port. Same edit: `client.go:147` appends the `keyfile` text, which carries the gateway's uid and the key file's mode; keep it in the cause.

## Advisory

**A1. Nothing in this package stops `Upload` at `look`.** `sftp.go:142` passes `AccessFull` to `dial`, but `IdentityFor` ignores the level and `srv.Access` is never read: the suite uploads to servers with `Access: look`. Only the policy engine (`WritesFiles`) stands in the way, and the doc comment ("it needs AccessFull from the server's identity seam") reads as if a guard existed. Fix: in `dial`, refuse when `!tenant.Permits(srv.Access, level)` (the one comparison function), and build the upload tests with a `full` server.

**A2. A host key of another type is reported as `ssh_unreachable`.** `HostKeyAlgorithms` is the pinned types, so a server (or impostor) that offers only another type fails negotiation before the callback runs and `handshakeErr` falls to the default. It fails closed, but spec 5.5 says a changed key is `host_key_mismatch` and audited. Fix: `errors.As(err, *ssh.AlgorithmNegotiationError)` with `What == "host key"` -> `CodeHostKeyMismatch`; add a test.

**A3. `ssh_auth_failed` is decided by text an unverified peer controls.** `client.go:242` matches a substring of the whole handshake error; a peer that sends a disconnect message with that text during key exchange, before any host key check, is reported as "the server refused the identity's key". Fix: set a flag when `pinned.Check` returns nil and classify auth failure only when it is set.

**A4. `ExitStatus.Signal` is server text, unbounded and unchecked** (`command.go:332`; up to one SSH packet, any bytes). `Facts` are cleaned, this is not. Fix: accept `[A-Z0-9+-]{1,16}`, otherwise a fixed "UNKNOWN".

**A5. A `Command` is not bound to its caller.** `Wait`, `Signal` and `Close` take no tenant. The Phase 3 job store must key handles by tenant; either say so in the doc or make `Signal` take the `TenantID` and compare with `cmd.tenant`.

**A6. `known_hosts` is read twice.** `hostkey.go:141` reads it once (regular file, 1 MiB, `O_NONBLOCK`) and scans for `@cert-authority`; `hostkey.go:150` reopens the path through `knownhosts.New`, with none of those bounds, and x/crypto's `checkAddr` treats a CA line's key as a host key. Only someone who can write the operator's file can use the gap, so low; parsing the bytes already read would close it.

**A7. SFTP edges.** `Download` opens before it checks the type (`sftp.go:98`), so a FIFO blocks until the context ends and a device node is opened; an `Lstat` first avoids both. A remote symlink is followed (accepted in the 5.3 callout). When the context ends mid-upload the hidden temp file stays (`sftp.go:159` cannot run on a closed connection). `size` (`sftp.go:110`) can be negative from a lying server. SFTP calls are bounded only by `ctx`, so a caller without a deadline waits forever: document it or apply a default.

## Verified, no finding

- **Host key trust:** the only route to `ssh.NewClientConn` is `dial` (`client.go:215`); `pin` returns a non-empty set or an error, there is no `ssh.Dial`, no permissive callback, no first-use path. Certificates are refused in `Check` at connect as well as at load; `Algorithms` never lists a certificate or SHA-1 RSA form; several pinned keys and `@revoked` behave as documented. x/crypto runs the version exchange and key exchange (callback at `ssh/handshake.go:844`) before `clientAuthenticate`, so no user name or auth request precedes verification.
- **Private key:** read through `keyfile.Read` per connect, raw bytes cleared, one `PublicKeys` method, no password or keyboard-interactive, no agent; no path or key bytes in any message.
- **Hostile server bounds:** banner 255 bytes x 1024 lines, packets 256 KiB, channel window enforced (2 MiB), probe output 16 KiB, SFTP reply copied into a caller-sized buffer, server-opened channels and global requests rejected by `ssh.NewClient`. Connect is bounded by the deadline plus `AfterFunc`.
- **Caller:** signal names checked against the fixed set before marshalling; one exec string; `os/exec` appears only under `internal/testkit`; `ProbeCommand` is a constant; temp name is 64 random bits, created with `O_EXCL`.
- **Tenancy:** `checkServer` runs first in every exported `Client` method.
- **Test kit:** `TestTestkitOnlyFromTests` plus `go list -deps` show no product import. Both servers bind `127.0.0.1`, accept one generated key for one user, and clean up. `realsshd` has password and keyboard-interactive off and a `restrict` key. The CI diff adds one env var: no permission, secret or action.
- **Supply chain:** build graph gains `github.com/pkg/sftp` (with its internal packages) and `github.com/kr/fs`; the other new `go.sum` lines are test-only requirements outside the graph. `govulncheck ./...`: no vulnerabilities found.
- **Hygiene:** the diff, the implementer report and the seven commit messages hold only loopback, `192.0.2.1`, `example.net` names and generated keys.
