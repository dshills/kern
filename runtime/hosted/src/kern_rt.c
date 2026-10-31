// kern hosted runtime (PLAN.md 2.2): traps, output, the runtime-provided
// library objects (Shared, Mutex), threads, the system allocator and
// the test hooks. Linked by `kern build` with every program. The containers
// (Box, Vec, Pool, Arena) are kern code in std/core.kern.
#ifndef _GNU_SOURCE
#define _GNU_SOURCE // pthread_getattr_np on glibc
#endif
#include <stddef.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <signal.h>
#include <pthread.h>
#include <stdatomic.h>
#include <unistd.h>
#include <time.h>
#include <sched.h>
#include <fcntl.h>
#include <errno.h>
#ifdef __APPLE__
#include <sys/ucontext.h>
#else
#include <ucontext.h>
#endif

typedef void (*kern_glue)(void *);

// ---- output (write(2)-based so the trap path never touches stdio) ----

static char kern_outbuf[65536];
static size_t kern_outlen = 0;
static pthread_mutex_t kern_outlock = PTHREAD_MUTEX_INITIALIZER;

static void kern_write_all(int fd, const char *p, size_t n) {
    while (n > 0) {
        ssize_t w = write(fd, p, n);
        if (w <= 0) return;
        p += w; n -= (size_t)w;
    }
}
static void kern_flush_locked(void) { kern_write_all(1, kern_outbuf, kern_outlen); kern_outlen = 0; }
static void kern_flush(void) { pthread_mutex_lock(&kern_outlock); kern_flush_locked(); pthread_mutex_unlock(&kern_outlock); }
// kern_flush_try flushes without blocking: for trap paths that may run on a
// thread interrupted while holding the output lock
static void kern_flush_try(void) {
    if (pthread_mutex_trylock(&kern_outlock) == 0) { kern_flush_locked(); pthread_mutex_unlock(&kern_outlock); }
    else kern_write_all(1, kern_outbuf, kern_outlen);
}
static void kern_out(const char *p, size_t n) {
    pthread_mutex_lock(&kern_outlock);
    if (n > sizeof kern_outbuf) { kern_flush_locked(); kern_write_all(1, p, n); }
    else {
        if (kern_outlen + n > sizeof kern_outbuf) kern_flush_locked();
        memcpy(kern_outbuf + kern_outlen, p, n);
        kern_outlen += n;
    }
    pthread_mutex_unlock(&kern_outlock);
}
static size_t kern_fmt_u64(char *dst, uint64_t v) {
    char tmp[24]; size_t n = 0;
    do { tmp[n++] = (char)('0' + v % 10); v /= 10; } while (v);
    for (size_t i = 0; i < n; i++) dst[i] = tmp[n - 1 - i];
    return n;
}
void print_u64(uint64_t v) { char b[24]; kern_out(b, kern_fmt_u64(b, v)); }
void print_i64(int64_t v) {
    char b[25]; size_t n = 0;
    if (v < 0) { b[n++] = '-'; n += kern_fmt_u64(b + n, (uint64_t)0 - (uint64_t)v); }
    else n = kern_fmt_u64(b, (uint64_t)v);
    kern_out(b, n);
}
void print_bool(uint8_t v) { if (v) kern_out("true", 4); else kern_out("false", 5); }
void print_str(const char *p, int64_t n) { kern_out(p, (size_t)n); }
void eprint_str(const char *p, int64_t n) { kern_write_all(2, p, (size_t)n); }
void println(void) { kern_out("\n", 1); }
#ifdef __APPLE__
#define KERN_CLOCK CLOCK_MONOTONIC_RAW // CLOCK_MONOTONIC has microsecond resolution on macOS
#else
#define KERN_CLOCK CLOCK_MONOTONIC
#endif
void kern_exit(int32_t code) { kern_flush(); _exit(code); }

// ---- std/io (hosted): descriptor-level file access ----
// Errors are negative errno values; the kern side maps them to IoErr.
// Descriptor 1 goes through the output buffer so that it interleaves with
// print_* as written.

