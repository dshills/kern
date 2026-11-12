// kern hosted runtime: allocation for runtime objects, container identity
// tags and the system allocator.
// Included by kern_rt.c; see kern_rt.h for the entries.

// ---- allocation failure injection ----

// kern_fail_next is armed by testing.fail_next_alloc: the next allocation
// (a runtime object's, or the system allocator's, which the kern containers
// use) fails.
static _Atomic int kern_fail_next = 0;
// kern_fail_armed takes the armed failure, if there is one. The flag is
// read before it is exchanged: an exchange is a write, and every thread
// that allocates would be writing the one cache line the flag is on (four
// threads allocating took nine times as long as one).
static inline int kern_fail_armed(void) {
    return atomic_load(&kern_fail_next) && atomic_exchange(&kern_fail_next, 0);
}
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
static _Noreturn void kern_trap_loc(int32_t kind, const kern_loc *at) {
    if (at == NULL) kern_trap(kind, 0, 0, NULL);
    kern_trap(kind, at->start, at->end, at->file);
}
// ---- the table of live blocks (specs/runtime RF1, RF2) ----
//
// A debug build records every block the system allocator hands out: its
// address, the size and alignment it was asked for, and the location of
// the call. free and realloc look the block up and trap explicit, at
// their call, when the address is not a live block or the size or
// alignment differs from the block's: a double free, a free of a pointer
// that was never allocated and a wrong size are found where they happen,
// not as a corrupted heap later. With KERN_ALLOC_REPORT=1 the runtime's
// own objects are recorded too (alignment 0 marks them) and four counters
// are kept; kern_alloc_report writes them when the program ends.
//
// The table is 64 open-addressed tables, each under its own lock, chosen
// by the address's hash: threads that allocate at the same time seldom
// meet. A release build has none of this: the checks are the caller's
// obligations there (spec §6).
#ifndef KERN_RELEASE
typedef struct { uintptr_t p; uint64_t size; const char *file; int64_t start; uint32_t align; } kern_block;
typedef struct { _Atomic(uint32_t) lock; kern_block *slots; size_t cap, used; } kern_stripe;
#define KERN_STRIPES 64
static kern_stripe kern_stripes[KERN_STRIPES];
static _Atomic uint64_t kern_alloc_total, kern_alloc_live, kern_alloc_live_bytes, kern_alloc_peak;
// kern_blocks_lost is set when a block that realloc had already moved could
// not be recorded for want of memory. Some live block is then missing from
// the table, so an address the table does not know is no longer proof of
// a bad free: from then on only a block that is found is checked.
static _Atomic int kern_blocks_lost;

