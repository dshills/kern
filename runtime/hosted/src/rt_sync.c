// kern hosted runtime: Shared, Mutex and Condvar.
// Included by kern_rt.c; see kern_rt.h for the entries.

// ---- Shared ----

struct kern_shared { _Atomic uint64_t count; int64_t size; char data[]; };
void *kern_shared_new(int64_t size, const void *src, const kern_loc *at) {
    kern_shared *s = kern_alloc(sizeof *s + (size_t)size, at);
    atomic_store(&s->count, 1); s->size = size;
    kern_copy(s->data, src, (size_t)size);
    return s;
}
void kern_shared_try_new(int64_t size, void *src, void (*glue)(void *), char *out, int64_t off, const kern_loc *at) {
    kern_shared *s = kern_try_alloc(sizeof *s + (size_t)size, at);
    if (s == NULL) { kern_try_failed(glue, src, out); return; }
    atomic_store(&s->count, 1); s->size = size;
    kern_copy(s->data, src, (size_t)size);
    out[0] = 0;
    memcpy(out + off, &s, sizeof s);
}
void *kern_shared_get(kern_shared *s) { return s->data; }
void *kern_shared_clone(kern_shared *s, const kern_loc *at) {
    uint64_t c = atomic_load(&s->count);
    for (;;) {
        if (c == UINT64_MAX) kern_trap_loc(1, at);
        if (atomic_compare_exchange_weak(&s->count, &c, c + 1)) return s;
    }
}
void kern_shared_drop(kern_shared *s, kern_glue g) {
    if (s == NULL) return;
    if (atomic_fetch_sub(&s->count, 1) == 1) { if (g) g(s->data); kern_obj_free(s); }
}
#ifdef KERN_TEST_HOOKS
void kern_test_shared_seed_count(kern_shared *s, int64_t n) { atomic_store(&s->count, (uint64_t)n); }
#endif

// ---- Mutex ----

// A Mutex is the runtime's own lock (specs/perf-gaps/SPEC.md): a 32-bit
// state that waiters park on (0 free; 1 held; 2 held, and a thread has
// set it so before parking, so the release must unpark one), and the
// holder's thread id beside it in the same cache line. The holder
// writes its id after the acquiring exchange, when it already owns the
// line, and clears it before the releasing exchange, so the id costs no
// coherence traffic of its own; a thread that fails the fast path and
// finds its own id there is acquiring the mutex it holds, which traps
// (spec §13 accepted decision, as the interpreter does). Only the holder
// writes its id and a thread reads its own last write, so the relaxed
// load never mistakes another thread's lock for a recursive one.
//
// This replaced a default pthread mutex with the owner stored beside it:
// the owner then shared the line that arriving waiters write with their
// compare-and-swap attempts, which cost lock_protected_map about 15% of
// its throughput, and every layout that moved the owner to another line
// cost thread_pool as much, since each hand-off then migrated one more
// line between cores (docs/reports/perf-gaps.md). There is no spin loop
// before parking: spinning makes the pool's hand-off between workers
// fair and migrates the line on every job, at 0.69 of C.
//
// Parking is os_sync_wait_on_address on macOS and a futex on Linux. Both
// compare the 32-bit value and sleep only while it is unchanged, so a
// wake between a caller's check and its sleep is not lost; both return
// without a wake at times, with or without an error, and every caller
// treats any return as spurious and re-checks before parking again. An
// unpark reports at most that nothing was parked, which is ignored (a
// notify with no waiter is lost, spec §5.5); the address is the
// runtime's own aligned word and the size and flags are constants, so
// the other documented errors cannot arise.
#if defined(__APPLE__)
#include <os/os_sync_wait_on_address.h>
void kern_park(_Atomic(uint32_t) *w, uint32_t expect) { (void)os_sync_wait_on_address(w, expect, sizeof *w, OS_SYNC_WAIT_ON_ADDRESS_NONE); }
void kern_unpark_one(_Atomic(uint32_t) *w) { (void)os_sync_wake_by_address_any(w, sizeof *w, OS_SYNC_WAKE_BY_ADDRESS_NONE); }
void kern_unpark_all(_Atomic(uint32_t) *w) { (void)os_sync_wake_by_address_all(w, sizeof *w, OS_SYNC_WAKE_BY_ADDRESS_NONE); }
// kern_park_ns is kern_park that also returns after ns nanoseconds: 0 only
// when the time elapsed, 1 for every other return (a wake, a value that
// already differs, an interruption).
int32_t kern_park_ns(_Atomic(uint32_t) *w, uint32_t expect, uint64_t ns) {
    if (ns == 0) return atomic_load_explicit(w, memory_order_relaxed) != expect; // a timeout of 0 means "forever" to the system call
    int r = os_sync_wait_on_address_with_timeout(w, expect, sizeof *w, OS_SYNC_WAIT_ON_ADDRESS_NONE, OS_CLOCK_MACH_ABSOLUTE_TIME, ns);
    return !(r < 0 && errno == ETIMEDOUT);
}
#elif defined(__linux__)
#include <linux/futex.h>
#include <sys/syscall.h>
#include <unistd.h>
void kern_park(_Atomic(uint32_t) *w, uint32_t expect) { (void)syscall(SYS_futex, w, FUTEX_WAIT_PRIVATE, expect, NULL, NULL, 0); }
void kern_unpark_one(_Atomic(uint32_t) *w) { (void)syscall(SYS_futex, w, FUTEX_WAKE_PRIVATE, 1, NULL, NULL, 0); }
void kern_unpark_all(_Atomic(uint32_t) *w) { (void)syscall(SYS_futex, w, FUTEX_WAKE_PRIVATE, INT32_MAX, NULL, NULL, 0); }
// kern_park_ns is kern_park that also returns after ns nanoseconds: 0 only
// when the time elapsed, 1 for every other return (a wake, a value that
// already differs, an interruption).
int32_t kern_park_ns(_Atomic(uint32_t) *w, uint32_t expect, uint64_t ns) {
    if (ns == 0) return atomic_load_explicit(w, memory_order_relaxed) != expect; // nothing to wait for: no system call
    struct timespec ts = { (time_t)(ns / 1000000000ull), (long)(ns % 1000000000ull) };
    long r = syscall(SYS_futex, w, FUTEX_WAIT_PRIVATE, expect, &ts, NULL, 0);
    return !(r < 0 && errno == ETIMEDOUT);
}
#else
#error "kern: no park primitive for this platform (os_sync_wait_on_address on macOS 14.4+, futex on Linux)"
#endif

