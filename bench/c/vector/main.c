/* vector: 4 rounds; each round pushes PER=5,000,000 u64 values into a vector
 * that is cleared (capacity kept) at the start of the round, then sums the
 * elements through a (pointer, len) view. Pushed value for global push index
 * g = r*PER + k is g*3. ops = 20,000,000 pushes; the sum passes are timed but
 * not counted as ops. Sampled operation: one push.
 * checksum = wrapping sum of the four round sums. */
#include "../bench.h"

#define ROUNDS 4ull
#define PER 5000000ull
#define OPS (ROUNDS * PER)

typedef struct {
    uint64_t *buf;
    size_t len, cap;
} vec;

static inline void vec_push(vec *v, uint64_t x) {
    if (v->len == v->cap) {
        v->cap = v->cap ? v->cap * 2 : 8;
        v->buf = xrealloc(v->buf, v->cap * sizeof(uint64_t));
    }
    v->buf[v->len++] = x;
}

/* vec_get keeps the bounds check the workload note requires. */
static inline uint64_t vec_get(const vec *v, size_t i) {
    if (i >= v->len) abort();
    return v->buf[i];
}

/* sum iterates a view (pointer + length) of the vector. */
static uint64_t sum(const uint64_t *items, size_t n) {
    uint64_t total = 0;
    for (size_t i = 0; i < n; i++) total += items[i];
    return total;
}

int main(void) {
    vec v = {0, 0, 0};
    sampler s;
    sampler_init(&s, OPS);
    uint64_t checksum = 0;

    uint64_t start = now_ns();
    for (uint64_t r = 0; r < ROUNDS; r++) {
        v.len = 0; /* clear: capacity retained */
        for (uint64_t k = 0; k < PER; k++) {
            uint64_t g = r * PER + k;
            int w = sampler_want(&s, g);
            if (w) sampler_begin(&s);
            vec_push(&v, g * 3);
            if (w) sampler_end(&s);
        }
        checksum += sum(v.buf, v.len);
    }
    uint64_t ns = now_ns() - start;

    if (vec_get(&v, 0) != (ROUNDS - 1) * PER * 3) abort();
    bench_report(OPS, ns, &s, checksum);
    free(v.buf);
    return 0;
}
