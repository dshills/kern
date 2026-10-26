/* page_frame_allocator: a bitmap over 65,536 frames (1024 u64 words, set bit
 * = free, all free at start, hint = 0) and a FIFO ring of at most 4096 held
 * frames. N=10,000,000 ops; op i draws x from the LCG and allocates when
 * held == 0 or (x>>63 == 0 and held < 4096), else frees the oldest held
 * frame. alloc scans words from hint (wrapping) for the first non-zero word,
 * takes its lowest set bit, sets hint to that word; free checks the bit is
 * clear (double free aborts), sets it, and lowers hint to the word if
 * smaller. cs = (cs XOR f) * 0x100000001b3 after an alloc of frame f,
 * cs = (cs XOR (f | 1<<32)) * 0x100000001b3 after a free; at the end
 * cs += free frame count. KERN_BENCH_FAIL=1: frames 1024..65535 are marked
 * allocated in setup (never freed), so allocation fails once 1024 frames are
 * held (failures++, no checksum contribution). */
#include "../bench.h"

#define N 10000000ull
#define NFRAMES 65536
#define WORDS (NFRAMES / 64)
#define MAX_HELD 4096
#define FNV 0x100000001b3ull

typedef struct {
    uint64_t bits[WORDS];
    size_t free_count, hint;
} frame_alloc;

/* fa_alloc returns 0 when no frame is free. */
static inline int fa_alloc(frame_alloc *a, uint32_t *out) {
    if (a->free_count == 0) return 0;
    size_t w = a->hint;
    for (size_t step = 0; step < WORDS; step++) {
        uint64_t word = a->bits[w];
        if (word != 0) {
            uint32_t bit = (uint32_t)__builtin_ctzll(word);
            a->bits[w] = word & (word - 1);
            a->free_count--;
            a->hint = w;
            *out = (uint32_t)(w * 64) + bit;
            return 1;
        }
        w = (w + 1) & (WORDS - 1);
    }
    abort();
}

/* fa_free returns 0 on an out-of-range or double free. */
static inline int fa_free(frame_alloc *a, uint32_t f) {
    if (f >= NFRAMES) return 0;
    size_t w = f / 64;
    uint64_t m = 1ull << (f % 64);
    if (a->bits[w] & m) return 0;
    a->bits[w] |= m;
    a->free_count++;
    if (w < a->hint) a->hint = w;
    return 1;
}

int main(void) {
    int fail_mode = bench_fail_mode();
    static frame_alloc a;
    memset(a.bits, 0xFF, sizeof a.bits);
    a.free_count = NFRAMES;
    a.hint = 0;
    if (fail_mode) {
        for (size_t w = 1024 / 64; w < WORDS; w++) a.bits[w] = 0;
        a.free_count = 1024;
    }
    static uint32_t held[MAX_HELD];
    size_t hhead = 0, hlen = 0;
    sampler s;
    sampler_init(&s, N);
    uint64_t checksum = 0, failures = 0, x = LCG_SEED;

    uint64_t start = now_ns();
    for (uint64_t i = 0; i < N; i++) {
        x = lcg_next(&x);
        int do_alloc = hlen == 0 || ((x >> 63) == 0 && hlen < MAX_HELD);
        int w = sampler_want(&s, i);
        if (w) sampler_begin(&s);
        if (do_alloc) {
            uint32_t f;
            if (fa_alloc(&a, &f)) {
                held[(hhead + hlen) & (MAX_HELD - 1)] = f;
                hlen++;
                checksum = (checksum ^ f) * FNV;
            } else {
                failures++;
            }
        } else {
            uint32_t f = held[hhead];
            hhead = (hhead + 1) & (MAX_HELD - 1);
            hlen--;
            if (!fa_free(&a, f)) abort();
            checksum = (checksum ^ (f | (1ull << 32))) * FNV;
        }
        if (w) sampler_end(&s);
    }
    uint64_t ns = now_ns() - start;

    checksum += a.free_count;
    if (!fail_mode && failures != 0) abort();
    if (fail_mode)
        bench_report_failures(N, ns, &s, checksum, failures);
    else
        bench_report(N, ns, &s, checksum);
    return 0;
}