// The stripe lock is the three-state parking lock of the runtime's Mutex
// (0 free, 1 held, 2 held with a thread parked), which needs no
// initialization and no destruction.
static void kern_stripe_lock(kern_stripe *s) {
    uint32_t free_ = 0;
    if (atomic_compare_exchange_strong_explicit(&s->lock, &free_, 1, memory_order_acquire, memory_order_relaxed)) return;
    while (atomic_exchange_explicit(&s->lock, 2, memory_order_acquire) != 0) kern_park(&s->lock, 2);
}
static void kern_stripe_unlock(kern_stripe *s) {
    if (atomic_exchange_explicit(&s->lock, 0, memory_order_release) == 2) kern_unpark_one(&s->lock);
}
static uint64_t kern_block_hash(uintptr_t p) {
    uint64_t h = (uint64_t)p * 0x9E3779B97F4A7C15ull;
    return h ^ (h >> 29);
}
// A block's home slot comes from the hash bits the stripe did not use.
static size_t kern_block_home(const kern_stripe *s, uintptr_t p) { return (size_t)(kern_block_hash(p) >> 6) & (s->cap - 1); }
static void kern_block_place(kern_stripe *s, const kern_block *b) {
    size_t i = kern_block_home(s, b->p);
    while (s->slots[i].p != 0) i = (i + 1) & (s->cap - 1);
    s->slots[i] = *b;
}
// kern_block_put records a block: 1, or 0 when the table could not grow.
static int kern_block_put(const kern_block *b) {
    kern_stripe *s = &kern_stripes[kern_block_hash(b->p) & (KERN_STRIPES - 1)];
    kern_stripe_lock(s);
    if ((s->used + 1) * 2 > s->cap) { // at most half full
        kern_stripe grown = { .cap = s->cap ? s->cap * 2 : 64 };
        grown.slots = calloc(grown.cap, sizeof *grown.slots);
        if (grown.slots == NULL) { kern_stripe_unlock(s); return 0; }
        for (size_t i = 0; i < s->cap; i++) if (s->slots[i].p != 0) kern_block_place(&grown, &s->slots[i]);
        free(s->slots);
        s->slots = grown.slots;
        s->cap = grown.cap;
    }
    kern_block_place(s, b);
    s->used++;
    kern_stripe_unlock(s);
    return 1;
}
// kern_block_take removes the block at p into *out: 1, or -1 when none is
// recorded there. With check set the block must have this size and
// alignment, else it stays and the result is 0.
static int kern_block_take(void *p, int check, uint64_t size, uint32_t align, kern_block *out) {
    kern_stripe *s = &kern_stripes[kern_block_hash((uintptr_t)p) & (KERN_STRIPES - 1)];
    int found = -1;
    kern_stripe_lock(s);
    if (s->cap != 0 && p != NULL) {
        size_t mask = s->cap - 1, i = kern_block_home(s, (uintptr_t)p);
        while (s->slots[i].p != 0 && s->slots[i].p != (uintptr_t)p) i = (i + 1) & mask;
        if (s->slots[i].p != 0) found = 0;
        if (s->slots[i].p != 0 && (!check || (s->slots[i].size == size && s->slots[i].align == align))) {
            found = 1;
            *out = s->slots[i];
            // close the gap: move back every later block of the run that
            // the gap now stands between and its home slot
            size_t j = i;
            for (;;) {
                j = (j + 1) & mask;
                if (s->slots[j].p == 0) break;
                size_t k = kern_block_home(s, s->slots[j].p);
                if (i <= j ? (i < k && k <= j) : (i < k || k <= j)) continue;
                s->slots[i] = s->slots[j];
                i = j;
            }
            s->slots[i].p = 0;
            s->used--;
        }
    }
    kern_stripe_unlock(s);
    return found;
}
// kern_count_grow and kern_count_shrink keep the report's counters; they
// are called only when the report is enabled.
static void kern_count_grow(uint64_t bytes) {
    uint64_t now = atomic_fetch_add(&kern_alloc_live_bytes, bytes) + bytes;
    uint64_t peak = atomic_load(&kern_alloc_peak);
    while (now > peak && !atomic_compare_exchange_weak(&kern_alloc_peak, &peak, now)) {}
}
static void kern_count_new(uint64_t bytes) {
    atomic_fetch_add(&kern_alloc_total, 1);
    atomic_fetch_add(&kern_alloc_live, 1);
    kern_count_grow(bytes);
}
static void kern_count_gone(uint64_t bytes) {
    atomic_fetch_sub(&kern_alloc_live, 1);
    atomic_fetch_sub(&kern_alloc_live_bytes, bytes);
}
// kern_block_checked takes the block that a free or a realloc names: 1
// with the block in *out, 0 when the table cannot judge the address
// (kern_blocks_lost), and a trap explicit at the call when the address is
// not a live block or the size or alignment is not the block's.
static int kern_block_checked(void *p, int64_t size, int64_t align, kern_block *out, const kern_loc *at) {
    int r = size < 0 || align <= 0 || align > 4096 ? 0 : kern_block_take(p, 1, (uint64_t)size, (uint32_t)align, out);
    if (r == 0 || (r < 0 && !atomic_load(&kern_blocks_lost))) kern_trap_loc(11, at);
    return r > 0;
}
// kern_block_new records a new block of the system allocator (align > 0)
// or a runtime object (align 0, only when the report is enabled): 1, or 0
// when the table could not grow.
static int kern_block_new(void *p, uint64_t size, uint32_t align, const kern_loc *at) {
    kern_block b = { (uintptr_t)p, size, at ? at->file : NULL, at ? at->start : 0, align };
    if (!kern_block_put(&b)) return 0;
    if (kern_cfg.alloc_report) kern_count_new(size);
    return 1;
}
#endif

