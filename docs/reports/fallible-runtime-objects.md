# Fallible runtime objects

**Date:** 2026-11-04. **Spec:** `specs/fallible-runtime-objects/SPEC.md`
v0.2. speccritic's first round had two critical findings, both
resolved: success is now conditioned on the allocation, and the
destructor's own traps are excluded from the guarantee. **Plan:**
`specs/fallible-runtime-objects/PLAN.md`. **Follows:** the open item
"Runtime objects ... fallible forms are a runtime ABI change" of
`docs/reports/fallible-alloc.md`.

## API

```kern
impl Shared[T] { func try_new(v: own T) -> Result[Shared[T], AllocErr] }
impl Mutex[T]  { func try_new(v: own T) -> Result[Mutex[T], AllocErr] }
impl Condvar   { func try_new() -> Result[Condvar, AllocErr] }
```

When the runtime cannot allocate the object, each returns
`Err(OutOfMemory)` instead of trapping `oom`. `Shared` and `Mutex` first
destroy the value they were given, which no object took.

## How it works

- **Native.**
  - `kern_shared_try_new`, `kern_mutex_try_new` and
    `kern_condvar_try_new` allocate with `kern_try_alloc`, which never
    traps; the `testing.fail_next_alloc` hook still fails it.
  - They write the `Result` into the caller's buffer, as
    `SystemAllocator.alloc` does.
  - On failure, the value the backend spilled is destroyed through the
    drop glue it passes (`glueFor(T)`).
- **Interpreter.** With the hook armed, the intrinsic calls
  `it.destroy(v, T)` and returns `Err(OutOfMemory)`.
- **Effects.** The three are allocations in the allocation table, as
  `new` is, and list no trap kind.

## Tests

- **`tests/run/runtime_try_new.kern`** checks that the objects work: a
  `Shared` cloned and read, a `Mutex` locked twice, a `Condvar`
  notified. It runs in the interpreter and natively, at debug and
  release.
- **`tests/run/runtime_try_new_oom.kern`** (`test-hooks`) arms the hook
  before each form.
  - Each returns `Err(OutOfMemory)`, and each value's destructor prints
    once before the result.
  - A later `try_new` succeeds, and its value is destroyed when the
    object is dropped.
  - It runs in the interpreter and natively at debug.
- **`compiler/analysis`:**
  - `TestTrapKindsPerSource` gains three rows, each with no kinds;
  - `TestRuntimeTryNewAllocates` checks that each form allocates and
    does not trap;
  - `TestHostedOnlyRuntimeTryNew` checks the freestanding rejection
    (`ENV_HOSTED_ONLY`).
- **Unchanged.** The five `oom` trap fixtures (`Shared.new`,
  `Mutex.new`, `Condvar.new`, `spawn`, scope and scope spawn) pass
  unchanged.

## Decisions and limits

- **Spawns are unchanged.** `thread.spawn`, `Scope.spawn` and
  `thread.scope` still trap `oom` when the runtime cannot allocate their
  blocks, as their fixtures pin. A spawn's `Err(ResourceExhausted)`
  reports a thread that could not be created. Folding allocation failure
  into it would be a separate decision.
- **The destructor's effects aren't attributed.** The moved value's
  destructor runs inside the runtime call on failure, as a `Shared`'s
  contents are destroyed by its drop today. Its effects are not
  attributed to the caller.
