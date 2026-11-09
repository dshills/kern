# Plan: the kern runtime, complete

Implements `specs/runtime/SPEC.md` v0.6 in eleven phases, one or more
commits each, on `main`, nothing pushed. Each phase ends with the gates of
spec §4 (1 to 7) and a line per requirement in `specs/runtime/STATUS.md`.
The order puts the interface checks first, so that every later phase adds
entries under them, and the documentation last, when everything it
describes exists.

## How every phase works

- **A new facility has five parts**, added together: the kern declaration
  (`std/*.kern`, and the identical prelude copy for `std/core.kern`); the
  lowering in `compiler/backend/llvm/calls.go`; the C entry and its
  prototype in `kern_rt.h`; the interpreter's implementation in
  `compiler/interp`; and the trap kinds in `compiler/analysis/effects.go`
  when it can trap. A hosted-only entry needs no freestanding code: the
  emitter's record and `freestanding.Provided()` already make its use
  `ENV_HOSTED_ONLY`.
- **Tests**: fixtures with expected output under `tests/run` (and
  `tests/trap`, `tests/concurrency`), which the harness runs in the
  interpreter and natively at debug and release; Go tests in `tests/` or
  `cmd/kern` for what needs a process around the program (environment
  variables, signals, a pseudo-terminal, a sanitizer).
- **Linux**: `scripts/linux-check.sh` takes the phase's test names and runs
  them in the `kern-linux-check` container; a phase that adds test
  patterns extends the script's list.
- **Review**: an internal review of the phase's diff (line-by-line, removed
  behaviour, callers) before the gates; findings fixed or recorded in the
  phase report `docs/reports/runtime-<phase>.md`.
- **Commit** message ends with the attribution line; `docs/kern.webp` is
  never staged.
- **Deviations** from a signature in the spec go to `STATUS.md` with the
  reason, in the phase that makes them.
- **Test mapping and acceptance.** Each phase's report has a table with one
  row per clause of each of its requirements (every sentence of the spec
  that states a behaviour, a bound, an error or an ordering) naming the test
  that fails without it. A phase is accepted when that table has no empty
  row, every named test passes in both engines (or natively, for the
  clauses the spec limits to native programs) on macOS and in the Linux
  container, and gates 1 to 7 hold. The "Tests" lists below name the
  principal tests, not all of them.

## The startup contract (fixed from phase 1)

The entry code of every hosted program does, in order: `kern_set_file`,
`kern_set_args`, `kern_set_fn_locs` (and, from phase 4, `kern_set_lines`),
then `kern_start(ptr @kern_rt_abi_2)`, then `kern_run_main(ptr @thunk)`,
whose result is the process's exit status. `kern_start` initializes the
runtime once (configuration, descriptors, signals, the fault handler) and
ignores its argument, which exists so that the program references the ABI
symbol. `kern_run_main(int32_t (*fn)(void))` calls `fn` on the calling
thread, or from phase 3 on a thread with the configured stack, and returns
its result; in phase 1 it only calls `fn`. `kern_rt.h` defines
`KERN_RT_ABI` as 2 and the symbol's name from it. A test links a program
against a runtime compiled with the symbol renamed and requires the link to
fail naming `kern_rt_abi_2`; it runs in every phase (gate 4).

## Phase 1: structure and ABI (RA1 to RA6)

**Files.** `runtime/hosted/src/kern_rt.c` split into `rt_base.c`,
`rt_trap.c`, `rt_alloc.c`, `rt_sync.c`, `rt_thread.c`, `rt_io.c`,
`rt_proc.c`, `rt_time.c`, `rt_str.c` plus `kern_rt.h`;
`runtime/hosted/hosted.go` embeds the directory and exposes the files;
`runtime/freestanding/src/kern_rt_fs.h`; `compiler/backend/native/build.go`
writes all units to the build and cache directories;
`compiler/backend/llvm` (extension attributes, the ABI symbol reference);
`runtime/abi_test.go` (RA2, RA3, RA6); `tests/sanitize_test.go` (RA5);
`.github/workflows/ci.yml` (a `sanitize` job).

