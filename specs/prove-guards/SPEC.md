# Proof of the common bounds guards

**Version:** 0.3 (2026-10-29). **Scope:** the bounds-check proof pass
(`compiler/opt/prove.go`), the lowering of conditions, the proof
differential test, the agent harness's description of the pass, and the
agent task suites that depend on its counts. **Language impact:** none:
spec §7 invariant 3 keeps its three proof facts (`loop_bound`,
`dominated_by_check`, `constant`), and a program's observable behaviour is
the same with a check proven or executed (the interpreter executes every
check; a proven check that would have failed is a compiler bug, spec §3.3).
**Related:** `docs/reports/agent-tasks-v3.md` (the table of idioms the
pass rejects, the source of this document), `docs/reports/inliner.md`
(PLAN.md 6.4, what the pass gained from inlining),
`tests/prove_diff_test.go` (the soundness test).

**Review status.** speccritic (OpenAI gpt-6.1-sol, profile general, with
`compiler/opt/prove.go` as context): VALID_WITH_GAPS, no critical
finding, after two rounds (the wrapping-recomputation contradiction, the
slice lower-bound predicate, literal equivalence and C1's scope were
answered); the remaining warnings are the preflight's flags on the
upper-case labels (F1, A1, CFG, IR, API) and two wordings it reads as
non-binding, accepted.

**Glossary.** Check: a `check_bounds` instruction of the semantic IR, for
an index (`xs[i]`) or a slice (`xs[lo:hi]`). Fact: what the pass knows at
a program point. Proven: a check the pass marks with a proof fact, which
the native backend then leaves out. Guard: a comparison in an `if` or
`while` condition. Edge fact: what a branch establishes on one of its
edges. Join: a block with several predecessors. Length value: the value
of a `len` instruction, or a literal naming an array's length. Base and
offset: the pass reads an index as a value plus a constant, `i + 2` being
base `i` with offset 2. CFG: control-flow graph. IR: intermediate
representation. API: application programming interface.

## 1. Problem

The pass proves an index in range only when the guard is a strict `<` or
`>=` comparison of the same index value, or that value plus a constant,
against the same length value, on a path with no join between the guard
and the check. Ordinary programs guard their indexes in ways the pass
reads as unrelated: a cached length in a loop head, `<=` and `==`, `&&`
and `||`, a recomputed index expression, an index minus one, a range
loop, a literal equal to an array's length, a second store under one
guard, an access after a call, and any `if` between the guard and the
access. The third agent suite found that every such program is correct,
checks clean and still carries every check at runtime. Each form below
must prove the class of checks it names in correct programs.

## 2. Definitions

- **Unsigned index.** Every rule concerns `usize`-typed (or other
  unsigned) indexes and lengths; nothing is proven about signed values.
- **Same value.** Two IR values are the same when one is a load of a
  place that holds the other (the existing flow fact), when they are the
  same `len` of a place whose length cannot have changed (the existing
  rule), or when F6 makes them equal.
- **Fact kinds.** `lt`: base + k < length value, for the largest known k
  (k is negative under F7: the fact base - c < length for c ≥ 0 holds
  whenever base < length holds, since base - c ≤ base). `locals`: the
  value a place holds. `lens`: the value `len` of a place yielded. `same
  length`: two length values known equal (F4). `equiv`: the F6
  equalities (value numbering) and the load/length equalities.
- **Intersection.** The facts common to two states: an `lt` fact with the
  smaller k, a `locals`, `lens`, `same length` or `equiv` entry with the
  same value on both sides.

## 3. Non-goals

- No proof involving a container's method (`Vec.len()` against
  `v.as_view()[i]`): a library call's result is not a length value.
- No lockstep proof without a length equality (`b[i]` under
  `i < len(a)` alone stays runtime-checked).
- No reading of a wrapping operation as a base plus an offset: a guard
  `wrapping_add(i, 1usize) < len(xs)` establishes nothing about `xs[i]`,
  and `xs[wrapping_add(i, 1usize)]` under `i + 1usize < len(xs)` stays
  runtime-checked. (F6's equality of recomputation does apply to a
  wrapping operation: `xs[wrapping_add(i, 1usize)]` under
  `wrapping_add(i, 1usize) < len(xs)` is proven, the two being the same
  value.) No facts from signed comparisons, from `!=`, or from
  arithmetic on two non-constant operands other than that equality.
- No change to overflow checks, which are never proven (spec §4.1).
- No change to the inliner or to when the pass runs.
- No new proof label: every proof is `loop_bound`, `dominated_by_check` or
  `constant` as spec §7 invariant 3 lists them.

