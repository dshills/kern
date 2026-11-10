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
// kern_cpath copies a kern path into a NUL-terminated buffer of 4096
// bytes: 0, or -ENAMETOOLONG or -EINVAL (an embedded NUL would cut the
// path).
static int kern_cpath(char path[4096], const char *p, int64_t n) {
    if (n < 0 || n >= 4096) return -ENAMETOOLONG;
    if (n > 0 && memchr(p, 0, (size_t)n) != NULL) return -EINVAL;
    kern_copy(path, p, (size_t)n);
    path[n] = 0;
    return 0;
}
// kern_io_open opens the file at a path. opts are std/io's OpenOptions as
// bits (1 read, 2 write, 4 append, 8 create, 16 truncate, 32 create_new),
// which the kern side has validated. The descriptor is close-on-exec from
// the start, so that no child inherits the file (specs/std-process R6),
// and above 2: when a standard stream was closed at startup, opened files
// stay above the descriptors the output buffer and the kern side treat
// specially. A new file's mode is 0666 less the process's umask.
int32_t kern_io_open(const char *p, int64_t n, uint32_t opts) {
    char path[4096];
    int e = kern_cpath(path, p, n);
    if (e < 0) return e;
    int flags = O_CLOEXEC | ((opts & (2u | 4u)) == 0 ? O_RDONLY : (opts & 1u) ? O_RDWR : O_WRONLY);
    if (opts & 4u) flags |= O_APPEND;
    if (opts & 32u) flags |= O_CREAT | O_EXCL; // fails on anything that exists, a symbolic link included
    else flags |= ((opts & 8u) ? O_CREAT : 0) | ((opts & 16u) ? O_TRUNC : 0);
    int fd;
    do fd = kern_open_above2(path, flags, 0666); while (fd == -EINTR);
    return fd;
}
// A read or a write that a signal interrupts before it moved a byte is
// made again: std/signal's handlers do not show through as short calls.
int64_t kern_io_read(int32_t fd, char *buf, int64_t n) {
    if (fd == 0) kern_flush(); // what the program wrote is seen before it waits for input (RB2)
    ssize_t r;
    do r = read(fd, buf, (size_t)n); while (r < 0 && errno == EINTR);
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
    ssize_t r;
    do r = write(fd, p, (size_t)n); while (r < 0 && errno == EINTR);
    return r < 0 ? -errno : (int64_t)r;
}
// kern_io_close closes a descriptor the program opened (the kern side never
// passes the standard streams); a delayed write error surfaces here.
int32_t kern_io_close(int32_t fd) { if (fd < 0) return -EBADF; return close(fd) < 0 ? -errno : 0; }
// ---- files beyond read and write (specs/runtime RG2, RG3) ----

