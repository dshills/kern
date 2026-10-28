# Condition variables, and the thread_pool gap

**Date:** 2026-10-28. **Follows:** `docs/reports/quiet-harness-2026-10-28.md`,
which left thread_pool and lock_protected_map as the two workloads missing
the throughput target (0.59 and 0.57 of C) and diagnosed most of their gap
as the mutex kind, fixed there with the owner-checked default mutex.

## What was left

The kern thread_pool had no way to wait: with an empty ring a worker
released the lock and took it again at once, and the main thread spun the
same way on a full ring ("no condition variables in kern", its header
said). The C version waits on two `pthread_cond_t`s (`ready`, `space`), so
an idle thread costs nothing and the lock is not contended by spinners.

## Change: `Condvar` (spec §5.5)

`Condvar` is a `Send`, `Share`, `Interior` prelude type with `new()`,
`wait[T](view self, g: edit[T])`, `notify_one` and `notify_all`. `wait`
takes the `edit` a `Mutex` lock bound: the checker requires the argument to
be that binding, unprojected (`CONDVAR_NOT_GUARD`, rule 5.5.4, with a
rejecting fixture and its accepting twin under `tests/negative`), and the
runtime recovers the `Mutex` from the guard. Spurious wakeups are
permitted, so a caller re-tests its condition in a loop, as the C and Rust
idiom does.

- **Hosted runtime** (`runtime/hosted/src/kern_rt.c`): a `pthread_cond_t`
  plus the address of the `Mutex` its first wait used. The guard is the
  `Mutex`'s data pointer; `kern_condvar_wait` takes the containing
  `kern_mutex`, checks that the calling thread holds it (the owner word the
  mutex already keeps), clears the owner, waits and restores it. A wait by
  a thread that does not hold the lock, or with a different `Mutex` from the
  first wait's (undefined in POSIX), traps `explicit`, as the interpreter
  does.
- **Interpreter**: the edit a lock returns now reads and writes the
  `Mutex` through a `mutexRef`, so `wait` can find the `Mutex` behind its
  guard without a registry. Waiters block on the channel of the condition
  variable's current generation; a notify closes it and opens the next. The
  thread-identity and same-`Mutex` checks mirror the runtime's.
- **IR validator**: a call's returned projection (a lock guard, `Vec.at`)
  is now recorded as derived from its access argument, and when that
  argument's access ends the derivation passes to its ancestors. Before,
  reborrowing a guard taken through a with-bound view (`with p := x.get()
  { with g := p.a.lock() { p.cv.wait(g) } }`) was rejected after lowering
  although the checker accepts it: the validator saw an edit on `x.a`
  overlapping the open view of `x` and did not know the guard came from it.
- Fixtures: `tests/concurrency/condvar_handoff` (a bounded queue with two
  condition variables, ThreadSanitizer clean), `tests/trap/condvar_mismatch`
  (a second `Mutex` on one `Condvar` traps) and its twin, all agreeing
  between the interpreter and both native builds.

`bench/kern/thread_pool` now waits as the C version does: workers on
`ready` while the ring is empty, the main thread on `space` while it holds
more than 1,024 jobs, with the same signal and broadcast points.

## Measured

Release builds, seven interleaved runs each, one-minute load 4.3 to 4.8
(not a quiet host; the full harness was not rerun):

| | Mops/s (min to max, median) | of C |
|---|---|---|
| C (`clang -O2`) | 33.6 to 34.6, 33.8 | 1.00 |
| kern before (spinning) | 27.0 to 28.7, 28.2 | 0.83 |
| kern with `Condvar` | 30.3 to 32.0, 30.7 | 0.91 |

The checksum is unchanged (14753395421204281632). The remaining difference
is within the 10% target. lock_protected_map does not wait on anything, so
nothing here changes it; its gap (0.57 on the quiet-host run) is still the
lock hand-off between four threads and is not diagnosed further here.
