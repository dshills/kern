# Plan: a runtime-owned Mutex for the two lock-bound workloads

**Spec:** `specs/perf-gaps/SPEC.md` v0.1. **Profile:** davin-go (the
repository's Go harness drives the C runtime's tests). **Order:** the
phases depend on each other in order; each ends with `go test ./...`
green and is committed on its own only if the owner asks, otherwise the
change lands as one commit after Phase 4.

## Phase 1: the runtime lock and condition variable (R1–R10, R11)

**Goal.** Replace the pthread mutex and condition variable in
`runtime/hosted/src/kern_rt.c` with the runtime's own parking lock and
sequence condition variable, exactly as spec §4.1 and §4.2 prescribe,
with no change to the nine entry points' names or signatures (the
backend's declarations in `compiler/backend/llvm/calls.go` and
`types.go` stay as they are).

**Files.** `runtime/hosted/src/kern_rt.c` only: the `// ---- Mutex ----`
and `// ---- Condvar (spec §5.5) ----` sections (today lines 297–358).

**Steps.**

1. Add the parking primitives above the Mutex section, under
   `#ifdef __APPLE__` (`<os/os_sync_wait_on_address.h>`:
   `os_sync_wait_on_address`, `os_sync_wake_by_address_any`,
   `os_sync_wake_by_address_all`, flags `_NONE`), `#elif defined(__linux__)`
   (`<linux/futex.h>`, `<sys/syscall.h>`, `<unistd.h>`: `syscall(SYS_futex,
   addr, FUTEX_WAIT_PRIVATE, expect, NULL, NULL, 0)` and
   `FUTEX_WAKE_PRIVATE` with 1 or `INT32_MAX`), and `#else #error "kern:
   no park primitive for this platform (os_sync_wait_on_address or
   futex)"`. Each wrapper ignores the primitive's return value (spec §2,
   "Park / unpark": every return is treated as spurious by the caller,
   an unpark's result is ignored).
2. Define `kern_mutex` as `{ _Atomic(uint32_t) state; _Atomic(uintptr_t)
   owner; int64_t size; char data[]; }` and assert the layout of R6 at
   compile time: `_Static_assert(offsetof(kern_mutex, owner) == 8 &&
   offsetof(kern_mutex, size) == 16 && offsetof(kern_mutex, data) == 24,
   ...)`. `kern_mutex_new` allocates through `kern_alloc` (malloc, 16-byte
   alignment), initialises state and owner to 0, copies the element.
3. `kern_mutex_lock`: `self = (uintptr_t)pthread_self()`; a strong
   compare-and-swap of state 0→1 with acquire ordering (the fast path,
   R2); on failure, if `owner` (relaxed load) equals `self`, trap 11 at the
   lock site's span (R2, R3); otherwise `kern_mutex_acquire`: loop
   `s = exchange(state, 2, acquire); if s == 0 break; park(&state, 2)`
   (R4, including the spurious-return rule: every return repeats the
   exchange). Then store `owner = self` (relaxed) and return `data`.
   No spin loop (spec §3).
4. `kern_mutex_release` (shared by unlock and wait): store `owner = 0`
   (relaxed), `exchange(state, 0, release)`, and if the previous state
   was 2, `unpark_one(&state)` (R5). `kern_mutex_unlock` calls it.
   `kern_mutex_drop`: glue, then `free` (R7).
5. `kern_condvar` is `{ _Atomic(uint32_t) seq; _Atomic(uintptr_t) mutex; }`;
   `kern_condvar_new` allocates and zeroes it (R10, no failing
   initialisation). `kern_condvar_wait`: recover the Mutex from `data` by
   `offsetof`; trap 11 if `owner != self` (relaxed load); bound-Mutex
   check as R8 states it (relaxed load, one seq_cst compare-and-swap from
   0 only when the load is 0, trap if the value found is another Mutex);
   `seq = load(cv->seq, relaxed)`; `kern_mutex_release(mx)`;
   `park(&cv->seq, seq)`; `kern_mutex_acquire(mx)`; `owner = self`.
   `notify_one`/`notify_all`: `fetch_add(seq, 1, relaxed)` then
   `unpark_one`/`unpark_all` on `&cv->seq` (R9). `kern_condvar_drop`:
   `free` (R10).
