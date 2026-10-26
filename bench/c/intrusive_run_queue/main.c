/* intrusive_run_queue: 1024 tasks in an array; each task embeds next/prev
 * links, a u64 prio and a u32 id. At the start task i has id i, prio i and
 * the queue links them in index order (head = task 0), so the queue is
 * non-decreasing in prio from head to tail. N=10,000,000 ops; op i pops the
 * head, adds 1024 + (x >> 58) to its prio (x from the LCG), and re-inserts
 * it by walking prev from the tail while the node's prio is greater than the
 * new prio, inserting after the node found (at the head when none).
 * Per op cs += id XOR new prio (wrapping). After the loop the queue is
 * walked from the head with position k = 0, 1, ...: cs += (k+1) * (id+1). */
#include "../bench.h"

#define N 10000000ull
#define TASKS 1024

typedef struct task {
    struct task *next, *prev;
    uint64_t prio;
    uint32_t id;
} task;

typedef struct {
    task *head, *tail;
    size_t len;
} runq;

static inline task *runq_pop(runq *q) {
    task *t = q->head;
    if (!t) return NULL;
    q->head = t->next;
    if (q->head)
        q->head->prev = NULL;
    else
        q->tail = NULL;
    t->next = t->prev = NULL;
    q->len--;
    return t;
}

/* runq_insert places t after the last node (from the tail) whose prio is
 * not greater than t's. */
static inline void runq_insert(runq *q, task *t) {
    task *p = q->tail;
    while (p && p->prio > t->prio) p = p->prev;
    t->prev = p;
    if (p) {
        t->next = p->next;
        p->next = t;
    } else {
        t->next = q->head;
        q->head = t;
    }
    if (t->next)
        t->next->prev = t;
    else
        q->tail = t;
    q->len++;
}

int main(void) {
    static task tasks[TASKS];
    runq q = {NULL, NULL, 0};
    for (uint32_t i = 0; i < TASKS; i++) {
        tasks[i].id = i;
        tasks[i].prio = i;
        tasks[i].next = tasks[i].prev = NULL;
        runq_insert(&q, &tasks[i]);
    }
    sampler s;
    sampler_init(&s, N);
    uint64_t checksum = 0, x = LCG_SEED;

    uint64_t start = now_ns();
    for (uint64_t i = 0; i < N; i++) {
        x = lcg_next(&x);
        uint64_t delta = 1024 + (x >> 58);
        int w = sampler_want(&s, i);
        if (w) sampler_begin(&s);
        task *t = runq_pop(&q);
        if (!t) abort();
        t->prio += delta;
        runq_insert(&q, t);
        if (w) sampler_end(&s);
        checksum += t->id ^ t->prio;
    }
    uint64_t ns = now_ns() - start;

    if (q.len != TASKS) abort();
    uint64_t k = 0;
    for (task *t = q.head; t; t = t->next, k++) checksum += (k + 1) * ((uint64_t)t->id + 1);
    if (k != TASKS) abort();
    bench_report(N, ns, &s, checksum);
    return 0;
}
