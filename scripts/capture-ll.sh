#!/bin/zsh
# capture-ll.sh <kern-binary> <out-dir>: emit the LLVM IR of every single-file
# program (bench/kern, tests/run, tests/trap, tests/positive) in both
# configurations for the host target, so two compilers can be compared
# byte for byte (specs/cross-file-inlining/SPEC.md R11). Programs that do
# not build (another target, a missing C counterpart) go to failed.txt.
# Both arguments are resolved against the invocation directory before the
# script moves to the repository root; a bare command name stays a PATH lookup.
set -u
K=$1; OUT=$2
case $K in */*) K=${K:A};; esac
mkdir -p "$OUT" || exit 1
OUT=${OUT:A}
cd "$(dirname "$0")/.." || exit 1
rm -f "$OUT"/*.ll(N) "$OUT/tmpbin"  # a reused directory holds nothing from an earlier run (N: no error when empty)
: > "$OUT/failed.txt"
for f in bench/kern/*/*.kern tests/run/*.kern tests/trap/*.kern tests/positive/*.kern; do
  for opt in debug release; do
    name=$(echo $f | tr '/' '_' | sed 's/\.kern$//')
    if ! "$K" build --opt=$opt --emit-llvm="$OUT/$name.$opt.ll" -o "$OUT/tmpbin" $f > /dev/null 2>&1; then
      echo "$f $opt" >> "$OUT/failed.txt"
      rm -f "$OUT/$name.$opt.ll"
    fi
  done
done
rm -f "$OUT/tmpbin"
ls "$OUT"/*.ll | wc -l; wc -l < "$OUT/failed.txt"