static int kern_argc = 0;
static char **kern_argv = NULL;
void kern_set_args(int32_t argc, char **argv) { kern_argc = argc; kern_argv = argv; }
int32_t kern_io_open(const char *p, int64_t n, uint8_t write) {
    char path[4096];
    if (n < 0 || (size_t)n >= sizeof path) return -ENAMETOOLONG;
    if (n > 0 && memchr(p, 0, (size_t)n) != NULL) return -EINVAL; // an embedded NUL would cut the path
    memcpy(path, p, (size_t)n);
    path[n] = 0;
    int fd = write ? open(path, O_WRONLY | O_CREAT | O_TRUNC, 0644) : open(path, O_RDONLY);
    if (fd < 0) return -errno;
    if (fd < 3) {
        // a standard stream was closed at startup: keep opened files above
        // the descriptors the output buffer and the kern side treat specially
        int d = fcntl(fd, F_DUPFD, 3);
        int saved = errno;
        close(fd);
        if (d < 0) return -saved;
        fd = d;
    }
    return fd;
}
int64_t kern_io_read(int32_t fd, char *buf, int64_t n) {
    ssize_t r = read(fd, buf, (size_t)n);
    return r < 0 ? -errno : (int64_t)r;
}
int64_t kern_io_write(int32_t fd, const char *p, int64_t n) {
    if (fd == 1) { kern_out(p, (size_t)n); return n; }
    ssize_t r = write(fd, p, (size_t)n);
    return r < 0 ? -errno : (int64_t)r;
}
// kern_io_close closes a descriptor the program opened (the kern side never
// passes the standard streams); a delayed write error surfaces here.
int32_t kern_io_close(int32_t fd) { if (fd < 0) return -EBADF; return close(fd) < 0 ? -errno : 0; }
int32_t kern_io_remove(const char *p, int64_t n) {
    char path[4096];
    if (n < 0 || (size_t)n >= sizeof path) return -ENAMETOOLONG;
    if (n > 0 && memchr(p, 0, (size_t)n) != NULL) return -EINVAL;
    memcpy(path, p, (size_t)n);
    path[n] = 0;
    return unlink(path) < 0 ? -errno : 0;
}
int64_t kern_io_argc(void) { return kern_argc; }
int64_t kern_io_arg(int64_t i, char *buf, int64_t n) {
    if (i < 0 || i >= kern_argc) return 0;
    size_t len = strlen(kern_argv[i]);
    memcpy(buf, kern_argv[i], len < (size_t)n ? len : (size_t)n);
    return (int64_t)len;
}
void kern_yield(void) { sched_yield(); }
uint64_t kern_now_ns(void) { struct timespec ts; clock_gettime(KERN_CLOCK, &ts); return (uint64_t)ts.tv_sec * 1000000000ull + (uint64_t)ts.tv_nsec; }

// ---- traps (spec §6, PLAN.md 2.2 signal-safety contract) ----

static const char *kern_trap_names[] = {"bounds", "overflow", "div_zero", "shift", "conversion", "generation", "vacant", "identity", "disjoint", "oom", "stack", "explicit", "unreachable"};

// TrapInfo with the §4.1 layout of `struct TrapInfo { kind: TrapKind; file: RawPtr[u8]; start: usize; end: usize; thread_id: u64 }`
typedef struct { uint8_t kind; uint8_t pad[7]; const char *file; uint64_t start, end, thread_id; } kern_trapinfo;

static void *_Atomic kern_handler = NULL;
static const char *kern_file = "";
static _Atomic uint64_t kern_next_tid = 1;

// per-thread state, preallocated at thread start: the guard range, the
// record buffer and the TrapInfo
static __thread uintptr_t kern_stack_lo = 0, kern_stack_hi = 0;
static __thread uint64_t kern_tid = 0;
static __thread int kern_in_trap = 0;
static __thread kern_trapinfo kern_info;
static __thread char kern_record[512];

void *kern_set_handler(void *fn) { return atomic_exchange(&kern_handler, fn); }
void kern_set_file(const char *name) { kern_file = name; }

static void kern_die(void) {
    kern_flush_try();
    signal(SIGABRT, SIG_DFL);
    abort();
}

static size_t kern_cat(char *b, size_t n, const char *s) { size_t l = strlen(s); memcpy(b + n, s, l); return n + l; }

// kern_trap_at runs the trap protocol: build TrapInfo, select the handler
// with one seq_cst load, run it at most once, terminate by SIGABRT. It uses
// no stdio, lock or allocation, so it may run in signal context.
static void kern_trap_at(int32_t kind, int64_t start, int64_t end, const char *file, int in_signal) {
    if (kern_in_trap) kern_die(); // a trap inside the handler terminates
    kern_in_trap = 1;
    if (!in_signal) kern_flush();
    kern_info.kind = (uint8_t)kind;
    kern_info.file = file ? file : kern_file; // the trapping function's file, or the program's
    kern_info.start = (uint64_t)start;
    kern_info.end = (uint64_t)end;
    kern_info.thread_id = kern_tid;
    void (*h)(void *) = atomic_load(&kern_handler);
    if (h == NULL) {
        // the default handler: one JSON record on standard error
        char *b = kern_record; size_t n = 0;
        n = kern_cat(b, n, "{\"trap\":{\"kind\":\"");
        n = kern_cat(b, n, kern_trap_names[kind]);
        n = kern_cat(b, n, "\",\"file\":\"");
        for (const char *c = kern_info.file; *c && n < 400; c++) {
            if (*c == '"' || *c == '\\') b[n++] = '\\';
            b[n++] = *c;
        }
        n = kern_cat(b, n, "\",\"span\":{\"start\":");
        n += kern_fmt_u64(b + n, (uint64_t)start);
        n = kern_cat(b, n, ",\"end\":");
        n += kern_fmt_u64(b + n, (uint64_t)end);
        n = kern_cat(b, n, "},\"thread\":");
        n += kern_fmt_u64(b + n, kern_tid);
        n = kern_cat(b, n, "}}\n");
        kern_write_all(2, b, n);
    } else {
        h(&kern_info);
    }
    kern_die();
}

