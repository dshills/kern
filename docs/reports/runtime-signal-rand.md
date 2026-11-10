# Runtime, phase 9: signals and randomness

**Date:** 2026-11-09. **Spec:** `specs/runtime/SPEC.md` v0.6, area I (RI1,
RI2). **Plan:** `specs/runtime/PLAN.md`, phase 9. **Hosts:** macOS 27.2 on
arm64 and Ubuntu 24.04 on arm64 in the `kern-linux-check` container.

## What changed

- **`std/signal` (RI1).** `Signal` names eight signals. `ignore` and
  `reset` set a disposition, `raise` sends a signal to the program's own
  process, and `catch` returns a `Signals`, which `add` extends and `wait`,
  `wait_timeout` and `try_next` read.
  - A caught signal is recorded, not acted on. The runtime's handler sets
    a bit and writes a byte to a pipe; a wait reads the bits and, finding
    none it wants, polls the pipe. No code of the program runs at the
    moment of delivery, so there is nothing a handler could do wrong.
  - Deliveries of one signal while its bit is set are one. Signals that
    wait together come out in the order `Signal` declares them.
  - One `Signals` exists at a time. Destroying it gives each signal the
    disposition it had when it was caught and discards what was recorded.
  - The pipe is made at the first `catch`: a program that catches nothing
    has no descriptor for it.
- **`std/rand` (RI2).** `fill`, `u64` and `below` draw from the system's
  generator (`getentropy`, in requests of at most 256 bytes). `Rng` is
  xoshiro256** with its state filled from a seed by splitmix64:
  `seeded`, `from_os`, `from_state`, `state`, `next_u64`, `below`
  (rejection, so every value is as likely as any other), `fill` and
  `shuffle`. `Rng` is kern code and needs no runtime: it works on bare
  metal.
- **Blocked calls and signals.** With handlers in the program, a system
  call can now be interrupted. The loops phases 2, 5, 7 and 8 wrote for
  that are exercised: accept, read, sleep and a condition variable's wait
  each end as they would have without the signals.

## Language change

- **`swap_at(xs, i, j)` in the prelude.** There was no way to exchange
  two elements of a sequence whose type cannot be copied, which a generic
  `shuffle` needs. `swap_at` exchanges the elements' bytes and traps
  `bounds` on a bad index; it is one entry in each runtime
  (`kern_swap`), so it works freestanding (`tests/run/swap_at.kern`,
  `tests/trap/swap_at_bounds.kern`, `spec/types.md` §1).

## Requirement to test

| Clause | Test |
|---|---|
| RI1: each of the eight signals raised, caught and returned by `wait` | `tests/run/signal_basics.kern` (`each`), both engines |
| RI1: deliveries of one signal while pending are one | same |
| RI1: several pending signals come in `Signal`'s order | same (User2, Quit, Interrupt, Hangup raised; Interrupt, Hangup, Quit, User2 returned) |
| RI1: `wait_timeout` gives up after its duration and not before; zero looks once; `try_next` | same |
| RI1: `add` of a held signal does nothing | same |
| RI1: `Child` when a child ends | same |
| RI1: `ignore`: a raised signal does nothing | same (`dispositions`) |
| RI1: destroying `Signals` restores the earlier disposition (ignored again, not the default, which would end the program) and discards recorded deliveries | same |
| RI1: a second `catch` while one is live traps `explicit` | `tests/trap/signal_second_catch.kern` and twin |
| RI1: `ignore` of a held signal traps `explicit`; of another signal, and after the `Signals` is gone, it does not | `tests/trap/signal_ignore_held.kern` and twin |
| RI1: signals sent from outside the program are caught and returned | `TestSignalsFromOutside`, native and interpreter |
| RI1: an ignored signal sent from outside does nothing; after `reset` it ends the program by that signal | `TestSignalIgnoredThenDefault`, native and interpreter |
| RI1: children start with std/process's dispositions whatever the parent ignored or caught | `tests/run/signal_children.kern` |
| RI1, RH4: `wait` and every other blocked call is retried when interrupted | `tests/concurrency/signal_interrupts.kern` (30 signals during each of accept, read, sleep and a condition variable's wait; also under the thread sanitizer) |
| RI2: splitmix64's and xoshiro256**'s published sequences | `tests/run/rand_rng.kern`; its expected output is computed from the published algorithms by a program outside kern, and the three engines reproduce it |
| RI2: `seeded`: the same seed, the same sequence, in both engines | same |
| RI2: `below` against a table; its rejection loop at 2^64 - 1 and at 2^63 + 1 | same |
| RI2: `fill` (eight bytes a number, low byte first, a partial last number); `shuffle` of a fixed array, of one and of no element, of a move-only type | same |
| RI2: `below(0)` traps `explicit` | `tests/trap/rand_below_zero.kern` and twin |
| RI2: the system's generator at lengths 0, 1, 255, 256, 257 and 4,096; two fills of 64 bytes differ; `u64`, `below`, `from_os` | `tests/run/rand_os.kern` |
| RI2: the generator failing: `fill` is `Err`, a fill whose second request fails is `Err`, `from_os` is `Err`, the next call works | `tests/run/rand_os_fails.kern` (the hook `testing.fail_random`) |
| RI2: `u64` traps `explicit` when the generator fails | `tests/trap/rand_u64_source_fails.kern` and twin |
| RI2: `Rng` freestanding | `tests/freestanding/rand_rng.kern`, in the emulation and under QEMU |

## Deviations and open points

- **The interpreter is a Go program**, and for signals that shows:
  - a signal at its default disposition does what the Go runtime does:
    `Interrupt`, `Terminate` and `Hangup` end the program, `Quit` ends it
    with the runtime's stack dump, and `User1`, `User2` and `Pipe` do
    nothing, where the system would end the program;
  - a `Pipe` signal sent by another process is kept by the Go runtime and
    not reported (one the program raises itself is);
  - an ignored signal is received and dropped rather than discarded by
    the system, so that children do not inherit the disposition;
  - `reset` cannot give the default to a signal the program was started
    ignoring.
- **`raise` is delivered to the process**, so in a program with several
  threads any of them may take the signal, and a moment may pass before
  it is recorded.
- **`Rng.from_state` and `Rng.state`** are additions: a sequence can be
  saved and taken up again, and the published test vectors need a state.
- **`rand.u64`** is written `rand.u64()`; inside `std/rand` the name is
  the conversion, so the work is in `system_u64`.

## Verification

`go test ./...` passes on macOS; the phase's fixtures and tests, the
area-A checks and the sanitized corpus pass in the Linux container;
`golangci-lint run ./...` reports no issue. The signal fixtures were
repeated natively without a differing run.
