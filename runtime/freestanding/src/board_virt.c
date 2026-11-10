// kern freestanding runtime: the board file of the QEMU virt machine
// (AArch64), the default for aarch64 targets. See kern_board.h.
//
// The console is the PL011 UART at 0x09000000, which QEMU leaves enabled;
// power-off is the PSCI SYSTEM_OFF call; the clock is the generic timer.
#include "kern_board.h"

void kern_board_init(void) {}

void kern_board_putc(char c) {
    while (*(volatile uint32_t *)0x09000018u & (1u << 5)) {} // UARTFR.TXFF: wait for room in the FIFO
    *(volatile uint32_t *)0x09000000u = (uint32_t)(uint8_t)c;
}

_Noreturn void kern_board_poweroff(int code) {
    (void)code; // PSCI has no way to pass it on
    register uint64_t x0 __asm__("x0") = 0x84000008u; // PSCI SYSTEM_OFF
    __asm__ volatile("hvc #0" : : "r"(x0) : "memory");
    for (;;) { __asm__ volatile("wfi"); }
}

// The generic timer's count over its frequency. The split conversion
// (c / f) * 10^9 + (c % f) * 10^9 / f cannot overflow for a frequency
// under 18 GHz.
uint64_t kern_board_now_ns(void) {
    uint64_t c, f;
    __asm__ volatile("isb\n\tmrs %0, cntvct_el0" : "=r"(c) : : "memory");
    __asm__ volatile("mrs %0, cntfrq_el0" : "=r"(f));
    if (f == 0) return c; // no frequency known: ticks as nanoseconds
    return (c / f) * 1000000000ull + (c % f) * 1000000000ull / f;
}
