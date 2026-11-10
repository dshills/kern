# Spec: the kern runtime, complete

**Version:** 0.7. **Status:** implemented (`specs/runtime/STATUS.md`). **Date:** 2026-11-09.

## 0. Goal

Bring kern's runtime to the standard of a commercial release: complete for
the programs a systems language is used to write, specified, checked against
the compiler mechanically, hardened, observable when a program fails, and
documented. Spec §6 of `specs/initial` fixes the philosophy and this work
keeps it: no garbage collector, no scheduler, traps that abort, errors as
values, allocation through explicit allocators.

"The runtime" is everything a kern program needs beyond its own compiled
code:
- the C code linked into every program (`runtime/hosted`, `runtime/freestanding`);
- its mirror in the reference interpreter (`compiler/interp`), which must
  behave the same;
- the standard-library modules that are the program's interface to it
  (`std/core.kern` and the modules under `std/`).

An audit on 2026-11-09 (the state at `a78f7c3`) found the runtime correct for
what it does and small for what a release needs:
- one 1,443-line C file with no declared interface; the compiler's
  declarations of its entries are strings scattered through the emitter, and
  nothing checks that the two agree (they do not: small integer arguments
  are passed without the extension the C side assumes);
- a `Thread` dropped without `join` leaks its result, which rule 5.5.2 says
  the thread destroys;
- spawned threads get the platform's default stack (512 KiB on macOS, 8 MiB
  on Linux) and nothing can change it;
- standard output is flushed only when 64 KiB accumulate or the program
  ends, so a prompt written before reading standard input is not seen, and
  a write interrupted by a signal loses output;
