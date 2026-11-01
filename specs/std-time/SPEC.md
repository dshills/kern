# `std/time`: clocks, durations, deadlines and sleep

**Version:** 0.1 (2026-11-01). **Scope:** a new embedded module
`compiler/resolve/prelude/time.kern` (mirrored in `std/time.kern`, listed
in `resolve.StdModules`); two runtime entries in
`runtime/hosted/src/kern_rt.c` with their backend calls
(`compiler/backend/llvm/calls.go`) and interpreter intrinsics
(`compiler/interp/intrinsics.go`); the interpreter's `now_ns`; fixtures;
documents. **Language impact:** none.
**Related:** `docs/reports/std-process.md` ("no timeouts or sleep (a
later `std/time`)"); `spec/types.md` §1 (the `std/*` modules); the
prelude's `now_ns` (a monotonic clock, used by the benchmarks).

**Review status.** speccritic (gpt-6.1-sol): one critical finding (no
response for a clock or sleep failure; resolved: an `explicit` trap,
listed by the analysis) and the warnings (the expiry comparison, a
timing bound a loaded host could break, host clock conditions, mechanism
in normative text) folded in.

**Glossary.** R*n*: requirement *n* of §4. Monotonic clock: the clock
`now_ns` reads (natively `CLOCK_MONOTONIC_RAW` on macOS,
`CLOCK_MONOTONIC` elsewhere), which never goes backwards. Wall clock:
`CLOCK_REALTIME`, nanoseconds since the Unix epoch, which may jump.
Hosted: a program run under an operating system (not `--env
freestanding`). Abbreviations: ns, nanoseconds; ms, milliseconds.

## 0. Goals

Programs and libraries can measure elapsed time, wait, and express
deadlines with a small typed interface instead of raw `now_ns`
arithmetic, in both engines. Non-goals: calendars, time zones, date
formatting, timers or alarms, cancellation, timeouts inside `std/io` or
`std/process`, and a freestanding clock.

## 1. Background

The prelude has `now_ns() -> u64`, monotonic natively; the interpreter
implements it with Go's `time.Now().UnixNano()`, which is the wall
clock, so the interpreter's `now_ns` can go backwards when the system
time is set. There is no way to sleep (only `yield_now`) and no wall
clock.

## 2. Design

```kern
module time
struct Duration { ns: u64 }           // Copy
struct Instant { ns: u64 }            // Copy: a monotonic reading
struct Deadline { at: u64 }           // Copy: a monotonic instant

func nanos(n: u64) -> Duration
func micros(n: u64) -> Duration
func millis(n: u64) -> Duration
func seconds(n: u64) -> Duration
impl Duration {
    func as_nanos(view self) -> u64
    func as_millis(view self) -> u64      // truncating
    func plus(view self, d: Duration) -> Duration
    func minus(view self, d: Duration) -> Duration   // 0 when d is longer
    func less(view self, d: Duration) -> bool
}
func now() -> Instant
impl Instant {
    func elapsed(view self) -> Duration           // now() since self
    func since(view self, earlier: Instant) -> Duration   // 0 if earlier is later
    func plus(view self, d: Duration) -> Instant
}
func after(d: Duration) -> Deadline                // now() + d
impl Deadline {
    func expired(view self) -> bool
    func remaining(view self) -> Duration          // 0 once expired
}
func sleep(d: Duration)
func wall_nanos() -> u64                           // the wall clock
```

- **Overflow.** The constructors and `plus` use checked `u64` arithmetic
  and so trap `overflow` past `u64` (about 584 years), like any kern
  arithmetic; `minus`, `since` and `remaining` saturate at 0.
- **`sleep(d)`** returns after at least `d` has passed on the monotonic
  clock, including when a signal interrupts the wait; a zero duration
  returns at once.
- **`wall_nanos()`** reads the wall clock.
- **`Deadline.expired()`** is `now().ns >= at`; `remaining()` is
  `at - now().ns`, or 0 when expired.
