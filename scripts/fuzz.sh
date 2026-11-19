#!/usr/bin/env bash
# Run every Fuzz* target in the module for 30 seconds each. Passes, saying so,
# when there are none yet.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

fuzztime="${FUZZTIME:-30s}"
count=0
while IFS=: read -r file name; do
  [ -n "$file" ] || continue
  pkg="./$(dirname "$file")"
  echo "fuzz: $name in $pkg for $fuzztime"
  mise exec -- go test -run '^$' -fuzz "^${name}\$" -fuzztime "$fuzztime" "$pkg"
  count=$((count + 1))
done < <(grep -rEo --include='*_test.go' '^func (Fuzz[A-Za-z0-9_]*)\(' . 2>/dev/null |
  sed -E 's#^\./##; s#:func (Fuzz[A-Za-z0-9_]*)\(#:\1#' | sort)

if [ "$count" -eq 0 ]; then
  echo "fuzz: no Fuzz targets in the module yet -- nothing to run"
fi
