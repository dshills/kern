// kern hosted runtime: clocks and std/time.
// Included by kern_rt.c; see kern_rt.h for the entries.

// ---- clocks ----

#ifdef __APPLE__
#define KERN_CLOCK CLOCK_MONOTONIC_RAW // CLOCK_MONOTONIC has microsecond resolution on macOS
#else
#define KERN_CLOCK CLOCK_MONOTONIC
#endif
uint64_t kern_now_ns(void) { struct timespec ts; clock_gettime(KERN_CLOCK, &ts); return (uint64_t)ts.tv_sec * 1000000000ull + (uint64_t)ts.tv_nsec; }

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