void kern_trap(int32_t kind, int64_t start, int64_t end, const char *file) { kern_trap_at(kind, start, end, file, 0); }

// ---- the location of a fault (specs/native-trap-loc/SPEC.md) ----

// Every kern function of a hosted build sits in the kern text section
// behind a 16-byte prefix: the magic word, then the index of its
// declaration's location in the table the entry passed to
// kern_set_fn_locs, or -1 for a library function (whose location is its
// caller's). The fault handler names the user function the fault happened
// in: the one containing the faulting instruction, or else the nearest
// caller found through the link register, the word at the stack pointer
// and the frame-pointer chain. It reads only the kern text section and
// the live part of the thread's stack, so it cannot fault again.
#ifdef __APPLE__
extern const char kern_text_lo[] __asm("section$start$__TEXT$__kern_text");
extern const char kern_text_hi[] __asm("section$end$__TEXT$__kern_text");
#else
extern const char __start_kern_text[] __attribute__((weak));
extern const char __stop_kern_text[] __attribute__((weak));
#define kern_text_lo __start_kern_text
#define kern_text_hi __stop_kern_text
#endif
#define KERN_LOC_MAGIC 0x21434f4c4e52454bull
typedef struct { int64_t start, end; const char *file; } kern_fnloc;
static const kern_fnloc *kern_fn_locs = NULL;
static int64_t kern_fn_nlocs = 0;
void kern_set_fn_locs(const void *t, int64_t n) { kern_fn_locs = (const kern_fnloc *)t; kern_fn_nlocs = n; }

// kern_fn_at is the location of the user function whose code holds a: 1
// with *loc set, 0 for a library function, -1 for an address outside kern
// code. The marker is the nearest magic word before a, 4-aligned.
static int kern_fn_at(uintptr_t a, const kern_fnloc **loc) {
    uintptr_t lo = (uintptr_t)kern_text_lo, hi = (uintptr_t)kern_text_hi;
    if (lo == 0 || a < lo + 16 || a >= hi) return -1;
    for (uintptr_t p = (a - 8) & ~(uintptr_t)3;; p -= 4) {
        uint64_t w;
        __builtin_memcpy(&w, (const void *)p, 8);
        if (w == KERN_LOC_MAGIC) {
            int64_t i;
            __builtin_memcpy(&i, (const void *)(p + 8), 8);
            if (i < 0 || i >= kern_fn_nlocs || kern_fn_locs == NULL) return 0;
            *loc = &kern_fn_locs[i];
            return 1;
        }
        if (p < lo + 4) return -1;
    }
}

// kern_stack_word reads the word at a when it lies in the live part of the
// thread's stack: at or above floor (the stack pointer, the thread's stack
// base and, for a stack fault, the end of the faulting guard page; the
// probes touch the stack page by page, so the fault is in the guard's top
// page and everything above it is mapped) and below its top.
static int kern_stack_word(uintptr_t a, uintptr_t floor, uintptr_t *w) {
    if (kern_stack_lo == 0 || a < floor || a < kern_stack_lo || a >= kern_stack_hi || kern_stack_hi - a < 8 || (a & 7) != 0) return 0;
    __builtin_memcpy(w, (const void *)a, 8);
    return 1;
}

static uintptr_t kern_page = 4096; // the system page size, set at start

