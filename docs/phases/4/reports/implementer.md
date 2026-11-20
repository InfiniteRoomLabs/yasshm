# Phase 4 (Authenticator) -- implementer report

Implementer: opus. Phase base `64101fd`. Code head before this report: `8477f45` (gate green on the clean tree, CI green on that SHA).

## Exported API (`internal/auth`)

- `type Authenticator interface { Authenticate(r *http.Request) (tenant.Principal, error) }`
- `type CloudflareAccess struct` -- `NewCloudflareAccess(cfg config.CloudflareAccess, tenants []tenant.Tenant, opts ...Option) (*CloudflareAccess, error)`; methods `Start(ctx)`, `Close()`, `Ready() bool`, `Status() string` ("key set loaded" / "key set not loaded"), `Authenticate(*http.Request)`, `String()`.
- Options: `WithClock(func() time.Time)`, `WithHTTPClient(*http.Client)` (its Transport is used; Jar, Timeout and CheckRedirect are replaced), `WithRefreshInterval(time.Duration)`, `WithLogger(*slog.Logger)`.
- Constants: `Header` (`Cf-Access-Jwt-Assertion`), `MaxTokenBytes` (16 KiB), `Leeway` (30 s), `FetchTimeout` (10 s), `MaxKeySetBytes` (1 MiB), `MinRefetchInterval` (1 min), `DefaultRefreshInterval` (1 h).
- Errors: `type Error` (`Error()` = `auth: <reason>`, `Reason()`, `Is`), `type Reason` (`String()`, `Status()`, `Audit() audit.RejectReason`), the `Reason*` values (below), sentinels `ErrUnmapped`, `ErrKeysUnavailable`.
- Middleware: `type AuditWriter interface { Write(audit.Event) error }` (`*audit.Logger` is one), `Middleware(a Authenticator, w AuditWriter, opts ...MiddlewareOption) func(http.Handler) http.Handler`, `WithMiddlewareLogger`, `PrincipalFrom(ctx)`, `RequestIDFrom(ctx)`, `MustPrincipal(ctx)` (panics), `Require(next)` (500 without a principal).
- `audit` gained `RejectMalformedToken`, `RejectOversizedToken`, `RejectBadAlgorithm`, `RejectUnknownKey`, `RejectBadSignature`, `RejectWrongIssuer`, `RejectWrongAudience`, `RejectExpired`, `RejectNotYetValid`, `RejectIssuedInFuture`, `RejectMissingClaim`, `RejectAmbiguousIdentity` (existing four kept).
- `internal/testkit/accesstest`: `New(t, now)`, `Issuer` (`CertsURL`, `Config`, `Fetches`, `WaitFetches`, `Rotate`, `DropOld`, `SigningKey`, `ServedKeys`, `Down`, `Up`, `Hold`, `SetFault`, `Close`, `User(email)`, `Service(cn)`), `Builder` (`Claim`, `Without`, `Times`, `Header`, `WithoutHeader`, `SignWith`, `Alg`, `HS256WithPublicKey`, `Claims`, `Sign`), `SignRaw`, `Segments`, `TamperPayload`, `TamperSignature`, `RSAKey(i)`, `ECKey()`, `RSAJWK`, `JWKS`, faults `FaultSlow`, `FaultGarbage`, `FaultHuge`, `FaultRedirectOtherHost`, `FaultRedirectNonLoopback`, `FaultRedirectSameHost`, `FaultWrongContentType`, `FaultEmptyKeySet`, `FaultNoUsableKey`, `FaultStatus500` (down = `Down`/`Up`, connection refused).

Nothing is wired into `cmd/yasshm` or `internal/cli`.

## Rule of 5.7 -> enforced by -> test

