/* lock_free_queue: a bounded single-producer single-consumer ring of 4096
 * u64 slots. head (consumer) and tail (producer) are monotonically
 * increasing u64 atomics on separate cache lines; each side keeps its own
 * index in a private counter (it is the sole writer) plus a cached copy of
 * the other side's index, and publishes its own index with a release store
 * every B = 64 elements and before waiting (B-Queue style batching: without
 * it the two threads fall into a lock-step regime where every release store
 * stalls on the line the other thread is polling, 20-40x slower on Apple
 * silicon). push: if tail - head_cache == 4096, re-read head with acquire;
 * still full -> return false; write slots[tail & 4095]; tail++; publish when
 * tail % 64 == 0. pop mirrors it with head/tail swapped, re-reading tail with
 * acquire only when the cached copy says empty. The main thread is the
 * producer: element i is the LCG's i-th value; N=8,000,000 elements, each
 * pushed until push succeeds (publish, spin, retry). The consumer thread
 * pops every element (publish, spin, retry while empty) and chains
 * cs = (cs XOR v) * 0x100000001b3 (wrapping) from 0. ops = N; the sampled
 * operation is the consumer's i-th pop (including any wait). Timed: spawn
 * through join; threads = 2. checksum = cs + number of popped elements.
 * KERN_BENCH_FAIL=1: the consumer is held at a gate while the producer
 * attempts elements 0..5119 once each; the 1024 pushes on the full ring
 * return false (failures = 1024; those elements are dropped), then tail is
 * published, the gate opens and elements 5120..N-1 follow as in the normal
 * run; the consumer pops N - 1024 elements. */
#include "../bench.h"
#include <pthread.h>
#include <stdatomic.h>

#define N 8000000ull
#define CAP 4096
#define B 64
#define FNV 0x100000001b3ull
#define FAIL_PREFIX 5120

typedef struct {
    uint64_t slots[CAP];
    _Alignas(128) _Atomic uint64_t head;
    _Alignas(128) _Atomic uint64_t tail;
    _Alignas(128) _Atomic int go;
    uint64_t expected;
} spsc;

/* side is one thread's private index and its cached copy of the other
 * thread's published index. */
typedef struct {
    uint64_t pos, cache;
} side;

/* spin matches Rust's std::hint::spin_loop: pause on x86-64, isb on AArch64. */
static inline void spin(void) {
#if defined(__x86_64__)
    __builtin_ia32_pause();
#elif defined(__aarch64__)
    __asm__ volatile("isb sy" ::: "memory");
#endif
}

/* spsc_push returns 0 when the ring is full. */
static inline int spsc_push(spsc *q, side *p, uint64_t v) {
    if (p->pos - p->cache == CAP) {
        p->cache = atomic_load_explicit(&q->head, memory_order_acquire);
        if (p->pos - p->cache == CAP) return 0;
    }
    q->slots[p->pos & (CAP - 1)] = v;
    p->pos++;
    if ((p->pos & (B - 1)) == 0) atomic_store_explicit(&q->tail, p->pos, memory_order_release);
    return 1;
}

static inline void spsc_publish_tail(spsc *q, const side *p) {
    atomic_store_explicit(&q->tail, p->pos, memory_order_release);
}

/* spsc_pop returns 0 when the ring is empty. */
static inline int spsc_pop(spsc *q, side *c, uint64_t *out) {
    if (c->pos == c->cache) {
        c->cache = atomic_load_explicit(&q->tail, memory_order_acquire);
        if (c->pos == c->cache) return 0;
    }
    *out = q->slots[c->pos & (CAP - 1)];
    c->pos++;
    if ((c->pos & (B - 1)) == 0) atomic_store_explicit(&q->head, c->pos, memory_order_release);
    return 1;
}

static inline void spsc_publish_head(spsc *q, const side *c) {
    atomic_store_explicit(&q->head, c->pos, memory_order_release);
}

typedef struct {
    spsc *q;
    uint64_t cs, popped;
    sampler s;
} consumer_arg;

static void *consumer(void *arg) {
    consumer_arg *ca = arg;
    spsc *q = ca->q;
    while (!atomic_load_explicit(&q->go, memory_order_acquire)) spin();
    uint64_t cs = 0, expected = q->expected;
    side c = {0, 0};
    for (uint64_t i = 0; i < expected; i++) {
        uint64_t v;
        int w = sampler_want(&ca->s, i);
        if (w) sampler_begin(&ca->s);
        while (!spsc_pop(q, &c, &v)) {
            spsc_publish_head(q, &c);
            spin();
        }
        if (w) sampler_end(&ca->s);
        cs = (cs ^ v) * FNV;
    }
    spsc_publish_head(q, &c);
    ca->cs = cs;
    ca->popped = expected;
    return NULL;
}

int main(void) {
    int fail_mode = bench_fail_mode();
    static spsc q;
    atomic_init(&q.head, 0);
    atomic_init(&q.tail, 0);
    atomic_init(&q.go, 0);
    static consumer_arg ca;
    ca.q = &q;
    sampler_init(&ca.s, N);
    uint64_t failures = 0, x = LCG_SEED, first = 0;
    side p = {0, 0};
    pthread_t tid;

    uint64_t start = now_ns();
    if (pthread_create(&tid, NULL, consumer, &ca) != 0) abort();
    if (fail_mode) {
        for (uint64_t i = 0; i < FAIL_PREFIX; i++)
            if (!spsc_push(&q, &p, lcg_next(&x))) failures++;
        spsc_publish_tail(&q, &p);
        first = FAIL_PREFIX;
    }
    q.expected = N - failures;
    atomic_store_explicit(&q.go, 1, memory_order_release);
    for (uint64_t i = first; i < N; i++) {
        uint64_t v = lcg_next(&x);
        while (!spsc_push(&q, &p, v)) {
            spsc_publish_tail(&q, &p);
            spin();
        }
    }
    spsc_publish_tail(&q, &p);
    pthread_join(tid, NULL);
    uint64_t ns = now_ns() - start;

    if (ca.popped != N - failures || (fail_mode && failures != FAIL_PREFIX - CAP)) abort();
    uint64_t checksum = ca.cs + ca.popped;
    if (fail_mode)
        bench_report_failures(N, ns, &ca.s, checksum, failures);
    else
        bench_report(N, ns, &ca.s, checksum);
    return 0;
}
