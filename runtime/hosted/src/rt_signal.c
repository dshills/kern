// kern hosted runtime: std/signal (specs/runtime RI1).
// Included by kern_rt.c; see kern_rt.h for the entries.
//
// A caught signal is recorded, not acted on: the handler sets the signal's
// bit and writes one byte to a pipe, both of which a handler may do. A
// wait looks at the bits and, finding none it wants, polls the pipe's
// read end, which the byte makes readable. Deliveries of one signal while
// its bit is set are one. The pipe exists from the first catch on; a
// program that catches nothing has no descriptor for it.
//
// The eight signals are numbered as std/signal's Signal declares them.

#define KERN_SIGNALS 8
static const int kern_signals[KERN_SIGNALS] = {SIGINT, SIGTERM, SIGHUP, SIGQUIT, SIGUSR1, SIGUSR2, SIGPIPE, SIGCHLD};
static _Atomic uint32_t kern_sig_pending; // a bit per signal recorded and not yet returned by a wait
static _Atomic uint32_t kern_sig_held;    // a bit per signal the live Signals has caught
static _Atomic int kern_sig_live;         // a Signals exists
static struct sigaction kern_sig_saved[KERN_SIGNALS];
static int kern_sig_pipe[2] = {-1, -1};

static void kern_sig_record(int sig) {
    int saved = errno;
    for (int i = 0; i < KERN_SIGNALS; i++) {
        if (kern_signals[i] == sig) atomic_fetch_or(&kern_sig_pending, 1u << i);
    }
    char b = 0;
    // a full pipe already says "look at the bits"
    while (write(kern_sig_pipe[1], &b, 1) < 0 && errno == EINTR) {}
    errno = saved;
}

// kern_signal_open claims the one Signals a program may have at a time
// and makes the pipe on first use: 0, -EBUSY when one is live, or -errno.
int32_t kern_signal_open(void) {
    int idle = 0;
    if (!atomic_compare_exchange_strong(&kern_sig_live, &idle, 1)) return -EBUSY;
    if (kern_sig_pipe[0] < 0) {
        int p[2];
        if (pipe(p) != 0) { int e = errno; atomic_store(&kern_sig_live, 0); return -e; }
        for (int i = 0; i < 2; i++) {
            // above 2, close-on-exec and non-blocking: the handler must
            // never wait, and a wait drains without waiting
            int fd = p[i] < 3 ? fcntl(p[i], F_DUPFD_CLOEXEC, 3) : p[i];
            if (fd != p[i]) close(p[i]);
            if (fd < 0 || fcntl(fd, F_SETFD, FD_CLOEXEC) < 0 || fcntl(fd, F_SETFL, fcntl(fd, F_GETFL) | O_NONBLOCK) < 0) {
                int e = errno;
                if (fd >= 0) close(fd);
                if (i == 1) close(p[0]); else close(p[1]);
                atomic_store(&kern_sig_live, 0);
                return -e;
            }
            p[i] = fd;
        }
        kern_sig_pipe[0] = p[0];
        kern_sig_pipe[1] = p[1];
    }
    return 0;
}
// kern_signal_catch makes the runtime record deliveries of a signal,
// remembering the disposition it had: 0 (also when it is already held),
// or -errno with nothing changed.
int32_t kern_signal_catch(int32_t which) {
    if (which < 0 || which >= KERN_SIGNALS) return -EINVAL;
    uint32_t bit = 1u << which;
    if (atomic_load(&kern_sig_held) & bit) return 0;
    struct sigaction sa;
    memset(&sa, 0, sizeof sa);
    sa.sa_handler = kern_sig_record;
    sigemptyset(&sa.sa_mask);
    sa.sa_flags = SA_RESTART | SA_ONSTACK; // a system call that can be restarted is; the rest are retried by their callers
    if (sigaction(kern_signals[which], &sa, &kern_sig_saved[which]) != 0) return -errno;
    atomic_fetch_or(&kern_sig_held, bit);
    return 0;
}
// kern_signal_close gives every held signal the disposition it had when
// it was caught, discards what was recorded and frees the claim.
void kern_signal_close(void) {
    uint32_t held = atomic_exchange(&kern_sig_held, 0);
    for (int i = 0; i < KERN_SIGNALS; i++) {
        if (held & (1u << i)) (void)sigaction(kern_signals[i], &kern_sig_saved[i], NULL);
    }
    atomic_store(&kern_sig_pending, 0);
    char drain[64];
    while (kern_sig_pipe[0] >= 0 && read(kern_sig_pipe[0], drain, sizeof drain) > 0) {}
    atomic_store(&kern_sig_live, 0);
}
// kern_signal_set makes a signal ignored (how 1) or gives it the system's
// default (how 0): 0, -EBUSY for a signal the live Signals holds, or
// -errno with nothing changed.
int32_t kern_signal_set(int32_t which, int32_t how) {
    if (which < 0 || which >= KERN_SIGNALS || how < 0 || how > 1) return -EINVAL;
    if (atomic_load(&kern_sig_held) & (1u << which)) return -EBUSY;
    struct sigaction sa;
    memset(&sa, 0, sizeof sa);
    sa.sa_handler = how == 1 ? SIG_IGN : SIG_DFL;
    sigemptyset(&sa.sa_mask);
    return sigaction(kern_signals[which], &sa, NULL) != 0 ? -errno : 0;
}
// kern_signal_wait returns the first signal of mask, in Signal's order,
// that has been recorded, and forgets that record. It waits until there
// is one, or for ns nanoseconds when timed: then -2^32.
int64_t kern_signal_wait(uint32_t mask, uint64_t ns, uint8_t timed) {
    uint64_t start = timed ? kern_now_ns() : 0;
    for (;;) {
        uint32_t pending = atomic_load(&kern_sig_pending) & mask;
        for (int i = 0; pending != 0 && i < KERN_SIGNALS; i++) {
            uint32_t bit = 1u << i;
            // taken only if still set: another thread cannot hold this
            // Signals, but the handler sets bits at any moment
            if ((pending & bit) && (atomic_fetch_and(&kern_sig_pending, ~bit) & bit)) return i;
        }
        int ms = -1;
        if (timed) {
            uint64_t spent = kern_now_ns() - start;
            if (spent >= ns) return KERN_TIMED_OUT;
            uint64_t left = ns - spent;
            uint64_t whole = left / 1000000u + (left % 1000000u != 0);
            ms = whole > (uint64_t)INT_MAX ? INT_MAX : (int)whole;
        }
        struct pollfd p = {kern_sig_pipe[0], POLLIN, 0};
        if (poll(&p, 1, ms) > 0) {
            char drain[64];
            while (read(kern_sig_pipe[0], drain, sizeof drain) > 0) {}
        }
        // a signal, an interruption or the end of the wait: look again
    }
}
// kern_signal_raise sends a signal to the program's own process: 0 or
// -errno.
int32_t kern_signal_raise(int32_t which) {
    if (which < 0 || which >= KERN_SIGNALS) return -EINVAL;
    return kill(getpid(), kern_signals[which]) != 0 ? -errno : 0;
}
