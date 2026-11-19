#!/usr/bin/env bash
# Run every Fuzz* target in the module for 30 seconds each. Passes, saying so,
# when there are none yet.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

fuzztime="${FUZZTIME:-30s}"
count=0
mapfile -d '' tests < <(git ls-files -z --cached --others --exclude-standard -- '*_test.go')
targets=""
if [ "${#tests[@]}" -gt 0 ]; then
  rc=0
  targets=$(grep -HEo '^func (Fuzz[A-Za-z0-9_]*)\(' -- "${tests[@]}") || rc=$?
  if [ "$rc" -gt 1 ]; then
    echo "fuzz: grep failed (exit $rc)" >&2
    exit 1
  fi
fi
while IFS=: read -r file decl; do
  [ -z "$file" ] && continue
  name="${decl#func }"
  name="${name%(}"
  pkg="./$(dirname "$file")"
  echo "fuzz: $name in $pkg for $fuzztime"
  mise exec -- go test -run '^$' -fuzz "^${name}\$" -fuzztime "$fuzztime" "$pkg"
  count=$((count + 1))
done <<<"$targets"

if [ "$count" -eq 0 ]; then
  echo "fuzz: no Fuzz targets in the module yet -- nothing to run"
fi
