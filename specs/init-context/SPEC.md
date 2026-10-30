# In-place initializers with a context argument

**Version:** 0.1 (2026-10-30). **Scope:** one unsafe library intrinsic
(`ptr_init_ctx`) in the core prelude and its implementation in the
interpreter (`compiler/interp`), the LLVM backend
(`compiler/backend/llvm`) and the effect analysis
(`compiler/analysis`); three library methods in the core prelude
(`compiler/resolve/prelude/core.kern`, mirrored in `std/core.kern`);
fixtures under `tests/`; the documents that describe in-place
construction. **Language impact:** none to the grammar, the checker's
rules or the §16 schemas; the prelude gains the four declarations of
§2. **Decides:** the open question of `docs/reports/in-place-init.md`
("initializers with context"), as the owner chose on 2026-10-30: an
explicit context argument, added beside the fixed `_with` methods, not
`out` parameters on closures. **Related:**
`specs/in-place-init/SPEC.md` (the `_with` methods and `ptr_init`,
whose rules this spec reuses); `specs/initial/AI_Native_Systems_Language_SPEC.md`
§5.2 (the `out` convention and its failure rule), §5.4 (access
conflicts at a call); `docs/decisions/ADR-010-function-pointers-and-closures.md`
invariant 2 (callbacks are a function paired with a context);
`docs/decisions/ADR-005-out-convention.md`.

**Review status.** speccritic (OpenAI gpt-6.1-sol, profile general,
with `specs/in-place-init/SPEC.md` as context): VALID_WITH_GAPS, no
critical finding; its one substantive warning (a test-hook fixture
cannot run in release) is folded into R7 and §4. The rest are the
preflight's flags on code identifiers and the goal's prose.

**Glossary.** R*n*: requirement *n* of §3. Initializer: a function value
of type `func(out T, view[C]) -> Result[(), E]` for this spec, or
`func(out T) -> Result[(), E]` for the `_with` methods. Context: the
`view[C]` argument passed to a `_with_ctx` method and handed unchanged
to the initializer. Slot, in place, engine, opt level, out failure rule
and test hooks: as `specs/in-place-init/SPEC.md` defines them. Engine
pair: the interpreter and the native build of one opt level.

## 0. Goals and non-goals

An initializer can read per-call data: an id, a size, a configuration,
a buffer to copy from. Today it cannot, because it is a function value
that captures nothing and a closure cannot take an `out` parameter. The
data travels as a `view[C]` argument next to the initializer, the
function-plus-context pattern ADR-010 already names for callbacks.

Non-goals: an `edit` or `own` context; more than one context argument
(a struct holds several values); `out` parameters on closures; any
change to `new_with`, `alloc_with` or `ptr_init`; rewriting benchmark
programs to use the new methods.

## 1. Background

`Box.new_with`, `Pool.alloc_with` and `Arena.alloc_with` take
`init: func(out T) -> Result[(), E]` and are written in kern over the
unsafe intrinsic `ptr_init(p, init)`, which calls `init` with the raw
storage at `p` as its `out` parameter. Their signatures were fixed by
the plan's Phase 1 and stay as they are.

## 2. Declarations

The prelude gains, in `compiler/resolve/prelude/core.kern` and
identically in `std/core.kern`:

```kern
func ptr_init_ctx[T, C, E](p: RawPtr[T], init: func(out T, view[C]) -> Result[(), E], ctx: view[C]) -> Result[(), E]
// in impl Box[T]
func new_with_ctx[C, E](init: func(out T, view[C]) -> Result[(), E], ctx: view[C]) -> Result[Box[T], E]
// in impl Pool[T]
func alloc_with_ctx[C, E](edit self, init: func(out T, view[C]) -> Result[(), E], ctx: view[C]) -> Result[PoolId[T], AllocOrInit[E]]
// in impl Arena[T]
func alloc_with_ctx[C, E](edit self, init: func(out T, view[C]) -> Result[(), E], ctx: view[C]) -> Result[ArenaId[T], AllocOrInit[E]]
```

The initializer comes before its context, as `thread.spawn(f, args...)`
puts the function first.

## 3. Requirements

