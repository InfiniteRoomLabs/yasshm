#!/usr/bin/env -S usage bash
#USAGE arg "[step]" help="A single gate step, or 'all' (default: all)"

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
step="${usage_step:-all}"

# Minimum total statement coverage, in percent. Only internal/testkit/... is
# excluded from the measurement (spec section 7); no other exclusion is allowed.
COVER_FLOOR=85

coverage_profile="$repo_root/coverage.out"

run_fmt() {
  echo "== fmt =="
  local bad
  bad=$(cd "$repo_root" && { git ls-files '*.go'; git ls-files --others --exclude-standard '*.go'; } | sort -u | xargs -r mise exec -- gofmt -l)
  if [ -n "$bad" ]; then
    echo "fmt: these files are not gofmt-clean (run gofmt -w on them):" >&2
    echo "$bad" >&2
    exit 1
  fi
}

run_lint() {
  echo "== lint =="
  (cd "$repo_root" && mise run lint)
}

# A skipped test is a silenced test. t.Skip, t.Skipf, t.SkipNow (also on b and
# f, also taken as a method value or split across lines) and testing.Short
# guards all count; each needs an issue link on the same line. Scanned: every
# _test.go file, every .go file under internal/testkit/, and every other .go
# file that imports "testing".
run_skipcheck() {
  echo "== skipcheck =="
  local all scan importers hits rc=0 offenders
  # mapfile + grep rather than xargs: xargs folds "no match" and "error" into one exit code.
  mapfile -d '' all < <(cd "$repo_root" && git ls-files -z --cached --others --exclude-standard -- '*.go')
  [ "${#all[@]}" -gt 0 ] || return 0
  mapfile -d '' scan < <(printf '%s\0' "${all[@]}" | grep -zE '(_test\.go|^internal/testkit/.*\.go)$' || [ $? -eq 1 ])
  mapfile -d '' importers < <(cd "$repo_root" && grep -lZ '"testing"' -- "${all[@]}" || [ $? -eq 1 ])
  mapfile -d '' scan < <(printf '%s\0' ${scan[@]+"${scan[@]}"} ${importers[@]+"${importers[@]}"} | sort -zu)
  [ "${#scan[@]}" -gt 0 ] || return 0
  hits=$(cd "$repo_root" && grep -nHE '\.(Skip|Skipf|SkipNow)\b|^[[:space:]]*(Skip|Skipf|SkipNow)\(|testing\.Short' -- "${scan[@]}") || rc=$?
  if [ "$rc" -gt 1 ]; then
    echo "skipcheck: grep failed (exit $rc)" >&2
    exit 1
  fi
  offenders=$(printf '%s\n' "$hits" | grep -vE 'https://github\.com/[^ ]+/issues/[0-9]+' | grep -v '^$' || true)
  if [ -n "$offenders" ]; then
    echo "skipcheck: skipped tests without an issue link on the same line:" >&2
    echo "$offenders" >&2
    exit 1
  fi
}

run_test() {
  echo "== test =="
  rm -f "$coverage_profile"
  (cd "$repo_root" && mise run test)
}

# The real-sshd half of the SSH contract suite (spec section 7). Its tests are
# named TestRealSSHD*. With sshd present they run verbosely with
# YASSHM_REQUIRE_SSHD=1 (so a missing binary fails instead of returning),
# together with the in-process half (TestContractSSHTest) in the same run.
# The step fails unless every scenario the in-process half passed also
# passed against real sshd (the count is derived, not hard-coded) and the
# real-sshd login test passed: "ran zero tests" or "ran some" cannot pass.
# Without sshd it prints one line, unless YASSHM_REQUIRE_SSHD=1 (CI), where
# it fails.
run_sshd() {
  echo "== sshd =="
  if ! command -v sshd >/dev/null 2>&1 && [ ! -x /usr/sbin/sshd ]; then
    if [ "${YASSHM_REQUIRE_SSHD:-}" = "1" ]; then
      echo "sshd: YASSHM_REQUIRE_SSHD=1 but sshd is not installed" >&2
      exit 1
    fi
    echo "check.sh: sshd is not installed here; the real-sshd lane runs in CI only (spec section 7)"
    return 0
  fi
  local log
  log=$(mktemp)
  # shellcheck disable=SC2064
  trap "rm -f '$log'" RETURN
  if ! (cd "$repo_root" && YASSHM_REQUIRE_SSHD=1 CGO_ENABLED=1 mise exec -- go test -race -shuffle=on -count=1 -v -run '^(TestRealSSHD.*|TestContractSSHTest)$' ./...) >"$log" 2>&1; then
    cat "$log"
    echo "sshd: the real-sshd tests failed" >&2
    exit 1
  fi
  # The evidence QA reads in the CI log: sshd's version, the F10 and A8
  # observations and every real-sshd verdict.
  grep -E 'realsshd: |F10 observed|A8 observed|^[[:space:]]*--- (PASS|FAIL): TestRealSSHD' "$log" || true
  local real inproc login
  real=$(grep -cE '^[[:space:]]*--- PASS: TestRealSSHDContract/' "$log" || true)
  inproc=$(grep -cE '^[[:space:]]*--- PASS: TestContractSSHTest/' "$log" || true)
  login=$(grep -cE '^[[:space:]]*--- PASS: TestRealSSHDLogin/' "$log" || true)
  if [ "${inproc:-0}" -lt 1 ] || [ "${real:-0}" -ne "${inproc:-0}" ] || [ "${login:-0}" -lt 1 ]; then
    cat "$log"
    echo "sshd: real-sshd contract scenarios passed: ${real:-0}, in-process: ${inproc:-0}, login test: ${login:-0}; every scenario must pass on both servers" >&2
    exit 1
  fi
  echo "sshd: $real of $inproc contract scenarios passed against real sshd, and the login test"
}