static const kern_fnloc *kern_fault_loc(void *ctx, uintptr_t floor) {
    uintptr_t pc = 0, lr = 0, fp = 0, sp = 0;
#if defined(__APPLE__) && defined(__aarch64__)
    ucontext_t *uc = (ucontext_t *)ctx;
    pc = (uintptr_t)__darwin_arm_thread_state64_get_pc(uc->uc_mcontext->__ss);
    lr = (uintptr_t)__darwin_arm_thread_state64_get_lr(uc->uc_mcontext->__ss);
    fp = (uintptr_t)__darwin_arm_thread_state64_get_fp(uc->uc_mcontext->__ss);
    sp = (uintptr_t)__darwin_arm_thread_state64_get_sp(uc->uc_mcontext->__ss);
#elif defined(__APPLE__) && defined(__x86_64__)
    ucontext_t *uc = (ucontext_t *)ctx;
    pc = uc->uc_mcontext->__ss.__rip; fp = uc->uc_mcontext->__ss.__rbp; sp = uc->uc_mcontext->__ss.__rsp;
#elif defined(__linux__) && defined(__x86_64__)
    ucontext_t *uc = (ucontext_t *)ctx;
    pc = (uintptr_t)uc->uc_mcontext.gregs[REG_RIP]; fp = (uintptr_t)uc->uc_mcontext.gregs[REG_RBP]; sp = (uintptr_t)uc->uc_mcontext.gregs[REG_RSP];
#elif defined(__linux__) && defined(__aarch64__)
    ucontext_t *uc = (ucontext_t *)ctx;
    pc = uc->uc_mcontext.pc; lr = uc->uc_mcontext.regs[30]; fp = uc->uc_mcontext.regs[29]; sp = uc->uc_mcontext.sp;
#else
    (void)ctx;
    return NULL;
#endif
    if (sp > floor) floor = sp;
    const kern_fnloc *loc = NULL;
    if (kern_fn_at(pc, &loc) == 1) return loc;
    // a return address names the call, one byte before it
    if (lr != 0 && kern_fn_at(lr - 1, &loc) == 1) return loc;
    uintptr_t w;
    if (lr == 0 && kern_stack_word(sp, floor, &w) && kern_fn_at(w - 1, &loc) == 1) return loc;
    for (int n = 0; n < 4096; n++) {
        uintptr_t next, ret;
        if (!kern_stack_word(fp, floor, &next) || fp > UINTPTR_MAX - 8 || !kern_stack_word(fp + 8, floor, &ret)) break;
        if (kern_fn_at(ret - 1, &loc) == 1) return loc;
        if (next <= fp) break;
        fp = next;
    }
    return NULL;
}

// the fault handler classifies the fault as `stack` iff the address lies in
// the thread's guard range; any other fault in safe code is `unreachable`.
// Either names the user function it happened in, when it can be found.
static void kern_segv(int sig, siginfo_t *si, void *ctx) {
    (void)sig;
    uintptr_t addr = (uintptr_t)si->si_addr;
    int32_t kind = 12;
    uintptr_t floor = 0;
    if (kern_stack_lo != 0 && addr < kern_stack_lo + 4096 && addr + (1 << 20) >= kern_stack_lo) {
        kind = 10;
        floor = (addr | (kern_page - 1)) + 1;
    }
    const kern_fnloc *loc = kern_fault_loc(ctx, floor);
    if (loc != NULL) kern_trap_at(kind, loc->start, loc->end, loc->file, 1);
    kern_trap_at(kind, 0, 0, NULL, 1);
}

static void kern_thread_init(void *altstack) {
    stack_t ss = { .ss_sp = altstack, .ss_size = 65536, .ss_flags = 0 };
    sigaltstack(&ss, NULL);
#ifdef __APPLE__
    kern_stack_hi = (uintptr_t)pthread_get_stackaddr_np(pthread_self());
    kern_stack_lo = kern_stack_hi - pthread_get_stacksize_np(pthread_self());
#else
    pthread_attr_t at; void *lo; size_t sz;
    if (pthread_getattr_np(pthread_self(), &at) == 0 && pthread_attr_getstack(&at, &lo, &sz) == 0) {
        kern_stack_lo = (uintptr_t)lo; kern_stack_hi = kern_stack_lo + sz;
        pthread_attr_destroy(&at);
    }
#endif
}

void kern_start(void) {
    static char altstack[65536];
    long pg = sysconf(_SC_PAGESIZE);
    if (pg > 0) kern_page = (uintptr_t)pg;
    kern_thread_init(altstack);
    struct sigaction sa;
    memset(&sa, 0, sizeof sa);
    sa.sa_sigaction = kern_segv;
    sa.sa_flags = SA_SIGINFO | SA_ONSTACK;
    sigaction(SIGSEGV, &sa, NULL);
    sigaction(SIGBUS, &sa, NULL);
    atexit(kern_flush);
}

// ---- allocation failure injection ----

// kern_fail_next is armed by testing.fail_next_alloc: the next allocation
// (a runtime object's, or the system allocator's, which the kern containers
// use) fails.
static _Atomic int kern_fail_next = 0;
#ifdef KERN_TEST_HOOKS
void kern_test_fail_next_alloc(void) { atomic_store(&kern_fail_next, 1); }
// kern_test_require_hooks exists only in a --test-hooks build: a program
// using the kern-side test hooks without it fails to link.
void kern_test_require_hooks(void) {}
#endif
// Every entry point below that can raise a trap takes the location of the
// user-level call last, as a pointer to a record the backend emits once
// per call site (a constant, or a stack slot inside a library function,
// whose location is its caller's), and raises its traps there, so that a
// runtime-raised trap names that call as the interpreter does
// (specs/runtime-trap-file/SPEC.md). One pointer costs what the two span
// immediates of the old kern_mutex_lock did; three arguments cost the
// uncontended lock four instructions. A null record is no location: the
// span 0-0 and the program's file. A null file in a record is the
// program's file with the record's span, as a null file is for kern_trap
// (a function without a file of its own; the interpreter reports the
// same).
typedef struct { int64_t start, end; const char *file; } kern_loc;
static void kern_trap_loc(int32_t kind, const kern_loc *at) {
    if (at == NULL) kern_trap(kind, 0, 0, NULL);
    kern_trap(kind, at->start, at->end, at->file);
}
static void *kern_alloc(size_t n, const kern_loc *at) {
    if (atomic_exchange(&kern_fail_next, 0)) kern_trap_loc(9, at);
    void *p = malloc(n ? n : 1);
    if (p == NULL) kern_trap_loc(9, at);
    return p;
}

