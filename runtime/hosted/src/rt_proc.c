// kern hosted runtime: std/process, the start-time environment, signal
// dispositions and child processes.
// Included by kern_rt.c; see kern_rt.h for the entries.

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
    kern_copy(buf, kern_env[i], len < (size_t)n ? len : (size_t)n);
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
    kern_copy(c, p, (size_t)n);
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
