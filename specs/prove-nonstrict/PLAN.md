# Non-strict guards and slice bounds: implementation plan

**Spec:** `specs/prove-nonstrict/SPEC.md` v0.2 (§1 G1–G4, §2 C1–C4, R1–R9,
§4). **Branch:** `main`, one commit per phase after a prism review
(`--provider openai --model gpt-6.1-sol` while Wafer has no credit, said
in the commit message). **Gates for every phase:**
- `go build ./...`;
- `go test ./...`, which includes `./tests`, `./cmd/kern` and
  `./bench/agent/...`. `bench/agent/harness/budgeted_test.go` runs the
  budgeted suite's dry run;
- `golangci-lint run ./...` clean, and `gofmt -l .` empty (lint here
  does not run gofmt);
- `go test -race ./compiler/opt/... ./compiler/analysis/... ./cmd/kern/...`.

Doc-only commits also run `go test ./tests`, for the citation lint.

**Review status.** plancritic (openai gpt-6.1-sol, profile `davin-go`,
with the spec) ran six rounds, ending EXECUTABLE_AS_IS, score 100, with
31 of 31 requirements covered. The rounds made these changes:
- R8 has a base worktree for the benchmarks and no deferred measurement;
- the evaluation stops rather than skipping when the run directory is
  missing;
- `HighNegative` aggregates over every occurrence;
- `simple` is defined by `variable`'s grammar, with rejection tests;
- G3 runs as a fallback after the low switch;
- R6 diffs every function's checks in `emit-ir`, and std is reached
  through one scratch program per module;
- G1 has an edge-orientation table, and a deterministic boundary test
  was added;
- the differential test alone must catch each mutation.

They also amended the spec to v0.2: §4's hand checks cover the forms the
run contains.

**Baseline.** Before phase 1, add a git worktree of 3e0d653 (the spec
commit; no compiler change since da73057) in the scratchpad
(`git worktree add <scratch>/base 3e0d653`). Build the compiler from it
as `kern-base`. The worktree stays until phase 3 is committed, for
R8's benchmarks, then is removed with `git worktree remove`. Every comparison below (R6, R7, R8, §4) is made against
it. Each phase's binary is built from the working tree as `kern-new`.

## Phase dependencies

1 → 2 → 3. Phase 1 changes the proof pass, the first observable change:
labels move from `runtime-checked` to `proven`. Phase 2 changes the
classification (covering rule and two hints). Phase 3 is documents,
the change listing, the IR comparison and the evaluation.

Known facts the plan relies on (checked while writing it):
- `prove.go`:
  - `edgeFacts` records the non-strict bound only under `if off >= 1`
    (the last statement of the function). G1 is that condition removed.
  - `sliceCheck(s, i, mark)` computes `hiOK`/`loOK` in two `switch`
    statements. Its high case `ho >= 1` uses `s.bound(hb, ln)` with hb
    possibly nil, so a literal high bound under `len >= K` is already
    proven (probe: `h[0usize:2usize]` under `len(h) >= 2usize` is
    `dominated_by_check`).
  - `i.HighProven` is set when `mark`.
  - Past the check it adds `hb + (ho - 1) < ln` only for `ho >= 1`.
    That line stays as it is (spec §0, no new facts past a slice).
- `s.bound(base, ln)` looks a fact up through `ln`'s same-length class.
  `p.lenKey(s, v)` is the key an edge records a comparison's right side
  under: the canonical value, or the synthetic constant length for an
  array length or a literal. G3's first lookup uses
  `p.lenKey(s, i.Hi)`, as the edge from a guard on H keys it.
- `ir.CheckBounds` has `HighProven bool`. `compiler/opt/inline.go`
  copies checks field by field (the `*ir.CheckBounds` case), and the IR
  printer does not print the field, so `emit-ir` is unchanged by a new
  one.
- `analysis.Snapshot` aggregates occurrences by (loc, kind). It skips
  `Library` and `Inlined` functions and keeps a `highFailed` map set
  when any unproven occurrence failed its high bound. `HighNegative`
  aggregates the same way: true when any occurrence has it set
  (spec C2).
