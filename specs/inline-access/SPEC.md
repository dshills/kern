# Inlining functions that return a view or edit

**Version:** 0.1 (2026-10-31). **Scope:** both inliners in
`compiler/opt` (user functions before the proof pass, library functions
after it, in release execution), their tests, and the report
`docs/reports/inline-access.md`. **Language impact:** none. **Follows:**
`specs/inline-library/SPEC.md`, which left these functions as calls.

## 1. Requirements

**R1 — When.** A callee that returns an access is copied, under every
other rule of its inliner, when the copy can define the call's
destinations directly:
- the callee has one `ret`;
- its values are distinct and defined by the callee's own instructions;
- none of them is the access a view or edit parameter opened.

The caller's `access_end` of a destination then ends the copied access.
Any other such callee stays a call, since an access cannot pass through
a result local.

**R2 — Validity.** The IR validator passes after both passes. A copied
projection derived from an argument access whose `access_end` comes
first is re-parented as the validator already does.

**R3 — Effects.**
- The report lists every bounds check of the repository's programs whose
  label changes.
- Suite 4's dry run passes with its natural-solution counts unchanged.

**R4 — Tests.**
- A view-returning and an edit-returning function are copied, with
  output the same as without inlining.
- A function returning one of two projections stays a call.
- The full suite passes.
