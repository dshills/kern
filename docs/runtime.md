# The kern runtime

This is the reference for the code that runs under a kern program: what it
does before `main`, how the program's output, failures, threads, memory
and devices behave, and every knob a build or an environment variable
turns. The language's own rules are in `docs/ownership.md` and `spec/`;
the reasons for the runtime's shape are in
`docs/decisions/ADR-017-runtime-architecture.md`; each area's tests are
listed in its report under `docs/reports/runtime-*.md`.

## 1. Two runtimes and an interpreter

| | Where | For | Size of the idea |
|---|---|---|---|
| Hosted runtime | `runtime/hosted/src` | a program run by Linux or macOS | threads, files, sockets, signals, child processes over the system's own |
| Freestanding runtime | `runtime/freestanding/src` | an image run by a machine with no operating system | a console, a clock, a power switch and a heap, behind four board functions |
| Interpreter | `compiler/interp` | `kern run`, `kern test`, and the reference every native build is compared with | the same behaviour, written a second time |

The hosted runtime is one translation unit, `kern_rt.c`, which includes
the interface `kern_rt.h` and then one file per area (`rt_base.c`,
`rt_io.c`, `rt_proc.c`, `rt_trap.c`, `rt_alloc.c`, `rt_mem.c`,
`rt_time.c`, `rt_net.c`, `rt_signal.c`, `rt_rand.c`, `rt_str.c`,
`rt_sync.c`, `rt_thread.c`, `rt_start.c`). `kern build` compiles it with
the program and links only what the program reaches: a program that opens
no socket imports no socket call.

Every function compiled kern code may call is declared in the header with
a one-line contract; section 13 lists them all. The header's
`KERN_RT_ABI` (2) is the interface's version: every program references
the symbol `kern_rt_abi_2`, so a program and a runtime of different
versions do not link. The number changes when an existing entry's
prototype or contract changes.

A release build (`--opt=release`) and a debug build differ in the runtime
in three ways, all checks: a debug build issues container identity tags,
keeps a table of live allocations (section 7), and compiles the program
at a lower optimization level. Nothing else about the runtime depends on
the level.

## 2. Startup and exit

A hosted program's entry does this, in order:

1. hands the runtime the program's tables: where each function was
   declared, where each source line starts, the program's file name and
   its arguments;
2. calls `kern_start`, once, on the initial thread. It
   - fills any of the standard descriptors 0, 1 and 2 that was closed
     with `/dev/null`, so that no file the program opens later lands on
     one;
   - reads the environment variables of section 9;
   - takes a snapshot of the environment for `std/process`;
   - records which signals the program was started ignoring, and gives
     `SIGCHLD` the default disposition;
   - gives the thread an alternate signal stack and installs the
     handlers for `SIGSEGV` and `SIGBUS` that turn a stack overflow into
     a trap (section 4);
3. calls `kern_run_main`, which runs `main`: on the initial thread, or,
   when a stack size is configured, on a thread with that stack.

Nothing runs before `main` on the program's behalf: kern has no static
constructors.

The program ends in one of three ways.

| | Standard output | Destructors | Exit status |
|---|---|---|---|
| `main` returns `n` | flushed | those of `main`'s own values have run | the low 8 bits of `n` (0 for a `main` that returns nothing) |
| `exit(code)` | flushed | none run | the low 8 bits of `code` |
| a trap | flushed, unless the trap is a fault inside a signal handler | none run | the process ends by `SIGABRT` (a shell shows 134) |

Threads still running do not delay any of the three. A program that
needs a thread's work waits for it: `join`, a `thread.scope`, a
`WaitGroup`.

## 3. The standard streams

**Standard output is buffered**, in a buffer of 64 KiB shared by
`print_str` and its kin and by `io.stdout()`. It reaches the descriptor

- when the buffer fills;
- after a write that holds a newline, when descriptor 1 is a terminal
  (asked once, at startup);
- before the program reads standard input;
- before a child process inherits the stream;
- on `io.flush_stdout()` and `Stdout.flush()`;
- when the program ends, by any of the three ways above.

A write to the descriptor that is interrupted is made again, a short one
is continued, and a non-blocking descriptor that is full is waited for.
Any other failure drops the bytes and is remembered: the first failed
write is kept until `io.flush_stdout()` returns it, once. `print_str`
never fails visibly.

**Standard error is not buffered.** `eprint_str` and `Stderr.write` are
one system call each. The default trap handler writes there.

**Standard input** is read by `io.read_stdin`, `read_stdin_timeout` and
`io.stdin()`; each first flushes standard output, so a prompt is seen
before the program waits.

`SIGPIPE` is left as the program found it: with the default disposition a
write to a pipe nobody reads ends the process, as for any program of the
system; with the signal ignored or caught (`std/signal`) the write is the
recorded failure. Writes to sockets and to a child's pipes never raise
it.

## 4. Traps

A trap is the end of the program for a reason the language defines: an
index out of bounds, an arithmetic overflow, a division by zero, a stack
overflow, an explicit `trap()`, and the rest of `TrapKind`. It runs no
destructor, unwinds nothing and cannot be caught.

The trapping thread loads the handler (one atomic load), runs it at most
once, and ends the process. The default handler writes one line to
standard error:

```
{"trap":{"kind":"bounds","file":"app.kern","span":{"start":65,"end":70},"line":3,"col":12,"thread":0}}
```

`start` and `end` are byte offsets in `file`; `line` is the 1-based line
of the first byte and `col` its 1-based byte column; both are 0 when the
program has no line table for the file, when the offset is past its end,
and for the span 0-0, which stands for "no location". A trap raised
inside the library or the runtime names the program's call, not the
library's line. `thread` is the thread's identifier, 0 for the initial
one.

**Backtrace.** With `KERN_BACKTRACE=1`, or in a program built with
`--backtrace` and not run with `KERN_BACKTRACE=0`, the default handler
follows the record with one more line:

```
{"backtrace":{"thread":0,"frames":[{"function":"t.third","file":"bt.kern","line":2},{"function":"t.second","file":"bt.kern","line":5}]}}
```

The frames are the kern functions on the trapping thread's stack,
innermost first, at most 64 (then `"truncated":true`). Library and
runtime frames are left out, and a function the optimizer inlined has no
frame: a debug build made with `--backtrace` keeps every user function's
frame, so its line lists the calls as written. The walk follows the
frame-pointer chain and reads every word through a pipe, so a broken
chain ends the walk and cannot fault.

