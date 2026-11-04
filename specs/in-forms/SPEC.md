# Construct-in-place and pair access for `ArenaIn` and `PoolIn`

**Version:** 0.3 (2026-11-04). **Review status.** speccritic
(gpt-6.1-sol) rounds 1 and 2: VALID_WITH_GAPS, no critical finding.
Round 1 fixes: R4 and R5 exclude the initializer's own code; the
stale-handle case reuses the slot first, since vacancy is checked before
generation. Round 2: both handle checks precede the `disjoint` check.
**Scope:**
- `compiler/resolve/prelude/core.kern` and `std/core.kern` (kept
  identical): methods added to two library structs written in kern;
- fixtures under `tests/run/` and `tests/trap/`;
- `compiler/analysis/try_alloc_test.go`;
- `docs/ownership.md`, the open items of
  `docs/reports/alloc-containers.md` and
  `docs/reports/alloc-containers-more.md`, and a new report
  `docs/reports/in-forms.md`.

**Language impact:** library additions only. No compiler change.
**Follows:** `specs/alloc-containers-more/SPEC.md`, whose §0 lists these
forms as non-goals still open.

**Glossary.**
- R*n*: requirement *n* of §3.
- *The allocator*: the `A: Allocator` value a container holds inline and
  owns.
- *The initializer*: the `init` argument. Its contract is that of
  `Pool.alloc_with` (rule 5.6.2 of the language spec): on `Ok(())` it has
  initialized the whole `T` through its `out` parameter; on `Err(e)` it
  has destroyed what it initialized and the storage is uninitialized.
- *Growth*: the existing `try_grow` of each container, unchanged.
- *IR*: the LLVM intermediate representation `kern build --emit-llvm`
  writes.
- *A stale handle*: a handle to a slot that was freed and then reused,
  so the slot is live with a newer generation than the handle's.

## 0. Goal and non-goals

`Arena` and `Pool` construct a value in its slot (`alloc_with`,
`alloc_with_ctx`), and `Pool` lends two distinct elements at once
(`get2`). `ArenaIn` and `PoolIn` have neither. This adds:

```kern
impl[T, A: Allocator] ArenaIn[T, A] {
    func try_alloc_with[E](edit self, init: func(out T) -> Result[(), E]) -> Result[ArenaId[T], AllocOrInit[E]]
    func try_alloc_with_ctx[C, E](edit self, init: func(out T, view[C]) -> Result[(), E], ctx: view[C]) -> Result[ArenaId[T], AllocOrInit[E]]
}
impl[T, A: Allocator] PoolIn[T, A] {
    func try_alloc_with[E](edit self, init: func(out T) -> Result[(), E]) -> Result[PoolId[T], AllocOrInit[E]]
    func try_alloc_with_ctx[C, E](edit self, init: func(out T, view[C]) -> Result[(), E], ctx: view[C]) -> Result[PoolId[T], AllocOrInit[E]]
    func get2(edit self, a: PoolId[T], b: PoolId[T]) -> (edit[T], edit[T])
}
```

The names carry `try_` because every allocating method of an `In`
container does (`try_alloc`, `try_push`, `try_insert`). `Arena` has no
`get2`, so `ArenaIn` gets none.

Non-goals, still open:
- an allocator-generic `String`;
- sharing `Pool`'s and `PoolIn`'s slot code (the duplication recorded in
  `docs/reports/alloc-containers-more.md`);
- `realloc` in the `Allocator` interface (the owner's decision).

## 1. Behaviour

### 1.1 `ArenaIn.try_alloc_with` and `try_alloc_with_ctx`

1. When the arena is full (`len` equals its capacity), it grows. If the
   growth returns `Err(e)`, the method returns `Err(Alloc(e))`. The
   initializer is not called and the arena is unchanged.
2. The initializer is called once with the next slot's storage as its
   `out` parameter. The `_ctx` form also passes `ctx`, unchanged, as its
   second argument.
3. On `Ok(())` the method returns `Ok(id)`, where `id` is the handle
   `try_alloc` would have returned at that point, and `len` grows by
   one.
4. On `Err(e)` the method returns `Err(Init(e))`. `len` and every
   existing element are unchanged. A growth made in step 1 is kept: the
   capacity is the grown one, and the allocator holds the grown buffer.

### 1.2 `PoolIn.try_alloc_with` and `try_alloc_with_ctx`

1. When the pool has no vacant slot and is full (`count` equals its
   capacity), it grows. If the growth returns `Err(e)`, the method
   returns `Err(Alloc(e))`. The initializer is not called and the pool
   is unchanged.
2. The slot is chosen as `try_alloc` chooses it: the most recently freed
   vacant slot first, otherwise a new slot at index `count`, which then
   counts as a slot of the pool with generation 0.
3. The initializer is called once with that slot's element storage, as
   in §1.1 step 2.
4. On `Ok(())` the slot is live and the method returns `Ok(id)`, with
   the slot's index and generation. `len` grows by one.
5. On `Err(e)` the method returns `Err(Init(e))`. The slot is vacant
   with its generation unchanged and is the next one a vacant-slot
   allocation takes (it is on top of the free list). `len` and every
   live element are unchanged. A growth made in step 1 is kept, as in
   §1.1 step 4.

This is `Pool.alloc_with`'s rule, with `Pool`'s refusal
`Alloc(OutOfMemory)` replaced by the allocator's own error.

### 1.3 `PoolIn.get2`

`get2(a, b)` checks `a`, then `b`, each as `get` checks a handle (bounds,
identity in debug builds, vacancy, generation: the §5.6 order). It
returns edits of the two elements. Both handle checks come first: only
when both pass, and `a` and `b` name the same slot, does it trap
`disjoint`. So a check failure traps with that check's kind before
anything is returned, and one stale handle passed twice traps
`generation`, not `disjoint`.

## 2. Contracts

Each new method carries a `contract` block, as its `Pool` and `Arena`
counterparts do:
- the `_with` forms: the container's existing `requires` text (the
  buffers were allocated from `a` and hold the live elements), followed
  by the initializer's contract from the glossary;
