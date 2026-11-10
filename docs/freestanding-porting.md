# Porting the freestanding runtime to a board

A freestanding kern image is the program, the runtime (`rt.c`), the
start-up code of its architecture, and a **board file**: four C functions
that are everything the runtime knows about the machine. This guide is
what a board file must do, what a linker script must provide, and how the
heap and the stack are laid out. `docs/runtime.md` §11 is the summary;
`specs/runtime/SPEC.md` RJ1 is the contract.

The two boards kern ships are QEMU's `virt` machine (AArch64) and `q35`
machine (x86-64): `runtime/freestanding/src/board_virt.c` and
`board_q35.c`. Each is under sixty lines, and a new board is best
started from the one of its architecture.

## What a port consists of

| Piece | Whose | How it is chosen |
|---|---|---|
| The architecture | kern's: AArch64 or x86-64 | `--target=aarch64-unknown-none-elf` (the default) or `x86_64-unknown-none-elf` |
| The start-up code | kern's, one per architecture; yours when the board is not entered the way kern's expects | by the target, or `--startup=<file.S>` (`startup`) |
| The board file | yours | `--board=<file.c>`, or `board` in `kern.mod` |
| The linker script | yours, when the default layout does not fit | `--linker-script=<file.ld>`, or `linker_script` |
| The heap's size | yours | `--heap-bytes=N`, or `heap_bytes`; 1,048,576 by default |
| The stack's size | yours | `--stack-bytes=N`, or `stack_bytes`; 65,536 by default |

```
kern build --env=freestanding --board=boards/myboard.c \
    --linker-script=boards/myboard.ld --heap-bytes=262144 -o image main.kern
```

or, once, in `kern.mod`:

```json
{
  "entry": "main.kern",
  "env": "freestanding",
  "board": "boards/myboard.c",
  "linker_script": "boards/myboard.ld",
  "heap_bytes": 262144,
  "output": "image"
}
```

A flag overrides the manifest; a manifest's paths are relative to the
project root.

## The board file

It includes `kern_board.h`, which is on its include path, and defines all
four functions. One that is missing is a link error naming it.

```c
#include "kern_board.h"

#define UART_DATA   (*(volatile uint32_t *)0x10000000u)
#define UART_STATUS (*(volatile uint32_t *)0x10000004u)
#define UART_TX_FULL (1u << 3)

void kern_board_init(void) {
    // clocks, pin functions, the UART's baud rate: whatever the
    // other three functions need
}

void kern_board_putc(char c) {
    while (UART_STATUS & UART_TX_FULL) {}
    UART_DATA = (uint32_t)(uint8_t)c;
}

_Noreturn void kern_board_poweroff(int code) {
    (void)code;             // nowhere to report it on this board
    for (;;) {}             // or a reset, a power controller, a debug exit
}

uint64_t kern_board_now_ns(void) {
    return 0;               // no clock: time.sleep returns at once
}
```

**`kern_board_init`** runs once, on the boot stack, after the start-up
code and before the program's `main`. Nothing of the board has been
called before it.

**`kern_board_putc`** writes one byte and returns when the device has
taken it. It is the whole console: `print_str`, the trap record and the
halt line all arrive a byte at a time. It must not fail, allocate or
trap, because the trap handler calls it. A board with no console may do
nothing.

**`kern_board_poweroff`** ends the machine and does not return. `code` is
the halt code: `main`'s result or `exit`'s argument, 0 to 255, or 134
after a trap. A board passes it on where the machine has a way (QEMU's
`isa-debug-exit` device on `q35`) and discards it otherwise. The line
`{"halt":{"code":N}}` has already been written to the console.

**`kern_board_now_ns`** is the time in nanoseconds since a fixed moment
at or before `kern_board_init`. It must never decrease. `now_ns()`,
`std/time` and `time.sleep` (which busy-waits on it: no interrupts are
enabled) are built on it. A board without a clock returns 0 always; the
runtime takes a clock that reads 0 twice in a row for no clock, and
`time.sleep` returns at once.

### How a board file is compiled

As the runtime is: `-ffreestanding -nostdlib -fno-builtin -O1`, and on
AArch64 `-mstrict-align` (the image runs with the MMU off, where an
access must be aligned to its size). `<stdint.h>` and `<stddef.h>` are
available; nothing of a C library is, except `memcpy`, `memmove` and
`memset`, which the runtime defines for the compiler's own use. A board
file may hold static data. It must not call the kern runtime's entries.

## The linker script

The default scripts are a dozen lines each (`virt.ld`, `q35.ld`). A
script of your own must keep four promises the start-up code relies on.

