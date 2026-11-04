# Map: reservation and an allocator-generic form

**Date:** 2026-11-04. **Spec:** `specs/map-in/SPEC.md` v0.2 (speccritic
VALID_WITH_GAPS, no critical finding). **Plan:** `specs/map-in/PLAN.md`
(plancritic EXECUTABLE_WITH_CLARIFICATIONS, no critical finding; see
"Found on the way" for its one warning).

## What exists now

- **`Map.reserve(n)` and `Map.try_reserve(n)`.** They make room for *n*
  absent inserts: the next *n* inserts of keys the map does not hold
  make no allocation call and never rebuild.
  - When the table lacks room, it is rebuilt at the smallest size that
    has it, never smaller than the present one.
  - A request past `usize` or past 2^58 slots, or a refused allocation,
    traps `oom` (`reserve`) or returns `Err(OutOfMemory)`
    (`try_reserve`). Either way the map is unchanged.
  - `find`'s slot-validity contract now names `reserve` and
    `try_reserve` beside insert and remove.
- **`map.MapIn[K, V, A: Allocator]`.** This is `Map` over a caller's
  allocator: the same probe, growth rule and slot order.
  - A table is three allocations from `a`: the states (`cap` bytes,
    alignment 1), the keys and the values.
  - A rebuild requests them in that order. When one is refused, the
    granted ones are freed in reverse order and the map is unchanged.
  - It is fallible throughout: `new_in` (no table yet), `try_insert`,
    `try_reserve`, `release` and the destructor, with `Map`'s lookups,
    slot accessors, `each`, `each_mut` and cursor protocol. No method
    traps `oom`.
  - The type and its helpers are written in kern in `std/map.kern` and
    its identical prelude copy.

## Fixtures

- **`tests/run/map_reserve.kern`** (R1, test hooks):
  - After `try_reserve(40)`, 40 inserts each made with the next
    allocation armed to fail all succeed. The convenience `insert` would
    trap `oom` if it rebuilt; reserving 10 instead makes it trap, which
    shows the check bites.
  - A reserve with room makes no call, so the hook is still armed, and
    the first insert that must rebuild is refused. This happens at key
    47 of a 64-slot table, as the growth rule predicts.
  - A refused reserve and a reserve of 2^62 leave `len` and the slot
    order unchanged.
  - `reserve` and `try_reserve` leave the same table.
- **`tests/run/map_in.kern`** (R2, R3, over the counting `Budget`
  allocator, which traps on any free that does not match a live
  allocation):
  - Growth through rebuilds to 32, 64 and 128 slots, each with 16 + 128
    + 128 bytes and the sizes doubling.
  - Removes, a removed key put back, and two same-size rebuilds driven
    by fresh keys inserted and removed. The entries are unchanged and
    the checksum is equal.
  - `release`, after which the budget reports `live 0 bytes 0`.
  - `remove` destroys the passed key and the stored key, and returns the
    value. `release` destroys key then value in slot order. A map
    destroyed with entries releases them, and then its allocator, which
    reports nothing live.
  - A `try_insert` refused at the states, the keys and the values:
    - each refusal frees the granted allocations in reverse;
    - it destroys the key and then the value;
    - it leaves `len` 11 and three live allocations.
  - A `try_reserve` refused at the keys, and `try_reserve(2^62)` with no
    allocation call (the call count is unchanged).
- **`tests/run/map_in_diff.kern`** (R4): a `Map` and a `MapIn` take the
  same 2,000 xorshift-driven operations (1,326 inserts, 674 removes) over
  300 keys.
  - Every return and `len` agree, and the final 200 entries are in the
    same slot order.
  - It prints `equal` and the order checksum, then moves the `MapIn` to
    another thread, sums it there, and compares the sum.
- **`tests/freestanding/map_in.kern`** (R5): a `MapIn` over a
  4,096-byte budget on the freestanding `SystemAllocator`.
  - Tables of up to 128 slots fit, so the 96th insert is refused at the
    256-slot table.
  - All 95 entries are found, and 48 are removed.
  - `release` leaves nothing live.
- The run fixtures give the same output in the interpreter and natively,
  at debug and release. `--test-hooks` fixtures are debug-only.

## Effects (R6)

`compiler/analysis/try_alloc_test.go` now covers:
- `Map.try_reserve`, every `MapIn` method but `each`, `each_mut` and the
  destructor, and `map.probe_in`;
- `Map.reserve`, among the controls that must list `oom`.

Neither the walk over library calls nor the `show-effects` summaries
reach `oom` from any of them. `each` and `each_mut` call the caller's
closure, so their summaries describe that code, as the `_with`
constructors' do.

A mutation proves the test bites: one `Err(OutOfMemory)` in
`MapIn.try_rebuild`, turned into `trap_kind(oom)`, fails both tests.

## Nothing else changes (R7)

- The existing map fixtures pass unchanged.
- The 120 agent-suite files give the same `kern check` result as at
  103e5e3 (exit status, codes, spans).
- The generated inventory's corpus count grows by the new fixtures.

## Found on the way

- **A checker crash on raw-memory accesses.** Passing `ptr_view(p)` and
  `ptr_edit(q)` to one call is a conflict. Accesses opened on raw memory
  by separate calls have no root, so the checker cannot tell them apart.
  The checker then dereferenced the missing root while naming the place,
  and failed with an internal error. It now names the place "raw
  memory".
  - `tests/negative/raw_access_conflict.kern` gives `ACC_CONFLICT`.
  - Its twin opens both with one `ptr_view_edit`, which `MapIn.each_mut`
    now does too.
  - The fix is committed on its own, ahead of this change.
  - This is the one compiler change, a crash fixed into an ordinary
    diagnostic. The plan's warning was that compiler defects needed
    their own approval. The conflict rule itself is unchanged.
- **Markers for the `In` containers.** The resolver gives the
  containers' `Send`, `Share` and `Interior` markers by name, because a
  raw-pointer field alone makes a type neither `Send` nor `Share`.
  - `ArenaIn` and `PoolIn` were missing from that rule
    (`docs/reports/alloc-containers-more.md` never stated their
    markers), so neither could be moved to another thread. They now
    follow `VecIn`'s rule.
  - `MapIn` is `Send` (`Share`) iff its key, value and allocator all
    are.
  - `TestMapInMarkers` checks all eight combinations, and
    `TestAllocContainerMarkers` covers `ArenaIn` and `PoolIn`.
  - `map_in_diff.kern` moves its `MapIn` to a thread and sums it there.
    With the rule disabled, that program is `SEND_NOT_SEND`.
  - This is a second compiler change, in the resolver's marker table;
    the spec's v0.3 records it.
- **One commit, not two.** The plan put `Map.reserve` and `MapIn` in two
  phases with a commit each. Both edit the same file and test, so they
  are committed together.
- **A `hash` that traps during a rebuild** leaves entries half moved, in
  `MapIn` as in `Map`. That is user code (spec §1) and is left as it is.

## Still open

- An allocator-generic `String`, and `PoolIn`'s `alloc_with` forms and
  `get2`.
- Sharing one allocator between containers.
- `realloc` in `Allocator` (the owner's decision).
