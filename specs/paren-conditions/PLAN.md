# Parenthesized conditions: implementation plan

**Spec:** `specs/paren-conditions/SPEC.md` v0.2 (P1, P2, R1 to R7).
**Branch:** `main`, one commit after a prism review
(`--provider openai --model gpt-6.1-sol` while Wafer has no credit, said
in the commit message). It lands after the uncommitted
`specs/prove-nonstrict` phases 1 and 2 and the scoped-spawn lowering fix.
It shares no file with them, except `tests/run/prove_forms.kern` and
`.out` and `tests/prove_diff_test.go`: those are edited only after
phase 1's commit, so each commit holds one change.

**Gates:**
- `go build ./...`;
- `go test ./...`, which includes `./tests`, `./cmd/kern` and
  `./bench/agent/...`;
- `golangci-lint run ./...` and `gofmt -l .` empty;
- `go test -race ./compiler/lower/... ./compiler/opt/...`.

All of these wait until the owner says the host is free. While it is
busy, only `go test` on `compiler/lower` and single fixtures run.

**Baseline.** The spec's baseline compiler is the parent of this change's
commit. Build it into the scratchpad as `kern-pc-base` from a worktree
of that commit, once the earlier commits have landed.

Known facts (checked while writing the plan):
- `condBr(e, then, els)` in `compiler/lower/stmts.go` switches on
  `*ast.Binary` (`&&`, `||`) and `*ast.Unary` (`!`). Any other expression,
  `*ast.Paren` included, goes through `fl.value` and one `CondBr`.
  `ifStmt` and `whileStmt` are its only external callers.
- The value lowering of `&&`/`||` short circuits through `sc.rhs` and
  `sc.join` blocks, which is how R1 detects the old shape.
- The reasons classifier's `cond` already strips parentheses (`unparen`),
  so its guard candidates are unchanged.
- R3's trace already exists. `tests/run/short_circuit.kern` evaluates
  `if !(t("f", false) || t("g", false)) && !t("h", false)` and
  `while k < 3usize && (k != 2usize || t("w", false))`, where `t` prints
  its name, so the output records each operand evaluated and each one
  skipped. The file is not edited. Its unchanged `.out` is the P2
  check, in every `TestFixtures` run.
- No `.kern` file under `bench/agent/` has a parenthesized condition.
  `std/str.kern` (lines 248 and 250), `examples/selfhost/lexer.kern`,
  `bench/kern/lock_free_queue/*` and `tests/run/short_circuit.kern` do.

## Phase 1 — The lowering, tests, listing, documents

**Files:**
- `compiler/lower/stmts.go`: in `condBr`, add
  `case *ast.Paren: fl.condBr(x.X, then, els); return`, and extend
  `condBr`'s comment (P1).
- `compiler/lower/lower_test.go`, or the lower package's existing test
  file for conditions: `TestCondBrParens` (R1). It lowers a function per
  form:
  - `!(a && b)`, `(a && b)`, `((a))` and `a && (b || c)`, each with a
    comparison operand;
  - a parenthesized comparison `(i < n)`.

  For each, it asserts that no block in the function is named
  `sc.rhs`/`sc.join`, and that the number of `CondBr` terminators equals
  the number of comparisons. For the parenthesized comparison, and for
  `((a))`, the test also lowers the same function written without the
  parentheses. It compares the two `ir.Print` outputs, with the function
  name replaced, and they must be equal: same operands, same blocks,
  same branch targets.
- `tests/run/prove_forms.kern` and `.out`: `paren_not`, `paren_and` and
  `paren_mixed` (R2), with `main` calls whose checks pass. In
  `compiler/opt/prove_test.go`, a `TestProveParenFixtures` pins them
  `dominated_by_check` at both levels, with `labelsWithin`.
- `tests/prove_diff_test.go`: a helper `pnot(p, i, j)` in `diffHelpers`,
  `if !(i < len(p) && j < len(p)) { return 4u64 } else { return p[i] }`,
  as R4 writes it, and a case drawing i and j each with `idx()`.
  `idx()` draws 0 to live + 1, so each falls below, at and past the
  length (live). The fixed boundary test `TestNonStrictBoundaries` gains
  rows with (i, j) at (0, 0), (len − 1, 0), (len, 0) and (0, len).