- `get2`: `invariant "a != b is checked at runtime; the two accesses are
  disjoint"`, as on `Pool.get2`.

## 3. Requirements

**R1. Arena forms.** `ArenaIn.try_alloc_with` and `try_alloc_with_ctx`
behave as §1.1 states.

**R2. Pool forms.** `PoolIn.try_alloc_with` and `try_alloc_with_ctx`
behave as §1.2 states.

**R3. `get2`.** `PoolIn.get2` behaves as §1.3 states: for two distinct
live handles it returns two edits, and a write through each must change
its element; the same live handle twice traps `disjoint`; a stale handle
traps `generation`. (A handle to a freed slot that is still vacant traps
`vacant`, as `get` does.)

**R4. Allocation only through the allocator.** Apart from what the
initializer itself does, no new method allocates or frees except through
growth, which already allocates only from the allocator. A fixture over a counting allocator that traps on any free not
matching a live allocation ends with no live allocation after `release`.

**R5. No `oom` trap.** None of the five methods' own code, nor any
library function it calls, reaches an `oom` trap. The initializer is the
caller's code and is excluded: if it traps `oom`, that trap is its own.
`TestTryFormsReachNoOOMTrap` (`compiler/analysis/try_alloc_test.go`)
walks from each of them, as it walks from `Box.try_new_with`; its walk
does not enter calls through function values.

**R6. Engines agree.** Every new run fixture gives the same output in
the interpreter and natively, at debug and release. Every new trap
fixture traps with the same kind in each engine.

**R7. Nothing else changes.**
- The existing `Arena`, `Pool`, `ArenaIn`, `PoolIn` and `alloc_with`
  fixtures pass unchanged.
- For every program under `tests/run`, `tests/trap`, `tests/positive`
  and `bench/kern` that builds before this change
  (`scripts/capture-ll.sh`), `kern build` at debug and
  release gives byte-identical IR (`--emit-llvm`) before and after it. Generic methods
  are emitted only when instantiated, so a program that calls none of
  the new methods emits none of them.

## 4. Tests

- `tests/run/in_alloc_with.kern` (R1, R2, R4, R6), over the counting
  `Budget` allocator of `tests/run/arenain_poolin.kern`:
  - each of the four `_with` forms succeeds across a growth, and the
    values read back;
  - a failing initializer gives `Init`; for the pool, the next
    allocation reuses that slot at the same generation;
  - a budget refused at growth gives `Alloc(OutOfMemory)` and the
    initializer prints nothing (it is not called);
  - after `release`, the budget reports nothing live.
- `tests/run/poolin_get2.kern` (R3): swaps two elements through `get2`
  and prints both.
- `tests/trap/poolin_get2_same.kern`: the same handle twice traps
  `disjoint`.
- `tests/trap/poolin_get2_stale.kern`: a handle whose slot was freed
  and then reused by `try_alloc` traps `generation`.
- `compiler/analysis/try_alloc_test.go` (R5): the five methods are
  added to the forms it walks, and a mutation (one growth `Err` turned
  into `trap_kind(oom)`) is shown to fail it.
- The IR comparison of R7, from a worktree at the commit before.

## 5. Documents

- `docs/ownership.md`: the `In` containers paragraph names the new
  forms.
- `docs/reports/alloc-containers.md` and
  `docs/reports/alloc-containers-more.md`: the open item points to the
  new report.
- `docs/reports/in-forms.md`: what exists, the fixtures and the R7
  result.
- `docs/unsafe-audit.md`: a row for each new function with an `unsafe`
  block, and the function and block counts updated.
