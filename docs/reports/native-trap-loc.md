# Native locations for stack traps and faults in safe code

**Date:** 2026-10-31. **Spec:** `specs/native-trap-loc/SPEC.md`.
**Finishes:** the "Not done" of `docs/reports/runtime-trap-file.md`.

## Before

A hosted native `stack` record, and an `unreachable` record from a fault
in safe code, carried span 0-0 and the program's file. The fault handler
knew no source location (ADR-012).

## What changed

- **Markers.** Every hosted kern function sits in a kern text section
  behind a 16-byte prefix: a magic word, then the index of its
  declaration in a location table (-1 for a library function).
  - The entry wrapper carries `main`'s index and hands the table to the
    runtime.
  - Test functions are called `noinline` from the `kern test`
    dispatcher.
- **Lookup.** The fault handler tries, in order:
  1. the faulting instruction;
  2. the link register, or else the word at the stack pointer;
  3. the frame-pointer chain.

  For each, it scans back to the nearest marker, and the first user
  function found names the record.
- **Safe reads.** The handler reads only the kern text section and stack
  words that are certainly mapped. Those are at or above the stack
  pointer and, for a stack fault, above the faulting guard page, using
  the real page size (16 KB on arm64 macOS).

Two designs were rejected:
- A table of function addresses would have been a use of each function.
  That stops LLVM from discarding a function it inlined into its only
  caller, which the backend relies on.
- A pointer in the prefix was a text relocation, which macOS's linker
  refuses ("Found illegal text-relocations"). The prefix now holds an
  index instead.

The first lookup floor (the stack base plus 4 KB) rejected every frame
of an overflow inside the runtime's C code. The faulting page's end is
the bound that works.

## Results (macOS arm64)

| program | interpreter | native debug | native release |
|---|---|---|---|
| `tests/trap/stack.kern` | 65-122 | 65-122 | — |
| `tests/trap/import_stack` | `lib/lib.kern` 11-68 | the same | the same |
| recursion on a spawned thread | `down` | `down` | `down` |
| recursion through `Vec.push` | the call to `down` (59-68) | `down` (9-106) | `down` |
| mutual recursion `ping`/`pong` | `pong` | `ping` | `ping` |
| overflow inside `kern_io_remove` (C) | — | `down` | `down` |
| `ptr_load` of address 16 in `peek` | `explicit` | `main` (`peek` inlined) | `main` |

The interpreter and native give the same record for plain recursion and
for the imported module. The other rows show why `stack` spans are still
not compared: the two engines run out of stack in different frames.

## Verification

- **`TestEmitFunctionMarkers`.** User and library markers, the table
  entry of a function's declaration, the wrapper's marker and the table
  hand-over, the ELF section name, and no marker in a freestanding build.
- **`TestNativeTrapLocations`.** Five programs in debug and release; each
  record names the expected declaration, and every process dies by
  `SIGABRT`.
  - The programs: plain recursion, recursion through `Vec.push`, a
    spawned thread, an overflow inside the runtime's C code, and a fault
    in unsafe code.
  - Removing the frame-chain step fails the C-runtime case at both
    levels.
- **Existing native trap and handler tests** pass, including nested
  traps, handlers under a mutex, and stack traps during allocation.
- **Gates.** `golangci-lint run ./...` reports 0 issues. `go test ./...`
  passes except `TestFixtures/concurrency/race_raw_ptr`, the
  ThreadSanitizer fixture. Its symbolizer (`/usr/bin/atos -i -p`) hangs
  on this host, and it fails identically on the committed compiler
  7e5f638.

## Reviews

- **prism:** one finding, fixed. The stack-word bounds check could wrap
  for an address near the top of the address space, which a corrupt
  frame pointer could supply. It now compares without addition.

## Linux (added 2026-10-31, the same day)

Run in OrbStack containers (Debian trixie, Go 1.25, clang 19):

- **Linux arm64, native.** `TestNativeTrapLocations` and every other
  native trap test pass as written. So does the whole `tests` package,
  freestanding fixtures under QEMU included.
- **Linux x86-64, emulated (Rosetta).** Two things needed changing:
  - **Frame pointers in the runtime.** The overflow inside the runtime's
    C code was found only through the frame chain, and clang omits frame
    pointers on x86-64 at `-O1` and above. The runtime (and any FFI C
    source) is now compiled with `-fno-omit-frame-pointer`, which finds
    it: the C function's own frame record leads to its kern caller. macOS
    and AArch64 keep frame pointers by default, so nothing changes there.
  - **Not in kern code.** Giving kern functions
    `"frame-pointer"="non-leaf"` as well made a stack overflow die by
    `SIGSEGV`, without a record, in 8 of 12 emulated runs. This happened
    even with the walk removed, while the commit before this change
    aborted 12 of 12. I could not tell whether real x86-64 hardware would
    behave the same, so the attribute is not used.
  - **Stack words through a pipe.** The handler reads stack words by
    writing them into a pipe created at start and reading them back, so
    the kernel answers `EFAULT` where a direct read would fault again.
    Below the Linux main thread's stack lies an unmapped gap of up to
    1 MiB in which a stack fault can land. `mincore` was tried first, but
    it accepts a mapped page that cannot be read. An atomic flag pairs
    each write with its own read when threads fault at once (prism).

  With these, every native trap test passes three times in a row on
  x86-64, and the overflow during allocation aborts cleanly 20 of 20. The
  whole `tests` package and `kern test`'s tests also pass on x86-64
  (ThreadSanitizer is unavailable under emulation, so its fixtures skip).

## CI (added 2026-10-31)

CI already ran `go test ./...` on Ubuntu x86-64 and macOS, so these
tests also passed on real x86-64 hardware at f65c0b7 (run 41197968257).
Two changes followed:
- **An arm64 Linux runner.** `ubuntu-24.04-arm` joins the matrix, so the
  AArch64 Linux register reads run on every push, not only in a
  container.
- **`TestNativeStackTrapRepeated`.** It runs the overflow during
  allocation 20 times per opt level and requires every run to die by
  `SIGABRT` with a record naming `down`. Under emulation, the rejected
  kern-code frame pointers broke about 8 runs in 12. The x86-64 container
  was no longer running when the test was written, so its catching that
  change is reasoned, not run.

## Not done

- **Kern code on x86-64 keeps no frame pointer.** An overflow in a
  frameless library function called from kern code may be found only
  through the next C frame. One whose whole chain is frameless falls back
  to 0-0.
- **Real x86-64 Linux hardware** was not available; the x86-64 runs are
  emulated.
