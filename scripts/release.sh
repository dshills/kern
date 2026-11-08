#!/usr/bin/env bash
# Reproducible release builds of the kern compiler (PLAN.md Phase 5 packaging):
# Linux and macOS on arm64 and amd64, built with -trimpath and a cleared build
# id so that the same source and toolchain produce byte-identical binaries.
# Usage: scripts/release.sh [version]   (default: git describe)
# Output: dist/kern-<version>-<os>-<arch>.tar.gz and dist/SHA256SUMS. Each binary
# reports <version> from `kern version` (-X main.version, specs/release-v0.1).
set -euo pipefail
cd "$(dirname "$0")/.."
version="${1:-$(git describe --always --tags --dirty)}"
# deterministic archives need GNU tar (gtar from Homebrew on macOS)
TAR="$(command -v gtar || command -v tar)"
if ! "$TAR" --version 2>/dev/null | grep -q "GNU tar"; then echo "release.sh: GNU tar is required for reproducible archives (brew install gnu-tar)" >&2; exit 1; fi
rm -rf dist && mkdir -p dist
export CGO_ENABLED=0 SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct)}"
for target in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64; do
  os="${target%/*}"; arch="${target#*/}"
  out="dist/kern-${version}-${os}-${arch}"
  mkdir -p "$out"
  GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags="-s -w -buildid= -X main.version=${version}" -o "$out/kern" ./cmd/kern
  cp README.md LICENSE CHANGELOG.md "$out/"
  cp -R runtime/hosted/src "$out/runtime-hosted-src"
  cp -R runtime/freestanding/src "$out/runtime-freestanding-src"
  # gzip -n leaves the name and time out of the gzip header
  "$TAR" --sort=name --owner=0 --group=0 --numeric-owner --mtime="@${SOURCE_DATE_EPOCH}" -cf - -C dist "$(basename "$out")" | gzip -n -9 > "$out.tar.gz"
  rm -rf "$out"
done
(cd dist && shasum -a 256 -- *.tar.gz > SHA256SUMS)
echo "release $version:"; cat dist/SHA256SUMS
