// kern hosted runtime: the entries compiled kern code calls, with their C
// prototypes (the ABI, specs/runtime/SPEC.md RA1). kern_rt.c includes this
// header and then every unit; a function that is not declared here is
// static in its unit. TestRuntimeABI checks every declaration the compiler
// emits against these definitions, and docs/runtime.md's entry table is
// generated from this file: each prototype is preceded by its one-line
// contract.
//
// Conventions. A (p, n) pair is a byte sequence of n bytes, not
// NUL-terminated. An `out`/`off` pair is the address of a Result the entry
// fills: out[0] is the tag (0 Ok, 1 Err) and the payload goes at out + off.
// A trailing `const kern_loc *at` is the location of the user-level call,
// where the entry raises its traps (null: no location). An entry that
// returns int32_t or int64_t "or -errno" returns the negated system error
// on failure. Entries under KERN_TEST_HOOKS exist only in a --test-hooks
// build.
#ifndef KERN_RT_H
#define KERN_RT_H

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
#include <sys/mman.h>
#include <sys/socket.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <netdb.h>
#include <sys/un.h>
#include <net/if.h>
#include <sys/file.h>
#include <sys/random.h>
#if defined(__linux__)
#include <sys/auxv.h>
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

// KERN_RT_ABI is the version of this interface. It changes when an existing
// entry's prototype or contract changes; every program references the
// symbol kern_rt_abi_<KERN_RT_ABI>, so one built for another version does
// not link (RA4).
#define KERN_RT_ABI 2
#define KERN_RT_ABI_SYMBOL kern_rt_abi_2
extern const int32_t KERN_RT_ABI_SYMBOL;

// kern_glue is a destructor the compiler emits for a type: it destroys the
// value at its argument.
typedef void (*kern_glue)(void *);
// kern_loc is the location of a user-level call: a byte span and the file
// (null: the program's file).
typedef struct { int64_t start, end; const char *file; } kern_loc;

typedef struct kern_shared kern_shared;
typedef struct kern_mutex kern_mutex;
typedef struct kern_condvar kern_condvar;
typedef struct kern_thread kern_thread;
typedef struct kern_scope kern_scope;
typedef struct kern_join kern_join;
typedef struct kern_sysalloc kern_sysalloc;

// ---- rt_start.c: startup ----

// Initializes the runtime once, on the initial thread, before the program runs; abi is ignored (it makes the program reference the ABI symbol).
void kern_start(const void *abi);
// Runs the program's main function and returns its result, the process's exit status.
int32_t kern_run_main(int32_t (*fn)(void));

// ---- rt_base.c: standard output and exit ----

// Writes v in decimal to standard output.
void print_u64(uint64_t v);
// Writes v in decimal to standard output.
void print_i64(int64_t v);
// Writes "true" or "false" to standard output.
void print_bool(uint8_t v);
// Writes the n bytes at p to standard output.
void print_str(const char *p, int64_t n);
// Writes the n bytes at p to standard error, unbuffered.
void eprint_str(const char *p, int64_t n);
// Writes a newline to standard output.
void println(void);
// Flushes standard output and ends the process with status code, running no destructor.
void kern_exit(int32_t code);
// Flushes standard output and takes the recorded write failure: 0, or -errno of the first failed write since the last call.
int32_t kern_io_flush_stdout(void);
// Reports whether standard stream 0, 1 or 2 is a terminal: 1 or 0.
int32_t kern_io_is_terminal(int32_t stream);

// ---- rt_io.c: std/io ----

