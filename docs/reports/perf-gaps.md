# The two lock-bound workloads: a runtime-owned Mutex

**Date:** 2026-10-29. **Spec and plan:** `specs/perf-gaps/SPEC.md`,
`specs/perf-gaps/PLAN.md`. **Follows:** `docs/reports/quiet-harness-2026-10-28.md`
(the error-checking pthread mutex replaced by a default mutex with an
owner word, lock_protected_map 0.57 → 0.76 of C) and
`docs/reports/condvar.md` (condition variables, thread_pool 0.59 → 0.88).
**Status:** lock_protected_map closed (0.76 → 1.02 of C); thread_pool not
closed (0.88 → 0.88), its residual shown to lie outside the lock; spec R14
met for the map only.

## The question

`bench/REPORT.md` of 2026-10-28 had two workloads below the throughput
target: thread_pool at 0.88 of C (31.4 against 35.4 Mops/s) and
lock_protected_map at 0.76 (26.2 against 34.6). Both are bound by one
`Mutex` that four threads take once per operation; `condvar.md` left the
map's gap as "the lock hand-off between four threads", not diagnosed
further. The third gap of the research report, btree's peak RSS at 1.23
of C, is the generation a checked 8-byte handle carries and ADR-014
decided to keep the handle, so it is out of scope.

## How the measurements were taken

The host was not quiet all day (Microsoft Teams and another agent held
five to ten cores; one-minute load averages of 3.4 to 14), so the
protocol's five-run medians would not have separated variants a few
percent apart. Every number below is instead from an interleaved run: a
script that executes each binary in turn, round after round (eleven to
twenty-one rounds), and reports each binary's median wall time with
`uptime` before and after. A ratio is always a same-round ratio, the C
median divided by the variant's median within one run; ratios are never
compared across runs. The same binaries were used throughout: the C
workloads from `clang -O2` of `bench/c/*/main.c`, and the kern workloads
from the LLVM text that `kern build --opt=release --emit-llvm` wrote,
compiled with `clang -O2 -DKERN_RELEASE` against a copy of
`runtime/hosted/src/kern_rt.c` with one change each, which is exactly
how `kern build` links a program. Checksums were identical in every
run (thread_pool 14753395421204281632, lock_protected_map
1619761243042050329).

## What the gap was not

- **The uncontended lock path.** A C microbenchmark linked against the
  runtime times one lock/unlock pair at 3 to 4 ns for both the pthread
  mutex and the runtime's Mutex, on one thread, with no difference.
- **The two wrapper calls.** `kern_mutex_lock` and `kern_mutex_unlock`
  are out-of-line calls in another translation unit; linking with
  `-flto` changed thread_pool by nothing (32.7 → 32.4 ms, C 28.9) and
  in fact left both calls in place.
- **The code around the lock.** The emitted IR at the map's two lock
  sites is a lock call, the map operation, an unlock call and nothing
  else; the single-threaded hash_table workload, which shares the map,
  is at 0.96 of C.
- **The pool's job loop.** Its 64 indexed reads per job are each
  runtime bounds-checked (the view's length is not known to the proof
  pass), which blocks vectorisation; rewriting the job to slice the
  block once and iterate the slice (3 runtime bounds checks instead of
  5 in the program) is worth about one point: 32.0 against 32.9 ms, C
  29.3, in one run, and 30.3 against 30.5 ms with the new lock. Not
  applied: the benchmark programs are not changed to fit the compiler.

## What the gap was