- **Failure.** For the arguments the runtime passes, reading either clock
  and waiting cannot fail except by a signal's interruption, which sleep
  resumes. Any other failure of the underlying call is a runtime bug and
  traps `explicit` at the call (natively through `kern_trap_loc`, in the
  interpreter by `it.trap`); the effect analysis lists `explicit` for both
  entries.
  *Implementation guidance (non-normative):* `kern_time_sleep_ns` loops on
  `nanosleep` with the remaining time after `EINTR`; `kern_time_wall_ns`
  is `clock_gettime(CLOCK_REALTIME)`; the interpreter uses Go's
  `time.Sleep` and `time.Now().UnixNano()`.
- **The interpreter's `now_ns`** becomes monotonic: the nanoseconds since a
  fixed process start reading of Go's monotonic clock, plus that start's
  wall-clock value, so it never goes backwards and stays in the same range
  as before.
- **Hosted only**, like `std/io`: the freestanding runtime defines neither
  entry, so a freestanding program importing `std/time` fails to link,
  as one importing `std/io` does.

## 3. Out of scope

Calendars, formatting, time zones; a freestanding clock; timeouts in
`std/process` (`wait` with a deadline) and `std/io`; a `Duration` with
sub-nanosecond or negative values.

## 4. Requirements

**R1. Module.** `std/time` is embedded, listed in `StdModules`, mirrored
in `std/time.kern`, and checks (`TestStdModulesCheck`). *Verified by:*
the existing std module tests, which cover every listed module.

**R2. Durations and instants.** The constructors, accessors, `plus`,
`minus`, `less`, `since`, `plus` on `Instant` and `Deadline.remaining`
give the values of §2, saturating where it says. *Verified by:*
`tests/run/std_time.kern` (all four configurations) printing the
deterministic results (`millis(1500).as_nanos()`, `minus` past zero,
`since` of a later instant, `remaining` of an expired deadline) and a
trap fixture `tests/trap/time_overflow.kern` (`seconds` of `u64` max
traps `overflow`) with its completing twin.

**R3. Sleep and the monotonic clock.** `sleep(millis(20))` returns after
at least 20 ms by `now()`; consecutive `now()` readings never decrease;
a deadline 10 s ahead is not expired at once (no host delays a check by
10 s); a deadline 10 ms ahead is expired after a 20 ms sleep. *Verified by:* `std_time.kern` printing the results as
`true`/`false`, in both engines.

**R4. Wall clock.** `wall_nanos()` is after 2020-01-01 (1577836800 s) and
before 2100-01-01, on a host whose clock is set to the present (the test
hosts and CI are). *Verified by:* `std_time.kern`.

**R5. Interpreter `now_ns` monotonic.** *Verified by:* an interpreter
unit test that reads `now_ns` many times and checks it never decreases,
and, on a host whose wall clock is not adjusted while the test runs,
that its value is within a second of Go's `time.Now().UnixNano()`.

**R6. Existing programs unchanged.** The LLVM IR of the
`scripts/capture-ll.sh` corpus is byte-identical (no existing program
imports `std/time`; `now_ns` keeps its runtime entry). *Verified by:* a
baseline capture before the change and `cmp`.

**R7. Documents.** README's standard-library row and `spec/types.md` §1
name `std/time`; `docs/reports/std-process.md`'s note on a later
`std/time` points to it; `docs/reports/std-time.md` records the design,
the measurements of R3 on this host, and the review. *Verified by:*
review; `go test ./tests`.

## 5. Acceptance criteria

`golangci-lint run ./...` and `KERN_REQUIRE_QEMU=1 go test ./...` clean;
R6 byte-identical.

## 6. Risks

- **Timing on a loaded host.** R3 checks lower bounds (at least 20 ms)
  and one generous upper bound (a check within 10 s), so a loaded host
  does not fail it.
- **Interrupted sleep.** `nanosleep` returns early on a signal; the
  runtime loops on the remaining time.
