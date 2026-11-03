# Parenthesized conditions lowered as branches

**Version:** 0.2 (2026-11-03; 0.1 the same day, whose R4 asked the differential test to catch a lowering mutation). **Scope:** the lowering of `if` and `while`
conditions (`condBr` in `compiler/lower/stmts.go`); its tests; fixtures
under `tests/run/`; the differential test of proven checks
(`tests/prove_diff_test.go`); `docs/bounds-checks.md`; the report
`docs/reports/paren-conditions.md`.
**Language impact:** none: a program's observable behavior is unchanged.
**Proof impact:** some bounds checks change from `runtime-checked` to
`proven`.
**Follows:** F2 of `specs/prove-guards/SPEC.md`, which lowers a condition
whose operator is `&&`, `||` or `!` as branches.

## 0. Goals and non-goals

F2 lowers `a && b`, `a || b` and `!a` in an `if` or `while` condition as
branches. Each comparison then establishes its facts on its own edges,
which is how a guard proves a bounds check. When parentheses enclose
the operator, the condition is lowered as a value instead, and the
guards inside it prove nothing. Probed on the compiler at 3e0d653, `xs[i]`
stays runtime-checked in:
- `if !(i < len(xs) && j < len(xs)) { return 0u8 }` followed by `return xs[i]`;
- `if (i < len(xs) && j < len(xs)) { return xs[i] }`.

Its unparenthesized equivalent, `if i >= len(xs) || j >= len(xs) { return 0u8 }`
followed by `return xs[i]`, is proven. An agent naturally writes the
first form for an early return.

Goals: a parenthesized condition is lowered as its contents are.

Non-goals:
- conditions in value position (`x := (a && b)`), whose lowering F2 left
  unchanged;
- `match` guards and any other construct that is not an `if` or `while`
  condition;
- any other proof rule.

## 1. Rule

**P1.** Wherever F2 lowers a condition as branches (the condition of an
`if` or `while`, and recursively each operand of `&&` and `||` and the
operand of `!` within it), a parenthesized expression `(e)` is lowered as
`e` is, with the same targets. So `!(a && b)`, `(a && b)`, `((a))` and
`a && (b || c)` are lowered as `!` of `&&`, `&&`, `a`, and `&&` of `||`.
A parenthesized expression whose contents are not one of these
operators, such as a comparison, is lowered as a value, exactly as that
expression is today.

**P2.** The operands are evaluated in the same order and at most the same
number of times as before, and the program's output, exit status and
traps are unchanged. Lowering `&&` and `||` as a value already short
circuits, so P1 changes the blocks that test the operands and not the
operands that are evaluated.

## 2. Requirements and acceptance criteria

Each requirement is an acceptance criterion; the change is accepted when
all hold. The *baseline compiler* is the one built from this change's
parent commit on `main`, the same tree without P1. Because work in
progress may land first, it is not a fixed revision. R5 and R6 compare
against it.

**R1 — Lowering.** A unit test in `compiler/lower` shows that each form
of P1 is lowered with no value-position short circuit: no `sc.rhs` or
`sc.join` block in the condition's lowering. It also shows that a
parenthesized comparison is lowered as the comparison is.

**R2 — Proofs.** `tests/run/prove_forms.kern` gains functions for the two
forms of §0 and for `a && (b || c)` guarding an index in `a`, each now
proven. A unit test in `compiler/opt` pins their labels.

**R3 — Behavior.** `tests/run/short_circuit.kern` traces evaluation
order through a parenthesized `!( … || … )`. It and every other fixture
under `tests/` pass unchanged in every run `TestFixtures`
(`tests/harness_test.go`) makes of it: the interpreter, and the native
builds at `--opt debug` and `--opt release` where the fixture's header
allows them.

**R4 — Both engines agree.** The differential test of proven checks
gains a generated case, `if !(i < len(p) && j < len(p)) { … } else { … p[i] … }`,
with i and j drawn at, below and past the length. It passes with its
default count and with 400 programs. A lowering mutation that routes a
parenthesized `!` operand without swapping its targets fails the
fixtures with expected output, `short_circuit.kern` and
`prove_forms.kern` (R3). It does not fail the differential test: both
engines run the same lowered IR, so a lowering change alters both alike.
(Version 0.1 asked the differential test to catch it. Running the
mutation showed it cannot.)

**R5 — Agent suites.** These dry runs exit 0, with `-kern` set to the
new binary:
- `bench/agent/tasks`;
- `bench/agent/tasks-budgeted`;
- `bench/agent/tasks-v3`.

Every `calibration/natural/` solution's set of runtime-checked bounds
sites (file and span, at `--opt=debug`) is identical to the baseline
compiler's in `bench/agent/tasks` and `bench/agent/tasks-budgeted`, and no
calibration file is edited. (No program under `bench/agent/` contains a parenthesized
condition. The comparison confirms that nothing else moves.)

**R6 — Which checks change.** The report lists, at both levels, every
bounds check whose label changes in the programs under `bench/kern/`,
`examples/`, `tests/run/` and `tests/trap/`, and in `std/`, read from
`show-bounds-checks` and from `emit-ir`'s check lines for library
functions. It also lists the programs whose emitted LLVM IR
(`scripts/capture-ll.sh`) differs from the baseline compiler's. Every
such program contains a parenthesized condition, in its own files or in
a library function it compiles.

**R7 — Documents.** `docs/bounds-checks.md` says that conditions inside
parentheses count. `go test ./tests` passes.

## 3. Risks

- **Performance.** A condition lowered as branches has a different block
  shape. LLVM normally produces the same code for both shapes. The
  report does not time programs. R6 lists the changed IR, and a
  benchmark program whose IR changes is named so that the owner can
  decide whether to time it.
