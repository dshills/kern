// kern hosted runtime: threads and scopes.
// Included by kern_rt.c; see kern_rt.h for the entries.

// ---- stack size (specs/runtime RC2) ----
//
// Every spawned thread gets a stack of the configured size, rounded up to
// a whole page: the last thread.set_stack_bytes that succeeded, else
// KERN_STACK_BYTES, else the build's --stack-bytes, else 8 MiB. When the
// size came from the environment or the build, the program's main function
// runs on a thread of that size too (kern_run_main).

#define KERN_STACK_DEFAULT 8388608ull

// kern_stack_api is thread.set_stack_bytes's value (0: never set);
// kern_stack_build the build's (0: none), set by the entry code before
// kern_start.
static _Atomic uint64_t kern_stack_api = 0;
static uint64_t kern_stack_build = 0;

void kern_set_stack_bytes(int64_t n) {
    if (n >= (int64_t)KERN_STACK_MIN && n <= (int64_t)KERN_STACK_MAX) kern_stack_build = (uint64_t)n;
}
// kern_stack_configured is the size the environment or the build asked
// for, 0 when neither did.
static uint64_t kern_stack_configured(void) {
    return kern_cfg.stack_bytes != 0 ? kern_cfg.stack_bytes : kern_stack_build;
}
static uint64_t kern_page_round(uint64_t n) { return (n + kern_page - 1) / kern_page * kern_page; }
uint64_t kern_thread_stack_bytes(void) {
    uint64_t n = atomic_load(&kern_stack_api);
    if (n == 0) n = kern_stack_configured();
    if (n == 0) n = KERN_STACK_DEFAULT;
    return kern_page_round(n);
}
int32_t kern_thread_set_stack_bytes(uint64_t n) {
    if (n < KERN_STACK_MIN || n > KERN_STACK_MAX) return 0;
    atomic_store(&kern_stack_api, n);
    return 1;
}

// ---- queries (RC4) ----

uint64_t kern_thread_current_id(void) { return kern_tid; }
uint64_t kern_thread_available_parallelism(void) {
#if defined(__linux__)
    cpu_set_t set;
    if (sched_getaffinity(0, sizeof set, &set) == 0) {
        int n = CPU_COUNT(&set);
        if (n > 0) return (uint64_t)n;
    }
#endif
    long n = sysconf(_SC_NPROCESSORS_ONLN);
    return n > 0 ? (uint64_t)n : 1;
}

// ---- threads (spec §5.5, specs/runtime RC1 and RC3) ----
//
// A thread's record and its block (the function pointer, the result slot
// and the arguments) live until one of the transitions below frees them.
// The thread writes its result into the block before its transition.
//
//   running  --the function returns-->   finished
//   running  --the handle is destroyed--> detached
//   finished --join-->                    gone: the joiner takes the result and frees
//   finished --the handle is destroyed--> gone: the destroyer destroys the result and frees
//   detached --the function returns-->   gone: the thread destroys the result and frees
//
// So the result is destroyed exactly once (Rule 5.5.2), by whichever of the
// thread and the handle's destruction comes second. The alternate signal
// stack is the thread's own: mapped by the spawner, so that a spawn that
// cannot have one fails before any thread exists, and unmapped by the
// thread when its function has returned.

enum { KERN_T_RUNNING = 0, KERN_T_FINISHED = 1, KERN_T_DETACHED = 2 };

#define KERN_ALTSTACK 65536u

struct kern_thread {
    pthread_t t;
    void (*thunk)(void *);
    char *block;     // the packed arguments and the result slot: the bytes after the record, in its allocation
    int64_t roff;
    void *altmap;    // the alternate signal stack's mapping: a guard page, then KERN_ALTSTACK bytes
    size_t altlen;
    kern_glue glue;  // the result's destructor, set by the handle's destruction before it detaches
    _Atomic int state;
    int joined;      // pthread_join has returned (a scoped thread may be asked for twice)
};
struct kern_scope { kern_thread **joins; int64_t n, cap; };
struct kern_join { kern_thread *t; int taken; };

