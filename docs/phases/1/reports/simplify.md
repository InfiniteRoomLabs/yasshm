# Phase 1 simplification report -- Config, tenants, policy, audit

Range `6cc1f31..1546eb6`. Nothing was changed, built or run. Verdict: little to cut -- about 75 safe lines (items 1 to 4), about 65 more that can wait (5 and 6). No proposal changes a message, path, order, code, rule name, audit field or exported signature.

## APPLY-RECOMMENDED

1. **`cli_test.go`: one case type, one runner.** `internal/cli/cli_test.go:95-162` re-declares the case struct of `TestRun` and copies its loop (`:18-87`). After: one `type runCase` and `runCases(t, []runCase)`; the two `failWriter` subtests (`:174-185`) become rows with `failStdout: true`, and the 2000-byte stderr cap becomes the default `maxErr`. Test names and assertions stay; product code untouched. Saves about 40 lines. Risk: none.

2. **`decode.go`: one helper for a required mapping item.** The sequence `usable` -> `isNull` -> "expected a mapping, got null" -> `mapping` -> nil check is written out at `internal/config/decode.go:490-500`, `:517-527`, `:586-593` and `:622-632`. After: `func (d *decoder) item(path string, n *yaml.Node, keys ...string) *fields` with exactly those steps. Same calls, order and messages. The identities site gains a `usable` call that `entries` makes anyway; `newError` drops the identical duplicate (`config.go:159-164`), as at the other three sites today. Saves about 16 lines. Risk: low.

3. **`policy`: drop the parallel `operatorCommand` type.** `internal/policy/policy.go:116-120` duplicates `commandRule` (`commands.go:8-12`), with `name` against `Name`; paths already use one `pathRule` for both kinds (`policy.go:167`). After: `tenantRules.commands []commandRule`; `:160` builds `commandRule{Name: ..., Pattern: p, re: re}`; `:217-221` reads `o.Name`. Rule names and match order unchanged. Saves about 6 lines and one type. Risk: low.

4. **Standard-library one-liners.** (a) `decode.go:174-181`, the known-key loop -> `if !slices.Contains(keys, e.key)`. (b) `rules.go:38-45`, the body of `hasControl` -> `strings.ContainsFunc(s, unicode.IsControl)`; `IndexFunc(...) >= 0` at `:50` and `:59` -> `ContainsFunc`. (c) `commands.go:44-46`, `cmd(word)` -> `return cmdArgs(word, wordEnd)`; the bodies differ only in the tail. (d) `limits.go:73-80`, `limitByKey` returns a bool that all 11 callers discard; return the `limitSpec` only. Saves about 14 lines. Risk: none.

## OPTIONAL

5. **`decode.go`: generalise `absPath` into `fields.checked(key, required, check func(string) string) string`.** `absPath` (`:393-403`) is this shape for one check. Eight sites repeat it by hand, naming the key three times each (`f.str("host")`, `p+".host"`, `f.vals["host"]`): `:381-387`, `:415-421`, `:430-436`, `:437-443`, `:444-450`, `:595-601`, `:641-647`, `:648-654`. After: `s.Host = f.checked("host", true, checkHost)`. `fields.strings` can take the check the same way (`:741-756` -> two lines; the tag count at `:681` must stay on the string items). `join(f.path, key)` equals every hand-built path. Saves about 40 lines. Risk: low to medium -- message literals move, and the tests compare path and message but not `Line`, so a wrong line node would pass. Better at a phase start than in the fix commit.

6. **Test support and comments.** `config_test.go:545-572`: four known_hosts subtests of one shape -> a table. `helpers_test.go:159`, `:161-167`: two helpers used once; `keys.keyPEM` and `keys.hostPub` are read only in `genKeys`. `cli_test.go:94` builds a `configtest.Files` literal to chain a second `Rewrite`; a variadic `Rewrite` removes it. `config.go:200-201` and `audit.go:67` repeat the `nolint` reason beside them. Saves about 25 lines. Risk: none.

## DO-NOT-APPLY

7. **Struct decoding with `KnownFields`.** Checked in the library source: its messages are `line N: field x not found in type T`; it has no key paths, required-key check or alias refusal, and skips the duplicate check for `yaml.Node`. The walk would stay and the message form would change. A recorded gotcha (`CLAUDE.md`, spec F18).

8. **Merging the operator-pattern checks** of `config/rules.go:148-176` and `policy.go:154-168`. `policy` may not import `config`, and `NewEngine` takes tenants built in code.

9. **Moving the `config` tests onto `configtest`.** About 150 cases edit exact strings of `baseTemplate`; the churn outweighs 20 shared lines.

## Left alone, on purpose

The D5 and D7 seams; `WithClock` and `CleanPath` (exported); the access `switch` at `decode.go:670-678` (its messages differ from `ParseAccessLevel`); the mode-bit check in both `files.go:62` and `audit.go:81` (no shared home); the limits table repeated in spec 5.1; the evidence comments in `limits.go` and `commands.go`; the `isX` and `checkX` mix in `rules.go` (item 5 would settle it).

## Naming notes (not proposals)

- `Config.Gateway`, `ServerSettings`, YAML `server` and `serverSettings()` name one thing four ways (exported).
- For the code-review lane: `decode.go:468` takes a duplicate tenant id's line from the tenant node, `:636` a duplicate server id's from the id node.
