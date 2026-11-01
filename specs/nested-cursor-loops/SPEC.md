# Nested `for` loops over one cursor-protocol place

**Version:** 0.1 (2026-11-01). **Scope:** the resolver's cursor-loop
desugaring (`compiler/resolve/bodies.go`, `decls.go`), the checker's
conflict rule (`compiler/check`); fixtures; `spec/types.md` §7 and the
for-cursor report. **Language impact:** one rule relaxed: nested
cursor-protocol loops over the same place are accepted when the outer
loop binds views.
**Related:** `specs/for-cursor/SPEC.md` (the desugaring);
`docs/reports/for-cursor.md` ("nested loops over the same map are
rejected"; two loops over one array nest); spec §5.4 (a live binding's
root is accessed only through it).

**Review status.** speccritic (gpt-6.1-sol, `specs/for-cursor` as
context): no critical finding; R3's corpus and baseline pinned.

**Glossary.** R*n*: requirement *n* of §4. Cursor loop: a `for` over a
type with the cursor protocol, desugared by the resolver into a `while`
over positions whose body binds the element with `with` from the
getter. View getter: `cursor_get` or `cursor_entry` (bindings are
views); edit getter: the `_mut` ones. Cursor call: a call the
desugaring generated (`cursor_first`, `cursor_next`, `cursor_end`, the
getter). Freeze: the checker's record that a `with` binding's root may
be accessed only through the binding.

## 0. Goals

`for a, x in m { for b, y in m { ... } }` over a map, the shape of every
all-pairs computation, is accepted when the outer loop binds views, as
two loops over one array already are. Everything else §5.4 rejects stays
rejected: a read of the map in the loop body other than the inner loop's
own cursor calls, and any nesting when the outer loop binds an edit.

## 1. Background

The desugared outer loop's getter binding freezes the map; the inner
loop's cursor calls are view accesses to the map, which a freeze forbids.
Over an array, the inner `for` creates its own freeze instead of an
access, and the checker treats two freezes as compatible, so array loops
nest. Neither engine nor the generated code depends on this rule; it is
a checker rule only.

## 2. Design

- The resolver records which calls are cursor calls and which getter
  bindings use a view getter.
- The checker does not report a conflict between a view access made by a
  cursor call and a freeze made by a view-getter binding. Every other
  pair is checked as before: an edit getter's freeze (outer `_mut` loop)
  still conflicts with the inner loop's calls; a non-cursor read of the
  place in the body still conflicts with either freeze.
- No other diagnostic changes; programs accepted before are accepted
  with the same code.

## 3. Out of scope

Nesting under an edit-binding outer loop; reads of the iterated place in
a loop body; loops over different places (already accepted).

## 4. Requirements

**R1. Nesting accepted.** Nested two-name and one-name cursor loops over
one map with `Copy` values, and over a `view` of a map with any values,
check and run. *Verified by:* `tests/run/for_cursor_nested.kern` (all
four configurations) printing an all-pairs sum and a count.

**R2. Still rejected.** Nesting over a mutable map whose values are not
`Copy` (the outer loop binds an edit) is `ACC_CONFLICT`, once; a read of
the map in a loop body is `ACC_CONFLICT`. *Verified by:*
`tests/negative/for_cursor_nested.kern` (now the edit case) with its
accepting twin (the same loops over a `view`), and
`tests/negative/for_cursor_body_read.kern` with its twin.

**R3. Existing programs unchanged.** The LLVM IR of every program of the
`scripts/capture-ll.sh` corpus (`bench/kern`, `tests/run`, `tests/trap`,
`tests/positive`, both opt levels, the host target) is byte-identical
between a compiler built from cf9c50a, the commit this work starts from,
and the changed one, with the same programs failing to build (the change
is a checker rule; the desugaring emits the same nodes). *Verified by:*
the two captures and a file-by-file `cmp`.

**R4. Documents.** `spec/types.md` §7 states the rule;
`docs/reports/for-cursor.md`'s "Not done" item points here;
`docs/reports/nested-cursor-loops.md` records the change and the review.

## 5. Acceptance criteria

`golangci-lint run ./...` and `KERN_REQUIRE_QEMU=1 go test ./...` clean;
R3 byte-identical.
