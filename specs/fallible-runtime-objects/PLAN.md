# Plan: fallible runtime objects

Implements `specs/fallible-runtime-objects/SPEC.md` v0.2 in one phase and
one commit.

## Phase 1

**Goals.** R1 to R6.

**Files.**
- `compiler/resolve/prelude/core.kern` and `std/core.kern` (identical):
  `Shared.try_new`, `Mutex.try_new` and `Condvar.try_new`, declared
  bodiless beside the `new` forms, so they are intrinsics with those
  names.
- `runtime/hosted/src/kern_rt.c`:
  - `kern_try_alloc` is `malloc` behind the armed hook, never trapping.
  - `kern_try_failed` calls the drop glue on the spilled value and
    writes `Err(OutOfMemory)` (`out[0] = 1`).
  - `kern_shared_try_new`, `kern_mutex_try_new` and
    `kern_condvar_try_new` build the object as `new` does and write `Ok`
    and the handle at the payload offset.
- `compiler/backend/llvm/calls.go`: the three intrinsics.
  - `unionOut` and `loadResult` handle the `Result`, as for
    `SystemAllocator.alloc`.
  - The value is spilled as `new` spills it, and passed with
    `glueFor(T)` (`null` when `T` needs no destruction).
- `compiler/interp/intrinsics.go`: the three intrinsics. With the hook
  armed they call `it.destroy(v, T)` and return `Err(OutOfMemory)`;
  otherwise they return `Ok` of the object `new` makes.
- `compiler/analysis/analysis.go`: the three are in the `allocating`
  table. They are not in the `trapping` table, so they list no `oom`.
- Tests:
  - fixtures `tests/run/runtime_try_new.kern` (R1) and
    `runtime_try_new_oom.kern` (R2, `test-hooks`), with `.out`;
  - `compiler/analysis`: rows in `TestTrapKindsPerSource` (no kinds),
    `TestRuntimeTryNewAllocates` (allocates, no trap) and
    `TestHostedOnlyRuntimeTryNew` (R5);
  - the generated hosted-only inventory regenerated, because the corpus
    gains the fixtures.
- Documents: `docs/reports/fallible-runtime-objects.md`, and notes in
  the open items of `docs/reports/fallible-alloc.md` and
  `docs/reports/effect-trap-kinds.md`.

**Key decisions.**
- **The runtime destroys the value.** On failure the runtime runs the
  moved value's drop glue, because the IR moved the value into the
  intrinsic and holds no copy.
- **Spawns are unchanged.** `tests/trap/oom_spawn.kern` and the scope
  fixtures still pin that a spawn's allocation failure traps `oom`.

**Acceptance.**
- The fixtures pass in the interpreter and natively, at debug and at
  release; the `test-hooks` fixture runs natively at debug only, as the
  harness runs such fixtures.
- The `oom_*` trap fixtures pass unchanged (R3).
- `go test ./...`, `golangci-lint run ./...` and `gofmt -l` are clean.

**Risks.** A drop glue that traps runs inside the runtime call, and its
trap is reported from there, as a `Shared`'s drop is today.
