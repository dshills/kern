// kern hosted runtime: threads and scopes.
// Included by kern_rt.c; see kern_rt.h for the entries.

// ---- threads (spec §5.5) ----

struct kern_thread { pthread_t t; char *block; int64_t roff; int joined, started; };
struct kern_scope { kern_thread **joins; int64_t n, cap; };
struct kern_join { kern_thread *t; int taken; };

typedef struct { void (*thunk)(void *); char *block; } kern_spawn_arg;
static void *kern_thread_run(void *arg) {
    kern_spawn_arg *s = arg;
    char *altstack = malloc(65536);
    kern_tid = atomic_fetch_add(&kern_next_tid, 1);
    if (altstack) kern_thread_init(altstack);
    s->thunk(s->block);
    stack_t ss = { .ss_flags = SS_DISABLE };
    sigaltstack(&ss, NULL);
    free(altstack);
    free(s);
    return NULL;
}
// kern_fail_spawn is armed by testing.fail_next_spawn: the next spawn
// fails as a failed pthread_create would, before any thread exists.
static _Atomic int kern_fail_spawn = 0;
#ifdef KERN_TEST_HOOKS
void kern_test_fail_next_spawn(void) { atomic_store(&kern_fail_spawn, 1); }
#endif
// On failure the packed arguments are freed as bytes only: the kern side
// destroys the owned ones it kept (Rule 5.5.3), since no thread took them.
static kern_thread *kern_spawn_common(void (*thunk)(void *), const void *pack, int64_t size, int64_t roff, int *ok, const kern_loc *at) {
    if (atomic_load(&kern_fail_spawn) && atomic_exchange(&kern_fail_spawn, 0)) { *ok = 0; return NULL; }
    kern_thread *t = kern_alloc(sizeof *t, at);
    t->block = kern_alloc((size_t)size, at);
    kern_copy(t->block, pack, (size_t)size);
    t->roff = roff; t->joined = 0; t->started = 0;
    kern_spawn_arg *s = kern_alloc(sizeof *s, at);
    s->thunk = thunk; s->block = t->block;
    if (pthread_create(&t->t, NULL, kern_thread_run, s) != 0) { free(s); free(t->block); free(t); *ok = 0; return NULL; }
    t->started = 1;
    *ok = 1;
    return t;
}
void kern_thread_spawn(void (*thunk)(void *), const void *pack, int64_t size, int64_t roff, char *out, int64_t off, const kern_loc *at) {
    int ok;
    kern_thread *t = kern_spawn_common(thunk, pack, size, roff, &ok, at);
    if (!ok) { out[0] = 1; return; }
    out[0] = 0;
    memcpy(out + off, &t, sizeof t);
}
void kern_thread_join(kern_thread *t, void *dst, int64_t size) {
    if (!t->joined) { pthread_join(t->t, NULL); t->joined = 1; }
    kern_copy(dst, t->block + t->roff, (size_t)size);
    free(t->block); free(t);
}
void kern_thread_drop(kern_thread *t) {
    if (t == NULL) return;
    if (!t->joined) pthread_detach(t->t); // the detached thread's result is leaked (Phase 2 limitation)
    free(t);
}
void *kern_scope_new(const kern_loc *at) { kern_scope *s = kern_alloc(sizeof *s, at); memset(s, 0, sizeof *s); return s; }
void kern_scope_spawn(kern_scope *sc, void (*thunk)(void *), const void *pack, int64_t size, int64_t roff, char *out, int64_t off, const kern_loc *at) {
    // reserve the join slot before the worker starts, so a failed
    // reservation is an ordinary spawn failure
    if (sc->n == sc->cap) {
        int64_t ncap = sc->cap ? sc->cap * 2 : 4;
        kern_thread **nj = realloc(sc->joins, (size_t)ncap * sizeof *sc->joins);
        if (!nj) { out[0] = 1; return; }
        sc->joins = nj; sc->cap = ncap;
    }
    int ok;
    kern_thread *t = kern_spawn_common(thunk, pack, size, roff, &ok, at);
    if (!ok) { out[0] = 1; return; }
    sc->joins[sc->n++] = t;
    kern_join *j = kern_alloc(sizeof *j, at);
    j->t = t; j->taken = 0;
    out[0] = 0;
    memcpy(out + off, &j, sizeof j);
}
void kern_join_get(kern_join *j, void *dst, int64_t size) {
    if (!j->t->joined) { pthread_join(j->t->t, NULL); j->t->joined = 1; }
    kern_copy(dst, j->t->block + j->t->roff, (size_t)size);
    j->taken = 1;
    free(j);
}
void kern_join_drop(kern_join *j, kern_glue g) {
    if (j == NULL) return;
    if (!j->taken) {
        if (!j->t->joined) { pthread_join(j->t->t, NULL); j->t->joined = 1; }
        if (g) g(j->t->block + j->t->roff);
    }
    free(j);
}
void kern_scope_drop(kern_scope *sc) {
    if (sc == NULL) return;
    for (int64_t i = 0; i < sc->n; i++) {
        kern_thread *t = sc->joins[i];
        if (!t->joined) { pthread_join(t->t, NULL); t->joined = 1; }
        free(t->block); free(t);
    }
    free(sc->joins); free(sc);
}

void kern_yield(void) { sched_yield(); }
