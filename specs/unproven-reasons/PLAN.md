# Why a bounds check is not proven — implementation plan

**Spec:** `specs/unproven-reasons/SPEC.md` v0.1 (§2, §3, R1 to R8, §5).
**Branch:** `main`, one commit per phase after a prism review (fallback
`--provider openai --model gpt-6.1-sol` while Wafer has no credit, said
in the commit message). **Gates for every phase:** `go build ./...`,
`go test ./...` (the root packages, `./tests`, `./cmd/kern` and
`./bench/agent/...` included), `golangci-lint run ./...` clean, and
`go test -race ./compiler/analysis/... ./compiler/opt/... ./cmd/kern/...`.
Doc-only commits also run `go test ./tests` (the citation lint).

**Review status.** plancritic (openai gpt-6.1-sol, profile `davin-go`,
with the spec), six rounds, ending EXECUTABLE_AS_IS, score 100. The
rounds moved the classifier behind one exported API over a single
collection of the module (`Snapshot`), replaced a package-global
classifier with explicit injection (`showBoundsChecksWith`,
`budgetDeps`), made the suite sweep in process so it sees
`ClassifyStats`, read the evaluation from `show-bounds-checks` instead
of the truncated message, and corrected the spec: closures use only the
innermost body's guards, an unlocated entry has a fixed hint, a
byte-order index's offset includes W − 1, and R2 compares the LLVM IR
instead of the linked executable (two builds by one binary differ on
this host).

## Phase dependencies

1 → 2 → 3 → 4 → 5. Phase 1 changes no output. Phase 2 adds a classifier
no command calls. Phase 3 is the first observable change (schema 0.5);
phase 4 the second (the `BUDGET_EXCEEDED` message). Phase 5 is
documents, the suite sweep, the final byte-identity check and the
evaluation.

Known facts the plan relies on (checked while writing it):
- `ir.CheckBounds` is `{Index; Lo, Hi; Len; Prov; Pos}`; the IR printer
  (`compiler/ir/print.go`) prints only `Prov.ProvenBy`, so a new field
  on `CheckBounds` does not change `emit-ir`. The inliner copies a check
  field by field (`compiler/opt/inline.go`, the `*ir.CheckBounds` case),
  so a new field must be copied there; `merge_returns.go` only reads
  operands.
- `prove.go`'s `sliceCheck(s, i, mark)` computes `hiOK` and `loOK` and
  sets `Prov` only when `mark` is set (the marking pass over the stable
  facts), which is where the high-bound outcome is recorded.