1. **`ENTRY(_start)`**, and the section `.text._start` first in `.text`:
   the start-up code is entered at the image's first instruction by a
   loader that jumps to the load address. On x86-64 the `.multiboot`
   section comes before everything, within the first 8 KiB of the image.
2. **`.bss` and `COMMON` are in the image's memory**, after the data. The
   stack and the heap live there.
3. **`.bss` reads as zeros when `_start` runs.** An ELF loader does this
   (QEMU's `-kernel` does). A loader that copies a raw binary must zero
   the range itself, or the board cannot rely on the heap.
4. **The load address is RAM the machine has**, with room for the image,
   the stack and the heap.

```
ENTRY(_start)
SECTIONS {
  . = 0x80000000;                 /* where this board's RAM begins */
  .text : { *(.text._start) *(.text*) }
  .rodata : { *(.rodata*) }
  .data : { *(.data*) }
  .bss : { *(.bss*) *(COMMON) }
}
```

## What the start-up code assumes

**AArch64.** Entered at `_start` at EL1 (or higher, with EL1's system
registers accessible) with the MMU off. It enables floating point and
SIMD (`CPACR_EL1.FPEN`), sets the stack pointer to the top of the boot
stack and the stack limit one page above its bottom, calls the program,
and powers off with its result. A board that is entered at EL2 or EL3 and
must drop to EL1 needs start-up code of its own: `--startup=<file.S>`
compiles yours in place of kern's. Start from `start_aarch64.S`; what the
rest of the runtime needs of it is the symbol `_start` in the section
`.text._start`, a stack with `kern_stack_limit` set a page above its
lowest address, a call of `kern_main` and then of `kern_exit` with its
result. The file is preprocessed, and `KERN_STACK_SIZE` is defined when
`--stack-bytes` is given.

**x86-64.** Entered by a Multiboot 1 loader in 32-bit protected mode. It
identity-maps the first gibibyte, enables long mode and SSE, and
continues as above. The image is rewritten to the 32-bit ELF class,
which that loader requires.

## The heap

The system allocator's arena is a static array of `--heap-bytes` bytes
in `.bss`. It is a first-fit free list with a grain of 16 bytes:

- an allocated block has no header, so a heap of 65,536 bytes holds
  exactly sixteen blocks of 4,096;
- a block is aligned to what the request asks, up to 4,096;
- freed neighbours join, so a heap whose blocks are all freed is one
  block again;
- a request that does not fit is `Err(OutOfMemory)`, never a trap: the
  `try_` forms of the containers report it, the convenience forms trap
  `oom`;
- a `free` or `realloc` of a block outside the arena, off the grain, or
  overlapping a free block (a double free) traps `explicit`.

The size is a positive multiple of 16, at most 1,073,741,824.

## The stack

One stack, in `.bss`, of 65,536 bytes or what `--stack-bytes=N` says
(65,536 to 1,073,741,824), with the lowest page as a guard the program
never reaches: every function's prologue compares the stack pointer with
the limit and traps `stack` below it, with the function's location.
`--unsafe-no-stack-check` drops the comparison; overflow is then the
program's own risk.

## What a freestanding program can use

The language, the prelude's containers, `std/str`, `std/map`, `std/json`,
`std/time` (on the board's clock) and `std/rand`'s `Rng`. There is one
thread and no operating system, so a call that reaches something only
the hosted runtime has is refused at compile time, with a diagnostic
naming the call: `thread.spawn`, `Mutex`, `Condvar`, `park`, and whatever
in `std/io`, `std/process`, `std/net`, `std/signal`, `std/sync` and
`std/mem` asks the system for something. What asks for nothing still
works: `net.parse_addr`, for one, is plain code.

## Trying a port

The two shipped boards run under QEMU as the test suite runs them:

```
qemu-system-aarch64 -M virt -cpu cortex-a72 -serial stdio -display none -no-reboot -kernel image
qemu-system-x86_64 -M q35 -serial stdio -display none -no-reboot \
    -device isa-debug-exit,iobase=0xf4,iosize=0x04 -kernel image
```

A first program for a new board is three lines, and its console says
everything about the port's state:

```
module main
func main() -> i32 {
    print_str("up")
    println()
    return 0i32
}
```

- nothing at all: the image is not entered where the loader jumps, or
  `kern_board_putc` does not reach the device;
- `up` and no halt line: `kern_board_putc` loses bytes or the program
  faulted (try a trap on purpose: `trap()` must print its record);
- `up`, the halt line `{"halt":{"code":0}}`, and the machine keeps
  running: `kern_board_poweroff` returned or does nothing, which is a
  legitimate choice for a board with no power control.

`kern run --env=freestanding main.kern` runs the same program in the
interpreter's emulation, which has a console and a halt line of its own
and no board: it is the reference for what the program should print.
