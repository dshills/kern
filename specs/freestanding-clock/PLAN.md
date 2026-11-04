# Plan: a monotonic clock on bare metal

Implements `specs/freestanding-clock/SPEC.md` v0.2 in one phase and one
commit.

## Phase 1

**Goals.** R1 to R5.

**Files.**
- `runtime/freestanding/src/rt.c`, at the end:
  - `kern_ticks_to_ns`, the split conversion, treating a zero frequency
    as ticks equal to nanoseconds.
  - `kern_now_ns` on AArch64: `isb` and `cntvct_el0`, then
    `cntfrq_el0`.
  - On x86-64:
    - `kern_rdtsc` (`lfence; rdtsc`);
    - `kern_tsc_calibrate`: PIT channel 2, latch 11,932, polling port
      `0x61` bit 5, bounded at 2^32 TSC ticks with 1 GHz assumed after;
    - `kern_now_ns`: calibrate on the first call, then the ticks since
      calibration converted.
  - `kern_time_sleep_ns`: a busy-wait on `kern_now_ns`; zero returns at
    once.
- `std/time.kern` and `compiler/resolve/prelude/time.kern` (identical),
  and the `now_ns` comment in both `core.kern` files: the hosted-only
  notes now say which parts a freestanding image has.
- Tests:
  - `tests/freestanding/clock.kern` with `.out`: 1,000 readings never
    decrease, a 20 ms sleep measures at least 20 ms, and a zero sleep
    returns. Its output is booleans and counts only.
  - The hosted-only examples change: `tests/freestanding/hosted_only.kern`
    uses `time.wall_nanos`. In `TestHostedOnlyR1`, the reachability
    cases, `TestHostedOnlyLibraryCycle`, `TestHostedOnlyNoPosition`,
    `TestHostedOnlyMatchesLinker` and the `cmd/kern` hosted-only tests:
    - `time.now` becomes `time.wall_nanos`;
    - `now_ns()` becomes `io.io_argc()`, a runtime intrinsic called
      directly, since no core intrinsic stays hosted-only;
    - `time.sleep` becomes `time.wall_nanos` in the program's own
      function.

    The expectations are otherwise unchanged.
  - The generated inventory is regenerated.
- Documents: `docs/reports/freestanding-clock.md`, and a note on the
  std-time report's open item.

**Key decisions.**
- **TSC over the PM timer.** The ACPI PM timer's port depends on how
  firmware set the chipset's PM base, and it wraps every 4.7 s. The PIT
  is at fixed ports on every PC.
- **Calibration on first use.** Programs that never read the clock pay
  nothing.

**Acceptance.**
- `clock.kern` gives the same transcript in the interpreter and under
  QEMU on both targets, as the harness checks.
- `TestProvidedMatchesObjects` passes with the two entries.
- `go test ./...`, `golangci-lint run ./...` and `gofmt -l` are clean.

**Risks.** An emulator without a PIT would make the first reading take
up to 2^32 TSC ticks, which the bound keeps finite.
