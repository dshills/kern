// kern hosted runtime: standard output, the output buffer and exit.
// Included by kern_rt.c; see kern_rt.h for the entries.

// kern_copy is memcpy for bytes that come from compiled kern code: a
// sequence of length 0 may carry a null pointer, which memcpy must not be
// given even with a length of 0 (found by the sanitized fixture run,
// specs/runtime RA5).
static inline void kern_copy(void *dst, const void *src, size_t n) {
    if (n != 0) memcpy(dst, src, n);
}

// ---- output (write(2)-based so the trap path never touches stdio) ----

static char kern_outbuf[65536];
static size_t kern_outlen = 0;
static pthread_mutex_t kern_outlock = PTHREAD_MUTEX_INITIALIZER;

static void kern_write_all(int fd, const char *p, size_t n) {
    while (n > 0) {
        ssize_t w = write(fd, p, n);
        if (w <= 0) return;
        p += w; n -= (size_t)w;
    }
}
static void kern_flush_locked(void) { kern_write_all(1, kern_outbuf, kern_outlen); kern_outlen = 0; }
static void kern_flush(void) { pthread_mutex_lock(&kern_outlock); kern_flush_locked(); pthread_mutex_unlock(&kern_outlock); }
// kern_flush_try flushes without blocking: for trap paths that may run on a
// thread interrupted while holding the output lock
static void kern_flush_try(void) {
    if (pthread_mutex_trylock(&kern_outlock) == 0) { kern_flush_locked(); pthread_mutex_unlock(&kern_outlock); }
    else kern_write_all(1, kern_outbuf, kern_outlen);
}
static void kern_out(const char *p, size_t n) {
    if (n == 0) return; // an empty sequence may carry a null pointer (kern_copy)
    pthread_mutex_lock(&kern_outlock);
    if (n > sizeof kern_outbuf) { kern_flush_locked(); kern_write_all(1, p, n); }
    else {
        if (kern_outlen + n > sizeof kern_outbuf) kern_flush_locked();
        memcpy(kern_outbuf + kern_outlen, p, n);
        kern_outlen += n;
    }
    pthread_mutex_unlock(&kern_outlock);
}
static size_t kern_fmt_u64(char *dst, uint64_t v) {
    char tmp[24]; size_t n = 0;
    do { tmp[n++] = (char)('0' + v % 10); v /= 10; } while (v);
    for (size_t i = 0; i < n; i++) dst[i] = tmp[n - 1 - i];
    return n;
}
void print_u64(uint64_t v) { char b[24]; kern_out(b, kern_fmt_u64(b, v)); }
void print_i64(int64_t v) {
    char b[25]; size_t n = 0;
    if (v < 0) { b[n++] = '-'; n += kern_fmt_u64(b + n, (uint64_t)0 - (uint64_t)v); }
    else n = kern_fmt_u64(b, (uint64_t)v);
    kern_out(b, n);
}
void print_bool(uint8_t v) { if (v) kern_out("true", 4); else kern_out("false", 5); }
void print_str(const char *p, int64_t n) { kern_out(p, (size_t)n); }
void eprint_str(const char *p, int64_t n) { kern_write_all(2, p, (size_t)n); }
void println(void) { kern_out("\n", 1); }
void kern_exit(int32_t code) { kern_flush(); _exit(code); }