- `reasons.go`:
  - `covers(cd, o)` returns `cd.a >= 1 && cd.a-1 >= o` for the
    non-strict forms. C1 drops `cd.a >= 1 &&`.
  - `classify(f, span, highFailed)` builds both slice hints in its
    `acc.slice` branch. `variable(e)` reports "has a variable".
- `emit-ir` prints every function, library ones included, with each
  check as `check_bounds … @runtime-checked` or `@proven(<by>)`. So
  label changes inside `std/` can be listed by diffing `emit-ir` per
  function.
- Probe on the base compiler (all runtime-checked today):
  - `h[n - 1usize]` and `h[0usize:n]` under `n <= len(h)`;
  - `a[s:e]` under `s <= e && e <= len(a)` (high fails);
  - `a[s:len(a)]` under `s <= len(a)` (low fails);
  - `h[0usize:0usize]`;
  - `xs[len(xs) - 1usize]` under `len(xs) > 0usize`.
- Calibrated sites that must stay `runtime-checked` (R7), in both
  `tasks/` and `tasks-budgeted/`:
  - every site of every `calibration/idioms.json`, including
    `h[0usize:len(h) - 1usize]` in t07 `pop`, which comes after
    `h[len(h) - 1usize]`;
  - the natural solutions' `while i + w <= len(xs)` loops (t03
    `max_window`, t05 `window_sums`), whose fact is keyed by `len(xs)`
    while the indexes are `xs[i + k]` and `ps[i + w]`;
  - t06 `trim_len`'s `s[n - 1usize]`, which has no `<=` guard.

## Phase 1 — The proof rules (G1–G4, `HighNegative`)

**Goals:** R1, R2, R3, R4, R7 and R8, and the `prove.go` part of R9.

**Files:**
- `compiler/opt/prove.go`:
  - **G1.** In `edgeFacts`, the non-strict branch adds
    `s.addLt(base, off-1, p.lenKey(s, hi))` for every off. off − 1
    cannot overflow: `index` bounds offsets to ±2^62.
  - **G2.** In `sliceCheck`'s high switch, the case `ho >= 1` becomes
    `ho >= 0`, still requiring `s.bound(hb, ln)` with `k >= ho-1`.
    `hb == nil && ho == 0` (G4) is a case before it with
    `hiOK, hiConst = true, true`.
  - **G3.** After the low switch (whose cases stay as they are), a
    fallback block `if !loOK { … }` tries these in order and sets
    `loOK` on the first that holds. A `switch` case such as `hiIsLen`
    that matches but whose own test fails must not hide them, which is
    why they are not added as further cases:
    - `k, ok := s.bound(lb, p.lenKey(s, i.Hi)); ok && k >= lo-1`;
    - `hb != nil` and `k, ok := s.bound(lb, p.lenKey(s, hb)); ok && k >= lo-ho-1`.

    Each is guarded against offset overflow, as `index` is. lb may be
    nil: `s.bound(nil, …)` reads facts on literals, as today. The
    `hiIsLen` case stays as it is: the fallback's first test widens it
    from `k >= lo` to `k >= lo-1`. `TestProveNonStrict` has
    `a[s:len(a)]` under `s <= len(a)` as its regression case.
  - **Labels.** Today's label assignment is unchanged. For G4,
    `constant` needs `hiConst && loConst`. The `hasLen` condition is
    relaxed only when the high bound is the literal 0, so a slice proven
    today keeps its label, and the label of a slice G2 or G3 proves is
    `dominated_by_check`.
  - **`HighNegative`.** When `mark`, set
    `i.HighNegative = i.HighNegative || (hb != nil && ho < 0)`, beside
    `HighProven`.
  - **Doc comment.** The fact rules, F3's "offset of at least one"
    sentence, and the slice rules.
- `compiler/ir/ir.go`: the `HighNegative bool` field, with a comment
  like `HighProven`'s.