**Stack overflow** is a trap of kind `stack`. Each thread's stack ends in
a guard page; the fault it raises is handled on the thread's alternate
signal stack, and the handler finds the function by a marker the compiler
places before each one. A fault at any other address (a wild pointer in
an `unsafe` block, a fault in foreign code) is a trap of kind
`unreachable`: safe kern code cannot raise one. Either record names the
kern function the fault happened in, when the walk finds one, and the
span 0-0 otherwise.

**A custom handler** (`trap.set_handler`) replaces the default line with
whatever it does, and gets no backtrace. It may run in a signal handler
on an interrupted thread, so it must not allocate, lock, spawn or do
buffered output (ADR-012).

## 5. Threads and stacks

A thread is a system thread; there is no scheduler in the runtime. A
spawn acquires everything the thread needs (its record with the
arguments and the result's slot, its alternate signal stack) before the
thread exists, so a spawn that cannot have them is `Err(SpawnErr)`, has
destroyed the arguments it owned, and leaves nothing behind.

A `Thread[R]` handle that is joined gives the result. One that is
destroyed without a join detaches the thread: when its function returns,
the runtime destroys the result and frees the record, exactly once,
whichever of the two happens first.

**Stack size.** Every spawned thread has a stack of the configured size,
rounded up to a whole page, the same on every host. The size is the first
of:

1. `thread.set_stack_bytes(n)`, called earlier by the program;
2. `KERN_STACK_BYTES`;
3. the build's `--stack-bytes=N`, or `stack_bytes` in `kern.mod`;
4. 8,388,608.

Each is a number from 65,536 to 1,073,741,824. A size below what the
system accepts for a thread is raised to that (131,072 bytes on Linux
arm64); `thread.stack_bytes()` still reports the configured size. When the size comes from
the environment or the build, `main` runs on a thread with that stack
too, so that the depth a program can recurse to does not depend on the
shell's limit; if that thread cannot be created the runtime writes
`{"runtime":{"fatal":"main stack"}}` and ends with status 134.
`thread.stack_bytes()` is the size the next spawn will use;
`thread.available_parallelism()` and `thread.current_id()` complete the
queries.

## 6. Synchronization

One primitive blocks a thread: **park**. `park(a, expect)` sleeps while
an `Atomic[u32]` holds `expect`; `park_ns` also returns after a time;
`unpark_one` and `unpark_all` wake. The system compares the value and
sleeps in one step (a futex on Linux, `os_sync_wait_on_address` on
macOS), so a wake between a caller's check and its sleep is not lost. Any
return may be spurious: a caller checks its condition again.

Everything else is built on it.

| | Where | Notes |
|---|---|---|
| `Mutex[T]` | runtime | three states in one word; the uncontended lock and unlock are one atomic exchange each and make no system call. A second lock by the holder traps `explicit` |
| `Condvar` | runtime | a sequence number; `wait` and `wait_ns` release the mutex, park and take it again |
| `Shared[T]` | runtime | an atomic reference count |
| `RwLock`, `Once`, `WaitGroup`, `Channel` | `std/sync`, in kern | each counts its parked threads and wakes only when there are some |

A trap ends the program, so no lock is ever left poisoned.

## 7. Memory

`SystemAllocator` is the system's `malloc` family (hosted) or a
first-fit free list over a static arena (freestanding). Its obligations
are the caller's: a power-of-two alignment up to 4,096, a size that is a
multiple of it, and `free` and `realloc` with exactly the values of one
live allocation. The library containers are kern code over it.

**A hosted debug build checks the obligations on the block.** It keeps a
table of the live blocks, and a `free` or `realloc` whose pointer is not
a live block, or whose size or alignment is not the block's, traps
`explicit` at the call. A double free and a wrong size are found where
they happen. The interpreter makes the same checks; a release build makes
none. The freestanding allocator traps on a block that lies outside the
arena or overlaps a free block.

**Allocation report.** A hosted debug build run with
`KERN_ALLOC_REPORT=1` writes one line to standard error when the program
returns from `main` or calls `exit`:

```
{"alloc":{"total":9,"live":4,"live_bytes":4480,"peak_bytes":4480,"sites":[{"file":"t.kern","line":21,"count":1,"bytes":4096}]}}
```

`total` counts the blocks allocated, `live` and `live_bytes` those not
freed, `peak_bytes` the most that were live at once, and `sites` the up
to 16 call sites holding the most live bytes. A block belongs to the
program's call that allocated it, also when the library made the call;
the runtime's own objects (a `Shared`, a `Mutex`, a thread) are one block
each, counted at the call that made them.

`std/mem` has `PageAllocator`, which maps each allocation from the
system and unmaps it on `free`.

## 8. Files, sockets, signals, child processes, randomness

Each is a `std` module of kern code over runtime entries that are one
system call each and return the call's value or the negated error number.
The modules hold the policy.

- **Errors** are `io.IoErr`, which carries the system's number;
  `io.kind(e)` is the kind, found by the error's name and so the same on
  every host.
- **Descriptors** the runtime opens are close-on-exec and numbered above
  2. A child process is given the descriptors `std/process` names and no
  other.
- **A call that a signal interrupts is made again.** No `Interrupted`
  error reaches a program from the runtime's own calls.
- **Sockets**, TCP, UDP and Unix-domain (streams and datagrams), are
  non-blocking inside the runtime; a call that waits polls with the time
  that is left, so a timed call never outlasts its duration. A datagram
  sent to a Unix-domain socket whose queue is full is sent again each
  millisecond until it is taken: none is lost.
- **One thread can wait for many sockets** with `net.Poller`, which is
  the system's readiness queue (epoll on Linux, kqueue on macOS) as a
  value: the program watches each socket under a token, a wait returns
  those that can be read or written without waiting, and the program
  then makes the ordinary calls. Reports are level-triggered on both
  hosts, at most 64 a wait, and a socket ready both ways is one report.
  It is not a scheduler: the runtime runs nothing of the program's, and
  an event loop is a loop in the program. To wake a thread that waits,
  watch one end of `UnixStream.pair()` and write to the other.
- **A Unix-domain stream carries open files and sockets** between
  processes (`send_with`, `send_file`, `recv_with`). What arrives is a
  `Descriptor`, close-on-exec, which the program turns into the type it
  expects; the runtime asks the system what it is and refuses a wrong
  claim. On macOS, which will not say through a socket option whether a
  socket is listening, the runtime reads that from the process's own
  descriptor table. `peer_credentials` is the system's record of the
  user, group and process at the other end.
- **A Unix-domain path whose first byte is zero** names a socket in
  Linux's abstract namespace, which is in no file system. macOS has no
  such namespace, and the same call there is `Unsupported`.
- **An address** crosses the interface as 23 bytes: the family, 16 bytes
  of address, the port and, for an IPv6 address that belongs to one link,
  its scope (the interface's index).
- **Socket options** are set and read by number through two entries; the
  methods of `std/net` name them. Linger is in seconds on every system.
- **A file lock** is advisory and belongs to the open file: it ends when
  the file is closed, and closing another open of the same file does not
  end it. `File.lock` locks the whole file (`flock`); `File.lock_range`
  locks a range of its bytes (the lock of the open file description,
  `F_OFD_SETLK`), and needs the file open for reading (a shared lock) or
  for writing (an exclusive one). The two are different kinds of lock:
  macOS has each see the other, Linux does not, so a file is guarded with
  one kind.
- **A mapped file** (`mem.map_file`) is the file's own pages: a write
  through a writable mapping is a write to the file, seen at once by
  every other mapping and read of it. A byte past the end of a file cut
  short under its mapping faults, which is a trap of kind `unreachable`.
- **A caught signal runs no code of the program.** The runtime's handler
  sets a bit and writes a byte to a pipe; `Signals.wait` returns it.
  `signal.raise` flushes standard output before it sends the signal, so
  what the program wrote is seen when the signal ends it.
- **Random bytes** come from `getentropy`.

## 9. Environment variables and build options

**Environment variables**, read once by `kern_start`. A value outside
the accepted form is ignored and named in one line on standard error,
`{"runtime":{"ignored":"KERN_STACK_BYTES"}}`; an unset or empty variable
is silent; a set-user-ID or set-group-ID process reads none.

| Variable | Values | Effect |
|---|---|---|
| `KERN_BACKTRACE` | `0`, `1` | turns the backtrace of section 4 on or off, over the build's choice |
| `KERN_STACK_BYTES` | 65536 to 1073741824 | the stack of spawned threads and of `main` (section 5) |
| `KERN_ALLOC_REPORT` | `0`, `1` | the allocation report of section 7 (debug builds) |

`TMPDIR` is read by `io.make_temp_dir` and `make_temp_file`, as the
system's convention has it.

**Build options** (`kern build`; each also a key of `kern.mod` where one
is named).

| Option | `kern.mod` | Effect on the runtime |
|---|---|---|
| `--opt=debug\|release` | `opt` | the checks of section 1 |
| `--env=hosted\|freestanding` | `env` | which runtime |
| `--stack-bytes=N` | `stack_bytes` | hosted: section 5; freestanding: the size of the image's one stack, by default 65536 |
| `--backtrace` | | section 4 |
| `--test-hooks` | | links the `testing.*` entries (debug only) |
| `--board=<file.c>` | `board` | a freestanding image's board file (section 11) |
| `--linker-script=<file.ld>` | `linker_script` | a freestanding image's layout |
| `--startup=<file.S>` | `startup` | a freestanding image's start-up code, in place of the architecture's |
| `--heap-bytes=N` | `heap_bytes` | a freestanding image's heap: a positive multiple of 16 up to 1073741824, by default 1048576 |

## 10. Signals the runtime touches

| Signal | What the runtime does |
|---|---|
| `SIGSEGV`, `SIGBUS` | handled on the thread's alternate stack: a fault in the stack's guard range is a `stack` trap, any other an `unreachable` trap |
| `SIGABRT` | given the default action and raised to end a trapped program |
| `SIGCHLD` | given the default disposition at startup (a program started with it ignored could not wait for its children) |
| `SIGPIPE` | untouched; never raised by the runtime's writes to sockets and child pipes |
| the eight of `std/signal` | whatever the program asks: ignored, default, or recorded |
| every other | untouched |

A child process starts with every signal at its default, except those
the program itself was started ignoring, which stay ignored. What the
program has caught or ignored since does not pass to its children.

## 11. The freestanding runtime

A freestanding image has one thread, no files and no allocator but the
arena. The runtime is `rt.c` and holds no device: the machine is four
functions of a board file, declared in `kern_board.h`.

| Function | Does |
|---|---|
| `kern_board_init` | sets the board up; called once before anything else |
| `kern_board_putc` | writes one byte to the console |
| `kern_board_poweroff` | ends the machine with the halt code |
| `kern_board_now_ns` | the time, or 0 always |

The defaults are the QEMU `virt` machine for AArch64 targets and the
QEMU `q35` machine for x86-64 ones. `docs/freestanding-porting.md` is the
guide to writing another.

A freestanding program ends by writing `{"halt":{"code":N}}` to the
console and powering off: N is `main`'s result, `exit`'s code, or 134
after a trap, whose record goes to the console first. Stack overflow is
found by a check the compiler places in every function's prologue.
A call that reaches an entry only the hosted runtime has is refused at
compile time, with a diagnostic naming the call.

## 12. Platforms

| | Hosted | Freestanding |
|---|---|---|
| Linux, x86-64 and arm64 | glibc 2.34 or newer | |
| macOS, x86-64 and arm64 | macOS 14.4 or newer (park uses `os_sync_wait_on_address`) | |
| AArch64, no system | | QEMU `virt` by default; any board with a board file |
| x86-64, no system | | QEMU `q35` by default (a Multiboot 1 image); any board with a board file |

Every test of the runtime runs on macOS arm64 and, in a container, on
Linux arm64; CI adds Linux x86-64. Freestanding images boot under QEMU
for both architectures on both hosts.

**The interpreter** behaves as the hosted runtime does: the fixtures
give the same output and the same trap records in both. It makes the
system calls the runtime makes (a mapped file is the system's mapping, a
lock the system's lock, a socket the system's socket), and under `kern
run` a signal left at its default ends the program by that signal, as it
ends a native one. Five things follow from its being a program of
another runtime, and are properties of it, not of kern programs:

- its output is not buffered, so nothing waits for a flush;
- its memory is its host's: it writes no allocation report, and its
  freestanding emulation has no arena to exhaust;
- a `Pipe` signal that another process sends is kept by its host runtime
  and not seen (one the program raises is, and a write to a closed pipe
  or socket is an error in both engines);
- the record of a fault in mapped memory has the span 0-0, and a
  backtrace lists the interpreter's activations, which for a debug
  `--backtrace` build is the same line;
- on Linux its host's network package reads a Unix-domain path that
  begins with `@` as an abstract name, so the interpreter binds a file's
  relative path that begins with `@` as `./@...`: the same file, whose
  name a datagram's receiver sees with the `./`.

## 13. The entries

Generated from the two headers; `go test ./compiler/analysis -run
TestRuntimeEntryTable` fails when this table is not what they give.

A `(p, n)` pair is a sequence of `n` bytes, not NUL-terminated. An
`out`/`off` pair is the address of a `Result` the entry fills: `out[0]` is
the tag (0 `Ok`, 1 `Err`) and the payload goes at `out + off`. A trailing
`const kern_loc *at` is the location of the program's call, where the
entry raises its traps.

<!-- entries:begin (generated from the runtime headers: go test ./compiler/analysis -run TestRuntimeEntryTable -update-runtime-doc) -->

### The hosted runtime (`runtime/hosted/src/kern_rt.h`)

**rt_start.c: startup**

| Entry | Contract |
|---|---|
| `void kern_start(const void *abi)` | Initializes the runtime once, on the initial thread, before the program runs; abi is ignored (it makes the program reference the ABI symbol). |
| `int32_t kern_run_main(int32_t (*fn)(void))` | Runs the program's main function and returns its result, the process's exit status. |

**rt_base.c: standard output and exit**

| Entry | Contract |
|---|---|
| `void print_u64(uint64_t v)` | Writes v in decimal to standard output. |
| `void print_i64(int64_t v)` | Writes v in decimal to standard output. |
| `void print_bool(uint8_t v)` | Writes "true" or "false" to standard output. |
| `void print_str(const char *p, int64_t n)` | Writes the n bytes at p to standard output. |
| `void eprint_str(const char *p, int64_t n)` | Writes the n bytes at p to standard error, unbuffered. |
| `void println(void)` | Writes a newline to standard output. |
| `void kern_exit(int32_t code)` | Flushes standard output and ends the process with status code, running no destructor. |
| `int32_t kern_io_flush_stdout(void)` | Flushes standard output and takes the recorded write failure: 0, or -errno of the first failed write since the last call. |
| `int32_t kern_io_is_terminal(int32_t stream)` | Reports whether standard stream 0, 1 or 2 is a terminal: 1 or 0. |

**rt_io.c: std/io**

| Entry | Contract |
|---|---|
| `void kern_set_args(int32_t argc, char **argv)` | Records the program's arguments for kern_io_argc and kern_io_arg; called by the entry code before kern_start. |
| `int32_t kern_io_open(const char *p, int64_t n, uint32_t opts)` | Opens the path with std/io's OpenOptions as bits (1 read, 2 write, 4 append, 8 create, 16 truncate, 32 create_new): a descriptor above 2, close-on-exec, or -errno. |
| `int64_t kern_io_read(int32_t fd, char *buf, int64_t n)` | Reads up to n bytes from fd: the count, 0 at end of file, or -errno. |
| `int64_t kern_io_read_timeout(int32_t fd, char *buf, int64_t n, uint64_t ns)` | Reads once after waiting up to ns nanoseconds for fd to be ready: the count, 0 at end of file, -2^32 on timeout, or -errno. |
| `int64_t kern_io_write(int32_t fd, const char *p, int64_t n)` | Writes n bytes to fd (descriptor 1 through the output buffer): the count or -errno. |
| `int32_t kern_io_close(int32_t fd)` | Closes a descriptor the program opened: 0 or -errno. |
| `int64_t kern_io_seek(int32_t fd, int64_t off, int32_t whence)` | Moves a descriptor's offset (whence 0 start, 1 current, 2 end): the new offset or -errno. |
| `int32_t kern_io_sync(int32_t fd)` | Asks the system to make a file's data durable: 0 or -errno. |
| `int32_t kern_io_set_len(int32_t fd, uint64_t n)` | Sets a file's length to n bytes, zero-filling growth: 0 or -errno. |
| `int32_t kern_io_flock(int32_t fd, int32_t op)` | Takes (op 0 shared, 1 exclusive) or gives up (2) an advisory lock on an open file; 4 added returns -EWOULDBLOCK instead of waiting: 0 or -errno. |
| `int32_t kern_io_lock_range(int32_t fd, int32_t op, uint64_t start, uint64_t len)` | Takes or gives up (op as kern_io_flock's) an advisory lock on len bytes from start of an open file, len 0 for all that follow: 0 or -errno. |
| `int32_t kern_io_stat(const char *p, int64_t n, uint8_t follow, uint64_t *out, int64_t cap)` | Writes a path's metadata (kind, size, modified_ns, mode) into out[0..4], following a final link when follow is set: 0 or -errno. |
| `int32_t kern_io_fstat(int32_t fd, uint64_t *out, int64_t cap)` | As kern_io_stat for an open descriptor. |
| `int32_t kern_io_rename(const char *p, int64_t n, const char *q, int64_t m)` | Renames p to q, replacing q: 0 or -errno. |
| `int32_t kern_io_link(const char *p, int64_t n, const char *q, int64_t m)` | Makes q another name of the file p: 0 or -errno. |
| `int32_t kern_io_symlink(const char *p, int64_t n, const char *q, int64_t m)` | Makes q a symbolic link whose text is p: 0 or -errno. |
| `int32_t kern_io_chmod(const char *p, int64_t n, uint32_t mode)` | Sets the low 12 mode bits of the file at p: 0 or -errno. |
| `int64_t kern_io_readlink(const char *p, int64_t n, char *buf, int64_t cap)` | Writes a symbolic link's text into buf when it fits in cap bytes and returns its length, or -errno. |
| `int64_t kern_io_realpath(const char *p, int64_t n, char *buf, int64_t cap)` | Writes the absolute path of p, links resolved, into buf when it fits in cap bytes and returns its length, or -errno. |
| `int64_t kern_io_getcwd(char *buf, int64_t cap)` | Writes the working directory into buf when it fits in cap bytes and returns its length, or -errno. |
| `int32_t kern_io_err_kind(int32_t code)` | Returns the ordinal of std/io's ErrKind for a system error number. |
| `int32_t kern_io_err_code(int32_t kind)` | Returns the host's error number that names an ErrKind ordinal (EIO for Other). |
| `int32_t kern_io_remove(const char *p, int64_t n)` | Removes the file at the path: 0 or -errno. |
| `int32_t kern_io_mkdir(const char *p, int64_t n)` | Creates the directory at the path: 0 or -errno. |
| `int32_t kern_io_rmdir(const char *p, int64_t n)` | Removes the empty directory at the path: 0 or -errno. |
| `int32_t kern_io_kind(const char *p, int64_t n)` | Classifies the path, following links: 1 regular file, 2 directory, 3 other, or -errno. |
| `int64_t kern_io_list(const char *p, int64_t n, char *buf, int64_t cap)` | Writes the directory's entry names, sorted, each NUL-terminated, into buf when they fit in cap: the bytes needed or -errno. |
| `int64_t kern_io_temp(const char *p, int64_t n, uint8_t dir, char *buf, int64_t cap)` | Creates a fresh temporary file or directory named with the prefix and writes its path into buf: the length or -errno. |
| `int64_t kern_io_argc(void)` | Returns the number of program arguments. |
| `int64_t kern_io_arg(int64_t i, char *buf, int64_t n)` | Copies at most n bytes of argument i into buf: the argument's length, 0 when there is none. |

**rt_proc.c: std/process**

| Entry | Contract |
|---|---|
| `int64_t kern_proc_env_count(void)` | Returns the number of entries in the environment the program started with. |
| `int64_t kern_proc_env_at(int64_t i, char *buf, int64_t n)` | Copies at most n bytes of environment entry i into buf: the entry's length, 0 when there is none. |
| `int32_t kern_proc_wait(int64_t h, uint8_t block, int32_t *st, int64_t n)` | Waits for child h (block) or polls it: 1 with st filled (0 and the exit code, or 1 and the signal), 0 while it runs, or -errno. |
| `int32_t kern_proc_signal(int64_t h, int32_t sig)` | Sends signal sig to child h: 0 or -errno. |
| `uint32_t kern_proc_pid(int64_t h)` | Returns child h's process ID, 0 for an invalid handle. |
| `void kern_proc_release(int64_t h)` | Releases a child's handle; nothing to do natively. |
| `void kern_proc_flush_inherited(int64_t h)` | Waits for a child's inherited output to be forwarded; nothing to do natively. |
| `int32_t kern_proc_close(int32_t fd)` | Closes a pipe end: 0 or -errno. |
| `int64_t kern_proc_read(int32_t fd, char *buf, int64_t n)` | Reads from a pipe end, retrying when interrupted: the count, 0 at end of file, or -errno. |
| `int64_t kern_proc_write(int32_t fd, const char *data, int64_t n)` | Writes to a pipe end, retrying when interrupted, never raising SIGPIPE: the count or -errno. |
| `int32_t kern_proc_errno(uint8_t which)` | Returns the host's error number for a portable index (0 EINVAL, 1 ENAMETOOLONG, 2 EBADF, 3 EPIPE, 5 EAGAIN, else EIO). |
| `int32_t kern_proc_fault_take(uint8_t kind)` | Takes the armed fault of a pipe kind: its error number, 0 when none is armed. |
| `void kern_proc_fault_arm(uint8_t kind, int32_t code)` | Arms a fault: the next pipe operation of the kind fails with code. (A `--test-hooks` build only.) |

**rt_trap.c: traps**

| Entry | Contract |
|---|---|
| `void *kern_set_handler(void *fn)` | Installs the process-wide trap handler with one atomic exchange and returns the previous one (null: the default). |
| `void kern_set_file(const char *name)` | Records the program's file name, reported by a trap that names none. |
| `_Noreturn void kern_trap(int32_t kind, int64_t start, int64_t end, const char *file)` | Raises a trap of the kind at the span in file (null: the program's): runs the handler once and aborts; never returns. |
| `void kern_set_fn_locs(const void *t, int64_t n)` | Registers the table of function locations and names the fault handler and the backtrace read. |
| `void kern_set_lines(const void *t, int64_t n)` | Registers the program's line tables, from which a trap's line and column are computed. |
| `void kern_set_backtrace(int32_t on)` | Records that the build asked for backtraces (--backtrace); KERN_BACKTRACE overrides it either way. |

**rt_alloc.c: allocation**

| Entry | Contract |
|---|---|
| `void kern_test_fail_next_alloc(void)` | Arms a failure of the next allocation. (A `--test-hooks` build only.) |
| `void kern_test_require_hooks(void)` | Exists only with test hooks, so a program using them without the flag fails to link. (A `--test-hooks` build only.) |
| `void kern_swap(char *a, char *b, int64_t n)` | Exchanges two regions of n bytes that are the same region or do not overlap. |
| `uint32_t kern_next_identity(void)` | Issues the next container identity tag: from 1 in a debug build, 0 (unchecked) in a release build or after wrap-around. |
| `void *kern_sysalloc_new(void)` | Returns the system allocator, a process-wide object. |
| `void kern_sysalloc_alloc(kern_sysalloc *a, int64_t size, int64_t align, char *out, int64_t off, const kern_loc *at)` | Allocates size bytes at align into the Result at out: Ok(pointer) or Err(OutOfMemory); traps explicit on a violated obligation. |
| `void kern_sysalloc_realloc(kern_sysalloc *a, void *p, int64_t old_size, int64_t size, int64_t align, char *out, int64_t off, const kern_loc *at)` | Resizes an allocation, keeping the common prefix, into the Result at out; on Err the old allocation stays; a debug build traps explicit when p is not a live block of old_size and align. |
| `void kern_sysalloc_alloc_zeroed(kern_sysalloc *a, int64_t size, int64_t align, char *out, int64_t off, const kern_loc *at)` | Allocates zeroed bytes, as kern_sysalloc_alloc. |
| `void kern_sysalloc_free(kern_sysalloc *a, void *p, int64_t size, int64_t align, const kern_loc *at)` | Frees an allocation made with the same size and align; a debug build traps explicit when p is not a live block of that size and align. |

**rt_mem.c: pages from the operating system (std/mem)**

| Entry | Contract |
|---|---|
| `int64_t kern_mem_page_size(void)` | Returns the size of a page of memory in bytes. |
| `void *kern_mem_map(int64_t bytes)` | Maps bytes (a positive multiple of the page size) of zeroed, readable and writable memory: its address, or NULL. |
| `void kern_mem_unmap(void *p, int64_t bytes)` | Unmaps the bytes at p that one kern_mem_map or kern_mem_map_file call mapped. |
| `int64_t kern_mem_map_file(int32_t fd, int64_t len, uint8_t writable)` | Maps the first len bytes of an open file, shared, for reading or for reading and writing: the address or -errno. |
| `int32_t kern_mem_sync(void *p, int64_t len)` | Writes a file mapping's changed pages to its file: 0 or -errno. |

**rt_net.c: sockets (std/net)**

| Entry | Contract |
|---|---|
| `int32_t kern_net_tcp_listen(const uint8_t *addr, int64_t n, int32_t backlog)` | Makes a TCP socket bound to the address and listening with the backlog: a descriptor above 2, close-on-exec, or -errno. |
| `int32_t kern_net_udp_bind(const uint8_t *addr, int64_t n)` | Makes a UDP socket bound to the address: a descriptor or -errno. |
| `int64_t kern_net_accept(int32_t fd, uint64_t ns, uint8_t timed)` | Accepts the next connection of a listening socket: its descriptor, -errno, or -2^32. |
| `int64_t kern_net_connect(const uint8_t *addr, int64_t n, uint64_t ns, uint8_t timed)` | Connects a new TCP socket to the address: its descriptor, -errno, or -2^32 (the socket is then closed). |
| `int32_t kern_net_unix_listen(const char *p, int64_t n, int32_t backlog)` | Makes a Unix-domain stream socket bound to the path and listening with the backlog: a descriptor or -errno. |
| `int64_t kern_net_unix_connect(const char *p, int64_t n, uint64_t ns, uint8_t timed)` | Connects a new Unix-domain stream socket to the path: its descriptor, -errno, or -2^32. |
| `int32_t kern_net_poller_new(void)` | Makes a readiness queue (epoll on Linux, kqueue on macOS): a descriptor or -errno. |
| `int32_t kern_net_poller_set(int32_t p, int32_t fd, uint64_t token, int32_t interest)` | Watches fd for reading (interest 1), writing (2) or both (3) under the token, replacing what was asked of it before, or stops watching it (0; -ENOENT when it was not watched): 0 or -errno. |
| `int64_t kern_net_poller_wait(int32_t p, uint64_t *out, int64_t cap, uint64_t ns, uint8_t timed)` | Waits until a watched descriptor is ready and writes (token, flags) pairs into out, flags 1 readable, 2 writable, 4 the peer closed, 8 an error is pending: the number of pairs, 0 when the time passed, or -errno. |
| `int64_t kern_net_send_fd(int32_t fd, const char *p, int64_t n, int32_t pass)` | Sends at least one byte over a Unix-domain stream with the descriptor pass attached to the first: the count or -errno. |
| `int64_t kern_net_recv_fd(int32_t fd, char *buf, int64_t n, int32_t *got, int64_t cap, uint64_t ns, uint8_t timed)` | Receives at most n bytes and the first descriptor that came with them into got[0] (-1: none; others are closed), close-on-exec: the count, 0 when the peer closed, -errno, or -2^32. |
| `int32_t kern_net_fd_kind(int32_t fd)` | Says what an open descriptor is: 1 not a socket, 2 a TCP stream, 3 a TCP listener, 4 a UDP socket, 5 a Unix-domain stream, 6 a Unix-domain listener, 7 a Unix-domain datagram socket, 0 another socket; or -errno. |
| `int32_t kern_net_fd_adopt(int32_t fd, int32_t kind)` | Readies a received descriptor for use as the kind named (as kern_net_fd_kind's): the descriptor, or -EINVAL when it is not of that kind, or -errno; on failure it stays open. |
| `int32_t kern_net_peer_cred(int32_t fd, uint32_t *out, int64_t cap)` | Writes the user, group and process (0: not known) of a Unix-domain stream's peer, as they were when it connected, into out[0..3]: 0 or -errno. |
| `int32_t kern_net_unix_dgram(const char *p, int64_t n)` | Makes a Unix-domain datagram socket, bound to the path when n > 0: a descriptor or -errno. |
| `int64_t kern_net_unix_send_to(int32_t fd, const char *p, int64_t n, const char *path, int64_t pn)` | Sends one datagram to the socket bound to the path, waiting while its queue is full: the count or -errno. |
| `int64_t kern_net_unix_recv_from(int32_t fd, char *buf, int64_t n, char *from, int64_t fcap, uint64_t ns, uint8_t timed)` | Receives one datagram, cut to n bytes, and its sender's path into from, as its length in from[0] and then its bytes (cut at 104; length 0 for a sender with no path): the count, -errno, or -2^32. |
| `int32_t kern_net_unix_pair(int32_t *fds, int64_t cap)` | Makes two connected Unix-domain stream sockets into fds[0] and fds[1]: 0 or -errno. |
| `int64_t kern_net_recv(int32_t fd, char *buf, int64_t n, uint64_t ns, uint8_t timed)` | Receives at most n bytes: the count, 0 when the peer closed, -errno, or -2^32. |
| `int64_t kern_net_send(int32_t fd, const char *p, int64_t n)` | Sends at most n bytes, waiting while the socket's buffer is full: the count or -errno (never a SIGPIPE). |
| `int32_t kern_net_shutdown(int32_t fd, int32_t how)` | Shuts down reading (0), writing (1) or both (2): 0 or -errno. |
| `int32_t kern_net_set_nodelay(int32_t fd, uint8_t on)` | Turns TCP_NODELAY on or off: 0 or -errno. |
| `int32_t kern_net_set_opt(int32_t fd, int32_t opt, int64_t value)` | Sets a socket option (1 keep-alive, 2 receive buffer, 3 send buffer, 4 broadcast, 5 hop limit, 6 linger seconds or -1, 7 no-delay): 0 or -errno. |
| `int64_t kern_net_get_opt(int32_t fd, int32_t opt)` | Reads a socket option's value (numbered as kern_net_set_opt's): the value, or -errno - 1. |
| `uint32_t kern_net_if_index(const char *p, int64_t n)` | Finds a network interface by name: its index, or 0 when there is none. |
| `int64_t kern_net_if_name(uint32_t index, char *buf, int64_t cap)` | Writes the name of the interface with an index into buf: its length, or 0 when there is none or it does not fit. |
| `int32_t kern_net_addr(int32_t fd, uint8_t peer, uint8_t *rec, int64_t cap)` | Writes the socket's own address, or its peer's, into rec: 0 or -errno. |
| `int64_t kern_net_send_to(int32_t fd, const char *p, int64_t n, const uint8_t *addr, int64_t an)` | Sends one datagram to the address: the count or -errno. |
| `int64_t kern_net_recv_from(int32_t fd, char *buf, int64_t n, uint8_t *from, int64_t fcap, uint64_t ns, uint8_t timed)` | Receives one datagram, cut to n bytes, and its sender's record into from: the count, -errno, or -2^32. |
| `int32_t kern_net_close(int32_t fd)` | Closes a socket: 0 or -errno. |
| `int64_t kern_net_resolve(const char *host, int64_t n, uint16_t port, uint8_t *recs, int64_t cap)` | Resolves a host name: its addresses' records, each with the port, into recs while they fit; the bytes they need, or -errno. |

**rt_signal.c: std/signal**

| Entry | Contract |
|---|---|
| `int32_t kern_signal_open(void)` | Claims the one live Signals: 0, -EBUSY when one is live, or -errno. |
| `int32_t kern_signal_catch(int32_t which)` | Records deliveries of the signal from now on, remembering its disposition: 0 or -errno. |
| `void kern_signal_close(void)` | Restores every caught signal's earlier disposition, discards the records and frees the claim. |
| `int32_t kern_signal_set(int32_t which, int32_t how)` | Makes the signal ignored (how 1) or default (how 0): 0, -EBUSY when the live Signals holds it, or -errno. |
| `int64_t kern_signal_wait(uint32_t mask, uint64_t ns, uint8_t timed)` | Returns the first recorded signal of mask and forgets it, waiting for one (at most ns nanoseconds when timed, then -2^32). |
| `int32_t kern_signal_raise(int32_t which)` | Flushes standard output and sends the signal to the program's own process: 0 or -errno. |

**rt_rand.c: std/rand**

| Entry | Contract |
|---|---|
| `int32_t kern_rand_fill(char *buf, int64_t n)` | Fills n bytes from the system's random generator: 0 or -errno. |
| `void kern_test_fail_random(uint32_t after)` | Arms a failure of the random source: the request numbered after + 1 fails, once. (A `--test-hooks` build only.) |

**rt_time.c: clocks**

| Entry | Contract |
|---|---|
| `uint64_t kern_now_ns(void)` | Returns the monotonic clock in nanoseconds. |
| `void kern_time_sleep_ns(uint64_t ns, const kern_loc *at)` | Sleeps at least ns nanoseconds, resuming when interrupted. |
| `uint64_t kern_time_wall_ns(const kern_loc *at)` | Returns the wall clock in nanoseconds since the Unix epoch. |

**rt_str.c: floating-point text**

| Entry | Contract |
|---|---|
| `int64_t kern_str_f64_digits(double v, char *buf, int64_t n)` | Writes the shortest decimal digits and exponent that round-trip v into buf: the length. |
| `double kern_str_parse_f64(const char *p, int64_t n, const kern_loc *at)` | Parses validated decimal text to the nearest double; traps oom when a copy of long text cannot be allocated. |

**rt_sync.c: Shared, Mutex, Condvar**

| Entry | Contract |
|---|---|
| `void *kern_shared_new(int64_t size, const void *src, const kern_loc *at)` | Creates a Shared holding a copy of the size bytes at src, with count 1; traps oom. |
| `void kern_shared_try_new(int64_t size, void *src, void (*glue)(void *), char *out, int64_t off, const kern_loc *at)` | As kern_shared_new into the Result at out; on failure destroys the value at src with glue and stores Err(OutOfMemory). |
| `void *kern_shared_get(kern_shared *s)` | Returns the address of the shared value. |
| `void *kern_shared_clone(kern_shared *s, const kern_loc *at)` | Adds a reference and returns s; traps overflow at the maximum count. |
| `void kern_shared_drop(kern_shared *s, kern_glue g)` | Drops a reference; the last one destroys the value with g and frees the object. |
| `void kern_test_shared_seed_count(kern_shared *s, int64_t n)` | Sets a Shared's reference count. (A `--test-hooks` build only.) |
| `void *kern_mutex_new(int64_t size, const void *src, const kern_loc *at)` | Creates a Mutex holding a copy of the size bytes at src; traps oom. |
| `void kern_mutex_try_new(int64_t size, void *src, void (*glue)(void *), char *out, int64_t off, const kern_loc *at)` | As kern_mutex_new into the Result at out; on failure destroys the value at src with glue and stores Err(OutOfMemory). |
| `void *kern_mutex_lock(kern_mutex *mx, const kern_loc *at)` | Acquires the lock, blocking, and returns the guarded value's address; traps explicit when the caller holds it. |
| `void kern_mutex_unlock(kern_mutex *mx)` | Releases the lock the caller holds and wakes one waiter. |
| `int32_t kern_mutex_try_lock(kern_mutex *mx)` | Acquires the lock only if it is free: 1, or 0 when any thread holds it, the caller included. |
| `void *kern_mutex_data(kern_mutex *mx)` | Returns the guarded value's address, for a caller that holds the lock. |
| `void kern_park(_Atomic(uint32_t) *w, uint32_t expect)` | Blocks the calling thread while the 32-bit word at w equals expect; may return at any time, and a wake between the caller's check and the sleep is not lost. |
| `int32_t kern_park_ns(_Atomic(uint32_t) *w, uint32_t expect, uint64_t ns)` | As kern_park, returning also after ns nanoseconds: 0 only when the time elapsed, else 1. |
| `void kern_unpark_one(_Atomic(uint32_t) *w)` | Wakes one thread parked on w. |
| `void kern_unpark_all(_Atomic(uint32_t) *w)` | Wakes every thread parked on w. |
| `void kern_mutex_drop(kern_mutex *mx, kern_glue g)` | Destroys the guarded value with g and frees the lock. |
| `void *kern_condvar_new(const kern_loc *at)` | Creates a Condvar; traps oom. |
| `void kern_condvar_try_new(char *out, int64_t off, const kern_loc *at)` | As kern_condvar_new into the Result at out: Ok(pointer) or Err(OutOfMemory). |
| `void kern_condvar_wait(kern_condvar *cv, void *data, const kern_loc *at)` | Releases the Mutex guarding data, blocks until notified (or spuriously), and reacquires it; traps explicit on a lock not held or a second Mutex. |
| `int32_t kern_condvar_wait_ns(kern_condvar *cv, void *data, uint64_t ns, const kern_loc *at)` | As kern_condvar_wait, returning also after ns nanoseconds with the lock held again: 0 only when the time elapsed, else 1. |
| `void kern_condvar_notify_one(kern_condvar *cv)` | Wakes one thread waiting on the Condvar. |
| `void kern_condvar_notify_all(kern_condvar *cv)` | Wakes every thread waiting on the Condvar. |
| `void kern_condvar_drop(kern_condvar *cv)` | Frees the Condvar. |

**rt_thread.c: threads**

| Entry | Contract |
|---|---|
| `void kern_test_fail_next_spawn(void)` | Arms a failure of the next spawn. (A `--test-hooks` build only.) |
| `void kern_thread_spawn(void (*thunk)(void *), const void *pack, int64_t size, int64_t roff, char *out, int64_t off, const kern_loc *at)` | Starts a thread running thunk on a copy of the size-byte argument block, into the Result at out: Ok(handle) or Err(SpawnErr) with nothing left behind. |
| `void kern_thread_join(kern_thread *t, void *dst, int64_t size)` | Waits for the thread, copies its size-byte result to dst and frees the handle. |
| `void kern_thread_drop(kern_thread *t, kern_glue g)` | Destroys an unjoined handle: a running thread is detached and destroys its result with g when it ends; a finished one's result is destroyed with g now. |
| `void kern_set_stack_bytes(int64_t n)` | Records the build's stack size, when in range; called by the entry code before kern_start. |
| `uint64_t kern_thread_stack_bytes(void)` | Returns the stack size the next spawned thread gets, a whole number of pages. |
| `int32_t kern_thread_set_stack_bytes(uint64_t n)` | Sets the stack size of threads spawned from now on: 1, or 0 with nothing changed when n is outside 65,536 to 1,073,741,824. |
| `uint64_t kern_thread_current_id(void)` | Returns the calling thread's identifier, the one a trap record carries (0 for the thread that runs main). |
| `uint64_t kern_thread_available_parallelism(void)` | Returns the number of processors the process may run on, at least 1. |
| `void *kern_scope_new(const kern_loc *at)` | Creates a scope for scoped spawns; traps oom. |
| `void kern_scope_spawn(kern_scope *sc, void (*thunk)(void *), const void *pack, int64_t size, int64_t roff, char *out, int64_t off, const kern_loc *at)` | Starts a scoped thread, as kern_thread_spawn, into the Result at out: Ok(join handle) or Err(SpawnErr). |
| `void kern_join_get(kern_join *j, void *dst, int64_t size)` | Waits for a scoped thread and copies its size-byte result to dst. |
| `void kern_join_drop(kern_join *j, kern_glue g)` | Destroys a join handle; a result not taken is destroyed with g after the thread ends. |
| `void kern_scope_drop(kern_scope *sc)` | Joins every thread of the scope in creation order and frees it. |
| `void kern_yield(void)` | Yields the processor to another thread. |

### The freestanding runtime (`runtime/freestanding/src/kern_rt_fs.h`)

**rt.c: startup, console, exit**

| Entry | Contract |
|---|---|
| `void kern_start(const void *abi)` | Initializes the runtime before the program runs: it sets the board up (kern_board_init); abi is ignored (it makes the image reference the ABI symbol). |
| `int32_t kern_run_main(int32_t (*fn)(void))` | Runs the program's main function and returns its result, the halt code. |
| `void print_u64(uint64_t v)` | Writes v in decimal to the console. |
| `void print_i64(int64_t v)` | Writes v in decimal to the console. |
| `void print_bool(uint8_t v)` | Writes "true" or "false" to the console. |
| `void print_str(const char *p, int64_t n)` | Writes the n bytes at p to the console. |
| `void eprint_str(const char *p, int64_t n)` | Writes the n bytes at p to the console (there is one device). |
| `void println(void)` | Writes a newline to the console. |
| `_Noreturn void kern_exit(int32_t code)` | Writes the halt line and powers the machine off; never returns. |
| `void kern_yield(void)` | Does nothing: there is one thread. |

**rt.c: traps**

| Entry | Contract |
|---|---|
| `void kern_set_file(const char *name)` | Records the program's file name, reported by a trap that names none. |
| `void kern_set_lines(const void *t, int64_t n)` | Registers the program's line tables, from which a trap's line and column are computed. |
| `void *kern_set_handler(void *fn)` | Installs the trap handler and returns the previous one (null: the default). |
| `_Noreturn void kern_trap(int32_t kind, int64_t start, int64_t end, const char *file)` | Raises a trap of the kind at the span in file (null: the program's): writes the record or runs the handler once, then halts with code 134; never returns. |

**rt.c: the C library symbols LLVM may call**

| Entry | Contract |
|---|---|
| `double trunc(double x)` | Rounds toward zero. |
| `float truncf(float x)` | Rounds toward zero. |
| `void *memcpy(void *d, const void *s, size_t n)` | Copies n bytes; the ranges must not overlap. |
| `void *memmove(void *d, const void *s, size_t n)` | Copies n bytes; the ranges may overlap. |
| `void *memset(void *d, int c, size_t n)` | Fills n bytes with c. |

**rt.c: allocation**

| Entry | Contract |
|---|---|
| `void kern_swap(char *a, char *b, int64_t n)` | Exchanges two regions of n bytes that are the same region or do not overlap. |
| `uint32_t kern_next_identity(void)` | Issues the next container identity tag: from 1 in a debug build, 0 (unchecked) in a release build or after wrap-around. |
| `void *kern_sysalloc_new(void)` | Returns the system allocator, one object over the static arena. |
| `void kern_sysalloc_alloc(kern_sysalloc *a, int64_t size, int64_t align, char *out, int64_t off, const kern_loc *at)` | Allocates size bytes at align into the Result at out: Ok(pointer) or Err(OutOfMemory); traps explicit on a violated obligation. |
| `void kern_sysalloc_alloc_zeroed(kern_sysalloc *a, int64_t size, int64_t align, char *out, int64_t off, const kern_loc *at)` | Allocates zeroed bytes, as kern_sysalloc_alloc. |
| `void kern_sysalloc_realloc(kern_sysalloc *a, void *p, int64_t old_size, int64_t size, int64_t align, char *out, int64_t off, const kern_loc *at)` | Resizes an allocation, keeping the common prefix, into the Result at out; on Err the old allocation stays. |
| `void kern_sysalloc_free(kern_sysalloc *a, void *p, int64_t size, int64_t align, const kern_loc *at)` | Frees an allocation made with the same size and align; traps explicit for a block the arena does not hold. |

**rt.c: clock**

| Entry | Contract |
|---|---|
| `uint64_t kern_now_ns(void)` | Returns a monotonic clock in nanoseconds. |
| `void kern_time_sleep_ns(uint64_t ns, const kern_loc *at)` | Busy-waits until the board's clock has advanced by at least ns nanoseconds; returns at once on a board without a clock. |

**f64text.c: floating-point text (linked only into an image that calls it)**

| Entry | Contract |
|---|---|
| `int64_t kern_str_f64_digits(double v, char *buf, int64_t n)` | Writes the shortest decimal digits and exponent that round-trip v into buf: the length. |
| `double kern_str_parse_f64(const char *p, int64_t n, const kern_loc *at)` | Parses validated decimal text to the nearest double; never traps. |

<!-- entries:end -->
