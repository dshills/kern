# Proof pass: remainders, bounded locals and masks by a value

**Date:** 2026-10-31. **Spec:** `specs/prove-bounds/SPEC.md`.
**Follows:** `docs/reports/prove-masks.md`, whose "out of scope" list
this takes up in part.

## What changed

The masked-index rule of `compiler/opt/prove.go` now reads a general
upper bound of an unsigned value:
- a literal;
- a bitwise and with a bounded operand;
- a remainder by a literal `d >= 1`, which is at most `d - 1`;
- a conversion to an unsigned type of a bounded value;
- a load of a local whose every store, on every path to it, was bounded.

The last form is what carries a bound around a loop and across an
`if`/`else`. The bound of a place is kept in the pass's map of what
places hold, under a key of its own, as a synthetic value naming the
constant. So everything that forgets a place's value (a call, an edit
access, a move, a drop) forgets its bound too. At a join the larger
bound is kept. A bound names a constant, not a value, so it settles in
the loop solver: the lattice is the function's constants.

One detail of the IR mattered. Every store to a local is followed by an
`init` marker, which the pass treated as a write and so forgot the
store's value. Keeping the value across `init` was tried and reverted.
A value first remembered at a loop header in the optimistic first round
became, in later rounds, a fresh name that never matched the previous
round's. That lost an existing proof in a nested loop (`TestProveSolver`,
`t.nested`). Only the bound now survives the `init` that marks its own
store.

A second rule proves a mask by a value below the length: `(x & m) + d`
when a fact puts `m + d` below the length, or when `m` is the length
less one (computed with a checked subtraction, so the length is at least
one). These are `dominated_by_check`.

Kept checked by design:
- **A remainder by a value** (`i % len(xs)`). It is the "modulo" idiom
  calibrated into the fourth agent suite, as byte ranges are.
- **A bound carried through a field another method wrote**
  (`ring_buffer`'s `self.tail`).
- **A loop whose first value is unbounded**
  (`page_frame_allocator`'s `w := self.hint`).

## Effect on the repository's programs

This compares the compiler at 6a68124 with this change, both on the
current sources.

| programs | opt | runtime-checked bounds checks |
|---|---|---|
| `bench/kern/`, `examples/`, `tests/run/` | debug | 109 → 102 |
| the same | release | 83 → 73 |

- **Benchmarks.** One site changes in each of two programs:
  `page_frame_allocator` and `page_frame_allocator_fail` read
  `held[hh]`, where `hh` starts at 0 and is only ever stored
  `(hh + 1usize) & 4095usize`. It is now `constant` at both opt levels.
- **Fixtures.** The other changes are the new fixture functions.
- **Release only.** Four inlined copies of `prove_forms_not` functions
  are also proven in `main`, where their arguments are literals. One
  example is `mask_len(mr, 7usize)`, whose `7 & 8` is at most 7. The
  functions' own bodies stay runtime-checked, as the fixture test pins.

No check lost its proof.

## Verification

- **Unit tests** (`TestProveBounds`).
  - Proven: remainders (also with an offset, under a mask and through a
    conversion); a bounded loop, including through a copy; a join of two
    bounded paths; a mask by the length less one; a guarded mask.
  - Not proven: `x % 9` into `[8]`; `i % len(xs)`; a loop mask of 15
    into `[8]`; a loop whose first value is a parameter; a join with an
    unbounded path; a bound forgotten by an edit; `x & len(xs)`;
    `(x & (len - 1)) + 1`; a `u8` into `[256]`.
- **Fixtures.** Each runs in both engines and both opt levels.
  - `prove_forms` gains five functions, all proven.
  - `prove_forms_not` gains five, all runtime-checked.
  - `TestProveBoundsFixtures` pins their labels.
- **Differential test of proven checks.**
  - The generator gains remainders (`x % 9` in a loop that reaches 8),
    bounded and wide loops, a three-way join, and masks by the length,
    by the length less one, and by a guarded value with and without an
    offset.
  - It passes at its default count and with 400 programs.
- **Prism** found that the bound search expanded shared operands as a
  tree (`x & x` chains: exponential). It is memoized per search;
  `TestProveBoundsShared` runs a 60-step chain, which hangs without
  the memo.
  - Each of four widened rules fails it: remainder bound `d - 2` (22
    failing lines), the meet keeping the smaller bound (64), the length
    rule admitting `mo + d = 0` (40), and the guard rule admitting
    `k + 1` (11).
- **Agent suites.** The dry runs of the fourth and third suites exit 0.
  The fourth suite's natural-solution counts are unchanged (9, 6, 7, 14,
  8, 7, 9, 9, 8, 6).
- **Cost.** `BenchmarkProve` was run five rounds, before and after
  alternating, with load near 25:

  | benchmark | before (median) | after (median) | ratio |
  |---|---|---|---|
  | lexer | 1.734 ms | 1.798 ms | 1.04 |
  | sequential guards | 2.472 ms | 2.558 ms | 1.03 |
