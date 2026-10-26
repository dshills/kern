/* arena_ast: an arena of exactly N=1,000,000 expression nodes addressed by
 * u32 index. Slot 0 is a Num(0) sentinel (the null handle, never referenced);
 * slots 1..999,999 hold a balanced binary tree built post-order by
 * build(n) (n odd): n==1 -> Num(lcg>>40); else m=n-1, l=m/2 (l-=1 if l is
 * even), r=m-l, build(l), build(r), then x=lcg: Add if x>>63 else Mul.
 * LCG seed 42; one draw per node, in allocation order. Evaluation is a
 * recursive walk in arithmetic modulo the prime M = 4294967291 (2^32-5):
 * Num -> v (v < 2^24 < M), Add -> (l + r) % M, Mul -> (l * r) % M; operands
 * are below 2^32 so no intermediate exceeds 2^64 and nothing wraps.
 * ops = N allocations; sampled operation: one allocation; the evaluation is
 * timed but not counted.
 * checksum = eval(root) + N. */
#include "../bench.h"

#define N 1000000ull
#define M 4294967291ull

enum { NUM = 0, ADD = 1, MUL = 2 };

typedef struct {
    uint64_t num;
    uint32_t l, r;
    uint8_t kind;
} expr;

typedef struct {
    expr *nodes;
    size_t len, cap;
} arena;

static sampler samp;

static uint32_t alloc(arena *a, expr e) {
    int w = sampler_want(&samp, a->len);
    if (w) sampler_begin(&samp);
    if (a->len == a->cap) {
        a->cap = a->cap ? a->cap * 2 : 16;
        a->nodes = xrealloc(a->nodes, a->cap * sizeof(expr));
    }
    a->nodes[a->len] = e;
    uint32_t id = (uint32_t)a->len++;
    if (w) sampler_end(&samp);
    return id;
}

static inline const expr *get(const arena *a, uint32_t id) {
    if (id >= a->len) abort();
    return &a->nodes[id];
}

static uint32_t build(arena *a, uint64_t *x, uint64_t n) {
    if (n == 1) {
        expr e = {lcg_next(x) >> 40, 0, 0, NUM};
        return alloc(a, e);
    }
    uint64_t m = n - 1, l = m / 2;
    if (l % 2 == 0) l--;
    uint32_t left = build(a, x, l);
    uint32_t right = build(a, x, m - l);
    expr e = {0, left, right, (lcg_next(x) >> 63) ? ADD : MUL};
    return alloc(a, e);
}

static uint64_t eval(const arena *a, uint32_t id) {
    const expr *e = get(a, id);
    switch (e->kind) {
    case NUM:
        return e->num;
    case ADD:
        return (eval(a, e->l) + eval(a, e->r)) % M;
    default:
        return (eval(a, e->l) * eval(a, e->r)) % M;
    }
}

int main(void) {
    arena a = {0, 0, 0};
    sampler_init(&samp, N);

    uint64_t start = now_ns();
    uint64_t x = LCG_SEED;
    expr sentinel = {0, 0, 0, NUM};
    alloc(&a, sentinel);
    uint32_t root = build(&a, &x, N - 1);
    uint64_t checksum = eval(&a, root) + N;
    uint64_t ns = now_ns() - start;

    if (a.len != N) abort();
    bench_report(N, ns, &samp, checksum);
    free(a.nodes);
    return 0;
}
