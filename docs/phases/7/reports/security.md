# Phase 7 security review

Lane: security (read-only). Range: `038cdc0..7a7a41a` on `claude/serene-franklin-jv4pu7`.

## Verdict: BLOCK

2 blocking, 8 advisory. Both blocking findings are documentation fixes.

## Blocking

**1. BLOCKING -- the `look` account can rewrite its own `authorized_keys`, which undoes `restrict` and `from=` and opens a path around the gateway.** `deploy/README.md:69-80`, `docs/operator-guide.md:105`, `SECURITY.md:68`, spec section 8 line 421.
Evidence: the steps make `/home/look/.ssh` (0700) and `authorized_keys` (0600) owned by `look`. Any command the assistant runs as `look` -- including one written by a prompt injection in target output, which SECURITY.md lists as not guaranteed -- can append a key (`echo ... >> ~/.ssh/authorized_keys`) or remove `restrict`/`from=`. No built-in rule stops it (`internal/policy/paths.go:112` allows `authorized_keys` on purpose). After that, the account takes logins that never pass Access, the token check or the audit log, and they stay after the gateway is uninstalled. The docs treat `restrict` and `from=` as controls, but the account they restrict can turn them off.
Fix: in deploy/README step 4, have root own the key file through sshd:
```sh
install -d -m 0755 -o root -g root /etc/ssh/authorized_keys
install -m 0644 -o root -g root /dev/null /etc/ssh/authorized_keys/look
# put the restrict line in /etc/ssh/authorized_keys/look, then:
printf 'Match User look\n    AuthorizedKeysFile /etc/ssh/authorized_keys/%%u\n' > /etc/ssh/sshd_config.d/50-yasshm-look.conf
sshd -t && systemctl reload ssh   # or sshd, by distribution
```
Add this wording: "The account must not be able to change which keys log in as it: keep its `authorized_keys` in a root-owned file named by `AuthorizedKeysFile`, not in its home directory." Make the same change in the operator guide step 1, the SECURITY.md deployment assumption and spec section 8 (with a STATE AS OF callout). Add an MT-002 step that runs `test -w /etc/ssh/authorized_keys/look || test -w ~/.ssh/authorized_keys && echo writable || echo not-writable` and expects `not-writable`.

**2. BLOCKING -- nothing tells the operator to restrict who can push `v*` tags, and the ancestry check is not a control.** `.github/workflows/release.yml:3-5,59-63`. No guidance in `deploy/README.md`, `SECURITY.md` or the operator guide (grep for ruleset or tag protection finds nothing).
Evidence: a `push` on a tag runs the workflow file of the tagged commit. Anyone who can push a branch and a tag can tag an unmerged commit whose `release.yml` drops the "Check the tag" step, and that job then has `contents: write` and publishes binaries under this repository's name. The ancestry check catches mistakes, not someone with write access. Fork pull requests cannot push tags, and a branch named `v1` does not match `tags:` (both correct).
Fix: add a "Releases" paragraph to SECURITY.md (or the operator guide) and a Blocked-on-user item: "Before the first release, add a repository ruleset for tags matching `v*` that restricts creation, update and deletion to the repository admin (bypass list: you only). Protect the default branch (no force push, no deletion, pull request required). Turn on immutable releases if the repository offers it. The workflow's check that the tag is on the default branch prevents mistakes; it does not stop someone who can push tags." Change the comment at `release.yml:3-5` to say the same.

## Advisory

**3. ADVISORY -- `GH_TOKEN` is in the job-level `env`.** `release.yml:34-37`. Every step sees it: `mise-action` (tool downloads) and `mise run build` (Go module fetches, the build). Fix: put `GH_TOKEN` in the `env:` of only the "Check the tag" and "Create the release" steps.

**4. ADVISORY -- no provenance.** The docs say honestly that `SHA256SUMS` proves integrity, not origin (`deploy/README.md:48`). Smallest next step: in the `release` job add `id-token: write` and `attestations: write`, add `actions/attest-build-provenance` (pinned by SHA) over `dist/yasshm_*`, and document `gh attestation verify <file> --repo InfiniteRoomLabs/yasshm` in step 2. The toolchain is fetched by mise with no lock file; committing a `mise.lock` would pin the tool checksums too.

**5. ADVISORY -- `systemd-analyze security --offline=true` gives 1.2 (OK).** Flagged items: `PrivateNetwork` 0.5, `RestrictAddressFamilies` INET/INET6 0.3 and AF_UNIX 0.1, `PrivateUsers` 0.2, `IPAddressDeny` 0.2, `RootDirectory` 0.1, `DeviceAllow` (char-rtc, from `ProtectClock`) 0.1. Network, INET and IPAddressDeny must stay as they are: the gateway dials targets and the key-set host, and the allow list is the operator's (documented at `deploy/yasshm.service:98-107`). `RootDirectory` and `DeviceAllow` are fine. Two can be tightened once MT-004 has run on a real host: `PrivateUsers=yes` (the service's own user and group and root stay mapped, so `root:yasshm` 0640 config and `yasshm`-owned keys still read correctly), and dropping `AF_UNIX` (a CGO-off binary logging to an inherited stderr probably never calls `socket(AF_UNIX)`; INFERRED, so verify first). Everything else checked passes: `User=yasshm`, empty capability sets, `NoNewPrivileges`, `ProtectSystem=strict` with the two `ReadWritePaths` (the keys under `/etc` stay read-only), `ProtectHome`, `PrivateTmp`, `UMask=0077`, no `Environment=`, absolute `ExecStartPre`/`ExecStart` with no shell, output to the journal, and `StartLimitBurst` limiting restart loops (and there is no inbound credential that a restart could help brute-force).