// ---- container identity tags (spec §5.6) ----

// kern_next_identity issues the next container identity tag. Tags are a
// debug-build feature (PLAN.md 2.2): a release build issues identity 0,
// which disables the check; so does the counter once it wraps to 0.
static _Atomic uint32_t kern_identity = 1;
uint32_t kern_next_identity(void) {
#ifdef KERN_RELEASE
    return 0;
#else
    return atomic_fetch_add(&kern_identity, 1); // 0 after wrap-around: unchecked
#endif
}

// ---- Shared ----

typedef struct { _Atomic uint64_t count; int64_t size; char data[]; } kern_shared;
void *kern_shared_new(int64_t size, const void *src, const kern_loc *at) {
    kern_shared *s = kern_alloc(sizeof *s + (size_t)size, at);
    atomic_store(&s->count, 1); s->size = size;
    memcpy(s->data, src, (size_t)size);
    return s;
}
void *kern_shared_get(kern_shared *s) { return s->data; }
void *kern_shared_clone(kern_shared *s, const kern_loc *at) {
    uint64_t c = atomic_load(&s->count);
    for (;;) {
        if (c == UINT64_MAX) kern_trap_loc(1, at);
        if (atomic_compare_exchange_weak(&s->count, &c, c + 1)) return s;
    }
}
void kern_shared_drop(kern_shared *s, kern_glue g) {
    if (s == NULL) return;
    if (atomic_fetch_sub(&s->count, 1) == 1) { if (g) g(s->data); free(s); }
}
#ifdef KERN_TEST_HOOKS
void kern_test_shared_seed_count(kern_shared *s, int64_t n) { atomic_store(&s->count, (uint64_t)n); }
#endif

// ---- Mutex ----

// A Mutex is the runtime's own lock (specs/perf-gaps/SPEC.md): a 32-bit
// state that waiters park on (0 free; 1 held; 2 held, and a thread has
// set it so before parking, so the release must unpark one), and the
// holder's thread id beside it in the same cache line. The holder
// writes its id after the acquiring exchange, when it already owns the
// line, and clears it before the releasing exchange, so the id costs no
// coherence traffic of its own; a thread that fails the fast path and
// finds its own id there is acquiring the mutex it holds, which traps
// (spec §13 accepted decision, as the interpreter does). Only the holder
// writes its id and a thread reads its own last write, so the relaxed
// load never mistakes another thread's lock for a recursive one.
//
// This replaced a default pthread mutex with the owner stored beside it:
// the owner then shared the line that arriving waiters write with their
// compare-and-swap attempts, which cost lock_protected_map about 15% of
// its throughput, and every layout that moved the owner to another line
// cost thread_pool as much, since each hand-off then migrated one more
// line between cores (docs/reports/perf-gaps.md). There is no spin loop
// before parking: spinning makes the pool's hand-off between workers
// fair and migrates the line on every job, at 0.69 of C.
//
// Parking is os_sync_wait_on_address on macOS and a futex on Linux. Both
// compare the 32-bit value and sleep only while it is unchanged, so a
// wake between a caller's check and its sleep is not lost; both return
// without a wake at times, with or without an error, and every caller
// treats any return as spurious and re-checks before parking again. An
// unpark reports at most that nothing was parked, which is ignored (a
// notify with no waiter is lost, spec §5.5); the address is the
// runtime's own aligned word and the size and flags are constants, so
// the other documented errors cannot arise.
#if defined(__APPLE__)
#include <os/os_sync_wait_on_address.h>
static void kern_park(_Atomic(uint32_t) *w, uint32_t expect) { (void)os_sync_wait_on_address(w, expect, sizeof *w, OS_SYNC_WAIT_ON_ADDRESS_NONE); }
static void kern_unpark_one(_Atomic(uint32_t) *w) { (void)os_sync_wake_by_address_any(w, sizeof *w, OS_SYNC_WAKE_BY_ADDRESS_NONE); }
static void kern_unpark_all(_Atomic(uint32_t) *w) { (void)os_sync_wake_by_address_all(w, sizeof *w, OS_SYNC_WAKE_BY_ADDRESS_NONE); }
#elif defined(__linux__)
#include <linux/futex.h>
#include <sys/syscall.h>
#include <unistd.h>
static void kern_park(_Atomic(uint32_t) *w, uint32_t expect) { (void)syscall(SYS_futex, w, FUTEX_WAIT_PRIVATE, expect, NULL, NULL, 0); }
static void kern_unpark_one(_Atomic(uint32_t) *w) { (void)syscall(SYS_futex, w, FUTEX_WAKE_PRIVATE, 1, NULL, NULL, 0); }
static void kern_unpark_all(_Atomic(uint32_t) *w) { (void)syscall(SYS_futex, w, FUTEX_WAKE_PRIVATE, INT32_MAX, NULL, NULL, 0); }
#else
#error "kern: no park primitive for this platform (os_sync_wait_on_address on macOS 14.4+, futex on Linux)"
#endif

