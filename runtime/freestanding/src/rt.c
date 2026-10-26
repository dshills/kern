// kern freestanding runtime (PLAN.md 3.1, ADR-011): no OS assumptions, no
// allocator, no threads. Output goes to a console device (the QEMU virt
// PL011 UART on AArch64, the 16550 at port 0x3F8 on x86-64); traps run the
// handler once and halt; exit and halt write a final `{"halt": {"code": n}}`
// line and power the machine off.
#include <stdint.h>
#include <stddef.h>

typedef struct { uint8_t kind; uint8_t pad[7]; const char *file; uint64_t start, end, thread_id; } kern_trapinfo;

static void *volatile kern_handler = 0;
static const char *kern_file = "";
static int kern_in_trap = 0;
// the lowest address the stack may reach; set by the startup code and
// compared in every function prologue (compiler-inserted stack check)
void *kern_stack_limit = 0;

// ---- console device ----
#if defined(__aarch64__)
static void kern_putc(char c) {
    while (*(volatile uint32_t *)0x09000018u & (1u << 5)) {} // UARTFR.TXFF: wait for FIFO space
    *(volatile uint32_t *)0x09000000u = (uint32_t)(uint8_t)c;
}
static void kern_poweroff(int code) {
    (void)code;
    register uint64_t x0 __asm__("x0") = 0x84000008u; // PSCI SYSTEM_OFF
    __asm__ volatile("hvc #0" : : "r"(x0) : "memory");
    for (;;) { __asm__ volatile("wfi"); }
}
#elif defined(__x86_64__)
static void kern_outb(uint16_t port, uint8_t v) { __asm__ volatile("outb %0, %1" : : "a"(v), "Nd"(port)); }
static uint8_t kern_inb(uint16_t port) { uint8_t v; __asm__ volatile("inb %1, %0" : "=a"(v) : "Nd"(port)); return v; }
static void kern_putc(char c) {
    while (!(kern_inb(0x3FD) & 0x20)) {} // LSR.THRE: transmitter ready
    kern_outb(0x3F8, (uint8_t)c);
}
static void kern_poweroff(int code) {
    __asm__ volatile("outw %0, %1" : : "a"((uint16_t)code), "Nd"((uint16_t)0xF4)); // isa-debug-exit: exits with (code << 1) | 1
    for (;;) { __asm__ volatile("hlt"); }
}
#else
static void kern_putc(char c) { (void)c; }
static void kern_poweroff(int code) { (void)code; for (;;) {} }
#endif

static void kern_puts(const char *s) { while (*s) kern_putc(*s++); }
// kern_putjson writes s as the body of a JSON string
static void kern_putjson(const char *s) {
    for (; *s; s++) {
        unsigned char c = (unsigned char)*s;
        if (c == '"' || c == '\\') { kern_putc('\\'); kern_putc((char)c); }
        else if (c < 0x20) { kern_puts("\\u00"); kern_putc("0123456789abcdef"[c >> 4]); kern_putc("0123456789abcdef"[c & 15]); }
        else kern_putc((char)c);
    }
}
static void kern_putu(uint64_t v) {
    char b[24]; int n = 0;
    do { b[n++] = (char)('0' + v % 10); v /= 10; } while (v);
    while (n > 0) kern_putc(b[--n]);
}

void print_u64(uint64_t v) { kern_putu(v); }
void print_i64(int64_t v) { if (v < 0) { kern_putc('-'); kern_putu((uint64_t)0 - (uint64_t)v); } else kern_putu((uint64_t)v); }
void print_bool(uint8_t v) { kern_puts(v ? "true" : "false"); }
void print_str(const char *p, int64_t n) { for (int64_t i = 0; i < n; i++) kern_putc(p[i]); }
void println(void) { kern_putc('\n'); }

// ---- halt protocol ----
static void kern_halt(int code) {
    kern_puts("{\"halt\":{\"code\":");
    kern_putu((uint64_t)(uint32_t)code);
    kern_puts("}}\n");
    kern_poweroff(code);
}
void kern_exit(int32_t code) { kern_halt((int)code); }
void kern_start(void) {}
void kern_set_file(const char *name) { kern_file = name; }
void *kern_set_handler(void *fn) { void *old = kern_handler; kern_handler = fn; return old; }
void kern_fence(uint8_t o) { (void)o; __atomic_thread_fence(__ATOMIC_SEQ_CST); }

static const char *kern_trap_names[] = {"bounds", "overflow", "div_zero", "shift", "conversion", "generation", "vacant", "identity", "disjoint", "oom", "stack", "explicit", "unreachable"};

// kern_trap writes the record to the console (the default handler), runs a
// custom handler once instead when installed, and halts with code 134.
void kern_trap(int32_t kind, int64_t start, int64_t end) {
    if (kern_in_trap) kern_halt(134);
    kern_in_trap = 1;
    void (*h)(void *) = (void (*)(void *))kern_handler;
    if (h == 0) {
        kern_puts("{\"trap\":{\"kind\":\""); kern_puts(kern_trap_names[kind]);
        kern_puts("\",\"file\":\""); kern_putjson(kern_file);
        kern_puts("\",\"span\":{\"start\":"); kern_putu((uint64_t)start);
        kern_puts(",\"end\":"); kern_putu((uint64_t)end);
        kern_puts("},\"thread\":0}}\n");
    } else {
        kern_trapinfo info = {(uint8_t)kind, {0}, kern_file, (uint64_t)start, (uint64_t)end, 0};
        h(&info);
    }
    kern_halt(134);
}

// memcpy/memset/memmove may be emitted by LLVM for aggregate moves
void *memcpy(void *d, const void *s, size_t n) { char *dp = d; const char *sp = s; while (n--) *dp++ = *sp++; return d; }
void *memmove(void *d, const void *s, size_t n) {
    char *dp = d; const char *sp = s;
    if ((uintptr_t)dp < (uintptr_t)sp) { while (n--) *dp++ = *sp++; }
    else { dp += n; sp += n; while (n--) *--dp = *--sp; }
    return d;
}
void *memset(void *d, int c, size_t n) { char *dp = d; while (n--) *dp++ = (char)c; return d; }
