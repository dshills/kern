// kern hosted runtime: the trap protocol, the fault handler, the location
// of a fault, line tables and the backtrace.
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
// kern_cat_json appends s as the body of a JSON string, stopping before
// the buffer's position reaches limit.
static size_t kern_cat_json(char *b, size_t n, size_t limit, const char *s) {
    for (const char *c = s; *c && n + 2 < limit; c++) {
        if (*c == '"' || *c == '\\') b[n++] = '\\';
        b[n++] = *c;
    }
    return n;
}

// ---- line tables (specs/runtime RE1) ----
//
// The entry code registers one record per source file of the program: its
// name (the same constant every trap site passes), the byte offset of each
// line's first byte, and the file's length. A trap's line and column are
// computed from them with no allocation and no lock.
typedef struct { const char *name; const int32_t *starts; int64_t count; int64_t size; } kern_srcfile;
static const kern_srcfile *kern_files = NULL;
static int64_t kern_nfiles = 0;
void kern_set_lines(const void *t, int64_t n) { kern_files = (const kern_srcfile *)t; kern_nfiles = n; }

// kern_line_col is the 1-based line and byte column of offset start in
// file: 0 and 0 when no table names the file, when start is past the
// file's end, or for the span 0-0, which stands for "no location".
static void kern_line_col(const char *file, int64_t start, int64_t end, uint64_t *line, uint64_t *col) {
    *line = 0; *col = 0;
    const kern_srcfile *files = kern_files, *f = NULL;
    const int64_t nfiles = kern_nfiles;
    if (file == NULL || files == NULL || (start == 0 && end == 0)) return;
    // by the name's address (the constant a trap site passes), else by its text
    for (int64_t i = 0; i < nfiles; i++) if (files[i].name == file) { f = &files[i]; break; }
    if (f == NULL) for (int64_t i = 0; i < nfiles; i++) if (files[i].name != NULL && strcmp(files[i].name, file) == 0) { f = &files[i]; break; }
    if (f == NULL || f->count <= 0 || start < 0 || start > f->size) return;
    int64_t lo = 0, hi = f->count; // the last line start at or before start
    while (hi - lo > 1) {
        int64_t mid = (lo + hi) / 2;
        if ((int64_t)f->starts[mid] <= start) lo = mid; else hi = mid;
    }
    *line = (uint64_t)lo + 1;
    *col = (uint64_t)(start - (int64_t)f->starts[lo]) + 1;
}

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
// kern_fnloc is one function's entry in the table: its declaration's span
// and file, and its qualified name (for the backtrace, specs/runtime RE2).
typedef struct { int64_t start, end; const char *file; const char *name; } kern_fnloc;
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

// kern_regs is where a walk of the stack starts: the registers of the
// interrupted code for a fault, or the caller's frame for a trap raised by
// a call (pc and lr 0).
typedef struct { uintptr_t pc, lr, fp, sp, floor; } kern_regs;

// kern_regs_of reads the registers a fault's signal context holds; 0 on a
// platform whose context the runtime does not read.
static int kern_regs_of(void *ctx, kern_regs *r) {
    r->pc = r->lr = r->fp = r->sp = 0;
#if defined(__APPLE__) && defined(__aarch64__)
    ucontext_t *uc = (ucontext_t *)ctx;
    r->pc = (uintptr_t)__darwin_arm_thread_state64_get_pc(uc->uc_mcontext->__ss);
    r->lr = (uintptr_t)__darwin_arm_thread_state64_get_lr(uc->uc_mcontext->__ss);
    r->fp = (uintptr_t)__darwin_arm_thread_state64_get_fp(uc->uc_mcontext->__ss);
    r->sp = (uintptr_t)__darwin_arm_thread_state64_get_sp(uc->uc_mcontext->__ss);
#elif defined(__APPLE__) && defined(__x86_64__)
    ucontext_t *uc = (ucontext_t *)ctx;
    r->pc = uc->uc_mcontext->__ss.__rip; r->fp = uc->uc_mcontext->__ss.__rbp; r->sp = uc->uc_mcontext->__ss.__rsp;
#elif defined(__linux__) && defined(__x86_64__)
    ucontext_t *uc = (ucontext_t *)ctx;
    r->pc = (uintptr_t)uc->uc_mcontext.gregs[REG_RIP]; r->fp = (uintptr_t)uc->uc_mcontext.gregs[REG_RBP]; r->sp = (uintptr_t)uc->uc_mcontext.gregs[REG_RSP];
#elif defined(__linux__) && defined(__aarch64__)
    ucontext_t *uc = (ucontext_t *)ctx;
    r->pc = uc->uc_mcontext.pc; r->lr = uc->uc_mcontext.regs[30]; r->fp = uc->uc_mcontext.regs[29]; r->sp = uc->uc_mcontext.sp;
#else
    (void)ctx;
    return 0;
#endif
    if (r->sp > r->floor) r->floor = r->sp;
    return 1;
}