// kern_io_seek moves a descriptor's offset: whence 0 from the start, 1
// from the current offset, 2 from the end; the new offset, or -errno
// (-EINVAL for any other whence).
int64_t kern_io_seek(int32_t fd, int64_t off, int32_t whence) {
    if (whence < 0 || whence > 2) return -EINVAL;
    off_t r = lseek(fd, (off_t)off, whence == 0 ? SEEK_SET : whence == 1 ? SEEK_CUR : SEEK_END);
    return r < 0 ? -errno : (int64_t)r;
}
// kern_io_sync asks the system to make a file's data and metadata
// durable. On macOS fsync only hands the data to the drive; F_FULLFSYNC
// asks the drive to write it, and file systems without it fall back.
int32_t kern_io_sync(int32_t fd) {
    int r;
#if defined(__APPLE__)
    do r = fcntl(fd, F_FULLFSYNC); while (r < 0 && errno == EINTR);
    if (r == 0) return 0;
    if (errno == EBADF) return -EBADF;
#endif
    do r = fsync(fd); while (r < 0 && errno == EINTR);
    return r < 0 ? -errno : 0;
}
int32_t kern_io_set_len(int32_t fd, uint64_t n) {
    if (n > (uint64_t)INT64_MAX) return -EINVAL;
    int r;
    do r = ftruncate(fd, (off_t)n); while (r < 0 && errno == EINTR);
    return r < 0 ? -errno : 0;
}
// kern_io_flock takes or gives up an advisory lock on an open file
// (specs/runtime RL3): op 0 shared, 1 exclusive, 2 unlock, and 4 added to
// either of the first two returns -EWOULDBLOCK where the call would wait.
// The lock is the open file's and ends when it is closed.
int32_t kern_io_flock(int32_t fd, int32_t op) {
    int how = (op & 3) == 0 ? LOCK_SH : (op & 3) == 1 ? LOCK_EX : LOCK_UN;
    if ((op & 3) == 3 || op < 0 || op > 5) return -EINVAL;
    if (op & 4) how |= LOCK_NB;
    int r;
    do r = flock(fd, how); while (r < 0 && errno == EINTR);
    return r < 0 ? -errno : 0;
}
// kern_io_lock_range takes or gives up an advisory lock on a range of an
// open file's bytes (specs/runtime RM1): op as kern_io_flock's; len 0 is
// "to the end of the file, wherever that comes to be". The lock is the
// open file description's (F_OFD_SETLK), as flock's is: it is not lost
// when another descriptor of the same file is closed, and two opens of
// one file in one process contend like two processes.
int32_t kern_io_lock_range(int32_t fd, int32_t op, uint64_t start, uint64_t len) {
    if ((op & 3) == 3 || op < 0 || op > 5) return -EINVAL;
    if (start > (uint64_t)INT64_MAX || len > (uint64_t)INT64_MAX) return -EINVAL;
    struct flock fl;
    memset(&fl, 0, sizeof fl);
    fl.l_type = (op & 3) == 0 ? F_RDLCK : (op & 3) == 1 ? F_WRLCK : F_UNLCK;
    fl.l_whence = SEEK_SET;
    fl.l_start = (off_t)start;
    fl.l_len = (off_t)len;
    int r;
    do r = fcntl(fd, (op & 4) || (op & 3) == 2 ? F_OFD_SETLK : F_OFD_SETLKW, &fl); while (r < 0 && errno == EINTR);
    if (r < 0) {
        if (errno == EACCES) return -EAGAIN; // a conflict is one error, whichever the system names it
        if (errno == EBADF) return -EINVAL;  // the file is not open the way the lock needs
        return -errno;
    }
    return 0;
}
// kern_stat_out writes std/io's Metadata as four numbers: the kind (1 a
// regular file, 2 a directory, 3 anything else, 4 a symbolic link), the
// size in bytes, the modification time in nanoseconds since the Unix
// epoch (0 for an earlier time, the largest u64 for one too late to
// represent) and the low 12 bits of the mode.
static void kern_stat_out(const struct stat *st, uint64_t out[4]) {
    out[0] = S_ISREG(st->st_mode) ? 1 : S_ISDIR(st->st_mode) ? 2 : S_ISLNK(st->st_mode) ? 4 : 3;
    out[1] = st->st_size < 0 ? 0 : (uint64_t)st->st_size;
#if defined(__APPLE__)
    const struct timespec m = st->st_mtimespec;
#else
    const struct timespec m = st->st_mtim;
#endif
    if (m.tv_sec < 0) out[2] = 0;
    else if ((uint64_t)m.tv_sec > (UINT64_MAX - 999999999ull) / 1000000000ull) out[2] = UINT64_MAX;
    else out[2] = (uint64_t)m.tv_sec * 1000000000ull + (uint64_t)m.tv_nsec;
    out[3] = (uint64_t)(st->st_mode & 07777);
}
// kern_io_stat reads the metadata of a path into out (at least four
// numbers, kern_stat_out), following a final symbolic link when follow is
// set: 0 or -errno.
int32_t kern_io_stat(const char *p, int64_t n, uint8_t follow, uint64_t *out, int64_t cap) {
    char path[4096];
    int e = kern_cpath(path, p, n);
    if (e < 0) return e;
    if (cap < 4) return -EINVAL;
    struct stat st;
    if ((follow ? stat(path, &st) : lstat(path, &st)) < 0) return -errno;
    kern_stat_out(&st, out);
    return 0;
}
int32_t kern_io_fstat(int32_t fd, uint64_t *out, int64_t cap) {
    if (cap < 4) return -EINVAL;
    struct stat st;
    if (fstat(fd, &st) < 0) return -errno;
    kern_stat_out(&st, out);
    return 0;
}
// kern_two_paths runs a system call that takes two paths: 0 or -errno.
static int32_t kern_two_paths(const char *p, int64_t n, const char *q, int64_t m, int (*call)(const char *, const char *)) {
    char a[4096], b[4096];
    int e = kern_cpath(a, p, n);
    if (e == 0) e = kern_cpath(b, q, m);
    if (e < 0) return e;
    return call(a, b) < 0 ? -errno : 0;
}
int32_t kern_io_rename(const char *p, int64_t n, const char *q, int64_t m) { return kern_two_paths(p, n, q, m, rename); }
int32_t kern_io_link(const char *p, int64_t n, const char *q, int64_t m) { return kern_two_paths(p, n, q, m, link); }
int32_t kern_io_symlink(const char *p, int64_t n, const char *q, int64_t m) { return kern_two_paths(p, n, q, m, symlink); }
int32_t kern_io_chmod(const char *p, int64_t n, uint32_t mode) {
    char path[4096];
    int e = kern_cpath(path, p, n);
    if (e < 0) return e;
    return chmod(path, (mode_t)(mode & 07777)) < 0 ? -errno : 0;
}
// kern_text_out copies a NUL-terminated text into buf when its bytes fit
// in cap, and returns how many it has: a caller with too small a buffer
// asks again with one that size.
static int64_t kern_text_out(const char *text, char *buf, int64_t cap) {
    size_t l = strlen(text);
    if ((int64_t)l <= cap) kern_copy(buf, text, l);
    return (int64_t)l;
}
int64_t kern_io_readlink(const char *p, int64_t n, char *buf, int64_t cap) {
    char path[4096], target[4096];
    int e = kern_cpath(path, p, n);
    if (e < 0) return e;
    ssize_t r = readlink(path, target, sizeof target - 1); // no system's link is longer
    if (r < 0) return -errno;
    target[r] = 0;
    if ((int64_t)r <= cap) kern_copy(buf, target, (size_t)r); // by length: a link's text may hold any byte but NUL
    return (int64_t)r;
}
int64_t kern_io_realpath(const char *p, int64_t n, char *buf, int64_t cap) {
    char path[4096];
    int e = kern_cpath(path, p, n);
    if (e < 0) return e;
    char *real = realpath(path, NULL);
    if (real == NULL) return -errno;
    int64_t l = kern_text_out(real, buf, cap);
    free(real);
    return l;
}
int64_t kern_io_getcwd(char *buf, int64_t cap) {
    char *dir = getcwd(NULL, 0); // allocated to fit, on glibc and on macOS
    if (dir == NULL) return -errno;
    int64_t l = kern_text_out(dir, buf, cap);
    free(dir);
    return l;
}