typedef struct { _Atomic(uint32_t) state; _Atomic(uintptr_t) owner; int64_t size; char data[]; } kern_mutex;
_Static_assert(offsetof(kern_mutex, owner) == 8 && offsetof(kern_mutex, size) == 16 && offsetof(kern_mutex, data) == 24, "the state and the owner share the first 16 bytes (specs/perf-gaps/SPEC.md R6)");
static inline uintptr_t kern_self(void) { return (uintptr_t)pthread_self(); }
void *kern_mutex_new(int64_t size, const void *src, const kern_loc *at) {
    kern_mutex *mx = kern_alloc(sizeof *mx + (size_t)size, at);
    atomic_init(&mx->state, 0);
    atomic_init(&mx->owner, 0);
    mx->size = size;
    memcpy(mx->data, src, (size_t)size);
    return mx;
}
// kern_mutex_acquire takes a lock whose fast path failed: it exchanges
// the state to 2 (acquiring at once when it was free) and parks while it
// is 2. A thread that parked cannot know whether another is parked, so it
// acquires as 2 and its release wakes one more.
static void kern_mutex_acquire(kern_mutex *mx) {
    while (atomic_exchange_explicit(&mx->state, 2, memory_order_acquire) != 0) kern_park(&mx->state, 2);
}
void *kern_mutex_lock(kern_mutex *mx, const kern_loc *at) {
    uintptr_t self = kern_self();
    uint32_t s = 0;
    if (!atomic_compare_exchange_strong_explicit(&mx->state, &s, 1, memory_order_acquire, memory_order_relaxed)) {
        if (atomic_load_explicit(&mx->owner, memory_order_relaxed) == self) kern_trap_loc(11, at); // recursive acquisition
        kern_mutex_acquire(mx);
    }
    atomic_store_explicit(&mx->owner, self, memory_order_relaxed);
    return mx->data;
}
static inline void kern_mutex_release(kern_mutex *mx) {
    atomic_store_explicit(&mx->owner, 0, memory_order_relaxed);
    if (atomic_exchange_explicit(&mx->state, 0, memory_order_release) == 2) kern_unpark_one(&mx->state);
}
void kern_mutex_unlock(kern_mutex *mx) { kern_mutex_release(mx); }
void kern_mutex_drop(kern_mutex *mx, kern_glue g) { if (mx == NULL) return; if (g) g(mx->data); free(mx); }

// ---- Condvar (spec §5.5) ----

// wait is given the guard a Mutex lock returned (the checker requires it to
// be that edit, unprojected), which is the Mutex's data pointer. A wait by a
// thread that does not hold the lock, or with a different Mutex from the
// one the Condvar's first wait used, traps with kind explicit, as the
// interpreter does. The Condvar is a sequence number: a waiter reads it
// under the lock, releases the Mutex and parks while the number is
// unchanged; a notify bumps it and wakes one or every parked thread,
// which then reacquire the Mutex. A notify between the read and the
// park is not lost (the park returns at once), a return without a
// notify is a spurious wakeup, which the spec permits, and a multiple
// of 2^32 notifies between the read and the park is the accepted
// limitation of every 32-bit sequence (specs/perf-gaps/SPEC.md §2).
typedef struct { _Atomic(uint32_t) seq; _Atomic(uintptr_t) mutex; } kern_condvar;
void *kern_condvar_new(const kern_loc *at) {
    kern_condvar *cv = kern_alloc(sizeof *cv, at);
    atomic_init(&cv->seq, 0);
    atomic_init(&cv->mutex, 0);
    return cv;
}
void kern_condvar_wait(kern_condvar *cv, void *data, const kern_loc *at) {
    kern_mutex *mx = (kern_mutex *)((char *)data - offsetof(kern_mutex, data));
    uintptr_t self = kern_self();
    if (atomic_load_explicit(&mx->owner, memory_order_relaxed) != self) kern_trap_loc(11, at); // the lock is not held by this thread
    uintptr_t bound = atomic_load_explicit(&cv->mutex, memory_order_relaxed), want = 0;
    if (bound != (uintptr_t)mx && !atomic_compare_exchange_strong(&cv->mutex, &want, (uintptr_t)mx) && want != (uintptr_t)mx) kern_trap_loc(11, at); // a second Mutex on one Condvar
    uint32_t seq = atomic_load_explicit(&cv->seq, memory_order_relaxed);
    kern_mutex_release(mx);
    kern_park(&cv->seq, seq);
    kern_mutex_acquire(mx);
    atomic_store_explicit(&mx->owner, self, memory_order_relaxed);
}
void kern_condvar_notify_one(kern_condvar *cv) { atomic_fetch_add_explicit(&cv->seq, 1, memory_order_relaxed); kern_unpark_one(&cv->seq); }
void kern_condvar_notify_all(kern_condvar *cv) { atomic_fetch_add_explicit(&cv->seq, 1, memory_order_relaxed); kern_unpark_all(&cv->seq); }
void kern_condvar_drop(kern_condvar *cv) { free(cv); }

