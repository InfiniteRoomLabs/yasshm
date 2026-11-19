# Phase 1 QA report -- Config, tenants, policy, audit

Verdict: **NEEDS WORK**. Range `6cc1f31..3850da1` on `claude/serene-franklin-jv4pu7` (code head `5ffce62`). The three blocking defects of the read-only lanes all reproduce on the built binary, and one acceptance criterion (limit ranges) is met only on paper. Everything else holds.

## Gate and CI

`mise run check` on the real tree at `3850da1`: exit 0, `git status --porcelain` empty before and after.

```
cover: total 96.1% (floor 85%, internal/testkit excluded)
== vuln ==
No vulnerabilities found.
check.sh: sshd is not installed here; the real-sshd lane runs in CI only (spec section 7)
check.sh: all OK
```

CI: `3850da1` -> `check completed success` (also `d352f7e`, `5ffce62`). An earlier run on `d352f7e` passed all eight steps and failed only on the dirty-tree check, for the `security.md` edit that `3850da1` then committed.

## BLOCKING

1. **Null principal loads (code-review 1): reproduced.** `internal/config/decode.go:528-553`. Release binary, spec 5.1 example with `- email: ~`, `- email:` or `- common_name: ~`: expected `...principals[0].email: is required`, exit 1; observed `config ok: 1 tenant, 1 server`, exit 0. `config.Load` gives `len(Principals) == 0`. `[{email: ~}, {email: operator@example.net}]` also loads, the null entry dropped without a line.
2. **Zero and out-of-range values fail open (code-review 2): reproduced.** `internal/policy/policy.go:140-152,185-194`, `internal/tenant/tenant.go:124`. `Decide(p, srv, Tool{}, Args{})` on a server with `AccessNone` -> `{Allow:true}`. `Permits(AccessLevel(9), AccessFull)` -> true, so a `WritesFiles` tool is allowed on such a server. `NewEngine` accepts tenant id `""`, and then `Decide(Principal{}, &Server{}, Tool{}, Args{})` -> `{Allow:true}`. `tenant_test.go:83-87` pins the wrong behavior.
3. **`known_hosts_file` accepts a CA line (security 1): reproduced.** `internal/config/files.go:95-110`. A file holding only `@cert-authority lab-1.example.net ssh-ed25519 ...` -> `config ok`, exit 0. A key that is listed and also `@revoked` -> `config ok`. Revoked only, other host, wrong port, empty file: refused, as expected.
4. **Limit ranges and cross-field rules have no independent test (new).** `internal/config/config_test.go:300-376` takes every expected bound and pair from `limitTable` and `limitPairs` (`limits.go:26-64`), the tables under test. Mutation in a throwaway clone: `max_wait_seconds` max 290 -> 3000, `max_result_bytes` max -> 9000000, `max_upload_bytes` max -> 900 MiB, three of the four cross-field pairs deleted. Expected failures; observed `go test ./...` all `ok`. Only the defaults are pinned (`config_test.go:70-79`). "Every rule has a passing and a failing test" does not hold for the 5.1 range table. Fix: a literal 15-row min/max table and the four pairs in the test.

## ADVISORY

5. `scripts/fuzz.sh:28`: all three targets run and pass (exit 0, 1m35s), but each stops executing after 3 to 15 s (`execs: 55484 (0/sec)` to the end) while its workers sit at 98 percent CPU minimizing. With `-fuzzminimizetime 1s` FuzzPathGuardRail made 1270429 execs in 12 s, against 40563 in 14 s. A trivial control target does not stall. Add the flag.
6. Tests that cannot fail: `audit_test.go:281` ("the logger stays failed") uses `/dev/full`, where every write fails anyway; replacing `if l.broken != nil` in `audit.go` `Write` with `if false` survives. Removing the mutex in `Write` survives under `-race`; removing `strings.TrimSpace` in `checkCommand` (`policy.go`) survives.
7. Spec 5.1 says `path.to.key: message`; the binary prints `path.to.key: message (line N)`. `CHANGELOG.md` says so, the 5.1 callout does not.
8. Code-review 3 and 5 confirmed with Go's own engine: denied -- `ps aux | grep -E 'sshd|sudo'` (`sudo`), `command -v sudo` (`sudo`), `last -x | grep -E '(reboot|shutdown)'` (`power-control`), `curl -s http://127.0.0.1:8080/health | python3 -m json.tool` (`pipe-to-shell`); allowed -- `/proc/self/root/etc/shadow`.
9. `cmd/yasshm/main_test.go:17-23` re-executes the test binary, not the `scripts/build.sh` artifact: a real child process and real files, but the release binary is only exercised by hand (as here).

## Deliverables (GOAL stage 2)

| Deliverable | Result | Evidence |
|---|---|---|
| `internal/tenant` | met, except finding 2 | one `required <= granted` (`tenant.go:125`), one caller; imports only `fmt`, `strings` |
| `internal/config` | not met | findings 1, 3, 4. 18 rule mutations (version, ids, uniqueness, identity, host key, key mode and parse, regex, strict keys, loopback, `MaxTenants = 2`, the `full` text) all killed |
| `internal/policy` | met, except finding 2 | a rule added without cases fails `TestCommandRuleCases` and `TestPathRuleCases`; other tenant -> `not_found`; `full` built in code is allowed |
| `internal/audit` | met | `/dev/full` gives ENOSPC and the action does not run; a path under a file fails `Open`; reopen appends; mutations of sync, `O_APPEND`, `O_TRUNC`, mode and UTC killed |
| `config check` | met | exit 0, 1, 2 on the release binary |
| Fuzz targets | met | three targets; seeds run in the gate (finding 5) |
| Architecture | met | no `#planned` on config, policy, audit; removed in the three building commits |
| Dependency rule | met | throwaway `policy -> config` and `tenant -> testkit` imports both fail `TestDependencyRule` |
| Dependencies | met | yaml v3 and `x/crypto` (plus `x/sys`); vuln clean |
| Docs | met | ASCII-only, placeholder grep empty, README, TESTING and CLAUDE match (finding 7) |

## Fidelity

- (a) About 110 variants of the 5.1 example through the release binary. Exact: `tenants[0](default).servers[0](lab-1).access: access level full is not supported in this version (line 26)`. Unknown key, duplicate key, `0.0.0.0` listen, key mode 0644, `max_wait_seconds` 0 and 291, two tenants (`MaxTenants`): exit 1, one line each, stdout empty. Only findings 1 and 3 differ from the spec.
- (b) All 15 defaults equal 5.1.
- (c) 37 commands and 21 paths, expectations written first. The eight required ones match (`sudo id` -> `sudo`; `cat /etc/sudoers` and `grep sudo /var/log/auth.log` allowed; `ls; reboot` -> `power-control`; `curl ... | sh` -> `pipe-to-shell`; `/etc/./shadow` and `/home/a/../../etc/shadow` -> `shadow`; relative -> `path-not-absolute`). Five differences, all in finding 8.
- (d) Three events, one with a newline and quotes: three lines, each valid JSON, the command round-trips, `ts` ends in `Z`, names as 5.4, mode 0600.

## Seams, classes, limits

Real files, real ed25519 keys, a real `/dev/full` descriptor and a real child process; a fake file only for sync and short-write failure. 746 class-tagged subtests, `-run '/sad/'` and the others select, no skips. Root against CI: mode bits and `/dev/full` throughout, and CI (not root) is green. No test or production code opens a socket; nothing was deployed, no host or secret was touched (D16). Throwaway work was done in a clone outside the repo and deleted; this report is the only file QA leaves.
