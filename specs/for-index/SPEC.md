# `for i, x in xs`, and one diagnostic per conflict

**Version:** 0.1 (2026-10-31). **Scope:**
- the resolver, checker and lowering of `for`;
- the checker's diagnostic list (`compiler/check`);
- the conversion check in the resolver;
- `spec/types.md` §7, the language specification's `for` paragraph, and
  the report `docs/reports/for-index.md`.

**Language impact:** `for` with two names over arrays and sequences.
**Follows:** `specs/for-cursor/SPEC.md`.

## 1. Requirements

**R1 — Index form.** Over an array, an array place or a sequence access,
`for i, x in xs` binds `i`, a `usize` local, to the position (from 0) and
`x` to the element, exactly as `for x in xs` binds it:
- a copy of a `Copy` element;
- a view, or an edit when iterating an edit, otherwise.

**R2 — Other types.** Two names over a range, or over a type that is
neither iterable nor has the cursor protocol, is one `TYPE_MISMATCH` on
the second name. An iterable that fails declares the loop's names with
an error type, so the body reports nothing more about them. A
conversion of an error-typed value is not reported again.

**R3 — One diagnostic per conflict.** The checker drops a diagnostic
identical to an earlier one (code, rule, file, span, message). A cursor
`for` checks its iterated place in several desugared statements, and
used to report one conflict once per statement.

**R4 — Tests.**
- **A fixture**, in both engines and opt levels: an array (copies, with
  `break`), a view of Strings and an edit of them (with `continue`).
- **Negative fixtures, each with exactly one diagnostic:**
  - two names over a struct (`TYPE_MISMATCH`);
  - a nested cursor `for` over the same map (`ACC_CONFLICT`, before this
    change reported five times).
- **`for_two_names_ok`** now iterates an array with two names.
