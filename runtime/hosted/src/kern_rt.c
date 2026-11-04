// kern hosted runtime (PLAN.md 2.2): traps, output, the runtime-provided
// library objects (Shared, Mutex), threads, the system allocator and
// the test hooks. Linked by `kern build` with every program. The containers
// (Box, Vec, Pool, Arena) are kern code in std/core.kern.
#ifndef _GNU_SOURCE
#define _GNU_SOURCE // pthread_getattr_np on glibc
#endif
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <signal.h>
#include <pthread.h>
#include <stdatomic.h>
#include <unistd.h>
#include <time.h>
#include <sched.h>
#include <fcntl.h>
#include <errno.h>
#include <dirent.h>
#include <sys/stat.h>
#include <sys/wait.h>
#include <poll.h>
#include <limits.h>
#include <spawn.h>
#if defined(__linux__)
#include <features.h>
// posix_spawn_file_actions_addclosefrom_np keeps every descriptor above 2
// out of a child (specs/std-process R4); glibc has it since 2.34
#if !defined(__GLIBC__) || !__GLIBC_PREREQ(2, 34)
#error "kern's hosted runtime needs glibc 2.34 or newer on Linux (specs/std-process)"
#endif
#endif
#ifdef __APPLE__
#include <sys/ucontext.h>
#else
#include <ucontext.h>
#endif

typedef void (*kern_glue)(void *);

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
#ifdef __APPLE__
#define KERN_CLOCK CLOCK_MONOTONIC_RAW // CLOCK_MONOTONIC has microsecond resolution on macOS
#else
#define KERN_CLOCK CLOCK_MONOTONIC
#endif
void kern_exit(int32_t code) { kern_flush(); _exit(code); }

// ---- std/io (hosted): descriptor-level file access ----
// Errors are negative errno values; the kern side maps them to IoErr.
// Descriptor 1 goes through the output buffer so that it interleaves with
// print_* as written.

// kern_std_closed records which of descriptors 0, 1 and 2 were closed
// when the program started. kern_start fills each such slot with a
// close-on-exec placeholder on /dev/null before any thread exists, so that
// no descriptor the program opens later lands there: a spawn on one thread
// can then never mistake a file another thread is opening for a standard
// stream, and no lock is needed (specs/std-process R4). A child still gets
// such a stream closed. The program's own reads of it see end of file and
// its writes succeed, as in the interpreter, where Go's runtime reopens
// closed standard descriptors on /dev/null.
static int kern_std_closed[3];
static void kern_std_init(void) {
    for (int i = 0; i < 3; i++) {
        if (fcntl(i, F_GETFD) >= 0) continue;
        kern_std_closed[i] = 1;
        int fd = open("/dev/null", O_RDWR | O_CLOEXEC); // the lowest free slot, i itself
        if (fd >= 0 && fd != i) {
            dup2(fd, i);
            close(fd);
        }
        fcntl(i, F_SETFD, FD_CLOEXEC);
    }
}
// kern_open_above2 opens path (flags include O_CLOEXEC) and returns a
// descriptor numbered 3 or more, or -errno: a descriptor below 3 arises
// only when the program closed a standard one itself.
static int kern_open_above2(const char *path, int flags, mode_t mode) {
    int fd = open(path, flags, mode);
    if (fd < 0) return -errno;
    if (fd < 3) {
        int d = fcntl(fd, F_DUPFD_CLOEXEC, 3);
        int e = errno;
        close(fd);
        if (d < 0) return -e;
        fd = d;
    }
    return fd;
}

