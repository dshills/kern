/* buffer_pool: 256 frames of 4 KiB over a 1024-page in-memory backing store.
 * Store page p holds p (LE u64) in bytes 0..8 and a counter (LE u64, 0) in
 * bytes 8..16. The page table is a direct-mapped array page -> frame.
 * N=5,000,000 ops; op i draws x from the LCG, page p = (x>>32)&127 when
 * x>>56 < 224, else (x>>32)&1023; pins p (a miss loads it into the unpinned
 * frame with the smallest last-unpin tick, lowest index on ties, writing the
 * victim back when dirty), verifies bytes 0..8 == p, increments the counter,
 * marks the frame dirty, unpins (last = ++tick). Per op cs += p | (f << 16).
 * After the loop every dirty frame is flushed; cs += sum over pages of
 * counter(p) * (p + 1), then cs += evictions. KERN_BENCH_FAIL=1: pages 0..255
 * are pinned in setup and never released, so a miss finds no victim and
 * returns an error (failures++, no checksum contribution). */
#include "../bench.h"

#define N 5000000ull
#define PAGES 1024
#define FRAMES 256
#define PAGE_SIZE 4096
#define NONE 0xFFFFFFFFu

typedef struct {
    uint8_t data[PAGE_SIZE];
    uint64_t page; /* UINT64_MAX when empty */
    uint64_t last; /* tick of the last unpin; 0 = never */
    uint32_t pins;
    int dirty;
} frame;

typedef struct {
    frame frames[FRAMES];
    uint32_t table[PAGES];
    uint64_t tick, evictions;
    uint8_t (*store)[PAGE_SIZE];
} pool;

static inline uint64_t get_u64(const uint8_t *p) {
    uint64_t v = 0;
    for (int i = 7; i >= 0; i--) v = (v << 8) | p[i];
    return v;
}

static inline void put_u64(uint8_t *p, uint64_t v) {
    for (int i = 0; i < 8; i++) p[i] = (uint8_t)(v >> (8 * i));
}

/* pool_pin returns the frame holding page p (loading it on a miss), or -1
 * when every frame is pinned. */
static int pool_pin(pool *bp, uint64_t p) {
    uint32_t f = bp->table[p];
    if (f != NONE) {
        bp->frames[f].pins++;
        return (int)f;
    }
    int victim = -1;
    uint64_t best = UINT64_MAX;
    for (int i = 0; i < FRAMES; i++) {
        const frame *fr = &bp->frames[i];
        if (fr->pins == 0 && fr->last < best) {
            best = fr->last;
            victim = i;
        }
    }
    if (victim < 0) return -1;
    frame *fr = &bp->frames[victim];
    if (fr->page != UINT64_MAX) {
        bp->table[fr->page] = NONE;
        if (fr->dirty) memcpy(bp->store[fr->page], fr->data, PAGE_SIZE);
        fr->dirty = 0;
        bp->evictions++;
    }
    memcpy(fr->data, bp->store[p], PAGE_SIZE);
    fr->page = p;
    fr->pins = 1;
    bp->table[p] = (uint32_t)victim;
    return victim;
}

static inline void pool_unpin(pool *bp, int f) {
    bp->frames[f].pins--;
    bp->frames[f].last = ++bp->tick;
}

int main(void) {
    int fail_mode = bench_fail_mode();
    static pool bp;
    bp.store = xmalloc((size_t)PAGES * PAGE_SIZE);
    memset(bp.store, 0, (size_t)PAGES * PAGE_SIZE);
    for (uint64_t p = 0; p < PAGES; p++) put_u64(bp.store[p], p);
    for (int i = 0; i < FRAMES; i++) bp.frames[i].page = UINT64_MAX;
    for (int i = 0; i < PAGES; i++) bp.table[i] = NONE;
    if (fail_mode)
        for (uint64_t p = 0; p < FRAMES; p++)
            if (pool_pin(&bp, p) != (int)p) abort();

    sampler s;
    sampler_init(&s, N);
    uint64_t checksum = 0, failures = 0, x = LCG_SEED;

    uint64_t start = now_ns();
    for (uint64_t i = 0; i < N; i++) {
        x = lcg_next(&x);
        uint64_t p = (x >> 56) < 224 ? (x >> 32) & 127 : (x >> 32) & 1023;
        int w = sampler_want(&s, i);
        if (w) sampler_begin(&s);
        int f = pool_pin(&bp, p);
        if (f >= 0) {
            uint8_t *d = bp.frames[f].data;
            if (get_u64(d) != p) abort();
            put_u64(d + 8, get_u64(d + 8) + 1);
            bp.frames[f].dirty = 1;
            pool_unpin(&bp, f);
        }
        if (w) sampler_end(&s);
        if (f >= 0)
            checksum += p | ((uint64_t)f << 16);
        else
            failures++;
    }
    uint64_t ns = now_ns() - start;

    for (int i = 0; i < FRAMES; i++) {
        frame *fr = &bp.frames[i];
        if (fr->page != UINT64_MAX && fr->dirty) memcpy(bp.store[fr->page], fr->data, PAGE_SIZE);
    }
    for (uint64_t p = 0; p < PAGES; p++) checksum += get_u64(bp.store[p] + 8) * (p + 1);
    checksum += bp.evictions;
    if (!fail_mode && failures != 0) abort();

    if (fail_mode)
        bench_report_failures(N, ns, &s, checksum, failures);
    else
        bench_report(N, ns, &s, checksum);
    free(bp.store);
    return 0;
}
