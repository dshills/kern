# Proof pass: non-strict guards and slice bounds

**Version:** 0.1 (2026-11-03). **Scope:** the bounds-check proof pass
(`compiler/opt/prove.go`) and the field it records on `ir.CheckBounds`;
the reason classification (`compiler/analysis/reasons.go`) where its
covering rule and slice hints follow the proof rules; fixtures under
`tests/run/`; the differential test of proven checks
(`tests/prove_diff_test.go`); `docs/bounds-checks.md`; the amendment of
`specs/unproven-reasons/SPEC.md` to v0.3; the report
`docs/reports/prove-nonstrict.md`.
**Language impact:** none. The §16 `proven_by` values, the wire schema
(`schema_version` 0.5) and the reason codes are unchanged; some
checks change from `runtime-checked` to `proven`, and two hints' text
changes.
**Follows:** `specs/unproven-reasons/SPEC.md` §6, whose list of proof
gaps this takes up in part.

## 0. Goals and non-goals

Goals: prove three forms the rules leave checked although the check
provably passes (`specs/unproven-reasons` §6, items 2 to 4):
- an index or a slice high bound under a non-strict guard with no
  positive offset: `h[n - 1usize]` and `h[0usize:n]` under
  `n <= len(h)`;
- a slice's low bound under a guard against the high bound's variable:
  `a[s:e]` under `s <= e && e <= len(a)`;
- the literal high bound `0` on a sequence: `h[0usize:0usize]`.

Non-goals:
- **The indexed length minus a literal**, as an index
  (`xs[len(xs) - 1usize]`, with or without a guard `len(xs) > 0usize`)
  or as a slice high bound (`h[0usize:len(h) - 1usize]`): the fourth agent
  suite's calibrated "last element" idiom (`bench/agent/tasks` and
  `bench/agent/tasks-budgeted`, 8 sites in t02, t03, t06, t07 and t10,
  and one site mapped "other" in t07), kept checked as the byte ranges and
  remainders by a value are. Proving it would put t02, t07 and t10 under
  the calibration's 6-site minimum. The owner chose to keep both suites'
  calibration (2026-11-03).
- **A slice high bound read as a value minus a literal**
  (`h[0usize:n - 1usize]`), for the same reason: the rule that proves it
  would prove t07's `h[0usize:len(h) - 1usize]`, after the index check
  `h[len(h) - 1usize]` on the line before it.
- New facts recorded past a passed slice check, and transitive bounds
  (`j <= i` with `i < len(xs)`).

## 1. Rules

The pass reads a value as a base plus a literal offset and records facts
`base + k < y` (`docs/bounds-checks.md`; `prove.go`'s doc comment).
Facts hold in the mathematical integers: k is any integer, negative
included, and `base + k` then need not be a value the program computes. The three
rules below are changes to that pass; every other rule is unchanged.

**G1 — Non-strict edges.** A non-strict relation `x <= y` on an edge
(the then edge of `x <= y` or `y >= x`, the else edge of `x > y` or
`y < x`), on unsigned operands, with x read as base + off, establishes
`base + (off - 1) < y` for every off. Today it does only for off ≥ 1.
So `n <= len(h)` establishes `n - 1 < len(h)`, which proves `h[n - 1usize]`
(F7 of `specs/prove-guards`: an index base − c is covered by a fact with
k ≥ −c) and not `h[n]`. A literal x has base none, as today.

**G2 — Slice high bound.** A slice's high bound H, read as hb + ho (hb
none for a literal), is in range when ho ≥ 0 and a fact `hb + k < len`
holds with k ≥ ho − 1, where len is the indexed place's length. Today
this needs ho ≥ 1. A high bound with ho < 0 is not in range by this rule
(§0). So `n <= len(h)` proves the high bound of `h[0usize:n]` and of
`h[i:n]`, `i + 2usize <= len(h)` still proves `h[i:i + 2usize]`, and
`2usize <= len(h)` still proves `h[0usize:2usize]` (G1 gives the literal
1 below the length).

**G3 — Slice low bound.** A slice's low bound L, read as lb + lo (lb
possibly none, for a literal), is not above its high bound H, read as
hb + ho, when one of these facts holds, in addition to today's cases:
- `lb + k < H` with k ≥ lo − 1, keyed by H's own value as an edge from a
  guard on H keys it (G1, and the strict edges as today);
- `lb + k < hb` with k ≥ lo − ho − 1, when hb is not none.

The fact `lb + k < H` gives lb + lo ≤ lb + k + 1 ≤ H. So `s <= e` proves
the low bound of `a[s:e]` and of `a[s:e + 1usize]`, and `s <= len(a)`
proves it for `a[s:len(a)]`. Today `a[s:len(a)]` needs `s < len(a)`.