static int kern_argc = 0;
static char **kern_argv = NULL;
void kern_set_args(int32_t argc, char **argv) { kern_argc = argc; kern_argv = argv; }
int32_t kern_io_open(const char *p, int64_t n, uint8_t write) {
    char path[4096];
    if (n < 0 || (size_t)n >= sizeof path) return -ENAMETOOLONG;
    if (n > 0 && memchr(p, 0, (size_t)n) != NULL) return -EINVAL; // an embedded NUL would cut the path
    memcpy(path, p, (size_t)n);
    path[n] = 0;
    // close-on-exec from the start, so that no child inherits the file
    // (specs/std-process R6), and above 2: when a standard stream was
    // closed at startup, keep opened files above the descriptors the
    // output buffer and the kern side treat specially
    return kern_open_above2(path, write ? O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC : O_RDONLY | O_CLOEXEC, 0644);
}
int64_t kern_io_read(int32_t fd, char *buf, int64_t n) {
    ssize_t r = read(fd, buf, (size_t)n);
    return r < 0 ? -errno : (int64_t)r;
}
// kern_io_read_timeout waits up to ns nanoseconds for fd to be ready
// (data, end of file or a pending error), then reads once: the bytes read,
// 0 at end of file or for an empty buf, KERN_TIMED_OUT (-2^32, outside
// every errno) when fd was not ready in time (nothing is read), or -errno
// (specs/std-timeouts). poll waits whole
// milliseconds, rounded up; EINTR is retried with the time left.
uint64_t kern_now_ns(void);
#define KERN_TIMED_OUT (-((int64_t)1 << 32))
int64_t kern_io_read_timeout(int32_t fd, char *buf, int64_t n, uint64_t ns) {
    if (n <= 0) return 0;
    if (fd < 0) return -EBADF;
    uint64_t start = kern_now_ns();
    for (;;) {
        uint64_t spent = kern_now_ns() - start;
        uint64_t left = spent >= ns ? 0 : ns - spent;
        uint64_t ms = left / 1000000u + (left % 1000000u != 0);
        if (ms > INT_MAX) ms = INT_MAX;
        struct pollfd p = {fd, POLLIN, 0};
        int r = poll(&p, 1, (int)ms);
        if (r < 0) {
            if (errno == EINTR) continue;
            return -errno;
        }
        if (r == 0) {
            if (kern_now_ns() - start >= ns) return KERN_TIMED_OUT;
            continue; // woke early (a timeout clamped to INT_MAX ms)
        }
        if (p.revents & POLLNVAL) return -EBADF;
        break;
    }
    ssize_t r;
    do r = read(fd, buf, (size_t)n); while (r < 0 && errno == EINTR);
    return r < 0 ? -errno : (int64_t)r;
}
int64_t kern_io_write(int32_t fd, const char *p, int64_t n) {
    if (fd == 1) { kern_out(p, (size_t)n); return n; }
    ssize_t r = write(fd, p, (size_t)n);
    return r < 0 ? -errno : (int64_t)r;
}
// kern_io_close closes a descriptor the program opened (the kern side never
// passes the standard streams); a delayed write error surfaces here.
int32_t kern_io_close(int32_t fd) { if (fd < 0) return -EBADF; return close(fd) < 0 ? -errno : 0; }
// kern_cpath copies a kern path into a NUL-terminated buffer of 4096
// bytes: 0, or -ENAMETOOLONG or -EINVAL (an embedded NUL).
static int kern_cpath(char path[4096], const char *p, int64_t n) {
    if (n < 0 || n >= 4096) return -ENAMETOOLONG;
    if (n > 0 && memchr(p, 0, (size_t)n) != NULL) return -EINVAL;
    memcpy(path, p, (size_t)n);
    path[n] = 0;
    return 0;
}
int32_t kern_io_remove(const char *p, int64_t n) {
    char path[4096];
    int e = kern_cpath(path, p, n);
    if (e < 0) return e;
    return unlink(path) < 0 ? -errno : 0;
}
// directories and temporary files (specs/std-files)
int32_t kern_io_mkdir(const char *p, int64_t n) {
    char path[4096];
    int e = kern_cpath(path, p, n);
    if (e < 0) return e;
    return mkdir(path, 0777) < 0 ? -errno : 0;
}
int32_t kern_io_rmdir(const char *p, int64_t n) {
    char path[4096];
    int e = kern_cpath(path, p, n);
    if (e < 0) return e;
    return rmdir(path) < 0 ? -errno : 0;
}
// kern_io_kind is 1 for a regular file, 2 for a directory, 3 for anything
// else, following symbolic links; -errno on failure.
int32_t kern_io_kind(const char *p, int64_t n) {
    char path[4096];
    int e = kern_cpath(path, p, n);
    if (e < 0) return e;
    struct stat st;
    if (stat(path, &st) < 0) return -errno;
    return S_ISREG(st.st_mode) ? 1 : S_ISDIR(st.st_mode) ? 2 : 3;
}
static int kern_cmp_names(const void *a, const void *b) { return strcmp(*(const char *const *)a, *(const char *const *)b); }
// kern_io_list writes the names of a directory's entries, `.` and `..`
// left out, in bytewise order, each followed by a NUL, into buf when they
// fit in cap bytes, and returns the bytes they need; -errno on failure.
int64_t kern_io_list(const char *p, int64_t n, char *buf, int64_t cap) {
    char path[4096];
    int e = kern_cpath(path, p, n);
    if (e < 0) return e;
    int dfd = kern_open_above2(path, O_RDONLY | O_DIRECTORY | O_CLOEXEC, 0);
    if (dfd < 0) return dfd;
    DIR *d = fdopendir(dfd);
    if (d == NULL) { int de = errno; close(dfd); return -de; }
    char **names = NULL;
    size_t count = 0, room = 0;
    int64_t total = 0;
    for (;;) {
        // errno is reset before each readdir: the allocations below may set
        // it, and readdir leaves it alone at the end of the directory
        errno = 0;
        struct dirent *ent = readdir(d);
        if (ent == NULL) {
            if (errno != 0) e = -errno;
            break;
        }
        if (strcmp(ent->d_name, ".") == 0 || strcmp(ent->d_name, "..") == 0) continue;
        if (count == room) {
            room = room ? 2 * room : 64;
            char **grown = realloc(names, room * sizeof *names);
            if (grown == NULL) { e = -ENOMEM; break; }
            names = grown;
        }
        names[count] = strdup(ent->d_name);
        if (names[count] == NULL) { e = -ENOMEM; break; }
        total += (int64_t)strlen(names[count]) + 1;
        count++;
    }
    closedir(d);
    if (e == 0) {
        if (count > 1) qsort(names, count, sizeof *names, kern_cmp_names);
        if (total <= cap) {
            int64_t at = 0;
            for (size_t i = 0; i < count; i++) {
                size_t l = strlen(names[i]) + 1;
                memcpy(buf + at, names[i], l);
                at += (int64_t)l;
            }
        }
    }
    for (size_t i = 0; i < count; i++) free(names[i]);
    free(names);
    return e < 0 ? e : total;
}
// kern_io_temp creates a fresh directory (dir) or empty file named prefix
// and six random characters in $TMPDIR (/tmp when unset) and writes its
// path into buf; the path's length, or -errno (-EINVAL for a prefix with a
// '/' or a NUL, -ENAMETOOLONG when it does not fit).
int64_t kern_io_temp(const char *p, int64_t n, uint8_t dir, char *buf, int64_t cap) {
    if (n < 0 || n > 255) return -ENAMETOOLONG;
    if (n > 0 && (memchr(p, 0, (size_t)n) != NULL || memchr(p, '/', (size_t)n) != NULL)) return -EINVAL;
    const char *t = getenv("TMPDIR");
    if (t == NULL || *t == 0) t = "/tmp";
    size_t tl = strlen(t);
    while (tl > 1 && t[tl - 1] == '/') tl--;
    char tmpl[4096];
    if (tl + 1 + (size_t)n + 6 >= sizeof tmpl) return -ENAMETOOLONG;
    memcpy(tmpl, t, tl);
    tmpl[tl] = '/';
    memcpy(tmpl + tl + 1, p, (size_t)n);
    memcpy(tmpl + tl + 1 + n, "XXXXXX", 7);
    if (dir) {
        if (mkdtemp(tmpl) == NULL) return -errno;
    } else {
        int fd = mkostemp(tmpl, O_CLOEXEC);
        int ce = fd < 0 ? errno : (close(fd) < 0 ? errno : 0);
        if (fd < 0) return -ce;
        if (ce != 0) {
            unlink(tmpl);
            return -ce;
        }
    }
    size_t l = strlen(tmpl);
    if ((int64_t)l > cap) {
        if (dir) rmdir(tmpl); else unlink(tmpl);
        return -ENAMETOOLONG;
    }
    memcpy(buf, tmpl, l);
    return (int64_t)l;
}
int64_t kern_io_argc(void) { return kern_argc; }

int64_t kern_io_arg(int64_t i, char *buf, int64_t n) {
    if (i < 0 || i >= kern_argc) return 0;
    size_t len = strlen(kern_argv[i]);
    memcpy(buf, kern_argv[i], len < (size_t)n ? len : (size_t)n);
    return (int64_t)len;
}

// ---- std/process (specs/std-process) ----

