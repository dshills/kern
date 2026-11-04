# Fallible runtime objects: `Shared`, `Mutex` and `Condvar` try forms

**Version:** 0.2 (2026-11-04). **Review status.** speccritic
(gpt-6.1-sol) round 1: two critical findings, both resolved. Success
is now conditioned on the allocation and failure covers real
exhaustion, and the destructor's own traps are excluded from the
continue guarantee and from R4. **Scope:**
- `compiler/resolve/prelude/core.kern` (and `std/core.kern` if it
  mirrors it): three declarations;
- the hosted runtime (`runtime/hosted/src/kern_rt.c`), the LLVM backend
  (`compiler/backend/llvm/calls.go`) and the interpreter
  (`compiler/interp/intrinsics.go`): three intrinsics;
- the effect tables (`compiler/analysis/effects.go`, `analysis.go`);
- fixtures under `tests/run/`;
- the open items of `docs/reports/fallible-alloc.md` and
  `docs/reports/effect-trap-kinds.md`;
- the report `docs/reports/fallible-runtime-objects.md`.

**Language impact:** three library functions; no grammar or rule
change. **Follows:** `specs/fallible-alloc/SPEC.md` (the `try_` forms of
`Box`, `Vec` and `String`, and `AllocErr`), whose report lists "fallible
forms are a runtime ABI change" as open.

**Glossary.**
- R*n*: requirement *n* of §3.
- *The hook*: `testing.fail_next_alloc()`, under `--test-hooks`, which
  makes the next allocation fail. In the native runtime that is the next
  runtime allocation (`kern_fail_next`); in the interpreter, the next
  allocation it models.
- *Destroyed*: run through the value's drop glue, as the end of its
  scope would (user destructors included).

## 0. Goal and non-goals

A program must be able to create a `Shared`, a `Mutex` or a `Condvar`
without trapping when memory runs out:

```kern
impl Shared[T] { func try_new(v: own T) -> Result[Shared[T], AllocErr] }
impl Mutex[T]  { func try_new(v: own T) -> Result[Mutex[T], AllocErr] }
impl Condvar   { func try_new() -> Result[Condvar, AllocErr] }
```

On failure the value given is destroyed, as `Box.try_new` does.

Non-goals:
- **Spawns and scopes.** `thread.spawn`, `Scope.spawn` and
  `thread.scope` keep trapping `oom` when the runtime cannot allocate
  their blocks, as `tests/trap/oom_spawn.kern`, `oom_scope_spawn.kern`
  and `oom_scope.kern` pin. A spawn's `Result` reports a thread that
  could not be created, and folding allocation failure into it is a
  separate decision.
- **The `new` forms.** `Shared.new`, `Mutex.new` and `Condvar.new` are
  unchanged and still trap `oom`.
- **Freestanding.** These are hosted-only, like the `new` forms: the
  freestanding runtime has no `Shared`, `Mutex` or `Condvar`.

## 1. Background

The three `new` forms are intrinsics. The runtime allocates the object
with `kern_alloc`, which traps `oom`, and the interpreter's
`runtimeAlloc` traps the same way. `SystemAllocator.alloc` is the model
for an intrinsic that returns a `Result`: the runtime writes the result
into an out buffer (`out[0]` is the tag, `1` for `Err(OutOfMemory)`),
and the backend loads it (`unionOut`, `loadResult`).

## 2. Design (informative)

- **Runtime.** `kern_shared_try_new(size, src, glue, out, off, at)`,
  `kern_mutex_try_new(...)` and `kern_condvar_try_new(out, off)` work as
  follows:
  - they allocate without trapping, and the armed hook makes the
    allocation fail;
  - on success they build the object as `new` does and write `Ok`;
  - on failure they write `Err(OutOfMemory)`, and for `Shared` and
    `Mutex` they call `glue(src)` (when it is not null) so the moved
    value is destroyed.
- **Backend.** The value is spilled as `new` spills it and passed with
  its drop glue (`glueFor(T)`, `null` for a type that needs none).
- **Interpreter.** When the hook is armed, it destroys the value with
  `it.destroy(v, T)` and returns `Err(OutOfMemory)`. Otherwise it returns
  `Ok` of the object `new` makes.
- **Effects.** A `try_new` lists no `oom` trap kind, as no `try_` form
  does (`specs/effect-trap-kinds`). It reports an allocation where `new`
  does.

## 3. Requirements and acceptance criteria

**R1. Success.** When the runtime's allocation succeeds, each `try_new`
returns `Ok` of an object that behaves exactly as one from `new`: `Shared.get`, `clone`
and drop; `Mutex.lock` and `unlock`; `Condvar.wait` and `notify_one`.
Fixture `tests/run/runtime_try_new.kern`.

**R2. Failure.** When the runtime's allocation fails, because memory is
exhausted or the hook is armed, each `try_new` returns
`Err(OutOfMemory)` and the program continues. The tests use the hook,
since real exhaustion cannot be produced reliably. For `Shared` and `Mutex`,
the moved value is destroyed exactly once: its destructor prints once.
The destructor is user code, and what it does is its own: it may trap
(even `oom`, through a `new` form), and then the program traps there, as
dropping the value anywhere else would. The guarantee to continue covers
`try_new` itself, not the code its destruction runs.
The next allocation succeeds again. The fixture
`tests/run/runtime_try_new_oom.kern` (`test-hooks: true`) checks this in
the interpreter and natively.

**R3. No change elsewhere.** The `new` forms and the spawn and scope
fixtures behave as before: `tests/trap/oom_shared_new.kern`,
`oom_condvar_new.kern`, `oom_spawn.kern`, `oom_scope_spawn.kern` and
`oom_scope.kern` pass unchanged.

**R4. Effects.** `show-effects` reports a `try_new` as an allocation, as
it reports `new`. It lists no `oom` kind for a function whose only
allocation is a `try_new`, and lists `oom` for one calling `new`. The
destruction of the moved value on failure runs inside the intrinsic,
like a `Shared`'s contents destroyed by its drop today, and its effects
are not attributed to the caller. It lists `oom` for one calling `new`. A
test in `compiler/analysis` checks both.

**R5. Freestanding.** A freestanding program calling a `try_new` is
rejected with `ENV_HOSTED_ONLY`, as one calling `new` is.

**R6. Documents.** The two reports' open items point here, and the new
report records the API, the fixtures and the spawn decision.
