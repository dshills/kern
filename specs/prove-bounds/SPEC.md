# Proof pass: remainders, bounded locals and masks by a value

**Version:** 0.1 (2026-10-31). **Scope:** the bounds-check proof pass
(`compiler/opt/prove.go`) and its tests; fixtures under `tests/run/`; the
differential test of proven checks (`tests/prove_diff_test.go`); the
schema note on `proven_by`; the report `docs/reports/prove-bounds.md`.
**Language impact:** none. The §16 `proven_by` values are unchanged.
**Follows:** `specs/prove-masks/SPEC.md`, whose non-goals this takes up
in part.

## 0. Goals and non-goals

Goals: prove three more forms of index that cannot reach the length.
- A remainder by a literal: `a[x % 16usize]` into a `[16]`.
- A local whose every store on every path to a read was bounded by a
  constant, across branches and loops: `w = (w + 5usize) & 15usize`
  carried around a loop that starts it at 0.
- A mask by a value below the length: `xs[i & (len(xs) - 1usize)]`, or
  `xs[i & m]` under `m < len(xs)`.

Non-goals:
- a remainder by a value (`i % len(xs)`): a calibrated idiom of the
  fourth agent suite (`bench/agent/tasks`, idiom "modulo"), kept checked
  as the byte ranges are;
- a value's range from its type (`u8` into `[256]`), for the same reason;
- a bound carried across calls or through fields another function wrote
  (`ring_buffer`'s `self.tail`), and a loop whose first value is
  unbounded (`page_frame_allocator`'s `w := self.hint`).

## 1. The facts

**Upper bound.** An unsigned value has an upper bound `c` when it is:
a literal `c`; a bitwise and of which either operand has a bound (the
smaller); a remainder by a literal `d >= 1` (`d - 1`, or the dividend's
bound when smaller); a conversion to an unsigned type of a bounded
unsigned value; or a load of a local place whose every store, on every
path to the load, stored a bounded value (the largest of their bounds).
Whatever may write the place without a store (an edit access, a call, a
move, a drop) forgets its bound, as it forgets its value today.

An index check whose index is a bounded value plus a constant offset is
proven exactly as a masked index is (`specs/prove-masks` §1): `constant`
below a known length, `dominated_by_check` under a guarded lower bound,
`constant` when both hold.

**Mask by a value.** An index `(x & m) + d` with `m = mb + mo` read as
the pass reads offsets is below the length `n` when a fact says
`mb + k < n` with `k >= mo + d`, or when `mb` is `n` (or one with it) and
`mo + d < 0` (`m = n - 1` was computed with a checked subtraction, so
`n >= 1`). Such a check is `dominated_by_check`.

## 2. Requirements

**R1 — Proofs.** Every check §1 matches is proven with its label; every
other check keeps the label it had.

**R2 — Soundness.** No check is proven whose index can reach the length.

**R3 — Both engines agree.** The differential test of proven checks
gains remainders below and at the length, bounded loops below and past
it, a join with an unbounded path, and masks by the length less one,
by a guarded value, by a value with an offset, and by the length itself.
It passes with its default count and with 400 programs, and fails when
any of these rules is widened by one (remainder bound, the meet taking
the smaller bound, the length rule admitting `mo + d = 0`, the guard
rule admitting `k + 1`).

**R4 — Fixtures.** `tests/run/prove_forms.kern` gains one function per
proven form; `prove_forms_not.kern` gains a remainder that reaches the
length, a remainder by the length, a loop with an unbounded first value,
a join with an unbounded path and a mask by the length. A unit test pins
their labels.

**R5 — Which checks change.** The report lists every bounds check of the
programs under `bench/kern/`, `examples/` and `tests/run/` whose label
changes, in debug and release.

**R6 — Agent suites.** The dry runs of the third and fourth suites exit
0, with the fourth suite's natural-solution counts unchanged.

**R7 — Cost.** `BenchmarkProve`, before and after interleaved over 5
rounds, adds at most 10% to each median.
