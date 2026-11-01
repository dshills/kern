# Nested `for` loops over one cursor-protocol place

**Date:** 2026-11-01. **Spec:** `specs/nested-cursor-loops/SPEC.md` v0.1.
**Plan:** `specs/nested-cursor-loops/PLAN.md`. **Item:** the ninth of the
ten-item list ("nested loops over the same map are rejected, while two
loops over one array nest").

## Cause

A cursor loop is desugared into a `while` whose body binds the element
with `with x := m.cursor_get(#cur)`. That binding freezes `m`: for the
block, `m` may be accessed only through it (§5.4). The nested loop's
`m.cursor_first()`, `cursor_end()`, `cursor_next()` and getter are view
accesses to `m`, so each conflicted. An inner array loop instead creates
its own freeze, and two freezes are compatible, which is why array loops
nest.

## Change

- The resolver records the calls it generates for a cursor loop
  (`Info.CursorCalls`) and its getter bindings that use a view getter
  (`Info.CursorViews`: `cursor_get`, `cursor_entry`, not `_mut`).
- The checker marks the corresponding accesses (`access.cursor`,
  `access.cursorView`) and skips a cursor call's view access against a
  view getter's freeze. Every other pair is checked as before, so an outer
  loop binding an edit (a mutable map whose values are not `Copy`) still
  rejects the inner loop, and a non-cursor read of the map in the body
  is still `ACC_CONFLICT`.
- An edit-binding outer loop now conflicts with both the inner loop's view
  calls and its edit getter; since all of one loop's desugared calls share
  its span, the checker reports one `ACC_CONFLICT` per span
  (`fn.cursorConflicts`), as before.

## Verification

- `tests/run/for_cursor_nested` (all four configurations): an all-pairs
  sum over a map with `Copy` values (8,500), three nested loops (125
  triples), an all-pairs sum over a view of a map with `String` values
  (11).
- `tests/negative/for_cursor_nested`, rewritten to the edit case (one
  `ACC_CONFLICT`), and its twin over a view (accepted);
  `tests/negative/for_cursor_body_read` (a `t.len()` in the body) and its
  twin. The old `for_cursor_nested` (a map with `Copy` values) is now
  accepted, which is the change.

## Review

- **speccritic** (gpt-6.1-sol, `specs/for-cursor` as context): no
  critical finding; R3's corpus and baseline pinned.
- **R3.** Baseline from cf9c50a (314 `.ll` files, 46 failure lines):
  all 314 byte-identical after, the same failures; new:
  `for_cursor_nested` (2 `.ll` files).
- **prism** (default settings, medium effort): three low findings, none
  adopted: a fresh-root result access is never a cursor call's (a cursor
  call always has its receiver as source); the dedupe's reliance on one
  span per loop holds by construction (the desugaring gives every call the
  iterated expression's span) and is stated in the field's comment; a
  `_mut` inner loop inside a view outer loop over one place cannot occur
  (the getter depends only on the place's mutability and the value type,
  which both loops share).
- **realitycheck** PARTIALLY_ALIGNED, score 100, no warning;
  **verifier** offline: 38 low findings, none on this change; clarion
  skipped. `golangci-lint` and `KERN_REQUIRE_QEMU=1 go test ./...` clean.
