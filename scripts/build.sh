#!/usr/bin/env bash
# Build the release files: dist/yasshm_<version>_linux_<arch> for amd64 and
# arm64 plus dist/SHA256SUMS. CGO off, -trimpath, version metadata injected
# with -ldflags -X; GOFLAGS is cleared so the environment cannot add build
# flags. <version> is VERSION from the environment if set, else
# `git describe --tags --always --dirty`.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

version="${VERSION:-$(git describe --tags --always --dirty)}"
if ! [[ "$version" =~ ^[0-9A-Za-z][0-9A-Za-z._+-]*$ ]]; then
  echo "build: invalid VERSION '$version' (allowed: letters, digits, . _ + -, starting with a letter or digit)" >&2
  exit 2
fi
commit="$(git rev-parse HEAD)"
# Reproducible: SOURCE_DATE_EPOCH if set, else the HEAD commit time.
epoch="${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct)}"
date="$(date -u -d "@$epoch" +%Y-%m-%dT%H:%M:%SZ)"
pkg="github.com/InfiniteRoomLabs/yasshm/internal/cli"
ldflags="-s -w -X ${pkg}.Version=${version} -X ${pkg}.Commit=${commit} -X ${pkg}.Date=${date}"

rm -rf dist
mkdir -p dist
for arch in amd64 arm64; do
  out="dist/yasshm_${version}_linux_${arch}"
  echo "build: $out"
  CGO_ENABLED=0 GOFLAGS= GOOS=linux GOARCH="$arch" \
    mise exec -- go build -trimpath -ldflags "$ldflags" -o "$out" ./cmd/yasshm
done
(cd dist && sha256sum yasshm_* >SHA256SUMS)
echo "build: dist/SHA256SUMS"
