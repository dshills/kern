# Runtime, phase 3: threads

**Date:** 2026-11-09. **Spec:** `specs/runtime/SPEC.md` v0.6, area C (RC1 to
RC4). **Plan:** `specs/runtime/PLAN.md`, phase 3. **Hosts:** macOS 27.2 on
arm64 and Ubuntu 24.04 on arm64 in the `kern-linux-check` container.

## What changed

- **A detached thread's result is destroyed (RC1).** Rule 5.5.2 says a
  `Thread` destroyed without `join` detaches, and the thread destroys its
  own result. The runtime leaked the result, its block and, when the
  thread outlived the handle, nothing else knew of them; the interpreter
  dropped the result without destroying it.
  - Each thread record now has one state word, moved by compare-and-swap:
    running, finished, detached. The thread's end and the handle's
    destruction each try to move it; whichever comes second destroys the
    result and frees the record. `join` takes the result instead.
  - `kern_thread_drop` takes the result's destructor (the ABI changes: the
    emitter passes the glue it already passed for a scoped join handle).
  - The interpreter does the same under a mutex in its `Thread` value.
- **Stack size (RC2).** Spawned threads had the platform's default: 512 KiB
  on macOS, 8 MiB on Linux. Every spawned thread now gets the configured
  size, rounded up to a page: the last successful
  `thread.set_stack_bytes(n)`, else `KERN_STACK_BYTES`, else the build's
  `--stack-bytes=N` (or `stack_bytes` in `kern.mod`), else 8 MiB.
  - When the environment or the build names a size, `kern_run_main` runs
    the program's `main` on a thread of that size, so recursion depth no
    longer depends on the shell's limit. Otherwise `main` stays on the
    initial stack. That thread keeps identifier 0.
  - `kern run` gives the interpreter's simulated stack the same sources,
    and its `--stack-bytes` now has the range the others have (65,536 to
    1,073,741,824); it took any positive integer before.
  - A `kern test` executable runs each test through `kern_run_main` too,
    so `KERN_STACK_BYTES` reaches tests.
- **Spawn acquires everything first (RC3).** The record, the argument
  block and the alternate signal stack are obtained before the thread is
  created, and the scoped spawn's join handle with them. The alternate
  stack is now a mapping with a guard page below it, made by the spawner
  and unmapped by the thread when its function has returned; it used to be
  allocated by the new thread, which ran without one when that failed.
- **A spawn that cannot allocate is `Err(SpawnErr)`.** It trapped `oom`
  (`specs/runtime-trap-file` R4). A spawn already had an error result for
  "no thread exists", and a program that must survive memory pressure had
  no way around the trap. `tests/trap/oom_spawn.kern` and
  `oom_scope_spawn.kern` became `tests/run/spawn_alloc_fails.kern` and
  `scope_spawn_alloc_fails.kern`; `show-effects` no longer lists `oom` for
  the two spawn forms. `thread.scope()` still traps `oom`.
- **Queries (RC4).** `thread.available_parallelism()` (the affinity mask on
  Linux, the online processors on macOS) and `thread.current_id()`.

## Found on the way

- **The interpreter decremented a `Shared` count without its lock.**
  `clone` incremented under `atomicMu`; destruction decremented with no
  lock, so concurrent drops lost updates and the count could wrap. The
  detach stress fixture, which clones one `Shared` into 300 threads, hit
  it as an `overflow` trap. The decrement is now under the same lock.

## Requirement to test

| Clause | Test |
|---|---|
| RC1: the handle destroyed while the thread runs: the thread destroys its result | `tests/concurrency/thread_detached_result.kern` (first half; the thread is held at a gate until the handle is gone) |
| RC1: the handle destroyed after the function returned: destroyed at once | same (second half) |
| RC1: exactly once, whichever comes first | `tests/concurrency/thread_detach_stress.kern` (300 races; the count of destructions equals the count of threads) |
| RC1: both engines | both fixtures, by the harness; and natively under ThreadSanitizer (`tsan: clean`) |
| RC1: no leak | `TestFixturesSanitized` on Linux (LeakSanitizer) over both fixtures |
| RC2: the default is 8 MiB on both systems | `TestStackBytes` (a 3 MiB recursion on a spawned thread), `tests/run/thread_queries.kern` |
| RC2: `set_stack_bytes` bounds, no change on refusal, effect on the next spawn | `tests/run/thread_queries.kern`; `TestStackBytes` ("the program's own setting overrides both") |
| RC2: order of the sources | `TestStackBytes` (eleven cases), `TestStackBytesSources` (flag, manifest, environment, for `run` and `build`) |
| RC2: page rounding | `thread.stack_bytes()` in the fixture (values that are whole pages on both systems); the rounding itself is `kern_page_round`, reached by every spawn |
| RC2: an unaccepted flag or manifest value builds nothing | `TestStackBytesSources`, `TestParseManifest` |
| RC2: `main` on a thread of the configured size | `TestStackBytes` ("build 1 MiB: main overflows too", "build 64 MiB: main recurses 20 MiB", "the environment alone moves main") |
| RC2: the fatal line when that thread cannot be created | `TestMainStackUnavailable` (Linux: an address-space limit) |
| RC2: stack exhaustion is a `stack` trap on any thread | `TestStackBytes` (the trapping cases check the record) |
| RC2: the interpreter follows the same sources | `TestStackBytesSources` (`run`) |
| RC3: a failed spawn returns `Err`, has destroyed the owned arguments, leaks nothing | `tests/run/spawn_alloc_fails.kern`, `scope_spawn_alloc_fails.kern`, the existing `fail_next_spawn` fixtures, all under `TestFixturesSanitized` |
| RC3: the alternate stack is released at thread end | the sanitized run of every threaded fixture; `thread_detach_stress` creates and ends 300 threads |
| RC4: `available_parallelism() >= 1`, `current_id()` 0 on main and not on a spawned thread | `tests/run/thread_queries.kern` |

## Deviations and open points

- **`kern test` has no `--stack-bytes`.** The plan listed it; the spec
  names `build`, `run` and the manifest. A test executable honours
  `KERN_STACK_BYTES`.
- **The manifest's error code is `PROJECT_INVALID`**, the code every other
  manifest error has; the spec wrote `PROJ_MANIFEST_INVALID`.
- **Forcing each ordering deterministically** (plan, phase 3) is done for
  "destroyed while running" (a gate) and approximated for "destroyed after
  return" (the thread signals just before returning, and the main thread
  yields 2,000 times before destroying the handle). The outcome is the
  same in either order, which is the point; the stress fixture covers the
  race itself.
- **A thread that cannot have an alternate signal stack** is now a failed
  spawn. A stack overflow on a thread without one could not be reported.

## Verification

`go test ./...` passes on macOS; the phase's fixtures and Go tests, the
area-A checks and the sanitized fixture run pass in the Linux container;
`golangci-lint run ./...` reports no issue.
