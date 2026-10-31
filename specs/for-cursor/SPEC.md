# `for` over a map: the cursor protocol

**Version:** 0.1 (2026-10-31). **Scope:**
- the parser, formatter and AST of `for` (a second name);
- the resolver, which desugars a cursor `for`;
- the checker and the lowering, which run the desugared loop;
- the IR validator (multi-access results);
- two raw-access intrinsics (`ptr_view2`, `ptr_view_edit`), `Vec.ptr_at`,
  and `std/map`'s protocol methods;
- the documents `spec/types.md` §7, the language specification's `for`
  paragraph, `docs/ownership.md`, `docs/unsafe-audit.md`, `README.md`,
  and the report `docs/reports/for-cursor.md`.

**Language impact:** a second name in `for`, and the protocol itself.
Both are authorized by the owner's request to implement the iterator
protocol. No aliasing rule changes. **Follows:** the "Still not done" of
`docs/reports/std-gaps.md` (an iterator protocol).

## 0. Constraint

Accesses are second class (spec §5): an iterator object holding a
borrow cannot exist. So the protocol is positional: the loop keeps a
`usize` position and asks the collection for the element at it, each
time through a fresh projection. While a `with` binding is live, its
root may be accessed only through it (spec §5.4). Two names therefore
come from one call returning a tuple of accesses, which the
multi-access rule requires to be implemented in `unsafe` with a
disjointness contract.

## 1. Requirements

**R1 — Syntax.** `for a in e` as before, and `for a, b in e`. The
formatter prints both.

**R2 — Protocol.** As `spec/types.md` §7 states:
- the position methods `cursor_first`, `cursor_next` and `cursor_end`;
- the one-name getter `cursor_get`, with `cursor_get_mut`;
- the two-name getter `cursor_entry`, with `cursor_entry_mut`;
- the `_mut` choice for a mutable place and a non-`Copy` element, and
  `Copy` elements bound as copies.

Two names over any other type, or a missing getter, is `TYPE_MISMATCH`,
with one diagnostic.

**R3 — Desugaring.** The resolver rewrites the loop to a `while` over a
position computed before the body (so `continue` needs no step), with the
getter's names bound by `with`. An iterated expression other than a name
or field path is bound once. The checker and lowering run that loop, so
the loop's accesses and conflicts are the ordinary ones: the iterated
place is bound for the body.

**R4 — Map.** `Map[K, V]` has the protocol over slots, in the order of
`each`. `cursor_entry` and `cursor_entry_mut` open the key and the value
at the payload offset of `Some` in the two vectors, through
`Vec.ptr_at`, `ptr_view2` and `ptr_view_edit`.

**R5 — Validator.** The accesses one call returns as a tuple are
distinct roots, as spec §5.4 says of multi-access bindings.

**R6 — Tests.**
- **A fixture**, in both engines and both opt levels: Copy and `String`
  keys, values edited in place, `break`, `continue` and `return`,
  tombstones, an empty map, a map reached through a projection call and
  nested in another loop, and a type of the program's own with one name,
  read and edited.
- **Negative fixtures:** inserting into the visited map
  (`ACC_CONFLICT`), two names over an array, and two names over a type
  without `cursor_entry`. Each has an accepted twin.