// kern_altstack_map maps an alternate signal stack with a guard page below
// it; NULL when the system has none to give.
static void *kern_altstack_map(size_t *len) {
    size_t n = (size_t)kern_page + KERN_ALTSTACK;
    void *m = mmap(NULL, n, PROT_READ | PROT_WRITE, MAP_PRIVATE | MAP_ANON, -1, 0);
    if (m == MAP_FAILED) return NULL;
    (void)mprotect(m, (size_t)kern_page, PROT_NONE); // without the guard an overflow of the handler's stack is undetected, as before
    *len = n;
    return m;
}
// kern_thread_enter gives the calling thread its alternate signal stack and
// stack bounds; kern_thread_leave takes the alternate stack away and
// unmaps it.
static void kern_thread_enter(void *altmap) { kern_thread_init((char *)altmap + kern_page); }
static void kern_thread_leave(void *altmap, size_t altlen) {
    stack_t ss = { .ss_flags = SS_DISABLE };
    sigaltstack(&ss, NULL);
    munmap(altmap, altlen);
}

static void kern_thread_free(kern_thread *t) { kern_obj_free(t); }

static void *kern_thread_run(void *arg) {
    kern_thread *t = arg;
    kern_tid = atomic_fetch_add(&kern_next_tid, 1);
    kern_thread_enter(t->altmap);
    t->thunk(t->block);
    kern_thread_leave(t->altmap, t->altlen);
    int running = KERN_T_RUNNING;
    if (!atomic_compare_exchange_strong(&t->state, &running, KERN_T_FINISHED)) {
        // detached: nobody will take the result
        if (t->glue != NULL) t->glue(t->block + t->roff);
        kern_thread_free(t);
    }
    return NULL;
}

// kern_fail_spawn is armed by testing.fail_next_spawn: the next spawn
// fails as a failed pthread_create would, before any thread exists.
static _Atomic int kern_fail_spawn = 0;
#ifdef KERN_TEST_HOOKS
void kern_test_fail_next_spawn(void) { atomic_store(&kern_fail_spawn, 1); }
#endif