The Mutex was a default pthread mutex plus an owner word, the address
of a thread-local, that the holder stored after locking and cleared
before unlocking, so that a thread finding its own address there is
acquiring the Mutex it holds and traps `explicit`. The owner sat on the
cache line of the pthread mutex, the line that arriving waiters write
with their compare-and-swap attempts. Variants of that design, each
one change to the runtime (ms are medians; the ratio is C's median over
the variant's):

| run (load) | workload | C | as built | variant | ms | ratio |
|---|---|---|---|---|---|---|
| A (3.4–3.8) | lock_protected_map | 105.4 | 132.8 (0.79) | trylock first, no owner load on the fast path | 124.7 | 0.85 |
| A | lock_protected_map | | | owner and data each padded to their own line | 114.7 | 0.92 |
| A | thread_pool | 28.9 | 32.2 (0.90) | trylock first | 33.1 | 0.87 |
| A | thread_pool | | | owner and data each padded | 37.1 | 0.78 |
| B (3.6–3.9) | lock_protected_map | 105.3 | 137.3 (0.77) | owner on its own line, data adjacent to the mutex | 119.1 | 0.88 |
| B | lock_protected_map | | | owner adjacent, data on its own line | 131.3 | 0.80 |
| B | thread_pool | 28.9 | 32.5 (0.89) | owner on its own line | 38.3 | 0.75 |
| B | thread_pool | | | data on its own line | 32.1 | 0.90 |
| C (4.4–4.7) | lock_protected_map | 126.6 | 162.4 (0.78) | no owner at all (no recursion check: a bound, not a candidate) | 140.2 | 0.90 |
| C | lock_protected_map | | | a thread-local list of held Mutexes, nothing shared written | 164.2 | 0.77 |
| C | thread_pool | 27.5 | 31.2 (0.88) | the thread-local list | 37.0 | 0.74 |
| D (5.0–5.9) | lock_protected_map | 110.3 | 136.7 (0.81) | no owner at all | 114.5 | 0.96 |
| D | lock_protected_map | | | owner on its own line | 121.5 | 0.91 |
| D | lock_protected_map | | | trylock first, owner on the data's line (128-byte aligned allocation) | 124.6 | 0.89 |
| F (8.5) | thread_pool | 29.3 | 32.9 (0.89) | owner on the data's line | 37.7 | 0.78 |

Read together: on the map the owner's two stores and its load on the
waiters' line cost about 15% (no owner at all reaches 0.96, the owner
on its own line 0.91); on the pool, whose hand-offs are between parked
workers rather than contended, every layout that touches a second line
per lock costs 15% to 18%, because each hand-off migrates one more line
between cores, and the owner cannot leave the lock's line. A
thread-local held-list, which writes nothing shared, is slower than
either on both (thread-local access on macOS is an indirect call per
use). Nothing with a pthread mutex underneath satisfied both workloads.

## The design

The runtime owns the lock (`runtime/hosted/src/kern_rt.c`, the Mutex
and Condvar sections): a 32-bit state that waiters park on (0 free, 1
held, 2 held with a thread parked or about to park), the holder's
`pthread_self()` beside it in the same 16 bytes, and the element after
a 24-byte header. The fast path is one compare-and-swap of the state
from 0 to 1 with acquire ordering; only when it fails does the lock
load the owner, trapping if it is its own id, and otherwise exchange
the state to 2 and park on it until the exchange finds 0. The holder
writes its id after the acquisition, when it already owns the line,
and clears it before the releasing exchange, so the id costs no line
transfer of its own; the release unparks one thread only when the state
it released was 2. There is no spin loop: the same lock with a hundred
spins before parking ran the pool at 0.69 of C (41.9 against 29.0 ms),
because spinning makes the hand-off between workers fair and migrates
the line on every job, while parking lets the releasing worker take the
next job as the pthread mutex did. The Condvar is a 32-bit sequence: a
waiter reads it under the lock, releases, parks while it is unchanged,
reacquires; a notify bumps it and unparks one or all. Parking is
`os_sync_wait_on_address` on macOS and the futex system call on Linux;
the Linux path is compiled only by review, since the development host
has no Linux headers.

The recursion check is the same as before (a thread finding its own id
traps `explicit` at the lock site before any wait), and so are the
Condvar's two traps; `tests/trap/mutex_recursive.kern`,
`tests/trap/condvar_mismatch.kern` and their twins pass unchanged, as
do the TSan-clean fixtures `mutex_counter`, `condvar_handoff` and
`shared_handoff`. A new fixture, `tests/concurrency/mutex_parked.kern`
(TSan clean), makes four threads contend for a critical section of
about a microsecond, so that waiters park and are unparked, and hands
500 values through a one-slot Mutex with two Condvars.

## The result

