// kern hosted runtime: allocation for runtime objects, container identity
// tags and the system allocator.
// Included by kern_rt.c; see kern_rt.h for the entries.

// ---- allocation failure injection ----

// kern_fail_next is armed by testing.fail_next_alloc: the next allocation
// (a runtime object's, or the system allocator's, which the kern containers
// use) fails.
static _Atomic int kern_fail_next = 0;
#ifdef KERN_TEST_HOOKS
void kern_test_fail_next_alloc(void) { atomic_store(&kern_fail_next, 1); }
// kern_test_require_hooks exists only in a --test-hooks build: a program
// using the kern-side test hooks without it fails to link.
void kern_test_require_hooks(void) {}
#endif
// Every entry point below that can raise a trap takes the location of the
// user-level call last, as a pointer to a record the backend emits once
// per call site (a constant, or a stack slot inside a library function,
// whose location is its caller's), and raises its traps there, so that a
// runtime-raised trap names that call as the interpreter does
// (specs/runtime-trap-file/SPEC.md). One pointer costs what the two span
// immediates of the old kern_mutex_lock did; three arguments cost the
// uncontended lock four instructions. A null record is no location: the
// span 0-0 and the program's file. A null file in a record is the
// program's file with the record's span, as a null file is for kern_trap
// (a function without a file of its own; the interpreter reports the
// same).
static _Noreturn void kern_trap_loc(int32_t kind, const kern_loc *at) {
    if (at == NULL) kern_trap(kind, 0, 0, NULL);
    kern_trap(kind, at->start, at->end, at->file);
}
static void *kern_alloc(size_t n, const kern_loc *at) {
    if (atomic_exchange(&kern_fail_next, 0)) kern_trap_loc(9, at);
    void *p = malloc(n ? n : 1);
    if (p == NULL) kern_trap_loc(9, at);
    return p;
}
// kern_try_alloc is kern_alloc returning NULL instead of trapping (the
// try_new forms, specs/fallible-runtime-objects); the armed failure of
// testing.fail_next_alloc makes it fail.
static void *kern_try_alloc(size_t n) {
    if (atomic_exchange(&kern_fail_next, 0)) return NULL;
    return malloc(n ? n : 1);
}
// kern_try_failed writes Err(OutOfMemory) into a try_new's result and
// destroys the value the caller moved in, which no object took.
static void kern_try_failed(void (*glue)(void *), void *src, char *out) {
    if (glue != NULL) glue(src);
    out[0] = 1;
}

// ---- container identity tags (spec §5.6) ----

// kern_next_identity issues the next container identity tag. Tags are a
// debug-build feature (PLAN.md 2.2): a release build issues identity 0,
// which disables the check; so does the counter once it wraps to 0.
static _Atomic uint32_t kern_identity = 1;
uint32_t kern_next_identity(void) {
#ifdef KERN_RELEASE
    return 0;
#else
    return atomic_fetch_add(&kern_identity, 1); // 0 after wrap-around: unchecked
#endif
}

// ---- SystemAllocator (spec §6) ----

struct kern_sysalloc { int unused; };
void *kern_sysalloc_new(void) { static kern_sysalloc a; return &a; }
void kern_sysalloc_alloc(kern_sysalloc *a, int64_t size, int64_t align, char *out, int64_t off, const kern_loc *at) {
    (void)a;
    if (size < 0 || align <= 0 || (align & (align - 1)) != 0 || align > 4096 || size % align != 0) kern_trap_loc(11, at);
    if (atomic_exchange(&kern_fail_next, 0)) { out[0] = 1; return; } // the armed failure: Err(OutOfMemory)
    // aligned_alloc needs an alignment of at least sizeof(void *) and a size
    // that is a multiple of it (macOS rejects smaller alignments): the
    // request is rounded up; the §6 obligations above are the caller's values
    size_t al = (size_t)align < sizeof(void *) ? sizeof(void *) : (size_t)align;
    if ((uint64_t)size > SIZE_MAX - (al - 1)) { out[0] = 1; return; } // unrepresentable after rounding
    size_t sz = ((size_t)size + al - 1) / al * al;
    void *p = aligned_alloc(al, sz ? sz : al);
    if (p == NULL) { out[0] = 1; return; }
    out[0] = 0;
    memcpy(out + off, &p, sizeof p);
}
// kern_sysalloc_realloc resizes an allocation in place when the allocator
// can (realloc, whose result is 16-byte aligned on every supported host),
// else by aligned_alloc, copy and free; on failure the old allocation stays.
void kern_sysalloc_realloc(kern_sysalloc *a, void *p, int64_t old_size, int64_t size, int64_t align, char *out, int64_t off, const kern_loc *at) {
    (void)a;
    if (old_size < 0 || size < 0 || align <= 0 || (align & (align - 1)) != 0 || align > 4096 || size % align != 0) kern_trap_loc(11, at);
    if (atomic_exchange(&kern_fail_next, 0)) { out[0] = 1; return; }
    size_t al = (size_t)align < sizeof(void *) ? sizeof(void *) : (size_t)align;
    if ((uint64_t)size > SIZE_MAX - (al - 1)) { out[0] = 1; return; }
    size_t sz = ((size_t)size + al - 1) / al * al;
    if (sz == 0) sz = al;
    void *np;
    if (al <= 16) np = realloc(p, sz);
    else {
        np = aligned_alloc(al, sz);
        if (np) { kern_copy(np, p, (size_t)(old_size < size ? old_size : size)); free(p); }
    }
    if (np == NULL) { out[0] = 1; return; }
    out[0] = 0;
    memcpy(out + off, &np, sizeof np);
}
// kern_sysalloc_alloc_zeroed is kern_sysalloc_alloc with zeroed bytes:
// calloc (16-byte aligned on every supported host, and a large block comes
// from fresh zero pages without a memset), else aligned_alloc and memset.
void kern_sysalloc_alloc_zeroed(kern_sysalloc *a, int64_t size, int64_t align, char *out, int64_t off, const kern_loc *at) {
    (void)a;
    if (size < 0 || align <= 0 || (align & (align - 1)) != 0 || align > 4096 || size % align != 0) kern_trap_loc(11, at);
    if (atomic_exchange(&kern_fail_next, 0)) { out[0] = 1; return; }
    size_t al = (size_t)align < sizeof(void *) ? sizeof(void *) : (size_t)align;
    if ((uint64_t)size > SIZE_MAX - (al - 1)) { out[0] = 1; return; }
    size_t sz = ((size_t)size + al - 1) / al * al;
    if (sz == 0) sz = al;
    void *p;
    if (al <= 16) p = calloc(1, sz);
    else {
        p = aligned_alloc(al, sz);
        if (p) memset(p, 0, sz);
    }
    if (p == NULL) { out[0] = 1; return; }
    out[0] = 0;
    memcpy(out + off, &p, sizeof p);
}
void kern_sysalloc_free(kern_sysalloc *a, void *p, int64_t size, int64_t align, const kern_loc *at) { (void)a; (void)size; (void)align; (void)at; free(p); }
