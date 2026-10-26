/* bench.h: timing, latency sampling, output and LCG helpers shared by the C
 * benchmark programs (bench/PROTOCOL.md). Every program prints exactly:
 *   ops=<N> ns=<elapsed> p50=<ns/op> p99=<ns/op> checksum=<u64>
 * Per-op latency is sampled every ops/4096 operations (at most 4096 samples);
 * p50 = sorted[n/2], p99 = sorted[n*99/100]. */
#ifndef KERN_BENCH_H
#define KERN_BENCH_H

#include <inttypes.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#define BENCH_MAX_SAMPLES 4096
#define LCG_SEED 42ull

/* On macOS CLOCK_MONOTONIC has microsecond resolution; CLOCK_MONOTONIC_RAW
 * ticks in nanoseconds, matching Rust's Instant (CLOCK_UPTIME_RAW). */
#ifdef __APPLE__
#define BENCH_CLOCK CLOCK_MONOTONIC_RAW
#else
#define BENCH_CLOCK CLOCK_MONOTONIC
#endif

static inline uint64_t now_ns(void) {
    struct timespec ts;
    clock_gettime(BENCH_CLOCK, &ts);
    return (uint64_t)ts.tv_sec * 1000000000ull + (uint64_t)ts.tv_nsec;
}

/* The 64-bit LCG shared by all implementations: x = x*a + c, seed 42. */
static inline uint64_t lcg_next(uint64_t *x) {
    *x = *x * 6364136223846793005ull + 1442695040888963407ull;
    return *x;
}

typedef struct {
    uint64_t samples[BENCH_MAX_SAMPLES];
    size_t n;
    uint64_t stride;
    uint64_t t0;
} sampler;

static inline void sampler_init(sampler *s, uint64_t ops) {
    s->n = 0;
    s->stride = ops / BENCH_MAX_SAMPLES;
    if (s->stride == 0) s->stride = 1;
}

/* sampler_want reports whether operation i is a sampled one. */
static inline int sampler_want(const sampler *s, uint64_t i) {
    return i % s->stride == 0 && s->n < BENCH_MAX_SAMPLES;
}
static inline void sampler_begin(sampler *s) { s->t0 = now_ns(); }
static inline void sampler_end(sampler *s) { s->samples[s->n++] = now_ns() - s->t0; }

static int bench_cmp_u64(const void *a, const void *b) {
    uint64_t x = *(const uint64_t *)a, y = *(const uint64_t *)b;
    return x < y ? -1 : x > y;
}

static inline void bench_report(uint64_t ops, uint64_t ns, sampler *s, uint64_t checksum) {
    uint64_t p50 = 0, p99 = 0;
    if (s->n > 0) {
        qsort(s->samples, s->n, sizeof(uint64_t), bench_cmp_u64);
        p50 = s->samples[s->n / 2];
        p99 = s->samples[s->n * 99 / 100];
    }
    printf("ops=%" PRIu64 " ns=%" PRIu64 " p50=%" PRIu64 " p99=%" PRIu64 " checksum=%" PRIu64 "\n",
           ops, ns, p50, p99, checksum);
}

static inline void *xmalloc(size_t n) {
    void *p = malloc(n ? n : 1);
    if (!p) abort();
    return p;
}

static inline void *xrealloc(void *p, size_t n) {
    void *q = realloc(p, n ? n : 1);
    if (!q) abort();
    return q;
}

#endif
