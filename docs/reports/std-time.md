# `std/time`: clocks, durations, deadlines and sleep

**Date:** 2026-11-01. **Spec:** `specs/std-time/SPEC.md` v0.1.
**Plan:** `specs/std-time/PLAN.md`. **Item:** the fourth of the ten-item
list ("no clock, sleep or deadline; `std/process` deferred timeouts to a
later `std/time`").

## Before

The prelude had `now_ns() -> u64`, monotonic natively but, in the
interpreter, Go's `time.Now().UnixNano()`: the wall clock, which goes
backwards when the system time is set. There was no sleep (only
`yield_now`) and no wall clock.

## The module

`std/time` (embedded, `resolve.StdModules`) is kern over two new runtime
entries and the prelude's `now_ns`: `Duration`, `Instant` and `Deadline`
are one-field `Copy` structs; `nanos`, `micros`, `millis`, `seconds`
build durations with checked arithmetic (past `u64` nanoseconds is an
`overflow` trap); `minus`, `since` and `remaining` saturate at 0;
`after(d)` is a deadline, `expired()` is `now >= at`; `sleep(d)` and
`wall_nanos()` are the runtime entries.

- **Runtime.** `kern_time_sleep_ns` loops on `nanosleep` with the
  remainder after `EINTR`; `kern_time_wall_ns` is
  `clock_gettime(CLOCK_REALTIME)`. Either traps `explicit` on any other
  failure, which the arguments make impossible; the analysis's
  `trapping` table and `TestTrappingMatchesRuntime` list both with
  `explicit`.
- **Interpreter.** `time_sleep_ns` sleeps in pieces of at most
  `math.MaxInt64` ns (a `u64` duration can exceed one `time.Duration`;
  `sleepChunks` is unit-tested at `u64` max without sleeping);
  `time_wall_ns` is `time.Now().UnixNano()`; `now_ns` is now the wall
  clock at the first reading plus Go's monotonic time since, so it never
  decreases and stays in the range programs saw before.
- **Hosted only**, like `std/io`: a freestanding build importing
  `std/time` fails to link (`undefined symbol: kern_time_sleep_ns`),
  checked by hand. The link error surfaces as an `INTERNAL` diagnostic,
  as `std/io`'s does; a clearer diagnostic for a hosted-only module in a
  freestanding build is an open item.

## Verification

| Requirement | Evidence |
|---|---|
| R1 module | `TestStdModulesCheck` and `TestPreludeSync` over every listed module |
| R2 values | `tests/run/std_time`: 1,500 ms is 1,500,000,000 ns; 3 s is 3,000 ms; 7 µs + 3 ns; 2 ms − 5 ms is 0; `since` of a later instant is 0; `remaining` of an expired deadline is 0 (all four configurations); `tests/trap/time_overflow` (`seconds(u64 max)` traps `overflow` at the user's call) and its twin |
| R3 sleep, clock | `std_time`: a 20 ms sleep measures at least 20 ms; 1,000 readings never decrease; a 10 s deadline is not expired at once; a 10 ms deadline is expired after 20 ms; `sleep(nanos(0))` returns. On this host a native 20 ms sleep averages 20.75 ms over 20 runs |
| R4 wall clock | `std_time`: between 2020 and 2100 |
| R5 interpreter `now_ns` | `TestMonotonicNow` (10,000 readings never decrease; within a second of the wall clock), `TestSleepChunks` |

## Open items

- Timeouts in `std/process` (`wait` with a deadline) and `std/io`.
- A clock for freestanding mode (a timer device per target).
- A diagnostic, not a link error, for a hosted-only module in a
  freestanding build.

## Review

- **speccritic** (gpt-6.1-sol): one critical finding (no response for a
  clock or sleep failure; resolved: an `explicit` trap the analysis
  lists); warnings folded in (the expiry comparison, a timing bound a
  loaded host could break, host clock conditions, mechanism in normative
  text).
- **plancritic** (davin-go): no critical finding; warnings folded in
  (chunked sleep, zero duration, the interpreter's failure path, the
  freestanding check, the report's contents).
- **R6.** Baseline: `scripts/capture-ll.sh` with a compiler built from
  f49cf88 before any edit (304 `.ll` files, 46 failure lines). After: all
  304 byte-identical, the same 46 failure lines, no other baseline
  program added; new: `std_time`, `time_overflow` and its twin (6 `.ll`
  files).
- **prism** (default settings, medium effort): one medium and three low
  findings, none adopted: the prelude/`std` copy of `time.kern` is the
  repository's required mirror (`TestPreludeSync`); the interpreter's
  year-2262 limits are Go's `UnixNano` range, far past any use; 32-bit
  `time_t` is not a supported host (macOS, Linux x86-64 and arm64).
- **realitycheck** PARTIALLY_ALIGNED, score 100, no warning;
  **verifier** offline: 17 low findings, none on this change; clarion
  skipped. `golangci-lint` (after removing a comparison staticcheck
  flagged as always false in `TestSleepChunks`) and
  `KERN_REQUIRE_QEMU=1 go test ./...` clean.