## 4. Requirements

### 4.1 Facts across joins

F1. Facts reach a join as the intersection of the facts on every incoming
edge (each predecessor's state plus that edge's edge facts). For a loop,
the head's facts are computed to a fixed point: starting from the
intersection over the forward edges, the body is analysed, and the
head's facts are intersected with the back edge's facts until they no
longer change (facts only shrink, so this terminates; an implementation
bounds the iterations and, past the bound, falls back to no facts at that
head, which is sound). Proof labels are assigned only once the facts
have stabilised. Consequence: a guard before a loop, a cached length
(`n := len(xs)` then `while i < n { xs[i] }`), an `if` between a guard
and an access (`while i < len(t) { c := …; if … {}; t[i] = c }`) and a
range loop (`for i in 0usize..len(xs) { xs[i] }`, whose bound is one
value tested in the head) are proven.

### 4.2 Conditions as control flow

F2. A condition of an `if` or `while` whose operator is `&&`, `||` or `!`
is lowered as branches, not as a temporary: `a && b` branches on `a` to
the test of `b` or to the else target; `a || b` branches on `a` to the
then target or to the test of `b`; `!a` swaps the targets. The lowering
of `&&` and `||` in value position (`x := a && b`) is unchanged. In
consequence the then edge of `i < len(a) && j < len(b)` carries both
facts, the else edge of `i >= len(xs) || …` carries `i < len(xs)`, and
the second operand of `||` is evaluated under the negation of the first,
so `if i >= len(xs) || xs[i] == 0u8` proves `xs[i]`. A program's
observable behaviour is unchanged: the operands are evaluated in the
same order and at most the same number of times.

### 4.3 Comparison forms

F3. `x <= y` on its then edge, `x > y` on its else edge, `y >= x` on its
then edge and `y < x` on its else edge, with x read as base + off and
off ≥ 1, establish base + (off - 1) < y. (`i + 2 <= len(xs)` proves
`xs[i]` and `xs[i + 1]`, not `xs[i + 2]`.)

F4. `len == K` on its then edge (either operand order), with K a literal
≥ 1, establishes K - 1 < len, as `len >= K` already does. `l1 == l2` on
its then edge, with l1 and l2 two length values, establishes that they
are the same length: an `lt` fact against either covers a check against
the other, and a `constant` proof against an array's length covers the
other too. The fact is flow-sensitive: it holds on paths from the edge
and intersects at joins like every other fact.

