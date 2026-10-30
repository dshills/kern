# In-place initializers with a context argument

**Date:** 2026-10-30. **Spec:** `specs/init-context/SPEC.md` v0.1.
**Plan:** `specs/init-context/PLAN.md`. **Decides:** the open question of
`docs/reports/in-place-init.md`. The owner chose an explicit context
argument, added beside the fixed `_with` methods, over `out`
parameters on closures.

## Before

`Box.new_with`, `Pool.alloc_with` and `Arena.alloc_with` take an
initializer `func(out T) -> Result[(), E]`. A function value captures
nothing, and a closure cannot take an `out` parameter, so per-call data
could not reach an initializer except through a constant or a
thread-local. The adversarial notes' first draft,
`alloc_with(init_page, id)`, was not expressible.

## What changed

- **Three methods.** `Box[T].new_with_ctx(init, ctx)`,
  `Pool[T].alloc_with_ctx(init, ctx)` and
  `Arena[T].alloc_with_ctx(init, ctx)` take
  `init: func(out T, view[C]) -> Result[(), E]` and `ctx: view[C]`. Each
  behaves exactly as its `_with` form and passes `ctx` to `init`
  unchanged. They are kern code in the core prelude, copies of the
  `_with` methods with one call changed.
- **One unsafe primitive.** `ptr_init_ctx(p, init, ctx)` is `ptr_init`
  with the context as the initializer's second argument. In the
  interpreter it passes the caller's access on. Natively it is the same
  indirect call with the context rendered exactly as a call's view
  argument. The effect analysis treats it as `ptr_init`, a call through
  a function value with label `unknown`.
- **The context is an ordinary view argument.** A context rooted in the
  pool or arena being edited is `ACC_CONFLICT`, the same code a
  user-written function with the same conventions gets. A temporary
  cannot be passed, since a view needs a place, so bind it first.
  Several values travel in one struct.

```kern
struct Spec { id: u64 }
func init_page(p: out Page, s: view[Spec]) -> Result[(), IoErr] {
    p.id = s.id
    p.data = zero()
    return Ok(())
}
id := pool.alloc_with_ctx(init_page, spec)?
```

## Verification

- **Run fixtures** in both engines, debug and release:
  - `box_new_with_ctx`: a 4 KiB page from a struct context, and a
    partial failure.
  - `pool_alloc_with_ctx`: two contexts, a failure that leaves `len()`
    unchanged and its slot reused, and the contexts unchanged afterward.
  - `arena_alloc_with_ctx`: a scalar `view[u64]` context.
  - `pool_alloc_with_ctx_oom`: test hooks, debug only, as its `_with`
    twin. The initializer never runs when the container cannot grow.
- **Negative fixtures, each with an accepted twin:**
  - `ptr_init_ctx` outside `unsafe`.
  - A pool passed as its own context.
  - The same call shape through a user function, which gets the same
    `ACC_CONFLICT`.
- **Analysis test.** `show-copies` reports no copy of a 4,096-byte
  `Page` built by each method, in both opt levels. `show-effects` labels
  a function calling `ptr_init_ctx` `unknown`.
- **Unchanged programs (R8).** The fixture harness's 236 programs and
  configurations that built before emit byte-identical LLVM with the new
  compiler. `scripts/capture-ll.sh` compared a compiler built at ed9e10a
  with this change. The only new build failure is the
  out-of-memory fixture, which needs test hooks as `pool_alloc_with_oom`
  does.

## Reviews

- **speccritic:** VALID_WITH_GAPS with no critical finding. The one
  substantive warning, that a test-hook fixture cannot run in release,
  is folded in.
- **plancritic:** EXECUTABLE_AS_IS, score 100, every requirement
  covered.
- **prism:** no finding.
- **realitycheck:** PARTIALLY_ALIGNED, score 100, no violations.
- **verifier:** 4 recommendations. They are package-level "no test
  files" and one exported name, which the single-file include pattern
  produces. The packages' tests cover the change.

## Not done

- **Other context conventions.** There is no `edit` or `own` context,
  by the spec's non-goals. To put an owned value such as a `Vec` into a
  new object, initialize the field empty and assign it through the
  returned id.
- **Benchmarks.** `bench/kern/btree` still builds its nodes by value.
  Benchmark programs are not rewritten, and its nodes need no context.