// ---- error kinds (specs/runtime RG1) ----

// kern_io_err_kind is the ordinal of std/io's ErrKind for a system error
// number, by the error's name: the numbers differ between hosts, the
// names do not.
int32_t kern_io_err_kind(int32_t code) {
    switch (code) {
    case ENOENT: return 0;
    case EACCES: case EPERM: return 1;
    case EEXIST: return 2;
    case EINVAL: case ENAMETOOLONG: return 3;
    case EISDIR: return 4;
    case ENOTDIR: return 5;
    case ENOTEMPTY: return 6;
    case EPIPE: return 7;
    case ECONNREFUSED: return 8;
    case ECONNRESET: return 9;
    case ECONNABORTED: return 10;
    case ENOTCONN: return 11;
    case EADDRINUSE: return 12;
    case EADDRNOTAVAIL: return 13;
    case ETIMEDOUT: return 14;
    case EINTR: return 15;
    case EAGAIN: return 16;
#if EWOULDBLOCK != EAGAIN
    case EWOULDBLOCK: return 16;
#endif
    case ENOMEM: return 17;
    case ENOSYS: case ENOTSUP: return 18;
#if EOPNOTSUPP != ENOTSUP
    case EOPNOTSUPP: return 18;
#endif
    default: return 19;
    }
}
// kern_io_err_code is the host's number of the error that names a kind
// (the first of kern_io_err_kind's list), EIO for Other: how kern code
// makes an IoErr of a kind without writing a number.
int32_t kern_io_err_code(int32_t kind) {
    static const int codes[] = {ENOENT, EACCES, EEXIST, EINVAL, EISDIR, ENOTDIR, ENOTEMPTY, EPIPE, ECONNREFUSED, ECONNRESET, ECONNABORTED, ENOTCONN, EADDRINUSE, EADDRNOTAVAIL, ETIMEDOUT, EINTR, EAGAIN, ENOMEM, ENOTSUP};
    return kind >= 0 && kind < (int32_t)(sizeof codes / sizeof codes[0]) ? codes[kind] : EIO;
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