F5. A literal equal to the length of a place of array type names that
length: a guard `idx < 8usize` covers `self.buf[idx]` for `buf: [8]u32`,
and a `len` of an array place is the same length value wherever it is
taken, whatever stores or calls lie between (an array's length is its
type's). Two `len` instructions of array places of the same length are
the same length value.

### 4.4 Index forms

F6. Two values computed by the same pure operation on the same operands
are the same value: the binary operators `+ - * / % << >> & | ^` in any
mode (checked, wrapping; a checked operation that did not trap and a
wrapping one are each a function of their operands), the checked and
truncating conversions, loads under the existing rule, and two unsigned literals of the same
type and value. So an index
expression recomputed at the access (`xs[i * 6usize + 5usize]` guarded
by `i * 6usize + 5usize < len(xs)`, `buf[(head + i) % len(buf)]` guarded
the same way, `tab[usize(b)]` guarded by `usize(b) < len(tab)`) is the
guarded value.

F7. A checked unsigned subtraction of a constant, `x - c`, is read as base
x with offset -c; a fact base + k < length with k ≥ -c covers it, so
`xs[i - 1usize]` is proven wherever `xs[i]` would be. (The subtraction
trapped before the index existed if it would have wrapped, so the index
is at most `x`.)

### 4.5 Stores and calls

F8. A store to an element of a sequence, a call that receives a sequence
access (`view[]T` or `edit[]T`), and a call that receives a scalar access to a place of array type must not forget the length of that
sequence or array: its storage is fixed and the callee cannot change its
length. The facts about what index variables hold are forgotten as today where
a write to the variable is possible. Consequence: `if i + 1usize < len(dst) {
dst[i] = a; dst[i + 1usize] = b }` proves both stores, and `if i <
len(xs) { f(xs, 0usize); xs[i] }` proves the read.

### 4.6 Slices

F9. A slice `xs[lo:hi]` is proven when both hold: (a) hi is in range: hi
is the length value of `xs` (`xs[i:len(xs)]`, or the omitted bound), or a
literal not above the array's length, or read as base + off with off ≥ 1
and a fact base + k < length with k ≥ off - 1 (so `i + 2usize <=
len(xs)` covers `xs[i:i + 2usize]`); and (b) lo is not above hi, by one of: lo is
the literal 0 or omitted; lo and hi read as the same base with the
offset of lo ≤ the offset of hi; both are literals with lo ≤ hi; or hi
is the length value of `xs` and lo, read as base + off, has a fact base
+ k < that length with k ≥ off (lo < length, so lo ≤ hi). The proof fact is `constant` when both
bounds are literals under an array's length, else `dominated_by_check`.

### 4.7 Soundness and reporting

F10. The interpreter executes every check as today, so the differential
harness compares a run with every check against a native run without the
proven ones; the proof differential generator (`tests/prove_diff_test.go`)
gains program forms for F1–F9 (a cached length in a loop, `&&` and `||`
guards, `<=` and `==` guards, a recomputed index, an index minus one, a
range loop, a literal against an array length, two stores under one
guard, an access after a call, and guarded slices), each with inputs that
make the guard pass and fail, so that a wrong proof shows as a native read
past the end or a trap at a different site.

F11. `show-bounds-checks` reports every proof with one of the three
labels of spec §7 invariant 3 and never a new one; the IR validator's
rule that a proven check names its fact is unchanged.

F12. The agent harness's description of `show_bounds_checks`
(`bench/agent/harness/trial.go`) names what proves a check in one
sentence that is true after this document: a constant index under a
length the type gives, a dominating comparison against the same length
through joins and recomputation, or a `for` loop's bound. The change to
the structured arm's prompt is recorded for the owner, since the arm's
wording is part of the experiment.

F13. The three agent task suites are re-validated with the harness's dry
run; a task whose starting program now meets its budget is reported, and
a reference whose count fell has its `max_runtime_bounds_checks` lowered
to the new count so that the task keeps asking for the pass's best. No
model is called.

## 5. Implementation constraints

C1. The pass must remain sound under F1's iteration: a proof label is
assigned only from facts that hold on every path to the check, and every
relation that depends on the path (the equalities of loads and lengths,
F4's same-length facts, F6's equalities) must be intersected at joins
with the other facts. (Design guidance, not a requirement: a forward
dataflow in reverse postorder whose per-block state holds every
path-dependent relation, with the immutability of an access referent's
length and of an array's length kept as global facts.)

C2. The pass runs where it runs today (after inlining in release, once in
debug) and does not change the IR other than check provenance; F2 is a
change to lowering, visible in `emit-ir` as the absence of the `sc.rhs`
temporary for conditions.

C3. No new dependency; the interpreter and both backends are unchanged.

## 6. Acceptance criteria

A1. A fixture `tests/run/prove_forms.kern` holds one function per form
F1–F9 (the idioms of `docs/reports/agent-tasks-v3.md`'s table, verbatim
where the table gives them), and `kern show-bounds-checks` on it labels
every index and slice check in those functions proven, with the three
labels only; a companion `tests/run/prove_forms_not.kern` holds the forms
§3 excludes (lockstep without equality, `!=`, a wrapping guard
read as an offset (`xs[wrapping_add(i, 1usize)]` under `i + 1usize <
len(xs)`), a `Vec.len()` guard, a signed index) and every check there
stays runtime-checked. Both fixtures print the same under the interpreter and
the native debug and release builds.

A2. On the probe programs of the third-suite report, the calibration
programs score: the ring buffer that indexes `self.buf[self.slot(i)]` is
proven where `slot` is inlined (release) and the recomputation is
visible, and the matrix `at` that guards `r * n + c < len(m)` and indexes
`m[r * n + c]` is proven in both optimisation levels.

A3. `tests/prove_diff_test.go` passes with its default forty programs
and with `KERN_DIFF_PROGRAMS=400`, after F10's generator extension.

A4. `go test ./...` passes; every example under `examples/` prints the
same output; the counts of runtime-checked bounds checks on the examples
do not rise for any file, and the report records the before/after
counts (`btree` 4, `packet_parser` 3, `selfhost/lexer` 19 before).

A5. The three suites' dry runs pass after F13's re-calibration, and the
report lists each task of the third suite with its start and reference
counts before and after.

A6. `emit-ir` of `func f(a: view[]u8, b: view[]u8, i: usize) -> bool {
if i < len(a) && i < len(b) { return a[i] == b[i] } return false }` holds
no block named `sc.rhs` and two proven checks.
