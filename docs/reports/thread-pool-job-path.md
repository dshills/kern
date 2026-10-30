# thread_pool: where the per-job path goes

**Date:** 2026-10-30. **Follows:** `docs/reports/perf-gaps.md` (the
runtime-owned lock: lock_protected_map 0.76 → 1.02 of C, thread_pool
0.88 → 0.88 with every lock and Condvar variant measuring the same).
**Question:** with the primitives ruled out, where do the pool's
remaining nanoseconds per job go, and which of them can a compiler or
runtime change remove without touching the benchmark program?
**Status:** attributed; no compiler change made, for the reasons in
§"What a compiler could do"; `bench/REPORT.md` regenerated on the quiet host twice
(2026-10-30T03:38 and 04:38, load 2.1 to 2.6): thread_pool 0.94 of C in
both, meets the target.

## Method

The pool was built with `kern build --opt=release --emit-llvm`, and
copies of the LLVM text were hand-edited to delete one group of checks
at a time (a `br i1 %c, label %trap, label %ok` becomes `br label %ok`,
and the trap block's phi loses that predecessor; `patch_ir.py` in the
session scratch), then compiled exactly as `kern build` links a program
(`clang -O2 -Wno-override-module -ffp-model=strict -DKERN_RELEASE
prog.ll runtime/hosted/src/kern_rt.c -lpthread -lm`). Every variant
printed the protocol checksum 14753395421204281632. Variants were
interleaved against the unmodified build and the C pool (`clang -O2
bench/c/thread_pool/main.c`) for 21 rounds, medians reported; ratios
are same-round. The host was quiet (one-minute load 2.4 to 4.0, the
night after a reboot).

## The per-job path, from the machine code

- **The job loop is already C's.** `run_job` indexes a view 64 times
  with a bounds check and an overflow check per iteration in the IR,
  but LLVM versions the loop on its own: the compiled worker computes
  `len - blk`, branches to the checked loop only if it is below 64, and
  otherwise runs the same 32 vector loads and adds the C worker runs.
  The proof pass's inability to prove `w[blk + j]` costs nothing here.
- **The pop** is about fifteen instructions: the ring read, the masked
  increment, the length decrement, the batch-boundary test, with one
  bounds check (`ring[head]`, unprovable: `head` is a field) and three
  overflow checks (`done_jobs + 1`, `head + 1`, `len - 1`). LLVM folds
  the bounds check on `ring[(head + len) & 2047]` by itself, since it
  knows the masked value's range.
- **The lock's fast path** is 40 instructions, 12 of them a call frame
  and a call to `pthread_self()` for the owner id; the unlock is 8.
- **The feeder's push loop**, which runs 1024 times per batch while the
  feeder holds the lock and the four workers wait, is scalar in kern
  (about twelve instructions per push: the overflow checks on
  `head + len`, `b + k` and `len + 1` are loop exits, and `len` is
  stored every iteration) and vectorized in C (two elements per
  instruction group).
- **Parking** is rare: per million jobs the runtime counts about 46,000
  parks, 105,000 wake calls (two per park: a release that observes
  state 2 wakes even when the parked thread is already running) and
  978 Condvar waits, all of them the feeder's; the workers never find
  the ring empty. The system's accounting agrees: kern takes fewer
  involuntary context switches than C (9,600 against 15,000 per run).
- **Instructions retired** per million jobs: C 442 to 458 million,
  kern 508 to 526 million; user CPU time 0.02 s against 0.04 s.

## Attribution

| variant (checks deleted from the IR, or runtime change) | ms | ratio to C |
|---|---|---|
| C | 28.9 | 1.00 |
| kern, unmodified | 30.7 | 0.94 |
| the job loop's 64 bounds and overflow checks | 30.0 | 0.96 |
| the pop's ring bounds check | 31.0 | 0.93 (noise) |
| the pop's three overflow checks | 30.6 | 0.94 |
| the feeder's push checks (the loop then vectorizes: 14 vector stores) | 30.4 | 0.95 |
| the owner id from the thread register instead of `pthread_self()` | 30.4 | 0.95 |
| every worker check (job loop, ring, pop overflow) | 30.1 | 0.96 |
| every check above, feeder included | 29.9 | 0.97 |

Earlier in the same session (run A, `perf-gaps.md`) the job loop's
rewrite to a slice was worth the same one point as deleting its checks
here: that is the versioned loop's guard and the slow path's code, not
the loop itself.

Each piece is one or two points; all of them together are three. The
rest, about one millisecond, is not in the checks: it is the difference
between two programs that execute the same vector loop and the same
fifteen-instruction pop with different lock functions and a different
feeder, and it is within the spread of the measurement.

## The harness's 0.88

