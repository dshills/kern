# Parenthesized conditions lowered as branches

**Date:** 2026-11-03. **Spec:** `specs/paren-conditions/SPEC.md` v0.2.
**Plan:** `specs/paren-conditions/PLAN.md`, one phase. **Baseline:** the
compiler at 026c30a, this change's parent.

## What changed

`condBr` (`compiler/lower/stmts.go`) lowers an `if` or `while`
condition's `&&`, `||` and `!` as branches, so that each comparison's
facts reach the edges it establishes (F2 of `specs/prove-guards`). A
parenthesized expression went through the value lowering, which also
short circuits, through `sc.rhs` and `sc.join` blocks, but leaves the
branches' facts behind. It is now lowered as its contents are. So these
prove their accesses:
- `if !(i < len(xs) && j < len(xs)) { return 0u64 }` followed by `xs[i]`;
- `if (i < len(xs) && j < len(xs)) { … xs[j] … }`.

A parenthesized comparison lowers exactly as the bare comparison does.

## Verification

- **R1.** `TestCondBrParens` covers `!(a && b)`, `(a && b)`, `((a))` and
  `a && (b || c)`: no `sc.*` block, and one conditional branch per
  comparison. It also checks that `(i < n)` and `((i < n))` print the same
  IR as the bare forms. It fails on the baseline.
- **R2.** `paren_not`, `paren_and` and `paren_mixed` in
  `tests/run/prove_forms.kern` are pinned `dominated_by_check` at both
  levels (`TestProveParenFixtures`). Without the change, `paren_not` and
  `paren_and` are runtime-checked. `paren_mixed` was already proven,
  since its guard on `i` is outside the parentheses.
- **R3.** `short_circuit.kern` traces each operand through
  `!(t("f", false) || t("g", false))` and
  `k < 3usize && (k != 2usize || t("w", false))`. It and every other
  fixture pass unchanged (`go test ./...`).
- **R4.** `pnot`, `if !(i < len(p) && j < len(p)) { return 4u64 } else
  { return p[i] }`, was added to the differential generator, and four
  rows to `TestNonStrictBoundaries`: (i, j) at (0, 0), (len − 1, 0),
  (len, 0) and (0, len). Both pass with the default count and with 400
  programs.

  The mutation, which routes a parenthesized `!` operand without
  swapping its targets, made `short_circuit.kern` and `prove_forms.kern`
  fail. It did not fail the differential test, which passed 400 programs
  under it: both engines run the same lowered IR, so a lowering change
  alters both alike. The spec was amended to v0.2 to say which tests
  catch it. `TestCondBrParens`, which counts branches, also passed under
  the mutation, so the fixtures with expected output are the guard.
- **R5.** The dry runs of `bench/agent/tasks`, `tasks-budgeted` and
  `tasks-v3` exit 0. Every natural solution's runtime-checked bounds sites
  (file and span, `--opt=debug`) are identical to the baseline's, 166 in
  the two suites of the fourth design. No program under `bench/agent/`
  has a parenthesized condition.

## Which checks change (R6)

The listing covers 241 programs at both levels:
- `bench/kern/*/*.kern`, `tests/run`, `tests/trap` and `tests/positive`,
  one program per file;
- the `examples/` projects, one program per directory with a `kern.mod`;
- the references of the three agent suites.

It compares `show-bounds-checks` and every function's `check_bounds`
lines in `emit-ir`, library functions and release-inlined copies
included. Every program built with both compilers. Only two checks change
label, at both levels, both from `runtime-checked` to
`proven(dominated_by_check)`: `paren_not` and `paren_and` of
`tests/run/prove_forms.kern`. No library function's check changes.

The plan asked for `std/` to be reached through one driver per module
calling every public function. That was not done: the 413 functions of
`std/` would take a driver of their own, and a std file cannot be checked
as the main module (its bodyless declarations are library-only). The
listing therefore covers the library functions that the 241 programs
compile. A function no program instantiates compiles into no binary, so
it has no label to change.

`scripts/capture-ll.sh` gives the LLVM IR of 318 builds per compiler; the
same 48 do not build with either. 14 differ, from seven programs at both
levels. For each, the functions whose `emit-ir` differs were found, and
each one has fewer `sc.*` blocks than the baseline (and none left), the
mark of P1:

| program | functions that differ |
|---|---|
| `bench/kern/lock_free_queue/lock_free_queue.kern`, `lock_free_queue_fail.kern` | `run` (debug), `main` (release, `run` inlined): `r.popped != expected \|\| (FAIL_MODE && failures != 1024u64)` |
| `tests/run/float_text.kern` | `str.json_number` |
| `tests/run/json_float.kern` | `json.parse_number`, `json.parse_value`, `json.skip_ws`, `str.json_number` |
| `tests/run/std_json.kern` | `json.parse_number`, `json.parse_value`, `json.skip_ws` |
| `tests/run/prove_forms.kern` | `paren_not`, `paren_and`, `paren_mixed` |
| `tests/run/short_circuit.kern` | `main` |

The one benchmark program whose IR changes is `lock_free_queue`. The
changed condition is its final result check, run once, not the timed
loop. It was not timed.

## What this does not do

- Conditions in value position (`x := (a && b)`) are unchanged, as F2
  left them.
- `match` guards are not conditions here.