- `compiler/opt/inline.go`: copy `HighNegative`.
- `compiler/opt/prove_test.go`:
  - a `TestProveNonStrict` over a source with one function per form of
    spec §0, G2's and G3's examples, and the six non-goal forms of R4.
    It checks each function's labels with `checkLabels`, and the slice
    outcomes with `sliceOutcomes`, extended to report `HighNegative`;
  - a release-inlined variant, as `TestProveSliceHighProvenInlined`
    does, showing the copy keeps `HighNegative`;
  - `TestProveNonStrictEdges`, a table over G1's four edge forms with an
    offset-0 left side: the then edges of `n <= len(h)` and
    `len(h) >= n`, and the else edges of `n > len(h)` and `len(h) < n`.
    In each, `h[n - 1usize]` is proven and `h[n]` is not. The table also
    has a signed control (`i <= i64(len(h))` guarding `h[usize(i) - 1usize]`),
    which stays runtime-checked;
  - `TestProveNonStrictFixtures`, which pins the new functions of the
    two fixture files at both levels. It uses `proveWith` for debug,
    and for release a variant that lowers with
    `opt.OptionsFor("release")`'s pipeline (Prove, then the inliner)
    and reads each function's labels after inlining, as
    `TestProveSliceHighProvenInlined` does.
- `tests/run/prove_forms.kern` and `prove_forms_not.kern`, with their
  `.out` files (R4):
  - new functions, called from `main` with values that keep every check
    passing in `prove_forms` and keep the non-goal checks passing in
    `prove_forms_not`, so the output is the same in all four
    configurations;
  - a call in `main` for `h[0usize:1usize]` on an empty sequence would
    trap, so that form is called on a non-empty sequence (the label is
    what R4 pins).
- `tests/prove_diff_test.go` (R3): generator cases, each a helper
  function plus a `case` in `genProgram`'s switch (`r.Intn(33)` grows to
  cover them):
  - G1: `ple(p, n)` reads `p[n - 1usize]` and `p[n - 2usize]` under
    `n <= len(p)`;
  - G1, the non-goal: `pleq(p, n)` reads `p[n]` under the same guard;
  - G2: `pslice(p, i, n)` takes `p[i:n]` under `n <= len(p)`;
  - G3: `plow(p, s, e)` takes `p[s:e]` and `p[s:e + 1usize]` under
    `s <= e && e <= len(p)`;
  - G4: `pzero(p)` takes `p[0usize:0usize]`, and `pone(p)` takes
    `p[0usize:1usize]` with no guard.

  Arguments are drawn from 0 to live + 1 as today, so values at 0, at
  the length and past it occur. The vector can be empty (n = 0 already
  occurs).

  Random draws do not guarantee every boundary, so a second test,
  `TestNonStrictBoundaries`, builds one program per row of a fixed
  table and compares the interpreter with both native levels, as the
  differential does. One program per row is needed because a trap ends
  a program. The rows:
  - G1 with n ∈ {0, len, len + 1};
  - G2 with n ∈ {len, len + 1};
  - G3 with s ∈ {e − 1, e, e + 1}, for both `p[s:e]` and
    `p[s:e + 1usize]`;
  - G4 `p[0usize:0usize]` on an empty and a non-empty vector, and
    `p[0usize:1usize]` on an empty one.

  It is extra deterministic coverage. R3's mutation criterion is met
  by the differential test alone (step 4).

**Steps:**
1. Write the new tests and fixtures first. Run them on the unchanged
   pass and record which fail: the proven forms must be reported
   runtime-checked.
2. Make the `prove.go`, `ir.go` and `inline.go` changes. Run the tests.
3. **R3.** Run `go test ./tests -run TestProvenChecksDifferential` with
   the default count, then with `KERN_DIFF_PROGRAMS=400`.
4. **R3 mutations.** Apply each of the five widenings in turn to an
   uncommitted copy of `prove.go`:
   - G1 records `off` instead of `off-1`;
   - G2 admits `k >= ho-2`;
   - G3 admits `k >= lo-2`;
   - G3 admits `k >= lo-ho-2`;
   - G4 admits the literal 1 (`ho <= 1`).

   For each, run the differential test with `KERN_DIFF_PROGRAMS=400`
   and record that it fails, with a seed and the mismatch. Also record
   whether `TestNonStrictBoundaries` fails. When the differential
   passes a mutation, add or adjust generator cases until it fails,
   even if the boundary test already catches it. Revert. When a
   mutation is not caught, add the generator case that catches it
   before going on.