// kern_env is the environment the program started with, taken by
// kern_start: environ's entries in order, keeping only the first entry of
// a name that occurs more than once, as Go's runtime does, so that the
// interpreter and a native build see the same list. C code changing the
// environment later does not alter it.
extern char **environ;
static char **kern_env = NULL;
static int64_t kern_envc = 0;
static uint64_t kern_env_hash(const char *e, size_t k) {
    uint64_t h = 14695981039346656037ull; // FNV-1a
    for (size_t i = 0; i < k; i++) h = (h ^ (uint8_t)e[i]) * 1099511628211ull;
    return h;
}
static void kern_env_init(void) {
    size_t n = 0;
    while (environ != NULL && environ[n] != NULL) n++;
    kern_env = malloc((n + 1) * sizeof(char *));
    // seen is an open-addressing set of the names kept so far (indexes
    // into kern_env plus one), so the scan is linear in the environment
    size_t cap = 16;
    while (cap < 2 * n) cap *= 2;
    int64_t *seen = calloc(cap, sizeof(int64_t));
    if (kern_env == NULL || seen == NULL) { free(seen); return; } // an empty environment then
    for (size_t i = 0; i < n; i++) {
        const char *e = environ[i];
        if (*e == 0) continue; // an empty entry, which Go's syscall.Environ leaves out too
        const char *eq = strchr(e, '=');
        size_t slot = 0;
        if (eq != NULL) { // an entry without '=' names no variable and is kept as is, as Go keeps it
            size_t k = (size_t)(eq - e);
            slot = (size_t)kern_env_hash(e, k) & (cap - 1);
            int dup = 0;
            while (seen[slot] != 0) {
                const char *o = kern_env[seen[slot] - 1];
                if (strncmp(o, e, k) == 0 && o[k] == '=') { dup = 1; break; }
                slot = (slot + 1) & (cap - 1);
            }
            if (dup) continue;
        }
        char *c = strdup(e);
        if (c == NULL) continue; // out of memory: the entry is left out
        kern_env[kern_envc++] = c;
        if (eq != NULL) seen[slot] = kern_envc;
    }
    free(seen);
}
int64_t kern_proc_env_count(void) { return kern_envc; }
// kern_proc_env_at copies at most n bytes of entry i into buf and returns
// the entry's length, 0 when there is no entry i.
int64_t kern_proc_env_at(int64_t i, char *buf, int64_t n) {
    if (i < 0 || i >= kern_envc) return 0;
    size_t len = strlen(kern_env[i]);
    memcpy(buf, kern_env[i], len < (size_t)n ? len : (size_t)n);
    return (int64_t)len;
}

// kern_sig_ignored holds the signals that were ignored when the program
// started; a child keeps them ignored and gets every other signal's
// default disposition (specs/std-process R4). SIGSEGV and SIGBUS, which
// kern_start catches for the fault handler, reach a child at their default
// even when ignored at start, as R4 states: an exec resets a caught signal
// and posix_spawn cannot set one to ignored (Go's runtime, which catches
// them too, does the same in the interpreter). SIGCHLD is the exception:
// ignoring it (or SA_NOCLDWAIT) makes the system reap children by itself,
// so that wait could not report a status and kill could reach a reused
// process ID. kern_sig_init therefore resets it to the default, which for
// SIGCHLD also discards the signal, and children get the default too, as
// in the interpreter, where Go always installs its own SIGCHLD handler.
static sigset_t kern_sig_ignored;
static void kern_sig_init(void) {
    sigemptyset(&kern_sig_ignored);
    for (int sig = 1; sig < NSIG; sig++) {
        struct sigaction old;
        if (sig == SIGCHLD || sigaction(sig, NULL, &old) != 0) continue;
        if (old.sa_handler == SIG_IGN) sigaddset(&kern_sig_ignored, sig);
    }
    struct sigaction dfl;
    memset(&dfl, 0, sizeof dfl);
    dfl.sa_handler = SIG_DFL;
    sigemptyset(&dfl.sa_mask);
    sigaction(SIGCHLD, &dfl, NULL); // flags 0: SA_NOCLDWAIT cleared too
}

// kern_null is /dev/null, opened for reading and writing above 2 by
// kern_start, before any thread exists: the source of every Null stream.
static int kern_null = -1;

// kern_pipe makes a pipe whose ends are close-on-exec and numbered 3 or
// more: 0, or -errno. Linux sets the flag atomically; macOS has no pipe2,
// and the moment before the flag is set cannot reach a std/process child,
// which POSIX_SPAWN_CLOEXEC_DEFAULT keeps to the descriptors it names (R6).
static int kern_pipe(int pp[2]) {
#ifdef __APPLE__
    if (pipe(pp) != 0) return -errno;
    fcntl(pp[0], F_SETFD, FD_CLOEXEC);
    fcntl(pp[1], F_SETFD, FD_CLOEXEC);
#else
    if (pipe2(pp, O_CLOEXEC) != 0) return -errno;
#endif
    for (int i = 0; i < 2; i++) {
        if (pp[i] > 2) continue; // only when the program closed a standard descriptor itself
        int d = fcntl(pp[i], F_DUPFD_CLOEXEC, 3);
        int e = errno;
        close(pp[i]);
        pp[i] = d;
        if (d < 0) {
            if (pp[1 - i] >= 0) close(pp[1 - i]);
            return -e;
        }
    }
    return 0;
}

