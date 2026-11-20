# Phase 7 QA lane report

**Verdict: NEEDS WORK** -- 1 blocking, 6 advisory. The content commit `026dbfb` is green locally and in CI; the head `bc33d70` is red locally and in CI because a committed review report breaks the `docs` link test.

## Gate and CI

- Head `bc33d70`, `mise run check` in `/home/user/yasshm`, tree clean before and after: RED at `test`. `TestDocsLinks` (`internal/deprule/docs_test.go:156`): `docs/phases/7/reports/simplify.md:7`, `:9`, `:21` have relative Markdown links that do not resolve from the reports directory. Steps after `test` did not run.
- CI for `bc33d70` (job 136225695177): `check completed failure`, same three lines.
- `026dbfb` in a private clone, tree clean before and after: `e2e: 21 of 21 end-to-end scenarios passed against the real binary in 6s (uid 0)`, `deploy: systemd-analyze verify passed on deploy/yasshm.service (systemd 255 (255.4-1ubuntu8.18))`, `docs: 4 documentation checks passed`, `cover: total 94.7% (floor 85%, internal/testkit excluded)`, `check.sh: all OK`. CI for `026dbfb` (job 136220259523): success; its log has `deploy: systemd-analyze verify passed on deploy/yasshm.service (systemd 259 (259.9-0ubuntu0.1))`, so verify ran on the runner.

## Deliverables (GOAL stage 2)

All met at `026dbfb`: unit, example config, `deploy/README.md`, `release.yml` with its lint tests, operator guide with the earlier-phase notes, `SECURITY.md`, MT-001 to MT-005 (empty results, active in `TESTING.md:52`), README quick start, the `docs` step, CHANGELOG entries. `git diff 038cdc0..026dbfb -- internal cmd` touches only `_test.go` files: no product change.

## Quick start and install steps (private temp tree, loopback only)

Followed `README.md` quick start verbatim with `T` in a temp dir. The sandbox has no `ssh-keygen` (the text says it is needed), so a Go stand-in made the two keys (0600). Build, `version`, the `sed`, then `config check`: `config ok: 1 tenant, 1 server`, exit 0. Before `serve` I added `certs_url: http://127.0.0.1:1/certs` (finding 2). A Go probe fixed to `127.0.0.1:8822`: `/healthz` 503 `{"status":"degraded","detail":"key set not loaded","version":"026dbfb"}`, POST `/mcp` 401 `{"error":"unauthorized"}`, `/nope` 404, POST `/healthz` 405. SIGTERM: exit 0, `gateway.stopped` last, log 0600. All as documented.

`deploy/README.md` in a temp root: `sha256sum --check --ignore-missing SHA256SUMS` OK on the built file and FAILED on a truncated copy; `install` and `yasshm version` OK; the shipped example installed as the config is refused (key file missing, host key placeholder, exit 1) and accepted after the substitutions step 6 lists. By reading: `useradd`, `install -d` modes, `ReadWritePaths`, the `Exec*` paths and the 0640 root:yasshm config agree with what the binary refuses. Binary proofs: key 0640 -> `private_key_file: is accessible by group or other (mode 0640)`; config 0620 -> `config: is writable by group or other`; `known_hosts_file` 0660 -> `is writable by group or other`; audit dir 0703 -> `start-up failed` with `the audit log cannot be opened (...)`; data dir 0750 -> `the data directory or database is not private to this user (...)`. All match the guide's table.

## Unit

`systemd-analyze verify` on a stub-binary copy: no output, exit 0. `security --offline=true`: exposure 1.2 OK; open items are network, `RootDirectory`, `IPAddressDeny` (intentional, documented), `DeviceAllow`, `PrivateUsers`. `TimeoutStopSec=330s` vs longest stop: grace max 300 (`internal/config/limits.go:55`) plus `httpCloseWait` 5 s under one context (`internal/cli/serve.go`): 305 < 330. OK.

## Guide statements not pinned by a test (run against the binary)