struct kern_mutex { _Atomic(uint32_t) state; _Atomic(uintptr_t) owner; int64_t size; char data[]; };
_Static_assert(offsetof(kern_mutex, owner) == 8 && offsetof(kern_mutex, size) == 16 && offsetof(kern_mutex, data) == 24, "the state and the owner share the first 16 bytes (specs/perf-gaps/SPEC.md R6)");
static inline uintptr_t kern_self(void) { return (uintptr_t)pthread_self(); }
void *kern_mutex_new(int64_t size, const void *src, const kern_loc *at) {
    kern_mutex *mx = kern_alloc(sizeof *mx + (size_t)size, at);
    atomic_init(&mx->state, 0);
    atomic_init(&mx->owner, 0);
    mx->size = size;
    kern_copy(mx->data, src, (size_t)size);
    return mx;
}
void kern_mutex_try_new(int64_t size, void *src, void (*glue)(void *), char *out, int64_t off, const kern_loc *at) {
    kern_mutex *mx = kern_try_alloc(sizeof *mx + (size_t)size, at);
    if (mx == NULL) { kern_try_failed(glue, src, out); return; }
    atomic_init(&mx->state, 0);
    atomic_init(&mx->owner, 0);
    mx->size = size;
    kern_copy(mx->data, src, (size_t)size);
    out[0] = 0;
    memcpy(out + off, &mx, sizeof mx);
}
// kern_mutex_acquire takes a lock whose fast path failed: it exchanges
// the state to 2 (acquiring at once when it was free) and parks while it
// is 2. A thread that parked cannot know whether another is parked, so it
// acquires as 2 and its release wakes one more.
static void kern_mutex_acquire(kern_mutex *mx) {
    while (atomic_exchange_explicit(&mx->state, 2, memory_order_acquire) != 0) kern_park(&mx->state, 2);
}
void *kern_mutex_lock(kern_mutex *mx, const kern_loc *at) {
    uintptr_t self = kern_self();
    uint32_t s = 0;
    if (!atomic_compare_exchange_strong_explicit(&mx->state, &s, 1, memory_order_acquire, memory_order_relaxed)) {
        if (atomic_load_explicit(&mx->owner, memory_order_relaxed) == self) kern_trap_loc(11, at); // recursive acquisition
        kern_mutex_acquire(mx);
    }
    atomic_store_explicit(&mx->owner, self, memory_order_relaxed);
    return mx->data;
}
static inline void kern_mutex_release(kern_mutex *mx) {
    atomic_store_explicit(&mx->owner, 0, memory_order_relaxed);
    if (atomic_exchange_explicit(&mx->state, 0, memory_order_release) == 2) kern_unpark_one(&mx->state);
}
void kern_mutex_unlock(kern_mutex *mx) { kern_mutex_release(mx); }
// kern_mutex_try_lock acquires the lock only if it is free: 1, or 0 when
// it is held by any thread, the caller included (no trap).
int32_t kern_mutex_try_lock(kern_mutex *mx) {
    uint32_t s = 0;
    if (!atomic_compare_exchange_strong_explicit(&mx->state, &s, 1, memory_order_acquire, memory_order_relaxed)) return 0;
    atomic_store_explicit(&mx->owner, kern_self(), memory_order_relaxed);
    return 1;
}
// kern_mutex_data is the guarded value's address, for a caller that holds
// the lock.
void *kern_mutex_data(kern_mutex *mx) { return mx->data; }
void kern_mutex_drop(kern_mutex *mx, kern_glue g) { if (mx == NULL) return; if (g) g(mx->data); kern_obj_free(mx); }

