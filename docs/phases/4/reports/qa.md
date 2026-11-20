# Phase 4 (Authenticator) -- QA / reality-check lane

**Verdict: PASS.** 0 BLOCKING, 3 ADVISORY. Subject: `claude/serene-franklin-jv4pu7`, head `97cd9c4` (code head `8477f45`), phase base `64101fd`. Throwaway work ran in a clone outside the repo, now deleted.

## 1. Gate and CI

`mise run check` on the current tree: exit 0 in 28 s; `git status --porcelain` empty before and after.

```
ok  	github.com/InfiniteRoomLabs/yasshm/internal/auth	5.437s
== sshd ==
check.sh: sshd is not installed here; the real-sshd lane runs in CI only (spec section 7)
== cover ==
cover: total 95.3% (floor 85%, internal/testkit excluded)
== vuln ==
No vulnerabilities found.
...
check.sh: all OK
```

CI (`gh api .../commits/<sha>/check-runs`): `97cd9c4` -> `check completed success`; `8477f45` -> `check completed success`.

## 2. GOAL stage 2 deliverables

| Deliverable | Result | Evidence |
|---|---|---|
| `internal/auth`, interface and Access implementation | met | section 3: 33 hostile requests, 33 as predicted |
| Key set (start, cache, refresh, limit, down at start, timeout, cap, redirects) | met | section 5 |
| Middleware (401/403, events, context, `Require`, `MustPrincipal`) | met | section 3; mutations 8 and 9 |
| `accesstest` with every listed class | met | 68 classes in `tokenClasses()`; rotation in `TestRotation` |
| Every class: reason + status + event | met, two gaps (finding 2) | `TestTokenClasses`, `TestTokenClassesThroughMiddleware` |
| Nothing leaks | met | `leak_test.go:21-101` scans the audit file, both loggers, error strings, bodies and response headers for each token, each segment of 8+ bytes and key fragments |
| Refetch limit: counter and injected clock | met | `TestRefetchLimit`; my K1 |
| Fuzz target with a seed corpus | met | `mise run fuzz` prints `fuzz: FuzzResolve in ./internal/auth`; 11 seeds run in the gate |
| Dependency rule covers `auth` | met | a throwaway `import _ ".../internal/store"` in `internal/auth` -> `TestDependencyRule/happy/auth` FAIL |
| One new dependency, `vuln` clean | met | `go.mod:6` go-jose v4.1.9 only |
| `yasshm.auth` not `#planned`, diagrams current | met | `model.c4:37-39`, changed in the build commit `aa6e393`; `arch` green |
| README, TESTING, CLAUDE gotcha, changelog | met | section 8 |
| Gate green, coverage >= 85, CI green | met | section 1 |

## 3. Hostile tokens

Expectations written before the run; real `Authenticate`, middleware and audit log. All matched on reason, status, event and handler call: (a) `wrong_issuer` 401; (b) `wrong_issuer` 401; (c) accepted 200; (d) prefix and superstring `wrong_audience` 401; (e) now-30 accepted, now-31 `expired`; (f) now+30 accepted, now+31 `not_yet_valid`; (g) now+30 accepted, now+31 `issued_in_future`; (h) user without `nbf` `missing_claim` 401, service without `nbf` accepted; (i) `ambiguous_identity` 401; (j) accepted, principal `user:Operator@example.net` (finding 3); (k) `unmapped_principal` 403, no principal in the event; (l) `bad_signature` 401; (m) embedded `jwk` with `kid` key-0 `bad_signature`, with no or an unknown `kid` `unknown_key`; (n) `malformed_token`; (o) `oversized_token`; (p) `malformed_token`; (q) Bearer only and cookie only `missing_token` 401. Also `keys_unavailable`: 503, `Retry-After: 60`, its event.

(r) Duplicate `email`: **the last one wins.** Mapped then unmapped -> 403, event principal `user:stranger@example.net`; unmapped then mapped -> 200 as the mapped user. Duplicate `aud` likewise. The claim checks and the mapper decode the same bytes with the same decoder, so they agree with each other; the library's own decoder does not agree with them (finding 1).

## 4. Mutations: 10 of 10 killed

Failing tests per mutation: audience 12, issuer 12, `exp` required 4, `nbf` on a user token 4, `iat` 4, HS256 allowed 6, limiter removed 11, `auth.accepted` not written 72, `next` called after a failed accepted write 2, first-tenant fallback 16 (`FuzzResolve` seeds included).

## 5. Key set, measured

- 1000 random `kid`s after the load: 1 fetch in total; +60 s and 1000 more: 2; +59 s and 1000 more: 2.
- 50 concurrent requests during one held refetch: 1 fetch, 50 `unknown_key`.
- Down at `Start`: `keys_unavailable`, `Ready` false; issuer up, same instant and +59 s: still unavailable, 0 fetches; at +60 s a junk token with an unknown `kid` starts the one fetch and `Ready` flips; the valid token then passes. The traffic-free retry is a real 60 s timer (`auth.go:174`), not measured.
- Rotation: new-key token `unknown_key` inside the minute, accepted after +60 s; the old-key token passes while served and, once dropped at the issuer, until the next fetch (cached; no test states this), then `unknown_key`.
- 5 MiB body: `too_large` in 50 ms, not ready. Redirect to another host or to `https://example.net`: `redirect`, 1 request at the issuer, not ready; a same-host redirect is followed.

## 6. Flakiness and leaks

`CGO_ENABLED=1 mise exec -- go test -race -shuffle=on -count=20 ./internal/auth/ ./internal/testkit/accesstest/`: `ok` in 70.2 s, 0 failures; one pass 4.9 s. `leakcheck.Check` runs in `TestFetchCancel` and `TestStartClose` (`keyset_test.go:290,344`).

## 7. Limits (D16)

`strace -f -e trace=connect` on the package's test binary: 26 connects, all to `127.0.0.1`. The package, with my tests, also passes in a network namespace with loopback only. No credential read.

## 8. Docs

README, TESTING.md, the CLAUDE.md gotcha, the changelog, the spec 5.7 callout and rows F7/F8 match what ran. Files changed since the base are ASCII-only; the placeholder grep prints nothing.

## Findings

1. **ADVISORY -- `internal/auth/auth.go:274`** (security 3, code review 1; new evidence from a run). The same signed tokens through the library's `jwt.ParseSigned(...).Claims(key, ...)`: duplicate `email` -> the library errors `duplicate key 'email'`, `Authenticate` answers 403 for the last one; `{"email": stranger, "EMAIL": operator}` -> the library sees the stranger, `Authenticate` accepts the operator (200); `{"Email": operator}` alone -> the library sees no email, `Authenticate` accepts. Only the issuer can sign such a token. Decode with the library's decoder and add these three classes.
2. **ADVISORY -- `internal/auth/keyset_test.go:145-178,191-207`.** Rotated-key tokens and `keys_unavailable` are asserted by reason only (the latter also by status at `auth_test.go:381-391`, without its event; code review 6). Observed through the middleware: correct. Add both to the middleware table.
3. **ADVISORY -- `internal/auth/identity.go:48`** (security 7). Expected and observed: principal `user:Operator@example.net` for the configured `operator@example.net`.