| Rule | Enforced by | Test |
|---|---|---|
| Only the header `Cf-Access-Jwt-Assertion`; cookie, query, `Authorization` ignored; two values refused | this package | `TestTokenClasses/sad/token only in the CF_Authorization cookie`, `.../in Authorization`, `.../in a query parameter`, `raging/two header values`, `raging/a valid header beside a bad one` |
| Token capped at 16 KiB before parsing | this package | `raging/oversized token`, `edge/token of exactly the size cap is parsed` |
| RS256 only; none/HS256/ES256/PS256/RS384/no alg refused before any key lookup or fetch | go-jose (`ParseSignedCompact` with `[RS256]`, typed `*ErrUnexpectedSignatureAlgorithm`) | `raging/alg none`, `raging/alg None in another case`, `raging/HS256 signed with the public key`, `raging/ES256`, `raging/PS256`, `raging/RS384`, `raging/no alg`, `corner/alg none with a valid RS256 signature appended`, `TestRefetchLimit/raging/refused tokens never cause a fetch` |
| `kid` present and in the key set | this package | `sad/no kid`, `sad/unknown kid` |
| Signature valid | go-jose (`JSONWebSignature.Verify` with the `*rsa.PublicKey`) | `raging/tampered payload`, `raging/tampered signature`, `raging/signed by a key the issuer never served`, `raging/empty signature` |
| Key set parsing (RSA >= 2048 bits, kid, alg RS256/absent, use sig/absent; private keys, other types, duplicates skipped) | go-jose `JSONWebKey` parse/validate + this package's filter | `TestParseKeySet` |
| `iss` == `https://` + team_domain exactly | go-jose (`Expected.Issuer`) | `sad/wrong issuer`, `sad/issuer with a trailing slash`, `sad/issuer over http`, `sad/issuer in another case`, `sad/missing iss` |
| `aud` (string or array) contains the audience | go-jose (`Expected.AnyAudience`, `Audience` unmarshal) | `happy/audience array with several entries`, `edge/audience as a single string`, `sad/wrong audience`, `sad/audience that differs in case`, `sad/empty audience array`, `sad/missing aud`, `raging/aud is a number` |
| `exp`/`nbf`/`iat` with exactly 30 s leeway | go-jose (`ValidateWithLeeway(.., 30s)`), clock injected | `edge/exp exactly 30 seconds ago`, `sad/expired` (31 s), `edge/nbf and iat exactly 30 seconds ahead`, `sad/not yet valid` (31 s), `sad/issued in the future` (31 s), `sad/service token with nbf in the future` |
| `exp`, `iat` required; `nbf` required on user tokens | this package, on the verified claims | `sad/missing exp`, `sad/missing iat`, `sad/missing nbf on a user token`, `sad/missing iat on a service token`, `edge/service token without nbf` |
| Identity mapping | this package (`resolve`, pure) | `TestResolve`, `FuzzResolve`, token classes below |
| Unknown-kid refetch <= 1/min, shared fetch | this package (`keySet`, injected clock) | `TestRefetchLimit` (1000 random kids, 59 s -> 0 fetches, 60 s -> 1, ten minutes -> 10 more), `TestSharedFetch` (50 concurrent -> 1 fetch) |
| Fetch: 10 s timeout, 1 MiB, JSON content type, 200, same-host redirects (https, or http to loopback IP), no cookies, `Close` cancels | this package | `TestIssuerFaults`, `TestFetchClient`, `TestFetchCancel` |
| Keys unavailable until first success; failed refresh keeps last good keys; rotation | this package | `TestIssuerDownAtStart`, `TestIssuerFaults` (recovery half), `TestRotation` |
| No goroutine outlives `Close` | this package | `TestStartClose`, `TestFetchCancel` (both with `leakcheck.Check`) |
| Nothing leaks | this package | `TestNothingLeaks` (mutation-checked: logging the header in the middleware makes it fail) |

## Token classes -> reason / status / event

Every class runs through `Authenticate` (`TestTokenClasses`) and through `Middleware` with a real `audit.Logger` (`TestTokenClassesThroughMiddleware`: status, fixed body, one event with a 32-hex `request_id`, reason, no tenant on rejections, principal only for unmapped).

