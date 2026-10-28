# The remaining §4.1 arithmetic forms

**Date:** 2026-10-28. **Spec:** §4.1 lists the explicit alternatives to the
trapping operators as exactly `wrapping_add`, `wrapping_sub`,
`wrapping_mul`, `wrapping_neg`, `wrapping_shl`, `wrapping_shr`,
`saturating_add`, `saturating_sub`, `saturating_mul`, and `checked_add`,
`checked_sub`, `checked_mul`, `checked_div`, `checked_rem`, `checked_shl`,
`checked_shr` returning `Option[T]`. **Starting point:** the prelude
declared the first three; the other thirteen did not exist in any stage,
although the Phase 1 report listed "trapping, wrapping, saturating and
checked forms" as done.

## What changed

- **Prelude** (`compiler/resolve/prelude/core.kern`): the thirteen
  declarations, integer-only like the wrapping forms (the resolver's
  check now covers all sixteen names).
- **Wrapping negation and shifts** lower to the existing IR: `wrapping_neg`
  is the `neg` unary without the preceding overflow check, and
  `wrapping_shl`/`wrapping_shr` are the shift binaries in wrapping mode,
  whose count the backend already masked to `width - 1`. The interpreter
  masked a wrapping shift's count to 63 whatever the width; it now masks
  to `width - 1` as the spec says (`wrapping_shl(1u8, 9)` is 2, not 0).
- **Saturating and checked forms** are intrinsic calls: the generic call
  lowering already emits one for a prelude function without a body. In
  the interpreter, `arithTrap(op, a, b)`, factored out of the overflow
  check, names the trap the trapping form would take ("" when none); the
  checked forms return `None` exactly then and `Some` of the wrapping
  result otherwise, and the saturating forms clamp to the limit in the
  direction of the overflow. The backend uses `llvm.{s,u}{add,sub}.sat`,
  the `with.overflow` intrinsics (with a sign-selected limit for
  `saturating_mul`), a zero-or-minimum test for division and remainder
  with the operands replaced by safe ones on the `None` path so the
  untaken division is never undefined, and a width test plus a logical
  shift back for the shifts; an `Option[T]` result is built in memory as
  `Atomic.compare_exchange` builds its `Result`. The proof pass treats all
  of them as read-only calls.
- **A pre-existing backend bug.** The trapping left shift checked for a
  set bit shifted out by shifting back arithmetically for signed types,
  so `-1i32 << 1` passed natively and trapped in the interpreter, which
  shifts the representation back logically as §4.1 reads ("a set bit is
  shifted out"). The backend now shifts back logically too;
  `tests/trap/shift_signed_out.kern` and its twin pin it.

## Tests

`tests/run/arith_forms.kern` prints every form at the edges where the
trapping form traps and just inside them, for `u8`, `i8`, `i32`, `u64`
and `i64`, in both engines and both configurations.

`tests/arith_diff_test.go` (`TestIntegerSemanticsDifferential`) is the
integer counterpart of the proven-checks differential test: random
expressions over every integer type using the trapping operators, all
sixteen alternative forms, conversions, truncation and shifts with counts
past the width, operands drawn near the limits, printed by a program the
interpreter and both native builds must agree on in output, status and
trap record. 600 programs agree; 40 run in the suite.

## Verification

`go test ./...` and `golangci-lint run ./...` pass; the Prism review is
recorded in the commit.
