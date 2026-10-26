// kern hosted runtime (PLAN.md 2.2): traps, output, the standard library's
// containers as opaque objects, threads, the system allocator and the
// test hooks. Linked by `kern build` with every program.
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <signal.h>
#include <pthread.h>
#include <stdatomic.h>
#include <unistd.h>
#include <time.h>

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
void println(void) { kern_out("\n", 1); }
#ifdef __APPLE__
#define KERN_CLOCK CLOCK_MONOTONIC_RAW // CLOCK_MONOTONIC has microsecond resolution on macOS
#else
#define KERN_CLOCK CLOCK_MONOTONIC
#endif
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
static void kern_trap_at(int32_t kind, int64_t start, int64_t end, int in_signal) {
    if (kern_in_trap) kern_die(); // a trap inside the handler terminates
    kern_in_trap = 1;
    if (!in_signal) kern_flush();
    kern_info.kind = (uint8_t)kind;
    kern_info.file = kern_file;
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
        for (const char *c = kern_file; *c && n < 400; c++) {
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

void kern_trap(int32_t kind, int64_t start, int64_t end) { kern_trap_at(kind, start, end, 0); }

// the fault handler classifies the fault as `stack` iff the address lies in
// the thread's guard range; any other fault in safe code is `unreachable`
static void kern_segv(int sig, siginfo_t *si, void *ctx) {
    (void)sig; (void)ctx;
    uintptr_t addr = (uintptr_t)si->si_addr;
    if (kern_stack_lo != 0 && addr < kern_stack_lo + 4096 && addr + (1 << 20) >= kern_stack_lo) kern_trap_at(10, 0, 0, 1);
    kern_trap_at(12, 0, 0, 1);
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

static _Atomic int kern_fail_next = 0;
#ifdef KERN_TEST_HOOKS
void kern_test_fail_next_alloc(void) { atomic_store(&kern_fail_next, 1); }
#endif
static void *kern_alloc(size_t n, int64_t start, int64_t end) {
    if (atomic_exchange(&kern_fail_next, 0)) kern_trap(9, start, end);
    void *p = malloc(n ? n : 1);
    if (p == NULL) kern_trap(9, start, end);
    return p;
}

// ---- Box ----

typedef struct { int64_t size; char data[]; } kern_box;
void *kern_box_new(int64_t size, const void *src, int64_t start, int64_t end) {
    kern_box *b = kern_alloc(sizeof *b + (size_t)size, start, end);
    b->size = size;
    memcpy(b->data, src, (size_t)size);
    return b;
}
void *kern_box_get(kern_box *b) { return b->data; }
void kern_box_into(kern_box *b, void *dst, int64_t size) { memcpy(dst, b->data, (size_t)size); free(b); }
void kern_box_drop(kern_box *b, kern_glue g) { if (b == NULL) return; if (g) g(b->data); free(b); }
void *kern_box_clone(kern_box *b) { return kern_box_new(b->size, b->data, 0, 0); }

// ---- Vec ----

typedef struct { char *data; int64_t len, cap, esize, align; } kern_vec;
// kern_bytes computes count * esize, trapping oom when it is unrepresentable
static size_t kern_bytes(int64_t count, int64_t esize, int64_t start, int64_t end) {
    if (count < 0 || esize < 0) kern_trap(9, start, end);
    if (esize == 0) return (size_t)count;
    if ((uint64_t)count > SIZE_MAX / (uint64_t)esize) kern_trap(9, start, end);
    return (size_t)count * (size_t)esize;
}
void *kern_vec_new(int64_t esize, int64_t align, int64_t cap) {
    kern_vec *v = kern_alloc(sizeof *v, 0, 0);
    v->data = NULL; v->len = 0; v->cap = 0; v->esize = esize; v->align = align;
    if (cap > 0) { v->data = kern_alloc(kern_bytes(cap, esize, 0, 0), 0, 0); v->cap = cap; }
    return v;
}
int64_t kern_vec_len(kern_vec *v) { return v->len; }
void *kern_vec_data(kern_vec *v) { return v->data; }
void kern_vec_push(kern_vec *v, const void *src, int64_t start, int64_t end) {
    if (atomic_exchange(&kern_fail_next, 0)) kern_trap(9, start, end);
    if (v->len == v->cap) {
        if (v->cap > INT64_MAX / 2) kern_trap(9, start, end);
        int64_t ncap = v->cap ? v->cap * 2 : 4;
        char *nd = realloc(v->data, kern_bytes(ncap, v->esize ? v->esize : 1, start, end));
        if (nd == NULL) kern_trap(9, start, end);
        v->data = nd; v->cap = ncap;
    }
    memcpy(v->data + v->len * v->esize, src, (size_t)v->esize);
    v->len++;
}
void kern_vec_pop(kern_vec *v, char *out, int64_t off) {
    if (v->len == 0) { out[0] = 1; return; }
    out[0] = 0;
    v->len--;
    memcpy(out + off, v->data + v->len * v->esize, (size_t)v->esize);
}
void *kern_vec_at(kern_vec *v, int64_t i, int64_t start, int64_t end) {
    if ((uint64_t)i >= (uint64_t)v->len) kern_trap(0, start, end);
    return v->data + i * v->esize;
}
void kern_vec_clear(kern_vec *v, kern_glue g) {
    if (g) for (int64_t i = v->len - 1; i >= 0; i--) g(v->data + i * v->esize);
    v->len = 0;
}
void kern_vec_drop(kern_vec *v, kern_glue g) { if (v == NULL) return; kern_vec_clear(v, g); free(v->data); free(v); }
void *kern_vec_clone(kern_vec *v) {
    kern_vec *n = kern_vec_new(v->esize, v->align, v->len);
    memcpy(n->data, v->data, (size_t)(v->len * v->esize));
    n->len = v->len;
    return n;
}

// ---- Pool (spec §5.6) ----

typedef struct { uint32_t gen; uint8_t live, retired; } kern_slot;
typedef struct { char *data; kern_slot *slots; int64_t count, cap, esize, align; int64_t *free_list; int64_t nfree; uint32_t identity; } kern_pool;
typedef struct { uint32_t index, gen, tag; } kern_poolid;
static _Atomic uint32_t kern_identity = 1;

// identity tags are a debug-build feature (PLAN.md 2.2): a release build
// issues identity 0, which disables the check
static uint32_t kern_next_identity(void) {
#ifdef KERN_RELEASE
    return 0;
#else
    uint32_t id = atomic_fetch_add(&kern_identity, 1);
    return id; // 0 after wrap-around: unchecked
#endif
}

void *kern_pool_new(int64_t esize, int64_t align) {
    kern_pool *p = kern_alloc(sizeof *p, 0, 0);
    memset(p, 0, sizeof *p);
    p->esize = esize; p->align = align; p->identity = kern_next_identity();
    return p;
}
void kern_pool_alloc(kern_pool *p, const void *src, char *out, int64_t off) {
    int64_t idx;
    if (p->nfree > 0) idx = p->free_list[--p->nfree];
    else {
        if (p->count == p->cap) {
            // grow transactionally: commit only when every buffer was obtained
            if (p->cap > INT64_MAX / 2 || (p->esize > 0 && (uint64_t)(p->cap ? p->cap * 2 : 8) > SIZE_MAX / (uint64_t)p->esize)) { out[0] = 1; return; }
            int64_t ncap = p->cap ? p->cap * 2 : 8;
            char *nd = malloc((size_t)ncap * (size_t)(p->esize ? p->esize : 1));
            kern_slot *ns = malloc((size_t)ncap * sizeof *p->slots);
            int64_t *nf = malloc((size_t)ncap * sizeof *p->free_list);
            if (!nd || !ns || !nf) { free(nd); free(ns); free(nf); out[0] = 1; return; }
            if (p->count > 0) {
                memcpy(nd, p->data, (size_t)p->count * (size_t)p->esize);
                memcpy(ns, p->slots, (size_t)p->count * sizeof *p->slots);
            }
            if (p->nfree > 0) memcpy(nf, p->free_list, (size_t)p->nfree * sizeof *p->free_list);
            memset(ns + p->cap, 0, (size_t)(ncap - p->cap) * sizeof *p->slots);
            free(p->data); free(p->slots); free(p->free_list);
            p->data = nd; p->slots = ns; p->free_list = nf; p->cap = ncap;
        }
        idx = p->count++;
    }
    kern_slot *s = &p->slots[idx];
    memcpy(p->data + idx * p->esize, src, (size_t)p->esize);
    s->live = 1;
    out[0] = 0;
    kern_poolid id = { (uint32_t)idx, s->gen, p->identity };
    memcpy(out + off, &id, sizeof id);
}
static kern_slot *kern_pool_check(kern_pool *p, const kern_poolid *id, int64_t start, int64_t end) {
    if (id->index >= (uint64_t)p->count) kern_trap(0, start, end);
    if (id->tag != 0 && p->identity != 0 && id->tag != p->identity) kern_trap(7, start, end);
    kern_slot *s = &p->slots[id->index];
    if (!s->live) kern_trap(6, start, end);
    if (s->gen != id->gen) kern_trap(5, start, end);
    return s;
}
void *kern_pool_at(kern_pool *p, const kern_poolid *id, int64_t start, int64_t end) {
    kern_pool_check(p, id, start, end);
    return p->data + (int64_t)id->index * p->esize;
}
void *kern_pool_get2(kern_pool *p, const kern_poolid *a, const kern_poolid *b, int64_t start, int64_t end) {
    kern_pool_check(p, a, start, end);
    kern_pool_check(p, b, start, end);
    if (a->index == b->index) kern_trap(8, start, end);
    return p->data + (int64_t)a->index * p->esize;
}
void kern_pool_free(kern_pool *p, const kern_poolid *id, kern_glue g, int64_t start, int64_t end) {
    kern_slot *s = kern_pool_check(p, id, start, end);
    s->live = 0;
    if (g) g(p->data + (int64_t)id->index * p->esize);
    if (s->gen == UINT32_MAX) s->retired = 1;
    else { s->gen++; p->free_list[p->nfree++] = id->index; }
}
int64_t kern_pool_len(kern_pool *p) { int64_t n = 0; for (int64_t i = 0; i < p->count; i++) n += p->slots[i].live; return n; }
void kern_pool_drop(kern_pool *p, kern_glue g) {
    if (p == NULL) return;
    if (g) for (int64_t i = p->count - 1; i >= 0; i--) if (p->slots[i].live) g(p->data + i * p->esize);
    free(p->data); free(p->slots); free(p->free_list); free(p);
}
#ifdef KERN_TEST_HOOKS
void kern_test_pool_seed_generation(kern_pool *p, int64_t slot, int64_t gen) {
    // allocate slots until the requested one exists, keeping them live so
    // the count advances, then free them in reverse order
    kern_poolid *ids = NULL; int64_t n = 0;
    char *tmp = calloc(1, p->esize > 0 ? (size_t)p->esize : 1);
    if (!tmp) kern_trap(9, 0, 0);
    while (p->count <= slot) {
        char out[16];
        kern_pool_alloc(p, tmp, out, 4);
        if (out[0] != 0) kern_trap(9, 0, 0);
        kern_poolid *nids = realloc(ids, (size_t)(n + 1) * sizeof *ids);
        if (!nids) kern_trap(9, 0, 0);
        ids = nids;
        memcpy(&ids[n++], out + 4, sizeof(kern_poolid));
    }
    for (int64_t i = n - 1; i >= 0; i--) kern_pool_free(p, &ids[i], NULL, 0, 0);
    free(ids); free(tmp);
    p->slots[slot].gen = (uint32_t)gen;
}
#endif

// ---- Arena ----

typedef struct { char *data; int64_t len, cap, esize, align; uint32_t identity; } kern_arena;
typedef struct { uint32_t index, tag; } kern_arenaid;
void *kern_arena_new(int64_t esize, int64_t align) {
    kern_arena *a = kern_alloc(sizeof *a, 0, 0);
    memset(a, 0, sizeof *a);
    a->esize = esize; a->align = align; a->identity = kern_next_identity();
    return a;
}
void kern_arena_alloc(kern_arena *a, const void *src, char *out, int64_t off) {
    if (a->len == a->cap) {
        int64_t ncap = a->cap ? a->cap * 2 : 8;
        char *nd = realloc(a->data, (size_t)(ncap * (a->esize ? a->esize : 1)));
        if (!nd) { out[0] = 1; return; }
        a->data = nd; a->cap = ncap;
    }
    memcpy(a->data + a->len * a->esize, src, (size_t)a->esize);
    kern_arenaid id = { (uint32_t)a->len, a->identity };
    a->len++;
    out[0] = 0;
    memcpy(out + off, &id, sizeof id);
}
void *kern_arena_at(kern_arena *a, const kern_arenaid *id, int64_t start, int64_t end) {
    if (id->index >= (uint64_t)a->len) kern_trap(0, start, end);
    if (id->tag != 0 && a->identity != 0 && id->tag != a->identity) kern_trap(7, start, end);
    return a->data + (int64_t)id->index * a->esize;
}
int64_t kern_arena_len(kern_arena *a) { return a->len; }
void kern_arena_drop(kern_arena *a, kern_glue g) {
    if (a == NULL) return;
    if (g) for (int64_t i = a->len - 1; i >= 0; i--) g(a->data + i * a->esize);
    free(a->data); free(a);
}

// ---- Shared ----

typedef struct { _Atomic uint64_t count; int64_t size; char data[]; } kern_shared;
void *kern_shared_new(int64_t size, const void *src) {
    kern_shared *s = kern_alloc(sizeof *s + (size_t)size, 0, 0);
    atomic_store(&s->count, 1); s->size = size;
    memcpy(s->data, src, (size_t)size);
    return s;
}
void *kern_shared_get(kern_shared *s) { return s->data; }
void *kern_shared_clone(kern_shared *s) {
    uint64_t c = atomic_load(&s->count);
    for (;;) {
        if (c == UINT64_MAX) kern_trap(1, 0, 0);
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

typedef struct { pthread_mutex_t m; int64_t size; char data[]; } kern_mutex;
void *kern_mutex_new(int64_t size, const void *src) {
    kern_mutex *mx = kern_alloc(sizeof *mx + (size_t)size, 0, 0);
    pthread_mutexattr_t at;
    pthread_mutexattr_init(&at);
    pthread_mutexattr_settype(&at, PTHREAD_MUTEX_ERRORCHECK);
    pthread_mutex_init(&mx->m, &at);
    pthread_mutexattr_destroy(&at);
    mx->size = size;
    memcpy(mx->data, src, (size_t)size);
    return mx;
}
void *kern_mutex_lock(kern_mutex *mx, int64_t start, int64_t end) {
    int rc = pthread_mutex_lock(&mx->m);
    if (rc != 0) kern_trap(11, start, end); // EDEADLK: recursive acquisition (spec §5.5), or a broken mutex
    return mx->data;
}
void kern_mutex_unlock(kern_mutex *mx) { pthread_mutex_unlock(&mx->m); }
void kern_mutex_drop(kern_mutex *mx, kern_glue g) { if (mx == NULL) return; if (g) g(mx->data); pthread_mutex_destroy(&mx->m); free(mx); }

// ---- Atomic ----

typedef struct { int64_t size; uint64_t v; } kern_atomic;
static int kern_order(uint8_t o) {
    switch (o) { case 0: return __ATOMIC_RELAXED; case 1: return __ATOMIC_ACQUIRE; case 2: return __ATOMIC_RELEASE; case 3: return __ATOMIC_ACQ_REL; default: return __ATOMIC_SEQ_CST; }
}
static uint64_t kern_mask(int64_t size, uint64_t v) { return size >= 8 ? v : (v & ((1ULL << (size * 8)) - 1)); }
void *kern_atomic_new(int64_t size, const void *src) {
    kern_atomic *a = kern_alloc(sizeof *a, 0, 0);
    a->size = size; uint64_t v = 0; memcpy(&v, src, (size_t)size); __atomic_store_n(&a->v, v, __ATOMIC_SEQ_CST);
    return a;
}
int64_t kern_atomic_load(kern_atomic *a, int64_t size, uint8_t o) { (void)size; return (int64_t)__atomic_load_n(&a->v, kern_order(o)); }
void kern_atomic_store(kern_atomic *a, int64_t size, int64_t v, uint8_t o) { __atomic_store_n(&a->v, kern_mask(size, (uint64_t)v), kern_order(o)); }
int64_t kern_atomic_swap(kern_atomic *a, int64_t size, int64_t v, uint8_t o) { return (int64_t)__atomic_exchange_n(&a->v, kern_mask(size, (uint64_t)v), kern_order(o)); }
int64_t kern_atomic_rmw(kern_atomic *a, int64_t size, int64_t op, int64_t v, uint8_t o) {
    uint64_t x = kern_mask(size, (uint64_t)v);
    int m = kern_order(o);
    if (size >= 8) {
        switch (op) {
            case 0: return (int64_t)__atomic_fetch_add(&a->v, x, m);
            case 1: return (int64_t)__atomic_fetch_sub(&a->v, x, m);
            case 2: return (int64_t)__atomic_fetch_and(&a->v, x, m);
            case 3: return (int64_t)__atomic_fetch_or(&a->v, x, m);
            default: return (int64_t)__atomic_fetch_xor(&a->v, x, m);
        }
    }
    // a narrow read-modify-write is one atomic update: compute the masked
    // result and publish it with compare-exchange
    uint64_t old = __atomic_load_n(&a->v, __ATOMIC_RELAXED);
    for (;;) {
        uint64_t nw;
        switch (op) {
            case 0: nw = old + x; break;
            case 1: nw = old - x; break;
            case 2: nw = old & x; break;
            case 3: nw = old | x; break;
            default: nw = old ^ x; break;
        }
        nw = kern_mask(size, nw);
        if (__atomic_compare_exchange_n(&a->v, &old, nw, 1, m, __ATOMIC_RELAXED)) return (int64_t)old;
    }
}
void kern_atomic_cmpxchg(kern_atomic *a, int64_t size, int64_t expected, int64_t desired, uint8_t so, uint8_t fo, char *out, int64_t off) {
    uint64_t e = kern_mask(size, (uint64_t)expected);
    int ok = __atomic_compare_exchange_n(&a->v, &e, kern_mask(size, (uint64_t)desired), 0, kern_order(so), kern_order(fo));
    out[0] = ok ? 0 : 1;
    memcpy(out + off, &e, (size_t)size);
}
void kern_fence(uint8_t o) { __atomic_thread_fence(kern_order(o)); }

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
static kern_thread *kern_spawn_common(void (*thunk)(void *), const void *pack, int64_t size, int64_t roff, int *ok) {
    kern_thread *t = kern_alloc(sizeof *t, 0, 0);
    t->block = kern_alloc((size_t)size, 0, 0);
    memcpy(t->block, pack, (size_t)size);
    t->roff = roff; t->joined = 0; t->started = 0;
    kern_spawn_arg *s = kern_alloc(sizeof *s, 0, 0);
    s->thunk = thunk; s->block = t->block;
    if (pthread_create(&t->t, NULL, kern_thread_run, s) != 0) { free(s); free(t->block); free(t); *ok = 0; return NULL; }
    t->started = 1;
    *ok = 1;
    return t;
}
void kern_thread_spawn(void (*thunk)(void *), const void *pack, int64_t size, int64_t roff, char *out, int64_t off) {
    int ok;
    kern_thread *t = kern_spawn_common(thunk, pack, size, roff, &ok);
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
void *kern_scope_new(void) { kern_scope *s = kern_alloc(sizeof *s, 0, 0); memset(s, 0, sizeof *s); return s; }
void kern_scope_spawn(kern_scope *sc, void (*thunk)(void *), const void *pack, int64_t size, int64_t roff, char *out, int64_t off) {
    // reserve the join slot before the worker starts, so a failed
    // reservation is an ordinary spawn failure
    if (sc->n == sc->cap) {
        int64_t ncap = sc->cap ? sc->cap * 2 : 4;
        kern_thread **nj = realloc(sc->joins, (size_t)ncap * sizeof *sc->joins);
        if (!nj) { out[0] = 1; return; }
        sc->joins = nj; sc->cap = ncap;
    }
    int ok;
    kern_thread *t = kern_spawn_common(thunk, pack, size, roff, &ok);
    if (!ok) { out[0] = 1; return; }
    sc->joins[sc->n++] = t;
    kern_join *j = kern_alloc(sizeof *j, 0, 0);
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
void kern_sysalloc_alloc(kern_sysalloc *a, int64_t size, int64_t align, char *out, int64_t off) {
    (void)a;
    if (align <= 0 || (align & (align - 1)) != 0 || align > 4096 || size % align != 0) kern_trap(11, 0, 0);
    void *p = aligned_alloc((size_t)align, size ? (size_t)size : (size_t)align);
    if (p == NULL) { out[0] = 1; return; }
    out[0] = 0;
    memcpy(out + off, &p, sizeof p);
}
void kern_sysalloc_free(kern_sysalloc *a, void *p, int64_t size, int64_t align) { (void)a; (void)size; (void)align; free(p); }
