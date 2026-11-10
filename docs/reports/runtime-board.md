# Runtime, phase 10: the freestanding board interface

**Date:** 2026-11-09. **Spec:** `specs/runtime/SPEC.md` v0.6, area J (RJ1,
RJ2). **Plan:** `specs/runtime/PLAN.md`, phase 10. **Hosts:** macOS 27.2 on
arm64, where images boot under QEMU for both architectures, and Ubuntu
24.04 on arm64 in the `kern-linux-check` container, which links images but
has no QEMU.

## What changed

- **The machine is behind four functions (RJ1).** The freestanding runtime
  held its devices in `#if` blocks: the UART, the power-off and the clock
  of the two QEMU machines. They are now a board file's, declared in
  `kern_board.h`:
  - `kern_board_init`, called once before anything else;
  - `kern_board_putc`, one byte to the console;
  - `kern_board_poweroff`, which ends the machine with the halt code;
  - `kern_board_now_ns`, the time, or 0 always on a board without a
    clock (`time.sleep` then returns at once).

  `rt.c` has no device in it. `board_virt.c` and `board_q35.c` are the
  defaults, each the code that was in `rt.c`.
- **Three build options**, as flags of `kern build --env=freestanding` and
  as keys of `kern.mod`:
  - `--board=<file.c>` (`board`) compiles a board file in place of the
    target's default, with `kern_board.h` on its include path;
  - `--linker-script=<file.ld>` (`linker_script`) replaces the default
    script;
  - `--heap-bytes=N` (`heap_bytes`) sizes the system allocator's arena: a
    positive multiple of 16, at most 1,073,741,824; the default stays
    1,048,576.

  A flag overrides the manifest; a manifest's paths are relative to the
  project root. The flags are refused on a hosted build and on `kern run`.
- **A double free traps (RJ2).** `free` and `realloc` trap `explicit` when
  the block they are given overlaps a free block. The free list is sorted
  by address, so the check is one walk that stops past the block.

## Requirement to test

| Clause | Test |
|---|---|
| RJ1: a board file of the program's own builds and boots on both architectures; its set-up runs first; its console is the program's | `TestCustomBoard` (a console that prefixes every line, a line written at set-up) |
| RJ1: a board without a clock returns 0, and `time.sleep` returns at once | same (an hour's sleep; the time printed is 0) |
| RJ1: the halt code reaches `kern_board_poweroff` | same (exit code 7: QEMU's status on x86-64) |
| RJ1: a missing function is a link error | `TestBoardMustDefineAllFour` (no `kern_board_now_ns`; also a board file that does not exist) |
| RJ1: the default boards are the two QEMU machines, each in its own file | every fixture of `tests/freestanding`, booted on both; `TestRuntimeWarningClean` and `TestRuntimeStaticAnalysis` cover both files |
| RJ1: `--heap-bytes`: 64 KiB hold sixteen blocks of 4,096 bytes and the default 256; exhaustion is `Err(OutOfMemory)`; freed blocks join again | `TestHeapBytes`, both architectures |
| RJ1: a heap that is not a positive multiple of 16 is refused | `TestHeapBytes`, `TestBuildFreestandingOptions/refusals` |
| RJ1: `--linker-script`: an image laid out a megabyte higher boots, and `_start` is where the script put it | `TestLinkerScript` |
| RJ1: the flags and the `kern.mod` keys | `TestBuildFreestandingOptions/flags` and `/kern.mod` (the image built either way boots with the board and a heap of 32 KiB) |
| RJ1: refusals: a hosted build, `kern run`, a missing file, a bad manifest value, a path out of the project | `TestBuildFreestandingOptions/refusals` |
| RJ2: `free` of a freed block traps `explicit` | `tests/freestanding/double_free.kern`, in the emulation and under QEMU on both |
| RJ2: `realloc` of a freed block traps `explicit` | `tests/freestanding/realloc_freed.kern` |
| RJ2: the checks the allocator had still pass | the other fixtures of `tests/freestanding` |

## Deviations and open points

- **The heap is at most a gibibyte.** The spec gives no upper bound; the
  arena is a static array in the image.
- **The x86-64 clock is calibrated at its first reading**, as before, not
  in `kern_board_init`: the calibration waits ten milliseconds, which an
  image that never reads the clock should not pay. `kern_board_init`
  records the counter, so the clock's zero is still the set-up.
- **A clock that reads 0 twice in a row is taken for no clock** by
  `time.sleep`.
- **The interpreter's freestanding emulation** has no board and no arena
  limit: `kern run --env=freestanding` writes to its own console and
  refuses nothing for want of memory. The options are `kern build`'s.
- **The porting guide** that walks through writing a board file is part of
  phase 11 (RK2).

## Verification

`go test ./...` passes on macOS, every freestanding image booted under QEMU
on both architectures; in the Linux container the area-A checks pass and
the images link (there is no QEMU there, so the board tests that boot are
skipped and `TestBoardMustDefineAllFour` runs); `golangci-lint run ./...`
reports no issue.