| Class | Reason | Status | Event |
|---|---|---|---|
| valid user, valid service token, user of the second tenant, aud array with several entries, aud as a string, email in another ASCII case, exp exactly -30 s, nbf/iat exactly +30 s, service token without nbf | -- | 200 (next runs) | `auth.accepted` with tenant and principal |
| expired | `expired` | 401 | `auth.rejected` |
| not yet valid (user; service with future nbf) | `not_yet_valid` | 401 | `auth.rejected` |
| issued in the future | `issued_in_future` | 401 | `auth.rejected` |
| wrong audience, aud in another case, empty aud array, missing aud, corner expired+wrong aud | `wrong_audience` | 401 | `auth.rejected` |
| wrong issuer, trailing slash, http, other case, missing iss | `wrong_issuer` | 401 | `auth.rejected` |
| unknown kid, no kid | `unknown_key` | 401 | `auth.rejected` |
| tampered payload, tampered signature, unserved key, empty signature | `bad_signature` | 401 | `auth.rejected` |
| alg none, None, HS256 with the public key, ES256, PS256, RS384, no alg, none header + RS256 signature | `bad_algorithm` | 401 | `auth.rejected` |
| missing exp, missing iat (user and service), missing nbf on a user, no email and no common_name, empty email | `missing_claim` | 401 | `auth.rejected` |
| email and common_name, service token with a sub | `ambiguous_identity` | 401 | `auth.rejected` |
| unmapped user, unmapped service, email used as a client id | `unmapped_principal` | 403 | `auth.rejected` with kind:subject, no tenant |
| email with a Kelvin sign or a control character | `unmapped_principal` | 403 | `auth.rejected` with no principal |
| no header, empty header, token only in cookie / Authorization / query | `missing_token` | 401 | `auth.rejected` |
| two header values, valid beside bad, not a JWT, four segments, header not JSON, padded base64, claims an array or not JSON, aud a number, exp a string, email an object, a 16384-byte non-token | `malformed_token` | 401 | `auth.rejected` |
| oversized token | `oversized_token` | 401 | `auth.rejected` |
| any token while no key set is loaded | `keys_unavailable` | 503 + `Retry-After: 60` | `auth.rejected` |
| accepted token, audit write fails | -- | 503, next does not run | (write failed) |

## Tests