6. Rewrite the two section comments to say what the lock is and why
   (the diagnosis in one paragraph: the owner must share the state's
   line and be written only by the holder; no spinning, since spinning
   makes the pool's hand-off fair and migrates the line per job).
7. Remove `kern_mutex_self` and every reference to `pthread_mutex_t m`
   and `pthread_cond_t c` in these sections; `kern_outlock` elsewhere
   stays a pthread mutex (spec §5).
8. Build and run: `go build ./... && go test ./...` (the fixture harness
   compiles every `tests/` fixture natively, including the `//! tsan:
   clean` fixtures with ThreadSanitizer and the `trap`/`twin` pairs), and
   explicitly `go test ./tests -run 'TestTrap|TestConcurrency|TestNative'`
   for the fixtures R11 names, plus `go test ./compiler/backend/...`.
   `golangci-lint run ./...` (no Go changes expected; still run).
9. Compile-check the Linux path by inspection only (spec §5): the
   development host has no Linux headers; the report (Phase 4) says so.

**Acceptance.** Every test green; `tests/trap/mutex_recursive.kern`
still reports `trap explicit` at the lock site and its twin prints
`mutex_recursive_ok`; `tests/trap/condvar_mismatch.kern` and twin
likewise; the three TSan fixtures clean; the `_Static_assert` of R6
holds; `rg -n "pthread_cond|kern_mutex_self" runtime/hosted/src/kern_rt.c`
finds nothing, and `rg -n pthread_mutex` finds only `kern_outlock`.

**Risks.** A lost wake would hang a test (the harness's per-fixture
timeout catches it: a hang is a failure, not a pass). An ordering mistake
would be a TSan report on `mutex_counter`, `condvar_handoff` or
`shared_handoff`. The Linux path is untested here (recorded).

## Phase 2: a fixture for the parked path (R13)

**Goal.** A TSan-clean fixture in which waiters park and a Condvar hands
values both ways, with a deterministic expected output, so that the
slow paths of R4, R5, R8 and R9 are exercised under the race detector
and the harness's timeout, not only by the benchmarks.

**Files.** `tests/concurrency/mutex_parked.kern` (header `//! phase: 3`,
`//! expect: output mutex_parked`, `//! tsan: clean`) and
`tests/concurrency/mutex_parked.out`.

**Steps.**

1. Four scoped threads each take one `Mutex[u64]` 2,000 times, adding
   their thread index + 1 to the counter under the lock and, inside the
   critical section, running a loop of 200 iterations of wrapping
   arithmetic on a local so that the section is long enough (on the order
   of a microsecond) for the other three to fail the fast path and park;
   the expected counter is 2,000 × (1 + 2 + 3 + 4) = 20,000.
2. In the same program, two threads ping-pong 500 values through a
   one-slot `Mutex[Slot]` with two Condvars (`full`, `empty`), each
   waiting in a loop; the consumer sums 1..=500 = 125,250.
3. `main` prints the two totals on one line each. The expected output
   file holds them.
4. Run `go test ./tests -run TestConcurrency` (natively and under TSan,
   as the harness does for `tsan: clean`).

**Acceptance.** The fixture passes natively and under TSan. **Known
limitation (R13 is met as far as a fixture can meet it).** A kern
program cannot observe whether a waiter parked, and the harness has no
test hook for the runtime's park count; the fixture makes parking
overwhelmingly likely (four threads contending for a section of about a
microsecond, where the map's 20 ns section already parks waiters) and a
lost wake shows as the harness's per-fixture timeout. **Failure-path
map.** R2 (recursive acquisition) is `tests/trap/mutex_recursive.kern`;
R8's two traps are `tests/trap/condvar_mismatch.kern` (second Mutex) and
`tests/native_test.go` `TestNativeStackTrapUnderMutexWithHandler` (a trap
while holding); R4/R5 (park, unpark, acquire as 2) and R9 (notify one and
all with parked waiters) are `mutex_parked.kern` and
`condvar_handoff.kern`; a spurious or failed park cannot be forced from
a test and is covered by review of the re-check loops (spec §2).

## Phase 3: measurement (R12, R14)

**Goal.** Show the two ratios at or above 0.90 as the protocol
measures them, with unchanged checksums and no regression elsewhere.

**Steps.**

1. Pre-check with interleaved runs (the scratch script of the diagnosis,
   fifteen rounds of C against the new `kern build --opt=release`
   binary) for thread_pool and lock_protected_map; the checksums must be
   14753395421204281632 and 1619761243042050329 (R12).
2. Check the bench slot and the host: no other `bench/harness` or timing
   run in progress (`pgrep -fl 'bench/harness|bench/agent/harness'`,
   excluding waiting shell wrappers), no `go test` or `go build` of this
   repository running in any session, and a one-minute load average
   below 4 (`uptime`). If the host is not quiet, wait and retry at intervals
   rather than measure under load; if it stays loaded, run anyway and
   record the load in the report (R14's second sentence) so the owner
   can re-run `go run ./bench/harness` later.
3. `go run ./bench/harness` (all workloads, all languages, five runs)
   regenerates `bench/REPORT.md`. Read the Targets lines for thread_pool
   and lock_protected_map and compare every other workload's ratio with
   the 2026-10-28 report: none that met the target may fall below 0.90.
   Compare every `Checksums:` line and every row's checksum with the
   previous report (`git diff bench/REPORT.md | rg -n "checksum|Checksums"`
   must show no changed checksum value) (R12).
4. If a ratio is below 0.90 on a quiet host, stop and diagnose before
   Phase 4; the spec's acceptance waits (R14).
5. Completion gate: this task is complete only once step 3 has run on a
   quiet host (load below 4, slot free). If the only run so far was under
   load, the report of Phase 4 says "pending a quiet-host run" in its
   status line with the load average, the research report's paragraph
   says the ratios are provisional, and the commit body says so; the
   quiet rerun is then the next task, not an optional follow-up.

**Acceptance.** `bench/REPORT.md` regenerated; both Targets lines read
"meets the 10% target" for throughput; checksums as R12; no new miss.

## Phase 4: documents (R15)

**Files.** `docs/reports/perf-gaps.md` (new), `docs/unsafe-audit.md`
(the `Mutex` row), `docs/security-review.md` (the `Mutex` row),
`docs/research-report.md` (the closing paragraph of the benchmark
section, which today names the lock hand-off as what remains), and
`docs/workloads/thread_pool.md` / `lock_protected_map.md` only if a
sentence there describes the kern lock's implementation (today neither
does; `lock_protected_map.md` line 90 describes the C version and stays).

**Steps.**

1. Write `docs/reports/perf-gaps.md`: date, the question, the diagnosis
   with the interleaved measurements of 2026-10-29 as a table whose
   ratios are same-round ratios (each variant's median divided by the C
   median of the same interleaved run, never across runs, with that
   run's load average in the row) for C, as built, no-owner, owner-far,
   owner-on-data, trylock-first, held-list, the prototype with and
   without spinning, and the sliced job (the source is the diagnosis's
   scratch directory of this session,
   `/private/tmp/claude-501/-Users-dshills-Development-projects-kern/108b2980-33da-4d04-9b28-5e43c89257d3/scratchpad/perf/`,
   whose `cmp.sh` printed each run's medians with `uptime` before and
   after; the scratch is ephemeral, so the report inlines every number
   it cites and nothing outside the repository is required to read it),
   the design (one paragraph per mechanism, with the reason each
   alternative lost), the Phase 3 result (the two Targets lines, the
   p50/p99 before and after), the Linux path's untested status, and what
   remains (the sliced job loop as a note for the owner, not applied; the
   btree memory under ADR-014).
2. Update the two audit rows to describe the runtime-owned lock (state,
   owner written only by the holder, recursive acquisition traps
   `explicit` before any wait) and the research report's paragraph to
   state the new ratios and that the mutex hand-off is closed.
3. `rg -n "error-checking pthread mutex|PTHREAD_MUTEX_ERRORCHECK" docs`
   must find nothing outside `docs/reports/` history.

**Acceptance.** The documents exist and say what R15 requires; no stale
description of the Mutex outside historical reports.

## After the phases

Phase review as the owner's standing workflow prescribes (a workflow
step authorised outside this spec, not an acceptance criterion of it): `golangci-lint run
./...`, `go test ./...`, `prism review staged --provider openai --model
gpt-6.1-sol` (fix high and medium or justify in the commit body),
`realitycheck check . --spec specs/perf-gaps/SPEC.md --plan
specs/perf-gaps/PLAN.md --provider openai --model gpt-6.1-sol` with the
unrelated directories ignored, `verifier analyze -root . -mode offline
-spec specs/perf-gaps/SPEC.md -plan specs/perf-gaps/PLAN.md -include
'runtime/**,tests/concurrency/**'`, no clarion (no
`docs/clarion-meta.json`); then one conventional commit on `main`
(`perf(runtime): own the Mutex and Condvar with a parking lock`), and
ask before pushing.
