# A monotonic clock on bare metal

**Date:** 2026-11-04. **Spec:** `specs/freestanding-clock/SPEC.md` v0.2
(speccritic VALID_WITH_GAPS, no critical finding). **Plan:**
`specs/freestanding-clock/PLAN.md`. **Follows:** the std-time report's
open item, "a clock for freestanding mode (a timer device per target)".

## What changed

The freestanding runtime now defines `kern_now_ns` and
`kern_time_sleep_ns`. A freestanding program can therefore use
`now_ns`, `time.now`, `Instant`, `Deadline` and `time.sleep`, which
were `ENV_HOSTED_ONLY` before. `time.wall_nanos` stays hosted-only.

- **AArch64 (QEMU `virt`).** The generic timer: `CNTVCT_EL0`, read after
  an `isb`, divided by `CNTFRQ_EL0`. A zero frequency counts ticks as
  nanoseconds.
- **x86-64 (QEMU `q35`).** The TSC, calibrated on the first reading
  against the PIT, using Linux's method:
  - channel 2 runs one-shot for 11,932 ticks (10 ms) while the TSC is
    read before and after;
  - waiting is bounded at 2^32 TSC ticks, after which 1 GHz is assumed.

  The PM timer was not used: its port depends on firmware, and it wraps
  every 4.7 s.
- **Sleep** busy-waits on the clock, since no interrupts are enabled.

The split conversion `(c / f) * 10^9 + (c % f) * 10^9 / f` keeps its
intermediate in range for frequencies under 18 GHz. The result fits a
`u64` for 584 years of counter time.

## Tests

- **`tests/freestanding/clock.kern`** checks that 1,000 readings never
  decrease, that a 20 ms sleep measures at least 20 ms, and that a zero
  sleep returns. It gives the same transcript in the interpreter's
  emulation and under QEMU on both targets.
- **A hand probe** printed the measured sleep: 20 ms in the interpreter
  and under QEMU on both targets.
- **The hosted-only examples** moved off the clock:
  - `tests/freestanding/hosted_only.kern` and the analysis tests use
    `time.wall_nanos`;
  - the case that calls a runtime intrinsic directly uses `io.io_argc()`,
    because no core intrinsic stays hosted-only;
  - the analysis and command tests (`TestHostedOnlyR1`, the
    reachability cases, the library cycle, the linker match,
    `TestHostedOnlyCommands`) keep their expectations otherwise.
- **The inventory**, regenerated, no longer lists `time.now`,
  `time.after`, `time.sleep`, `Instant.elapsed` or `Deadline.*`. The
  timeout functions no longer reach `kern_now_ns` or
  `kern_time_sleep_ns`.

## Image size

The clock is in `rt.c`, which every image links, so every freestanding
image now carries it. `hello`'s text grows from 4,792 to 4,948 bytes on
AArch64 and from 5,378 to 5,666 bytes on x86-64. Unlike the float
entries (`f64text.c`), it is not linked lazily: on x86-64 it shares
`rt.c`'s port helpers, and the cost is a few hundred bytes. A separate
lazily linked object is possible if image identity matters.

## Found on the way

A hosted-only diagnostic for a module's runtime intrinsic names it
without its module: ``call to `io_argc` ``, not `io.io_argc`. Intrinsic
names carry no module. It is cosmetic and left as it is.

## Not done

A wall clock on bare metal (a real-time-clock device), and timer
interrupts or a halted CPU while sleeping.