- a trap names a byte span, with no line, no function and no call chain;
- a lock cannot be tried and a wait cannot time out;
- files can only be read or written whole; there are no sockets, no signal
  handling, no operating-system randomness and no portable error kinds
  (`IoErr.Other` carries the platform's own error number);
- the freestanding runtime is fixed to two QEMU machines and a 1 MiB heap.

## 1. Terms

- **Entry**: a function the runtime defines for compiled code to call
  (`kern_mutex_lock`, `print_u64`).
- **ABI**: the entries' names, C prototypes and calling convention.
- **Hosted**: a program running as a process under Linux or macOS.
  **Freestanding**: a bare-metal image with no operating system.
- **Engine**: the interpreter or the native backend. **Both engines** means
  the interpreter and a native build at debug and at release.
- **Debug build**, **release build**: `--opt=debug` (the default) and
  `--opt=release`.
- **Trap record**: the JSON line the default trap handler writes.
- **Corpus**: every program under `tests/`, `examples/` and `bench/kern`
  that the fixture harness and the hosted-only inventory already lower.
- **Gate**: a check in §4 that must pass before a phase is committed.
- **EOF**: end of file. **TTY**: a terminal device. **CSPRNG**: the
  operating system's cryptographically secure random source. **PRNG**: a
  deterministic pseudo-random generator. **TCP**, **UDP**, **DNS**: the
  internet protocols of those names. **OOM**: out of memory. **TLS**:
  thread-local storage. **ASan**, **UBSan**: clang's address and
  undefined-behaviour sanitizers. **errno**: the operating system's error
  number.

## 2. Non-goals

- A garbage collector, green threads, an asynchronous I/O scheduler,
  recoverable panics or unwinding (spec §2.2 and ADR-006 stand).
- Windows, and hosted targets other than Linux (glibc 2.34 or newer) and
  macOS (14.4 or newer) on x86-64 and arm64.
- Protocols above the transport layer (TLS encryption, HTTP), compression,
  cryptography other than reading the operating system's random source.
- Dynamic loading of code, a plugin interface, hot reload.
- Call-site line numbers in a backtrace (that needs debug information at
  run time); a backtrace names functions and their declaration lines.
- A backtrace or an allocation report in freestanding images or in the
  interpreter's allocation model beyond what §3 states.
- Trap containment in freestanding images (ADR-015 option D stays deferred).
- Changing the performance of code that does not use the new facilities:
  the benchmark workloads' release instruction counts stay within 1% (R-P1).
- Pushing to origin or publishing a release.

## 3. Requirements

Each area below is one phase of the plan. Signatures are kern. Where the
language cannot express a signature as written, the phase may change the
language (the owner authorized this on 2026-11-09), recording the change in
`docs/ownership.md`, `spec/` and the phase report; it may also adjust a
signature's shape, recording the difference in `specs/runtime/STATUS.md`,
as long as the stated behaviour is kept.

### A. Structure and ABI

**RA1. Source layout (an implementation constraint, not a behaviour).**
`runtime/hosted/src/kern_rt.c` stays the one file
clang compiles and becomes a list of `#include`s of units, each one area:
`rt_base.c` (configuration, standard streams, exit), `rt_trap.c`,
`rt_alloc.c`, `rt_sync.c`, `rt_thread.c`, `rt_io.c`, `rt_proc.c`,
`rt_time.c`, `rt_str.c`, and the units later phases add. `kern_rt.h`
declares every entry with its C prototype and a one-line contract, grouped
by unit; every unit's functions that are not entries are `static`.
`runtime/freestanding/src/kern_rt_fs.h` does the same for `rt.c` and
`f64text.c`. The runtime-object cache keeps working: an edit to any unit or
header rebuilds the object.

**RA2. Warning-clean.** Both runtimes compile without a diagnostic under
`-std=c11 -Wall -Wextra -Wshadow -Wmissing-prototypes -Wstrict-prototypes
-Wvla -Werror`, hosted on the host triple at debug and release with and
without `KERN_TEST_HOOKS`, freestanding on both bare-metal triples. A Go test
runs the compiles and is skipped only when clang is absent.

**RA3. ABI conformance.** A Go test proves that the compiler and the
runtimes agree on every entry:
- it obtains each runtime's definitions as LLVM IR from clang (`-S
  -emit-llvm`) and reads every defined external function's return type,
  parameter types and `zeroext`/`signext` attributes;
- it lowers the corpus with the emitter recording every runtime
  declaration it makes, hosted and freestanding;
- every recorded declaration must name a defined entry and match it in
  return type, parameter count, parameter types and extension attributes,
  on arm64 and on x86-64 (the test compiles the runtime for both
  architectures of the host's operating system);
- every defined entry the corpus never declares must be on a list in the
  test with a reason (a test hook, an entry only the startup code calls).
The emitter is corrected until the test passes; in particular every `i8`,
`i16` and `i1` argument and result carries the extension clang gives it.

**RA4. ABI version.** `kern_rt.h` defines `KERN_RT_ABI` (this work makes it
`2`), the hosted and freestanding runtimes define the symbol
`kern_rt_abi_2`, and every program's entry code references it, so that a
program linked against a runtime of another ABI version fails to link. The
number changes when an existing entry's prototype or contract changes.

**RA5. Sanitized runs.** `native.Options` gains a sanitizer mode that builds
the program and the runtime with ASan and UBSan. A test, run when
`KERN_SANITIZE=1`, runs every `tests/run` and `tests/concurrency` fixture
natively at debug under it and requires the fixture's expected output and
no sanitizer report. Fixtures whose expected outcome is a trap are run only
when the trap is raised by a check instruction (not by a fault the sanitizer
intercepts). Every report it finds in the runtime or in the standard
library is fixed. A CI job runs it on Linux. The phase that introduces
the test fixes every report except those a later requirement of this spec
owns (RC1's leak), which the test lists by fixture with the requirement;
RA5 is complete, and gate 8 passes, only when that list is empty.

**RA6. Static analysis.** `clang --analyze` over both runtimes reports
nothing; a Go test runs it and is skipped when the analyzer is unavailable.

### B. Process lifecycle and standard streams

**RB1. Configuration.** The hosted runtime reads its environment variables
once, in `kern_start`, before the program runs: `KERN_BACKTRACE`,
`KERN_STACK_BYTES`, `KERN_ALLOC_REPORT`. A process running with elevated
privilege (set-user-ID or set-group-ID; `AT_SECURE` on Linux,
`issetugid()` on macOS) ignores all of them. Accepted values: `KERN_BACKTRACE` and
`KERN_ALLOC_REPORT` are exactly `0` or `1`; `KERN_STACK_BYTES` is a decimal
integer of 1 to 10 digits, no sign or space, from 65,536 to 1,073,741,824.
A variable set to anything else is ignored, and the runtime writes one line
`{"runtime":{"ignored":"<NAME>"}}` to standard error, before the program
runs. An unset or empty variable is not an error.

**RB2. Standard output policy.** Bytes written to standard output are
buffered and reach the descriptor in the order written. The buffer is
flushed:
- when it is full;
- after a write that contains a newline, when standard output is a TTY;
- before the program reads standard input (`io.read_stdin`, the stdin
  reader of RG, `read_stdin_timeout`);
- before a child process is started with an inherited standard output or
  standard error, as now;
- by `io.flush_stdout()`;
- when the program ends by returning from `main`, by `exit`, or by a trap.
Standard error is unbuffered. The interpreter produces the same bytes in the
same order on each stream.

**RB3. Robust writes.** A write to a standard stream is retried when
interrupted (`EINTR`), continued after a short write, and, when the
descriptor is non-blocking and not ready (`EAGAIN`), waits for it. Any
other failure is recorded (the first one is kept), and the bytes are
dropped. `io.flush_stdout() -> Result[(), IoErr]` flushes and returns and
clears the recorded failure. `print_*` and `println` never trap and never
fail visibly. The exit status is not changed by a recorded failure. The
runtime does not change the disposition of `SIGPIPE`: with the system
default, a write to a pipe with no reader ends the process by that signal,
as for any Unix program; when it is ignored (inherited so, or by
`signal.ignore(Pipe)` of RI1) the write is a recorded failure of kind
`BrokenPipe`.

**RB4. Terminal query.** `io.is_terminal(s: Stream) -> bool` with `union
Stream { Stdin; Stdout; Stderr }` reports whether the stream is a TTY.

**RB5. Exit.** Documented and tested: `exit(code)` flushes standard output
and ends the process with the low 8 bits of `code`, running no destructor;
returning `n` from `main` does the same with `n`; a trap ends the process by
`SIGABRT`; threads still running do not delay any of them.

### C. Threads

**RC1. Detached threads.** A `Thread[R]` destroyed without `join` detaches
its thread. When the thread's function returns, the runtime destroys the
result with `R`'s destructor and frees every byte the thread's record,
arguments and result occupied (rule 5.5.2). The result is destroyed exactly once whichever happens
first: when the handle is destroyed after the function returned, the
destruction of the handle destroys the result and frees the storage at
once. In both engines. A sanitized
run (RA5) of the fixture reports no leak.

**RC2. Stack size.** Every spawned thread has a stack of the configured
size, rounded up to a whole page, the same on Linux and macOS. The size is
the first of: `thread.set_stack_bytes(n: usize) -> bool` called earlier by
the program (it returns false and changes nothing when `n` is below 65,536
or above 1,073,741,824); `KERN_STACK_BYTES` (RB1's range); the build's
`--stack-bytes=N` (`kern build`, `kern run`, and `stack_bytes` in
`kern.mod`: a decimal integer in the same range, anything else is the
diagnostic `CLI_INVALID_FLAGS`, or `PROJ_MANIFEST_INVALID` for the
manifest, and nothing is built; the flag overrides the manifest, as for
every other key); and otherwise 8,388,608, the
interpreter's default. `thread.stack_bytes() -> usize`
returns the size the next spawn will use. The setting is process-wide: a
spawn uses the value of the last `set_stack_bytes` that returned true
before the spawn began, and when calls race, the value of one of them. When the size comes from the
environment or the build option, the program's `main` also runs on a thread
with that stack, so that recursion depth does not depend on the shell's
stack limit; otherwise `main` runs on the process's initial stack. If the runtime cannot
create that thread, it writes `{"runtime":{"fatal":"main stack"}}` to
standard error and ends the process with status 134 without running `main`.
Stack
exhaustion on any thread is a `stack` trap, as now. The interpreter's
simulated stack limit follows the same sources.

**RC3. Spawn robustness.** Everything a thread needs (its record, the
argument block, the alternate signal stack) is acquired before the thread
is created. A spawn that cannot acquire them, or whose thread the system
refuses, returns `Err(SpawnErr)`, has destroyed the owned arguments, and
leaks nothing. The alternate signal stack is released when the thread ends.

**RC4. Queries.** `thread.available_parallelism() -> usize` is the number
of processors the process may run on, at least 1. `thread.current_id() ->
u64` is the calling thread's identifier, the value a trap record carries
(0 for the thread that runs `main`).

### D. Synchronization

**RD1. Park.** The prelude gains four hosted-only intrinsics over an
`Atomic[u32]`:
- `park(a: view[Atomic[u32]], expect: u32)` blocks the calling thread while
  the atomic's value equals `expect`; it may return at any time (a caller
  re-checks its condition);
- `park_ns(a: view[Atomic[u32]], expect: u32, ns: u64) -> bool` is `park`
  that also returns after `ns` nanoseconds; it returns false only when the
  time elapsed;
- `unpark_one(a: view[Atomic[u32]])` and `unpark_all(a: view[Atomic[u32]])`
  wake one or every thread parked on the atomic.
A wake between a parker's check and its sleep is not lost. They are safe
operations.

**RD2. Timed wait.** `Condvar.wait_ns(view self, g: edit[T], ns: u64) ->
bool` is `wait` that also returns when `ns` nanoseconds have passed; the
lock is held again when it returns; false means the time elapsed. The traps
of `wait` apply.

**RD3. Try lock.** `Mutex.try_with(view self, f: closure(edit[T])) -> bool`
acquires the lock only if it is free, calls `f` with the guarded value,
releases the lock and returns true; it returns false, without calling `f`,
when the lock is held (by any thread, the caller included: no trap).

**RD4. Conditional marker assertions.** `unsafe assert Send for T` and
`unsafe assert Share for T` may carry a `where` clause over the type's
parameters (`unsafe assert Share for Channel where T: Send`); the type has
the marker only for arguments that satisfy it. `Send` and `Share` become
usable as constraints there. This is a language change (grammar, rule
5.5.1).

**RD5. `std/sync`.** A new module, written in kern over RD1, hosted only:
- `RwLock[T]`: `new(v: own T)`, `read(view self) -> view[T] release
  read_unlock`, `write(view self) -> edit[T] release write_unlock`. Any
  number of readers or one writer; a waiting writer blocks new readers.
  `Share` when `T: Send + Share`, `Send` when `T: Send`.
- `Once`: `new()`, `call(view self, f: closure())` runs `f` exactly once
  across all threads; every other caller returns after that run has
  finished. `is_done(view self) -> bool`.
- `WaitGroup`: `new()`, `add(view self, n: u32)`, `done(view self)`,
  `wait(view self)` blocks until the count is zero; `done` below zero traps
  `explicit`, and so does an `add` that would take the count above
  4,294,967,295.
- `Channel[T]`: a bounded first-in-first-out queue for any number of
  senders and receivers. `new(capacity: usize)` (capacity 0 traps
  `explicit`); `send(view self, v: own T) -> Result[(), T]` blocks while
  full and returns `Err(v)` when closed; `try_send(view self, v: own T) ->
  Result[(), T]` returns `Err(v)` when full or closed; `recv(view self) ->
  Option[T]` blocks while empty and returns `None` when closed and empty;
  `try_recv(view self) -> Option[T]` (never blocks); `recv_ns(view self,
  ns: u64) -> Option[T]` (waits at most `ns` nanoseconds, `0` polls once;
  `None` when the time passed with the channel empty, or when it is closed
  and empty; a call that returns `None` has removed nothing); `close(view self)` wakes every blocked thread; `is_closed`,
  `len`. Values left in a destroyed channel are destroyed. `Share` and
  `Send` when `T: Send`.
Each is tested in both engines with several threads, and by a native stress
test under the thread sanitizer.

### E. Failure diagnostics

**RE1. Line and column.** The trap record becomes
`{"trap":{"kind":K,"file":F,"span":{"start":S,"end":E},"line":L,"col":C,"thread":T}}`.
`L` is the 1-based line of byte `S` in `F` and `C` the 1-based byte column;
both are 0 when the runtime has no line table for `F` or `S` is greater
than the file's length in bytes (`S` equal to the length is the position
after the last byte). The compiler emits, per source file with code, a table of line-start
offsets; computing `L` and `C` allocates nothing and takes no lock. The
hosted runtime, the freestanding runtime and the interpreter write the same
record for the same trap. `TrapInfo` is unchanged.

**RE2. Backtrace.** When enabled, the default trap handler of a hosted
native program writes, after the trap record, one line
`{"backtrace":{"thread":T,"frames":[{"function":N,"file":F,"line":L},...]}}`:
the kern functions on the trapping thread's stack, innermost first, at most
64 (then `"truncated":true` follows `frames`). `N` is the function's
qualified name, `L` its declaration's line. Frames of library functions and
of C code are left out. It is enabled by `KERN_BACKTRACE=1` or by building
with `--backtrace`; `KERN_BACKTRACE=0` disables it. The walk reads only
memory it has verified readable, so it cannot fault. With the same setting
the interpreter writes a line of the same shape from its own frames. A
release build's line lists the functions that still have frames after
inlining.

**RE3. Parity.** Without `KERN_BACKTRACE` and `--backtrace`, the default
trap handler writes exactly one line, the trap record, identical in both
engines except where the existing rules already allow a difference (stack
traps). Lines the runtime writes for other reasons (RB1's `ignored` line,
RF2's report) are not the handler's and are outside this rule.

### F. Memory

**RF1. Checked allocator.** In a hosted debug build, `SystemAllocator.free`
and `SystemAllocator.realloc` trap `explicit`, at the call, when the pointer
is not a live allocation of the allocator or the size or alignment differs
from the allocation's. The interpreter traps on the same conditions. A
release build does not check (§6 makes them the caller's obligations). The
library containers pass these checks in every corpus program.

**RF2. Allocation report.** With `KERN_ALLOC_REPORT=1`, a hosted debug
build writes to standard error, when the program ends by returning from
`main` or by `exit`, one line
`{"alloc":{"total":N,"live":N,"live_bytes":N,"peak_bytes":N,"sites":[{"file":F,"line":L,"count":N,"bytes":N},...]}}`
with the allocations made, those still live, and the up to 16 call sites
holding the most live bytes. The counters: `total` counts every successful
`alloc`, `alloc_zeroed` and runtime-object allocation, and a successful
`realloc` only when it moved the block; `live` and `live_bytes` are the
blocks not yet freed and the sum of their requested sizes; `peak_bytes` is
the largest `live_bytes` reached, sampled after every successful
allocation and reallocation (a reallocation first subtracts the old size);
a failed call changes nothing. A block belongs to the call site that
allocated it, and a reallocation leaves it there. Sites are ordered by
bytes, largest first, ties by file then line. Runtime objects (`Shared`,
`Mutex`, threads) count at the call that created them. Without the variable nothing is
written and nothing is counted beyond what RF1 needs.

**RF3. Page allocator.** `std/mem` (hosted only): `page_size() -> usize`
and `PageAllocator`, an `Allocator` whose `alloc` maps whole pages from the
operating system (zeroed, aligned to the page size) and whose `free` unmaps
them; an alignment above the page size is `Err(OutOfMemory)`. It works as
the allocator of `VecIn`, `BoxIn`, `ArenaIn`, `PoolIn` and `MapIn`.

### G. Files and streams (`std/io`)

**RG1. Error kinds.** `union ErrKind { NotFound; PermissionDenied;
AlreadyExists; InvalidInput; IsADirectory; NotADirectory;
DirectoryNotEmpty; BrokenPipe; ConnectionRefused; ConnectionReset;
ConnectionAborted; NotConnected; AddrInUse; AddrNotAvailable; TimedOut;
Interrupted; WouldBlock; OutOfMemory; Unsupported; Other }` and
`kind(e: view[IoErr]) -> ErrKind`, the same for the same failure on Linux
and macOS and in both engines. `IoErr` is unchanged. The mapping is by the
system's error name, whatever its number on the host: `ENOENT` NotFound;
`EACCES`, `EPERM` PermissionDenied; `EEXIST` AlreadyExists; `EINVAL`,
`ENAMETOOLONG` InvalidInput; `EISDIR` IsADirectory; `ENOTDIR`
NotADirectory; `ENOTEMPTY` DirectoryNotEmpty; `EPIPE` BrokenPipe;
`ECONNREFUSED`, `ECONNRESET`, `ECONNABORTED`, `ENOTCONN`, `EADDRINUSE`,
`EADDRNOTAVAIL`, `ETIMEDOUT`, `EINTR` their namesakes; `EAGAIN`,
`EWOULDBLOCK` WouldBlock; `ENOMEM` OutOfMemory; `ENOSYS`, `ENOTSUP`,
`EOPNOTSUPP` Unsupported; every other error Other. The `IoErr` variants
`NotFound`, `Permission` and `Exists` map to their kinds.

**RG2. `File`.** `open(path: view[]u8, o: OpenOptions) -> Result[File,
IoErr]` with `OpenOptions { read, write, append, create, truncate,
create_new: bool }` and the shorthands `open_read(path)` (read), `create(path)`
(write, create, truncate) and `open_append(path)` (append, create; an
existing file is kept and every write goes to its end). Methods:
`read(edit self, buf: edit[]u8) -> Result[usize, IoErr]` (0 at EOF),
`write(edit self, data: view[]u8) -> Result[usize, IoErr]`, `write_all`,
`read_all(edit self) -> Result[Vec[u8], IoErr]`, `seek(edit self, to:
SeekFrom) -> Result[u64, IoErr]` with `union SeekFrom { Start(u64);
Current(i64); End(i64) }`, `sync(edit self)`, `set_len(edit self, n: u64)`,
`metadata(view self)`, `close(own self) -> Result[(), IoErr]`; the
destructor closes. Files are opened close-on-exec on descriptors above 2. An
invalid option set (neither read nor write nor append; truncate without
write; truncate with append; `create` or `create_new` without write or
append) is `Err` of kind `InvalidInput` without a system call.
`create_new` creates the file and fails with kind `AlreadyExists` when the
path exists (a symbolic link included); with it, `create` and `truncate`
have no further effect. `File` has no user-space buffer: `flush` (RG4)
does nothing and returns `Ok`, and only `sync` asks the system to make the
data durable.

**RG3. Metadata and paths.** Every path parameter is `view[]u8`, and every
operation that can fail returns `Result[_, IoErr]` whose error is the
system's (a missing path is kind `NotFound`, a refused one
`PermissionDenied`, a path with a NUL byte or of 4,096 bytes or more
`InvalidInput`):
- `metadata(path) -> Result[Metadata, IoErr]` (following symbolic links)
  and `symlink_metadata(path) -> Result[Metadata, IoErr]` (not following),
  with `Metadata { kind: FileKind; size: u64; modified_ns: u64; mode: u32
  }`: the size in bytes, the modification time in nanoseconds since the
  Unix epoch (0 for a time before it, 18,446,744,073,709,551,615 for one
  too late to represent), and the permission bits (the low 12 bits of the mode).
  `FileKind` gains `Symlink`, which only `symlink_metadata` reports.
- `rename(from, to) -> Result[(), IoErr]`, `hard_link(src, dst) ->
  Result[(), IoErr]`, `symlink(target, link) -> Result[(), IoErr]`,
  `set_mode(path, mode: u32) -> Result[(), IoErr]`.
- `read_link(path) -> Result[Vec[u8], IoErr]`, `canonicalize(path) ->
  Result[Vec[u8], IoErr]`, `current_dir() -> Result[Vec[u8], IoErr]`.
- `exists(path) -> bool` is true exactly when `metadata(path)` is `Ok`.
- `copy_file(from, to) -> Result[u64, IoErr]` creates or truncates `to`,
  copies the bytes and returns their count; on failure `to` may hold a
  prefix.
`File.sync`, `File.set_len` and `File.metadata` (RG2) return `Result[(),
IoErr]`, `Result[(), IoErr]` and `Result[Metadata, IoErr]`.

**RG4. Readers and writers.** `interface Reader { func read(edit self, buf:
edit[]u8) -> Result[usize, IoErr] }` and `interface Writer { func
write(edit self, data: view[]u8) -> Result[usize, IoErr]; func flush(edit
self) -> Result[(), IoErr] }`, `Reader` is implemented by `File`, `Stdin` (from `io.stdin()`),
`process.Pipe` and `net.TcpStream`; `Writer` by `File`, `Stdout` and
`Stderr` (from `io.stdout()`, `io.stderr()`), `process.Pipe` and
`net.TcpStream`. A `Pipe` used against its direction returns the system's
error, as its own methods do. `Stdout.write` buffers under RB2 and returns
`Ok(len(data))`; `Stdout.flush` is `io.flush_stdout` (RB3), sharing its
recorded failure; `Stderr.write` returns the system's result and its
`flush` does nothing. `BufReader[R: Reader]` with
`new(r: own R)`, `read`, `read_line(edit self, line: edit[Vec[u8]]) ->
Result[bool, IoErr]` (appends the next line without its terminator, which is
`\n` or `\r\n`; a final line with no terminator is a line; false at EOF
when nothing was read) and `into_inner(own self) -> R` (bytes read
ahead are discarded); `BufWriter[W: Writer]` with
`new(w: own W)`, `write`, `flush`, `into_inner(own self) -> Result[W,
IoErr]` (flushes first; on failure the writer and the unwritten bytes are
destroyed and the error returned), whose destructor flushes
and discards a failure of that flush (a program that must see it calls
`flush` or `into_inner` first; the reference says so).
`Stdin.read` follows RB2.

### H. Networking (`std/net`, hosted only)

**RH1. Addresses.** `union IpAddr { V4(a: [4]u8); V6(a: [16]u8) }`, `struct
SocketAddr { ip: IpAddr; port: u16 }`, `parse_addr(text: view[]u8) ->
Option[SocketAddr]` for `a.b.c.d:port` and `[v6]:port`, `format_addr(a:
view[SocketAddr]) -> Vec[u8]`, and `resolve(host: view[]u8, port: u16) ->
Result[Vec[SocketAddr], IoErr]` through the system resolver.

**RH2. TCP.**
- `TcpListener.bind(addr: view[SocketAddr]) -> Result[TcpListener, IoErr]`
  (address reuse on, listening);
  `accept(edit self) -> Result[TcpStream, IoErr]`;
  `accept_timeout(edit self, d: time.Duration) -> Result[Option[TcpStream], IoErr]`;
  `local_addr(view self) -> Result[SocketAddr, IoErr]`.
- `TcpStream.connect(addr: view[SocketAddr]) -> Result[TcpStream, IoErr]`;
  `TcpStream.connect_timeout(addr: view[SocketAddr], d: time.Duration) -> Result[Option[TcpStream], IoErr]`;
  `read(edit self, buf: edit[]u8) -> Result[usize, IoErr]` (0 when the
  peer closed);
  `read_timeout(edit self, buf: edit[]u8, d: time.Duration) -> Result[Option[usize], IoErr]`;
  `write(edit self, data: view[]u8) -> Result[usize, IoErr]`;
  `write_all(edit self, data: view[]u8) -> Result[(), IoErr]`;
  `shutdown(edit self, how: Shutdown) -> Result[(), IoErr]` with `union
  Shutdown { Read; Write; Both }`;
  `set_nodelay(edit self, on: bool) -> Result[(), IoErr]`;
  `peer_addr(view self) -> Result[SocketAddr, IoErr]`;
  `local_addr(view self) -> Result[SocketAddr, IoErr]`;
  `Reader` and `Writer` (`flush` does nothing).
- Destructors close the socket.

**RH3. UDP.**
- `UdpSocket.bind(addr: view[SocketAddr]) -> Result[UdpSocket, IoErr]`;
  `send_to(edit self, data: view[]u8, to: view[SocketAddr]) -> Result[usize, IoErr]`;
  `recv_from(edit self, buf: edit[]u8) -> Result[(usize, SocketAddr), IoErr]`
  (a datagram longer than `buf` is cut to it);
  `recv_from_timeout(edit self, buf: edit[]u8, d: time.Duration) -> Result[Option[(usize, SocketAddr)], IoErr]`;
  `local_addr(view self) -> Result[SocketAddr, IoErr]`.

Every `_timeout` method of RH returns `Ok(None)` when the duration passed
with nothing accepted, connected, read or received, and leaves the socket
usable; a duration of zero polls once.

**RH4. Rules.** Socket calls block the calling thread until they complete
or their duration passes (the API has no non-blocking mode); sockets are
close-on-exec, on descriptors above
2. A write to a connection the peer closed is `Err` of kind `BrokenPipe` or
`ConnectionReset` and never a `SIGPIPE`. A blocked call interrupted by a
signal is retried. `TcpListener`, `TcpStream` and `UdpSocket` are `Send`.
Tests use the loopback interface only.

### I. Signals and randomness (hosted only)

**RI1. `std/signal`.** `union Signal { Interrupt; Terminate; Hangup; Quit;
User1; User2; Pipe; Child }`. `ignore(s: Signal)` and `reset(s: Signal)`
set the signal's disposition to ignored and to the system default.
`catch(s: Signal) -> Signals` makes the runtime record deliveries of `s`
instead of acting on them, and `Signals` adds more with `add(edit self, s:
Signal)`; `wait(edit self) -> Signal` blocks until a caught signal has
arrived and returns it (deliveries of one signal while it is pending are
one), `wait_timeout(edit self, d: time.Duration) -> Option[Signal]` (`None`
when the duration passed; zero polls once), `try_next(edit self) ->
Option[Signal]`. When several caught signals are pending, the one declared
first in `Signal` is returned first. Destroying `Signals` restores its
signals' earlier dispositions (below). One `Signals` exists at a time (a second `catch` while one is live
traps `explicit`). `raise(s: Signal)` sends the signal to the program's own
process. Rules:
- The eight signals are valid on every supported host, so setting a
  disposition cannot fail for them; if the system refuses anyway, the
  operation traps `explicit` and no disposition has changed.
- `ignore` and `reset` of a signal a live `Signals` holds trap `explicit`;
  `add` of a signal it already holds does nothing.
- Destroying `Signals` gives each signal it holds the disposition it had
  when it was caught or added, and discards recorded deliveries.
- `wait` is retried when interrupted; it has no failure outcome.
- `raise` cannot fail for a valid signal. Children are started with the dispositions rule R4 of
`specs/std-process` gives them, unaffected by `catch`.

**RI2. `std/rand`.** `fill(buf: edit[]u8) -> Result[(), io.IoErr]` fills
the buffer from the CSPRNG; `u64() -> u64` and `below(n: u64) -> u64`
(uniform, `n == 0` traps `explicit`) draw from it and trap `explicit` if the
source fails. `Rng` is a PRNG (xoshiro256**, its state filled from the seed by
splitmix64) with `seeded(seed: u64) -> Rng`, `from_os() -> Result[Rng,
io.IoErr]` (seeded from the CSPRNG; `Err` when the source fails), `next_u64`, `below`,
`fill`, `shuffle[T](edit self, xs: edit[]T)`; the same seed gives the same
sequence in both engines and on every platform. `Rng` works freestanding
except `from_os`.

### J. Freestanding

**RJ1. Board interface.** The console, power-off and clock of the
freestanding runtime are functions a board file can replace:
declared in `kern_board.h`:
- `void kern_board_init(void)`: called once by the startup code, on the
  boot stack, before any other board function and before the program's
  entry; it may leave devices unconfigured that the board never uses.
- `void kern_board_putc(char c)`: writes one byte to the console and
  returns when the device has accepted it; it must not fail, allocate or
  trap, since the trap handler calls it.
- `void kern_board_poweroff(int code)`: ends the machine and does not
  return (`_Noreturn`); `code` is the halt code (0 to 255), which a board
  reports where it can and may discard.
- `uint64_t kern_board_now_ns(void)`: nanoseconds since an arbitrary fixed
  point at or before `kern_board_init`, never decreasing; a board without a
  clock returns 0 always, and `time.sleep` then returns at once.
A board file must define all four; a missing one is a link error. The QEMU `virt` and `q35` implementations are the defaults, each
in its own file. `kern build --env=freestanding --board=<file.c>` compiles
the given file in place of the default board; `--linker-script=<file.ld>`
replaces the default script; `--heap-bytes=N` sets the system allocator's
arena (a positive multiple of 16; default 1,048,576). The three options are
also keys of `kern.mod` (`board`, `linker_script`, `heap_bytes`). A fixture
builds and boots an image with a custom board file under QEMU.

**RJ2. Allocator checks.** The freestanding system allocator traps
`explicit` when `free` or `realloc` is given a block that overlaps a free
block (a double free), in addition to the checks it has.

### K. Documentation

**RK1. `docs/runtime.md`**: the runtime's reference. Startup and exit; the
standard streams' policy; the trap protocol, record and backtrace; threads
and stack sizes; the synchronization primitives; memory and the allocator
checks; every environment variable and build option; the signal
dispositions the runtime sets; the platform matrix; and the entry table of
both runtimes, generated from the headers by a test that fails when it is
stale.

**RK2.** `docs/freestanding-porting.md` (writing a board file, the linker
script's obligations, the heap); ADR-017, recording the runtime's
architecture decisions (blocking I/O with threads and no scheduler, park as
the one blocking primitive, synchronization types in kern, checks in debug
builds); `docs/ownership.md` for every language-visible addition; the
security review, the unsafe audit, the hosted-only inventory, the README
and `CHANGELOG.md` (`Unreleased`) updated; one report per phase under
`docs/reports/runtime-*.md`.

### L. Extensions (added in v0.7, after the first eleven phases)

The first complete pass left six things a program would miss. They are
requirements too.

**RL1. Unix-domain sockets.** `std/net` gains `UnixListener` (`bind(path:
view[]u8)`, `bind_with_backlog`, `accept`, `accept_timeout`) and
`UnixStream` (`connect(path)`, `pair() -> Result[UnixPair, IoErr]` with
`union UnixPair { Ends(UnixStream, UnixStream) }`, which a `match` takes apart, `read`, `read_timeout`,
`write`, `write_all`, `shutdown`, `Reader` and `Writer`). A path of 104
bytes or more, an empty one or one with a NUL byte is `InvalidInput`
before any system call. `bind` fails with `AddrInUse` when the path
exists; a listener does not remove its path. RH4's rules hold.

**RL2. Directories.** `io.read_dir(path) -> Result[Vec[DirEntry], IoErr]`
with `DirEntry { name: Vec[u8]; kind: FileKind }`, entries in bytewise
order, the kind not following a symbolic link; `io.create_dir_all(path)`
creates the directory and every missing parent and succeeds when it
exists; `io.remove_dir_all(path)` removes a directory and everything
under it without following symbolic links.

**RL3. File locks.** `File.lock(edit self, exclusive: bool) -> Result[(),
IoErr]` blocks until the advisory lock is held; `File.try_lock(edit self,
exclusive: bool) -> Result[bool, IoErr]` returns false when another open
file holds a conflicting lock; `File.unlock(edit self)`. The lock belongs
to the open file and ends when it is closed.

**RL4. Mapped files.** `std/mem`: `map_file(f: view[io.File], writable:
bool) -> Result[Mapping, io.IoErr]` maps the whole file, shared, so that a
write through the mapping is a write to the file. `Mapping` has `len`,
`bytes(view self) -> view[]u8`, `bytes_mut(edit self) -> edit[]u8` (traps
`explicit` on a mapping that is not writable), `sync(edit self) ->
Result[(), io.IoErr]`; its destructor unmaps. An empty file gives an empty
mapping. `File.raw_fd(view self) -> i32` is the descriptor.

**RL5. Freestanding stack and start-up.** A freestanding build's
`--stack-bytes=N` (`stack_bytes`) is the size of the image's stack, in
RC2's range, by default 65,536. `--startup=<file.S>` (`startup`) replaces
the architecture's start-up code.

**RL6. The Linux gate boots images.** The `kern-linux-check` container has
QEMU for both architectures, and the freestanding fixtures and board tests
boot there.

### P. Performance

**R-P1.** For each of the thirteen benchmark workloads at release, the
instructions retired by the binary built after the last phase are within 1%
of those of the binary built at `a78f7c3`, or the report explains the
difference by a requirement above. Uncontended `Mutex.lock`/`unlock`
executes the same instructions as before.

## 4. Acceptance criteria and tests

Each phase is committed only when all of these hold (each is a gate):

1. Every requirement of the phase has a test that fails without the
   implementation: fixtures under `tests/run`, `tests/trap` and
   `tests/concurrency` run in both engines by the harness; Go tests for
   what a fixture cannot show (a signal, a TTY, a sanitizer report, an
   environment variable).
2. `go test ./...` passes, with the one standing host exception of
   `specs/release-v0.1` §3 (the `race_raw_ptr` fixture timing out on this
   Mac).
3. `golangci-lint run ./...` reports no issue and `gofmt -l` lists nothing.
4. RA2, RA3 and RA6 pass for every phase after the first.
5. The phase's fixtures and Go tests pass on Linux, run in the
   `kern-linux-check` container through `scripts/linux-check.sh` (extended
   to take the phase's test names).
6. `std/core.kern` and its prelude copy are identical; the hosted-only
   inventory and the unsafe audit are regenerated when a phase changes
   them.
7. An internal review of the phase's diff has been done and its findings
   fixed or recorded in the phase report.

After the last phase:

8. RA5 passes on macOS and in the Linux container.
9. R-P1 is measured and reported.
10. `specs/runtime/STATUS.md` lists every requirement with the commit that
    met it, and every recorded deviation.