// kern_alloc, kern_try_alloc and kern_obj_free allocate and free the
// blocks of the runtime's own objects (Shared, Mutex, Condvar, threads,
// scopes, join handles). at is the user-level call that made the object.
static void *kern_try_alloc(size_t n, const kern_loc *at) {
    if (kern_fail_armed()) return NULL;
    void *p = malloc(n ? n : 1);
#ifndef KERN_RELEASE
    if (p != NULL && kern_cfg.alloc_report && !kern_block_new(p, n, 0, at)) { free(p); return NULL; }
#else
    (void)at;
#endif
    return p;
}
// kern_alloc is kern_try_alloc that traps oom at the call instead of
// returning NULL; the armed failure of testing.fail_next_alloc makes
// either fail.
static void *kern_alloc(size_t n, const kern_loc *at) {
    void *p = kern_try_alloc(n, at);
    if (p == NULL) kern_trap_loc(9, at);
    return p;
}
static void kern_obj_free(void *p) {
#ifndef KERN_RELEASE
    kern_block b;
    if (kern_cfg.alloc_report && kern_block_take(p, 0, 0, 0, &b) > 0) kern_count_gone(b.size);
#endif
    free(p);
}
// kern_try_failed writes Err(OutOfMemory) into a try_new's result and
// destroys the value the caller moved in, which no object took.
static void kern_try_failed(void (*glue)(void *), void *src, char *out) {
    if (glue != NULL) glue(src);
    out[0] = 1;
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

// ---- SystemAllocator (spec §6) ----

struct kern_sysalloc { int unused; };
void *kern_sysalloc_new(void) { static kern_sysalloc a; return &a; }
void kern_sysalloc_alloc(kern_sysalloc *a, int64_t size, int64_t align, char *out, int64_t off, const kern_loc *at) {
    (void)a;
    if (size < 0 || align <= 0 || (align & (align - 1)) != 0 || align > 4096 || (size & (align - 1)) != 0) kern_trap_loc(11, at);
    if (kern_fail_armed()) { out[0] = 1; return; } // the armed failure: Err(OutOfMemory)
    // aligned_alloc needs an alignment of at least sizeof(void *) and a size
    // that is a multiple of it (macOS rejects smaller alignments): the
    // request is rounded up; the §6 obligations above are the caller's values
    size_t al = (size_t)align < sizeof(void *) ? sizeof(void *) : (size_t)align;
    if ((uint64_t)size > SIZE_MAX - (al - 1)) { out[0] = 1; return; } // unrepresentable after rounding
    size_t sz = ((size_t)size + al - 1) & ~(al - 1);
    // malloc's block is 16-byte aligned on every supported host (the
    // realloc and zeroed entries rely on the same), and costs less than
    // aligned_alloc, which is kept for the alignments above that
    void *p = al <= 16 ? malloc(sz ? sz : al) : aligned_alloc(al, sz ? sz : al);
    if (p == NULL) { out[0] = 1; return; }
#ifndef KERN_RELEASE
    if (!kern_block_new(p, (uint64_t)size, (uint32_t)align, at)) { free(p); out[0] = 1; return; }
#endif
    out[0] = 0;
    memcpy(out + off, &p, sizeof p);
}
// kern_sysalloc_realloc resizes an allocation in place when the allocator
// can (realloc, whose result is 16-byte aligned on every supported host),
// else by aligned_alloc, copy and free; on failure the old allocation stays.
void kern_sysalloc_realloc(kern_sysalloc *a, void *p, int64_t old_size, int64_t size, int64_t align, char *out, int64_t off, const kern_loc *at) {
    (void)a;
    if (old_size < 0 || size < 0 || align <= 0 || (align & (align - 1)) != 0 || align > 4096 || (size & (align - 1)) != 0) kern_trap_loc(11, at);
#ifndef KERN_RELEASE
    // The block leaves the table before the allocator may free it, so that
    // another thread given the same address finds its slot free; a call
    // that fails puts it back.
    kern_block was;
    int known = kern_block_checked(p, old_size, align, &was, at);
#define KERN_REALLOC_FAILED { if (known && !kern_block_put(&was)) atomic_store(&kern_blocks_lost, 1); out[0] = 1; return; }
#else
#define KERN_REALLOC_FAILED { out[0] = 1; return; }
#endif
    if (kern_fail_armed()) KERN_REALLOC_FAILED
    size_t al = (size_t)align < sizeof(void *) ? sizeof(void *) : (size_t)align;
    if ((uint64_t)size > SIZE_MAX - (al - 1)) KERN_REALLOC_FAILED
    size_t sz = ((size_t)size + al - 1) & ~(al - 1);
    if (sz == 0) sz = al;
    void *np;
    if (al <= 16) np = realloc(p, sz);
    else {
        np = aligned_alloc(al, sz);
        if (np) { kern_copy(np, p, (size_t)(old_size < size ? old_size : size)); free(p); }
    }
    if (np == NULL) KERN_REALLOC_FAILED
#undef KERN_REALLOC_FAILED
#ifndef KERN_RELEASE
    if (known) {
        // the block keeps the call site that allocated it (RF2)
        kern_block now = { (uintptr_t)np, (uint64_t)size, was.file, was.start, was.align };
        if (!kern_block_put(&now)) atomic_store(&kern_blocks_lost, 1); // the old block is gone, so there is no Err to give
        if (kern_cfg.alloc_report) {
            if (np != p) atomic_fetch_add(&kern_alloc_total, 1);
            atomic_fetch_sub(&kern_alloc_live_bytes, was.size);
            kern_count_grow((uint64_t)size);
        }
    }
#endif
    out[0] = 0;
    memcpy(out + off, &np, sizeof np);
}
// kern_sysalloc_alloc_zeroed is kern_sysalloc_alloc with zeroed bytes:
// calloc (16-byte aligned on every supported host, and a large block comes
// from fresh zero pages without a memset), else aligned_alloc and memset.
void kern_sysalloc_alloc_zeroed(kern_sysalloc *a, int64_t size, int64_t align, char *out, int64_t off, const kern_loc *at) {
    (void)a;
    if (size < 0 || align <= 0 || (align & (align - 1)) != 0 || align > 4096 || (size & (align - 1)) != 0) kern_trap_loc(11, at);
    if (kern_fail_armed()) { out[0] = 1; return; }
    size_t al = (size_t)align < sizeof(void *) ? sizeof(void *) : (size_t)align;
    if ((uint64_t)size > SIZE_MAX - (al - 1)) { out[0] = 1; return; }
    size_t sz = ((size_t)size + al - 1) & ~(al - 1);
    if (sz == 0) sz = al;
    void *p;
    if (al <= 16) p = calloc(1, sz);
    else {
        p = aligned_alloc(al, sz);
        if (p) memset(p, 0, sz);
    }
    if (p == NULL) { out[0] = 1; return; }
#ifndef KERN_RELEASE
    if (!kern_block_new(p, (uint64_t)size, (uint32_t)align, at)) { free(p); out[0] = 1; return; }
#endif
    out[0] = 0;
    memcpy(out + off, &p, sizeof p);
}
void kern_sysalloc_free(kern_sysalloc *a, void *p, int64_t size, int64_t align, const kern_loc *at) {
    (void)a;
#ifndef KERN_RELEASE
    kern_block b;
    if (kern_block_checked(p, size, align, &b, at) && kern_cfg.alloc_report) kern_count_gone(b.size);
#else
    (void)size; (void)align; (void)at;
#endif
    free(p);
}

// ---- the allocation report (specs/runtime RF2) ----

// kern_alloc_report writes the report's line to standard error when
// KERN_ALLOC_REPORT=1: the counters, and the up to 16 call sites holding
// the most live bytes. It runs when the program returns from main or
// calls exit; a release build writes nothing.
static void kern_alloc_report(void) {
#ifndef KERN_RELEASE
    if (!kern_cfg.alloc_report) return;
    static atomic_flag once = ATOMIC_FLAG_INIT;
    if (atomic_flag_test_and_set(&once)) return; // exit from two threads
    typedef struct { const char *file; uint64_t line, count, bytes; } kern_site;
    // one site per live block at most, in a table at most half full; when
    // there is no memory for it the report has no sites
    size_t live = 0;
    for (int i = 0; i < KERN_STRIPES; i++) { kern_stripe_lock(&kern_stripes[i]); live += kern_stripes[i].used; kern_stripe_unlock(&kern_stripes[i]); }
    size_t cap = 64;
    while (cap < live * 2 + 64) cap *= 2; // room for blocks made while the table is read
    kern_site *sites = calloc(cap, sizeof *sites);
    for (int i = 0; sites != NULL && i < KERN_STRIPES; i++) {
        kern_stripe *s = &kern_stripes[i];
        kern_stripe_lock(s);
        for (size_t k = 0; k < s->cap; k++) {
            const kern_block *b = &s->slots[k];
            if (b->p == 0) continue;
            const char *file = b->file ? b->file : kern_file;
            uint64_t line, col;
            kern_line_col(file, b->start, b->start ? b->start + 1 : 0, &line, &col);
            uint64_t h = line * 0x9E3779B97F4A7C15ull;
            for (const char *c = file; *c; c++) h = (h ^ (uint8_t)*c) * 0x100000001B3ull;
            size_t at_ = (size_t)(h >> 20) & (cap - 1), probes = 0;
            while (sites[at_].file != NULL && !(sites[at_].line == line && strcmp(sites[at_].file, file) == 0) && probes < cap) { at_ = (at_ + 1) & (cap - 1); probes++; }
            if (probes == cap) continue; // full: more blocks appeared than there was room for
            sites[at_].file = file;
            sites[at_].line = line;
            sites[at_].count++;
            sites[at_].bytes += b->size;
        }
        kern_stripe_unlock(s);
    }
    static char b[16384];
    size_t n = 0;
    n = kern_cat(b, n, "{\"alloc\":{\"total\":");
    n += kern_fmt_u64(b + n, atomic_load(&kern_alloc_total));
    n = kern_cat(b, n, ",\"live\":");
    n += kern_fmt_u64(b + n, atomic_load(&kern_alloc_live));
    n = kern_cat(b, n, ",\"live_bytes\":");
    n += kern_fmt_u64(b + n, atomic_load(&kern_alloc_live_bytes));
    n = kern_cat(b, n, ",\"peak_bytes\":");
    n += kern_fmt_u64(b + n, atomic_load(&kern_alloc_peak));
    n = kern_cat(b, n, ",\"sites\":[");
    for (int rank = 0; sites != NULL && rank < 16; rank++) {
        // the largest site left: most bytes, then the file, then the line
        kern_site *top = NULL;
        for (size_t k = 0; k < cap; k++) {
            kern_site *c = &sites[k];
            if (c->file == NULL || c->count == 0) continue;
            int order = top == NULL ? -1 : c->bytes != top->bytes ? (c->bytes > top->bytes ? -1 : 1) : strcmp(c->file, top->file);
            if (order == 0) order = c->line < top->line ? -1 : 1;
            if (order < 0) top = c;
        }
        if (top == NULL) break;
        if (rank > 0) b[n++] = ',';
        n = kern_cat(b, n, "{\"file\":\"");
        n = kern_cat_json(b, n, n + 800, top->file);
        n = kern_cat(b, n, "\",\"line\":");
        n += kern_fmt_u64(b + n, top->line);
        n = kern_cat(b, n, ",\"count\":");
        n += kern_fmt_u64(b + n, top->count);
        n = kern_cat(b, n, ",\"bytes\":");
        n += kern_fmt_u64(b + n, top->bytes);
        b[n++] = '}';
        top->count = 0; // listed
    }
    n = kern_cat(b, n, "]}}\n");
    (void)kern_write_all(2, b, n);
    free(sites);
#endif
}
