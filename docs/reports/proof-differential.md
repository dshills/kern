# Differential test of proven checks

**Date:** 2026-10-28. **Spec:** §3.3 ("never confuse AI-produced claims
with compiler-established proofs") and §7 invariant 3 (a check marked
`proven` is skipped by the native backend and still executed by the
interpreter). **Starting point:** the proof pass (`compiler/opt/prove.go`)
and the inliner before it were tested by hand-written fixtures and by the
harness's differential run of every executable fixture; nothing searched
for a program on which a proof was wrong.

## What changed

`tests/prove_diff_test.go` (`TestProvenChecksDifferential`) generates
random programs over a `Vec[u64]` of zero to six elements and runs each
three ways: in the interpreter, which executes every check, and natively
under `--opt debug` and `--opt release`, where a proven check is gone.
Output, exit status and the trap record (kind and span) must agree, via
the harness's `nativeRunOpt`. Every statement kind is one the proof pass
reasons about or must be careful around:

- reads through helpers the release build inlines (`rd`, `pair`), and a
  helper with a loop it does not (`sum_to`, with a bound past the end
  about a third of the time);
- length tests on both edges (`if len(d) >= K`, `if len(d) < K ... else`)
  with the index at or past the tested length, a helper with the
  `len(p) < 3` early return (`head`), and `len(p) - 2` under
  `len(p) >= 2` (`tail`);
- a dominating check (`i := I; if i < len(d)`), a `for` over `len(d)`
  (loop bound), sub-slices (`d[lo : hi]`, with the slice itself in or
  out of range) read through a helper;
- a fixed `[4]u64` array read at literal indexes (constant length);
- between the reads, a `push` (the length changes) and a write through an
  `edit` access and a helper (`wr`), which must forget what was known.

Indexes are drawn near the vector's length, so roughly a third of the
reads trap; a program stops at its first trap, and the trap site is part
of the comparison. The default run is 40 programs in parallel (about two
seconds); `KERN_DIFF_PROGRAMS` and `KERN_DIFF_SEED` choose more or
others, and `KERN_DIFF_DUMP=1` logs each program. A failure prints the
program, so a wrong proof reproduces from its seed.

## Result

600 programs (seeds 1 to 600) agree in both opt levels: no proof was
wrong, and no trap moved. `show-bounds-checks --opt=release` on sampled
programs shows the proofs the test exercises (`dominated_by_check` and
`constant`, with `loop_bound` from the `for` statement) beside the
runtime checks that remain.

`go test ./...` and `golangci-lint run ./...` pass with the test in the
suite; the Prism review is recorded in the commit.