// kern_spawn_common starts a thread, or returns NULL with nothing left
// behind (RC3): the record, the block and the alternate signal stack are
// acquired before the thread is created. On failure the packed arguments
// are freed as bytes only: the kern side destroys the owned ones it kept
// (Rule 5.5.3), since no thread took them.
static kern_thread *kern_spawn_common(void (*thunk)(void *), const void *pack, int64_t size, int64_t roff, const kern_loc *loc) {
    if (atomic_load(&kern_fail_spawn) && atomic_exchange(&kern_fail_spawn, 0)) return NULL;
    // one allocation: the record, then the block at the next multiple of
    // 16 bytes, the alignment malloc gives a block of its own
    const size_t head = (sizeof(kern_thread) + 15) & ~(size_t)15;
    kern_thread *t = kern_try_alloc(head + (size_t)size, loc);
    if (t == NULL) return NULL;
    t->block = (char *)t + head;
    t->altmap = kern_altstack_map(&t->altlen);
    if (t->altmap == NULL) { kern_obj_free(t); return NULL; }
    kern_copy(t->block, pack, (size_t)size);
    t->thunk = thunk; t->roff = roff; t->glue = NULL; t->joined = 0;
    atomic_init(&t->state, KERN_T_RUNNING);
    pthread_attr_t at;
    int ok = pthread_attr_init(&at) == 0;
    if (ok) {
        ok = pthread_attr_setstacksize(&at, (size_t)kern_thread_stack_bytes()) == 0 && pthread_create(&t->t, &at, kern_thread_run, t) == 0;
        pthread_attr_destroy(&at);
    }
    if (!ok) { munmap(t->altmap, t->altlen); kern_thread_free(t); return NULL; }
    return t;
}
void kern_thread_spawn(void (*thunk)(void *), const void *pack, int64_t size, int64_t roff, char *out, int64_t off, const kern_loc *at) {
    kern_thread *t = kern_spawn_common(thunk, pack, size, roff, at);
    if (t == NULL) { out[0] = 1; return; }
    out[0] = 0;
    memcpy(out + off, &t, sizeof t);
}
void kern_thread_join(kern_thread *t, void *dst, int64_t size) {
    if (!t->joined) { pthread_join(t->t, NULL); t->joined = 1; }
    kern_copy(dst, t->block + t->roff, (size_t)size);
    kern_thread_free(t);
}
void kern_thread_drop(kern_thread *t, kern_glue g) {
    if (t == NULL) return;
    pthread_t th = t->t; // read before the thread may free the record
    t->glue = g;
    int running = KERN_T_RUNNING;
    if (atomic_compare_exchange_strong(&t->state, &running, KERN_T_DETACHED)) {
        pthread_detach(th); // the thread destroys its result and frees the record when its function returns
        return;
    }
    // finished: the function has returned and the result is in the block
    pthread_join(th, NULL);
    if (g != NULL) g(t->block + t->roff);
    kern_thread_free(t);
}
void *kern_scope_new(const kern_loc *at) { kern_scope *s = kern_alloc(sizeof *s, at); memset(s, 0, sizeof *s); return s; }
void kern_scope_spawn(kern_scope *sc, void (*thunk)(void *), const void *pack, int64_t size, int64_t roff, char *out, int64_t off, const kern_loc *at) {
    // the join slot and the join handle are acquired before the worker
    // starts, so that failing to get either is an ordinary spawn failure
    if (sc->n == sc->cap) {
        int64_t ncap = sc->cap ? sc->cap * 2 : 4;
        kern_thread **nj = realloc(sc->joins, (size_t)ncap * sizeof *sc->joins);
        if (!nj) { out[0] = 1; return; }
        sc->joins = nj; sc->cap = ncap;
    }
    kern_join *j = kern_try_alloc(sizeof *j, at);
    if (j == NULL) { out[0] = 1; return; }
    kern_thread *t = kern_spawn_common(thunk, pack, size, roff, at);
    if (t == NULL) { kern_obj_free(j); out[0] = 1; return; }
    sc->joins[sc->n++] = t;
    j->t = t; j->taken = 0;
    out[0] = 0;
    memcpy(out + off, &j, sizeof j);
}
void kern_join_get(kern_join *j, void *dst, int64_t size) {
    if (!j->t->joined) { pthread_join(j->t->t, NULL); j->t->joined = 1; }
    kern_copy(dst, j->t->block + j->t->roff, (size_t)size);
    j->taken = 1;
    kern_obj_free(j);
}
void kern_join_drop(kern_join *j, kern_glue g) {
    if (j == NULL) return;
    if (!j->taken) {
        if (!j->t->joined) { pthread_join(j->t->t, NULL); j->t->joined = 1; }
        if (g) g(j->t->block + j->t->roff);
    }
    kern_obj_free(j);
}
void kern_scope_drop(kern_scope *sc) {
    if (sc == NULL) return;
    for (int64_t i = 0; i < sc->n; i++) {
        kern_thread *t = sc->joins[i];
        if (!t->joined) { pthread_join(t->t, NULL); t->joined = 1; }
        kern_thread_free(t);
    }
    free(sc->joins); kern_obj_free(sc);
}

void kern_yield(void) { sched_yield(); }

// ---- the program's main function (the startup contract) ----

typedef struct { int32_t (*fn)(void); int32_t result; void *altmap; size_t altlen; } kern_main_arg;
static void *kern_main_thread(void *arg) {
    kern_main_arg *m = arg;
    kern_thread_enter(m->altmap); // identifier 0, as the thread that runs main always has
    m->result = m->fn();
    kern_thread_leave(m->altmap, m->altlen);
    return NULL;
}
// kern_run_main runs the program's main function and returns its result:
// on the calling thread, or, when the environment or the build configured
// a stack size, on a thread with that stack, so that recursion depth does
// not depend on the shell's stack limit (RC2). If that thread cannot be
// created the process ends with status 134 without running main.
int32_t kern_run_main(int32_t (*fn)(void)) {
    uint64_t size = kern_stack_configured();
    if (size == 0) return fn();
    kern_main_arg m = {fn, 0, NULL, 0};
    pthread_t th;
    pthread_attr_t at;
    m.altmap = kern_altstack_map(&m.altlen);
    int ok = m.altmap != NULL && pthread_attr_init(&at) == 0;
    if (ok) {
        ok = pthread_attr_setstacksize(&at, (size_t)kern_page_round(size)) == 0 && pthread_create(&th, &at, kern_main_thread, &m) == 0;
        pthread_attr_destroy(&at);
    }
    if (!ok) {
        static const char msg[] = "{\"runtime\":{\"fatal\":\"main stack\"}}\n";
        (void)kern_write_all(2, msg, sizeof msg - 1);
        _exit(134);
    }
    pthread_join(th, NULL);
    return m.result;
}
