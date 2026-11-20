# Phase 4 security review -- Authenticator

Range `64101fd..58ce7c3`, read-only. Library source read in the module cache (`go-jose/v4@v4.1.9`, cited as `jose:`).

## Verdict: PASS

0 BLOCKING, 7 ADVISORY. No path was found that returns a principal without an RS256 signature from the configured key set plus exact `iss`, matching `aud`, `exp`/`iat` (and `nbf` for users) and a tenant match; no token, key or URL reaches a log, error, audit event or response.

## Item 1: where each attack is refused

| Attack | Refused where |
|---|---|
| `alg` none (any case), missing, HS256, RS384/512, PS256, ES256 | `jose:jws.go:306-308`, exact string compare against the list from `auth.go:249`; before the key lookup at `auth.go:264` |
| HS256 with the public key (if the list ever widened) | the key is `*rsa.PublicKey`, so only RSA algs verify: `jose:signing.go:200`, `jose:asymmetric.go:313-321` |
| `jwk`, `jku`, `x5u`, `x5c` header | parsed, never used: the key comes only from `auth.go:264`; `jose:jwk.go:945-947` returns the caller's key as is; nothing is fetched |
| `kid` traversal, SQL-ish, long | a map key only (`keyset.go:141`), never logged; empty `auth.go:261`; non-string `jose:shared.go:432` |
| `kid` of a key with `use: enc`, another `alg`, a duplicate | filtered at load: `keyset.go:293`, `keyset.go:296` |
| JSON serialization, multiple signatures | no dots, `jose:jws.go:408-410`; `auth.go:257` |
| JWE, extra dots | `jose:jws.go:416` |
| padded base64, whitespace | `jose:jws.go:424-436` (raw URL alphabet; nothing stripped). Non-zero trailing bits decode: harmless, the signature covers the decoded bytes |
| duplicate header names | `jose:json/decode.go:638-643` |
| duplicate or case-variant claim names | NOT refused, last wins (finding 3); verifier and mapper decode the same bytes with the same decoder, so no differential |
| `aud` number, object, nested array | `jose:jwt/claims.go:109-124` -> `auth.go:274` |
| `exp` string / float / huge | `jose:jwt/claims.go:72` / truncated at `:87` / `:83` |
| `iss` trailing slash, case, userinfo, look-alike | exact compare, `jose:jwt/validation.go:80` |
| RSA under 2048 bits; exponent 1 | `keyset.go:290`; `jose:jwk.go:589` |

## Items 2 to 10

- **Cross-tenant / cross-application (2):** another team's token fails the key lookup or signature, then `iss`; another application's fails `aud` (`validation.go:92-103`). The key set URL comes from config only (`auth.go:134-137`); `certs_url` is https or loopback http in the loader (`config/rules.go:106-121`) and again at `auth.go:139` -- an operator choice. `type` is not checked and need not be: `aud` is the binding (an `org` token carries another audience, INFERRED).
- **Time (3):** valid through `exp`+30 s inclusive, from `nbf`-30 s, `iat` at most 30 s ahead. The `nbf` discriminator is the signed `email` claim (`auth.go:286`): a token with an email always needs `nbf`. A zero clock falls back to the real time (`validation.go:107-110`), not to a bypass.
- **Mapping (4):** `identity.go:41-72`; a nil error needs exactly one tenant with an id and a printable subject, so a zero principal cannot be returned with nil.
- **Key set (5):** same-host redirects, 10 s, 1 MiB after decompression, replaced whole on success, one fetch a minute shared by all callers. Cost of one hostile request: decode at most 16 KiB, one SHA-256 and one RSA-2048 public operation (only with the public `kid`), no egress -- and one fsync (finding 1).
- **Middleware (6), secrets (7):** every return of `middleware.go:69-92` traced; library errors are discarded, never wrapped (`auth.go:250-256`, `270`). The unknown-kid versus bad-signature timing difference reveals only the public `kid`.
- **Test kit (8):** keys generated at run time, loopback only, `TestTestkitOnlyFromTests` holds.
- **Supply chain (9):** `go-jose` has no requirements; `go mod verify` and `govulncheck ./...` clean; CI, scripts and `mise.toml` untouched.
- **Hygiene (10):** clean in the diff, report and commit messages.

## Findings

1. **ADVISORY (highest) -- an unauthenticated flood costs one synced audit line per request.** `middleware.go:78` writes `auth.rejected` for every rejection; `audit.go:148-184` holds one mutex across write and fsync. Anyone who reaches the listener (the D4 premise) delays real requests behind fsyncs and grows the log without bound; a full disk marks the logger broken until restart (`audit.go:176-180`), after which every request is 503 (`middleware.go:84-88`). Fails closed, and spec 5.7 demands the event, hence not blocking. Fix: limit `auth.rejected` for rejections before signature verification (a token bucket, with a `suppressed` count on the next event), or record the risk in spec section 6 and bound it in Phase 5.
2. **ADVISORY -- the middleware trusts any nil-error return.** `middleware.go:71-91`: an `Authenticator` returning a zero principal gets `auth.accepted` and `next` runs; only `Require` stops it. Fix: apply the `PrincipalFrom` predicate in `serve`, else record `invalid_token` and refuse; add the test.
3. **ADVISORY -- claims decoded with `encoding/json`** (`auth.go:274`): names match case-insensitively and the last duplicate wins. Only the issuer can exploit it. Fix: decode both structs with `github.com/go-jose/go-jose/v4/json` (case-sensitive, duplicates refused; what the library's own `Claims` uses); add duplicate and case-variant cases.
4. **ADVISORY -- last-good keys never age out** (`keyset.go:200-202`): with the endpoint unreachable, a retired key stays trusted indefinitely. Fix: after 7 days without a successful fetch (F8) drop the keys and report it in `Status`.
5. **ADVISORY -- library-enforced refusals without a test (D15):** an embedded `jwk`/`jku`/`x5c` carrying the attacker's key, JSON serialization, five segments, an unknown `crit`, exponent 1. Add to `tokenClasses` and `TestParseKeySet`.
6. **ADVISORY -- `common_name` is taken as a service token whatever the edge used** (`identity.go:49-53`). INFERRED: an Access policy admitting client certificates would put the certificate's name there. Phase 7 operator docs should say to admit service tokens only; confirm on the vendor page.
7. **ADVISORY -- minor.** The user subject keeps the token's letter case (`identity.go:48`), so one person can appear as two principal strings in the audit log and store: lower-case it. Fixtures use registrable team names on the vendor's real domain (`accesstest.go:42`, `auth_test.go:95`); never dialled, but a name under `example.net` cannot collide.