// kern_cstr copies n bytes of p into a fresh NUL-terminated string, or
// returns NULL (out of memory).
static char *kern_cstr(const char *p, int64_t n) {
    char *c = malloc((size_t)n + 1);
    if (c == NULL) return NULL;
    memcpy(c, p, (size_t)n);
    c[n] = 0;
    return c;
}
// kern_unpack checks that buf holds exactly count NUL-terminated strings
// back to back and returns a NULL-terminated array of pointers into it,
// or NULL (malformed: *bad set; or out of memory).
static char **kern_unpack(const char *buf, int64_t n, int64_t count, int *bad) {
    *bad = 0;
    int64_t nuls = 0;
    for (int64_t i = 0; i < n; i++) nuls += buf[i] == 0;
    if (count < 0 || nuls != count || (n > 0 && buf[n - 1] != 0)) { *bad = 1; return NULL; }
    char **v = malloc(((size_t)count + 1) * sizeof(char *));
    if (v == NULL) return NULL;
    int64_t at = 0;
    for (int64_t i = 0; i < count; i++) {
        v[i] = (char *)buf + at;
        at += (int64_t)strlen(buf + at) + 1;
    }
    v[count] = NULL;
    return v;
}
// kern_path_join writes a, '/' and b (b of bn bytes) into out (4096 bytes):
// 0, or -ENAMETOOLONG.
static int kern_path_join(char out[4096], const char *a, const char *b, size_t bn) {
    size_t an = strlen(a);
    if (an + 1 + bn >= 4096) return -ENAMETOOLONG;
    memcpy(out, a, an);
    out[an] = '/';
    memcpy(out + an + 1, b, bn);
    out[an + 1 + bn] = 0;
    return 0;
}
// kern_executable reports whether path names a regular file the process
// may execute (symbolic links followed).
static int kern_executable(const char *path) {
    struct stat st;
    return stat(path, &st) == 0 && S_ISREG(st.st_mode) && access(path, X_OK) == 0;
}
// kern_proc_spawn starts a child (specs/std-process R4) and returns its
// process ID, which is the handle, or -errno. The kern side has validated
// the strings; this entry checks only what memory safety needs. The
// failures come in R4's order: (s) the streams, (a) entering dir, (b)
// finding the program (R2), (c) executing it.
int64_t kern_proc_spawn(const char *prog, int64_t progn, const char *argvb, int64_t argvn, int64_t argc,
                        const char *envb, int64_t envn, int64_t envc, const char *dirp, int64_t dirn, uint8_t has_dir,
                        const char *search, int64_t searchn, const uint8_t *modes, int64_t modesn, int32_t *fds, int64_t fdsn) {
    if (progn <= 0 || progn >= 4096 || dirn < 0 || dirn >= 4096 || searchn < 0 || modesn != 3 || fdsn < 3 || argc < 1) return -EINVAL;
    for (int i = 0; i < 3; i++) if (modes[i] > 2) return -EINVAL;
    int bad = 0;
    char **argv = kern_unpack(argvb, argvn, argc, &bad);
    if (argv == NULL) return bad ? -EINVAL : -ENOMEM;
    char **envp = kern_unpack(envb, envn, envc, &bad);
    if (envp == NULL) { free(argv); return bad ? -EINVAL : -ENOMEM; }
    int64_t result = 0;
    int child[3] = {-1, -1, -1}, parent[3] = {-1, -1, -1};
    char path[4096], cwd[4096], absdir[4096];
    char *p = kern_cstr(prog, progn), *d = kern_cstr(dirp, dirn), *sp = kern_cstr(search, searchn);
    posix_spawn_file_actions_t fa;
    posix_spawnattr_t attr;
    int fa_ok = 0, attr_ok = 0;
    if (p == NULL || d == NULL || sp == NULL) { result = -ENOMEM; goto out; }
    kern_flush(); // the program's output so far precedes the child's; a failure is ignored (R4)
    // (s) the streams, stdin then stdout then stderr: Null is the runtime's
    // own /dev/null, opened above 2 at start; Piped a new pipe, the child's
    // end its stdin's read end or its output's write end
    for (int i = 0; i < 3; i++) {
        if (modes[i] == 1) {
            if (kern_null < 0) { result = -EMFILE; goto out; } // it could not be opened at start
            child[i] = kern_null;
        } else if (modes[i] == 2) {
            int pp[2];
            if ((result = kern_pipe(pp)) != 0) goto out;
            child[i] = i == 0 ? pp[0] : pp[1];
            parent[i] = i == 0 ? pp[1] : pp[0];
#ifdef __APPLE__
            // a write to a child that closed its stdin is EPIPE, never a
            // SIGPIPE that ends the program (R6)
            if (i == 0) fcntl(parent[i], F_SETNOSIGPIPE, 1);
#endif
        }
    }
    // (a) the working directory, and absolute forms of it and of the
    // program, so that nothing is resolved against a directory twice
    int need_cwd = (has_dir && d[0] != '/') || (memchr(p, '/', (size_t)progn) != NULL && p[0] != '/' && !(has_dir && d[0] == '/'));
    if (need_cwd && getcwd(cwd, sizeof cwd) == NULL) { result = -errno; goto out; }
    if (has_dir) {
        struct stat st;
        if (stat(d, &st) != 0) { result = -errno; goto out; }
        if (!S_ISDIR(st.st_mode)) { result = -ENOTDIR; goto out; }
        if (access(d, X_OK) != 0) { result = -errno; goto out; }
        if (d[0] == '/') {
            memcpy(absdir, d, (size_t)dirn + 1);
        } else if ((result = kern_path_join(absdir, cwd, d, (size_t)dirn)) != 0) {
            goto out;
        }
    }
    // (b) the program
    if (p[0] == '/') {
        memcpy(path, p, (size_t)progn + 1);
    } else if (memchr(p, '/', (size_t)progn) != NULL) {
        if ((result = kern_path_join(path, has_dir ? absdir : cwd, p, (size_t)progn)) != 0) goto out;
    } else {
        int found = 0;
        for (char *e = sp; !found;) {
            char *colon = strchr(e, ':');
            size_t en = colon ? (size_t)(colon - e) : strlen(e);
            if (en > 0 && e[0] == '/' && en < 4096) { // empty and relative elements are skipped (R2)
                char dir[4096];
                memcpy(dir, e, en);
                dir[en] = 0;
                if (kern_path_join(path, dir, p, (size_t)progn) == 0 && kern_executable(path)) found = 1;
            }
            if (colon == NULL) break;
            e = colon + 1;
        }
        if (!found) { result = -ENOENT; goto out; }
    }
    // (c) the child: streams onto 0 to 2, the directory, the signals, and
    // no other descriptor
    if ((errno = posix_spawn_file_actions_init(&fa)) != 0) { result = -errno; goto out; }
    fa_ok = 1;
    if ((errno = posix_spawnattr_init(&attr)) != 0) { result = -errno; goto out; }
    attr_ok = 1;
    for (int i = 0; i < 3; i++) {
        int e = 0;
        if (child[i] >= 0) {
            e = posix_spawn_file_actions_adddup2(&fa, child[i], i);
        } else if (!kern_std_closed[i] && fcntl(i, F_GETFD) >= 0) { // Inherit an open stream; a closed one stays closed
#ifdef __APPLE__
            e = posix_spawn_file_actions_addinherit_np(&fa, i);
#else
            e = posix_spawn_file_actions_adddup2(&fa, i, i); // clears FD_CLOEXEC in the child
#endif
        }
        if (e != 0) { result = -e; goto out; }
    }
#ifdef __APPLE__
    // POSIX.1-2024's posix_spawn_file_actions_addchdir replaces it from
    // macOS 26 on; the _np form also runs on every older macOS
#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wdeprecated-declarations"
#endif
    if (has_dir && (errno = posix_spawn_file_actions_addchdir_np(&fa, absdir)) != 0) { result = -errno; goto out; }
#ifdef __APPLE__
#pragma clang diagnostic pop
#endif
#ifndef __APPLE__
    if ((errno = posix_spawn_file_actions_addclosefrom_np(&fa, 3)) != 0) { result = -errno; goto out; }
#endif
    sigset_t def, none;
    sigfillset(&def);
    for (int sig = 1; sig < NSIG; sig++) if (sigismember(&kern_sig_ignored, sig) == 1) sigdelset(&def, sig);
    sigdelset(&def, SIGKILL);
    sigdelset(&def, SIGSTOP);
    sigemptyset(&none);
    short flags = POSIX_SPAWN_SETSIGDEF | POSIX_SPAWN_SETSIGMASK;
#ifdef __APPLE__
    flags |= POSIX_SPAWN_CLOEXEC_DEFAULT;
#endif
    if ((errno = posix_spawnattr_setsigdefault(&attr, &def)) != 0 || (errno = posix_spawnattr_setsigmask(&attr, &none)) != 0 ||
        (errno = posix_spawnattr_setflags(&attr, flags)) != 0) {
        result = -errno;
        goto out;
    }
    pid_t pid;
    int e = posix_spawn(&pid, path, &fa, &attr, argv, envp);
    result = e != 0 ? -e : (int64_t)pid;
out:
    if (fa_ok) posix_spawn_file_actions_destroy(&fa);
    if (attr_ok) posix_spawnattr_destroy(&attr);
    for (int i = 0; i < 3; i++) if (child[i] >= 0 && child[i] != kern_null) close(child[i]);
    // the parent's pipe ends go to the Child; nothing is left open on failure
    for (int i = 0; i < 3; i++) {
        if (result < 0 && parent[i] >= 0) close(parent[i]);
        fds[i] = result < 0 ? -1 : parent[i];
    }
    free(p);
    free(d);
    free(sp);
    free(argv);
    free(envp);
    return result;
}
// kern_proc_wait waits for the child h (block) or checks whether it has
// ended: 1 with st[0] 0 and st[1] its exit code, or st[0] 1 and st[1] the
// signal that ended it; 0 while it runs; or -errno.
int32_t kern_proc_wait(int64_t h, uint8_t block, int32_t *st, int64_t n) {
    if (h <= 0 || h > INT32_MAX) return -ESRCH; // never a process group or "any child"
    if (n < 2) return -EINVAL;
    int s;
    pid_t r;
    do r = waitpid((pid_t)h, &s, block ? 0 : WNOHANG); while (r < 0 && errno == EINTR);
    if (r < 0) return -errno;
    if (r == 0) return 0;
    if (WIFSIGNALED(s)) { st[0] = 1; st[1] = WTERMSIG(s); }
    else { st[0] = 0; st[1] = WEXITSTATUS(s); }
    return 1;
}
int32_t kern_proc_signal(int64_t h, int32_t sig) {
    if (h <= 0 || h > INT32_MAX) return -ESRCH;
    int r;
    do r = kill((pid_t)h, sig); while (r < 0 && errno == EINTR);
    return r < 0 ? -errno : 0;
}
uint32_t kern_proc_pid(int64_t h) { return h > 0 && h <= INT32_MAX ? (uint32_t)h : 0; }
// the handle is the process ID natively: nothing to release, and the
// child writes inherited streams to the real descriptors, so there is no
// forwarding to wait for
void kern_proc_release(int64_t h) { (void)h; }
void kern_proc_flush_inherited(int64_t h) { (void)h; }
int32_t kern_proc_close(int32_t fd) { return close(fd) < 0 ? -errno : 0; }
// kern_proc_read reads from a pipe end: bytes read, 0 at end of file, or
// -errno; EINTR is retried.
int64_t kern_proc_read(int32_t fd, char *buf, int64_t n) {
    ssize_t r;
    do r = read(fd, buf, (size_t)n); while (r < 0 && errno == EINTR);
    return r < 0 ? -errno : (int64_t)r;
}
// kern_proc_write writes to a pipe end: bytes written or -errno; EINTR is
// retried. A write to a child that closed its end is EPIPE and never
// ends the program (R6): macOS's ends carry F_SETNOSIGPIPE; on Linux the
// write runs with SIGPIPE blocked in the calling thread, and a SIGPIPE
// that became pending during it is taken back before the mask is
// restored; one already pending before is left for the program.
int64_t kern_proc_write(int32_t fd, const char *data, int64_t n) {
#ifdef __APPLE__
    ssize_t r;
    do r = write(fd, data, (size_t)n); while (r < 0 && errno == EINTR);
    return r < 0 ? -errno : (int64_t)r;
#else
    sigset_t pipeset, old, pending;
    sigemptyset(&pipeset);
    sigaddset(&pipeset, SIGPIPE);
    pthread_sigmask(SIG_BLOCK, &pipeset, &old);
    sigpending(&pending);
    int was_pending = sigismember(&pending, SIGPIPE) == 1;
    ssize_t r;
    do r = write(fd, data, (size_t)n); while (r < 0 && errno == EINTR);
    int e = errno;
    // the SIGPIPE this write raised: with EPIPE, or with a short write, since
    // when the reader goes away during a blocked write Linux returns the
    // bytes already written and still raises SIGPIPE. A SIGPIPE arriving
    // during a full write (sent to the thread) was not raised by it, and
    // is left pending for the program.
    int could_raise = (r < 0 && e == EPIPE) || (r >= 0 && r < n);
    sigpending(&pending);
    if (could_raise && !was_pending && sigismember(&pending, SIGPIPE) == 1) {
        struct timespec zero = {0, 0};
        while (sigtimedwait(&pipeset, NULL, &zero) < 0 && errno == EINTR) { }
    }
    pthread_sigmask(SIG_SETMASK, &old, NULL);
    return r < 0 ? -e : (int64_t)r;
#endif
}
int32_t kern_proc_errno(uint8_t which) {
    switch (which) {
    case 0: return EINVAL;
    case 1: return ENAMETOOLONG;
    case 2: return EBADF;
    case 3: return EPIPE;
    case 5: return EAGAIN;
    default: return EIO;
    }
}
// kern_proc_faults are the fault hook's slots, one per pipe kind (0 stdin
// writes, 1 stdout reads, 2 stderr reads): an errno the next operation of
// that kind returns instead of making its call. Taking is always there;
// arming exists only in a --test-hooks build, so a program arming a fault
// without it fails to link.
static _Atomic int32_t kern_proc_faults[3];
int32_t kern_proc_fault_take(uint8_t kind) {
    if (kind > 2) return 0;
    if (atomic_load_explicit(&kern_proc_faults[kind], memory_order_relaxed) == 0) return 0;
    return atomic_exchange(&kern_proc_faults[kind], 0);
}
#ifdef KERN_TEST_HOOKS
void kern_proc_fault_arm(uint8_t kind, int32_t code) {
    if (kind <= 2) atomic_store(&kern_proc_faults[kind], code);
}
#endif
void kern_yield(void) { sched_yield(); }
uint64_t kern_now_ns(void) { struct timespec ts; clock_gettime(KERN_CLOCK, &ts); return (uint64_t)ts.tv_sec * 1000000000ull + (uint64_t)ts.tv_nsec; }

