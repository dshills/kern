// kern hosted runtime: startup.
// Included by kern_rt.c; see kern_rt.h for the entries.

// kern_rt_abi_3 is the ABI symbol every program references (RA4).
const int32_t KERN_RT_ABI_SYMBOL = KERN_RT_ABI;

// kern_at_exit runs when main returns: what kern_exit does before it ends
// the process.
static void kern_at_exit(void) { kern_flush(); kern_alloc_report(); }

void kern_start(const void *abi) {
    (void)abi;
    static char altstack[65536];
    long pg = sysconf(_SC_PAGESIZE);
    if (pg > 0) kern_page = (uintptr_t)pg;
    kern_std_init(); // first: before anything else opens a descriptor
    kern_config_init();
    kern_out_tty = isatty(1);
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
    atexit(kern_at_exit);
}
