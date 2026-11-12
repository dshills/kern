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
//
// Standard output is buffered (specs/runtime RB2): bytes reach descriptor 1
// in the order written, when the buffer fills, after a write that holds a
// newline when the descriptor is a terminal, before the program reads
// standard input, before a child inherits the stream, on
// io.flush_stdout, and when the program ends. Standard error is not
// buffered.

static char kern_outbuf[65536];
static size_t kern_outlen = 0;
static pthread_mutex_t kern_outlock = PTHREAD_MUTEX_INITIALIZER;
// kern_out_tty: descriptor 1 is a terminal (set once by kern_start)
static int kern_out_tty = 0;
// kern_out_err is the first failure of a write to standard output since it
// was last taken (RB3): io.flush_stdout returns and clears it
static _Atomic int kern_out_err = 0;

// kern_write_all writes the n bytes at p to fd (RB3): an interrupted write
// is retried, a short one continued, and a descriptor that is non-blocking
// and not ready is waited for. Any other failure ends the write, dropping
// the remaining bytes, and is returned (0 otherwise). It uses no lock and
// no allocation, so the trap path may call it in signal context.
static int kern_write_all(int fd, const char *p, size_t n) {
    while (n > 0) {
        ssize_t w = write(fd, p, n);
        if (w > 0) { p += w; n -= (size_t)w; continue; }
        if (w < 0 && errno == EINTR) continue;
        if (w < 0 && (errno == EAGAIN || errno == EWOULDBLOCK)) {
            struct pollfd pf = {fd, POLLOUT, 0};
            if (poll(&pf, 1, -1) < 0 && errno != EINTR) return errno;
            continue;
        }
        return w < 0 ? errno : EIO; // a write of 0 bytes for n > 0 makes no progress
    }
    return 0;
}
// kern_out_write writes to standard output and records the first failure.
static void kern_out_write(const char *p, size_t n) {
    int e = kern_write_all(1, p, n);
    if (e != 0) {
        int none = 0;
        atomic_compare_exchange_strong(&kern_out_err, &none, e);
    }
}
static void kern_flush_locked(void) { kern_out_write(kern_outbuf, kern_outlen); kern_outlen = 0; }
static void kern_flush(void) { pthread_mutex_lock(&kern_outlock); kern_flush_locked(); pthread_mutex_unlock(&kern_outlock); }
// kern_flush_try flushes without blocking: for trap paths that may run on a
// thread interrupted while holding the output lock
static void kern_flush_try(void) {
    if (pthread_mutex_trylock(&kern_outlock) == 0) { kern_flush_locked(); pthread_mutex_unlock(&kern_outlock); }
    else kern_out_write(kern_outbuf, kern_outlen);
}
static void kern_out(const char *p, size_t n) {
    if (n == 0) return; // an empty sequence may carry a null pointer (kern_copy)
    pthread_mutex_lock(&kern_outlock);
    if (n > sizeof kern_outbuf) { kern_flush_locked(); kern_out_write(p, n); }
    else {
        if (kern_outlen + n > sizeof kern_outbuf) kern_flush_locked();
        memcpy(kern_outbuf + kern_outlen, p, n);
        kern_outlen += n;
        // a terminal sees each line as it is written
        if (kern_out_tty && memchr(p, '\n', n) != NULL) kern_flush_locked();
    }
    pthread_mutex_unlock(&kern_outlock);
}
// kern_io_flush_stdout flushes standard output and takes the recorded
// failure: 0, or -errno of the first failed write since the last call.
int32_t kern_io_flush_stdout(void) {
    kern_flush();
    return -atomic_exchange(&kern_out_err, 0);
}
// kern_io_is_terminal reports whether a standard stream (0, 1 or 2) is a
// terminal: 1 or 0.
int32_t kern_io_is_terminal(int32_t stream) { return stream >= 0 && stream <= 2 && isatty(stream) ? 1 : 0; }
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
void eprint_str(const char *p, int64_t n) { if (n > 0) (void)kern_write_all(2, p, (size_t)n); }
void println(void) { kern_out("\n", 1); }
static void kern_alloc_report(void); // rt_alloc.c: the report of KERN_ALLOC_REPORT=1 (RF2)
void kern_exit(int32_t code) { kern_flush(); kern_alloc_report(); _exit(code); }

// ---- configuration (specs/runtime RB1) ----
//
// The runtime's environment variables, read once by kern_start before the
// program runs. A process running with elevated privilege ignores them
// all. A variable set to a value outside its accepted form is ignored and
// named in one line on standard error; an unset or empty one is neither.
static struct {
    int backtrace;        // KERN_BACKTRACE: -1 unset, 0 or 1
    int alloc_report;     // KERN_ALLOC_REPORT: 0 or 1
    uint64_t stack_bytes; // KERN_STACK_BYTES: 0 unset, else 65536 to 2^30
} kern_cfg = {-1, 0, 0};

#define KERN_STACK_MIN 65536ull
#define KERN_STACK_MAX 1073741824ull

// kern_privileged reports a set-user-ID or set-group-ID process.
static int kern_privileged(void) {
#ifdef KERN_TEST_HOOKS
    // the test hook: there is no other way to run the branch without privilege
    const char *t = getenv("KERN_TEST_PRIVILEGED");
    if (t != NULL && t[0] == '1' && t[1] == 0) return 1;
#endif
#if defined(__linux__)
    return getauxval(AT_SECURE) != 0;
#else
    return issetugid() != 0;
#endif
}
static void kern_cfg_ignored(const char *name) {
    char b[96]; size_t n = 0;
    const char *head = "{\"runtime\":{\"ignored\":\"", *tail = "\"}}\n";
    size_t hl = strlen(head), nl = strlen(name), tl = strlen(tail);
    if (hl + nl + tl > sizeof b) return;
    memcpy(b, head, hl); n = hl;
    memcpy(b + n, name, nl); n += nl;
    memcpy(b + n, tail, tl); n += tl;
    (void)kern_write_all(2, b, n);
}
// kern_cfg_flag reads a variable whose values are exactly 0 and 1 into *v.
static void kern_cfg_flag(const char *name, int *v) {
    const char *s = getenv(name);
    if (s == NULL || s[0] == 0) return;
    if ((s[0] == '0' || s[0] == '1') && s[1] == 0) { *v = s[0] - '0'; return; }
    kern_cfg_ignored(name);
}
// kern_cfg_bytes reads a decimal integer of 1 to 10 digits in [lo, hi].
static void kern_cfg_bytes(const char *name, uint64_t lo, uint64_t hi, uint64_t *v) {
    const char *s = getenv(name);
    if (s == NULL || s[0] == 0) return;
    uint64_t x = 0;
    size_t i = 0;
    for (; s[i] >= '0' && s[i] <= '9' && i < 10; i++) x = x * 10 + (uint64_t)(s[i] - '0');
    if (s[i] != 0 || x < lo || x > hi) { kern_cfg_ignored(name); return; }
    *v = x;
}
static void kern_config_init(void) {
    if (kern_privileged()) return;
    kern_cfg_flag("KERN_BACKTRACE", &kern_cfg.backtrace);
    kern_cfg_bytes("KERN_STACK_BYTES", KERN_STACK_MIN, KERN_STACK_MAX, &kern_cfg.stack_bytes);
    kern_cfg_flag("KERN_ALLOC_REPORT", &kern_cfg.alloc_report);
}
