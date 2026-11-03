# Non-strict guards and slice bounds: what was built and what it changes

**Date:** 2026-11-03. **Spec:** `specs/prove-nonstrict/SPEC.md` v0.2.
**Plan:** `specs/prove-nonstrict/PLAN.md`, three phases. Phases 1 and 2
are one commit (8c0238e): with phase 1 alone, the v0.2 slice hint
"`n <= len(h)` does not prove it" would have been false and the reasons
tests would have failed. Phase 3 is this report. **Baseline:** the
compiler at 3e0d653, the spec commit, which has no compiler change since
da73057.

## What was built

- **G1.** A non-strict guard `x <= y` gives `x - 1 < y` for every offset
  of x, so `n <= len(h)` proves `h[n - 1usize]` and not `h[n]`.
- **G2.** A slice high bound read as a value plus ho ≥ 0 is in range
  under a fact with k ≥ ho − 1, which proves `h[0usize:n]` under
  `n <= len(h)`. A negative ho never is (spec §0).
- **G3.** A low bound is not above the high one under a fact against the
  high bound or its base: `a[s:e]` under `s <= e`.
- **G4.** The literal high bound 0 is in range on any sequence.
- **Hints.** `slice_high_bound` names the guard to write, `slice_low_bound`
  names `L <= H`, and the hint for a high bound read as a value minus a
  literal keeps v0.2's slice-to-length rewrite. `ir.CheckBounds.HighNegative`
  records that reading. `specs/unproven-reasons` is at v0.3.

`len(xs) - 1usize` stays runtime-checked as an index and as a high
bound, the fourth agent suite's calibrated "last element" idiom (the
owner's choice).

## Soundness

- **R3.** The differential test passes with 40 and with 400 programs.
  Its generator gained eleven cases, and a fixed boundary test,
  `TestNonStrictBoundaries`, has 41 rows, one program each (45 since dc83970 added four for parenthesized conditions). Each rule
  loosened by one fails both tests:

  | mutation | differential: failing seeds of 400 (first) | boundary rows failing |
  |---|---|---|
  | G1 records `base + off < y` | 31 (seed 324) | 6 |
  | G2 admits k ≥ ho − 2 | 4 (seed 321) | 1 |
  | G3 admits k ≥ lo − 2 | 5 (seed 249) | 2 |
  | G3 admits k ≥ lo − ho − 2 | 12 (seed 358) | 4 |
  | G4 admits the literal 1 | 8 (seed 359) | 1 |

  Every failure is a native run that does not trap where the
  interpreter, which executes every check, traps.
- **An overflow found in review.** The first implementation computed
  `off - 1` unchecked. `index` keeps each literal step to 2^62, but two
  subtractions reach `math.MinInt64`. The bound then wrapped to
  `MaxInt64`, and
  `if x - 4611686018427387904usize - 4611686018427387904usize <= len(xs) { return xs[x] }`
  proved `xs[x]`. All offset arithmetic of the new rules now goes through
  a checked `sub`. `TestSub` and a `wrap` case of `TestProveNonStrict`,
  which fails on the unchecked code, cover it. The table above was
  measured after the fix.

## Which checks change (R6)

The listing covers 241 programs at both levels:
- `bench/kern/*/*.kern`, `tests/run`, `tests/trap` and `tests/positive`,
  one per file;
- the `examples/` projects, one per `kern.mod` directory;
- the references of the three agent suites.

It compares `show-bounds-checks` and every function's `check_bounds`
lines in `emit-ir` (library functions and release-inlined copies
included). 480 of the 482 program-levels build with both compilers. The
other two are `tests/run/scoped_nested.kern`, which neither compiler
lowers: it is the fixture of the scoped-spawn fix (026c30a), which came
later.

13 entries change, all from `runtime-checked` to `proven`, all in the
fixture functions added for this work in `tests/run/prove_forms.kern`.
No check of a benchmark, example, suite reference or library function
changes. No change goes the other way, no `proven_by` changes between
two proven labels, and no entry appears in one compiler's answer only.

| function | debug | release | rule |
|---|---|---|---|
| `ns_index` | `dominated_by_check` | (proven at both by the inlined literals) | G1 |
| `ns_index_gt` | `dominated_by_check` | `dominated_by_check` | G1, else edge of `>` |
| `ns_slice` | `dominated_by_check` | `dominated_by_check` | G2 |
| `ns_low` | `dominated_by_check` | `dominated_by_check` | G2 and G3, H's own value |
| `ns_low_plus` | `dominated_by_check` | `dominated_by_check` | G3, H's base |
| `ns_low_len` | `dominated_by_check` | `dominated_by_check` | G3 with H the length |
| `ns_zero` | `constant` | `constant` | G4 |