run_cover() {
  echo "== cover =="
  if [ ! -s "$coverage_profile" ]; then
    echo "cover: no coverage profile at $coverage_profile -- run the test step first (mise run check -- test)" >&2
    exit 1
  fi
  # -coverpkg=./... makes every test binary write its own copy of every block,
  # so the profile holds duplicates. Merge by block key (field 1): a block
  # counts once and is covered if any copy ran it.
  local total
  total=$(awk '
    NR == 1 && /^mode:/ { next }
    $1 ~ /\/internal\/testkit\// { next }
    { n[$1] = $2; if ($3 > 0) hit[$1] = 1 }
    END {
      for (k in n) { stmts += n[k]; if (k in hit) covered += n[k] }
      if (stmts == 0) { print "none"; exit }
      printf "%.6f", 100 * covered / stmts
    }
  ' "$coverage_profile")
  if [ "$total" = "none" ]; then
    echo "cover: the profile has no statements to measure" >&2
    exit 1
  fi
  printf 'cover: total %.1f%% (floor %s%%, internal/testkit excluded)\n' "$total" "$COVER_FLOOR"
  if ! awk -v t="$total" -v f="$COVER_FLOOR" 'BEGIN { exit !(t + 0 >= f + 0) }'; then
    printf 'cover: %.1f%% is below the floor of %s%%\n' "$total" "$COVER_FLOOR" >&2
    exit 1
  fi
}

run_vuln() {
  echo "== vuln =="
  (cd "$repo_root" && mise exec -- govulncheck ./...)
}

run_build() {
  echo "== build =="
  (cd "$repo_root" && mise run build)
}

run_arch() {
  echo "== arch =="
  if ! ls "$repo_root"/docs/architecture/*.c4 >/dev/null 2>&1; then
    echo "arch: no .c4 sources in docs/architecture -- skipping"
    return 0
  fi
  local likec4="pnpm dlx --config.enable-global-virtual-store=false -y likec4@${LIKEC4_VERSION:-1.59.2}"
  (cd "$repo_root" && $likec4 validate)
  (cd "$repo_root" && $likec4 format --check)
  # --check never writes, so the dirty-tree banner below stays meaningful.
  "$repo_root/scripts/arch-gen.sh" --check
  # Custom model rules (every service has a technology, no #deprecated element
  # has inbound relationships, ...) are a Vitest + LikeC4 API recipe in the
  # docs ("Enforce and validate your model"); add a run_arch_rules step here
  # when this project has a Node test runner.
}

steps=(fmt lint skipcheck test sshd cover vuln build arch)

run_step() {
  case "$1" in
  fmt) run_fmt ;;
  lint) run_lint ;;
  skipcheck) run_skipcheck ;;
  test) run_test ;;
  sshd) run_sshd ;;
  cover) run_cover ;;
  vuln) run_vuln ;;
  build) run_build ;;
  arch) run_arch ;;
  *)
    echo "check.sh: unknown step: $1" >&2
    exit 2
    ;;
  esac
}

if [ "$step" = "all" ]; then
  for s in "${steps[@]}"; do run_step "$s"; done
else
  run_step "$step"
fi

dirty=$(cd "$repo_root" && git status --porcelain)
if [ -n "$dirty" ]; then
  echo
  echo "!! FAIL: the tree is DIRTY -- a result on a dirty tree describes disk, not HEAD:"
  echo "$dirty"
  echo
  exit 1
fi

echo "check.sh: $step OK"