**R1 — The intrinsic.** `ptr_init_ctx(p, init, ctx)` is `ptr_init(p,
init)` with `ctx` passed as `init`'s second argument, unchanged: the
same access the caller passed, not a copy. Its preconditions, its result
and the state of the storage after Ok and after Err are those of
`ptr_init`. It is unsafe by the `ptr_` name rule: a call outside an
`unsafe` block is rejected as a call to `ptr_init` is.

**R2 — `Box.new_with_ctx`.** Behaves as `Box.new_with` in every
respect (allocation, `oom` trap on a failed allocation, the result, the
freed allocation and returned `Err(e)` when the initializer fails),
with the initializer called with the context.

**R3 — `Pool.alloc_with_ctx`.** Behaves as `Pool.alloc_with` in every
respect: the slot is obtained as `alloc` obtains it;
`Err(Alloc(OutOfMemory))` when the buffers cannot grow, before the
initializer runs; on `Err(Init(e))` the slot is back on the free list,
vacant, its generation unchanged, and `len()` is unchanged; on Ok the
slot is live and the returned id addresses it.

**R4 — `Arena.alloc_with_ctx`.** Behaves as `Arena.alloc_with` in every
respect: `Err(Alloc(OutOfMemory))` before the initializer runs when the
arena cannot grow; on `Err(Init(e))` the arena's length is as it was;
on Ok the value is the arena's last element.

**R5 — The context is an ordinary view argument.** At a call of a
`_with_ctx` method, the context is a view access live until the call
returns and conflicts with the call's other arguments under §5.4 like
any argument: a context rooted in the pool or arena the call edits is
rejected with the same diagnostic code a user-written function of the
same parameter conventions gets for the same arguments. Inside the
initializer the context is read-only, as any `view[C]` parameter is.

**R6 — In place.** For a `T` of 4,096 bytes or more, `show-copies` on a
program that builds it with each of the three methods reports no copy
whose type is `T`, in both opt levels.

**R7 — The engines agree.** Every fixture of §4 gives the same output
(or the same trap record, under the harness's comparison rules) in the
interpreter and natively, in debug and in release; a fixture that needs
test hooks, which exist only in debug builds, runs in both engines in
debug only, as the existing `pool_alloc_with_oom` does.

**R8 — Programs that do not use them are unchanged.** For every program
the fixture harness builds that does not call a `_with_ctx` method or
`ptr_init_ctx`, the emitted LLVM is byte-identical to the compiler's
output before this change, checked with `scripts/capture-ll.sh` against
a compiler built from the commit before it.

**R9 — Effects.** `show-effects` treats a call of `ptr_init_ctx` as it
treats `ptr_init`: a call through a function value, every effect `true`
with label `unknown`.

**R10 — Documents.** `docs/ownership.md` (construction in place) names
the three methods and the context rule; `tests/adversarial/NOTES.md`
§9 and `docs/reports/in-place-init.md` record that per-call data now
reaches an initializer through the context; ADR-005 and ADR-010 cite
this spec where they cite the `_with` methods.

## 4. Acceptance criteria

1. Run fixtures, each in both engines and both opt levels (R2–R4, R7):
   - `box_new_with_ctx`: a boxed value whose fields come from the
     context; an initializer that fails after setting an owned field,
     whose destructor runs once and whose value's destructor does not.
   - `pool_alloc_with_ctx`: two contexts give two slots with their
     values; a failing initializer leaves `len()` unchanged and its
     slot is reused by the next allocation; the context is read after
     the calls and is unchanged.
   - `arena_alloc_with_ctx`: as the pool fixture, with the arena's
     length.
   - `pool_alloc_with_ctx_oom` (test hooks, debug only, both engines): `testing.fail_next_alloc()`
     before an allocation that must grow gives `Err(Alloc(OutOfMemory))`
     and the initializer does not run (it prints nothing).
2. Negative fixtures (R1, R5): `ptr_init_ctx` called outside `unsafe`;
   a pool's `alloc_with_ctx` passed a context rooted in that pool; and,
   for R5's "same code" clause, a user function with the same parameter
   conventions called the same way, expecting the same code.
3. An analysis test (R6) on a 4,096-byte `Page` built by each method,
   in both opt levels.
4. An analysis test (R9) that `show-effects` labels a function calling
   `ptr_init_ctx` `unknown`.
5. The R8 comparison recorded in the report: module count and the
   result.
