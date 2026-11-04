# Containers over a caller's allocator: `ArenaIn` and `PoolIn`

**Date:** 2026-11-04. **Spec:** `specs/alloc-containers-more/SPEC.md`.
**Plan:** `specs/alloc-containers-more/PLAN.md`. **Follows:**
`docs/reports/alloc-containers.md` (`BoxIn`, `VecIn`).

## API

`ArenaIn[T, A: Allocator]` and `PoolIn[T, A: Allocator]` are `Arena` and
`Pool` over an allocator the container holds inline and owns. Both
have `new_in`, `try_alloc`, `get`, `at`, `len`, `allocator` and
`release`; `PoolIn` also has `free`. They issue `ArenaId[T]` and
`PoolId[T]` and check them as `Arena` and `Pool` do:
- bounds;
- identity, in debug builds;
- for a pool, vacancy and generation.

Like `VecIn`, they are fallible throughout. `try_alloc` returns the
allocator's error and destroys the value, and the container is
unchanged.

**Growth.** The `Allocator` interface has no `realloc`, so growth
allocates the new buffers, moves the live slots into them with
`ptr_copy`, and frees the old ones. A pool's growth is transactional:
when its slot buffer is refused after its free list was granted, the
free list is freed.

## Tests

- **`tests/run/arenain_poolin.kern`** uses the budget allocator of the
  `VecIn` fixtures, which traces and checks every allocation and free.
  It covers:
  - an arena of 20 values, grown 8 → 16 → 32, read and changed;
  - a pool of 20 with two freed, where the next allocation reuses the
    most recently freed slot (9) at generation 1;
  - after `release`, no live allocation in either;
  - an arena whose budget refuses its second buffer: `Err` at the
    ninth value, that value destroyed once, the first eight intact and
    destroyed at `release`;
  - a pool whose free list is granted and slot buffer refused: the free
    list freed, no live allocation.

  It runs in the interpreter and natively at debug and release.
- **`tests/trap/poolin_stale.kern`**: a handle to a freed and reused
  slot traps `generation`.
- The `Arena`, `Pool`, `VecIn` and `BoxIn` fixtures pass unchanged.

## Found on the way

The two-phase argument gap again. `self.a.alloc(bytes, self.slot_align())`
is `ACC_CONFLICT` (5.4.4), because the call edits `self.a` while its
argument reads all of `self`. The method reads are hoisted into locals.
Field reads such as `self.data` are disjoint from `self.a` and are
accepted.

## Still open

These are recorded in `docs/reports/alloc-containers.md`:
- **More `In` forms:** the `alloc_with` forms and `get2`, and
  allocator-generic `String` and `Map`.
- **`realloc` in the interface:** a new interface method breaks every
  user allocator, which is the owner's decision.
- **Sharing one allocator** between containers.
- **Duplication.** `PoolIn` repeats `Pool`'s slot logic, so a fix to one
  must be made in both.