// kern_walk lists the user functions on the stack, innermost first: the
// one holding pc, the caller the link register (or, with none, the word at
// the stack pointer) names, then every return address of the frame-pointer
// chain that lies in a user function. Library and C frames are passed
// over. At most max are stored; *more is set when there were more. Every
// stack word is read through the probe, so a broken chain ends the walk.
static int kern_walk(const kern_regs *r, const kern_fnloc **out, int max, int *more) {
    int n = 0;
    const kern_fnloc *loc = NULL;
    uintptr_t first = 0, w; // the return address already taken from a register or the stack top
    *more = 0;
    if (r->pc != 0 && kern_fn_at(r->pc, &loc) == 1) out[n++] = loc;
    // a return address names the call, one byte before it
    if (r->lr != 0) {
        first = r->lr;
        if (n < max && kern_fn_at(r->lr - 1, &loc) == 1) out[n++] = loc;
    } else if (r->pc != 0 && kern_stack_word(r->sp, r->floor, &w)) {
        first = w;
        if (n < max && kern_fn_at(w - 1, &loc) == 1) out[n++] = loc;
    }
    uintptr_t fp = r->fp;
    for (int steps = 0; steps < 65536; steps++) {
        uintptr_t next, ret;
        if (!kern_stack_word(fp, r->floor, &next) || fp > UINTPTR_MAX - 8 || !kern_stack_word(fp + 8, r->floor, &ret)) break;
        // the first frame's return address is the one the link register
        // still held when the faulting function had saved it
        if (!(steps == 0 && ret == first) && kern_fn_at(ret - 1, &loc) == 1) {
            if (n == max) { *more = 1; break; }
            out[n++] = loc;
        }
        if (next <= fp) break;
        fp = next;
    }
    return n;
}

// ---- the backtrace (specs/runtime RE2) ----
//
// When enabled (KERN_BACKTRACE=1, or a build with --backtrace that
// KERN_BACKTRACE=0 does not turn off), the default handler writes, after
// the trap record, one line naming the kern functions on the trapping
// thread's stack, innermost first. The line is assembled in one buffer,
// which the first trapping thread takes; a second thread trapping at the
// same moment writes its record without a backtrace.

#define KERN_BT_FRAMES 64
static int kern_bt_build = 0;
void kern_set_backtrace(int32_t on) { kern_bt_build = on != 0; }
static int kern_bt_enabled(void) { return kern_cfg.backtrace >= 0 ? kern_cfg.backtrace : kern_bt_build; }
static char kern_bt_buf[16384];
static atomic_flag kern_bt_busy = ATOMIC_FLAG_INIT;