// Records the program's arguments for kern_io_argc and kern_io_arg; called by the entry code before kern_start.
void kern_set_args(int32_t argc, char **argv);
// Opens the path with std/io's OpenOptions as bits (1 read, 2 write, 4 append, 8 create, 16 truncate, 32 create_new): a descriptor above 2, close-on-exec, or -errno.
int32_t kern_io_open(const char *p, int64_t n, uint32_t opts);
// Reads up to n bytes from fd: the count, 0 at end of file, or -errno.
int64_t kern_io_read(int32_t fd, char *buf, int64_t n);
// Reads once after waiting up to ns nanoseconds for fd to be ready: the count, 0 at end of file, -2^32 on timeout, or -errno.
int64_t kern_io_read_timeout(int32_t fd, char *buf, int64_t n, uint64_t ns);
// Writes n bytes to fd (descriptor 1 through the output buffer): the count or -errno.
int64_t kern_io_write(int32_t fd, const char *p, int64_t n);
// Closes a descriptor the program opened: 0 or -errno.
int32_t kern_io_close(int32_t fd);
// Moves a descriptor's offset (whence 0 start, 1 current, 2 end): the new offset or -errno.
int64_t kern_io_seek(int32_t fd, int64_t off, int32_t whence);
// Asks the system to make a file's data durable: 0 or -errno.
int32_t kern_io_sync(int32_t fd);
// Sets a file's length to n bytes, zero-filling growth: 0 or -errno.
int32_t kern_io_set_len(int32_t fd, uint64_t n);
// Takes (op 0 shared, 1 exclusive) or gives up (2) an advisory lock on an open file; 4 added returns -EWOULDBLOCK instead of waiting: 0 or -errno.
int32_t kern_io_flock(int32_t fd, int32_t op);
// Takes or gives up (op as kern_io_flock's) an advisory lock on len bytes from start of an open file, len 0 for all that follow: 0 or -errno.
int32_t kern_io_lock_range(int32_t fd, int32_t op, uint64_t start, uint64_t len);
// Writes a path's metadata (kind, size, modified_ns, mode) into out[0..4], following a final link when follow is set: 0 or -errno.
int32_t kern_io_stat(const char *p, int64_t n, uint8_t follow, uint64_t *out, int64_t cap);
// As kern_io_stat for an open descriptor.
int32_t kern_io_fstat(int32_t fd, uint64_t *out, int64_t cap);
// Renames p to q, replacing q: 0 or -errno.
int32_t kern_io_rename(const char *p, int64_t n, const char *q, int64_t m);
// Makes q another name of the file p: 0 or -errno.
int32_t kern_io_link(const char *p, int64_t n, const char *q, int64_t m);
// Makes q a symbolic link whose text is p: 0 or -errno.
int32_t kern_io_symlink(const char *p, int64_t n, const char *q, int64_t m);
// Sets the low 12 mode bits of the file at p: 0 or -errno.
int32_t kern_io_chmod(const char *p, int64_t n, uint32_t mode);
// Writes a symbolic link's text into buf when it fits in cap bytes and returns its length, or -errno.
int64_t kern_io_readlink(const char *p, int64_t n, char *buf, int64_t cap);
// Writes the absolute path of p, links resolved, into buf when it fits in cap bytes and returns its length, or -errno.
int64_t kern_io_realpath(const char *p, int64_t n, char *buf, int64_t cap);
// Writes the working directory into buf when it fits in cap bytes and returns its length, or -errno.
int64_t kern_io_getcwd(char *buf, int64_t cap);
// Returns the ordinal of std/io's ErrKind for a system error number.
int32_t kern_io_err_kind(int32_t code);
// Returns the host's error number that names an ErrKind ordinal (EIO for Other).
int32_t kern_io_err_code(int32_t kind);
// Removes the file at the path: 0 or -errno.
int32_t kern_io_remove(const char *p, int64_t n);
// Creates the directory at the path: 0 or -errno.
int32_t kern_io_mkdir(const char *p, int64_t n);
// Removes the empty directory at the path: 0 or -errno.
int32_t kern_io_rmdir(const char *p, int64_t n);
// Classifies the path, following links: 1 regular file, 2 directory, 3 other, or -errno.
int32_t kern_io_kind(const char *p, int64_t n);
// Writes the directory's entry names, sorted, each NUL-terminated, into buf when they fit in cap: the bytes needed or -errno.
int64_t kern_io_list(const char *p, int64_t n, char *buf, int64_t cap);
// Creates a fresh temporary file or directory named with the prefix and writes its path into buf: the length or -errno.
int64_t kern_io_temp(const char *p, int64_t n, uint8_t dir, char *buf, int64_t cap);
// Returns the number of program arguments.
int64_t kern_io_argc(void);
// Copies at most n bytes of argument i into buf: the argument's length, 0 when there is none.
int64_t kern_io_arg(int64_t i, char *buf, int64_t n);