// ---- traps (spec §6, PLAN.md 2.2 signal-safety contract) ----

static const char *kern_trap_names[] = {"bounds", "overflow", "div_zero", "shift", "conversion", "generation", "vacant", "identity", "disjoint", "oom", "stack", "explicit", "unreachable"};

// TrapInfo with the §4.1 layout of `struct TrapInfo { kind: TrapKind; file: RawPtr[u8]; start: usize; end: usize; thread_id: u64 }`
typedef struct { uint8_t kind; uint8_t pad[7]; const char *file; uint64_t start, end, thread_id; } kern_trapinfo;

static void *_Atomic kern_handler = NULL;
static const char *kern_file = "";
static _Atomic uint64_t kern_next_tid = 1;

// per-thread state, preallocated at thread start: the guard range, the
// record buffer and the TrapInfo
static __thread uintptr_t kern_stack_lo = 0, kern_stack_hi = 0;
static __thread uint64_t kern_tid = 0;
static __thread int kern_in_trap = 0;
static __thread kern_trapinfo kern_info;
static __thread char kern_record[512];

void *kern_set_handler(void *fn) { return atomic_exchange(&kern_handler, fn); }
void kern_set_file(const char *name) { kern_file = name; }

static void kern_die(void) {
    kern_flush_try();
    signal(SIGABRT, SIG_DFL);
    abort();
}

static size_t kern_cat(char *b, size_t n, const char *s) { size_t l = strlen(s); memcpy(b + n, s, l); return n + l; }