The prototype in the interleaved runs (run G, load 4.9–5.0, 21 rounds):
lock_protected_map 87.6 ms against C's 111.0 (1.27 of C; as built
140.3, 0.79), thread_pool 30.5 ms against 29.0 (0.95; as built 32.4,
0.90). The map is at or above C because the C program's pthread mutex
pays owner-line traffic of its own (Apple's mutex stores the holder's
id in its line too) and parks through a heavier path; Rust's standard
mutex is a lock of the same shape and the report's Rust row for the map
was already at 0.98 of C.

## The pre-check with the real compiler

After the change landed in `runtime/hosted/src/kern_rt.c`, the two
workloads were built with `kern build --opt=release` and interleaved
against C and the previous runtime's binaries for fifteen rounds (load
4.8 to 4.9, right after the test suite): lock_protected_map 107.7 ms
against C's 133.0 and the previous 179.1 (1.24 of C, previously 0.74),
thread_pool 31.6 ms against 28.6 and 32.3 (0.905, previously 0.885);
checksums unchanged.

## The harness run

The host rebooted before the protocol run (the boot-time load was in
the forties), and `go run ./bench/harness -experiment` ran once five
consecutive 30-second samples of the one-minute load were below 3.5:
15:15 to 15:16, load 3.4 at the start and 4.7 at the end, five runs per
row. `bench/REPORT.md` of 2026-10-29T15:16 is that run as measured;
every checksum in it is unchanged from the 2026-10-28 report.

| workload | before (2026-10-28) | after | ratio before → after |
|---|---|---|---|
| lock_protected_map | kern 26.2 Mops/s, C 34.6 | kern 33.1, C 32.3 | 0.76 → 1.02 |
| thread_pool | kern 31.4, C 35.4 | kern 35.0, C 40.0 | 0.88 → 0.88 |

The map meets the throughput target. The pool does not: its kern row
rose with the new lock (31.4 → 35.0 Mops/s, p99 22375 → 9583 ns) but
the C row rose as much (35.4 → 40.0), so the ratio is where it was. The
rebooted host explains the C row: interleaved on it, the C pool runs in
24.5 to 25.3 ms where before the reboot it ran in 28.6 to 29.3, while
the kern pool is at 29.0 to 30.2 ms on both; the prototype's 0.95 of
§"The result" was measured against the slower pre-reboot C. On the
rebooted host two more runtime variants were interleaved against the
current one (21 rounds each, load 4.3 to 8.1): a woken waiter spinning
16, 64 or 256 iterations before parking again (30.0 to 30.3 ms against
29.6: nothing), and a Condvar with a waiter count whose notify skips the
wake when nobody is parked, as pthread's does, which removes the
feeder's nearly one thousand broadcasts to no one from the critical
section (29.0 against 29.0: nothing). With the lock, the parking, the
wake policy and the Condvar's wakes all ruled out, the pool's residual
is outside the synchronisation primitives: the per-job path a worker
runs under and around the lock (the ring's two runtime-checked indexes,
the job loop's 64 checked reads, the `Option` the pop returns) adds up
to the 4 ns per job that separate 29 from 25 ms, and the sliced-job
measurement (one point) is the only piece of it measured so far.

One other ratio moved: arena_ast reads 0.88 (was 0.92) because the
Rust reference rose from 128.5 to 134.7 Mops/s while the kern row is
unchanged (118.1 → 118.2); arena_ast takes no lock and no runtime code
it runs changed, so this is the reference's variance between two runs,
not a regression of this change. Every other workload's ratio is within
0.03 of the previous report, and no checksum changed.

## What remains

- thread_pool at 0.88 of C, with the lock and Condvar now ruled out.
  The next diagnosis is the worker's per-job path rather than the
  primitives: the job loop's checked reads (a range loop over a view
  with a constant trip count and an invariant base could be checked
  once before the loop and versioned, the classic bounds-check hoisting
  the proof pass does not do), the ring's masked indexes (a mask fact
  would prove `ring[i & 2047]` for a 2048-element array), and the pop's
  `Option`. The benchmark program stays as it is.
- The Linux park path is untested on this host.
- btree's memory is ADR-014's decision.