**G4 — Literal zero.** A high bound that is the literal 0 is in range on
any sequence or array, with no fact needed.

**Labels.** A check today's rules prove keeps today's label, whether or
not G1 to G4 also match it (`a[0usize:2usize]` on a `[4]` array stays
`constant`). Any other check G1 to G4 prove is `dominated_by_check`. The one
exception is a slice both of whose bounds are literals, where G4 proves
the high bound: it is `constant`, as literal bounds under an array's
length are today.

**Recorded.** When the marking pass judges a slice check, it records on
`ir.CheckBounds` whether the high bound was read with ho < 0
(`HighNegative`), and keeps the field once it is set, as it keeps
`HighProven`. The release inliner copies the field. Nothing reads it but
the classification (§2).

## 2. Classification and hints

`specs/unproven-reasons` is amended to v0.3, with these changes.

**C1 — Covering.** In §3, a candidate written `V + a <= len(Q)`,
`len(Q) >= V + a`, `V + a > len(Q)` or `len(Q) < V + a` covers an index
with offset o when a − 1 ≥ o. The condition "a ≥ 1 and" is dropped, to
follow G1.

**C2 — `slice_high_bound`.** The hint depends on `HighNegative`. An
entry's `HighNegative` is true when any of its occurrences (§2 of
`specs/unproven-reasons`: release-inlined copies, instantiations) has it
set. A *simple* bound is an integer literal or has a variable (a name or
field path of a name, plus or minus an integer literal). The low bound
L *needs a test* unless it is the literal `0usize` or omitted.
- **`HighNegative` false, H simple, and L simple or not needing a
  test.** The hint is ``test `H <= len(X)` just before it`` when L does
  not need a test, and ``test `L <= H && H <= len(X)` just before it``
  when it does.
- **`HighNegative` false otherwise.** The hint is ``put the bounds in
  locals, test the low one `<=` the high one when the low one is not
  `0usize` and the high one `<= len(X)` just before the access, and slice
  with them``.
- **`HighNegative` true.** The hint is
  ``the rules do not prove a high bound they read as a value minus a
  literal`` followed by the v0.2 rewrite: ``: slice to `len(X)` instead,
  `X[L:len(X)]`, and test each index below the slice's length``. The
  same text for a non-zero L as v0.2 gives follows.

When the access is located but its high bound is not one of these
forms, the fixed hint of §2 for an unlocated access is given, as today.

**C3 — `slice_low_bound`.** When L and H are each simple (C2), the
hint is ``test `L <= H` just before it``. Otherwise it is ``put `L` and
`H` in locals, test the first `<=` the second just before the access,
and slice with them``. Both are rewrites G3 proves: with the high bound
proven, a guard `L <= H` gives the first fact of G3 with k = lo − 1.
The same holds for C2's tests of L: a guard `L <= H` proves the low bound
and `H <= len(X)` the high one (G1 and G2), each with its operands read
as the slice reads its bounds.

**C4 — Truth.** §3's closing paragraph is extended: every hint of items
1 to 9 now gives a rewrite the rules prove, of the whole access. The
classification is judged from the source and the two recorded facts of
the proof (`HighProven`, `HighNegative`), where v0.2 used one. Every
index and slice bound has type `usize` (any other index type is a
`TYPE_MISMATCH`; a signed value indexes through a conversion, as
`xs[usize(i)]` in `prove_forms_not.kern`'s `signed`, whose signed guard
stays unproven), and a hint's test compares the access's own index or
bounds, as written, with `len(X)` or with each other, so every test a
hint names compares unsigned operands, which the rules read.

