/* thread_pool: 4 worker threads (pthreads) pull jobs from a mutex-protected
 * FIFO ring of 2048 job indices with two condition variables (ready, space).
 * The main thread feeds N=1,000,000 jobs in batches of 1024 (the last batch
 * has 576): it waits until the ring holds at most 1024 jobs, pushes the
 * batch, broadcasts ready.
 * Job b sums the 64 words of block b & 4095 of a 4096-block input
 * (word k of the whole input = k * 0x9E3779B97F4A7C15, wrapping) and yields
 * sum XOR b. A worker adds its previous job's result to the shared total
 * under the queue mutex when it takes its next job (or before exiting);
 * when a pop leaves exactly 1024 jobs it signals space. ops = N jobs; the
 * sampled operation is worker 0's i-th job (lock, pop, compute) with i its
 * own counter. Timed: feeding through joining; threads = 4 workers + main.
 * checksum = total (wrapping sum of the job results) + N. */
#include "../bench.h"
#include <pthread.h>

#define N 1000000ull
#define WORKERS 4
#define BATCH 1024
#define QCAP 2048
#define BLOCKS 4096
#define WORDS 64

static uint64_t words[BLOCKS * WORDS];

typedef struct {
    pthread_mutex_t mu;
    pthread_cond_t ready, space;
    uint64_t ring[QCAP];
    size_t head, len;
    uint64_t total, done_jobs;
    int done;
} queue;

typedef struct {
    queue *q;
    sampler s;
    int sample;
} worker_arg;

static inline uint64_t run_job(uint64_t b) {
    const uint64_t *blk = &words[(b & (BLOCKS - 1)) * WORDS];
    uint64_t sum = 0;
    for (int j = 0; j < WORDS; j++) sum += blk[j];
    return sum ^ b;
}

static void *worker(void *arg) {
    worker_arg *wa = arg;
    queue *q = wa->q;
    uint64_t pending = 0, i = 0;
    int have = 0;
    for (;;) {
        int w = wa->sample && sampler_want(&wa->s, i);
        if (w) sampler_begin(&wa->s);
        pthread_mutex_lock(&q->mu);
        if (have) {
            q->total += pending;
            q->done_jobs++;
            have = 0;
        }
        while (q->len == 0 && !q->done) pthread_cond_wait(&q->ready, &q->mu);
        if (q->len == 0) {
            pthread_mutex_unlock(&q->mu);
            return NULL;
        }
        uint64_t b = q->ring[q->head];
        q->head = (q->head + 1) & (QCAP - 1);
        q->len--;
        if (q->len == BATCH) pthread_cond_signal(&q->space);
        pthread_mutex_unlock(&q->mu);
        pending = run_job(b);
        have = 1;
        if (w) sampler_end(&wa->s);
        i++;
    }
}

int main(void) {
    for (uint64_t k = 0; k < BLOCKS * WORDS; k++) words[k] = k * 0x9E3779B97F4A7C15ull;
    static queue q;
    pthread_mutex_init(&q.mu, NULL);
    pthread_cond_init(&q.ready, NULL);
    pthread_cond_init(&q.space, NULL);
    static worker_arg args[WORKERS];
    pthread_t tids[WORKERS];

    uint64_t start = now_ns();
    for (int t = 0; t < WORKERS; t++) {
        args[t].q = &q;
        args[t].sample = t == 0;
        sampler_init(&args[t].s, N);
        if (pthread_create(&tids[t], NULL, worker, &args[t]) != 0) abort();
    }
    for (uint64_t b = 0; b < N; b += BATCH) {
        uint64_t n = N - b < BATCH ? N - b : BATCH;
        pthread_mutex_lock(&q.mu);
        while (q.len > BATCH) pthread_cond_wait(&q.space, &q.mu);
        for (uint64_t k = 0; k < n; k++) {
            q.ring[(q.head + q.len) & (QCAP - 1)] = b + k;
            q.len++;
        }
        pthread_cond_broadcast(&q.ready);
        pthread_mutex_unlock(&q.mu);
    }
    pthread_mutex_lock(&q.mu);
    q.done = 1;
    pthread_cond_broadcast(&q.ready);
    pthread_mutex_unlock(&q.mu);
    for (int t = 0; t < WORKERS; t++) pthread_join(tids[t], NULL);
    uint64_t ns = now_ns() - start;

    if (q.done_jobs != N) abort();
    uint64_t checksum = q.total + N;
    bench_report(N, ns, &args[0].s, checksum);
    return 0;
}