// ---- rt_proc.c: std/process ----

// Returns the number of entries in the environment the program started with.
int64_t kern_proc_env_count(void);
// Copies at most n bytes of environment entry i into buf: the entry's length, 0 when there is none.
int64_t kern_proc_env_at(int64_t i, char *buf, int64_t n);
// Starts a child process (specs/std-process R4) and stores the parent's pipe ends in fds: its process ID or -errno.
int64_t kern_proc_spawn(const char *prog, int64_t progn, const char *argvb, int64_t argvn, int64_t argc,
                        const char *envb, int64_t envn, int64_t envc, const char *dirp, int64_t dirn, uint8_t has_dir,
                        const char *search, int64_t searchn, const uint8_t *modes, int64_t modesn, int32_t *fds, int64_t fdsn);
// Waits for child h (block) or polls it: 1 with st filled (0 and the exit code, or 1 and the signal), 0 while it runs, or -errno.
int32_t kern_proc_wait(int64_t h, uint8_t block, int32_t *st, int64_t n);
// Sends signal sig to child h: 0 or -errno.
int32_t kern_proc_signal(int64_t h, int32_t sig);
// Returns child h's process ID, 0 for an invalid handle.
uint32_t kern_proc_pid(int64_t h);
// Releases a child's handle; nothing to do natively.
void kern_proc_release(int64_t h);
// Waits for a child's inherited output to be forwarded; nothing to do natively.
void kern_proc_flush_inherited(int64_t h);
// Closes a pipe end: 0 or -errno.
int32_t kern_proc_close(int32_t fd);
// Reads from a pipe end, retrying when interrupted: the count, 0 at end of file, or -errno.
int64_t kern_proc_read(int32_t fd, char *buf, int64_t n);
// Writes to a pipe end, retrying when interrupted, never raising SIGPIPE: the count or -errno.
int64_t kern_proc_write(int32_t fd, const char *data, int64_t n);
// Returns the host's error number for a portable index (0 EINVAL, 1 ENAMETOOLONG, 2 EBADF, 3 EPIPE, 5 EAGAIN, else EIO).
int32_t kern_proc_errno(uint8_t which);
// Takes the armed fault of a pipe kind: its error number, 0 when none is armed.
int32_t kern_proc_fault_take(uint8_t kind);
#ifdef KERN_TEST_HOOKS
// Arms a fault: the next pipe operation of the kind fails with code.
void kern_proc_fault_arm(uint8_t kind, int32_t code);
#endif

// ---- rt_trap.c: traps ----

// Installs the process-wide trap handler with one atomic exchange and returns the previous one (null: the default).
void *kern_set_handler(void *fn);
// Records the program's file name, reported by a trap that names none.
void kern_set_file(const char *name);
// Raises a trap of the kind at the span in file (null: the program's): runs the handler once and aborts; never returns.
_Noreturn void kern_trap(int32_t kind, int64_t start, int64_t end, const char *file);
// Registers the table of function locations and names the fault handler and the backtrace read.
void kern_set_fn_locs(const void *t, int64_t n);
// Registers the program's line tables, from which a trap's line and column are computed.
void kern_set_lines(const void *t, int64_t n);
// Records that the build asked for backtraces (--backtrace); KERN_BACKTRACE overrides it either way.
void kern_set_backtrace(int32_t on);

