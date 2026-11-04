# Containers over a caller's allocator: `ArenaIn` and `PoolIn`

**Version:** 0.2 (2026-11-04). **Review status.** speccritic
(gpt-6.1-sol) round 1: one critical finding, resolved (a refusal
restores the allocator's state, so the zero-allocation claim is limited
to an empty pool). Size overflow, the destructor and generation
exhaustion are now stated. **Scope:**
- `compiler/resolve/prelude/core.kern` and `std/core.kern` (identical):
  two library structs written in kern;
- fixtures under `tests/run/` and `tests/trap/`;
- the open items of `docs/reports/alloc-containers.md`;
- the report `docs/reports/alloc-containers-more.md`.

**Language impact:** library additions only. **Follows:**
`specs/alloc-containers/SPEC.md` (`BoxIn` and `VecIn`), whose report
lists `PoolIn` and `ArenaIn` as open.

**Glossary.**
- R*n*: requirement *n* of §3.
- *The allocator*: the `A: Allocator` value a container holds inline and
  owns.
- *A handle*: an `ArenaId[T]` or `PoolId[T]`, the same types `Arena` and
  `Pool` issue.

## 0. Goal and non-goals

A program that manages memory through its own allocator must be able to
use an arena and a pool of handles over that allocator, as it can
already use a box and a vector:

```kern
impl[T, A: Allocator] ArenaIn[T, A] {
    func new_in(a: own A) -> ArenaIn[T, A]
    func try_alloc(edit self, v: own T) -> Result[ArenaId[T], AllocErr]
    func get(view self, id: ArenaId[T]) -> view[T]
    func at(edit self, id: ArenaId[T]) -> edit[T]
    func len(view self) -> usize
    func allocator(view self) -> view[A]
    func release(edit self)
}
impl[T, A: Allocator] PoolIn[T, A] {
    func new_in(a: own A) -> PoolIn[T, A]
    func try_alloc(edit self, v: own T) -> Result[PoolId[T], AllocErr]
    func get(view self, id: PoolId[T]) -> view[T]
    func at(edit self, id: PoolId[T]) -> edit[T]
    func free(edit self, id: PoolId[T])
    func len(view self) -> usize
    func allocator(view self) -> view[A]
    func release(edit self)
}
```

Non-goals, recorded as still open:
- the `alloc_with` and `alloc_with_ctx` forms, and `get2`;
- an allocator-generic `String` or `Map`;
- `realloc` in the `Allocator` interface: adding a method to an
  interface breaks every user allocator, so that is the owner's
  decision;
- sharing one allocator between containers through a library type.

## 1. Background

`Arena` and `Pool` allocate through the system allocator with
`heap_try_alloc` and `heap_try_realloc`, and their trapping `alloc`
forms return `Result` already. `VecIn` shows the pattern for a caller's
allocator: hold it, allocate the new buffer through it, move the live
elements, and free the old buffer, since the interface has no `realloc`.

## 2. Design (informative)

`ArenaIn` and `PoolIn` are copies of `Arena` and `Pool` with the
allocator as a field. Growth goes through it (`try_grow`), the
operations are fallible, and the handle checks are the same:
- an arena handle is checked for bounds, then identity (debug);
- a pool handle is checked for bounds, then identity (debug), vacancy
  and generation.

A pool's growth is transactional. When the slot buffer is refused after
the free list was granted, the free list is freed and nothing changes.

## 3. Requirements and acceptance criteria

**R1. Allocation through the allocator only.** Fixture
`tests/run/arenain_poolin.kern` uses a budget allocator that traces and
checks every allocation and free. It checks that:
- an `ArenaIn` of 20 elements and a `PoolIn` of 20 elements (two freed,
  one reallocated) end with no live allocation after `release`;
- every free names a live allocation with its own size and alignment;
- the stored values read back.

**R2. Pool semantics.** A freed slot is reused before a new one, the
most recently freed first, with its generation increased by one. A stale
handle traps `generation` (`tests/trap/poolin_stale.kern`). The arena
and pool handle checks trap as `Arena`'s and `Pool`'s do.

**R3. Fallibility.**
- When the allocator refuses, `try_alloc` returns its error, the value
  given is destroyed exactly once, and the container is unchanged:
  every value stored before reads back and is destroyed at `release`.
- A refused growth leaves the allocator's live allocations as they were
  before the call. In particular, an empty `PoolIn` whose slot buffer is
  refused after its free list was granted frees the free list, and an
  allocator with no other allocation ends with none.
- A growth whose new capacity or byte size cannot be represented
  (capacity past 2^31 slots, or a byte count past `usize`) returns
  `Err(OutOfMemory)` without calling the allocator, with the same
  effect as a refusal.

These are checked by the same fixture, in the interpreter and natively
at debug and release.

**R3a. Lifecycle.** The destructor is `release`: the live values are
destroyed (last first), the buffers freed through the allocator, and
then the allocator itself, as the struct's last field, is destroyed.
A `PoolIn` slot whose generation would pass `u32` maximum is retired
instead of reused, as in `Pool`, so a stale handle can never revive.

**R4. Nothing else changes.** `Arena`, `Pool`, `VecIn` and `BoxIn` and
their fixtures are unchanged, and `std/core.kern` stays identical to the
prelude.

**R5. Documents.** The alloc-containers report's open items point here,
and the new report lists the API, the fixtures and the items still open.
