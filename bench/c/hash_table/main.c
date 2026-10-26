/* hash_table: N=5,000,000 keys from the LCG (seed 42; key = next value).
 * Insert (key, value=i) into an open-addressing table (linear probing,
 * Fibonacci hash h = (key*0x9E3779B97F4A7C15) >> (64-bits), structure of
 * arrays states/keys/vals, initial capacity 16, doubled before an insert
 * when count*4 >= cap*3), then look up every key in insertion order (the LCG
 * is re-run from the seed). ops = N; sampled operation: one insert; the
 * lookup pass is timed but not counted.
 * checksum = wrapping sum over lookups of (value XOR key), plus count. */
#include "../bench.h"

#define N 5000000ull
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
    size_t i = (size_t)((key * 0x9E3779B97F4A7C15ull) >> (64 - m->bits));
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

/* map_find returns 1 and the value when key is present. */
static inline int map_find(const map *m, uint64_t key, uint64_t *out) {
    size_t i = probe(m, key);
    if (m->states[i] != FULL) return 0;
    *out = m->vals[i];
    return 1;
}

int main(void) {
    map m;
    map_init(&m, 4);
    sampler s;
    sampler_init(&s, N);
    uint64_t checksum = 0;

    uint64_t start = now_ns();
    uint64_t x = LCG_SEED;
    for (uint64_t i = 0; i < N; i++) {
        uint64_t key = lcg_next(&x);
        int w = sampler_want(&s, i);
        if (w) sampler_begin(&s);
        map_insert(&m, key, i);
        if (w) sampler_end(&s);
    }
    x = LCG_SEED;
    for (uint64_t i = 0; i < N; i++) {
        uint64_t key = lcg_next(&x), val;
        if (!map_find(&m, key, &val)) abort();
        checksum += val ^ key;
    }
    checksum += (uint64_t)m.count;
    uint64_t ns = now_ns() - start;

    bench_report(N, ns, &s, checksum);
    free(m.states);
    free(m.keys);
    free(m.vals);
    return 0;
}
