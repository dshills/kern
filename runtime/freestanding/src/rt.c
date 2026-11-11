// kern freestanding runtime (PLAN.md 3.1, ADR-011): no OS assumptions, no
// threads. The machine is behind four functions a board file defines
// (kern_board.h: set-up, a console byte, power-off, the time), so this
// file has no device in it. Output goes to the board's console; traps run
// the handler once and halt; exit and halt write a final
// `{"halt": {"code": n}}` line and power the machine off. The system
// allocator behind the library containers is a first-fit free list over a
// static arena of KERN_HEAP_SIZE bytes. trunc/truncf and the memory
// routines are the libc symbols LLVM may call for in compiled code.
#include "kern_rt_fs.h"
#include "kern_board.h"

// kern_rt_abi_3 is the ABI symbol every image references (RA4).
_Static_assert(KERN_RT_ABI == 3, "the ABI symbol's name carries KERN_RT_ABI");
const int32_t kern_rt_abi_3 = KERN_RT_ABI;

typedef struct { uint8_t kind; uint8_t pad[7]; const char *file; uint64_t start, end, thread_id; } kern_trapinfo;

static void *volatile kern_handler = 0;
static const char *kern_file = "";
static int kern_in_trap = 0;
// the lowest address the stack may reach; set by the startup code and
// compared in every function prologue (compiler-inserted stack check)
void *kern_stack_limit = 0;

// ---- console ----
static void kern_putc(char c) { kern_board_putc(c); }

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
void eprint_str(const char *p, int64_t n) { print_str(p, n); }
void println(void) { kern_putc('\n'); }

// ---- halt protocol ----
static _Noreturn void kern_halt(int code) {
    kern_puts("{\"halt\":{\"code\":");
    kern_putu((uint64_t)(uint32_t)code);
    kern_puts("}}\n");
    kern_board_poweroff(code);
}
_Noreturn void kern_exit(int32_t code) { kern_halt((int)code); }
// kern_start is called by the image's entry (kern_main) before the program
// runs, and sets the board up before anything else uses it; kern_run_main
// runs the program's main function.
void kern_start(const void *abi) { (void)abi; kern_board_init(); }
int32_t kern_run_main(int32_t (*fn)(void)) { return fn(); }
void kern_yield(void) {}
void kern_set_file(const char *name) { kern_file = name; }
void *kern_set_handler(void *fn) { void *old = kern_handler; kern_handler = fn; return old; }

// ---- line tables (specs/runtime RE1) ----
// One record per source file of the program, registered by the image's
// entry: its name (the constant every trap site passes), the byte offset
// of each line's first byte, and the file's length; as in the hosted
// runtime.
typedef struct {
    const char *name;
    const int32_t *starts;
    int64_t count;
    int64_t size;
} kern_srcfile;
static const kern_srcfile *kern_files = 0;
static int64_t kern_nfiles = 0;
void kern_set_lines(const void *t, int64_t n) { kern_files = (const kern_srcfile *)t; kern_nfiles = n; }

static int kern_streq(const char *a, const char *b) {
    while (*a && *a == *b) { a++; b++; }
    return *a == *b;
}
// kern_line_col is the 1-based line and byte column of offset start in
// file: 0 and 0 when no table names the file, when start is past the
// file's end, or for the span 0-0, which stands for "no location".
static void kern_line_col(const char *file, int64_t start, int64_t end, uint64_t *line, uint64_t *col) {
    *line = 0; *col = 0;
    const kern_srcfile *files = kern_files, *f = 0;
    const int64_t nfiles = kern_nfiles;
    if (file == 0 || files == 0 || (start == 0 && end == 0)) return;
    // by the name's address (the constant a trap site passes), else by its text
    for (int64_t i = 0; i < nfiles; i++) if (files[i].name == file) { f = &files[i]; break; }
    if (f == 0) for (int64_t i = 0; i < nfiles; i++) if (files[i].name != 0 && kern_streq(files[i].name, file)) { f = &files[i]; break; }
    if (f == 0 || f->count <= 0 || start < 0 || start > f->size) return;
    int64_t lo = 0, hi = f->count; // the last line start at or before start
    while (hi - lo > 1) {
        int64_t mid = (lo + hi) / 2;
        if ((int64_t)f->starts[mid] <= start) lo = mid; else hi = mid;
    }
    *line = (uint64_t)lo + 1;
    *col = (uint64_t)(start - (int64_t)f->starts[lo]) + 1;
}

static const char *kern_trap_names[] = {"bounds", "overflow", "div_zero", "shift", "conversion", "generation", "vacant", "identity", "disjoint", "oom", "stack", "explicit", "unreachable"};

