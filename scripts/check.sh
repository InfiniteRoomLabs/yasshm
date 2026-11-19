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
# f), and testing.Short() guards all count; each needs an issue link on the
# same line.
run_skipcheck() {
  echo "== skipcheck =="
  local hits offenders
  hits=$(cd "$repo_root" && {
    git ls-files '*_test.go' 'internal/testkit/*'
    git ls-files --others --exclude-standard '*_test.go' 'internal/testkit/*'
  } | sort -u | xargs -r grep -nHE '\.(Skip|Skipf|SkipNow)\(|testing\.Short\(\)' || true)
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

run_cover() {
  echo "== cover =="
  if [ ! -s "$coverage_profile" ]; then
    echo "cover: no coverage profile at $coverage_profile -- run the test step first (mise run check -- test)" >&2
    exit 1
  fi
  local total
  total=$(awk -v floor="$COVER_FLOOR" '
    NR == 1 && /^mode:/ { next }
    $1 ~ /\/internal\/testkit\// { next }
    { stmts += $2; if ($3 > 0) covered += $2 }
    END { if (stmts == 0) { print "none"; exit } printf "%.1f", 100 * covered / stmts }
  ' "$coverage_profile")
  if [ "$total" = "none" ]; then
    echo "cover: the profile has no statements to measure" >&2
    exit 1
  fi
  echo "cover: total ${total}% (floor ${COVER_FLOOR}%, internal/testkit excluded)"
  if ! awk -v t="$total" -v f="$COVER_FLOOR" 'BEGIN { exit !(t + 0 >= f + 0) }'; then
    echo "cover: ${total}% is below the floor of ${COVER_FLOOR}%" >&2
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

steps=(fmt lint skipcheck test cover vuln build arch)

run_step() {
  case "$1" in
  fmt) run_fmt ;;
  lint) run_lint ;;
  skipcheck) run_skipcheck ;;
  test) run_test ;;
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
  # ${steps[@]+...}: bash 3.2 (macOS) errors on an empty array under set -u.
  for s in ${steps[@]+"${steps[@]}"}; do run_step "$s"; done
else
  run_step "$step"
fi

if ! command -v sshd >/dev/null 2>&1 && [ ! -x /usr/sbin/sshd ]; then
  echo "check.sh: sshd is not installed here; the real-sshd lane runs in CI only (spec section 7)"
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
