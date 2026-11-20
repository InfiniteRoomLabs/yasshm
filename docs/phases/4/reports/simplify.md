# Phase 4 (Authenticator) -- simplification lane

Range `64101fd..58ce7c3`. Read-only: nothing was run. The production code is already tight (one reason type, one `reject` constructor, one status switch, one body switch, one token-builder base), so the proposals are small: about 30 lines recommended, about 30 more optional. None changes an accepted token, a reason, a status, a body, an audit event, the refetch timing or an exported signature of `internal/auth`.

## Proposals

1. **APPLY-RECOMMENDED -- decode straight into `identity`, delete `claims`** (`internal/auth/auth.go:224-229,273-280`, `identity.go:16-23`). Before: `var id claims` ... `resolve(identity{Email: id.Email, CommonName: id.CommonName, Subject: std.Subject}, a.tenants)`. After: tag `identity`'s fields `json:"email"`, `json:"common_name"`, `json:"sub"`; `var id identity`; `resolve(id, a.tenants)`; `checkClaims` takes `*identity`. Preserving: `jwt.Claims.Subject` is a plain `string` tagged `sub` with no custom decoder (go-jose v4.1.9 `jwt/claims.go:35`) and both structs are decoded by `encoding/json` from the same verified payload, so `id.Subject == std.Subject` always, and a non-string `sub` already fails on `std` first. Saves ~7 lines. Risk: low.

2. **APPLY-RECOMMENDED -- drop `asError`** (`auth_test.go:254-269`); `reasonOf` has already asserted the concrete type. After: `var ae *Error; if !errors.As(err, &ae) || ae.who != c.who { ... }`, as `identity_test.go:47` does. Saves ~5 lines. Risk: low.

3. **APPLY-RECOMMENDED -- `maps.Clone`** for two hand-written map copies: `accesstest/token.go:210-216` (`Claims`) and `keyset_test.go:396-400` (`with`). Neither source map is nil. Saves ~7 lines. Risk: low.

4. **APPLY-RECOMMENDED -- remove the one-element loop** at `leak_test.go:79-83`: `for _, k := range []*accesstest.Key{{ID: "key-0", ...}}` becomes `k := accesstest.RSAKey(0)`; the ID is never read. Saves 2 lines and a nesting level. Risk: low.

5. **APPLY-RECOMMENDED -- `randomKid`** (`keyset_test.go:22-29`) becomes `rand.Text()` (`crypto/rand`, Go 1.24 and later) at its call sites; the kid only has to be random and unknown. Saves ~8 lines and the `encoding/hex` import. Risk: low.

6. **OPTIONAL -- `keySet.minRefetch` is a seam nothing sets** (`keyset.go:69,87,146`); tests move the injected clock instead. Use `MinRefetchInterval` at line 146 and drop the field. `fetchTimeout` stays (tests set it). Saves 1 line. Risk: low.

7. **OPTIONAL -- `Issuer.WaitFetches` duplicates `leakcheck.Poll`** (`accesstest.go:388-398`; callers `keyset_test.go:129-133,301-305`). `TestStartClose` already writes `leakcheck.Poll(5*time.Second, func() bool { return f.is.Fetches() >= 3 })`. The same form in the two callers removes two timeout contexts and the method: ~15 lines. Keep the method if Phase 5 or 6 wants it. Risk: low.

8. **OPTIONAL -- a table for the first three `TestRefetchLimit` subtests** (`keyset_test.go:56-75`): rows `{name, advance, wantFetches}` = `{0, 0}`, `{59s, 0}`, `{1s, 1}`, same subtest names. Saves ~10 lines. Risk: low.

9. **OPTIONAL -- `ErrKeysUnavailable`** (`errors.go:106-107`) is read by nothing, tests included (`ErrUnmapped` is). It is exported, so the lead decides; keep it if a Phase 5 handler will branch on it.

10. **DO-NOT-APPLY -- `jwt.ParseSigned` plus `tok.Claims(key, &std, &id)`** in place of `ParseSignedCompact`, `Verify` and two `Unmarshal` calls. `JSONWebToken.Claims` returns the signature error and the decode error through one untyped return (go-jose v4.1.9 `jwt/jwt.go:43-56`), so `bad_signature` and `malformed_token` could no longer be told apart.

11. **DO-NOT-APPLY -- `type Reason = audit.RejectReason`.** It would remove the sixteen mirrored values (~45 lines), but it changes exported signatures and puts HTTP statuses on an `audit` type.

12. **DO-NOT-APPLY -- replace `fetchCall`** with `sync.OnceValues` or `singleflight`. Each waiter selects on its own request context beside the shared `done` channel (`keyset.go:151-154,181-186`); a `OnceValues` waiter cannot give up, and `x/sync` is a dependency (D14).

## Left alone, and why

- `checkClaims`' `errors.Is` switch: a table is the same length and hides the order.
- `run`'s `NewTimer` and `Stop`, and `stop` beside `ctx`: the `internal/jobs` convention; a derived context saves two lines of lifecycle code.
- `keySet.closed` beside `ks.ctx`: the flag is what orders `wg.Add` before `wg.Wait` under the mutex.
- `c.Jar = nil`, the `origin == nil` guard, `len(jws.Signatures) != 1`, `found.ID == ""`, `asFailure`'s fallback: unreachable today, each a one-line guard on a security path.
- `Reason.Status` and `respond`: each mapping is written once; `TestReasonStatus` repeating it is the oracle.
- `accesstest`: `User` and `Service` already share `base`; `FaultSlow` and `Hold` differ (only one can be released).
- `syncBuffer` and `clock` in `helpers_test.go`: sharing them needs a new testkit package.
- `tokenClasses`: long, but one table driving three tests.
- Comments, docs, `model.c4`, `audit`, `deprule`: the package doc and the per-reason comments carry the contract; nothing worth cutting.