// ---- threads (spec §5.5) ----

typedef struct { pthread_t t; char *block; int64_t roff; int joined, started; } kern_thread;
typedef struct { kern_thread **joins; int64_t n, cap; } kern_scope;
typedef struct { kern_thread *t; int taken; } kern_join;

typedef struct { void (*thunk)(void *); char *block; } kern_spawn_arg;
static void *kern_thread_run(void *arg) {
    kern_spawn_arg *s = arg;
    char *altstack = malloc(65536);
    kern_tid = atomic_fetch_add(&kern_next_tid, 1);
    if (altstack) kern_thread_init(altstack);
    s->thunk(s->block);
    stack_t ss = { .ss_flags = SS_DISABLE };
    sigaltstack(&ss, NULL);
    free(altstack);
    free(s);
    return NULL;
}
static kern_thread *kern_spawn_common(void (*thunk)(void *), const void *pack, int64_t size, int64_t roff, int *ok, const kern_loc *at) {
    kern_thread *t = kern_alloc(sizeof *t, at);
    t->block = kern_alloc((size_t)size, at);
    memcpy(t->block, pack, (size_t)size);
    t->roff = roff; t->joined = 0; t->started = 0;
    kern_spawn_arg *s = kern_alloc(sizeof *s, at);
    s->thunk = thunk; s->block = t->block;
    if (pthread_create(&t->t, NULL, kern_thread_run, s) != 0) { free(s); free(t->block); free(t); *ok = 0; return NULL; }
    t->started = 1;
    *ok = 1;
    return t;
}
void kern_thread_spawn(void (*thunk)(void *), const void *pack, int64_t size, int64_t roff, char *out, int64_t off, const kern_loc *at) {
    int ok;
    kern_thread *t = kern_spawn_common(thunk, pack, size, roff, &ok, at);
    if (!ok) { out[0] = 1; return; }
    out[0] = 0;
    memcpy(out + off, &t, sizeof t);
}
void kern_thread_join(kern_thread *t, void *dst, int64_t size) {
    if (!t->joined) { pthread_join(t->t, NULL); t->joined = 1; }
    memcpy(dst, t->block + t->roff, (size_t)size);
    free(t->block); free(t);
}
void kern_thread_drop(kern_thread *t) {
    if (t == NULL) return;
    if (!t->joined) pthread_detach(t->t); // the detached thread's result is leaked (Phase 2 limitation)
    free(t);
}
void *kern_scope_new(const kern_loc *at) { kern_scope *s = kern_alloc(sizeof *s, at); memset(s, 0, sizeof *s); return s; }
void kern_scope_spawn(kern_scope *sc, void (*thunk)(void *), const void *pack, int64_t size, int64_t roff, char *out, int64_t off, const kern_loc *at) {
    // reserve the join slot before the worker starts, so a failed
    // reservation is an ordinary spawn failure
    if (sc->n == sc->cap) {
        int64_t ncap = sc->cap ? sc->cap * 2 : 4;
        kern_thread **nj = realloc(sc->joins, (size_t)ncap * sizeof *sc->joins);
        if (!nj) { out[0] = 1; return; }
        sc->joins = nj; sc->cap = ncap;
    }
    int ok;
    kern_thread *t = kern_spawn_common(thunk, pack, size, roff, &ok, at);
    if (!ok) { out[0] = 1; return; }
    sc->joins[sc->n++] = t;
    kern_join *j = kern_alloc(sizeof *j, at);
    j->t = t; j->taken = 0;
    out[0] = 0;
    memcpy(out + off, &j, sizeof j);
}
void kern_join_get(kern_join *j, void *dst, int64_t size) {
    if (!j->t->joined) { pthread_join(j->t->t, NULL); j->t->joined = 1; }
    memcpy(dst, j->t->block + j->t->roff, (size_t)size);
    j->taken = 1;
    free(j);
}
void kern_join_drop(kern_join *j, kern_glue g) {
    if (j == NULL) return;
    if (!j->taken) {
        if (!j->t->joined) { pthread_join(j->t->t, NULL); j->t->joined = 1; }
        if (g) g(j->t->block + j->t->roff);
    }
    free(j);
}
void kern_scope_drop(kern_scope *sc) {
    if (sc == NULL) return;
    for (int64_t i = 0; i < sc->n; i++) {
        kern_thread *t = sc->joins[i];
        if (!t->joined) { pthread_join(t->t, NULL); t->joined = 1; }
        free(t->block); free(t);
    }
    free(sc->joins); free(sc);
}

