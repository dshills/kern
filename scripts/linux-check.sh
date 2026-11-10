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
# The fixtures run are tests/run/std_process*, tests/run/std_timeouts*
# (specs/std-timeouts) and tests/ffi/process*, with the cmd/kern
# subprocess tests of the timeouts.
# The gate fails when a `go test` command fails, when any test fails, or
# when a named test has no pass event (it was skipped or never ran).
#
#   scripts/linux-check.sh --run "<go test arguments>" [--run "..."]... [TestName...]
#
# With --run, each group replaces the std/process runs above: `go test` is
# run once per group with those arguments (specs/runtime gate 5), e.g.
#   scripts/linux-check.sh --run "./compiler/analysis -run ^TestRuntime" TestRuntimeABI
# KERN_SANITIZE, when set, is passed into the container.
set -euo pipefail
cd "$(dirname "$0")/.."
if [ "$#" -eq 0 ]; then
  echo "usage: $0 [--run \"go test arguments\"]... TestName..." >&2
  exit 2
fi
runs=()
while [ "${1:-}" = "--run" ]; do
  [ "$#" -ge 2 ] || { echo "$0: --run needs an argument" >&2; exit 2; }
  runs+=("$2")
  shift 2
done
# the groups cross into the container as one newline-separated variable
groups=""
for r in ${runs[@]+"${runs[@]}"}; do groups+="$r"$'\n'; done

image=kern-linux-check:go1.25
docker build -q -t "$image" - >/dev/null <<'EOF'
FROM golang:1.25 AS go
FROM ubuntu:24.04
RUN apt-get update && apt-get install -y --no-install-recommends clang lld libclang-rt-18-dev llvm jq ca-certificates git qemu-system-arm qemu-system-x86 && rm -rf /var/lib/apt/lists/*
COPY --from=go /usr/local/go /usr/local/go
ENV PATH=/usr/local/go/bin:$PATH
EOF

# A network with IPv6, so that the container's interface has a link-local
# address and the tests of scoped addresses run (specs/runtime RM2).
network=kern-linux-check-v6
docker network inspect "$network" >/dev/null 2>&1 || docker network create --ipv6 "$network" >/dev/null
docker run --rm \
  --network "$network" \
  -v "$PWD:/src:ro" \
  -v kern-linux-check-cache:/cache \
  -e GOCACHE=/cache/build -e GOMODCACHE=/cache/mod -e HOME=/tmp/home \
  -e GOFLAGS=-buildvcs=false -e GOTOOLCHAIN=local \
  -e KERN_SANITIZE="${KERN_SANITIZE:-}" -e LINUX_CHECK_RUNS="$groups" \
  -e KERN_REQUIRE_QEMU=1 \
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
if [ -n "$LINUX_CHECK_RUNS" ]; then
  while IFS= read -r group; do
    [ -n "$group" ] || continue
    eval "run $group"
  done <<<"$LINUX_CHECK_RUNS"
else
  run ./tests -run "^TestFixtures\$/run/std_process"
  run ./tests -run "^TestFixtures\$/run/std_timeouts"
  run ./cmd/kern -run "^Test(ReadStdinTimeout|OutputTimeoutReaps)\$"
  run ./tests -run "^TestFixtures\$/ffi/process"
  run ./tests -run "^TestProcess"
  run ./compiler/interp -run "^TestProc"
fi
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
