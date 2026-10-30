# In-place initializers: `Box.new_with`, `Pool.alloc_with`, `Arena.alloc_with`

**Date:** 2026-10-30. **Spec:** `specs/in-place-init/SPEC.md` v0.1.
**Plan:** `specs/in-place-init/PLAN.md`. **Item:** the eighth of the
ten-item list ("the in-place initializers, fixed in the plan since Phase
1, still absent; they need `out` storage over raw memory, which is
compiler work before library work").

## Before

Spec §5.2 lets `out` storage be "an uninitialized local, or a fresh slot
provided by a container", and names `Box.new_with(init: func(out T) ->
Result[(), E])`; PLAN.md Phase 1 fixed `alloc_with(edit self, init:
func(out T) -> Result[(), E]) -> Result[Id, AllocOrInit[E]]` and declared
`AllocOrInit`. Only the local half existed: nothing in the library could
hand a container's raw slot to an initializer as its `out` parameter, so
none of the methods existed and the adversarial case "large-object
construction without copies" could be met only with a local.

## The compiler piece

One unsafe intrinsic in the prelude:

```kern
func ptr_init[T, E](p: RawPtr[T], init: func(out T) -> Result[(), E]) -> Result[(), E]
```

It calls `init` with the storage at `p` as its `out` parameter. Natively
an `out` parameter is a pointer, so the backend emits an indirect call
through `init` with `p` itself; the interpreter opens an edit access on
the raw storage, as `ptr_edit` does, and calls the function value with
it. Its name begins with `ptr_`, so the resolver's existing rule makes
it an unsafe operation (`tests/negative/ptr_init_outside_unsafe`), and
`show-effects` labels a caller `unknown`, as for any call through a
function value. Its obligation: `p` addresses storage for one `T` that
holds no live value. On `Err`, the initializer's own failure rule
(ADR-005) has already destroyed what it had built, so the caller only
has to dispose of the storage.

## The library

All three are kern code in the prelude, each with a contract:

- `Box[T].new_with(init) -> Result[Box[T], E]`: allocate (a failed
  allocation traps `oom`, as `Box.new`), initialize in place, return the
  box; on `Err` free the allocation.
- `Pool[T].alloc_with(init) -> Result[PoolId[T], AllocOrInit[E]]`: take a
  slot as `alloc` does, leave its meta vacant until `init` succeeds, then
  mark it live; on `Err` push the index back on the free list, its
  generation unchanged; `Alloc(OutOfMemory)` before `init` runs when the
  buffers cannot grow.
- `Arena[T].alloc_with(init) -> Result[ArenaId[T], AllocOrInit[E]]`: grow,
  initialize the next slot, advance `size` only on `Ok`.

`E` is inferred from the initializer's type at every call in the
fixtures; no explicit type argument was needed.

## A defect the fixtures found

A function that initializes every field of its `out` parameter and then
returns an error must destroy the whole value by §5.7 (spec §5.2). The
lowering emitted that drop, but the IR validator, which sees an `out`
parameter's fields stored one by one and never the whole place
initialized, rejected it: "drop of %c which is uninitialized", an
internal error on any such program, with or without the new methods
(reproduced with the compiler at 5a6114d). The lowering now emits an
`init` of the parameter, which has no runtime effect, before that drop.
`tests/run/out_whole_then_err` (an ordinary local caller) prints the
type's destructor, then the field's, in all four configurations.

## Measured

**Fixtures** (all four configurations unless noted; the harness compares
output, status and trap record between the engines):

| Fixture | What it shows |
|---|---|
| `run/box_new_with` | success read back; partial failure destroys the field once and not `Page`'s destructor; whole-then-fail destroys `Page` once (its destructor, then the field) |
| `trap/box_new_with_trap` | a bounds trap inside an initializer is reported at the initializer's own span |
| `run/pool_alloc_with` | partial failure: field destroyed, `Node`'s destructor not run, `len()` unchanged, the slot reused by the next allocation; a freed slot reused by `alloc_with` |
| `trap/pool_alloc_with_stale` | after free and reuse by `alloc_with`, the old handle traps `generation` |
| `run/arena_alloc_with` | a failed initializer leaves the arena as it was; the next allocation takes the index |
| `run/pool_alloc_with_oom` | test hooks, debug, both engines: `Alloc(OutOfMemory)` from both containers and the initializer never runs |
| `run/out_whole_then_err` | the defect above |
| `adversarial/large_object_out` | the 4,096-byte `Page` built by `Box.new_with` and `Pool.alloc_with` |

**No copy (R6).** An analysis test lowers `large_object_out` with a
`main` reaching every path, in both opt levels: `show-copies` reports no
copy whose type is `Page` (the only copy in the file is the original
local path's `[4096]u8` array, which this change does not touch), and
`show-effects` labels `main` `unknown` with every effect.

**Programs that do not use them (R8).** `scripts/capture-ll.sh` with a
compiler built from a clean worktree at 5a6114d and with this change:
all 222 modules the baseline built are byte-identical. The baseline
could not build this change's six new fixtures (they use the new
methods); the new compiler builds them in both opt levels, and the only
programs it cannot build are the twelve that failed before and
`pool_alloc_with_oom`, which needs test hooks, like `trap/oom`.

## Open question for the owner

An initializer is a function value, which cannot capture, and a closure
parameter cannot be `out` (`docs/reports/closures.md`). So per-call data
(the `id` of the adversarial notes' earlier draft, a buffer to read
from) cannot reach an initializer except through a thread-local or a
constant. Two ways forward, both language decisions: allow `out`
parameters on closures, which needs the checker's `INIT_OUT_NOT_SET` and
failure-rule analysis inside closure bodies; or give the methods an
explicit context argument, `init: func(out T, view[C]) -> Result[(),
E]`, which changes the fixed signature. Neither is done here.

*Decided 2026-10-30:* the owner chose the context argument, added beside
the fixed methods as `new_with_ctx` and `alloc_with_ctx`
(`specs/init-context/SPEC.md`, `docs/reports/init-context.md`).

## Review

- **speccritic** (gpt-6.1-sol, profile general, ADR-005 as context):
  VALID_WITH_GAPS on the first run, no critical finding; two
  clarifications folded in (capacity grown before a failed initializer
  is kept; the methods being kern code is a project constraint). R10 was
  added afterwards for the defect above.
- **plancritic** (davin-go, with the spec): EXECUTABLE_AS_IS, 100, every
  requirement covered.
- **prism** (gpt-6.1-sol, staged diff): no finding on the first run.
- **realitycheck**: PARTIALLY_ALIGNED, score 100, no violation (the
  partial statuses are the tool's truncated code index).
- **verifier** (offline): 49 recommendations, all "exported symbol has no
  corresponding test" by name for functions that predate this change
  (`Copies`, `Effects`, `Emit`, `Lower`, ...); the change itself adds
  no exported Go function.
- `golangci-lint run ./...` 0 issues; `go test ./...` green.

## Not done

- Initializers with context (above): done since in `docs/reports/init-context.md`.
- `bench/kern/btree` still builds `Node.empty()` by value and moves it
  into the pool slot; `docs/workloads/btree.md` suggests `alloc_with`,
  but benchmark programs are not rewritten by this change.