1. SIGHUP is ignored with one WARN (`operator-guide.md:244`, `:248`): right; process survived and logged `SIGHUP ignored: ...`.
2. A healthy start logs `config loaded`, `listening`, `readiness` (`deploy/README.md` step 7): right.
3. A graceful stop exits 0 and writes `gateway.stopped` last (`operator-guide.md:158`): right.
4. The audit log is created 0600 (`deploy/README.md` step 1 table): right.
5. Start-up failure reasons for audit dir and data dir modes (`operator-guide.md:313-316`): right, text identical.

## Doc-drift mutations (private clone, targeted tests)

1. Guide default of `shutdown_grace_seconds` 20 -> 25: killed by `TestGuideConfigReference`.
2. Deleted `shutdown_grace_seconds` from the example: killed by `TestExampleHasEveryKey`, `TestExampleLimitComments`.
3. Removed `NoNewPrivileges`: killed by `TestUnitDirectives`.
4. `actions/checkout@<sha>` -> `@v7.0.1` in `release.yml`: killed by `TestWorkflowActionsPinned`.
5. README link to `deploy/READ-ME.md`: killed by `TestDocsLinks`.
6. SURVIVED: a second action appended to an MT-004 `Expect` line, and "Restart the gateway, and then ..." prefixed to a `Do (manual)` line: `TestManualTests` passed both. It checks shape, not one action per step; a heuristic could catch common forms, but it cannot know in general.

## release.yml (read only)

Tag `v0.1.0` on the default branch: `check` runs the gate through `ci.yml` (`workflow_call`, read-only); `release` (contents: write, `GITHUB_TOKEN` only) validates the tag form, confirms with `git merge-base --is-ancestor` that the commit is on the default branch, builds with `VERSION=0.1.0` (`yasshm_0.1.0_linux_amd64`, `_arm64`, `SHA256SUMS`, matching `deploy/README.md` step 2) and creates the release with `--verify-tag`. Tag on an unmerged commit: the gate runs, then `release` stops with `refusing: the tagged commit is not on the default branch`. Branch pushes and PRs cannot trigger it. The remote has no tags, releases or Release runs.

## Findings

1. **BLOCKING** -- `docs/phases/7/reports/simplify.md:7,9,21`: relative links that do not resolve; the gate's `test` step fails on the head and CI is red on `bc33d70`. Expect green. Fix: make them code spans or repo-correct relative paths (or exclude `docs/phases/*/reports` from the link test, if the lead prefers).
2. **ADVISORY** -- `README.md` quick start: as written, `serve` fetches `https://example-team.cloudflareaccess.com/cdn-cgi/access/certs`, a real Cloudflare host a real team could own, though the section says it runs on loopback. Suggest adding a `certs_url` on a closed loopback port in the quick start's `sed`.
3. **ADVISORY** -- `deploy/config.example.yaml:48`: the `REPLACE_...` audience passes `config check`, so a forgotten audience reports `config ok` and every token fails `wrong_audience`. Fails closed; consider refusing `REPLACE_` values.
4. **ADVISORY** -- `test/manual` format test survives mutation 6 (above).
5. **ADVISORY** -- `docs/progress.md:11` lists the gate without `deploy docs`; `CLAUDE.md`, `TESTING.md`, `README.md` and the spec 7 callout match `scripts/check.sh:286`. Update at ship.
6. **ADVISORY** -- `internal/cli/serve.go:68`: the hand-written SIGHUP log line has no `time` field, unlike every other line.
7. **ADVISORY** -- `README.md` says a release binary is stamped with "the tag"; it is the tag without `v` (`VERSION="${TAG#v}"`), so `yasshm version` prints `0.1.0`.

ASCII, wrapping, placeholder grep: clean.

## Limits (D16)

The phase: no product code change, no tag or release on the remote, results files empty, no commit claims a manual-test run. This lane: no `curl`/`wget`; the gateway ran only with `certs_url` on a closed loopback port and was probed only at `127.0.0.1`; no unit started, no user created, nothing written under `/etc`, `/usr/local`, `/var`; no MT run; no commit, push or tag. One slip: an empty variable made one command run `/opt/node22/bin/serve` (a Node tool on `PATH`, not yasshm), which exited at once with an update notice, so it may have queried the npm registry; no target or edge involved. The private directory is deleted.
