# `ArenaIn` and `PoolIn`: construct in place, and `PoolIn.get2`

**Date:** 2026-11-04. **Spec:** `specs/in-forms/SPEC.md` v0.3 (speccritic
VALID_WITH_GAPS in both rounds, no critical finding). **Plan:**
`specs/in-forms/PLAN.md` (plancritic EXECUTABLE_AS_IS, score 100).
**Follows:** `docs/reports/alloc-containers-more.md`, which listed these
forms as still open.

## What exists now

- **`ArenaIn.try_alloc_with` and `try_alloc_with_ctx`.** These are
  `Arena.alloc_with` and `alloc_with_ctx` over the held allocator.
  - When the arena is full it grows first. A refused growth returns
    `Err(Alloc(e))` with the allocator's own error, before the
    initializer runs, and leaves the arena unchanged.
  - A failed initializer gives `Err(Init(e))`. The arena holds what it
    held; a growth already made is kept.
- **`PoolIn.try_alloc_with` and `try_alloc_with_ctx`.** These are
  `Pool.alloc_with` and `alloc_with_ctx` over the held allocator.
  - The slot is chosen as `try_alloc` chooses it: the most recently
    freed one first.
  - After a failed initializer the slot is vacant, keeps its generation,
    and is the next one taken.
- **`PoolIn.get2`.** Checks `a`, then `b`, as `get` does, and only then
  traps `disjoint` when both name one slot. A stale handle passed twice
  therefore traps `generation`.

All five are library code in `std/core.kern` and its identical prelude
copy. The compiler is unchanged.

## Fixtures

- **`tests/run/in_alloc_with.kern`** (R1, R2, R4) uses the counting
  `Budget` allocator of `tests/run/arenain_poolin.kern`, which traps on
  any free that does not match a live allocation. It covers:
  - for an arena, five `try_alloc_with` and five `try_alloc_with_ctx`
    values across the growth from 8 to 16 slots; the sum over all ten
    is 73,850;
  - two failing initializers, one of each form: each destroys only the
    token it had set (`drop tok 99`, `drop tok 98`) and gives `Init`,
    and `len` stays 10;
  - for a pool, the same ten values, then two slots freed (2 and 6):
    - a failing `try_alloc_with` takes slot 6 and gives it back;
    - the next `try_alloc_with` reuses slot 6 at generation 1;
    - the `_ctx` forms then do the same with slot 2;
  - the pool sum (79,350) over the ten live values;
  - after `release`, `budget live 0 bytes 0` for both, after 2 calls
    (arena) and 4 (pool);
  - a budget of 8 bytes, which refuses the first growth: all four forms
    give `Alloc`, the initializer (which would print `init called`)
    never runs, and nothing is allocated.
- **`tests/run/poolin_get2.kern`** (R3): a swap through `get2` of slots 1
  and 10, and an update of slot 11 from slot 0, in one statement each.
- **`tests/trap/poolin_get2_same.kern`**: `get2(a, a)` traps `disjoint`.
- **`tests/trap/poolin_get2_stale.kern`**: a slot freed and reused by
  `try_alloc`; `get2` of the new handle and a live one prints, then the
  old handle traps `generation`.
- Every fixture gives the same output in the interpreter and natively,
  at debug and release (R6), and the harness runs them all.

## Effects (R5)

`compiler/analysis/try_alloc_test.go` now walks from the five methods:
- `PoolIn.get2` joins the forms whose code and effect summary must not
  reach `oom`;
- the four `_with` forms join `Box.try_new_with` and
  `Box.try_new_with_ctx` in a list of forms that call the caller's
  initializer. Their own code is walked, and their summaries must carry
  the label `unknown`, since they run the caller's code.

A mutation proves the test bites: one `Err(Alloc(e))` in
`PoolIn.try_alloc_with`, turned into `trap_kind(oom)`, fails
`TestTryFormsReachNoOOMTrap` with the path
`PoolIn.try_alloc_with[T=u64,A=SystemAllocator,E=()]`.

## Nothing else changes (R7)

`scripts/capture-ll.sh` was run with a compiler built at `dd16b89` and
with this change, over every single-file program in `bench/kern`,
`tests/run`, `tests/trap` and `tests/positive` at debug and release.
- All 338 `.ll` files the baseline produced are byte-identical.
- The only differences are the four new fixtures, eight builds that the
  baseline cannot compile (it lacks the methods).

The existing `Arena`, `Pool`, `ArenaIn`, `PoolIn` and `alloc_with`
fixtures pass unchanged. The generated hosted-only inventory's corpus
count grows from 254 to 258, by the new fixtures.

## Found on the way

- **The unsafe audit was behind.** `docs/unsafe-audit.md` listed
  `BoxIn` and `VecIn` but no `ArenaIn`, `PoolIn` or `MapIn` function, a
  gap older than this change.
  - It now has a row for each of the 38 `ArenaIn`, `PoolIn` and `MapIn`
    functions with an `unsafe` block, the five new ones included.
  - The rows were generated from the source by a script that reads the
    blocks' raw operations and the contracts. It reproduces the existing
    `Pool` and `VecIn` rows exactly.
  - Its count for `std/core.kern`, which said 67 functions and 71
    blocks, now reads 86 and 90.
- **The duplication grew.** `PoolIn` now repeats three more of
  `Pool`'s methods. Sharing the slot code is still open (spec §0).

## Still open

- An allocator-generic `String`.
- Sharing `Pool`'s and `PoolIn`'s slot code.
- Sharing one allocator between containers.
- `realloc` in `Allocator` (the owner's decision).