// ---- rt_alloc.c: allocation ----

#ifdef KERN_TEST_HOOKS
// Arms a failure of the next allocation.
void kern_test_fail_next_alloc(void);
// Exists only with test hooks, so a program using them without the flag fails to link.
void kern_test_require_hooks(void);
#endif
// Exchanges two regions of n bytes that are the same region or do not overlap.
void kern_swap(char *a, char *b, int64_t n);
// Issues the next container identity tag: from 1 in a debug build, 0 (unchecked) in a release build or after wrap-around.
uint32_t kern_next_identity(void);
// Returns the system allocator, a process-wide object.
void *kern_sysalloc_new(void);
// Allocates size bytes at align into the Result at out: Ok(pointer) or Err(OutOfMemory); traps explicit on a violated obligation.
void kern_sysalloc_alloc(kern_sysalloc *a, int64_t size, int64_t align, char *out, int64_t off, const kern_loc *at);
// Resizes an allocation, keeping the common prefix, into the Result at out; on Err the old allocation stays; a debug build traps explicit when p is not a live block of old_size and align.
void kern_sysalloc_realloc(kern_sysalloc *a, void *p, int64_t old_size, int64_t size, int64_t align, char *out, int64_t off, const kern_loc *at);
// Allocates zeroed bytes, as kern_sysalloc_alloc.
void kern_sysalloc_alloc_zeroed(kern_sysalloc *a, int64_t size, int64_t align, char *out, int64_t off, const kern_loc *at);
// Frees an allocation made with the same size and align; a debug build traps explicit when p is not a live block of that size and align.
void kern_sysalloc_free(kern_sysalloc *a, void *p, int64_t size, int64_t align, const kern_loc *at);

// ---- rt_mem.c: pages from the operating system (std/mem) ----

// Returns the size of a page of memory in bytes.
int64_t kern_mem_page_size(void);
// Maps bytes (a positive multiple of the page size) of zeroed, readable and writable memory: its address, or NULL.
void *kern_mem_map(int64_t bytes);
// Unmaps the bytes at p that one kern_mem_map or kern_mem_map_file call mapped.
void kern_mem_unmap(void *p, int64_t bytes);
// Maps the first len bytes of an open file, shared, for reading or for reading and writing: the address or -errno.
int64_t kern_mem_map_file(int32_t fd, int64_t len, uint8_t writable);
// Writes a file mapping's changed pages to its file: 0 or -errno.
int32_t kern_mem_sync(void *p, int64_t len);

// ---- rt_net.c: sockets (std/net) ----
// An address is a record of 19 bytes: the family (4 or 6), 16 bytes of address (IPv4 in the first four), the port high byte first.
// A call with `ns` and `timed` waits at most ns nanoseconds when timed is set and returns -2^32 when that time passed.