// kern_trap_at runs the trap protocol: build TrapInfo, select the handler
// with one seq_cst load, run it at most once, terminate by SIGABRT. It uses
// no stdio, lock or allocation, so it may run in signal context.
static void kern_trap_at(int32_t kind, int64_t start, int64_t end, const char *file, int in_signal) {
    if (kern_in_trap) kern_die(); // a trap inside the handler terminates
    kern_in_trap = 1;
    if (!in_signal) kern_flush();
    kern_info.kind = (uint8_t)kind;
    kern_info.file = file ? file : kern_file; // the trapping function's file, or the program's
    kern_info.start = (uint64_t)start;
    kern_info.end = (uint64_t)end;
    kern_info.thread_id = kern_tid;
    void (*h)(void *) = atomic_load(&kern_handler);
    if (h == NULL) {
        // the default handler: one JSON record on standard error
        char *b = kern_record; size_t n = 0;
        n = kern_cat(b, n, "{\"trap\":{\"kind\":\"");
        n = kern_cat(b, n, kern_trap_names[kind]);
        n = kern_cat(b, n, "\",\"file\":\"");
        for (const char *c = kern_info.file; *c && n < 400; c++) {
            if (*c == '"' || *c == '\\') b[n++] = '\\';
            b[n++] = *c;
        }
        n = kern_cat(b, n, "\",\"span\":{\"start\":");
        n += kern_fmt_u64(b + n, (uint64_t)start);
        n = kern_cat(b, n, ",\"end\":");
        n += kern_fmt_u64(b + n, (uint64_t)end);
        n = kern_cat(b, n, "},\"thread\":");
        n += kern_fmt_u64(b + n, kern_tid);
        n = kern_cat(b, n, "}}\n");
        kern_write_all(2, b, n);
    } else {
        h(&kern_info);
    }
    kern_die();
}

void kern_trap(int32_t kind, int64_t start, int64_t end, const char *file) { kern_trap_at(kind, start, end, file, 0); }

// ---- the location of a fault (specs/native-trap-loc/SPEC.md) ----

// Every kern function of a hosted build sits in the kern text section
// behind a 16-byte prefix: the magic word, then the index of its
// declaration's location in the table the entry passed to
// kern_set_fn_locs, or -1 for a library function (whose location is its
// caller's). The fault handler names the user function the fault happened
// in: the one containing the faulting instruction, or else the nearest
// caller found through the link register, the word at the stack pointer
// and the frame-pointer chain. It reads only the kern text section and
// the live part of the thread's stack, so it cannot fault again.
#ifdef __APPLE__
extern const char kern_text_lo[] __asm("section$start$__TEXT$__kern_text");
extern const char kern_text_hi[] __asm("section$end$__TEXT$__kern_text");
#else
extern const char __start_kern_text[] __attribute__((weak));
extern const char __stop_kern_text[] __attribute__((weak));
#define kern_text_lo __start_kern_text
#define kern_text_hi __stop_kern_text
#endif
#define KERN_LOC_MAGIC 0x21434f4c4e52454bull
typedef struct { int64_t start, end; const char *file; } kern_fnloc;
static const kern_fnloc *kern_fn_locs = NULL;
static int64_t kern_fn_nlocs = 0;
void kern_set_fn_locs(const void *t, int64_t n) { kern_fn_locs = (const kern_fnloc *)t; kern_fn_nlocs = n; }

// kern_fn_at is the location of the user function whose code holds a: 1
// with *loc set, 0 for a library function, -1 for an address outside kern
// code. The marker is the nearest magic word before a, 4-aligned.
static int kern_fn_at(uintptr_t a, const kern_fnloc **loc) {
    uintptr_t lo = (uintptr_t)kern_text_lo, hi = (uintptr_t)kern_text_hi;
    if (lo == 0 || a < lo + 16 || a >= hi) return -1;
    for (uintptr_t p = (a - 8) & ~(uintptr_t)3;; p -= 4) {
        uint64_t w;
        __builtin_memcpy(&w, (const void *)p, 8);
        if (w == KERN_LOC_MAGIC) {
            int64_t i;
            __builtin_memcpy(&i, (const void *)(p + 8), 8);
            if (i < 0 || i >= kern_fn_nlocs || kern_fn_locs == NULL) return 0;
            *loc = &kern_fn_locs[i];
            return 1;
        }
        if (p < lo + 4) return -1;
    }
}

static uintptr_t kern_page = 4096; // the system page size, set at start

// kern_probe is a pipe the fault handler copies stack words through: the
// kernel reads the word for write(2) and answers EFAULT where a direct
// read would fault again (below the Linux main thread's stack lies an
// unmapped gap of up to 1 MiB in which a stack fault can land, and an
// emulator may keep a guard region there that is mapped but unreadable).
static int kern_probe[2] = {-1, -1};
// kern_probe_busy pairs each write with its own read when threads fault
// at once: held only across those two calls, which do not block (the pipe
// has room for one word and then holds it), and never re-entered by its
// holder, whose SIGSEGV is blocked while the handler runs.
static atomic_flag kern_probe_busy = ATOMIC_FLAG_INIT;

// kern_stack_word reads the word at a when it lies in the live part of the
// thread's stack (at or above floor, the stack pointer, the thread's stack
// base and, for a stack fault, the end of the faulting page; below its
// top) and the kernel can read it.
static int kern_stack_word(uintptr_t a, uintptr_t floor, uintptr_t *w) {
    if (kern_stack_lo == 0 || a < floor || a < kern_stack_lo || a >= kern_stack_hi || kern_stack_hi - a < 8 || (a & 7) != 0) return 0;
    if (kern_probe[0] < 0) return 0;
    while (atomic_flag_test_and_set_explicit(&kern_probe_busy, memory_order_acquire)) { }
    int ok = write(kern_probe[1], (const void *)a, 8) == 8 && read(kern_probe[0], w, 8) == 8;
    atomic_flag_clear_explicit(&kern_probe_busy, memory_order_release);
    return ok;
}

