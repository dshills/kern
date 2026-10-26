/* lock_protected_map: the hash_table map (open addressing, linear probing,
 * Fibonacci hash, states/keys/vals, initial capacity 16, grow at 3/4) behind
 * one pthread mutex, shared by 4 threads. Untimed setup inserts keys 0..K-1
 * (K = 262,144) with value key * 0x9E3779B97F4A7C15 (wrapping). Thread t
 * (0..3) runs its own LCG with seed 42 + t for 1,000,000 ops: x = next();
 * x >> 62 == 0 (25%): insert key K + (x >> 46) with the same value rule,
 * cs += key; otherwise look up key x >> 46 (always present; abort if not),
 * cs += value XOR key. Each op holds the lock once. ops = 4,000,000; the
 * sampled operation is thread 0's i-th op (lock, op, unlock). Timed:
 * spawning through joining; threads = 4.
 * checksum = wrapping sum of the four per-thread cs, plus the final count. */
#include "../bench.h"
#include <pthread.h>

#define N 4000000ull
#define THREADS 4
#define PER_THREAD (N / THREADS)
#define K 262144ull
#define GOLD 0x9E3779B97F4A7C15ull
#define EMPTY 0
#define FULL 2

typedef struct {
    uint8_t *states;
    uint64_t *keys, *vals;
    size_t cap, count;
    unsigned bits;
} map;

static void map_init(map *m, unsigned bits) {
    m->bits = bits;
    m->cap = (size_t)1 << bits;
    m->count = 0;
    m->states = xmalloc(m->cap);
    memset(m->states, EMPTY, m->cap);
    m->keys = xmalloc(m->cap * sizeof(uint64_t));
    m->vals = xmalloc(m->cap * sizeof(uint64_t));
}

static inline size_t probe(const map *m, uint64_t key) {
    size_t mask = m->cap - 1;
    size_t i = (size_t)((key * GOLD) >> (64 - m->bits));
    while (m->states[i] == FULL && m->keys[i] != key) i = (i + 1) & mask;
    return i;
}

static void map_insert(map *m, uint64_t key, uint64_t val);

static void map_grow(map *m) {
    map n;
    map_init(&n, m->bits + 1);
    for (size_t i = 0; i < m->cap; i++)
        if (m->states[i] == FULL) map_insert(&n, m->keys[i], m->vals[i]);
    free(m->states);
    free(m->keys);
    free(m->vals);
    *m = n;
}

static void map_insert(map *m, uint64_t key, uint64_t val) {
    if (m->count * 4 >= m->cap * 3) map_grow(m);
    size_t i = probe(m, key);
    if (m->states[i] == FULL) {
        m->vals[i] = val;
        return;
    }
    m->states[i] = FULL;
    m->keys[i] = key;
    m->vals[i] = val;
    m->count++;
}

static inline int map_find(const map *m, uint64_t key, uint64_t *out) {
    size_t i = probe(m, key);
    if (m->states[i] != FULL) return 0;
    *out = m->vals[i];
    return 1;
}

typedef struct {
    pthread_mutex_t mu;
    map m;
} locked_map;

typedef struct {
    locked_map *lm;
    int id;
    uint64_t cs;
    sampler s;
} worker_arg;

static void *worker(void *arg) {
    worker_arg *wa = arg;
    locked_map *lm = wa->lm;
    uint64_t x = LCG_SEED + (uint64_t)wa->id, cs = 0;
    for (uint64_t i = 0; i < PER_THREAD; i++) {
        x = lcg_next(&x);
        int w = wa->id == 0 && sampler_want(&wa->s, i);
        if (w) sampler_begin(&wa->s);
        if ((x >> 62) == 0) {
            uint64_t key = K + (x >> 46);
            pthread_mutex_lock(&lm->mu);
            map_insert(&lm->m, key, key * GOLD);
            pthread_mutex_unlock(&lm->mu);
            cs += key;
        } else {
            uint64_t key = x >> 46, val;
            pthread_mutex_lock(&lm->mu);
            int ok = map_find(&lm->m, key, &val);
            pthread_mutex_unlock(&lm->mu);
            if (!ok) abort();
            cs += val ^ key;
        }
        if (w) sampler_end(&wa->s);
    }
    wa->cs = cs;
    return NULL;
}

int main(void) {
    static locked_map lm;
    pthread_mutex_init(&lm.mu, NULL);
    map_init(&lm.m, 4);
    for (uint64_t k = 0; k < K; k++) map_insert(&lm.m, k, k * GOLD);
    static worker_arg args[THREADS];
    pthread_t tids[THREADS];
    for (int t = 0; t < THREADS; t++) {
        args[t].lm = &lm;
        args[t].id = t;
        sampler_init(&args[t].s, N);
    }

    uint64_t start = now_ns();
    for (int t = 0; t < THREADS; t++)
        if (pthread_create(&tids[t], NULL, worker, &args[t]) != 0) abort();
    for (int t = 0; t < THREADS; t++) pthread_join(tids[t], NULL);
    uint64_t ns = now_ns() - start;

    uint64_t checksum = 0;
    for (int t = 0; t < THREADS; t++) checksum += args[t].cs;
    checksum += lm.m.count;
    bench_report(N, ns, &args[0].s, checksum);
    free(lm.m.states);
    free(lm.m.keys);
    free(lm.m.vals);
    return 0;
}