// Makes a TCP socket bound to the address and listening with the backlog: a descriptor above 2, close-on-exec, or -errno.
int32_t kern_net_tcp_listen(const uint8_t *addr, int64_t n, int32_t backlog);
// Makes a UDP socket bound to the address: a descriptor or -errno.
int32_t kern_net_udp_bind(const uint8_t *addr, int64_t n);
// Accepts the next connection of a listening socket: its descriptor, -errno, or -2^32.
int64_t kern_net_accept(int32_t fd, uint64_t ns, uint8_t timed);
// Connects a new TCP socket to the address: its descriptor, -errno, or -2^32 (the socket is then closed).
int64_t kern_net_connect(const uint8_t *addr, int64_t n, uint64_t ns, uint8_t timed);
// Makes a Unix-domain stream socket bound to the path and listening with the backlog: a descriptor or -errno.
int32_t kern_net_unix_listen(const char *p, int64_t n, int32_t backlog);
// Connects a new Unix-domain stream socket to the path: its descriptor, -errno, or -2^32.
int64_t kern_net_unix_connect(const char *p, int64_t n, uint64_t ns, uint8_t timed);
// Makes a Unix-domain datagram socket, bound to the path when n > 0: a descriptor or -errno.
int32_t kern_net_unix_dgram(const char *p, int64_t n);
// Sends one datagram to the socket bound to the path, waiting while its queue is full: the count or -errno.
int64_t kern_net_unix_send_to(int32_t fd, const char *p, int64_t n, const char *path, int64_t pn);
// Receives one datagram, cut to n bytes, and its sender's path (cut at 104 bytes, NUL-terminated, empty for a sender with no path) into from: the count, -errno, or -2^32.
int64_t kern_net_unix_recv_from(int32_t fd, char *buf, int64_t n, char *from, int64_t fcap, uint64_t ns, uint8_t timed);
// Makes two connected Unix-domain stream sockets into fds[0] and fds[1]: 0 or -errno.
int32_t kern_net_unix_pair(int32_t *fds, int64_t cap);
// Receives at most n bytes: the count, 0 when the peer closed, -errno, or -2^32.
int64_t kern_net_recv(int32_t fd, char *buf, int64_t n, uint64_t ns, uint8_t timed);
// Sends at most n bytes, waiting while the socket's buffer is full: the count or -errno (never a SIGPIPE).
int64_t kern_net_send(int32_t fd, const char *p, int64_t n);
// Shuts down reading (0), writing (1) or both (2): 0 or -errno.
int32_t kern_net_shutdown(int32_t fd, int32_t how);
// Turns TCP_NODELAY on or off: 0 or -errno.
int32_t kern_net_set_nodelay(int32_t fd, uint8_t on);
// Sets a socket option (1 keep-alive, 2 receive buffer, 3 send buffer, 4 broadcast, 5 hop limit, 6 linger seconds or -1, 7 no-delay): 0 or -errno.
int32_t kern_net_set_opt(int32_t fd, int32_t opt, int64_t value);
// Reads a socket option's value (numbered as kern_net_set_opt's): the value, or -errno - 1.
int64_t kern_net_get_opt(int32_t fd, int32_t opt);
// Finds a network interface by name: its index, or 0 when there is none.
uint32_t kern_net_if_index(const char *p, int64_t n);
// Writes the name of the interface with an index into buf: its length, or 0 when there is none or it does not fit.
int64_t kern_net_if_name(uint32_t index, char *buf, int64_t cap);
// Writes the socket's own address, or its peer's, into rec: 0 or -errno.
int32_t kern_net_addr(int32_t fd, uint8_t peer, uint8_t *rec, int64_t cap);
// Sends one datagram to the address: the count or -errno.
int64_t kern_net_send_to(int32_t fd, const char *p, int64_t n, const uint8_t *addr, int64_t an);
// Receives one datagram, cut to n bytes, and its sender's record into from: the count, -errno, or -2^32.
int64_t kern_net_recv_from(int32_t fd, char *buf, int64_t n, uint8_t *from, int64_t fcap, uint64_t ns, uint8_t timed);
// Closes a socket: 0 or -errno.
int32_t kern_net_close(int32_t fd);
// Resolves a host name: its addresses' records, each with the port, into recs while they fit; the bytes they need, or -errno.
int64_t kern_net_resolve(const char *host, int64_t n, uint16_t port, uint8_t *recs, int64_t cap);

// ---- rt_signal.c: std/signal ----
// A signal is its position in std/signal's Signal: 0 Interrupt, 1 Terminate, 2 Hangup, 3 Quit, 4 User1, 5 User2, 6 Pipe, 7 Child.