The plan asked for one scratch driver per `std` module, calling every
public function. That was not done: `std/` has 413 functions, and a std
file cannot be checked as the main module, since its bodyless
declarations are library-only. The listing covers the library functions
that the 241 programs compile. A function that no program instantiates
compiles into no binary, so it has no label to change.

`scripts/capture-ll.sh` gives the LLVM IR of 316 builds per compiler.
The same 50 do not build with either: the 48 of earlier reports, plus
`scoped_nested` at both levels. Two differ, `tests/run/prove_forms` at
debug and at release, which hold every listed change. Every other build
is byte-identical.

## Agent suites (R7) and cost (R8)

- The dry runs of `bench/agent/tasks`, `tasks-budgeted` and `tasks-v3`
  exit 0 with both compilers, with the same natural-solution lines. Every
  natural solution's runtime-checked sites, as (file, span) at
  `--opt=debug`, are identical: 166 in the two suites of the fourth
  design. No calibration file was edited.
- `BenchmarkProve`, five interleaved rounds on a quiet host, base then
  new in each round, medians in ns/op:
  - `BenchmarkProveLexer`: 400,120 → 410,802 (+2.7%);
  - `BenchmarkProveSequentialGuards`: 762,505 → 779,075 (+2.2%).

  The limit is 10%. A run taken earlier, while the host was busy, was
  discarded.

## Classification (R5)

`TestReasonsEach` has an access and a proven rewrite for every hint
form: the covering boundary of C1 (`n > len(xs)` covers `xs[n - 1usize]`,
not `xs[n]`), the three C2 forms, both `HighNegative` hints, and both C3
forms. `TestReasonsHighNegativeAggregated` builds an entry whose two
release-inlined copies differ in `HighNegative`, and checks the premise
before the hint. `TestReasonsSuiteSweep` still sees 592 unproven entries
in 116 suite programs. All of them are located, and 0 slice entries have
a place that is not a name or field path.

## Evaluation (§4)

These are the final programs of the paid run of the budgeted suite
(`bench/agent/runs/20261102-134710`, git-ignored): the 22 of 100 whose
`check` reports only `BUDGET_EXCEEDED`, at `--opt=debug`.

- **Locations.** They have 82 runtime-checked locations with the
  baseline: no_guard 42, computed_index 15, slice_high_bound 13,
  type_range 5, remainder_by_value 4, other_length 3. Two become proven,
  both `slice_high_bound`, both in probe functions the agent wrote to see
  what the checker proves:
  - `h[0usize:n]` after `if n > len(h) { return 0u64 }` (t07, G2 through
    G1's else edge);
  - `g[0usize:0usize]` (t04, G4).
- **Budgets.** None of the 22 programs then meets its budget: their
  remaining locations are unguarded or computed indexes, which this
  change does not touch.
- **Hand checks.** The new hint forms the run's remaining locations
  receive were each rewritten as the hint says, in a scratch copy, and
  the rewritten slice read back:

  | form | program, access | rewrite | result |
  |---|---|---|---|
  | C2, simple H, L 0 | t04 3450304529 `u[0usize:w]` | `if w <= len(u) { … }` | proven |
  | C2, bounds in locals | t04 350340597 `a[y * n:(y + 1usize) * n]` | `lo := y * n; hi := (y + 1usize) * n; if lo <= hi && hi <= len(a)` | proven |
  | C2, read as a value minus a literal | t07 2005261572 `h[0usize:n - 1usize]` | `h[0usize:len(h)]` | proven |
  | C3, bounds in locals | t04 95975594 `g[i:i + w]` | `lo := i; hi := i + w; if lo <= hi` | proven |

  Two forms no run location receives: C2's `L <= H && H <= len(X)` and
  C3's `L <= H`. Fixtures `f8b` and `f9b` of
  `compiler/analysis/reasons_test.go` cover them.

## What this shows, and what it does not

- The rules prove the three forms of the spec, and no calibrated site of
  the agent suites changes: the suites measure what they measured.
- The direct effect on the paid run's failures is small: 2 of 82
  locations. Its larger effect is the hints, which now name a guard for
  a slice instead of the rewrite that gave up the trap. Whether agents
  follow them is a question for a paid run, the owner's decision.
- Found on the way, and fixed in their own commits: the scoped-spawn
  lowering error (026c30a), and parenthesized conditions that gave no
  guard facts (dc83970, `docs/reports/paren-conditions.md`).
