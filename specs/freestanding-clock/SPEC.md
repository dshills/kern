# A monotonic clock on bare metal

**Version:** 0.2 (2026-11-04). **Review status.** speccritic
(gpt-6.1-sol): VALID_WITH_GAPS, no critical finding; the conversion's
range is stated and the fixture's sleep has no upper bound. **Scope:**
- the freestanding runtime (`runtime/freestanding/src/rt.c`): two entries;
- the freestanding fixtures (`tests/freestanding/`);
- the hosted-only tests and fixtures that used the clock as their
  example;
- the generated inventory in `docs/reports/freestanding-availability.md`;
- the open items of `docs/reports/std-time.md`;
- the report `docs/reports/freestanding-clock.md`.

**Language impact:** none. **Follows:** `specs/std-time/SPEC.md`, whose
report lists "a clock for freestanding mode (a timer device per target)"
as open; `specs/freestanding-availability/SPEC.md` (Rule 6.8).

**Glossary.**
- R*n*: requirement *n* of §3.
- *The two entries*: `kern_now_ns() -> u64`, the monotonic clock behind
  `now_ns`, `time.now`, `Instant` and `Deadline`; and
  `kern_time_sleep_ns(ns, at)`, behind `time.sleep`.
- *The targets*: `aarch64-unknown-none-elf` (QEMU `virt`) and
  `x86_64-unknown-none-elf` (QEMU `q35`).
- *The generic timer*: AArch64's `CNTVCT_EL0` counter and `CNTFRQ_EL0`
  frequency, readable at EL1.
- *The TSC*: x86-64's time-stamp counter (`rdtsc`).
- *The PIT*: the 8254 programmable interval timer at 1,193,182 Hz.

## 0. Goal and non-goals

A freestanding program must be able to read a monotonic clock and sleep,
with the same `std/time` API as a hosted one. Today the two entries are
hosted-only, so such a program is rejected with `ENV_HOSTED_ONLY`.

Non-goals:
- **A wall clock.** `time.wall_nanos` (`kern_time_wall_ns`) stays
  hosted-only; a real-time clock is another device.
- **Timer interrupts or a sleeping CPU.** `sleep` busy-waits.
- **Accuracy beyond the device.** The clock is monotonic and advances at
  the device's rate; under an emulator that rate follows the host's
  clock as the emulator chooses.

## 1. Background

The freestanding runtime runs one thread at EL1 on AArch64 and in long
mode on x86-64, with no interrupts enabled. It already uses port I/O
helpers on x86-64 (`kern_outb`, `kern_inb`).

## 2. Design (informative)

- **AArch64.** `kern_now_ns` reads `CNTVCT_EL0` after an `isb`, and
  converts it with `CNTFRQ_EL0`. The conversion is
  `(c / f) * 10^9 + (c % f) * 10^9 / f`. Its intermediate
  `(c % f) * 10^9` cannot overflow for any frequency below 18 GHz, and
  the result fits a `u64` for 2^64 ns (about 584 years) of counter
  time. The clock is specified within that range only.
- **x86-64.** `kern_now_ns` reads the TSC.
  - On the first call it calibrates the TSC against the PIT: channel 2
    in one-shot mode for 11,932 ticks (10 ms), with the TSC read before
    and after, as Linux's `pit_calibrate_tsc` does.
  - The frequency is stored, and nanoseconds are counted from the first
    call's reading with the same split conversion.
  - The first call therefore takes about 10 ms.
- **Sleep.** On both targets `kern_time_sleep_ns` loops until
  `kern_now_ns` has advanced by `ns`.
- **Availability.** `freestanding.Provided()` includes both entries, so
  `ENV_HOSTED_ONLY` no longer fires for `now_ns`, `time.now`,
  `Instant`, `Deadline` and `time.sleep`.
  - The fixtures and tests that used `time.now` as their example of a
    hosted-only call switch to `time.wall_nanos`, which stays
    hosted-only.

## 3. Requirements and acceptance criteria

**R1. Monotonic.** On both targets, successive `now_ns` readings never
decrease. The fixture `tests/freestanding/clock.kern` checks this:
- it takes 1,000 readings in a loop and checks each against the last;
- it measures a `time.sleep(time.millis(20))`, checking that the
  measured duration is at least 20 ms. There is no upper bound, since a
  paused emulator or a loaded host may stretch it;
- it prints only booleans and counts, so its output is deterministic.

It runs in the interpreter's freestanding emulation and under QEMU on
both targets, with the same transcript.

**R2. Sleep.** `time.sleep(d)` returns after `now_ns` has advanced by at
least `d.ns` (checked by R1's fixture). A zero duration returns at once.

**R3. Availability.** A freestanding `check`, `build` and `run` accept
programs that use `now_ns`, `time.now`, `Instant`, `Deadline` and
`time.sleep`. `time.wall_nanos` is still `ENV_HOSTED_ONLY`.
`freestanding.Provided()` matches the objects (the existing
`TestProvidedMatchesObjects`). The generated inventory no longer lists
the clock functions.

**R4. The hosted-only examples.** `tests/freestanding/hosted_only.kern`
and the analysis and command tests that used `time.now` as a hosted-only
call use `time.wall_nanos` instead, with the same expectations
otherwise. The hosted runtime is unchanged.

**R5. Documents.** The std-time report's open item points here, and the
report records the devices, the calibration and the QEMU runs.

## 4. Failure modes

- **A PIT that never fires** (an emulator without it): the calibration
  loop would spin forever. It is bounded at 2^32 TSC ticks of waiting.
  After that it assumes 1 GHz and the clock still advances.
- **A zero `CNTFRQ_EL0`** (firmware that did not set it): `now_ns`
  counts ticks as nanoseconds rather than dividing by zero.