// Claims the one live Signals: 0, -EBUSY when one is live, or -errno.
int32_t kern_signal_open(void);
// Records deliveries of the signal from now on, remembering its disposition: 0 or -errno.
int32_t kern_signal_catch(int32_t which);
// Restores every caught signal's earlier disposition, discards the records and frees the claim.
void kern_signal_close(void);
// Makes the signal ignored (how 1) or default (how 0): 0, -EBUSY when the live Signals holds it, or -errno.
int32_t kern_signal_set(int32_t which, int32_t how);
// Returns the first recorded signal of mask and forgets it, waiting for one (at most ns nanoseconds when timed, then -2^32).
int64_t kern_signal_wait(uint32_t mask, uint64_t ns, uint8_t timed);
// Flushes standard output and sends the signal to the program's own process: 0 or -errno.
int32_t kern_signal_raise(int32_t which);

// ---- rt_rand.c: std/rand ----

// Fills n bytes from the system's random generator: 0 or -errno.
int32_t kern_rand_fill(char *buf, int64_t n);
#ifdef KERN_TEST_HOOKS
// Arms a failure of the random source: the request numbered after + 1 fails, once.
void kern_test_fail_random(uint32_t after);
#endif

// ---- rt_time.c: clocks ----

// Returns the monotonic clock in nanoseconds.
uint64_t kern_now_ns(void);
// Sleeps at least ns nanoseconds, resuming when interrupted.
void kern_time_sleep_ns(uint64_t ns, const kern_loc *at);
// Returns the wall clock in nanoseconds since the Unix epoch.
uint64_t kern_time_wall_ns(const kern_loc *at);

// ---- rt_str.c: floating-point text ----

// Writes the shortest decimal digits and exponent that round-trip v into buf: the length.
int64_t kern_str_f64_digits(double v, char *buf, int64_t n);
// Parses validated decimal text to the nearest double; traps oom when a copy of long text cannot be allocated.
double kern_str_parse_f64(const char *p, int64_t n, const kern_loc *at);

// ---- rt_sync.c: Shared, Mutex, Condvar ----

// Creates a Shared holding a copy of the size bytes at src, with count 1; traps oom.
void *kern_shared_new(int64_t size, const void *src, const kern_loc *at);
// As kern_shared_new into the Result at out; on failure destroys the value at src with glue and stores Err(OutOfMemory).
void kern_shared_try_new(int64_t size, void *src, void (*glue)(void *), char *out, int64_t off, const kern_loc *at);
// Returns the address of the shared value.
void *kern_shared_get(kern_shared *s);
// Adds a reference and returns s; traps overflow at the maximum count.
void *kern_shared_clone(kern_shared *s, const kern_loc *at);
// Drops a reference; the last one destroys the value with g and frees the object.
void kern_shared_drop(kern_shared *s, kern_glue g);
#ifdef KERN_TEST_HOOKS
// Sets a Shared's reference count.
void kern_test_shared_seed_count(kern_shared *s, int64_t n);
#endif
// Creates a Mutex holding a copy of the size bytes at src; traps oom.
void *kern_mutex_new(int64_t size, const void *src, const kern_loc *at);
// As kern_mutex_new into the Result at out; on failure destroys the value at src with glue and stores Err(OutOfMemory).
void kern_mutex_try_new(int64_t size, void *src, void (*glue)(void *), char *out, int64_t off, const kern_loc *at);
// Acquires the lock, blocking, and returns the guarded value's address; traps explicit when the caller holds it.
void *kern_mutex_lock(kern_mutex *mx, const kern_loc *at);
// Releases the lock the caller holds and wakes one waiter.
void kern_mutex_unlock(kern_mutex *mx);
// Acquires the lock only if it is free: 1, or 0 when any thread holds it, the caller included.
int32_t kern_mutex_try_lock(kern_mutex *mx);
// Returns the guarded value's address, for a caller that holds the lock.
void *kern_mutex_data(kern_mutex *mx);
// Blocks the calling thread while the 32-bit word at w equals expect; may return at any time, and a wake between the caller's check and the sleep is not lost.
void kern_park(_Atomic(uint32_t) *w, uint32_t expect);
// As kern_park, returning also after ns nanoseconds: 0 only when the time elapsed, else 1.
int32_t kern_park_ns(_Atomic(uint32_t) *w, uint32_t expect, uint64_t ns);
// Wakes one thread parked on w.
void kern_unpark_one(_Atomic(uint32_t) *w);
// Wakes every thread parked on w.
void kern_unpark_all(_Atomic(uint32_t) *w);
// Destroys the guarded value with g and frees the lock.
void kern_mutex_drop(kern_mutex *mx, kern_glue g);
// Creates a Condvar; traps oom.
void *kern_condvar_new(const kern_loc *at);
// As kern_condvar_new into the Result at out: Ok(pointer) or Err(OutOfMemory).
void kern_condvar_try_new(char *out, int64_t off, const kern_loc *at);
// Releases the Mutex guarding data, blocks until notified (or spuriously), and reacquires it; traps explicit on a lock not held or a second Mutex.
void kern_condvar_wait(kern_condvar *cv, void *data, const kern_loc *at);
// As kern_condvar_wait, returning also after ns nanoseconds with the lock held again: 0 only when the time elapsed, else 1.
int32_t kern_condvar_wait_ns(kern_condvar *cv, void *data, uint64_t ns, const kern_loc *at);
// Wakes one thread waiting on the Condvar.
void kern_condvar_notify_one(kern_condvar *cv);
// Wakes every thread waiting on the Condvar.
void kern_condvar_notify_all(kern_condvar *cv);
// Frees the Condvar.
void kern_condvar_drop(kern_condvar *cv);