`internal/auth`: 22 top-level tests, 255 subtests, all passing; fuzz target `FuzzResolve` (11 seeds run in the gate). No `t.Parallel`, no sleeps for synchronisation (the clock is injected; the only real-time waits are deadline-bounded polls and the issuer's held responses), nothing beyond loopback (the non-loopback redirect is refused before a dial).

Coverage (gate `cover` step): **total 95.3%** (floor 85, `internal/testkit` excluded). `internal/auth` functions are 100% except `verify` 95.8%, `checkClaims` 90.9% (the library's default error branch), `refresh` 90.9%, `fetch` 92.3%, `asFailure` 75%.

Gate tail on `8477f45` (clean tree):

```
[...] likec4.c4:codegen done in 466ms
check.sh: all OK
```

`mise run check -- vuln`: `No vulnerabilities found.`

Repeat run: `go test -race -shuffle=on -count=30 ./internal/auth/` -> `ok ... 102.258s` (30 runs green).

Fuzz: `mise run fuzz` ran every target for 30 s, all PASS; `FuzzResolve` alone for 30 s: about 1.0 M execs, PASS, no crasher (nothing to commit as a seed).

CI on `8477f45`: `check completed success`.

## Commits

```
8477f45 docs: record the authenticator in the changelog, README, TESTING and gotchas
aa6e393 feat(auth): verify Cloudflare Access tokens and map them to a principal
148017b feat(auth): add the local Access issuer test kit and finer rejection reasons
```

(plus this report's commit). `git status --porcelain`: empty before this report.

## Dependency

`github.com/go-jose/go-jose/v4` v4.1.9 (released 2026-11-05, Apache-2.0, go 1.24). Its `go.mod` has no requirements, so it is the only module added to the build graph (`go mod graph`: `yasshm -> go-jose/v4@v4.1.9`, `go-jose/v4 -> go@1.24.0`). Why: one maintained library does JWS parsing with a caller-given algorithm list (refusing everything but RS256 at parse time, typed error), signature verification, JWK parsing/validation and claim validation with configurable leeway and typed per-check errors; it is what `coreos/go-oidc` itself verifies with. Rejected after reading the sources: `coreos/go-oidc/v3` v3.21.0 (fixed 5-minute nbf leeway, no exp leeway, no iat check, string errors, adds `x/oauth2`, `RemoteKeySet` refetches on every unknown kid with no limit, unbounded `io.ReadAll`, fetch goroutine detached from cancellation -- it would have needed go-jose plus our own key set anyway); `golang-jwt/jwt/v5` v5.3.1 (good options and typed errors, but no JWKS parsing -> a second library `MicahParks/keyfunc` + `jwkset` + `x/time`, or hand-decoded moduli). Full reasoning and the rule table are in the spec 5.7 callout.

## Spec discrepancies, ambiguities and deliberate choices

1. **Service tokens have no `nbf`** (vendor "Application token" page re-read 2026-11-20 with WebFetch: service-token payload is `type`, `aud`, `exp`, `iss`, `common_name`, `iat`, `sub`). The work order said nbf is required; that would refuse every real service token. Implemented: nbf required on user tokens, checked when present on service tokens; exp and iat required on both. Recorded in F7, the 5.7 callout and a CLAUDE.md gotcha. **The lead should confirm this choice.**
2. **Order of checks vs "until the first fetch every request is keys_unavailable":** missing, oversized and malformed tokens, a wrong alg and a missing kid are refused for that reason before the key lookup (so they never cause a fetch, also while unloaded); every token that reaches the lookup while unloaded is `keys_unavailable`. A lookup while unloaded may start a fetch within the same once-a-minute limit and waits for it.
3. **Extra reasons:** `oversized_token` (separate from `malformed_token`), and `invalid_token` kept for a non-`*Error` from a foreign `Authenticator`. Two header values -> `malformed_token`; no kid -> `unknown_key`.
4. **Limiter counts every fetch** (periodic, start and traffic-driven), so an unknown kid within a minute of any fetch is refused at once. After a failed fetch the loop retries in one minute instead of the full interval (an issuer down at start recovers without traffic).
5. **Subjects:** only printable ASCII 0x21-0x7e, at most 320 bytes, may map (covers the "non-ASCII or control character never maps" property; also excludes spaces). An unmapped subject is recorded in `auth.rejected` only when it passes that test.
6. **Duplicate kid** in a JWKS keeps the first key. Accepted content types: `application/json`, `application/jwk-set+json`.
7. **Middleware:** `Retry-After: 60` only for 503; the audit-failure 503 also sets it (same `respond` path). Response headers: `Content-Type: application/json`, `Cache-Control: no-store`, `X-Content-Type-Options: nosniff`. The request id is not echoed in a response header (Phase 5 may decide).
8. **`WithHTTPClient`** keeps only the client's Transport; Jar, Timeout and redirect policy are always ours. The default transport is `http.DefaultTransport` (honours proxy env vars for the real edge; Go never proxies loopback).
9. Section 4: `auth -> audit` added to the dependency sentence, the relationship list and the model (`yasshm.auth` lost `#planned`), with a callout. 5.4: pointer to the new reasons. F8 "Re-verify in" now points at the 5.7 callout.

## Open questions for the lead

- Confirm item 1 (nbf only on user tokens) -- it is the one place the implementation deliberately differs from the work order.
- Phase 5 should call `Start` before serving, `Close` on shutdown, read `Ready`/`Status` for `/healthz`, and wrap the MCP handler as `Middleware(a, auditLogger)(Require(mcpHandler))`.
