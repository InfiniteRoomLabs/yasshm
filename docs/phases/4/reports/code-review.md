# Phase 4 (Authenticator) -- code review

Reviewer: fable. Range `64101fd..58ce7c3`. Read-only: nothing was run. Ground truth read: `go-jose/v4` v4.1.9 in the module cache (`jws.go`, `signing.go`, `asymmetric.go`, `jwt/validation.go`, `jwt/claims.go`, `jwt/jwt.go`, `json/`).

## Verdict: APPROVE

0 BLOCKING, 6 ADVISORY. Finding 1 belongs in the fix commit.

## Checked and holds

- **Order:** header count and size (`auth.go:234-242`) -> compact parse with `[RS256]` (any other `alg` refused at parse with a typed error, before a key or a fetch) -> `kid` present -> key lookup -> `Verify` -> decode -> required claims -> the library's `iss`, `aud`, `nbf`, `exp`, `iat` -> `resolve`. Only `alg` and `kid` are read from the unverified token; every claim comes from the bytes `Verify` returned. An embedded `jwk` header is never the key.
- **Leeway** (`jwt/validation.go:112-124`): accepted iff `nbf <= now+30`, `exp >= now-30`, `iat <= now+30`. Each passes at 30 seconds and fails at 31; both sides are tested. `exp == now-30` passes (a strict reading of RFC 7519 4.1.4 refuses at equality); that is the library's reading and matches the `ReasonExpired` doc. The clock is injected wherever times are compared; the only real-time use is the refresh timer.
- **Key set:** the map is swapped whole under `ks.mu` and never mutated after. Single flight is real: `inflight` is read and set under one lock, `wg.Add` runs under the lock `close` takes before `Wait`, `call.err` is written before `close(done)`. Double `Close`, `Start` after `Close`, `Authenticate` after `Close` and a fetch in flight at `Close` are all correct. No library or network error text survives `reject` or `fetchFailure`.
- **Model and docs:** `#planned` is gone; `auth -> audit` is in the model, the spec and `deprule`; the changelog matches the code. The spec callouts are fair readings, not redesigns.

## The implementer's choices

- **(a) go-jose directly (D14): sound.** Parsing, the algorithm allowlist, RSA verification, JWK validation and the `iss`/`aud`/time checks are the library's. Hand-written: presence of `exp`/`iat`/`nbf` (the library has no "required" switch; done on verified claims), the `kid` map and the JWK filter -- each stricter than the library's own path. One step is laxer: finding 1.
- **(b) `nbf`: the right reading, and it weakens nothing.** RFC 7519 4.1.5 makes `nbf` optional; F7's rule is met whenever the claim is present. The discriminator (`email != ""`) is a signed claim, so `nbf` cannot be stripped from a user token, and the required `iat` bounds a service token the same way. D4 names signature, issuer, audience and expiry only.
- **(c) the limiter counts every fetch: accept.** Stricter than 5.7. Cost: a rotation landing within a minute after a periodic refresh gives up to 60 seconds of `unknown_key` for new-key tokens (roughly 1 rotation in 60 with an hourly refresh).
- **(d) one-minute retry: accept**, but the loop's use of it is untested (finding 2).

## Findings

1. **ADVISORY (D14) -- `internal/auth/auth.go:274`.** The verified payload is decoded with `encoding/json`. The library's own path (`jwt.JSONWebToken.Claims`, `jwt/jwt.go:43`) uses its fork `go-jose/v4/json`, which matches member names case-sensitively and rejects duplicate keys. The standard decoder folds case (`"EMAIL"`, `"Iss"` bind) and lets the last duplicate win, and `jwt.Claims` has no `UnmarshalJSON`, so this covers `iss`, `aud` and `exp` as well as `email` and `common_name`. Not exploitable unless the vendor signs such a payload, but it is the one place where claim interpretation is ours and not the library's. Fix: import `github.com/go-jose/go-jose/v4/json` in `auth.go` in place of `encoding/json` (keep `jws.Verify`), and add two classes with `signedPayload`: a duplicate `email` -> `malformed_token`, and `"Email"` alone -> `missing_claim`.
2. **ADVISORY -- `auth.go:174`, `keyset_test.go:371-386`.** "An issuer down at start recovers without traffic" is asserted only on the pure `retryWait`; replacing its call in `run` with `a.interval` passes every test, and `TestIssuerDownAtStart` never calls `Start`. Fix: let `retryWait` take `ks.minRefetch` (the field exists), then test `Start` against a stopped issuer with a one-hour interval and a 20 ms `minRefetch`: `Up()`, poll `Ready()`.
3. **ADVISORY -- `middleware.go:84-87`, `:100-102`.** The audit-failure 503 carries `Retry-After: 60`, but `audit.Logger` refuses every write after one failure until a restart (spec 5.4), so a retry never helps. Send `Retry-After` only for `ReasonKeysUnavailable`. Also log `werr` at `:79` and `:85`: it is the audit package's own error, never token-derived, and without it the operator's log line has no cause.
4. **ADVISORY -- `middleware.go:84-91`.** On a nil error the middleware trusts any principal: an `Authenticator` returning `(Principal{}, nil)` gets `auth.accepted` written and `next` called, and only `Require` stops it. Apply the completeness test `PrincipalFrom` uses before the audit write; on failure write `auth.rejected` (`invalid_token`) and answer 500.
5. **ADVISORY -- `leak_test.go:89-93`.** The scan covers the token, its segments, key fragments and the certs URL, but not `accesstest.TeamDomain` or `accesstest.Audience`. The D8 property (no log line, error or body carries the team domain or the audience) is true by reading and unasserted. Add both strings to the scan.
6. **ADVISORY -- `auth_test.go:381-391`.** `keys_unavailable` through the middleware asserts status, body and `Retry-After` but discards the audit path, so it is the one class without the reason + status + event triple. Read the event and assert its `reason`.

## Considered, not findings

- `Ready()` stays true while refreshes keep failing: last good keys, as 5.7 says; the only signal is a warning per minute.
- The 401 has no `WWW-Authenticate`: no scheme applies at the origin, and the test pins its absence.
- The `type` claim (`app` or `org`) is not checked. INFERRED, not re-read: `aud` already separates an application token from an `org` one. Worth one line in manual test MT-001.