// ---- rt_thread.c: threads ----

#ifdef KERN_TEST_HOOKS
// Arms a failure of the next spawn.
void kern_test_fail_next_spawn(void);
#endif
// Starts a thread running thunk on a copy of the size-byte argument block, into the Result at out: Ok(handle) or Err(SpawnErr) with nothing left behind.
void kern_thread_spawn(void (*thunk)(void *), const void *pack, int64_t size, int64_t roff, char *out, int64_t off, const kern_loc *at);
// Waits for the thread, copies its size-byte result to dst and frees the handle.
void kern_thread_join(kern_thread *t, void *dst, int64_t size);
// Destroys an unjoined handle: a running thread is detached and destroys its result with g when it ends; a finished one's result is destroyed with g now.
void kern_thread_drop(kern_thread *t, kern_glue g);
// Records the build's stack size, when in range; called by the entry code before kern_start.
void kern_set_stack_bytes(int64_t n);
// Returns the stack size the next spawned thread gets, a whole number of pages.
uint64_t kern_thread_stack_bytes(void);
// Sets the stack size of threads spawned from now on: 1, or 0 with nothing changed when n is outside 65,536 to 1,073,741,824.
int32_t kern_thread_set_stack_bytes(uint64_t n);
// Returns the calling thread's identifier, the one a trap record carries (0 for the thread that runs main).
uint64_t kern_thread_current_id(void);
// Returns the number of processors the process may run on, at least 1.
uint64_t kern_thread_available_parallelism(void);
// Creates a scope for scoped spawns; traps oom.
void *kern_scope_new(const kern_loc *at);
// Starts a scoped thread, as kern_thread_spawn, into the Result at out: Ok(join handle) or Err(SpawnErr).
void kern_scope_spawn(kern_scope *sc, void (*thunk)(void *), const void *pack, int64_t size, int64_t roff, char *out, int64_t off, const kern_loc *at);
// Waits for a scoped thread and copies its size-byte result to dst.
void kern_join_get(kern_join *j, void *dst, int64_t size);
// Destroys a join handle; a result not taken is destroyed with g after the thread ends.
void kern_join_drop(kern_join *j, kern_glue g);
// Joins every thread of the scope in creation order and frees it.
void kern_scope_drop(kern_scope *sc);
// Yields the processor to another thread.
void kern_yield(void);

#endif