The guarantee is for an access whose place X is a name or a field path
of a name, which is evaluated to the same sequence each time it is
written. For any other X (a call's result, for one) the hint's text is
the same, and its rewrite is proven once X is bound to a name first; the
hint does not say so. R5's sweep counts such accesses. Under C2 and C3 a hint
names a test only when the test's own operands read as the access's
bounds do. A bound with no variable that is not a literal (a call, for
one) is the same value only when it is put in a local.

The reason codes, the order of §3's list and every other hint are
unchanged.

## 3. Requirements and acceptance criteria

Each requirement is an acceptance criterion; the change is accepted when
all hold.


**R1 — Proofs.** Every check G1 to G4 match is proven, with the label of
§1. A check changes label only from `runtime-checked` to `proven`, and
only where one of G1 to G4 applies. Every other check keeps the label it
had.

**R2 — Soundness.** The pass must not prove a check whose index or
bounds are out of range on some execution that reaches it.

**R3 — Both engines agree.** The differential test of proven checks
gains generated programs for each rule, at and past its limit:
- G1: `h[n - 1usize]` and `h[n - 2usize]` under `n <= len(h)` with n at
  0, at the length and one past it, and `h[n]` under the same guard;
- G2: `h[i:n]` under `n <= len(h)`, with n at and past the length;
- G3: `a[s:e]` and `a[s:e + 1usize]` under `s <= e`, with s at, below
  and above e;
- G4: `h[0usize:0usize]` on an empty and a non-empty sequence, and
  `h[0usize:1usize]` on an empty one.

It passes with its default count and with 400 programs. It fails when
any rule is widened by one: G1 establishing `base + off < y`, G2
admitting k ≥ ho − 2, G3 admitting k ≥ lo − 2 or k ≥ lo − ho − 2, or
G4 admitting the literal 1.

**R4 — Fixtures.** `tests/run/prove_forms.kern` gains one function per
proven form of §0's goals and G3's examples. `prove_forms_not.kern`
gains:
- `xs[len(xs) - 1usize]` under `len(xs) > 0usize`;
- `h[0usize:len(h) - 1usize]` after `h[len(h) - 1usize]`;
- `h[0usize:n - 1usize]` under `n <= len(h)`;
- `h[n]` under `n <= len(h)`;
- `a[s:e]` under `e <= len(a)` alone;
- `h[0usize:1usize]` on a sequence with no guard.

A unit test pins their labels at both levels.

**R5 — Classification.** Unit tests in `compiler/analysis` cover C1 to
C3: a non-strict guard with offset 0 that covers `V - 1`, both
`HighNegative` cases, an aggregated entry whose occurrences differ in
`HighNegative`, and every form of C2's and C3's hints, with L needing a
test and not. For each
hint form of C2 and C3 there is a fixture rewritten as its hint says,
and its rewritten access is proven, as fixtures `f8` to `f8c` show for
v0.2. `TestReasonsSuiteSweep` passes, and logs the number of unproven slice
entries whose place is not a name or a field path of a name (C4); the
report gives it.

**R6 — Which checks change.** The report lists every bounds check whose
label changes, at both levels, in the programs under `bench/kern/`,
`examples/`, `tests/run/`, `tests/trap/` and the agent suites'
references, and in `std/`. Against the compiler before this change,
every program whose emitted LLVM IR (`scripts/capture-ll.sh`) differs at
a level has at least one check that changes label at that level, in
its own files or in a library function it compiles.

**R7 — Agent suites.** These dry runs exit 0, with `-kern` set to the
new binary:
- `bench/agent/tasks`;
- `bench/agent/tasks-budgeted`;
- `bench/agent/tasks-v3`.

Every `calibration/idioms.json` and every natural solution's count of
runtime-checked bounds checks is unchanged in both suites of the fourth
design. No calibration file is edited.

**R8 — Cost.** `BenchmarkProve`, before and after, interleaved over 5
rounds, adds at most 10% to each median.

**R9 — Documents.** These are updated:
- `docs/bounds-checks.md`: the rules, "What does not prove a check",
  the reason table's `slice_high_bound` example, and the closing
  paragraph on slice hints;
- `prove.go`'s doc comment;
- `specs/unproven-reasons/SPEC.md` (v0.3, §2 of this specification);
- `docs/reports/unproven-reasons.md`: a note that §6's items 2 to 4 are
  closed by this specification.

`go test ./tests` passes.

## 4. Evaluation

The report `docs/reports/prove-nonstrict.md` gives, offline at
`--opt=debug`, for the final programs of the paid run of the budgeted
suite (`bench/agent/runs/20261102-134710`, git-ignored, the 22 of 100
over budget):
- how many of their 82 runtime-checked locations become proven, by
  their v0.2 reason;
- how many of the 22 programs then meet their budgets.

It hand-checks one rewrite of each new hint form (C2, C3) on a run
program. No model is called; a paid run is the owner's decision.

## 5. Risks

- **The suites' calibration.** A rule broader than §1 could prove a
  calibrated site. R7 detects it; the fix is to narrow the rule, not to
  edit the calibration.
- **Negative facts.** G1 records facts with negative k for offsets
  ≤ 0. Every consumer of a fact must read it in the integers, as F7's
  negative index offsets already require. Any rule that takes a fact
  `nil + k < len` as a lower bound on the length must keep its own k ≥ 0
  test, as the bounded-index rule does. R3 covers indexes at 0 and at
  the length.
- **Compile time.** More facts survive joins, so some loops take more
  rounds. R8 bounds this.
