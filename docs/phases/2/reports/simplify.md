# Phase 2 (SSH layer) -- simplification lane

Range `2300f0e..dab8dcc`. Read-only; nothing was run. The production code is already tight: one `dial`, one `openSFTP`, one `fileErr`/`ctxErr`, `Exec` is `Start` + `Wait`, `Probe` is `Exec`, and one scenario table with one runner serves both servers. Most of what can go is test scaffolding. 3 APPLY-RECOMMENDED, 5 OPTIONAL, 3 DO-NOT-APPLY.

## APPLY-RECOMMENDED

1. **Dead test scaffolding in `internal/sshclient`** (~18 lines; risk: none, the compiler checks it).
   - `harness_test.go:31-32,38`: `target.name`, `.real` and `.user` are set (61, 73, 87-88, 100) and never read. Delete them.
   - `contract_test.go:84`: `startWithPid`'s third result is `_` at all five call sites (212, 231, 256, 269, 281), and its comment (82-83) calls it "a cancel for the Wait context". Return `(*Command, int)`.
   - `harness_test.go:299`: `otherHostKey`'s second result is never used. Return `string` and delete the wrapper `mustKeyLine` (`client_test.go:228-232`).
   - `contract_test.go:509`: `var _ io.Writer = (*countWriter)(nil)` is the file's only use of `io`. Delete both.
2. **`sftp.go:151-154,207-213`: `tempName` cannot fail** (~6 lines; risk: low). `crypto/rand.Read` never returns an error since Go 1.24 (it aborts the process), and `go.mod` says 1.27. Before: `tmp, terr := tempName(p); if terr != nil { return newErr(..., "no random name", ...) }`. After: `tmp := tempName(p)`, with `_, _ = rand.Read(b[:])` inside. The removed branch is unreachable and untestable; the name format does not change.
3. **Testkit leftovers** (~16 lines; risk: none). `realsshd.go:141-146`: `AuthorizedKey` has no caller. `Server.Dir` (97-98, 183) is never read. `HostKeyLine` (148-151) has one caller, `harness_test.go:96`, which can use `keyLine(s.HostKey)` as `newSSHTest` does. `sshtest.go:436,452`: `reportExit` takes `err` only to run `_ = err`; drop the parameter (`_ = cmd.Wait()` at 424).

## OPTIONAL

4. **`scripts/check.sh:81-98`: a variable instead of a temp file and a `RETURN` trap** (~4 lines; risk: low to medium, because this path runs in CI only). Before: `log=$(mktemp)`, `trap "rm -f '$log'" RETURN` under an SC2064 waiver, `cat "$log"`, `grep ... "$log"`. After: `if ! out=$(cd ... 2>&1); then printf '%s\n' "$out"; ...`, then `grep ... <<<"$out"`. Same lines printed, same exit codes. The trap does not fire on the two `exit 1` paths, so a failure leaves the file today.
5. **One key writer, one `alive`** (~40 lines; risk: low). `sshtest.go:485-503` `writeClientKey` and `realsshd.go:331-349` `writeKey` are the same function, and `harness_test.go:281-296` `writeKeyFile` is a third copy. `alive` exists in `sshtest_test.go:51-61` and `harness_test.go:220-233`. Export `sshtest.WriteKey(t, path)` and `sshtest.Alive(pid)`. Cost: `realsshd` imports `sshtest`.
6. **Small cuts in `client.go` and `sftp.go`** (~18 lines; risk: low). `client.go:183-184` -> `context.WithTimeout(ctx, c.opts.ConnectTimeout)`. The pair "`ctx.Err()`, else `pastDeadline`" at 189-194 and 236-241 -> one `callerEnded(ctx) error` that replaces `pastDeadline` (keep the comment at 239-240). `trimSig` (265-266) has one caller; inline `strings.TrimPrefix`. `sftpSession` (`sftp.go:23-34`) -> `openSFTP` returns `(*sftp.Client, func(), error)`.
7. **`sshtest.go:317-340,364`: the session's `ended` flag and its mutex** (~10 lines; risk: medium, rerun the `-count=100` stress). `ended` is true exactly when `run`'s reaper has deleted `s.procs[pid]`. Test that under `s.mu` and drop the callback, the flag and the local mutex; only the loop goroutine touches `started` and `pid`.
8. **`hostkey.go:178-181,207-213`: `Pinned.Keys` and `Pinned.Callback`** (~12 lines; risk: none) have no caller outside `hostkey_test.go`; `dial` wraps `Check` itself. Remove them unless a later phase is meant to use them.

## DO-NOT-APPLY

9. Using `pinned.Callback()` in `dial` and recovering the mismatch with `errors.As` on the handshake error. It depends on how x/crypto wraps a callback error; a change there would turn `host_key_mismatch` into `ssh_unreachable`.
10. Merging the two `hasExited` checks (`command.go:193,207`). The outer one decides whether a stop cause is recorded, the inner one whether `TERM` goes on the wire.
11. Removing the unreachable "must hold exactly one key" branch (`hostkey.go:79-81`; a one-line input leaves no `rest`). It moved verbatim from `config` and is defence in depth in the pinning path.

## Left alone

- `Start`/`Wait`/`stop`/`finish`: one waiter goroutine and two `Once`s, with `context.AfterFunc` already in `dial` and `openSFTP`. Nothing simpler keeps the escalation timing.
- `limitedBuffer`'s mutex (`probe.go:44-63`): removable only by relying on `Session.Wait` waiting for the copy goroutines. Not worth six lines.
- The early `checkServer` in `Download` and `Upload`: needed before `srv.ID` is read for an argument error.
- By design: `Upload`, the tenant id, `IdentityFor`, `Done`/`Signal`/`Close`, `Command.Tenant`/`Server`, `Client.Options`, the fault switches.
- Comments carry evidence (F10, x/crypto quirks, SFTP v3); none is worth cutting.