5. **R7.** Run these dry runs with `-kern kern-new`. Each must exit 0
   with no `natural` message changed from the base binary's output:
   - `go run ./bench/agent/harness -dry-run -kern <kern-new>` (`tasks`);
   - the same with `-tasks-dir bench/agent/tasks-budgeted`;
   - the same with `-tasks-dir bench/agent/tasks-v3`.

   Passing the dry run already enforces the idiom invariant:
   `auditIdioms` (`bench/agent/harness/calibration.go`) requires the
   `idioms.json` mapping to equal the natural solution's runtime-checked
   sites as a multiset, with every mapped site still checked. It also
   requires at least 6 qualifying sites over 3 idioms. The comparison
   below adds exact counts.
   Compare each natural solution's runtime-checked sites between
   `kern-base` and `kern-new`, with `show-bounds-checks` over every
   `calibration/natural/` directory at `--opt=debug` (the level the
   harness uses), as (file, span) sets. They must be identical in both
   suites, which makes each task's count and each idiom's count equal
   to the base's. On a difference, narrow the rule (spec §5) and repeat from
   step 2.
6. **R8.** `BenchmarkProveLexer` and `BenchmarkProveSequentialGuards`
   are the whole `BenchmarkProve*` set (`compiler/opt/prove_bench_test.go`).
   Run 5 rounds, each round running
   `go test -run '^$' -bench '^BenchmarkProve' -count 1 ./compiler/opt`
   first in the base worktree, then in the working tree. Both use the
   same `go` binary, with `GOGC` unset and no other flags. Take the
   median of each benchmark's five ns/op per tree. Medians must rise by
   at most 10%. Measure on a quiet host. If the owner has said the host
   is busy today, the phase waits: it is not committed until the
   medians have been measured and meet the threshold. There is no
   deferred or owed measurement.
7. The gates, prism, then commit.

**Acceptance:**
- every new `prove_test` case and both fixture tests pass;
- the differential test passes at 40 and at 400 programs, and each
  mutation of step 4 fails it;
- the three dry runs exit 0;
- the natural-solution site sets are identical;
- `BenchmarkProve*` is within 10%;
- `git diff --stat` touches no file under `bench/agent/`.

**Key decisions:**
- **No facts past a passed slice.** The `ho >= 1` rule after the check
  is kept as it is. Otherwise a full slice `xs[i:]` would record
  `len - 1 < len` and prove a later `xs[len(xs) - 1usize]` (spec §0).
- **G2 excludes ho < 0** rather than "a high bound in the length's
  class". The rule is then a plain offset test, and the hint (C2) needs
  only one recorded bit.

**Risks:**
- A consumer of facts that assumes k ≥ 0 for a nil base. `indexCheck`'s
  bounded rule tests `k >= 0` itself, and `maskedBelow` compares
  offsets. Step 1's `pzero`/`ple` cases at n = 0 and the differential
  test cover it.
- **The loop solver.** Facts with negative k survive more joins.
  `TestProveSolver` and the benchmark show a change in rounds.

## Phase 2 — Classification (C1–C4)

**Goals:** R5, and the spec amendment of R9.

**Files:**
- `compiler/analysis/analysis.go`: `BoundsSnapshot` gains a parallel
  `highNegative []bool`, set in `Snapshot`'s `add` for every
  occurrence of the entry, proven or not, with
  `i.Index == nil && i.HighNegative`, aggregated with OR (spec C2: any
  occurrence).
- `compiler/analysis/reasons.go`:
  - **C1.** `covers` drops `cd.a >= 1 &&`.
  - `classify` takes the entry's `highNegative` beside `highFailed`.
  - **C2.** Three hint forms in the `highFailed` branch:
    - simple H, with L not needing a test;
    - simple H with simple L (`L <= H && H <= len(X)`);
    - the "put the bounds in locals" form.

    With `highNegative`, the v0.2 rewrite text is kept, with the
    sentence before it replaced as the spec says. *Simple* means
    `*ast.IntLit` (not negative), or `variable(e)` with a variable.
    *Needs a test* means L is neither nil nor the literal 0.
  - **C3.** Two forms in the low branch.
  - **`simple(e ast.Expr) bool`**, shared by C2 and C3. It holds exactly
    when e, without parentheses, is an `*ast.IntLit` with `!Neg`, or
    `variable(e)` reports a variable. `variable` already implements the
    spec's grammar: a name or a field path rooted in a name, alone or
    `+`/`-` an integer literal, through `path`, which accepts only
    `Ident` and `FieldExpr` chains ending in an `Ident`.
