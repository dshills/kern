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

## Not done

- **Linux is not exercised.** The register reads use glibc's
  `REG_RIP`/`REG_RBP`/`REG_RSP` on x86-64 and `regs[29]`/`regs[30]`/`pc`/`sp`
  on AArch64. The section bounds are the linker's `__start_kern_text`/
  `__stop_kern_text`. There is no Linux toolchain or running container on
  this host, so neither the build nor the lookup was run on Linux. The
  macOS x86-64 path compiles.
- **Optimised x86-64 Linux code may omit frame pointers.** The chain
  then ends early, and a record falls back to 0-0 when the faulting
  instruction is outside kern code.
