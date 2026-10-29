# Proof of the common bounds guards

**Date:** 2026-10-29. **Spec:** `specs/prove-guards/SPEC.md` v0.3
(speccritic VALID_WITH_GAPS, no critical finding). **Plan:**
`specs/prove-guards/PLAN.md` (plancritic EXECUTABLE_WITH_CLARIFICATIONS,
no critical finding). **Source of the work:** the idiom table of
`docs/reports/agent-tasks-v3.md`, which listed fourteen guard forms the
pass rejected in correct programs.

## What changed

- **The pass is a must-analysis with joins** (`compiler/opt/prove.go`).
  Facts reach a join as the intersection of every incoming edge's facts
  and loops are solved to a fixed point: round one leaves back edges out
  (the optimistic start), later rounds include them with the previous
  round's state, and since facts only shrink the rounds end when no loop
  header's entry state changed, at the sound fixed point. Past eight
  rounds a pessimistic round starts every header from no facts. Proofs
  are assigned in a final marking pass; a loop-free function marks in its
  single pass. The state is layered: a block's state is a child of its
  predecessor's, so taking it is constant work, and a join costs the two
  paths' deltas since their common ancestor.
- **New fact kinds.** A value computed by a pure operation on the same
  operands as an earlier one is that value (value numbering, so a
  recomputed index expression matches its guard); an index minus a
  constant is read as a negative offset; a literal equal to an array's
  length and every `len` of that array are one length key; `len == K`
  gives `K - 1 < len`; `len(a) == len(b)` makes the two lengths one class;
  `x <= y` with `x` an offset of at least one gives `x - 1 < y`; element
  stores and calls with sequence or array accesses no longer forget
  lengths; and a slice is proven when its high bound is the length, a
  literal under an array length or an offset whose predecessor is a known
  index, and its low bound is literal zero, a smaller offset of the same
  base, a smaller literal, or a known index when the high bound is the
  length.
- **Conditions are branches** (`compiler/lower/stmts.go`): `&&`, `||` and
  `!` in an `if` or `while` condition branch directly instead of going
  through a boolean temporary, so each comparison's edge carries its
  fact; the value-position lowering is unchanged. The IR validator's
  ownership worklist, which capped a block at eight visits, now scales
  the cap with the block's predecessor count, since a join fed by the
  four tests of an `||` chain plus inlined callees legitimately needs
  more.

## Forms proven

`tests/run/prove_forms.kern` holds one function per form and every index
and slice check in it is proven; `tests/run/prove_forms_not.kern` holds
the excluded forms (lockstep `b[i]` under `a`'s guard alone, a wrapping
operation read as an offset, `!=`, a `Vec.len()` guard, a signed
comparison) and every check there is runtime-checked. Both print the same
under the interpreter and the native debug and release builds, as does
`tests/run/short_circuit.kern`, which pins the evaluation order and count
of `&&`, `||` and `!` operands with operands that print and operands that
would trap.

## Measured

Bounds checks on the examples, runtime-checked before → after:
`selfhost/lexer` 19 → 15, `btree` 4 → 4, `packet_parser` 3 → 3 (their
remaining checks are of other shapes: computed slices and library calls);
no example's count rose, and every example prints the same output.

The three agent suites: every reference of the third suite now measures
0 runtime-checked bounds checks (before: 0, 2, 6, 5, 6, 0, 5, 6, 6, 1 for
t01–t10), and so do the second suite's `t03_proven_checks` (2 → 0) and
`t10_tlv_decoder` (4 → 0); the starting programs fell too (t02 6 → 3, t04
10 → 4, t05 10 → 4, t07 11 → 2, t09 18 → 6). The budgets were lowered to
the references' counts, after which every suite's dry run passes with no
start meeting its budget. What the third suite lost: its calibration
programs (the ring buffer indexing `self.buf[self.slot(i)]` and the matrix
`at` guarding `r * n + c < len(m)`) are now proven as written, so the plain
arm's habitual guards meet the budgets, and the suite no longer separates
the arms on the bounds-check requirement alone. The agent harness's
description of `show_bounds_checks` was reworded to stay true (a change to
the structured arm's prompt, recorded here for the owner).

Soundness: `tests/prove_diff_test.go` gained eleven program forms (a
cached length, `<=` windows that read one past the window, `==`, `&&`,
lockstep with a shorter second vector, `||`, `i - 1`, a recomputed index,
two stores, an access after a call, guarded and unguarded slices, a
literal against an array length) with inputs on both sides of each guard;
400 generated programs agree between the interpreter, which executes
every check, and the native builds at both levels, which leave the proven
ones out.

Compile time (`BenchmarkProveLexer`, `BenchmarkProveSequentialGuards`,
`compiler/opt/prove_bench_test.go`, same host): the pass on
`examples/selfhost/lexer.kern` 0.25 ms → 1.7 ms (7x), and on a generated
function of 200 consecutive guarded accesses, each joining before the
next, 0.51 ms → 2.6 ms (5x). **This misses the plan's gate of twice the
baseline.** The first version measured 3.0 ms and 96 ms;
the layered state, convergence decided on the loop headers' whole states
alone, lazily made maps and the single marking pass for loop-free
functions brought it to these figures. What remains is the cost of a join-aware analysis over a
single-edge walk: more passes for loops and chain walks for lookups. At
under 2 ms on the largest file in the repository the cost is accepted
rather than paid down further; a persistent map would be the next lever.

## Reviews

speccritic took two rounds (the wrapping-recomputation contradiction, the
slice lower-bound predicate, literal equivalence); plancritic's one
blocking finding was the solver's definition, now in the plan's §0 in
full. prism's findings and the phase review are recorded in the commit.

## Phase review

- prism (gpt-6.1-sol): the findings in the commit message, all fixed; the
  pass itself reviewed alone in smaller chunks after the full-diff review
  twice timed out on its chunk, with no finding.
- realitycheck (gpt-6.1-sol, spec and plan against `compiler/opt`,
  `compiler/lower/stmts.go`, `compiler/ir/validate.go` and their tests):
  PARTIALLY_ALIGNED, score 100, no drift and no violation; every item
  IMPLEMENTED, PARTIAL or UNCLEAR, the last two for the tool's usual
  reason, an inventory of declarations without bodies.
- verifier (offline): risk score 12, twelve low name-matching findings
  (`Validate`, `Run`, `Inline`, `OptionsFor`, exercised by the fixture
  harness and the inliner tests under other names). Nothing acted on.
- clarion: not used by this project.