- `compiler/analysis/reasons_test.go`:
  - the R5 unit cases:
    - C1 with a = 0 covering `V - 1usize`, and a = 0 not covering `V`;
    - each C2 form;
    - the `highNegative` form, both directly (`h[0usize:n - 1usize]`)
      and through a local (`k := n - 1usize; h[0usize:k]`);
    - both C3 forms;
    - `simple` rejecting a call (`f()`), a sum of two variables
      (`a + b`), a field of a call (`f().n`) and a negative literal, and
      accepting `n`, `s.n`, `n + 1usize`, `n - 2usize` and `3usize`;
    - an aggregated entry whose occurrences differ in `HighNegative`,
      one proven and one not. This is a generic function instantiated
      twice, where only one instantiation's high bound reads negative,
      or, if that cannot be built, a snapshot constructed in the test.
      In that case, a test of `Snapshot` itself checks that a proven
      occurrence's field is counted;
  - **rewrite fixtures** `f9`–`f9e`, like `f8`–`f8c`: for each C2 and
    C3 form, the access as the hint rewrites it. The test checks that
    the rewritten access is proven (`show-bounds-checks` label from the
    snapshot).
- `cmd/kern/reasons_sweep_test.go`: `TestReasonsSuiteSweep` also counts
  the unproven slice entries whose place is not a name or field path of
  a name, and logs the count (C4).
- `cmd/kern/reasons_version_test.go` and
  `testdata/reasons_version.04.golden`: no change is expected. The
  fixture's unproven access is an index under a guard after it, and its
  0.4 answer is labels only. If the label changes, the golden file is
  regenerated from the base binary's answer, and the test's `KERN_BASE`
  comparison shows the base answers the same.
- `specs/unproven-reasons/SPEC.md`: v0.3:
  - §3's covering sentence (C1);
  - items 8 and 9 rewritten per C2/C3, keeping the history notes;
  - the closing paragraph extended per C4;
  - §2's "the proof outcome" sentence now names both fields;
  - §6 marks items 2 to 4 closed by `specs/prove-nonstrict`.

**Steps:**
1. Tests first, against phase 1's pass: the new hint texts fail, and
   C1's case gives `guard_too_weak` where it now covers.
2. Implement. Run `go test ./compiler/analysis/... ./cmd/kern/...`.
3. Read every new hint in the sweep's log for the suites: any `slice_*`
   entry of `TestReasonsSuiteSweep`, dumped with `-v` and a temporary
   log line. Check each by eye against C2/C3's text.
4. The gates, prism, then commit.

**Acceptance:**
- the R5 tests pass, and every `f9*` rewrite is proven;
- the sweep passes, and logs its count of non-simple places;
- `TestBoundsReasonsByVersion` passes;
- no reason code or other hint text changes. `reasons.go`'s diff
  touches only:
  - `covers`;
  - `classify`'s signature, its slice branch, and its one caller in
    `ClassifyBounds`;
  - the new `simple` helper.

  Every existing reasons test other than C1's covering case passes
  unchanged.

**Key decisions:**
- **`HighNegative` comes from the pass, not the source.** A local
  (`k := n - 1usize`) hides the negative offset from the AST.
- **OR aggregation.** The `HighNegative` form's rewrite (slice to
  `len(X)`) is proven whatever any one occurrence's reading is, so
  OR keeps the hint true for every occurrence.

**Risks:**
- **A C2/C3 hint that the rules do not prove**, for a form the fixtures
  miss. Step 3's reading and §4's hand checks are the guard. A false
  hint is a spec defect to raise with the owner, not a text to weaken
  silently.

## Phase 3 — Documents, change listing, evaluation

**Goals:** R6, the rest of R9, and §4.

**Files:**
- `docs/bounds-checks.md`:
  - "What proves a check": `x <= y` for any offset, slice high bounds
    under `H <= len`, low bounds under `L <= H`, and literal 0;
  - "What does not prove a check": the length minus a literal as an
    index or high bound, and a high bound minus a literal;
  - the reason table's `slice_high_bound` example becomes
    `h[0usize:n - 1usize]`;
  - the closing paragraph on slice hints.
