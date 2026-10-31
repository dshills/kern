# `for` over a map: the cursor protocol

**Date:** 2026-10-31. **Spec:** `specs/for-cursor/SPEC.md`. **Closes:**
the iterator protocol of `docs/reports/std-gaps.md`'s "Still not done".

## What changed

```kern
for k, v in m { total = total + k * v }    // m: map.Map[u64, u64]
for k, v in tags { v.push_str("!") }        // values edited in place
for s in bag { say(s.as_view()) }           // a type of the program's own
```

- **Syntax.** `for` takes an optional second name. The parser and the
  formatter handle it.
- **Protocol.** A type iterates through position methods
  (`cursor_first`, `cursor_next`, `cursor_end`) and a getter:
  - `cursor_get` for one name;
  - `cursor_entry` for two, a tuple of accesses;
  - a `_mut` variant of each, used when the place is mutable and the
    element is not `Copy`.
- **Desugaring.** The resolver rewrites such a loop to a `while` over a
  `usize` position, the getter bound by `with`. The rewrite is kept on
  the `For` node in an unexported field, so tree walks (the formatter,
  the editor's index) see only the source. The checker and the lowering
  run it, so no new ownership rule was needed: the iterated place is
  bound for the body exactly as for a sequence.
- **`std/map`.** `Map` gains the protocol over its slots.
  `cursor_entry` and `cursor_entry_mut` are the module's first `unsafe`
  code, two audited blocks: they open the key and the value inside their
  `Some` at the §4.1 payload offset.
  - They read the slots through the new `Vec.ptr_at`, since `Vec`'s
    fields are private to the prelude.
  - They open both accesses with two new intrinsics, `ptr_view2` and
    `ptr_view_edit`, which are `ptr_edit2` with two element types.
- **IR validator.** The accesses one call returns as a tuple are now
  distinct roots, as spec §5.4 says of multi-access bindings. Before, an
  edit through the value conflicted with the open view of the key.

## Designs that did not work

Each failure was a rule of the language, not a defect:

- **Two separate getters** (`cursor_key`, `cursor_value`) conflict.
  While one `with` binding of the map is live, the map may be accessed
  only through it (§5.4), even by a view. Relaxing that would change the
  aliasing rules, which are the owner's, so the protocol uses one call
  returning both accesses: the multi-access rule's sanctioned form.
- **Source code cannot build a tuple of accesses** (`return (k, v)` does
  not parse). Hence the intrinsics.
- **Re-evaluating a projection call** on every iteration (`h.table()`)
  was wrong for any call with effects, and for an index the body could
  change. Anything but a name or field path is now bound once.

## Verification

- **`tests/run/for_cursor`.** The interpreter and native agree in debug
  and release. The values were checked by hand: 910 = 10·(1+4+…+36),
  14260 = (7·1+8·2)·(10+90+160+360).
- **Negative fixtures.** `for_cursor_mutate` (`ACC_CONFLICT`), and
  `for_two_names`, `for_range_two_names` and `for_cursor_no_entry`
  (`TYPE_MISMATCH`), each with exactly one diagnostic and an accepted
  twin.
- **Gates.** `golangci-lint run ./...` reports 0 issues. `go test ./...`
  passes except the ThreadSanitizer fixture `race_raw_ptr`, whose
  symbolizer hangs on this host (it fails the same way on 7e5f638).
- **`kern format`** prints `for k, v in m`.
- **The editor's index** skips the desugaring's names, which start with
  `#`.

## Reviews

prism, four passes; every finding fixed:
- **Fresh roots lost ancestry.** The first validator fix gave a tuple's
  accesses roots of their own, which lost their relation to the call's
  argument. They now keep the argument's root and parent, and only
  siblings are exempt from each other.
- **Reborrows of a member.** That exemption at first did not cover a
  reborrow of one tuple member; it now compares the tuples along both
  ancestor chains.
- **Ranges.** Two names over a range were accepted. They are
  `TYPE_MISMATCH`, with a negative fixture.
- **`Some`'s payload offset.** The offset was written as the payload's
  alignment, which is right for `Option` (a one-byte tag) but implicit.
  It is now computed explicitly, and the fixture iterates one-byte keys
  and values, where a wrong offset would read the tag.

## Not done

- **Nested loops over the same collection** are rejected: the inner
  loop accesses a place the outer binding holds. This is the same as for
  sequences today, and is the §5.4 rule.
- **A two-name loop over a sequence** (`for i, x in xs`) is not
  provided.
