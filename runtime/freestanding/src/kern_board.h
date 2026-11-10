// kern freestanding runtime: the board interface (specs/runtime RJ1).
//
// The runtime needs four things of the machine it runs on: to be set up,
// to write a byte to a console, to turn itself off, and to tell the time.
// A board file defines these four functions; the runtime calls nothing
// else that depends on the machine. board_virt.c (the QEMU virt machine,
// AArch64) and board_q35.c (the QEMU q35 machine, x86-64) are the
// defaults. `kern build --env=freestanding --board=<file.c>` compiles
// another file in their place, with this header on its include path; a
// board that leaves one of the four out does not link.
//
// A board file is compiled like the runtime: freestanding, no standard
// library, and on AArch64 with strictly aligned accesses. It may include
// <stdint.h> and <stddef.h>.
#ifndef KERN_BOARD_H
#define KERN_BOARD_H

#include <stdint.h>

// kern_board_init is called once, on the boot stack, before any other
// board function and before the program's entry. It sets up what the
// other three need; devices the board never uses may stay as they are.
void kern_board_init(void);

// kern_board_putc writes one byte to the console and returns when the
// device has accepted it. It must not fail, allocate or trap: the trap
// handler writes its record through it.
void kern_board_putc(char c);

// kern_board_poweroff ends the machine and does not return. code is the
// halt code (0 to 255: a program's exit code, or 134 after a trap), which
// a board reports where the machine has a way and may otherwise discard.
_Noreturn void kern_board_poweroff(int code);

// kern_board_now_ns is the time in nanoseconds since a fixed moment at or
// before kern_board_init. It never decreases. A board without a clock
// returns 0 always; time.sleep then returns at once.
uint64_t kern_board_now_ns(void);

#endif
