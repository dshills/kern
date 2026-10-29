# A runtime-owned Mutex for the two lock-bound workloads

**Version:** 0.1 (2026-10-29). **Scope:** the hosted runtime's `Mutex`
and `Condvar` implementation (`runtime/hosted/src/kern_rt.c`: the
functions `kern_mutex_new`, `kern_mutex_lock`, `kern_mutex_unlock`,
`kern_mutex_drop`, `kern_condvar_new`, `kern_condvar_wait`,
`kern_condvar_notify_one`, `kern_condvar_notify_all`,
`kern_condvar_drop`), the two benchmark workloads that are bound by them
(`bench/kern/thread_pool`, `bench/kern/lock_protected_map`), the
benchmark report (`bench/REPORT.md`) and the documents that describe the
Mutex. **Language impact:** none: no change to the language spec, the
checker, the interpreter, the backend or the generated code.
**Related:** `specs/initial/AI_Native_Systems_Language_SPEC.md` §5.5
(threads, `Mutex`, `Condvar`), §13 item 4 (recursive acquisition traps
`explicit`), §2 (the 10% targets); `bench/PROTOCOL.md`;
`docs/reports/quiet-harness-2026-10-28.md` (the owner-checked default
mutex), `docs/reports/condvar.md` (condition variables), `docs/adr/`
ADR-014 (the 8-byte release handle).

**Review status.** speccritic (OpenAI gpt-6.1-sol, profile general,
with `bench/PROTOCOL.md` and `runtime/hosted/src/kern_rt.c` as
context): VALID_WITH_GAPS, no critical finding; the warnings are the
preflight's flags on the R*n* labels and on words such as "fast path"
and "ratio" that §2 defines, one scope note that §4 prescribes
mechanisms (intended, see the next paragraph), and two clarifications
folded in (the thread-local boundary in §5, the targets' allocator
alignment in R6).

**Normative mechanisms.** The mechanisms §4 prescribes (the state, the
owner, parking, the sequence) are normative, not guidance: the ratio of
R14 was measured with them and depends on them, and the fixtures of R11
and R13 exercise their failure paths.

**Glossary.** R*n*: requirement *n* of §4. ADR: an architecture
decision record under `docs/adr/`. C11: the 2011 ISO C standard, whose
`<stdatomic.h>` the runtime uses. Fast path: the single compare-and-swap
of the state from 0 to 1 with which a lock begins. Mutex: the library type `Mutex[T]` of spec §5.5, one
holder at a time. Condvar: the library type `Condvar` of spec §5.5.
Lock word: the integer a Mutex's holder and waiters synchronise on.
Parking: a thread sleeping in the operating system until another thread
wakes it, as opposed to spinning. Hand-off: the sequence from one
thread's unlock to the next thread's lock of the same Mutex. The
protocol: `bench/PROTOCOL.md`, which fixes each workload's operation
count, output line and measurement (five runs, median). Quiet host: a
host whose one-minute load average is below 4 and on which no other
benchmark or test run is in progress. TSan: ThreadSanitizer, the race
detector the fixture harness runs with `//! tsan:` fixtures. RSS: peak
resident set size.

## 1. Problem

Two of the thirteen workloads are below the throughput target of spec
§2 (a ratio of 0.90) in `bench/REPORT.md` of 2026-10-28: thread_pool at 0.88 of C (31.4 against
35.4 Mops/s) and lock_protected_map at 0.76 of C (26.2 against 34.6
Mops/s). Both are bound by one Mutex that four threads take once per
operation. The hosted runtime implements a Mutex as a default pthread
mutex plus an owner word, the address of a thread-local, that the holder
stores after locking and clears before unlocking, so that a thread
finding its own address there is acquiring the Mutex it holds and traps
`explicit` (spec §13 item 4). The diagnosis of 2026-10-29 (interleaved
runs of C and runtime variants, `docs/reports/perf-gaps.md`) attributes
the gaps as follows.

- The owner word sits on the cache line of the pthread mutex, the line
  that arriving waiters write with their compare-and-swap attempts. On
  lock_protected_map the holder's two stores to that line and its load
  before locking cost about 15% of throughput: a variant with no owner
  bookkeeping at all runs at 0.96 of C, and one with the owner word on a
  line of its own at 0.91.