// ---- Condvar (spec §5.5) ----

// wait is given the guard a Mutex lock returned (the checker requires it to
// be that edit, unprojected), which is the Mutex's data pointer. A wait by a
// thread that does not hold the lock, or with a different Mutex from the
// one the Condvar's first wait used, traps with kind explicit, as the
// interpreter does. The Condvar is a sequence number: a waiter reads it
// under the lock, releases the Mutex and parks while the number is
// unchanged; a notify bumps it and wakes one or every parked thread,
// which then reacquire the Mutex. A notify between the read and the
// park is not lost (the park returns at once), a return without a
// notify is a spurious wakeup, which the spec permits, and a multiple
// of 2^32 notifies between the read and the park is the accepted
// limitation of every 32-bit sequence (specs/perf-gaps/SPEC.md §2).
struct kern_condvar { _Atomic(uint32_t) seq; _Atomic(uintptr_t) mutex; };
void *kern_condvar_new(const kern_loc *at) {
    kern_condvar *cv = kern_alloc(sizeof *cv, at);
    atomic_init(&cv->seq, 0);
    atomic_init(&cv->mutex, 0);
    return cv;
}
void kern_condvar_try_new(char *out, int64_t off, const kern_loc *at) {
    kern_condvar *cv = kern_try_alloc(sizeof *cv, at);
    if (cv == NULL) { out[0] = 1; return; }
    atomic_init(&cv->seq, 0);
    atomic_init(&cv->mutex, 0);
    out[0] = 0;
    memcpy(out + off, &cv, sizeof cv);
}
// kern_condvar_enter checks a wait's guard and binds the Condvar to its
// Mutex, trapping explicit for a lock this thread does not hold and for a
// second Mutex on one Condvar; it returns the Mutex.
static kern_mutex *kern_condvar_enter(kern_condvar *cv, void *data, uintptr_t self, const kern_loc *at) {
    kern_mutex *mx = (kern_mutex *)((char *)data - offsetof(kern_mutex, data));
    if (atomic_load_explicit(&mx->owner, memory_order_relaxed) != self) kern_trap_loc(11, at); // the lock is not held by this thread
    uintptr_t bound = atomic_load_explicit(&cv->mutex, memory_order_relaxed), want = 0;
    if (bound != (uintptr_t)mx && !atomic_compare_exchange_strong(&cv->mutex, &want, (uintptr_t)mx) && want != (uintptr_t)mx) kern_trap_loc(11, at); // a second Mutex on one Condvar
    return mx;
}
void kern_condvar_wait(kern_condvar *cv, void *data, const kern_loc *at) {
    uintptr_t self = kern_self();
    kern_mutex *mx = kern_condvar_enter(cv, data, self, at);
    uint32_t seq = atomic_load_explicit(&cv->seq, memory_order_relaxed);
    kern_mutex_release(mx);
    kern_park(&cv->seq, seq);
    kern_mutex_acquire(mx);
    atomic_store_explicit(&mx->owner, self, memory_order_relaxed);
}
// kern_condvar_wait_ns is kern_condvar_wait that also returns when ns
// nanoseconds have passed: 0 only when the time elapsed, else 1; the lock
// is held again in either case.
int32_t kern_condvar_wait_ns(kern_condvar *cv, void *data, uint64_t ns, const kern_loc *at) {
    uintptr_t self = kern_self();
    kern_mutex *mx = kern_condvar_enter(cv, data, self, at);
    uint32_t seq = atomic_load_explicit(&cv->seq, memory_order_relaxed);
    kern_mutex_release(mx);
    int32_t woken = kern_park_ns(&cv->seq, seq, ns);
    kern_mutex_acquire(mx);
    atomic_store_explicit(&mx->owner, self, memory_order_relaxed);
    return woken;
}
void kern_condvar_notify_one(kern_condvar *cv) { atomic_fetch_add_explicit(&cv->seq, 1, memory_order_relaxed); kern_unpark_one(&cv->seq); }
void kern_condvar_notify_all(kern_condvar *cv) { atomic_fetch_add_explicit(&cv->seq, 1, memory_order_relaxed); kern_unpark_all(&cv->seq); }
void kern_condvar_drop(kern_condvar *cv) { kern_obj_free(cv); }
