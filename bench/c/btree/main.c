/* btree: N=2,000,000 distinct u64 keys from the LCG (seed 42), value = insert
 * index i, inserted into an order-16 B-tree (15 keys, 16 children per node,
 * min degree 8) whose nodes live in a growable pool addressed by u32 index.
 * Insert is the CLRS preemptive algorithm: a full root is split before the
 * descent and a full child is split (median key index 7 moves up, the right
 * node takes keys 8..14) before stepping into it. Then an in-order traversal.
 * ops = N; sampled operation: one insert; the traversal is timed but not
 * counted.
 * checksum = (wrapping sum over in-order position p of (key XOR p) + value)
 *            XOR number of pool nodes. */
#include "../bench.h"

#define N 2000000ull
#define MAXK 15
#define T 8

typedef struct {
    uint64_t keys[MAXK], vals[MAXK];
    uint32_t children[MAXK + 1];
    uint32_t count;
    uint8_t leaf;
} node;

typedef struct {
    node *nodes;
    size_t len, cap;
    uint32_t root;
} btree;

static uint32_t pool_alloc(btree *t, uint8_t leaf) {
    if (t->len == t->cap) {
        t->cap = t->cap ? t->cap * 2 : 16;
        t->nodes = xrealloc(t->nodes, t->cap * sizeof(node));
    }
    node *n = &t->nodes[t->len];
    n->count = 0;
    n->leaf = leaf;
    return (uint32_t)t->len++;
}

static inline node *at(btree *t, uint32_t id) {
    if (id >= t->len) abort();
    return &t->nodes[id];
}

/* split_child splits the full child i of parent p. */
static void split_child(btree *t, uint32_t p, uint32_t i) {
    uint32_t l = at(t, p)->children[i];
    uint32_t r = pool_alloc(t, at(t, l)->leaf); /* may move the pool */
    node *left = at(t, l), *right = at(t, r), *par = at(t, p);
    right->count = T - 1;
    for (uint32_t k = 0; k < T - 1; k++) {
        right->keys[k] = left->keys[k + T];
        right->vals[k] = left->vals[k + T];
    }
    if (!left->leaf)
        for (uint32_t k = 0; k < T; k++) right->children[k] = left->children[k + T];
    left->count = T - 1;
    for (uint32_t k = par->count; k > i; k--) par->children[k + 1] = par->children[k];
    par->children[i + 1] = r;
    for (uint32_t k = par->count; k > i; k--) {
        par->keys[k] = par->keys[k - 1];
        par->vals[k] = par->vals[k - 1];
    }
    par->keys[i] = left->keys[T - 1];
    par->vals[i] = left->vals[T - 1];
    par->count++;
}

static void insert(btree *t, uint64_t key, uint64_t val) {
    if (at(t, t->root)->count == MAXK) {
        uint32_t s = pool_alloc(t, 0);
        at(t, s)->children[0] = t->root;
        t->root = s;
        split_child(t, s, 0);
    }
    uint32_t x = t->root;
    for (;;) {
        node *n = at(t, x);
        uint32_t i = n->count;
        while (i > 0 && key < n->keys[i - 1]) i--;
        if (i > 0 && n->keys[i - 1] == key) {
            n->vals[i - 1] = val;
            return;
        }
        if (n->leaf) {
            for (uint32_t k = n->count; k > i; k--) {
                n->keys[k] = n->keys[k - 1];
                n->vals[k] = n->vals[k - 1];
            }
            n->keys[i] = key;
            n->vals[i] = val;
            n->count++;
            return;
        }
        if (at(t, n->children[i])->count == MAXK) {
            split_child(t, x, i);
            n = at(t, x);
            if (key == n->keys[i]) {
                n->vals[i] = val;
                return;
            }
            if (key > n->keys[i]) i++;
        }
        x = n->children[i];
    }
}

static void traverse(btree *t, uint32_t id, uint64_t *checksum, uint64_t *pos) {
    node *n = at(t, id);
    for (uint32_t i = 0; i < n->count; i++) {
        if (!n->leaf) traverse(t, n->children[i], checksum, pos);
        n = at(t, id);
        *checksum += (n->keys[i] ^ *pos) + n->vals[i];
        (*pos)++;
    }
    if (!n->leaf) traverse(t, n->children[n->count], checksum, pos);
}

int main(void) {
    btree t = {0, 0, 0, 0};
    t.root = pool_alloc(&t, 1);
    sampler s;
    sampler_init(&s, N);
    uint64_t checksum = 0, pos = 0;

    uint64_t start = now_ns();
    uint64_t x = LCG_SEED;
    for (uint64_t i = 0; i < N; i++) {
        uint64_t key = lcg_next(&x);
        int w = sampler_want(&s, i);
        if (w) sampler_begin(&s);
        insert(&t, key, i);
        if (w) sampler_end(&s);
    }
    traverse(&t, t.root, &checksum, &pos);
    checksum ^= (uint64_t)t.len;
    uint64_t ns = now_ns() - start;

    if (pos != N) abort();
    bench_report(N, ns, &s, checksum);
    free(t.nodes);
    return 0;
}