- `docs/bounds-checks.md`: under "A guard on the same length", one line
  saying conditions inside parentheses count (R7).
- `docs/reports/paren-conditions.md`: the report (R6, R4, R5).

**Steps:**
1. Write `TestCondBrParens` and the fixture functions. Confirm both fail
   on the unchanged lowering: `sc.rhs` blocks are present, and the
   labels are `runtime`.
2. Add the `Paren` case. Run `go test ./compiler/lower ./compiler/opt`
   and the fixtures `TestFixtures/run/(prove_forms|short_circuit)` (R3).
3. **R4.** Run the differential test at the default count and with
   `KERN_DIFF_PROGRAMS=400`. Mutation: in an uncommitted copy, make the
   `!` case route a parenthesized operand wrongly: when `x.X` is a
   `*ast.Paren`, call `fl.condBr(x.X, then, els)` (no swap), so the else
   edge carries the then edge's facts. That is R4's mutation. Record
   that `TestFixtures/run/(short_circuit|prove_forms).kern` fail under
   it (spec v0.2: the differential test, comparing engines over one IR,
   cannot see a lowering change; done 2026-11-03, it passed 400 programs
   under the mutation while both fixtures failed). Revert.
4. **R5.** Run the three dry runs with `-kern` set to the new binary and
   check that each exits 0. Then compare each natural solution's
   runtime-checked site set between `kern-pc-base` and the new binary,
   as (file, span) at `--opt=debug`, over `bench/agent/tasks` and
   `bench/agent/tasks-budgeted`. The sets must be identical.
5. **R6.** Collect the label changes. Run `show-bounds-checks` with both
   binaries at both levels over `bench/kern/*/`, `examples/*/` (a
   project as one program), `tests/run/*.kern` and `tests/trap/*.kern`,
   and diff `emit-ir`'s `check_bounds` lines per function, so that
   library functions are included. To reach `std/` beyond what those
   programs import, this step uses one scratch program per std module
   (`core`, `io`, `json`, `map`, `process`, `str`, `time`), each calling
   every public function of its module once, with `u64` and `u8` for the
   generic ones. They are the same drivers the prove-nonstrict plan's
   third phase describes. If they are not in the scratchpad yet, this
   step writes them, then checks that each one checks and that each
   module's public functions are all called. A grep of `pub func` per
   module against the calls gives that list. A library
   function no program instantiates compiles into no binary, so it has
   no label to change.

   Then run `scripts/capture-ll.sh` with both binaries and list the `.ll`
   files that differ. For each, diff the two binaries' `emit-ir` of that
   program function by function to find the functions whose IR differs.
   For each such function, trace the difference: either its own source
   (located by its printed name in the program or in `std/`) contains a
   parenthesized `if`/`while` condition, or a parenthesized operand of
   `&&`, `||` or `!` in one; or the release inliner copied into it a
   function that does. R6's acceptance is per program: every differing
   program has at least one such function. A program whose differences
   no such function explains stops the phase as a defect. Name the `bench/kern` programs among them in the
   report, so the owner can decide whether to time them.
6. Write the documents and report. Run `go test ./tests`, the gates,
   prism, then commit.

**Acceptance:**
- `TestCondBrParens` and `TestProveParenFixtures` pass, and the fixtures
  pass;
- the differential test passes at 40 and at 400 programs, and the
  mutation fails both fixtures;
- the three dry runs exit 0, and the natural site sets are identical;
- every changed `.ll` is accounted for;
- the gates are clean.

**Risks:**
- **A fixture whose output depends on evaluation order.**
  `short_circuit.kern` is the guard (P2). A failure there means the
  `Paren` case changed the order, and the change is wrong.
- **The release inliner.** Inlining copies blocks after lowering, so it
  sees the new shape. The differential test's release runs and the
  release fixture runs cover it.