static void kern_backtrace(const kern_regs *r) {
    if (atomic_flag_test_and_set(&kern_bt_busy)) return;
    const kern_fnloc *frames[KERN_BT_FRAMES];
    int more = 0;
    int count = kern_walk(r, frames, KERN_BT_FRAMES, &more);
    char *b = kern_bt_buf;
    const size_t limit = sizeof kern_bt_buf - 64; // room for the line's end
    size_t n = 0;
    n = kern_cat(b, n, "{\"backtrace\":{\"thread\":");
    n += kern_fmt_u64(b + n, kern_tid);
    n = kern_cat(b, n, ",\"frames\":[");
    int listed = 0;
    for (int i = 0; i < count; i++) {
        const kern_fnloc *f = frames[i];
        // The thunk that calls main carries main's location under an entry
        // of its own, which shares the name's constant: its frame behind
        // main's own is not a second main. (When main was inlined into the
        // thunk, the thunk's is the only frame, and it is listed.)
        if (i > 0 && f != frames[i - 1] && f->name != NULL && f->name == frames[i - 1]->name) continue;
        if (n + 96 >= limit) { more = 1; break; } // a frame's fixed text and its line number always fit
        uint64_t line, col;
        kern_line_col(f->file, f->start, f->end, &line, &col);
        if (listed++ > 0) b[n++] = ',';
        n = kern_cat(b, n, "{\"function\":\"");
        n = kern_cat_json(b, n, limit - 64, f->name ? f->name : "");
        n = kern_cat(b, n, "\",\"file\":\"");
        n = kern_cat_json(b, n, limit - 32, f->file ? f->file : kern_file);
        n = kern_cat(b, n, "\",\"line\":");
        n += kern_fmt_u64(b + n, line);
        b[n++] = '}';
    }
    n = kern_cat(b, n, more ? "],\"truncated\":true}}\n" : "]}}\n");
    (void)kern_write_all(2, b, n);
}

// kern_trap_at runs the trap protocol: build TrapInfo, select the handler
// with one seq_cst load, run it at most once, terminate by SIGABRT. It uses
// no stdio, lock or allocation, so it may run in signal context. regs is
// where the stack walk of a backtrace starts.
static _Noreturn void kern_trap_at(int32_t kind, int64_t start, int64_t end, const char *file, int in_signal, const kern_regs *regs) {
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
        uint64_t line, col;
        kern_line_col(kern_info.file, start, end, &line, &col);
        char *b = kern_record; size_t n = 0;
        n = kern_cat(b, n, "{\"trap\":{\"kind\":\"");
        n = kern_cat(b, n, kern_trap_names[kind]);
        n = kern_cat(b, n, "\",\"file\":\"");
        n = kern_cat_json(b, n, 400, kern_info.file);
        n = kern_cat(b, n, "\",\"span\":{\"start\":");
        n += kern_fmt_u64(b + n, (uint64_t)start);
        n = kern_cat(b, n, ",\"end\":");
        n += kern_fmt_u64(b + n, (uint64_t)end);
        n = kern_cat(b, n, "},\"line\":");
        n += kern_fmt_u64(b + n, line);
        n = kern_cat(b, n, ",\"col\":");
        n += kern_fmt_u64(b + n, col);
        n = kern_cat(b, n, ",\"thread\":");
        n += kern_fmt_u64(b + n, kern_tid);
        n = kern_cat(b, n, "}}\n");
        (void)kern_write_all(2, b, n);
        if (regs != NULL && kern_bt_enabled()) kern_backtrace(regs);
    } else {
        h(&kern_info);
    }
    kern_die();
}

void kern_trap(int32_t kind, int64_t start, int64_t end, const char *file) {
    // the walk of a backtrace starts at this function's frame: its return
    // address is in the code that raised the trap
    kern_regs r = {0, 0, (uintptr_t)__builtin_frame_address(0), 0, 0};
    r.floor = (uintptr_t)&r;
    kern_trap_at(kind, start, end, file, 0, &r);
}

// the fault handler classifies the fault as `stack` iff the address lies in
// the thread's guard range; any other fault in safe code is `unreachable`.
// Either names the user function it happened in, when it can be found.
static void kern_segv(int sig, siginfo_t *si, void *ctx) {
    (void)sig;
    uintptr_t addr = (uintptr_t)si->si_addr;
    int32_t kind = 12;
    kern_regs r = {0, 0, 0, 0, 0};
    if (kern_stack_lo != 0 && addr < kern_stack_lo + 4096 && addr + (1 << 20) >= kern_stack_lo) {
        kind = 10;
        r.floor = (addr | (kern_page - 1)) + 1;
    }
    const kern_fnloc *loc = NULL;
    int more = 0;
    int have = kern_regs_of(ctx, &r);
    if (have && kern_walk(&r, &loc, 1, &more) == 1) kern_trap_at(kind, loc->start, loc->end, loc->file, 1, &r);
    kern_trap_at(kind, 0, 0, NULL, 1, have ? &r : NULL);
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
