// A board file for the board tests (specs/runtime RJ1): the QEMU virt and
// q35 consoles, with every line the program writes prefixed by "| ", a
// line written at set-up, and no clock.
#include "kern_board.h"

#if defined(__aarch64__)
static void raw(char c) {
    while (*(volatile uint32_t *)0x09000018u & (1u << 5)) {}
    *(volatile uint32_t *)0x09000000u = (uint32_t)(uint8_t)c;
}
_Noreturn void kern_board_poweroff(int code) {
    (void)code;
    register uint64_t x0 __asm__("x0") = 0x84000008u;
    __asm__ volatile("hvc #0" : : "r"(x0) : "memory");
    for (;;) { __asm__ volatile("wfi"); }
}
#elif defined(__x86_64__)
static void outb(uint16_t port, uint8_t v) { __asm__ volatile("outb %0, %1" : : "a"(v), "Nd"(port)); }
static uint8_t inb(uint16_t port) { uint8_t v; __asm__ volatile("inb %1, %0" : "=a"(v) : "Nd"(port)); return v; }
static void raw(char c) {
    while (!(inb(0x3FD) & 0x20)) {}
    outb(0x3F8, (uint8_t)c);
}
_Noreturn void kern_board_poweroff(int code) {
    __asm__ volatile("outw %0, %1" : : "a"((uint16_t)code), "Nd"((uint16_t)0xF4));
    for (;;) { __asm__ volatile("hlt"); }
}
#endif

static int line_start = 1;

void kern_board_init(void) {
    raw('u');
    raw('p');
    raw('\n');
}

void kern_board_putc(char c) {
    if (line_start) {
        raw('|');
        raw(' ');
        line_start = 0;
    }
    raw(c);
    if (c == '\n') line_start = 1;
}

uint64_t kern_board_now_ns(void) { return 0; }