The 0.88 of 2026-10-29's report is not the number above. In that run
the C pool took 25.0 ms (40.0 Mops/s); interleaved an hour later it
took 24.5 to 25.3 ms; interleaved this night it takes 28.9. The kern
pool took 28.6, 29.0 to 30.2 and 30.7 ms in the same three settings.
So C's pool varies by 15% with the host's state while kern's does not,
and the ratio moves with C. The protocol's five runs per row cannot see
this (its C row read 40.0 ± 0.9). What varies in C and not in kern is
not identified: both programs run the same vector loop, C is CPU-bound
in user mode (0.02 s user) and kern spends more of its time in user
mode, not less, so a clock-speed effect would move kern at least as
much; the difference may be in how the two park paths (psynch for the
pthread mutex, wait-on-address for the runtime's lock) interact with
the scheduler in the host's two states. The full harness run of this night (`go run ./bench/harness
-experiment`, 03:37 to 03:38, one-minute load 2.1 to 2.4, every checksum
unchanged) read thread_pool at 32.1 against C's 34.1 Mops/s, 0.94, which
meets the target, and lock_protected_map at 1.25. Two other rows moved
the other way in the same run: btree 0.87 (0.99 in the afternoon run)
and lock_free_queue 0.85 (1.03), neither of which takes a Mutex.
Interleaved minutes later on the same host, C against kern for seven
and eleven rounds, btree is 1.01 (362.9 against 358.1 ms) and
lock_free_queue 0.93 (7.6 against 8.2 ms). The harness runs each
language's five runs back to back, so a transient inside one row's
window moves that row alone and the ratio with it; the protocol's
confidence intervals describe the five runs within a row and cannot see
it. The report is committed as measured, since that is what the
protocol prescribes; the remedy is to the harness (run the languages'
repetitions round-robin, as the interleaved script does), which is a
task of its own.

## A second quiet run, one hour later

The owner offered a quiet machine and the harness ran again at 04:37
(load 2.6, checksums unchanged), with C against kern interleaved for
the four workloads right after. Between the two quiet runs an hour
apart, the ratios moved as follows; the interleaved column is the
same-round ratio measured minutes after the second run.

| workload | 03:38 run | 04:38 run | interleaved after 04:38 |
|---|---|---|---|
| thread_pool | 0.94 | 0.94 | 0.93 |
| lock_protected_map | 1.25 | 1.16 | 1.25 |
| btree | 0.87 | 0.93 | 0.91 (C 315 ms; it was 363 ms an hour earlier) |
| lock_free_queue | 0.85 | 0.95 | 0.93 |
| ring_buffer | 1.16 | 1.02 | |
| vector | 1.17 | 1.10 | |
| arena_ast | 0.91 | 0.87 | |
| hash_table | 0.91 | 0.96 | |

Two quiet runs of the protocol therefore differ by up to 0.1 on
individual workloads, in both directions, with the C or Rust reference
moving as often as the kern row; the pool and the map are among the
stable ones. `bench/REPORT.md` is the 04:38 run. The harness's
back-to-back rows are part of it (btree's 0.87 against 0.91
interleaved) but not all of it (btree's C time itself moved by 15%
between the hours), so the harness task should also consider more
repetitions, and a reader of any single report should treat a ratio
within 0.1 of the target as undecided.

## What a compiler could do

- **Overflow checks in the feeder and the pop** (`head + len`,
  `len + 1`, `b + k`, `done_jobs + 1`, `head + 1`, `len - 1`) are
  unprovable: the operands are fields with no invariant the language
  states, and the checks are semantics the program asked for. Making
  them cheaper means letting LLVM vectorize a loop whose exits are
  traps, which it will not do, or sinking the stores past the trap
  call, which would let a trap handler observe stale memory. Nothing
  to do without changing what a trap means.
- **A mask fact in the proof pass** (`x & 2047` proves an index into a
  `[2048]T`) would change no machine code, since LLVM folds that check
  already; it would change the label `show-bounds-checks` prints from
  `runtime-checked` to proven, which the agent benchmark's budgets
  count. That is an agent-metric improvement, not a performance one,
  and is left for a task of its own.
- **Loop versioning for the job loop** is already done by LLVM; the
  proof pass need not duplicate it.
- **The lock's `pthread_self()` call** could become a read of the
  thread register on Apple arm64 (`TPIDRRO_EL0`), saving the call frame:
  one point, platform-specific inline assembly in the runtime. Not
  done: the gain is within noise and the id would then differ between
  platforms.

## What this leaves

The pool is at 0.94 of C on a quiet host by interleaved measurement
and 0.97 with every removable check gone; the remaining distance is
below what the protocol can measure. The benchmark program is
unchanged. The open question is the C pool's own 15% swing with host
state, which decides whether the report's row reads 0.88 or 0.94 and is
a measurement question rather than a compiler one.
