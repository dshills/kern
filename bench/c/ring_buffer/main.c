/* ring_buffer: a 1024-slot ring of u64 (inline array, head/tail/len, index
 * masked with 1023). Untimed prefill pushes the values 0..511. Then
 * N=50,000,000 operations; operation i pushes 512+i and pops one value
 * (which is i). ops = N; sampled operation: one push+pop pair.
 * checksum = FNV-style chain over popped values p:
 *   cs = (cs XOR p) * 0x100000001b3 (wrapping), starting from 0. */
#include "../bench.h"

#define N 50000000ull
#define CAP 1024

typedef struct {
    uint64_t slots[CAP];
    size_t head, tail, len;
} ring;

/* ring_push returns 0 when the ring is full (the value is handed back). */
static inline int ring_push(ring *r, uint64_t v) {
    if (r->len == CAP) return 0;
    if (r->tail >= CAP) abort();
    r->slots[r->tail] = v;
    r->tail = (r->tail + 1) & (CAP - 1);
    r->len++;
    return 1;
}

/* ring_pop returns 0 when the ring is empty. */
static inline int ring_pop(ring *r, uint64_t *out) {
    if (r->len == 0) return 0;
    if (r->head >= CAP) abort();
    *out = r->slots[r->head];
    r->head = (r->head + 1) & (CAP - 1);
    r->len--;
    return 1;
}

int main(void) {
    static ring r;
    r.head = r.tail = r.len = 0;
    for (uint64_t i = 0; i < 512; i++)
        if (!ring_push(&r, i)) abort();
    sampler s;
    sampler_init(&s, N);
    uint64_t checksum = 0;

    uint64_t start = now_ns();
    for (uint64_t i = 0; i < N; i++) {
        uint64_t p;
        int w = sampler_want(&s, i);
        if (w) sampler_begin(&s);
        if (!ring_push(&r, 512 + i)) abort();
        if (!ring_pop(&r, &p)) abort();
        if (w) sampler_end(&s);
        checksum = (checksum ^ p) * 0x100000001b3ull;
    }
    uint64_t ns = now_ns() - start;

    if (r.len != 512) abort();
    bench_report(N, ns, &s, checksum);
    return 0;
}