// ---- SystemAllocator (spec §6) ----

typedef struct { int unused; } kern_sysalloc;
void *kern_sysalloc_new(void) { static kern_sysalloc a; return &a; }
void kern_sysalloc_alloc(kern_sysalloc *a, int64_t size, int64_t align, char *out, int64_t off, const kern_loc *at) {
    (void)a;
    if (size < 0 || align <= 0 || (align & (align - 1)) != 0 || align > 4096 || size % align != 0) kern_trap_loc(11, at);
    if (atomic_exchange(&kern_fail_next, 0)) { out[0] = 1; return; } // the armed failure: Err(OutOfMemory)
    // aligned_alloc needs an alignment of at least sizeof(void *) and a size
    // that is a multiple of it (macOS rejects smaller alignments): the
    // request is rounded up; the §6 obligations above are the caller's values
    size_t al = (size_t)align < sizeof(void *) ? sizeof(void *) : (size_t)align;
    if ((uint64_t)size > SIZE_MAX - (al - 1)) { out[0] = 1; return; } // unrepresentable after rounding
    size_t sz = ((size_t)size + al - 1) / al * al;
    void *p = aligned_alloc(al, sz ? sz : al);
    if (p == NULL) { out[0] = 1; return; }
    out[0] = 0;
    memcpy(out + off, &p, sizeof p);
}
// kern_sysalloc_realloc resizes an allocation in place when the allocator
// can (realloc, whose result is 16-byte aligned on every supported host),
// else by aligned_alloc, copy and free; on failure the old allocation stays.
void kern_sysalloc_realloc(kern_sysalloc *a, void *p, int64_t old_size, int64_t size, int64_t align, char *out, int64_t off, const kern_loc *at) {
    (void)a;
    if (old_size < 0 || size < 0 || align <= 0 || (align & (align - 1)) != 0 || align > 4096 || size % align != 0) kern_trap_loc(11, at);
    if (atomic_exchange(&kern_fail_next, 0)) { out[0] = 1; return; }
    size_t al = (size_t)align < sizeof(void *) ? sizeof(void *) : (size_t)align;
    if ((uint64_t)size > SIZE_MAX - (al - 1)) { out[0] = 1; return; }
    size_t sz = ((size_t)size + al - 1) / al * al;
    if (sz == 0) sz = al;
    void *np;
    if (al <= 16) np = realloc(p, sz);
    else {
        np = aligned_alloc(al, sz);
        if (np) { memcpy(np, p, (size_t)(old_size < size ? old_size : size)); free(p); }
    }
    if (np == NULL) { out[0] = 1; return; }
    out[0] = 0;
    memcpy(out + off, &np, sizeof np);
}
// kern_sysalloc_alloc_zeroed is kern_sysalloc_alloc with zeroed bytes:
// calloc (16-byte aligned on every supported host, and a large block comes
// from fresh zero pages without a memset), else aligned_alloc and memset.
void kern_sysalloc_alloc_zeroed(kern_sysalloc *a, int64_t size, int64_t align, char *out, int64_t off, const kern_loc *at) {
    (void)a;
    if (size < 0 || align <= 0 || (align & (align - 1)) != 0 || align > 4096 || size % align != 0) kern_trap_loc(11, at);
    if (atomic_exchange(&kern_fail_next, 0)) { out[0] = 1; return; }
    size_t al = (size_t)align < sizeof(void *) ? sizeof(void *) : (size_t)align;
    if ((uint64_t)size > SIZE_MAX - (al - 1)) { out[0] = 1; return; }
    size_t sz = ((size_t)size + al - 1) / al * al;
    if (sz == 0) sz = al;
    void *p;
    if (al <= 16) p = calloc(1, sz);
    else {
        p = aligned_alloc(al, sz);
        if (p) memset(p, 0, sz);
    }
    if (p == NULL) { out[0] = 1; return; }
    out[0] = 0;
    memcpy(out + off, &p, sizeof p);
}
void kern_sysalloc_free(kern_sysalloc *a, void *p, int64_t size, int64_t align, const kern_loc *at) { (void)a; (void)size; (void)align; (void)at; free(p); }