- The owner word cannot simply move: on thread_pool, whose hand-offs are
  between parked workers rather than contended, any layout that touches a
  second line per lock costs 15% to 18%, because each hand-off migrates
  one more line between cores.
- Nothing else measurable separates the workloads from C: the
  uncontended lock path costs the same in both, link-time inlining of the
  runtime's two wrapper calls changes nothing, and rewriting the pool's
  job loop to slice its block once is worth about one point.
- A prototype that replaces the pthread mutex with the runtime's own lock
  word (a 32-bit state that waiters park on, through the operating
  system's wait-on-address call, with the owner beside it on the same
  line and written only after the acquiring exchange) runs
  lock_protected_map at 1.27 of C and thread_pool at 0.95 of C on the
  loaded host of the diagnosis, with unchanged checksums. The same
  prototype with a spin loop before parking runs thread_pool at 0.69 of
  C, because spinning makes the hand-off between workers fair and
  migrates the line on every job.

The third gap of the research report, btree's peak RSS at 1.23 of C, is
the memory of the generation a checked 8-byte handle carries; ADR-014
decided to keep the handle, so that gap is a design decision and is out
of scope here.

## 2. Definitions

- **State.** The Mutex's lock word, a 32-bit unsigned integer: 0 when
  free, 1 when held and no thread has parked since it was last free, 2
  when held and a thread has set it so before parking (a release that
  observes 2 must unpark).
- **Owner.** The identifier of the thread holding the Mutex, stored
  beside the state: `pthread_self()` of the holder, 0 when free. It is
  written only by the holder: after its acquisition of the state and
  before its release of it.
- **Park / unpark.** On macOS, `os_sync_wait_on_address` (available
  since macOS 14.4) and `os_sync_wake_by_address_any` /
  `_all`; on Linux, the `futex` system call with `FUTEX_WAIT_PRIVATE` /
  `FUTEX_WAKE_PRIVATE`. Both sleep only while the 32-bit value at the
  address equals the value the caller expected, so a wake that happens
  between the caller's check and its sleep is not lost. Both return
  without a wake at times (a spurious return, including a return with
  an error such as `EINTR`); every caller treats any return, with or
  without an error, as spurious and re-checks the value it parked on
  before parking again (R4, R8). An unpark reports at most that no
  thread was parked, which is the lost notify of spec §5.5 and is
  ignored; the other errors the primitives document (`EINVAL`,
  `EFAULT`) cannot arise, since the address is the runtime's own
  naturally aligned 32-bit word and the size and flags are constants, so
  the runtime ignores an unpark's result. A park that fails persistently
  therefore degrades to a loop of failed parks and re-checks, never to a
  lost wake or a false trap.
- **Sequence.** A Condvar's 32-bit counter, incremented by every notify;
  a waiter parks on it while it is unchanged from the value the waiter
  read under the lock. If exactly a multiple of 2^32 notifies happen
  between a waiter's read and its park, the park sees the value
  unchanged and sleeps until the next notify; this is the known
  limitation of every 32-bit sequence condition variable and is
  accepted, as the Rust standard library accepts it.
- **Ratio.** A workload's kern release throughput divided by the best of
  C and Rust, as `bench/REPORT.md` reports it; the target is a ratio of
  at least 0.90 (spec §2: within 10%).

## 3. Non-goals

- No change to the language, the checker rules (5.3.6, 5.5.4), the
  interpreter's Mutex and Condvar, the backend's calls into the runtime
  or their signatures, or the trap record a violation produces.
- No change to the benchmark programs or to the protocol. The sliced job
  loop measured during the diagnosis is recorded, not applied.
- No fairness guarantee: as with the pthread mutex it replaces, a
  releasing thread is permitted to reacquire the Mutex before a parked
  thread wakes, and the order in which parked threads acquire is
  unspecified.
- No spinning before parking.
- No deadlock detection beyond the single-thread recursive case the spec
  already requires.
- No change to btree's memory (ADR-014) or to any other workload's
  implementation.
