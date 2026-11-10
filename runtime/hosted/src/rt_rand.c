// kern hosted runtime: std/rand's source of random bytes (specs/runtime
// RI2).
// Included by kern_rt.c; see kern_rt.h for the entries.

// kern_rand_fail is armed by testing.fail_random(after): the request of
// that number, counting from 1, fails; 0 when not armed.
static _Atomic int64_t kern_rand_fail = 0;
#ifdef KERN_TEST_HOOKS
void kern_test_fail_random(uint32_t after) { atomic_store(&kern_rand_fail, (int64_t)after + 1); }
#endif

// kern_rand_fill fills n bytes from the system's generator, which serves
// at most 256 bytes a request: 0, or -errno. A failure after earlier
// requests is the call's failure, and the buffer's contents are then
// unspecified.
int32_t kern_rand_fill(char *buf, int64_t n) {
    while (n > 0) {
        size_t k = n < 256 ? (size_t)n : 256;
        if (atomic_load(&kern_rand_fail) > 0 && atomic_fetch_sub(&kern_rand_fail, 1) == 1) return -EIO;
        if (getentropy(buf, k) != 0) return -errno;
        buf += k;
        n -= (int64_t)k;
    }
    return 0;
}
