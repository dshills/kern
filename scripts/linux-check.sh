#!/usr/bin/env bash
# Runs the std/process tests on Linux before a commit (specs/std-process
# PLAN.md, "Linux gate"): in an ubuntu:24.04 container (glibc 2.39, the
# distribution's clang and lld) through the host's Docker, on the host's
# architecture, with the repository mounted read-only.
#
#   scripts/linux-check.sh TestName...
#
# Each argument names a test the current phase owns, as `go test -json`
# reports it (TestProcessSpawnOrder, TestFixtures/run/std_process_env.kern).
# The fixtures run are tests/run/std_process* and tests/ffi/process*.
# The gate fails when a `go test` command fails, when any test fails, or
# when a named test has no pass event (it was skipped or never ran).
set -euo pipefail
cd "$(dirname "$0")/.."
if [ "$#" -eq 0 ]; then
  echo "usage: $0 TestName..." >&2
  exit 2
fi

image=kern-linux-check:go1.25
docker build -q -t "$image" - >/dev/null <<'EOF'
FROM golang:1.25 AS go
FROM ubuntu:24.04
RUN apt-get update && apt-get install -y --no-install-recommends clang lld jq ca-certificates git && rm -rf /var/lib/apt/lists/*
COPY --from=go /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:$PATH
EOF

docker run --rm \
  -v "$PWD:/src:ro" \
  -v kern-linux-check-cache:/cache \
  -e GOCACHE=/cache/build -e GOMODCACHE=/cache/mod -e HOME=/tmp/home \
  -e GOFLAGS=-buildvcs=false -e GOTOOLCHAIN=local \
  -w /src "$image" bash -c '
set -uo pipefail
mkdir -p "$HOME"
echo "glibc $(ldd --version | head -1 | awk "{print \$NF}"), $(clang --version | head -1), $(go version)"
status=0
run() {
  go test -json -count=1 "$@" >>/tmp/events.json # never a cached result: the container toolchain must run
  local code=$?
  [ "$code" -eq 0 ] || { echo "go test $* exited $code"; status=1; }
}
run ./tests -run "^TestFixtures\$/run/std_process"
run ./tests -run "^TestFixtures\$/ffi/process"
run ./tests -run "^TestProcess"
run ./compiler/interp -run "^TestProc"
failed=$(jq -r "select(.Action == \"fail\" and .Test != null) | .Test" /tmp/events.json | sort -u)
if [ -n "$failed" ]; then
  echo "failed:"; echo "$failed" | sed "s/^/  /"
  jq -r "select(.Action == \"output\" and .Test != null) | .Output" /tmp/events.json | grep -v "^=== \\(RUN\\|PAUSE\\|CONT\\)" | tail -60
  status=1
fi
for name in "$@"; do
  if ! jq -e -s --arg n "$name" "any(.[]; .Action == \"pass\" and .Test == \$n)" /tmp/events.json >/dev/null; then
    echo "no pass event for $name"; status=1
  fi
done
passed=$(jq -r "select(.Action == \"pass\" and .Test != null) | .Test" /tmp/events.json | sort -u | wc -l)
echo "linux gate: $passed tests passed, status $status"
exit $status
' linux-check "$@"