static const kern_fnloc *kern_fault_loc(void *ctx, uintptr_t floor) {
    uintptr_t pc = 0, lr = 0, fp = 0, sp = 0;
#if defined(__APPLE__) && defined(__aarch64__)
    ucontext_t *uc = (ucontext_t *)ctx;
    pc = (uintptr_t)__darwin_arm_thread_state64_get_pc(uc->uc_mcontext->__ss);
    lr = (uintptr_t)__darwin_arm_thread_state64_get_lr(uc->uc_mcontext->__ss);
    fp = (uintptr_t)__darwin_arm_thread_state64_get_fp(uc->uc_mcontext->__ss);
    sp = (uintptr_t)__darwin_arm_thread_state64_get_sp(uc->uc_mcontext->__ss);
#elif defined(__APPLE__) && defined(__x86_64__)
    ucontext_t *uc = (ucontext_t *)ctx;
    pc = uc->uc_mcontext->__ss.__rip; fp = uc->uc_mcontext->__ss.__rbp; sp = uc->uc_mcontext->__ss.__rsp;
#elif defined(__linux__) && defined(__x86_64__)
    ucontext_t *uc = (ucontext_t *)ctx;
    pc = (uintptr_t)uc->uc_mcontext.gregs[REG_RIP]; fp = (uintptr_t)uc->uc_mcontext.gregs[REG_RBP]; sp = (uintptr_t)uc->uc_mcontext.gregs[REG_RSP];
#elif defined(__linux__) && defined(__aarch64__)
    ucontext_t *uc = (ucontext_t *)ctx;
    pc = uc->uc_mcontext.pc; lr = uc->uc_mcontext.regs[30]; fp = uc->uc_mcontext.regs[29]; sp = uc->uc_mcontext.sp;
#else
    (void)ctx;
    return NULL;
#endif
    if (sp > floor) floor = sp;
    const kern_fnloc *loc = NULL;
    if (kern_fn_at(pc, &loc) == 1) return loc;
    // a return address names the call, one byte before it
    if (lr != 0 && kern_fn_at(lr - 1, &loc) == 1) return loc;
    uintptr_t w;
    if (lr == 0 && kern_stack_word(sp, floor, &w) && kern_fn_at(w - 1, &loc) == 1) return loc;
    for (int n = 0; n < 4096; n++) {
        uintptr_t next, ret;
        if (!kern_stack_word(fp, floor, &next) || fp > UINTPTR_MAX - 8 || !kern_stack_word(fp + 8, floor, &ret)) break;
        if (kern_fn_at(ret - 1, &loc) == 1) return loc;
        if (next <= fp) break;
        fp = next;
    }
    return NULL;
}

// the fault handler classifies the fault as `stack` iff the address lies in
// the thread's guard range; any other fault in safe code is `unreachable`.
// Either names the user function it happened in, when it can be found.
static void kern_segv(int sig, siginfo_t *si, void *ctx) {
    (void)sig;
    uintptr_t addr = (uintptr_t)si->si_addr;
    int32_t kind = 12;
    uintptr_t floor = 0;
    if (kern_stack_lo != 0 && addr < kern_stack_lo + 4096 && addr + (1 << 20) >= kern_stack_lo) {
        kind = 10;
        floor = (addr | (kern_page - 1)) + 1;
    }
    const kern_fnloc *loc = kern_fault_loc(ctx, floor);
    if (loc != NULL) kern_trap_at(kind, loc->start, loc->end, loc->file, 1);
    kern_trap_at(kind, 0, 0, NULL, 1);
}

static void kern_thread_init(void *altstack) {
    stack_t ss = { .ss_sp = altstack, .ss_size = 65536, .ss_flags = 0 };
    sigaltstack(&ss, NULL);
#ifdef __APPLE__
    kern_stack_hi = (uintptr_t)pthread_get_stackaddr_np(pthread_self());
    kern_stack_lo = kern_stack_hi - pthread_get_stacksize_np(pthread_self());
#else
    pthread_attr_t at; void *lo; size_t sz;
    if (pthread_getattr_np(pthread_self(), &at) == 0 && pthread_attr_getstack(&at, &lo, &sz) == 0) {
        kern_stack_lo = (uintptr_t)lo; kern_stack_hi = kern_stack_lo + sz;
        pthread_attr_destroy(&at);
    }
#endif
}

void kern_start(void) {
    static char altstack[65536];
    long pg = sysconf(_SC_PAGESIZE);
    if (pg > 0) kern_page = (uintptr_t)pg;
    kern_std_init(); // first: before anything else opens a descriptor
    kern_env_init();
    kern_sig_init(); // before the runtime installs its own handlers
    kern_null = kern_open_above2("/dev/null", O_RDWR | O_CLOEXEC, 0);
    if (pipe(kern_probe) != 0) kern_probe[0] = kern_probe[1] = -1; // no walk then
    for (int i = 0; i < 2 && kern_probe[i] >= 0; i++) {
        // above 2, so that the probe never stands in for a standard stream
        // the program was started without (specs/std-process R4)
        int fd = kern_probe[i] < 3 ? fcntl(kern_probe[i], F_DUPFD_CLOEXEC, 3) : kern_probe[i];
        if (fd != kern_probe[i]) { close(kern_probe[i]); kern_probe[i] = fd; }
        if (fd >= 0) fcntl(fd, F_SETFD, FD_CLOEXEC);
    }
    if (kern_probe[0] < 0 || kern_probe[1] < 0) {
        if (kern_probe[0] >= 0) close(kern_probe[0]);
        if (kern_probe[1] >= 0) close(kern_probe[1]);
        kern_probe[0] = kern_probe[1] = -1;
    }
    kern_thread_init(altstack);
    struct sigaction sa;
    memset(&sa, 0, sizeof sa);
    sa.sa_sigaction = kern_segv;
    sa.sa_flags = SA_SIGINFO | SA_ONSTACK;
    sigaction(SIGSEGV, &sa, NULL);
    sigaction(SIGBUS, &sa, NULL);
    atexit(kern_flush);
}

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
typedef struct { int64_t start, end; const char *file; } kern_loc;
static void kern_trap_loc(int32_t kind, const kern_loc *at) {
    if (at == NULL) kern_trap(kind, 0, 0, NULL);
    kern_trap(kind, at->start, at->end, at->file);
}
static void *kern_alloc(size_t n, const kern_loc *at) {
    if (atomic_exchange(&kern_fail_next, 0)) kern_trap_loc(9, at);
    void *p = malloc(n ? n : 1);
    if (p == NULL) kern_trap_loc(9, at);
    return p;
}

// ---- std/time (specs/std-time) ----

// kern_time_sleep_ns waits at least ns nanoseconds, resuming with the
// remainder when a signal interrupts nanosleep; any other failure (none is
// possible for a well-formed timespec) traps explicit at the call.
void kern_time_sleep_ns(uint64_t ns, const kern_loc *at) {
    struct timespec want = { .tv_sec = (time_t)(ns / 1000000000ull), .tv_nsec = (long)(ns % 1000000000ull) };
    struct timespec left;
    while (nanosleep(&want, &left) != 0) {
        if (errno != EINTR) kern_trap_loc(11, at);
        want = left;
    }
}

// kern_time_wall_ns reads the wall clock (CLOCK_REALTIME) in nanoseconds
// since the Unix epoch; a failure, impossible for this clock, traps
// explicit at the call.
uint64_t kern_time_wall_ns(const kern_loc *at) {
    struct timespec ts;
    if (clock_gettime(CLOCK_REALTIME, &ts) != 0) kern_trap_loc(11, at);
    return (uint64_t)ts.tv_sec * 1000000000ull + (uint64_t)ts.tv_nsec;
}

// ---- std/str floating-point text (specs/float-text) ----
//
// The runtime never calls setlocale or fesetround, so snprintf and strtod
// run in the C locale with round-to-nearest, both correctly rounded; the
// interpreter runs the same search with Go's strconv.

