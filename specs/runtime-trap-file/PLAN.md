# Plan: runtime-raised traps name the user-level call's file

**Spec:** `specs/runtime-trap-file/SPEC.md` v0.1. **Profile:** davin-go.
**Order:** Phases 1 and 2 are one correctness gate: the runtimes and the
backend change the same C signatures, so neither builds a working
program without the other, and nothing is committed between them; each
phase ends with `golangci-lint run ./...` and `go test ./...` green
(Phase 1's gate is a standalone `clang -fsyntax-only -Wall -Wextra` of
both runtime sources; fixtures are built and run only at the combined
gate after Phase 2, and the full-suite gates apply from there). The change lands as one commit after Phase 4
unless the owner asks for more.

**Baseline (done, 2026-10-30):** release builds of
`bench/kern/lock_protected_map` and `bench/kern/thread_pool` made with
the compiler at 54e0be6 (the commit before this change; the scratch
binary `kern-ii` was built from that tree), kept in the session's
scratch for R8, together with the uncontended lock loop
(`rt/lockloop.kern`: 10,000,000 lock and unlock of one `Mutex[u64]` in
one thread) built the same way: `kern-ii build --opt=release -o
rt/<name>_base <source>`; the new binaries are built with the same
command and the new compiler.

## Phase 1: the runtimes (R1)

**Files.** `runtime/hosted/src/kern_rt.c`, `runtime/freestanding/src/rt.c`.

**Steps.**

1. Hosted: `kern_alloc(n, at)`; the trailing `const kern_loc *at` (a
   pointer to `{ start, end, file }`) on `kern_shared_new`,
   `kern_shared_clone`, `kern_mutex_new`, `kern_mutex_lock` (which had
   start and end), `kern_condvar_new`, `kern_condvar_wait` (likewise),
   `kern_spawn_common` (internal), `kern_thread_spawn`, `kern_scope_new`,
   `kern_scope_spawn`, the four `kern_sysalloc_*`; every `kern_trap` they
   raise uses them. The fault handler (`kern_segv`) is unchanged.
2. Freestanding: the same trailing record on the four
   `kern_sysalloc_*`, threaded through `kern_check_request` and
   `kern_check_block`. The freestanding runtime implements no `Shared`,
   `Mutex`, `Condvar` or threads, so the other entry points of R1 do not
   exist there.
3. Check: `rg "kern_trap\(.*(NULL|0, 0, 0)\)"` over both runtimes finds
   only `kern_trap_loc`'s no-location branch (the fault handler calls
   `kern_trap_at`); every entry point's trap path is exercised by a
   fixture in Phase 3.

**Acceptance.** Both runtime sources pass `clang -fsyntax-only -Wall
-Wextra` (the freestanding one with `-ffreestanding`).

## Phase 2: backend and interpreter (R2, R4 interpreter half)

**Files.** `compiler/backend/llvm/calls.go`, `compiler/interp/intrinsics.go`,
`compiler/interp/interp.go`, `compiler/backend/llvm/emit_test.go`.

**Steps.**

1. Every `fe.rt` call to the entry points above declares the trailing
   `ptr` and passes `fe.loc(c.Pos)`: a private constant `{ i64, i64, ptr
   }` record per distinct span and file operand in user code, a stack slot
   filled from `%span.s, %span.e, %span.f` in a library function (which
   are 0–0 and null when the library function was entered without a call,
   drop glue or a function value, by the existing hidden parameters); both `kern_shared_clone` call sites
   (the `clone` intrinsic and `Shared.clone`) change together, since one
   declaration serves both.
2. Interpreter: `runtimeAlloc(pos)` consumes an armed
   `fail_next_alloc` and traps `oom` at the call; called by `Shared.new`,
   `Mutex.new`, `Condvar.new`, `thread.scope`, `thread.spawn`,
   `scope.spawn`.
3. Backend test `TestEmitRuntimeTrapLocation` as R2 states.

**Acceptance.** `go test ./compiler/...` green; the existing trap
fixtures (`mutex_recursive`, `condvar_mismatch`, `oom`, `stack`) pass.

## Phase 3: comparison rule and fixtures (R3–R7)

**Files.** `tests/harness_test.go`, `compiler/testrun/run.go`,
`compiler/testrun/testrun_test.go`; fixtures `tests/trap/import_mutex_recursive/`,
`import_condvar_mismatch/`, `import_oom/`, `import_stack/`,
`oom_mutex_new.kern`, `oom_shared_new.kern`, `oom_condvar_new.kern`,
`oom_scope.kern`, `oom_spawn.kern`, `oom_scope_spawn.kern`,
`shared_overflow.kern`, `sysalloc_obligation.kern`.

**Steps.**

1. Harness `compareTrapRecords` and `testrun.Agree`: a `stack` record's
   file is not compared, like its span; a unit test for both directions
   (R7).
2. Fixtures as R3–R7 name them; multi-file ones carry `//! trap-file:
   lib/lib.kern`; test-hook ones `//! test-hooks: true`.
3. Run them through the harness in every configuration they are valid
   in, and the freestanding fixtures with `KERN_REQUIRE_QEMU=1` (QEMU is
   installed on this host), so that the freestanding allocator's new
   signatures are exercised.

**Acceptance.** `go test ./...` green; `KERN_REQUIRE_QEMU=1 go test
./tests -run TestFixtures/freestanding/` green.

## Phase 4: measurement, documents, review (R8, R9)

**Steps.**

1. R8: release builds of the two lock workloads and of a
   10,000,000-iteration uncontended lock loop with the new compiler; at
   least seven interleaved runs of base and new binaries under
   `/usr/bin/time -l`, medians of instructions retired, ratio new/base;
   the bounds are R8's and both must hold (an inconclusive or failing
   measurement is investigated and rerun, not explained away); respect
   the bench slot (no harness or timing run of another session in
   progress, checked with `pgrep` excluding waiting shells).
2. Documents of R9 (`docs/reports/trap-file.md` "Not done",
   `specs/test-command/SPEC.md` R9, `spec/types.md` §6, the new report).
3. Review: `golangci-lint`, `go test ./...`, `prism review staged`,
   `realitycheck check`, `verifier analyze`; clarion skipped (no
   `docs/clarion-meta.json`).
4. Commit `fix(runtime): runtime-raised traps name the user-level call's
   file and span`, ask to push.

**Acceptance.** Every R8 bound holds (a deviation needs an approved
change to the spec, not a note in the report); reviews clean or
justified; commit made.