**Steps.**
1. Move code into units without changing a line of logic; the order of
   includes keeps every `static` helper before its uses. `kern_rt.h` gets
   each entry's prototype. Byte-compare the trap, run and concurrency
   fixtures' outputs before and after (the harness).
2. RA2: fix every warning the flag set reports (expected: unused
   parameters, sign comparisons, missing prototypes).
3. RA3: the test. Parse clang's `define` lines with a small tokenizer
   (return attributes, type, name, parameter list with attributes). Record
   declarations through `llvm.Options.Record`. Run for the host OS on both
   architectures (`-target arm64-apple-macos`/`x86_64-apple-macos`, or the
   Linux pair in the container). Then correct the emitter: the attributes
   clang gives each entry on each supported target are the reference (the
   test prints them); where they are the same on all four targets, which
   is expected for the C types the runtime uses (`uint8_t` zero-extended,
   `int8_t`/`int16_t` sign-extended, 32-bit and wider plain), one helper
   keyed by the entry's C parameter type supplies them to declarations and
   call sites; an entry whose attributes differ between targets gets them
   from a per-target table.
4. RA4: the startup contract above; `kern_rt_abi_2` is defined in
   `rt_base.c` and `rt.c`. The reference costs no instruction on any hot
   path.
5. RA5: `native.Options.Sanitize`; under it clang gets
   `-fsanitize=address,undefined -fno-sanitize-recover=undefined`; the
   test runs the fixtures with `ASAN_OPTIONS=detect_leaks=1` on Linux and
   `detect_leaks=0` on macOS (the platform's ASan has no leak checker). It
   leaves out only fixtures whose trap is raised by a fault the sanitizer
   intercepts (an expected `stack` trap, or `unreachable` reached through a
   bad address); fixtures that trap through a check instruction and those
   that use the allocation-failure hooks are run. Every report is fixed in
   this phase, with one named exception: leaks of detached threads'
   results, which RC1 fixes. Fixtures failing only for that reason are
   listed in the test with the reason, and phase 3 empties the list. The
   final gate (8) requires the list to be empty.
6. RA6: `clang --analyze -Xanalyzer -analyzer-output=text`; fix or, for a
   false positive, restructure the code so that it is not reported.

**Verification.** The corpus's IR changes only by the extension attributes
and the `kern_start` argument (shown by `scripts/capture-ll.sh` and a
`sed`-normalized diff). Benchmarks unaffected: instructions retired for
`fib`, `vector`, `lock_protected_map` before and after, three runs each.

**Risks.** The runtime-object cache keyed on one file: the key must cover
every unit and header (the preprocessed output already does; the test
edits a unit and expects a rebuild). `-Wmissing-prototypes` on functions
that are `static` only without test hooks.

## Phase 2: lifecycle and standard streams (RB1 to RB5)

**Files.** `rt_base.c` (configuration, the output policy, robust writes,
the recorded failure, `kern_io_is_terminal`, `kern_io_flush_stdout`);
`rt_io.c` (flush before a read of descriptor 0); `std/io.kern` (`Stream`,
`is_terminal`, `flush_stdout`); `compiler/interp` (the same calls; its
writers are unbuffered, so only the results are mirrored);
`cmd/kern/streams_test.go`.

**Key decisions.**
- The TTY test is one `isatty(1)` in `kern_start`.
- `KERN_*` parsing lives in one function with a table of names, so later
  phases add a row.
- Privilege check: `getauxval(AT_SECURE)` on Linux, `issetugid()` on macOS.

**Tests.** Go tests that run a built program with: a pipe as stdout and a
line written before a blocking stdin read (the line must arrive before
stdin is written); a pseudo-terminal as stdout (`posix_openpt` through
`golang.org/x/sys`-free `syscall` calls) and a line with no later flush;
a stdout pipe whose read end is closed with the default `SIGPIPE`
disposition (the process ends by `SIGPIPE`, spec RB3); the same pipe with
the program started through `sh
-c 'trap "" PIPE; exec ...'` so that `SIGPIPE` is ignored (`flush_stdout`
returns `Err` of kind `BrokenPipe`, a second call returns `Ok`: the
failure was cleared); stdout closed before the program starts (output is
dropped, as RB2's placeholder rule gives it `/dev/null`); a program
stopped and continued during a large write (no byte lost); a non-blocking pipe that
fills (no byte lost); each variable with a boundary, an invalid, an empty and an unset value
(the `ignored` line or none); a set-user-ID copy is not testable without
privilege, so the privilege check is a unit the test calls with the flag
forced; exit statuses 0, 1, 255, 256 (0) and -1 (255), a destructor that
would print not running after `exit`, the `SIGABRT` of a trap, and an
exit while another thread loops. Fixtures for `is_terminal` (false under the harness) and
`flush_stdout` (Ok).

## Phase 3: threads (RC1 to RC4)

**Files.** `rt_thread.c`; `compiler/backend/llvm/calls.go`
(`kern_thread_drop` takes the result's destructor; the stack size
constant; `main` on a thread); `std/core.kern` (`thread.set_stack_bytes`,
`stack_bytes`, `available_parallelism`, `current_id`); `cmd/kern`
(`--stack-bytes` for `build`, `run` and `test`, in both engines, with the
flag over the manifest key and the validation of spec RC2);
`compiler/project` (manifest); `compiler/interp` (detached results, the
queries, the stack sources).

**Key decisions.**
- **One state word per thread record**, updated by compare-and-swap. The
  record and its argument/result block live until the transitions below
  free them; the thread writes its result into the block before its
  transition.

  | State | Event | New state | Who destroys the result | Who frees record and block |
  |---|---|---|---|---|
  | running | the function returns | finished | nobody yet | nobody yet |
  | running | the handle is destroyed | detached | nobody yet | nobody yet |
  | finished | `join` | (gone) | nobody: the joiner takes it | the joiner |
  | finished | the handle is destroyed | (gone) | the destroying thread | the destroying thread |
  | detached | the function returns | (gone) | the exiting thread | the exiting thread |
  | running | `join` | waits for finished, then as above | | |

  `join` on a running thread uses `pthread_join`; a detached thread is
  `pthread_detach`ed at the transition. Tests force each ordering with the
  spawned function blocked on an atomic the test releases before or after
  the handle's destruction, and a stress test races them under the thread
  sanitizer.
- **The alternate signal stack** is mapped with `mmap` by the spawner,
  with a guard page below it, and unmapped by the thread's exit path after
  `sigaltstack` is disabled.
- **`main` on a thread** only when the size was given by the environment or
  the build. The startup contract does not change: `kern_start` still
  does the one-time initialization on the initial thread, and
  `kern_run_main(fn)`, which phase 1 introduced, now creates the thread,
  joins it and returns its result (or calls `fn` directly without a
  configured size). Each thread, the configured main included, installs
  its own alternate signal stack and stack bounds in its start routine;
  the initial thread installs its own in `kern_start`. The main function's
  thread keeps identifier 0.
- Thread stacks come from `pthread_attr_setstacksize`; the guard page is
  the platform's.

**Tests.** A concurrency fixture whose detached thread's result has a
printing destructor (a `WaitGroup` is not available yet: it waits on an
`Atomic`); a Go test that runs it sanitized on Linux for leaks; recursion
depth fixtures run with `--stack-bytes` at two sizes (deeper succeeds,
shallower traps `stack`), on a spawned thread and on `main`; spawn failure
through the existing test hook leaks nothing (sanitized); the queries.

## Phase 4: failure diagnostics (RE1 to RE3)

**Files.** `compiler/backend/llvm/emit.go` (the line tables, function
names in the location table, `kern_set_lines`); `rt_trap.c`; freestanding
`rt.c`; `compiler/interp/interp.go` (line and column, the backtrace);
`compiler/testrun` and `cmd/kern` where trap records are parsed;
`tools/agent-api/schema.*` if the `kern test` result carries the record's
fields; every `.out`/expected stderr that holds a record.

**Key decisions.**
- **Line tables** are `i32` arrays of line-start offsets, one per source
  file that has a function, in a table of `{file name pointer, table
  pointer, count, source length}` the entry code registers. An offset
  above the source length gives line and column 0; tests cover an empty
  file, a file with and without a final newline, a missing table and an
  offset past the end, in the three runtimes. Lookup is by pointer equality
  on the file name (the trap site passes the same constant), then a binary
  search.
- **Function names** extend each `kern.fn_locs` record by one pointer.
- **The walk** for a trap raised by a call starts from the caller's frame
  (`__builtin_frame_address`), for a fault from the signal context; each
  word is read through the existing probe, so a broken chain ends the walk.
- `--backtrace` sets a flag in the entry code; the environment variable
  overrides it both ways.

**Tests.** Trap fixtures keep their kinds; a Go test checks line and
column for a table of traps (first line, last line, a line after a
multi-byte character, a library-raised trap that names the user's call) in
both engines; a backtrace test with a three-deep call chain at debug
(exact frames) and release (the innermost user function present); the
interpreter's line; freestanding line and column under QEMU.

**Risks.** Every test that compares standard error literally. The harness
compares kinds; `kern test` compares records between engines, which stay
equal because both add the same fields.

## Phase 5: synchronization (RD1 to RD5)

**Files.** `rt_sync.c` (park entries; `kern_condvar_wait_ns`;
`kern_mutex_try_lock`); `std/core.kern`; `std/sync.kern` (new, with its
prelude copy and the module table); `compiler/resolve`, `compiler/check`,
`compiler/parser`, `spec/grammar.ebnf`, `docs/ownership.md` (conditional
assertions); `compiler/interp` (park on an atomic cell, the timed wait,
try lock).

**Key decisions.**
- **Park with a timeout** is `os_sync_wait_on_address_with_timeout` on
  macOS and `FUTEX_WAIT` with a relative timeout on Linux.
- **The interpreter's park** keys a table of waiters by the atomic cell's
  identity and re-checks the value under the table's lock, which gives the
  no-lost-wake property.
- **`try_with`** is kern code in the prelude over two intrinsics (`try
  acquire`, `guarded value`), so the closure is called from kern.
- **Conditional assertions** are resolved like an `impl`'s `where`
  clause: the marker holds for an instantiation when its arguments satisfy
  the constraints.
- **`std/sync` types keep their value on the heap** (as `Box` does) and
  return accesses through `ptr_view`/`ptr_edit` with release functions.

**Tests.** Per type: single-thread semantics as run fixtures;
multi-thread fixtures under `tests/concurrency` (readers and writers
counting, a producer and consumer pipeline, `Once` raced by eight
threads); timeouts measured loosely (at least the duration, under ten
times it); negative fixtures for the conditional assertion (a non-`Send`
payload in a `Channel` crossing threads is `SEND_NOT_SEND`); the thread
sanitizer over the native fixtures.

## Phase 6: memory (RF1 to RF3)

**Files.** `rt_alloc.c` (a table of live blocks in debug builds: open
addressing, striped locks; the report); `compiler/interp/rawmem.go` (size
and alignment checks); `std/mem.kern`; `rt_mem.c` (`mmap`).

**Key decisions.** The table stores pointer, size, alignment and the call
site's record pointer; it exists only without `KERN_RELEASE`, so a release
build's allocator is the code it has now. Counters are updated only when
the report is enabled.

**Tests.** Trap fixtures: free with a wrong size, a wrong alignment, twice,
of a pointer never allocated; realloc with a wrong old size; each with an
accepting twin. A Go test for the report's counters over a fixed program.
`PageAllocator` under `VecIn` and `MapIn`. The whole corpus passes with
the checks (gate 2).

## Phase 7: files and streams (RG1 to RG4)

**Files.** `rt_io.c`; `std/io.kern`; `std/process.kern` (`Pipe`'s
interfaces); `compiler/interp` (the same system calls through `syscall`).

**Key decisions.** One entry per system call, returning the value or
`-errno`; all policy (option validation, `read_all` growth, buffering,
lines) is kern. Error classification is one entry mapping the host's
number to the kind's ordinal; the interpreter maps Go's `syscall.Errno` by
name.

**Tests.** Run fixtures in a temporary directory for every operation and
its failures (missing, exists, directory, permission); seeks around EOF;
append; `create_new`; metadata of a file, a directory and a link; line
reading with both terminators and an unterminated last line; buffered
writes through a failing writer; `kind` of each mapped error.

## Phase 8: networking (RH1 to RH4)

**Files.** `rt_net.c`; `std/net.kern`; `compiler/interp/net.go`.

**Key decisions.**
- **Every socket descriptor is non-blocking inside the runtime**, and
  every operation is a deadline loop: try the call; on `EAGAIN` (or
  `EINPROGRESS` for connect) `poll` for readiness with the time left; on
  `EINTR` recompute the time left and continue; when the deadline passes
  return the timed-out value. An untimed call has no deadline. A call can
  therefore never block past its duration on stale readiness, and no mode
  needs restoring. The interpreter does the same over `syscall`.
- **Connect** is its own sequence: one `connect` call; on `EINPROGRESS`
  poll for writability until the deadline; then read `SO_ERROR`, which is
  the result. On a timeout or a failure the descriptor, which the caller
  never saw, is closed. The timeout test connects to a listener whose
  backlog is full (the loopback then leaves the attempt pending) and
  checks the descriptor count before and after.
- `MSG_NOSIGNAL` on Linux and `SO_NOSIGPIPE` on macOS.
- Addresses cross the ABI as a 19-byte record: byte 0 the family (4 or
  6), bytes 1 to 16 the address (IPv4 in the first four), bytes 17 and 18
  the port, high byte first.

**Tests.** Loopback fixtures: an echo server and client in two threads,
IPv4 and IPv6 (skipped when `::1` cannot be bound); timeouts on accept,
connect to a closed port (`ConnectionRefused`), read; write after the peer
closed; UDP exchange; address parsing and formatting tables; `resolve` of
`localhost`.

## Phase 9: signals and randomness (RI1, RI2)

**Files.** `rt_signal.c` (a handler that sets a bit and writes a byte to a
non-blocking pipe), `std/signal.kern`; `rt_rand.c` (`getentropy` in
chunks of at most 256 bytes; a failure after earlier chunks is the
call's failure; tested at lengths 0, 255, 256, 257 and 4,096, and with
the hook failing the second chunk),
`std/rand.kern`; `compiler/interp` (`os/signal`, `crypto/rand`).

**Tests.** Fixtures that `raise` each catchable signal and `wait` for it;
ignored and reset dispositions observed through a child (`std/process`);
`wait_timeout`; a Go test that signals a running program from outside. For
`rand`: xoshiro256**'s published sequence for a fixed state and
splitmix64's for a fixed seed; `below` against a table and its rejection
loop at the largest bound; `shuffle` of a fixed array under a fixed seed;
the failure path of the CSPRNG through a test hook (`fill` is `Err`,
`from_os` is `Err`, `u64` traps); `Rng` in a freestanding fixture. One
smoke test reads the real source (two 64-byte fills that are equal would
fail it, an event of probability 2^-512).

## Phase 10: freestanding (RJ1, RJ2)

**Files.** `runtime/freestanding/src/kern_board.h`, `board_virt.c`,
`board_q35.c`, `rt.c`; `compiler/backend/native/build.go`; `cmd/kern`
(`--board`, `--linker-script`, `--heap-bytes`); `compiler/project`.

**Tests.** A fixture with its own board file (a console that prefixes each
line) booted under QEMU on both architectures; a heap of 64 KiB that a
program exhausts (`Err(OutOfMemory)`); a double free trapping `explicit`.

## Phase 11: documentation, measurements, final gates (RK, R-P1)

**Files.** `docs/runtime.md` (with the generated entry table and its
test), `docs/freestanding-porting.md`, `docs/decisions/ADR-017-*.md`,
`docs/ownership.md`, `docs/security-review.md`, `docs/unsafe-audit.md`,
`README.md`, `CHANGELOG.md`, `docs/reports/runtime-summary.md`,
`specs/runtime/STATUS.md`.

**Steps.** Gate 8 (the sanitized corpus on macOS and Linux); gate 9 (the
thirteen workloads' instructions retired at `a78f7c3` and at the head, five
interleaved runs each, and the disassembly of `kern_mutex_lock` and
`kern_mutex_unlock` in a release build at both commits, which must be the
same instructions); gate 10.

## Gate policy

Every gate is mandatory. The standing exception is the `race_raw_ptr`
thread-sanitizer fixture on this Mac (spec §4, gate 2). A phase whose Linux
run fails for a reason outside kern (the container cannot start) is not
committed until the container runs.