// kern_trap writes the record to the console (the default handler), runs a
// custom handler once instead when installed, and halts with code 134.
_Noreturn void kern_trap(int32_t kind, int64_t start, int64_t end, const char *file) {
    if (kern_in_trap) kern_halt(134);
    kern_in_trap = 1;
    if (!file) file = kern_file; // the trapping function's file, or the program's
    void (*h)(void *) = (void (*)(void *))kern_handler;
    if (h == 0) {
        kern_puts("{\"trap\":{\"kind\":\""); kern_puts(kern_trap_names[kind]);
        kern_puts("\",\"file\":\""); kern_putjson(file);
        kern_puts("\",\"span\":{\"start\":"); kern_putu((uint64_t)start);
        kern_puts(",\"end\":"); kern_putu((uint64_t)end);
        uint64_t line, col;
        kern_line_col(file, start, end, &line, &col);
        kern_puts("},\"line\":"); kern_putu(line);
        kern_puts(",\"col\":"); kern_putu(col);
        kern_puts(",\"thread\":0}}\n");
    } else {
        kern_trapinfo info = {(uint8_t)kind, {0}, file, (uint64_t)start, (uint64_t)end, 0};
        h(&info);
    }
    kern_halt(134);
}

// trunc/truncf back the float-to-integer conversion's range check
// (llvm.trunc): AArch64 lowers it to frintz, baseline x86-64 (no SSE4.1)
// to a libm call, which a bare-metal image must supply itself. Round toward
// zero by clearing the fraction bits (musl's algorithm); NaN and infinity
// pass through.
double trunc(double x) {
    union { double f; uint64_t i; } u = {x};
    int e = (int)(u.i >> 52 & 0x7ff) - 0x3ff + 12;
    if (e >= 52 + 12) return x;      // no fraction bits, inf or NaN
    if (e < 12) e = 1;               // |x| < 1: keep only the sign
    uint64_t m = ~0ULL >> e;
    if ((u.i & m) == 0) return x;
    u.i &= ~m;
    return u.f;
}
float truncf(float x) {
    union { float f; uint32_t i; } u = {x};
    int e = (int)(u.i >> 23 & 0xff) - 0x7f + 9;
    if (e >= 23 + 9) return x;
    if (e < 9) e = 1;
    uint32_t m = ~0U >> e;
    if ((u.i & m) == 0) return x;
    u.i &= ~m;
    return u.f;
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
// kern_swap exchanges two regions of n bytes (the prelude's swap_at): two
// elements of one sequence, so the same region or regions that do not
// overlap.
void kern_swap(char *a, char *b, int64_t n) {
    if (a == b) return;
    while (n-- > 0) {
        char t = *a;
        *a++ = *b;
        *b++ = t;
    }
}

// ---- container identity tags (spec §5.6) ----

// kern_next_identity issues the next container identity tag, as the hosted
// runtime does: a counter from 1 in debug builds, 0 in a release build.
// Once the counter wraps it stays at 0, which disables the check, rather
// than issuing tags a second time. There are no threads, so a plain
// counter suffices.
#ifdef KERN_RELEASE
uint32_t kern_next_identity(void) { return 0; }
#else
static uint32_t kern_identity = 1;
uint32_t kern_next_identity(void) {
    if (kern_identity == 0) return 0;
    return kern_identity++;
}
#endif

// ---- SystemAllocator (spec §6, ADR-011) ----
//
// The system allocator serves the library containers (Box, Vec, Pool,
// Arena) from a static arena of KERN_HEAP_SIZE bytes in .bss, which both
// linker scripts place after the image (virt and q35 alike). It is a
// first-fit free list: every block boundary is a multiple of
// KERN_HEAP_GRAIN bytes, a free block keeps its size and the next free
// block in its first bytes (the list is sorted by address, so a release
// coalesces with both neighbours), and an allocated block carries no
// header: free and realloc receive the allocation's size and alignment,
// the §6 obligation. A request that violates the obligations (alignment
// not a power of two or above 4096, size not a multiple of it, a pointer
// the arena does not hold, a block that overlaps a free one: a double
// free) traps with kind `explicit`; exhaustion returns Err(OutOfMemory)
// and never traps. Allocations are 16-byte aligned at
// least, as on the hosts.
#ifndef KERN_HEAP_SIZE
#define KERN_HEAP_SIZE (1u << 20)
#endif
#define KERN_HEAP_GRAIN 16u
_Static_assert(KERN_HEAP_SIZE >= KERN_HEAP_GRAIN && KERN_HEAP_SIZE % KERN_HEAP_GRAIN == 0, "KERN_HEAP_SIZE must be a positive multiple of KERN_HEAP_GRAIN");

typedef struct kern_free_block { size_t size; struct kern_free_block *next; } kern_free_block;
struct kern_sysalloc {
    int unused;
};

static uint8_t kern_heap[KERN_HEAP_SIZE] __attribute__((aligned(4096)));
static kern_free_block *kern_free_head = 0;
static int kern_heap_ready = 0;

static void kern_heap_init(void) {
    if (kern_heap_ready) return;
    kern_heap_ready = 1;
    kern_free_head = (kern_free_block *)kern_heap;
    kern_free_head->size = KERN_HEAP_SIZE;
    kern_free_head->next = 0;
}

// kern_block_size is the arena bytes a request of size occupies: rounded
// up to the grain, at least one grain (size 0 is a unique pointer); 0 when
// the request can never fit.
static size_t kern_block_size(int64_t size) {
    if ((uint64_t)size > KERN_HEAP_SIZE) return 0;
    size_t n = ((size_t)size + KERN_HEAP_GRAIN - 1) / KERN_HEAP_GRAIN * KERN_HEAP_GRAIN;
    return n ? n : KERN_HEAP_GRAIN;
}
static size_t kern_block_align(int64_t align) { return (size_t)align < KERN_HEAP_GRAIN ? KERN_HEAP_GRAIN : (size_t)align; }
// The allocator's entry points take the location of the user-level call
// last, as a pointer to a record the backend emits (as in the hosted
// runtime), and raise their obligation traps there
// (specs/runtime-trap-file/SPEC.md); a null record is no location (the
// span 0-0 and the program's file), and a null file in a record is the
// program's file with the record's span, as for kern_trap.
static _Noreturn void kern_trap_loc(int32_t kind, const kern_loc *at) {
    if (at == 0) kern_trap(kind, 0, 0, 0);
    kern_trap(kind, at->start, at->end, at->file);
}
static void kern_check_request(int64_t size, int64_t align, const kern_loc *at) {
    if (size < 0 || align <= 0 || (align & (align - 1)) != 0 || align > 4096 || size % align != 0) kern_trap_loc(11, at);
}
// kern_check_block traps unless p with n bytes lies in the arena on a grain
// boundary: a pointer that was not one allocation's.
static void kern_check_block(const void *p, size_t n, const kern_loc *at) {
    uintptr_t a = (uintptr_t)p, lo = (uintptr_t)kern_heap, hi = lo + KERN_HEAP_SIZE;
    if (a < lo || a >= hi || n > hi - a || (a & (KERN_HEAP_GRAIN - 1)) != 0) kern_trap_loc(11, at);
}

// kern_check_live traps when the n bytes at p overlap a free block: the
// block, or part of it, was freed already (specs/runtime RJ2). The list
// is sorted by address, so the walk stops at the first block past p + n.
static void kern_check_live(const uint8_t *p, size_t n, const kern_loc *at) {
    for (const kern_free_block *cur = kern_free_head; cur && (const uint8_t *)cur < p + n; cur = cur->next) {
        if ((const uint8_t *)cur + cur->size > p) kern_trap_loc(11, at);
    }
}

// kern_heap_take carves n bytes at alignment al (both multiples of the
// grain) out of the first free block that fits; the parts of the block
// before and after the allocation stay free.
static uint8_t *kern_heap_take(size_t n, size_t al) {
    kern_free_block *prev = 0;
    for (kern_free_block *cur = kern_free_head; cur; prev = cur, cur = cur->next) {
        uintptr_t start = (uintptr_t)cur, end = start + cur->size;
        uintptr_t a = (start + al - 1) & ~(uintptr_t)(al - 1);
        if (a > end || end - a < n) continue;
        kern_free_block **link = prev ? &prev->next : &kern_free_head;
        kern_free_block *next = cur->next;
        size_t head = (size_t)(a - start), tail = (size_t)(end - (a + n));
        *link = next;
        if (tail) { kern_free_block *t = (kern_free_block *)(a + n); t->size = tail; t->next = next; *link = t; next = t; }
        if (head) { cur->size = head; cur->next = next; *link = cur; }
        return (uint8_t *)a;
    }
    return 0;
}

// kern_heap_release returns the n bytes at p to the free list, merging
// them with a free neighbour on either side.
static void kern_heap_release(uint8_t *p, size_t n) {
    kern_free_block *prev = 0, *cur = kern_free_head;
    while (cur && (uint8_t *)cur < p) { prev = cur; cur = cur->next; }
    if (cur && p + n == (uint8_t *)cur) { n += cur->size; cur = cur->next; }
    if (prev && (uint8_t *)prev + prev->size == p) { prev->size += n; prev->next = cur; return; }
    kern_free_block *b = (kern_free_block *)p;
    b->size = n;
    b->next = cur;
    if (prev) prev->next = b; else kern_free_head = b;
}

// kern_heap_extend grows the n-byte block at p to m bytes in place when the
// bytes after it are free; it reports whether it did.
static int kern_heap_extend(uint8_t *p, size_t n, size_t m) {
    kern_free_block *prev = 0, *cur = kern_free_head;
    while (cur && (uint8_t *)cur < p + n) { prev = cur; cur = cur->next; }
    if (!cur || (uint8_t *)cur != p + n || cur->size < m - n) return 0;
    kern_free_block **link = prev ? &prev->next : &kern_free_head;
    size_t rest = cur->size - (m - n);
    kern_free_block *next = cur->next;
    if (rest) { kern_free_block *t = (kern_free_block *)(p + m); t->size = rest; t->next = next; *link = t; }
    else *link = next;
    return 1;
}

void *kern_sysalloc_new(void) { static kern_sysalloc a; return &a; }
void kern_sysalloc_alloc(kern_sysalloc *a, int64_t size, int64_t align, char *out, int64_t off, const kern_loc *at) {
    (void)a;
    kern_check_request(size, align, at);
    kern_heap_init();
    size_t n = kern_block_size(size);
    uint8_t *p = n ? kern_heap_take(n, kern_block_align(align)) : 0;
    if (p == 0) { out[0] = 1; return; } // Err(OutOfMemory)
    out[0] = 0;
    memcpy(out + off, &p, sizeof p);
}
// kern_sysalloc_alloc_zeroed is kern_sysalloc_alloc with zeroed bytes.
void kern_sysalloc_alloc_zeroed(kern_sysalloc *a, int64_t size, int64_t align, char *out, int64_t off, const kern_loc *at) {
    kern_sysalloc_alloc(a, size, align, out, off, at);
    if (out[0] != 0 || size == 0) return;
    uint8_t *p;
    memcpy(&p, out + off, sizeof p);
    memset(p, 0, (size_t)size);
}
// kern_sysalloc_realloc shrinks or grows the allocation in place when the
// arena allows, else takes a new block, copies the common prefix and
// releases the old one; on failure the old allocation stays.
void kern_sysalloc_realloc(kern_sysalloc *a, void *p, int64_t old_size, int64_t size, int64_t align, char *out, int64_t off, const kern_loc *at) {
    (void)a;
    kern_check_request(size, align, at);
    kern_check_request(old_size, align, at);
    kern_heap_init();
    size_t on = kern_block_size(old_size), n = kern_block_size(size), al = kern_block_align(align);
    if (on == 0) kern_trap_loc(11, at); // larger than the arena: not one allocation's size
    kern_check_block(p, on, at);
    kern_check_live(p, on, at);
    uint8_t *q = p;
    if (n == 0) { out[0] = 1; return; }
    if (n < on) kern_heap_release(q + n, on - n);
    else if (n > on && !(((uintptr_t)q & (al - 1)) == 0 && kern_heap_extend(q, on, n))) {
        uint8_t *np = kern_heap_take(n, al);
        if (np == 0) { out[0] = 1; return; }
        memcpy(np, q, (size_t)(old_size < size ? old_size : size));
        kern_heap_release(q, on);
        q = np;
    }
    out[0] = 0;
    memcpy(out + off, &q, sizeof q);
}
void kern_sysalloc_free(kern_sysalloc *a, void *p, int64_t size, int64_t align, const kern_loc *at) {
    (void)a;
    kern_check_request(size, align, at);
    kern_heap_init();
    size_t n = kern_block_size(size);
    if (n == 0) kern_trap_loc(11, at);
    kern_check_block(p, n, at);
    kern_check_live(p, n, at);
    kern_heap_release(p, n);
}

// ---- clock (specs/freestanding-clock) ----
// kern_now_ns is the board's clock, monotonic and in nanoseconds.
uint64_t kern_now_ns(void) { return kern_board_now_ns(); }
// kern_time_sleep_ns waits until the clock has advanced by at least ns
// (busy-waiting: no interrupts are enabled). A board without a clock
// reads 0 always, and the sleep then returns at once; at is unused,
// since nothing here can fail.
void kern_time_sleep_ns(uint64_t ns, const kern_loc *at) {
    (void)at;
    if (ns == 0) return;
    uint64_t start = kern_board_now_ns();
    if (start == 0 && kern_board_now_ns() == 0) return; // no clock
    while (kern_board_now_ns() - start < ns) { }
}