- No support promised for hosts other than macOS 14.4 or later and
  Linux: on any other platform the runtime fails to compile with a
  message naming the missing primitive (R10).

## 4. Requirements

### 4.1 The Mutex

- **R1.** `kern_mutex_lock` returns the Mutex's data pointer once the
  calling thread holds the Mutex, and at most one thread holds a Mutex at
  any time (spec rule 5.5.4). The acquisition has acquire ordering and
  the release in `kern_mutex_unlock` has release ordering, so every write
  a holder makes before unlocking happens-before every read the next
  holder makes after locking (spec §5.5's memory model).
- **R2.** A thread that calls `kern_mutex_lock` on a Mutex it holds
  traps with kind `explicit` at the lock site's span, as today
  (`tests/trap/mutex_recursive.kern`), before any attempt to wait, so
  the program neither deadlocks nor acquires twice. The check reads the owner only after the fast path has failed, so an
  uncontended lock performs exactly one atomic read-modify-write on the
  state and no load of the owner.
- **R3.** The owner is written only by the holder: set after the
  acquisition that made it the holder, cleared before the release. A
  thread therefore never finds its own identifier in the owner of a Mutex
  it does not hold, and R2 never traps a thread that is merely contending
  with another.
- **R4.** A thread that fails the fast path and is not the holder
  parks until the Mutex is released, then acquires it: it exchanges the
  state to 2 (acquiring at once if the previous state was 0) and parks
  while the state is 2, repeating the exchange after every return from
  the park, so that it acquires as 2 and a release that observes 2
  unparks one thread. No wake is lost: a release that observes 2 always
  unparks, and a thread parks only while the state is still 2. A park
  that returns with an error is a spurious return: the thread repeats
  the exchange.
- **R5.** `kern_mutex_unlock` clears the owner, releases the state and,
  if the state it released was 2, unparks one parked thread. It performs
  no system call when the state it released was 1.
- **R6.** The Mutex's allocation begins with a header of exactly 24
  bytes: the state at offset 0 (4 bytes), 4 bytes of padding, the owner
  at offset 8 (8 bytes) and the element size at offset 16 (8 bytes); the
  data begins at offset 24. The allocation comes from the runtime's
  allocator (`malloc`), whose alignment is 16 bytes on every target the
  backend names (`arm64-apple-macosx`, `x86_64-apple-macosx`,
  `aarch64-unknown-linux-gnu`, `x86_64-unknown-linux-gnu`), with no
  padding to a line boundary. Rationale, not a further requirement: the state and the
  owner both lie in the first 16 bytes, so for every 16-byte-aligned
  allocation they lie in one 128-byte cache line (the line size of the
  Apple M-series hosts the protocol measures on), which is what the
  diagnosis of §1 found the pool needs, and the data follows at the
  smallest offset the header allows.
- **R7.** `kern_mutex_drop` runs the element's drop glue and frees the
  allocation; no operating-system object remains to destroy.

### 4.2 The Condvar

- **R8.** `kern_condvar_wait(cv, data)` traps `explicit` at the wait
  site's span if the calling thread does not hold the Mutex whose data
  pointer is `data`, or if `cv` has been waited on with a different Mutex
  (`tests/trap/condvar_mismatch.kern`), as today. Otherwise it reads the
  sequence, releases the Mutex as `kern_mutex_unlock` does, parks while
  the sequence is unchanged, reacquires the Mutex as a contending thread
  does (R4) and returns with the calling thread the holder (owner set).
  A notify that happens after the sequence was read and before the park
  is not lost (the park returns at once because the sequence changed),
  and a return without a notify, including a park that returns with an
  error, is a spurious wakeup, which spec §5.5 permits: `wait` returns
  and the program re-tests its condition. The Mutex bound to `cv` is an atomic pointer-sized word, 0 until the
  first wait; the check loads it with relaxed ordering and, only when
  the load returns 0, performs one sequentially consistent
  compare-and-swap from 0 to the calling Mutex; if that fails, the
  value it found must be the calling Mutex, else the wait traps. Two
  waits on one Condvar never run concurrently with the same Mutex,
  since a wait holds its Mutex; concurrent first waits with different
  Mutexes race on the compare-and-swap and exactly one of them traps.
- **R9.** `kern_condvar_notify_one` increments the sequence and unparks
  one thread parked on it; `kern_condvar_notify_all` increments it and
  unparks every thread parked on it. A notify with no waiter is lost,
  as the spec says. Neither takes the Mutex.
- **R10.** `kern_condvar_new` cannot fail other than by allocation,
  which traps as every runtime allocation does; `kern_condvar_drop`
  frees the allocation. The runtime compiles on macOS 14.4 or later and
  on Linux; on another platform the preprocessor stops the build with an
  `#error` naming the park primitive the platform lacks.

### 4.3 Behaviour that must not change

- **R11.** Every fixture under `tests/` passes unchanged, in particular
  `tests/trap/mutex_recursive.kern` and its twin,
  `tests/trap/condvar_mismatch.kern` and its twin,
  `tests/concurrency/mutex_counter.kern`,
  `tests/concurrency/condvar_handoff.kern` and
  `tests/concurrency/shared_handoff.kern` (the last three also under
  TSan, `//! tsan: clean`), and `tests/native_test.go`'s
  `TestNativeStackTrapUnderMutexWithHandler`.
- **R12.** Every workload's checksum in `bench/REPORT.md` is unchanged:
  thread_pool 14753395421204281632 and lock_protected_map
  1619761243042050329 in every row.
- **R13.** A new fixture under `tests/concurrency/` exercises the parked
  path and the Condvar together under TSan (`//! tsan: clean`): several
  threads take one Mutex many times each with a critical section long
  enough that waiters park, and hand a value through a Condvar in both
  directions; its expected output is the deterministic total.

### 4.4 Measurement and documents

- **R14.** Measured as the protocol prescribes (`go run ./bench/harness`,
  five runs, median) on a quiet host, thread_pool and lock_protected_map
  each reach a ratio of at least 0.90, and no workload that met the
  throughput target in `bench/REPORT.md` of 2026-10-28 falls below 0.90.
  The report is regenerated by the harness, never edited by hand. If
  the host is not quiet when the report is regenerated, the report of
  R15 says so and gives the load average, and the acceptance of this
  spec waits for a quiet run.
- **R15.** `docs/reports/perf-gaps.md` records the diagnosis of §1 with
  its measurements (same-round ratios, with the host load), the design,
  the measured result and what remains. `docs/unsafe-audit.md`'s row for
  `Mutex`, `docs/security-review.md`'s row for `Mutex` and the closing
  paragraph of `docs/research-report.md` describe the runtime-owned lock
  instead of a pthread mutex. `docs/workloads/lock_protected_map.md` and
  `docs/workloads/thread_pool.md`, where they describe the kern
  implementation's lock, say the same.

## 5. Implementation constraints

- The lock, unlock and wait paths use C11 atomics with the orderings of
  R1; the sequence counter and the owner use relaxed ordering, since the
  Mutex provides the happens-before edges they need.
- The runtime stays one C file compiled with the program (no new
  dependency, no new link flag); the macOS path includes
  `<os/os_sync_wait_on_address.h>`, the Linux path `<linux/futex.h>` and
  `<sys/syscall.h>`.
- The thread identifier is `pthread_self()`, not the address of a
  thread-local: the runtime's own lock, unlock and wait code declares
  and reads no thread-local variable (what the C library does inside
  `pthread_self()` is its own).
- The output lock the runtime's own printing uses (`kern_outlock`) is
  unrelated and stays a pthread mutex.
- The Linux path cannot be run on the development host; it is compiled
  only by review, and the report says so.

## 6. Acceptance criteria

1. `go test ./...` passes, including the fixture harness with the new
   fixture of R13 and the TSan fixtures of R11.
2. `golangci-lint run ./...` is clean (the change is C; this gates the
   Go that is touched, if any).
3. `bench/REPORT.md`, regenerated on a quiet host, shows thread_pool and
   lock_protected_map at a ratio of at least 0.90 with the checksums of
   R12, and no regression of R14 elsewhere.
4. `docs/reports/perf-gaps.md` and the documents of R15 exist and say
   what R15 requires.
