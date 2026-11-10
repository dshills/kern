// kern freestanding runtime: the board file of the QEMU q35 machine
// (x86-64), the default for x86_64 targets. See kern_board.h.
//
// The console is the 16550 UART at port 0x3F8; power-off is the
// isa-debug-exit device at port 0xF4, which makes QEMU exit with
// (code << 1) | 1; the clock is the time-stamp counter, calibrated once
// against the PIT.
#include "kern_board.h"

static void outb(uint16_t port, uint8_t v) { __asm__ volatile("outb %0, %1" : : "a"(v), "Nd"(port)); }
static uint8_t inb(uint16_t port) { uint8_t v; __asm__ volatile("inb %1, %0" : "=a"(v) : "Nd"(port)); return v; }

static uint64_t rdtsc(void) {
    uint32_t lo, hi;
    __asm__ volatile("lfence\n\trdtsc" : "=a"(lo), "=d"(hi) : : "memory");
    return ((uint64_t)hi << 32) | lo;
}

static uint64_t tsc_base = 0; // the counter at kern_board_init: the clock's zero
static uint64_t tsc_hz = 0;   // 0 until calibrated

void kern_board_init(void) { tsc_base = rdtsc(); }

void kern_board_putc(char c) {
    while (!(inb(0x3FD) & 0x20)) {} // LSR.THRE: the transmitter is ready
    outb(0x3F8, (uint8_t)c);
}

_Noreturn void kern_board_poweroff(int code) {
    __asm__ volatile("outw %0, %1" : : "a"((uint16_t)code), "Nd"((uint16_t)0xF4));
    for (;;) { __asm__ volatile("hlt"); }
}

// calibrate measures the counter over 11,932 PIT ticks (10 ms): channel 2
// in one-shot mode with its gate high, polling its output on port 0x61
// (Linux's pit_calibrate_tsc). A PIT that never fires is given up after
// 2^32 counter ticks, and 1 GHz is assumed. It runs at the first reading
// of the clock, so an image that never reads it does not wait.
static void calibrate(void) {
    const uint32_t latch = 11932;
    outb(0x61, (uint8_t)((inb(0x61) & ~0x02) | 0x01)); // gate on, speaker off
    outb(0x43, 0xb0);                                   // channel 2, lobyte/hibyte, mode 0, binary
    outb(0x42, (uint8_t)(latch & 0xff));
    outb(0x42, (uint8_t)(latch >> 8));
    uint64_t t1 = rdtsc(), t2 = t1;
    while ((inb(0x61) & 0x20) == 0) {
        t2 = rdtsc();
        if (t2 - t1 > 0xffffffffull) break;
    }
    uint64_t d = t2 - t1;
    tsc_hz = (inb(0x61) & 0x20) != 0 && d > 0 ? d * 1193182ull / latch : 1000000000ull;
}

uint64_t kern_board_now_ns(void) {
    if (tsc_hz == 0) calibrate();
    uint64_t c = rdtsc() - tsc_base;
    return (c / tsc_hz) * 1000000000ull + (c % tsc_hz) * 1000000000ull / tsc_hz;
}