- `analysis.BoundsChecks(mod)` aggregates occurrences by (loc, kind);
  `loc` is the block's `FileIn(f)` plus the check's `ast.Pos` span, so an
  inlined copy reports the callee's source location. `Budgets(prog,
  srcs, mod, target)` already has the resolved program and the sources;
  `cmd/kern`'s `showBoundsChecks` calls `Build(srcs, cliOpt)`, which
  resolves internally (`buildWith`) and returns only the module.
- `resolve.Program.Info.Types` maps every typed expression to its type;
  a type parameter inside a generic body is `*resolve.TypeParamType`;
  `resolve.Array` carries the length.
- AST: `IndexExpr{X; Args}` (one argument for an index), `SliceExpr{X;
  Lo, Hi}` (nil for an omitted bound), `CallExpr{Fn; Args []*Arg}`,
  `Binary`, `Unary`, `Paren{X}`, `FieldExpr{X; Name}`, `Ident`,
  `IntLit{Value; Suffix; Text}`, `RangeExpr{Lo, Hi}` (a `for` header's
  `Iter`), `If{Cond; Then; Else}`, `While{Cond; Body}`, `ClosureExpr`.
  Every node's `Pos` is `{Start, End}`.
- `diag.WriteAt(w, version, r)` already downgrades a result by copying it
  through an older struct (`effectsBefore04`); `diag.Accepted` lists the
  versions; `go generate ./compiler/diag` regenerates
  `tools/agent-api/schema.md` and `schema_test.go` fails when it is stale.
  Tests that name the newest version: `cmd/kern/effects_version_test.go`,
  `check_tests_test.go`, `budget_test.go`, `message_test.go`,
  `compiler/diag/schema_test.go`, `compiler/diag/diag_test.go`.
- `scripts/capture-ll.sh <kern> <dir>` writes the LLVM IR of every
  single-file program under `bench/kern`, `tests/run`, `tests/trap` and
  `tests/positive` at both levels, for a byte comparison of two
  compilers.
- The harness's query tool runs `kern show-bounds-checks` with files on
  the command line (`bench/agent/harness/trial.go`), so it answers at the
  newest version; `countRuntimeBoundsChecks` and the calibration decode
  only `label` and `kind`.

Baseline binary: before phase 1, `git worktree add` a scratch checkout
at 28f2751 (a local commit), build `kern` there into the scratchpad as
`kern-base`, and remove the worktree; phases 1, 3, 4 and 5 compare
against it.

## Phase 1 — Record the failed slice bound (R2)

**Goals.** The proof pass records, for every slice check, whether its
high bound was proven; nothing observable changes.

**Files.** `compiler/ir/ir.go`, `compiler/opt/prove.go`,
`compiler/opt/inline.go`, `compiler/opt/prove_test.go`.

**Steps.**
1. `ir.CheckBounds` gains `HighProven bool` with a comment: set by the
   proof pass's marking round on a slice check, true when the high bound
   is proven (whatever the low bound), meaningless on an index check and
   before the proof pass.
2. `sliceCheck`: when `mark` is set, `i.HighProven = hiOK` (a proven
   check sets it true as well). Nothing else in the function changes.
3. `inline.go` copies `HighProven` with the other fields.
4. Tests in `prove_test.go`, using its existing per-function proof
   strings: a slice whose high bound is unproven (`h[0usize:n]` under
   `n <= len(h)`), one whose low bound alone is unproven (`a[s:len(a)]`
   with no guard on `s`), one proven, each asserting `HighProven`; and an
   inlined slice (release) whose copy keeps the field.
5. Byte identity (R2): `scripts/capture-ll.sh` with `kern-base` and the
   new binary into two scratch directories, compared with `diff -r`;
   `emit-ir` on the same programs compared the same way. Recorded in the
   commit message.

**Decisions.** A field on `CheckBounds`, not on `Provenance`: provenance
is what the wire reports, and this is an implementation fact the wire
reports only through a reason.

**Risks.** A pass that rebuilds a check without the field would lose it
silently; the inlined-slice test covers the one rebuilder.

**Acceptance.** Gates green; the two comparisons show no difference.

## Phase 2 — The classifier (§2, §3, R1)

**Goals.** A function that assigns each unproven bounds entry its reason
and hint, tested on its own; no command calls it yet.

**Files.** `compiler/analysis/reasons.go` (new),
`compiler/analysis/reasons_test.go` (new), `compiler/analysis/analysis.go`
(occurrence data), `compiler/analysis/budget.go` (`snippet` reuse only).

**Steps.**
1. `analysis.go`: an unexported `boundsChecks(mod) (diag.ShowBoundsChecksResult,
   []bool)` that `BoundsChecks` wraps; the slice is parallel to `Checks`
   and true for an unproven `bounds` entry when any of its
   runtime-checked occurrences is a slice check with `HighProven` false.
   `BoundsChecks`'s output is unchanged.
2. `reasons.go`, the exported API (both callers, `cmd/kern` and
   `Budgets`, use only this):
   ```go
   // BoundsSnapshot is one collection of mod's bounds entries: the
   // show-bounds-checks result and, unexported, the per-entry
   // slice-high-failed flags of step 1.
   type BoundsSnapshot struct {
       Result     diag.ShowBoundsChecksResult
       highFailed []bool
   }
   func Snapshot(mod *ir.Module) *BoundsSnapshot
   type Reason struct{ Code, Hint string }
   // ClassifyStats is what one classification did, for tests.
   type ClassifyStats struct{ Entries, Unlocated int }
   // Classifier classifies the unproven bounds entries of snap for which
   // want returns true (want nil: all), returning a Reason per
   // classified entry keyed by its index in snap.Result.Checks.
   type Classifier func(prog *resolve.Program, srcs []*source.File,
       snap *BoundsSnapshot, want func(diag.BoundsCheck) bool) (map[int]Reason, ClassifyStats)
   func ClassifyBounds(...) // the Classifier
   ```
   `BoundsChecks(mod)` becomes `Snapshot(mod).Result`. The module is
   collected once per command; classification reads the snapshot and
   the AST, never the module again. Steps inside, each a
   small function:
   - **Locate** (§2): the `ast.File` whose `Name` is the entry's file,
     the top-level or `impl` `FuncDecl` whose span contains the entry's
     start, and in its body the node whose `Pos` equals the entry's span:
     an `IndexExpr` with one argument, a `SliceExpr`, or a `CallExpr`
     whose callee is `load_be`, `load_le`, `store_be` or `store_le`
     (directly or as `name[T]`). For a byte-order call, W is the byte
     width of the integer type: the call's type from `Info.Types` for a
     load, the value argument's for a store. An entry that cannot be
     located (none is expected) gets `no_guard` with spec §2's fixed
     hint and increments `ClassifyStats.Unlocated`, which phase 5's
     sweep asserts is zero.
   - **Normalize**: drop outer `Paren`s from E, L, H, X; an omitted
     slice bound is treated as written (spec §2): a nil `Lo` is the
     literal `0usize` (no variable) and a nil `Hi` is `len(X)`, with
     hint text `0usize` and `` `len(X)` `` built from X's text; compute the
     variable and offset of an index (a name or `FieldExpr` chain of a
     name, optionally a `Binary` `+`/`-` with an `IntLit` right operand).
     For the byte-order form, E's text is `I + (W-1)usize` built from I's
     text (I itself at W = 1) and truncated as one fragment; its variable
     and offset are I's with W − 1 added when I has a variable (spec §2),
     none otherwise.
   - **Candidates**: walk the innermost body containing the access (the
     `ClosureExpr` body when the access is in a closure, else the
     function body), not descending into any `ClosureExpr` nested in
     it. In each `If.Cond` and `While.Cond` (an `else if` is
     an `If` in `Else`), descend through `&&`, `||`, `!` and `Paren` to
     the comparisons `<`, `<=`, `>`, `>=` with one side a `CallExpr` of
     `len` with one argument Q; the other side must have a variable. A
     `For` whose `Iter` is a `RangeExpr` with `Hi` a `len(Q)` call gives
     the candidate `v < len(Q)` with variable `v` and offset 0. Each
     candidate records its operator, which side `len` is on, V, a, Q's
     normalized node and its start.
   - **Covers** (§3): a table from (operator, side) to "strict" (covers
     o ≤ a) or "non-strict" (covers o ≤ a − 1 when a ≥ 1, nothing
     otherwise), the eight written forms of §3.
   - **Same place**: lex the source text of the two normalized nodes'
     spans (`compiler/lexer`) and compare kind and text token by token.
   - **Decide** in §3's order, citing per §3's rule (the last candidate
     starting before the access, else the first after). `type_range`
     reads `Info.Types[E]`'s conversion: E is a `CallExpr` whose callee
     `Ident` names an unsigned integer type U, with one argument of an
     unsigned type of fewer bits, X's type (through an access type) a
     `resolve.Array` with length n ≥ 2^b, and none of the three types a
     `*resolve.TypeParamType` or containing one. `remainder_by_value`: E
     is a `Binary` `%` whose right operand, without parentheses, is not
     an `IntLit`. Slices use the `occ` flag.
   - **Hints**: one `fmt` template per reason, exactly §3's text, each
     substituted fragment passed through a `fragment(src, span)` helper
     that collapses whitespace and cuts at 40 code points plus `...`
     (`snippet`'s rule, factored out and shared); L:C through the
     existing `lineCol`. `type_range`'s M is `2^b - 1` in decimal and u
     is U's name (the literal suffix equals the type name).
3. `reasons_test.go`, programs compiled at `debug` through the same
   lowering `Budgets`' tests use:
   - per reason 1 to 7, an access that gets it, asserting the exact
     hint, and the rewrite the hint gives, asserting the entry is then
     proven (R8);
   - 8 and 9 with exact hints;
   - byte-order calls at W = 8 (`load_be[u64]`) and W = 1 (`load_be[u8]`
     if accepted, else `store_be` of a `u8`), and at W = 8 with an index
     `i + 1usize` (offset 8: a guard `i + 8usize < len(buf)` is
     `guard_not_on_every_path` when on one branch, `i + 7usize <
     len(buf)` is `guard_too_weak`), and a 40-code-point I whose
     synthetic E is cut as one fragment;
   - candidate choice (two candidates before, one after only); a
     guard in a nested closure the access is not in (ignored); an access
     in a closure with a guard in the enclosing function and another in
     the closure (only the closure's cited); a `for` header candidate;
     `len((xs))` against `xs`;
   - each of the eight written forms of §3 at its boundary, a table
     test: for the four strict forms with a = 1, offset 1 covered and 2
     not; for the four non-strict forms with a = 1, offset 0 covered and
     1 not, and with a = 0 offset 0 not covered (each uncovered case
     giving `guard_too_weak`, each covered one `guard_not_on_every_path`
     when the guard is put on one branch only);
   - an inverted guard (`if i >= len(xs) { xs[i] }`) gets
     `guard_not_on_every_path`;
   - truncation of a 50-code-point E inside `E < len(xs)`;
   - aggregation (R1): a generic slice instantiated twice, failing a
     different bound in each, gives `slice_high_bound`; a generic
     conversion index gives `computed_index`;
   - determinism: classifying the same module twice gives equal
     results, and with the files given in reverse order.

**Decisions.** The classifier reads the AST and types, not the proof
facts, so its statements are about the source (spec §3's truth
argument); the only proof input is the label and `HighProven`. Hints
are built in Go, not in a table of format strings shared with docs,
since the docs list conditions, not texts.

**Risks.** An entry span that matches no node (a desugared access, for
example the `for k, v in m` cursor protocol): the fallback keeps output
well-formed, and phase 5's sweep finds any in the suites.

**Acceptance.** Gates green; no command output changes (the classifier
is unreferenced outside tests; `golangci-lint`'s unused check is
satisfied by the tests).

## Phase 3 — `show-bounds-checks` at 0.5 (R3, R5 for queries)

**Goals.** Schema 0.5 with `reason` and `hint`; 0.2 to 0.4 unchanged.

**Files.** `compiler/diag/schema.go`, `compiler/diag/diag.go`,
`compiler/diag/diag_test.go`, `compiler/diag/schema_test.go`,
`tools/agent-api/schema.json`, `tools/agent-api/schema.md` (generated),
`cmd/kern/run.go`, `cmd/kern/commands.go`, `cmd/kern/*_test.go` naming
the newest version, a new `cmd/kern/reasons_version_test.go`,
`README.md` (version paragraph).

**Steps.**
1. `diag.BoundsCheck` gains `Reason *string \`json:"reason"\`` and
   `Hint *string \`json:"hint"\``. `SchemaVersion` = "0.5",
   `Accepted` = 0.2, 0.3, 0.4, 0.5. `WriteAt` below 0.5 copies a
   `ShowBoundsChecksResult` through a 0.4 struct without the two fields,
   leaving the caller's value untouched; at 0.2 and 0.3 the existing
   effects downgrade still applies.
2. `cmd/kern/run.go`: `buildWith` is split so a caller can also get the
   resolved program (`buildProg(srcs, opt) (*resolve.Program,
   *ir.Module, []diag.Diagnostic, error)`, which `buildWith` calls); no
   second resolve.
3. Explicit injection, no package variable: `showBoundsChecks` (the
   command-table entry, signature unchanged) calls
   `showBoundsChecksWith(analysis.ClassifyBounds, req, wire, srcs,
   stdout, stderr)`. At wire 0.5 that helper runs `buildProg`, `snap :=
   analysis.Snapshot(mod)`, `classify(prog, srcs, snap, nil)`, and `reason`/`hint` filled on the entries the map names
   (null elsewhere, so every entry has both keys); below 0.5, today's
   path, which never calls `classify` (R5).
4. `schema.go` descriptions: `BoundsCheck.reason` lists the nine codes,
   non-null iff `kind` is `bounds` and `label` `runtime-checked`, stable
   across compiler versions; `BoundsCheck.hint`, one clause for a
   reader, not stable; `Request.schema_version` names 0.5.
   `schema.json`: the two fields (`reason` an enum of the nine codes or
   null; `hint` string or null), each `description` carrying the same
   text as `schema.go`'s, stability included, and the version enum, by
   hand. `go
   generate ./compiler/diag`.
5. Tests: the version tests updated to 0.5; a new test runs one program
   with an unproven index, a proven index and an overflow check at 0.4
   and 0.5: the 0.4 output byte-identical to `kern-base`'s (golden file
   recorded from it, compared with `KERN_BASE` when set, as
   `effects_version_test.go` does), 0.5 with both fields set only on the
   unproven bounds entry. A test helper `counting()` returns a
   classifier that wraps `ClassifyBounds` and counts its calls in a
   per-test value; tests pass it to `showBoundsChecksWith` directly:
   at 0.4, zero calls; at 0.5, one. `show-copies`, `emit-ir`, `run` and
   `test` reach no classifier by construction (none is passed to them),
   which the phase-4 tests also exercise end to end through the CLI
   output (no `reason` key below 0.5, no hint in their output).
6. `README.md`: 0.5 newest, 0.4 without `reason`/`hint`.

**Risks.** The harness's query arm changes output (R7): stated in phase
5's report note, not guarded here.

**Acceptance.** Gates green; `schema.md` not stale.

## Phase 4 — Hints in `BUDGET_EXCEEDED` (R4, R5 for budgets)

**Goals.** Each listed bounds location carries its hint; `; `
separators; classification only for exceeded `bounds_checks` items.

**Files.** `compiler/analysis/budget.go`, `compiler/analysis/budget_test.go`,
`cmd/kern/budget_test.go`, `bench/agent/harness/budgeted_test.go` (only
if it asserts message text), `specs/declared-budgets/SPEC.md` (R4
amendment).

**Steps.**
1. `Budgets` gains a `classify analysis.Classifier` parameter. In
   `cmd/kern`, `lowerFunc`'s companion travels with it: `runCheck`,
   `budgetDiags` and `evaluateBudgets` take a `budgetDeps{lower
   lowerFunc; classify analysis.Classifier}` in place of `lower`, built
   by `check` and `build` from `Build` and `analysis.ClassifyBounds`
   (the call sites that pass `lower` today), so tests pass a counting
   classifier explicitly. It takes `snap := Snapshot(mod)`
   once, counts `bounds_checks` items from `snap.Result` as it counts
   from `BoundsChecks(mod)` today, and only then, when at least one
   `bounds_checks` item is exceeded, makes one `classify(prog, srcs,
   snap, want)` call, `want` selecting the entries attributed to the
   exceeded items' functions; none otherwise.
2. `budgetMessage` takes an optional hint per location: a location is
   `` L:C `snippet` `` followed by ` (hint)` when it has one; items are
   joined by `; `; the tail is `; and N more`. `copies` and
   `allocations` messages keep today's text exactly (no hint, and their
   separator stays `, `: R4 changes only bounds items). The span,
   related spans and null fix are unchanged.
3. Tests: the spec's `rank` example message exactly; the separator; 11
   locations (`; and 1 more`); an unchanged `copies` message; and R5's
   cases with `counting()` passed in `budgetDeps`: `check` and `build` on a budget-free
   program, `check` with a met `bounds_checks` item, a `copies` overrun
   alone, and `run` and `test` on an over-budget program (they do not
   enforce), all zero calls; `check` with an exceeded `bounds_checks`
   item, exactly one per evaluation.
   `cmd/kern/budget_test.go` expectations that quote messages updated.
4. R5 byte identity: `check` on every budget-free `.kern` under
   `tests/`, one invocation per file with `kern-base` and the new
   binary, each invocation's raw stdout, stderr and exit status compared
   byte for byte (the file list is sorted so the two runs visit files in
   the same order; no output is sorted).
5. `specs/declared-budgets/SPEC.md` R4: a sentence that the location
   list's format is amended by `specs/unproven-reasons` R4 (hints, `; `);
   version note 0.1 → 0.2 in its header.

**Decisions.** `, ` stays for `copies` and `allocations`: R4 amends only
the bounds message, and keeping the other two byte-identical is the
cheaper guarantee to test.

**Acceptance.** Gates green; the 452-file comparison shows no
difference.

## Phase 5 — Reference, suite sweep, identity, evaluation (R6, R7, R8, R2, §5)

**Goals.** Documents, the sweep over the agent suites, the final
byte-identity check, and the offline evaluation report.

**Files.** `docs/bounds-checks.md`, `docs/reports/declared-budgets.md`
(note), `docs/reports/unproven-reasons.md` (new),
`cmd/kern/reasons_sweep_test.go` (new), `bench/agent/harness/report.go`
(R7 disclosure), a scratch evaluation script under the scratchpad (not
committed).

**Steps.**
1. `docs/bounds-checks.md`: a section "Why a check is not proven" with
   the nine reasons, each its condition in one sentence and one example
   access; the "Declaring a budget" example output replaced by the R4
   message for `rank`, produced by the binary (not typed).
   `docs/reports/declared-budgets.md`: one note after its example output
   that messages carry hints since `specs/unproven-reasons`.
2. Suite sweep (R8, R7): `cmd/kern/reasons_sweep_test.go`, in process
   (package `main`, so it calls `buildProg`, `analysis.Snapshot` and
   `analysis.ClassifyBounds` directly and sees `ClassifyStats`), over
   every task's `reference/`, `start/` and natural solution
   (`calibration/`) of `bench/agent/tasks` and `bench/agent/tasks-budgeted`
   that checks: every unproven bounds entry has a reason among the nine
   and a non-empty hint, and `Unlocated` is zero. One CLI run on one
   suite reference confirms the same fields reach the JSON. The suites'
   existing dry runs and `budgeted_test.go` pass unchanged (no regenerated
   file; `git status bench/agent/tasks*` clean).
3. R2 final, `kern-base` against the final binary, every comparison a
   byte `diff`:
   - `capture-ll.sh` (the LLVM IR `build` compiles, both levels, every
     single-file program of `bench/kern`, `tests/run`, `tests/trap`,
     `tests/positive`), and `emit-ir` and `emit-asm` on the same files;
   - `run` output (stdout, stderr, exit code) of every program under
     `tests/run` and `tests/trap`;
   - the programs under `tests/` beyond `capture-ll.sh`'s four
     directories: every directory or file under `tests/` that
     `kern-base build` builds (the inventory is the base binary's success
     list, recorded in the commit message), `build --emit-llvm`,
     `emit-ir` and `emit-asm` compared, and `run` stdout, stderr and exit
     status for every one of them (each run under a 60-second timeout;
     a program that times out with both binaries counts as equal, and is
     listed);
   - the agent suites: `emit-ir`, `emit-asm`, `build --emit-llvm` and
     `run` of every `reference/` directory of `bench/agent/tasks`,
     `tasks-v3` and `tasks-budgeted`.
   Linked executables are not compared (spec R2: two builds by one
   binary already differ on this host). `kern-base` is the previous
   binary R2 means: 28f2751 is the last commit before phase 1, and no
   commit between it and phase 1 touches code.
4. Evaluation (§5): a scratch script that walks
   `bench/agent/runs/20261102-134710/*/` (present on this host, checked
   2026-11-03: 101 trial directories with their final programs). For
   each final program, `check` decides whether it ends over budget; for
   those, `show-bounds-checks --opt=debug` at 0.5 gives every unproven
   bounds entry (in the budgeted suite every function has `budget {
   bounds_checks 0 }`, so these are exactly the over-budget locations,
   with no 10-location truncation), and its `reason`. The same for the
   budgeted suite's natural solutions with budgets added as
   `budgeted_test.go` does. The report gets a table by reason for each,
   and ten hand checks (at least one per reason that occurs), each the
   access, its hint, the rewrite applied in a scratch copy, and the
   resulting label.
5. R7 disclosure: `bench/agent/harness/report.go`'s arm description
   (the "Arms:" sentence every generated report carries) says that the
   `show_bounds_checks` query returns `reason` and `hint` per unproven
   bounds check since schema 0.5, so every later run's report states it;
   its report test updated.
6. `go test ./tests` before committing documents.

**Acceptance.** Gates green; the report exists with both tables and ten
checks; identity comparisons show no difference; spec §7's criteria
met.

## Requirement coverage

| requirement | phase |
|---|---|
| §2 access forms, §3 reasons and hints, R1 | 2 (aggregation data in 2, wired in 3 and 4) |
| R2 proof recording, artifact identity | 1 (recording, first check), 5 (final check) |
| R3 schema 0.5 | 3 |
| R4 message | 4 |
| R5 cost | 3 (queries), 4 (budgets, byte identity) |
| R6 reference | 5 |
| R7 agent suites | 5 (sweep, unchanged trees), report note |
| R8 tests | 2, 3, 4, 5 |
| §5 evaluation | 5 |
| §7 acceptance | every phase's gates; 5 |
