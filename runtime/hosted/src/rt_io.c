// kern hosted runtime: std/io, descriptor-level file access.
// Included by kern_rt.c; see kern_rt.h for the entries.

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
    kern_copy(path, p, (size_t)n);
    path[n] = 0;
    // close-on-exec from the start, so that no child inherits the file
    // (specs/std-process R6), and above 2: when a standard stream was
    // closed at startup, keep opened files above the descriptors the
    // output buffer and the kern side treat specially
    return kern_open_above2(path, write ? O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC : O_RDONLY | O_CLOEXEC, 0644);
}
int64_t kern_io_read(int32_t fd, char *buf, int64_t n) {
    if (fd == 0) kern_flush(); // what the program wrote is seen before it waits for input (RB2)
    ssize_t r = read(fd, buf, (size_t)n);
    return r < 0 ? -errno : (int64_t)r;
}
// kern_io_read_timeout waits up to ns nanoseconds for fd to be ready
// (data, end of file or a pending error), then reads once: the bytes read,
// 0 at end of file or for an empty buf, KERN_TIMED_OUT (-2^32, outside
// every errno) when fd was not ready in time (nothing is read), or -errno
// (specs/std-timeouts). poll waits whole
// milliseconds, rounded up; EINTR is retried with the time left.
#define KERN_TIMED_OUT (-((int64_t)1 << 32))
int64_t kern_io_read_timeout(int32_t fd, char *buf, int64_t n, uint64_t ns) {
    if (n <= 0) return 0;
    if (fd < 0) return -EBADF;
    if (fd == 0) kern_flush(); // as kern_io_read
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
    kern_copy(path, p, (size_t)n);
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
    kern_copy(tmpl + tl + 1, p, (size_t)n);
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
    kern_copy(buf, kern_argv[i], len < (size_t)n ? len : (size_t)n);
    return (int64_t)len;
}