**6. ADVISORY -- the example config marks only two values with REPLACE.** `deploy/config.example.yaml:44,65,88`; `deploy/README.md:110` says "Replace the values marked REPLACE: the AUD tag ..., the team domain, your email ...", but `team_domain`, `email` and `host` carry no marker. `example-team.cloudflareaccess.com` is a name in a namespace anyone can register, unlike `example.net`. An operator who replaces only the marked values ends up trusting someone else's team's keys. The audience still has to match, so this is not exploitable alone (INFERRED: AUD tags are generated by the vendor). Fix: add `# REPLACE` comments on those three lines. Also reword the `host_key` placeholder (line 97) from `..._FROM_ssh-keyscan_OR_THE_HOST` to `..._READ_ON_THE_TARGET`, and update the `sed` in the README quick start and any test that matches the old text.

**7. ADVISORY -- MT-002 misses root-equivalent groups.** `test/manual/MT-002-look-account-unprivileged.md` step 2 checks `root sudo wheel adm docker disk shadow`. Add `lxd`, `incus-admin`, `libvirt`, `kvm` and `systemd-journal`, and the same names to the list at `deploy/README.md:72`. With finding 1, add the key-file step described there. Otherwise the test checks something real: it fails on uid 0 or any of the listed groups, and step 4 fails if `/etc/passwd` is writable.

**8. ADVISORY -- the quick start uses a fixed path in `/tmp`.** `README.md` quick start, `T=/tmp/yasshm-try`. On a shared machine another user can create that path first. Fix: `T=$(mktemp -d)`.

**9. ADVISORY -- the lock advice contradicts itself.** `deploy/README.md:68` runs `passwd --lock look`, while `docs/operator-guide.md:275` (`ssh_auth_failed`) says to check "that the account is not locked". With `UsePAM no`, OpenSSH refuses key logins to a `!`-locked account. Fix: replace the lock with `usermod -p '*' look` (no password can match, and the account is not locked), and keep the troubleshooting row.

**10. ADVISORY -- private vulnerability reporting must be switched on.** `SECURITY.md:5` sends reporters to the Security tab form. That form exists only once the setting is on. Add it to the Blocked-on-user list next to finding 2.

## Checked and clean

- Release workflow: triggered by `tags: ["v*"]` only, with top-level `contents: read` and `contents: write` only in `release`. There is no `id-token` or `packages` permission. Every `uses:` in `release.yml` and `ci.yml` is pinned by a 40-hex SHA. No `${{ }}` appears in a `run:` line: the tag goes through `env:`, is quoted, and is matched against a strict SemVer pattern before use. `build.sh` checks `VERSION` again and clears `GOFLAGS`. `SHA256SUMS` covers exactly the two uploaded binaries. `--verify-tag` is set, `persist-credentials: false` is set, and the workflow uses no secret except `GITHUB_TOKEN`. Caches: the release job uses `cache: false`. Fork pull-request caches are scoped to `refs/pull/N/merge` and a tag run cannot restore them. The `check` job's caches feed no artifact.
- Edge guidance: nothing exposes the port or turns off the origin check. The Access policy admits only the operator, and service tokens are mapped only deliberately. The tunnel goes to `127.0.0.1:8822`. The `Cf-Access-Jwt-Assertion` statement matches `internal/auth/auth.go:6-7,62` (no cookie, no `Authorization`). Vendor claims are marked CONFIRMED or INFERRED with links.
- Install modes: config 0640 `root:yasshm`, keys 0600 in a 0700 directory and generated on the host as the service user, data and audit directories 0700, the binary installed root 0755, no `curl | sh`. Host key pinning requires an out-of-band fingerprint comparison.
- SECURITY.md and the guide: no overstatement ("not a sandbox", "guard rail, not a boundary"). The not-guaranteed list includes a local user taking the port, a flood, secrets in output and prompt injection.
- Manual tests: placeholders only. `Record` lines use enum words. Values live in the gitignored `mise.local.toml`. No step asks for a token or key in chat or a committed file, and none disables a safeguard.
- Hygiene: the only host names added are `example.net`, `example-team.cloudflareaccess.com` and vendor documentation hosts. The only addresses are loopback and `192.0.2.0/24`. The hex ids are synthetic audit samples or action SHAs. The commit messages are clean.