- `docs/reports/unproven-reasons.md`: a note under "What this shows":
  §6's items 2 to 4 are closed by `specs/prove-nonstrict`, and the
  `slice_high_bound` hint now names a guard.
- `docs/reports/prove-nonstrict.md`: the report.

**Steps:**
1. **R6 label listing.** A scratch script runs `show-bounds-checks` with
   both binaries at both levels over:
   - every program under `bench/kern/*/`, `examples/*/` (a project
     directory as one program), `tests/run/*.kern` and
     `tests/trap/*.kern`;
   - the references of `bench/agent/tasks`, `tasks-budgeted` and
     `tasks-v3`.

   It writes each (file, line:col, kind) whose label differs. The
   aggregated entries can hide one occurrence's change behind another
   that stays checked (an instantiation, a release-inlined copy). So
   the listing also diffs the `check_bounds` lines of every function,
   user and library, in `emit-ir` of the same programs with both
   binaries at both levels. Each changed line is reported by function
   name and source position, and the listing is their union. It stops
   with an error on these:
   - a change from `proven` to `runtime-checked`;
   - a change of `proven_by` between two proven labels;
   - an entry present in one binary's answer only.

   Each remaining change is attributed in the report to the rule (G1 to
   G4) that proves it, read from the source of its access and its
   guard. A change no rule accounts for stops the phase as a spec
   defect (R1). For
   `std/`, the script diffs the `check_bounds` lines per library
   function in `emit-ir` of the same programs, with both binaries at
   both levels, and lists each library function and check whose label
   differs (deduplicated by function and source position). `std/` has
   no test files. To reach its functions beyond what the programs
   import, the script also builds one scratch program per `std` module
   (`core`, `io`, `json`, `map`, `process`, `str`, `time`), each calling
   every public function of its module once, with `u64` and `u8` for
   generic ones. A library function that no program and no scratch
   program instantiates compiles into no binary, so it has no label to
   change. The report lists the instantiations covered.
2. **R6 IR comparison.** Run `scripts/capture-ll.sh` with each binary
   into two scratch directories. For every `.ll` that differs, confirm
   the program (or a library function it compiles) is in step 1's list
   at that level. An unexplained difference stops the phase.
3. **§4 evaluation.** For the 22 over-budget final programs of
   `bench/agent/runs/20261102-134710`, run `show-bounds-checks` at
   `--opt=debug` with both binaries:
   - count the locations that become proven, grouped by their base
     (v0.2) reason;
   - run `check` with the new binary and count the programs that meet
     their budgets.

   The directory is git-ignored. It was present when this plan was
   written (202 entries, 2026-11-03). If it is missing when phase 3
   starts, the phase stops and the owner is asked: it must not be
   skipped or estimated.
4. **§4 hand checks.** For each new hint form (C2's three forms, C3's
   two, and C2's `HighNegative` form, six in all) given to at least one
   of the run's locations by the new binary,
   take one such run program, copy it into the scratchpad, rewrite it as
   the hint says, and read the rewritten access's label. The report
   names the forms no run location receives; those are covered by
   their `f9*` fixture (spec v0.2 §4).
5. Write the documents and the report. The report has the label list
   (summarised by program, the full list in a collapsed table), the IR
   comparison counts, R7's dry-run results and site-set identity, R8's
   medians, R3's mutation results, the sweep's non-simple count, and the
   evaluation.
6. `go test ./tests`, the gates, prism (for the report and docs), then
   commit.

**Acceptance:**
- every `.ll` difference is explained by a listed label change;
- the report gives every number §4 and R3–R8 name, measured, not
  estimated;
- `go test ./tests` passes.

**Risks:**
- **The `examples/` projects.** These need their directory as the unit,
  since `kern.mod` discovery puts the root at the module. The script
  passes every `.kern` file of the project, as the multi-module sweep
  did.
- **`emit-ir` naming.** It names library instantiations by type
  arguments, so the per-function diff keys by the printed name.
  Instantiations then appear once per type, and the listing collapses
  them by source position.