// kern_str_f64_digits writes the round-trip digits of a finite v in
// exponent form ([-]d[.ddd]e+XX): the fewest correctly rounded
// significant digits, 1 to 17, that read back as v. Returns the length.
int64_t kern_str_f64_digits(double v, char *buf, int64_t n) {
    char tmp[40];
    int len = 0;
    for (int p = 1; p <= 17; p++) {
        len = snprintf(tmp, sizeof tmp, "%.*e", p - 1, v);
        if (strtod(tmp, NULL) == v) break;
    }
    if (n < 0) n = 0;
    if (len > n) len = (int)n;
    memcpy(buf, tmp, (size_t)len);
    return len;
}

// kern_str_parse_f64 is the correctly rounded value of text already
// checked to be one JSON number (±Inf when it overflows). strtod needs a
// NUL-terminated string: the text is copied to the stack when it fits and
// to the heap otherwise, where a failed allocation traps oom at the call.
double kern_str_parse_f64(const char *p, int64_t n, const kern_loc *at) {
    char small[1024];
    char *s = small;
    if (n < 0 || n >= (int64_t)sizeof small) {
        if (n < 0 || (uint64_t)n >= SIZE_MAX) kern_trap_loc(9, at);
        s = malloc((size_t)n + 1);
        if (s == NULL) kern_trap_loc(9, at);
    }
    memcpy(s, p, (size_t)n);
    s[n] = 0;
    double v = strtod(s, NULL);
    if (s != small) free(s);
    return v;
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

// ---- Shared ----

typedef struct { _Atomic uint64_t count; int64_t size; char data[]; } kern_shared;
void *kern_shared_new(int64_t size, const void *src, const kern_loc *at) {
    kern_shared *s = kern_alloc(sizeof *s + (size_t)size, at);
    atomic_store(&s->count, 1); s->size = size;
    memcpy(s->data, src, (size_t)size);
    return s;
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
    if (atomic_fetch_sub(&s->count, 1) == 1) { if (g) g(s->data); free(s); }
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
static void kern_park(_Atomic(uint32_t) *w, uint32_t expect) { (void)os_sync_wait_on_address(w, expect, sizeof *w, OS_SYNC_WAIT_ON_ADDRESS_NONE); }
static void kern_unpark_one(_Atomic(uint32_t) *w) { (void)os_sync_wake_by_address_any(w, sizeof *w, OS_SYNC_WAKE_BY_ADDRESS_NONE); }
static void kern_unpark_all(_Atomic(uint32_t) *w) { (void)os_sync_wake_by_address_all(w, sizeof *w, OS_SYNC_WAKE_BY_ADDRESS_NONE); }
#elif defined(__linux__)
#include <linux/futex.h>
#include <sys/syscall.h>
#include <unistd.h>
static void kern_park(_Atomic(uint32_t) *w, uint32_t expect) { (void)syscall(SYS_futex, w, FUTEX_WAIT_PRIVATE, expect, NULL, NULL, 0); }
static void kern_unpark_one(_Atomic(uint32_t) *w) { (void)syscall(SYS_futex, w, FUTEX_WAKE_PRIVATE, 1, NULL, NULL, 0); }
static void kern_unpark_all(_Atomic(uint32_t) *w) { (void)syscall(SYS_futex, w, FUTEX_WAKE_PRIVATE, INT32_MAX, NULL, NULL, 0); }
#else
#error "kern: no park primitive for this platform (os_sync_wait_on_address on macOS 14.4+, futex on Linux)"
#endif

typedef struct { _Atomic(uint32_t) state; _Atomic(uintptr_t) owner; int64_t size; char data[]; } kern_mutex;
_Static_assert(offsetof(kern_mutex, owner) == 8 && offsetof(kern_mutex, size) == 16 && offsetof(kern_mutex, data) == 24, "the state and the owner share the first 16 bytes (specs/perf-gaps/SPEC.md R6)");
static inline uintptr_t kern_self(void) { return (uintptr_t)pthread_self(); }
void *kern_mutex_new(int64_t size, const void *src, const kern_loc *at) {
    kern_mutex *mx = kern_alloc(sizeof *mx + (size_t)size, at);
    atomic_init(&mx->state, 0);
    atomic_init(&mx->owner, 0);
    mx->size = size;
    memcpy(mx->data, src, (size_t)size);
    return mx;
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
void kern_mutex_drop(kern_mutex *mx, kern_glue g) { if (mx == NULL) return; if (g) g(mx->data); free(mx); }

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
typedef struct { _Atomic(uint32_t) seq; _Atomic(uintptr_t) mutex; } kern_condvar;
void *kern_condvar_new(const kern_loc *at) {
    kern_condvar *cv = kern_alloc(sizeof *cv, at);
    atomic_init(&cv->seq, 0);
    atomic_init(&cv->mutex, 0);
    return cv;
}
void kern_condvar_wait(kern_condvar *cv, void *data, const kern_loc *at) {
    kern_mutex *mx = (kern_mutex *)((char *)data - offsetof(kern_mutex, data));
    uintptr_t self = kern_self();
    if (atomic_load_explicit(&mx->owner, memory_order_relaxed) != self) kern_trap_loc(11, at); // the lock is not held by this thread
    uintptr_t bound = atomic_load_explicit(&cv->mutex, memory_order_relaxed), want = 0;
    if (bound != (uintptr_t)mx && !atomic_compare_exchange_strong(&cv->mutex, &want, (uintptr_t)mx) && want != (uintptr_t)mx) kern_trap_loc(11, at); // a second Mutex on one Condvar
    uint32_t seq = atomic_load_explicit(&cv->seq, memory_order_relaxed);
    kern_mutex_release(mx);
    kern_park(&cv->seq, seq);
    kern_mutex_acquire(mx);
    atomic_store_explicit(&mx->owner, self, memory_order_relaxed);
}
void kern_condvar_notify_one(kern_condvar *cv) { atomic_fetch_add_explicit(&cv->seq, 1, memory_order_relaxed); kern_unpark_one(&cv->seq); }
void kern_condvar_notify_all(kern_condvar *cv) { atomic_fetch_add_explicit(&cv->seq, 1, memory_order_relaxed); kern_unpark_all(&cv->seq); }
void kern_condvar_drop(kern_condvar *cv) { free(cv); }

// ---- threads (spec §5.5) ----

typedef struct { pthread_t t; char *block; int64_t roff; int joined, started; } kern_thread;
typedef struct { kern_thread **joins; int64_t n, cap; } kern_scope;
typedef struct { kern_thread *t; int taken; } kern_join;

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
    memcpy(t->block, pack, (size_t)size);
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
    memcpy(dst, t->block + t->roff, (size_t)size);
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
    memcpy(dst, j->t->block + j->t->roff, (size_t)size);
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

// ---- SystemAllocator (spec §6) ----

typedef struct { int unused; } kern_sysalloc;
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
        if (np) { memcpy(np, p, (size_t)(old_size < size ? old_size : size)); free(p); }
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
