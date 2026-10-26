#!/usr/bin/env bash
# Builds the compiler twice for every release target and fails when any
# binary differs (PLAN.md Phase 5: reproducible release builds).
set -euo pipefail
cd "$(dirname "$0")/.."
export CGO_ENABLED=0
tmp="$(mktemp -d)"
for target in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64; do
  os="${target%/*}"; arch="${target#*/}"
  GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags="-s -w -buildid=" -o "$tmp/a-$os-$arch" ./cmd/kern
  GOOS="$os" GOARCH="$arch" go build -a -trimpath -ldflags="-s -w -buildid=" -o "$tmp/b-$os-$arch" ./cmd/kern
  if cmp -s "$tmp/a-$os-$arch" "$tmp/b-$os-$arch"; then echo "$target: reproducible ($(shasum -a 256 "$tmp/a-$os-$arch" | cut -c1-16))"; else echo "$target: DIFFERS"; exit 1; fi
done
rm -rf "$tmp"
