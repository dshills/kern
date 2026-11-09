// kern hosted runtime: the trap protocol, the fault handler and the
// location of a fault.
// Included by kern_rt.c; see kern_rt.h for the entries.

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

static _Noreturn void kern_die(void) {
    kern_flush_try();
    signal(SIGABRT, SIG_DFL);
    abort();
}

static size_t kern_cat(char *b, size_t n, const char *s) { size_t l = strlen(s); memcpy(b + n, s, l); return n + l; }

// kern_trap_at runs the trap protocol: build TrapInfo, select the handler
// with one seq_cst load, run it at most once, terminate by SIGABRT. It uses
// no stdio, lock or allocation, so it may run in signal context.
static _Noreturn void kern_trap_at(int32_t kind, int64_t start, int64_t end, const char *file, int in_signal) {
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
        (void)kern_write_all(2, b, n);
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

static uintptr_t kern_page = 4096; // the system page size, set at start

// kern_probe is a pipe the fault handler copies stack words through: the
// kernel reads the word for write(2) and answers EFAULT where a direct
// read would fault again (below the Linux main thread's stack lies an
// unmapped gap of up to 1 MiB in which a stack fault can land, and an
// emulator may keep a guard region there that is mapped but unreadable).
static int kern_probe[2] = {-1, -1};
// kern_probe_busy pairs each write with its own read when threads fault
// at once: held only across those two calls, which do not block (the pipe
// has room for one word and then holds it), and never re-entered by its
// holder, whose SIGSEGV is blocked while the handler runs.
static atomic_flag kern_probe_busy = ATOMIC_FLAG_INIT;

// kern_stack_word reads the word at a when it lies in the live part of the
// thread's stack (at or above floor, the stack pointer, the thread's stack
// base and, for a stack fault, the end of the faulting page; below its
// top) and the kernel can read it.
static int kern_stack_word(uintptr_t a, uintptr_t floor, uintptr_t *w) {
    if (kern_stack_lo == 0 || a < floor || a < kern_stack_lo || a >= kern_stack_hi || kern_stack_hi - a < 8 || (a & 7) != 0) return 0;
    if (kern_probe[0] < 0) return 0;
    while (atomic_flag_test_and_set_explicit(&kern_probe_busy, memory_order_acquire)) { }
    int ok = write(kern_probe[1], (const void *)a, 8) == 8 && read(kern_probe[0], w, 8) == 8;
    atomic_flag_clear_explicit(&kern_probe_busy, memory_order_release);
    return ok;
}

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
