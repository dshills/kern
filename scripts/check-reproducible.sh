#!/usr/bin/env bash
# Builds the compiler twice for every release target and fails when any
# binary differs (PLAN.md Phase 5: reproducible release builds). The builds
# use release.sh's flags, version included, so they check what ships.
# Usage: scripts/check-reproducible.sh [version]   (default: git describe)
set -euo pipefail
cd "$(dirname "$0")/.."
version="${1:-$(git describe --always --tags --dirty)}"
flags="-s -w -buildid= -X main.version=${version}"
export CGO_ENABLED=0
tmp="$(mktemp -d)"
for target in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64; do
  os="${target%/*}"; arch="${target#*/}"
  GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags="$flags" -o "$tmp/a-$os-$arch" ./cmd/kern
  GOOS="$os" GOARCH="$arch" go build -a -trimpath -ldflags="$flags" -o "$tmp/b-$os-$arch" ./cmd/kern
  if cmp -s "$tmp/a-$os-$arch" "$tmp/b-$os-$arch"; then echo "$target: reproducible ($(shasum -a 256 "$tmp/a-$os-$arch" | cut -c1-16))"; else echo "$target: DIFFERS"; exit 1; fi
done
rm -rf "$tmp"
