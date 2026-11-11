# Changelog

All notable changes to kern are recorded here. The layout follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html) in its 0.x form:
see "Versioning and stability" in the README. The procedure that turns a
section of this file into a GitHub release is `docs/releasing.md`.

## [Unreleased]

### Language

- Fixed: an access that came with a release function and was passed as
  an argument ended without the release function being called, in both
  engines. `bump(m.lock())` left the `Mutex` locked for good, and the
  next lock on that thread trapped; a `with` block released as it
  should. The release function now runs when the call that took the
  access returns, or, when that call returns an access derived from it,
  when that access ends (Rule 5.3.5, whose wording now says so).
- The reference no longer speaks of payload fields a pattern leaves
  unbound: a pattern names every field of its variant.

### Standard library

- `io.LineBuf` splits bytes into lines without owning where they come
  from: the program feeds it what it has read (from a socket it also
  writes to, or in pieces as a `Poller` reports them) and takes whole
  lines out with `next_line`; `read_line` does both over any `Reader`.
- `TcpStream.write_timeout` and `UnixStream.write_timeout` send what the
  socket will take within a duration; with no duration they never wait,
  which is how one thread writes to many connections.
- A `signal.Signals` can be watched by a `net.Poller`, so one wait
  serves sockets and signals. `std/net` imports `std/signal` for it.
- `Vec.remove_prefix(n)` drops the first `n` elements and keeps the
  rest in place and in order.
- `str.ends_with`.

### Runtimes and targets

- Fixed: in a native program, the handle of a scoped thread (`Join`)
  that was asked for its result, or destroyed, after the block of its
  scope had ended read memory the scope had freed. A handle now keeps
  its thread's record for as long as it lives, so `get` works inside the
  block and after it, as the reference says (Rule 5.5.3).
- Destroying a `Join` no longer waits for its thread. Inside the block
  it leaves the result to the scope, which destroys it when it joins the
  thread, in the order the threads were spawned; after the block the
  handle destroys the result itself. Workers spawned in a loop whose
  handles are dropped there no longer stop the loop, and the results of
  handles left to the end of a block are destroyed in the order of the
  spawns, where it was the reverse order of the handles.
- The runtime's interface version is 3 (`kern_rt_abi_3`): the contracts
  of `kern_join_get`, `kern_join_drop` and `kern_scope_drop` changed.
  Two entries were added, `kern_net_send_timeout` and `kern_signal_fd`,
  and `kern_net_poller_set` refuses write interest in the signal
  descriptor.

### Tooling

- The test suite gained fixtures for behaviour the reference states and
  nothing exercised (`docs/reports/reference-conformance.md`): release
  accessors at every kind of exit, destruction at every scope exit, the
  arithmetic traps beyond one sum and one division, the order of a
  pool's checks, slot retirement, a trap inside a destructor, and who
  destroys a value that threads share. A trap fixture may now state what
  the program wrote before it trapped, and the examples that run are
  part of the sanitized corpus.

### Examples

- Five applications written on the runtime, under `examples/`. Each is a
  tool to use (its usage is the comment at its head) and, run with no
  arguments, a demonstration of itself whose output the test suite
  compares in both engines:
  - `kq`: queries over a JSON document, in the manner of jq (paths,
    `[]`, `length`, `keys`, `type`, `select`, pipes), with results that
    are views of the document passed through closures.
  - `kvstore`: a key-value server that one thread runs for every
    connection on a `net.Poller`, with an append-only log synced once a
    round before the round's replies are sent, replayed at start, and
    rewritten by `COMPACT`. It writes without waiting for a client to
    read, and sleeps until a connection or a signal has something for
    it.
  - `ksearch`: a search for a pattern (a small regular-expression
    dialect) over directory trees: one thread walks, the others search
    mapped files, and the results are sorted by path.
  - `supervise`: starts the programs a JSON configuration names, passes
    on their output line by line, restarts them by policy with a growing
    delay, and on Interrupt or Terminate stops them, with Kill after a
    grace.
  - `httpd`: serves a directory over HTTP/1.1 from a pool of threads fed
    by a channel, with a time limit on a request's head, directory
    listings, and nothing served from outside its root.

## [v0.2.0] - 2026-11-10

The runtime: what runs under a kern program, on a host and on a board, and
the standard library that reaches it (files, sockets, signals, random
numbers, synchronization, memory). The specification is `specs/runtime`,
the reference `docs/runtime.md`, the record
`docs/reports/runtime-summary.md`.

Four changes can alter a program written for v0.1.0, each listed in its
place below: `io.FileKind` gained `Symlink`, so an exhaustive `match` on it
needs the arm; a thread that cannot be spawned is `Err(SpawnErr)` where it
was a trap; a new file's mode is 0666 less the umask; and assignment
destroys the previous value, so a destructor that did not run now runs.
`spec/rules.lock` is unchanged, and the machine API answers the schema
versions it answered in v0.1.0 (0.2 to 0.5).

### Language

- String literals take `\r` and `\xHH` (a byte by two hexadecimal
  digits).
- `unsafe assert Send` and `unsafe assert Share` may carry a `where`
  clause over the type's parameters; the type has the marker only for
  arguments that satisfy it.
- A variant name that several unions of a module declare is the expected
  type's when written without its union; it is ambiguous only where
  nothing says which is wanted.
- An owned `match` moves a payload out also when the payload's type has a
  destructor (only a union that declares a destructor itself is refused),
  and its subject may be what `take`, `replace` or `?` yields.
- `swap_at(xs, i, j)` exchanges two elements of a sequence of any type.
- Fixed: a `?` in an operand of a call, a struct literal, a variant or
  an array left the owned operands before it undestroyed on its error
  path, and after an argument that edits a value with a destructor it
  was an internal error of the compiler.
- Fixed: assignment through an access, to a field or to an element did
  not destroy the previous value; a temporary used as a method receiver
  was never destroyed. Programs that relied on neither are unchanged;
  programs that leaked no longer do.

### Standard library

- `std/io`: `File` with `OpenOptions`, `seek`, `sync`, `set_len` and
  `metadata`; `metadata`, `symlink_metadata`, `exists`, `rename`,
  `hard_link`, `symlink`, `read_link`, `set_mode`, `canonicalize`,
  `current_dir`, `copy_file`; the interfaces `Reader` and `Writer` with
  `BufReader` (lines with either terminator) and `BufWriter`; `stdin()`,
  `stdout()`, `stderr()`; `ErrKind` and `kind(e)`, the same for the same
  failure on every host; `flush_stdout`, `is_terminal`. `FileKind` gained
  `Symlink`: an exhaustive `match` on it needs the new arm.
- `std/net` (new): IPv4 and IPv6 addresses, TCP listeners and streams,
  UDP sockets, Unix-domain listeners, streams and pairs, with a timed
  form of every call that waits.
- `std/io`: `read_dir` (entries with their kinds), `create_dir_all`,
  `remove_dir_all`, `join`; `File.lock`, `try_lock`, `unlock` (advisory
  locks), `File.raw_fd`.
- `std/mem`: `map_file` and `Mapping`, a file's bytes in memory.
- `std/io`: `File.lock_range`, `try_lock_range` and `unlock_range`,
  advisory locks on ranges of a file's bytes.
- `std/net`: `SocketAddr` has a `scope` (the interface of an IPv6
  link-local address), read and written as `[v6%zone]:port`, with
  `addr_scoped`, `interface_index` and `interface_name`; `UnixDatagram`,
  datagrams between programs on one machine; socket options as methods
  (`set_keepalive`, `set_recv_buffer`, `set_send_buffer`,
  `set_hop_limit`, `set_linger`, `set_broadcast`, each with its reading
  form, and `nodelay`).
- `signal.raise` flushes standard output before it sends the signal.
- `std/net`: `Poller`, the system's readiness queue (epoll, kqueue): one
  thread waits for many sockets and serves those that are ready.
  `UnixStream.send_with`, `send_file` and `recv_with` pass open sockets
  and files between processes; a received `Descriptor` becomes the type
  the program names, checked against what the system says it is; a
  descriptor the program does not take is closed, on macOS too, where
  the system would leave it open in the receiving process.
  `UnixStream.peer_credentials`. On Linux, a Unix-domain path that
  begins with a zero byte is a name in the abstract namespace.
- `std/sync` (new): `RwLock`, `Once`, `WaitGroup`, a bounded `Channel`.
- `std/signal` (new): `catch`, `ignore`, `reset`, `raise`, and `Signals`
  to wait for what was caught.
- `std/rand` (new): the system's generator, and `Rng`, a seeded generator
  with `below`, `fill` and `shuffle`.
- `std/mem` (new): `page_size` and `PageAllocator`.
- Prelude: `park`, `park_ns`, `unpark_one`, `unpark_all`;
  `Condvar.wait_ns`; `Mutex.try_with`; `thread.set_stack_bytes`,
  `stack_bytes`, `available_parallelism`, `current_id`.
- `process.Pipe` is an `io.Reader` and an `io.Writer`.
- A spawn that cannot allocate what the thread needs is
  `Err(SpawnErr)`; it trapped `oom`.
- A new file's mode is 0666 less the umask; it was 0644 less the umask.

### Runtimes and targets

- A trap's record carries the line and column of its span. With
  `KERN_BACKTRACE=1` or a build made with `--backtrace`, a second line
  lists the kern functions on the trapping thread's stack.
- Standard output is flushed after each line on a terminal and before a
  read of standard input; a failed write is reported by `flush_stdout`.
- A debug build traps `explicit` on a `free` or `realloc` that names no
  live block or the wrong size, and with `KERN_ALLOC_REPORT=1` reports
  its allocations when the program ends.
- A thread destroyed without `join` destroys its result and frees its
  record when it ends; it leaked both.
- `--stack-bytes=N` (`stack_bytes` in `kern.mod`) and `KERN_STACK_BYTES`
  give spawned threads and `main` a stack of N bytes.
- The freestanding runtime's devices are a board file's
  (`kern_board.h`): `--board`, `--linker-script`, `--startup` and
  `--heap-bytes` (`board`, `linker_script`, `startup`, `heap_bytes` in
  `kern.mod`), and `--stack-bytes` sizes its stack. Its allocator traps
  on a double free.
- The link drops the runtime code a program does not reach.
- The runtime's interface has a version (`kern_rt_abi_2`); a program and
  a runtime of different versions do not link.

### Tooling

- `kern run`: a signal left at its default ends an interpreted program by
  that signal, as it ends a native one (`User1`, `User2` and `Quit` did
  not); a mapped file is the system's mapping, shared with the file's
  other users; a fault in mapped memory is a trap of kind `unreachable`.
- `kern run`: `io.list_dir` lists a directory whose entries the system
  will not describe one by one (`/dev/fd` on macOS 26); the listing
  failed.
- `docs/runtime.md`, the runtime's reference, with every entry of both
  runtimes generated from their headers; `docs/freestanding-porting.md`.
- The fixture corpus runs under AddressSanitizer and
  UndefinedBehaviorSanitizer on macOS and Linux.

## [v0.1.0] - 2026-11-08

The first release: a research preview of the language, its compiler, its
standard library and its tools, as they stand after the six implementation
phases of `specs/initial/PLAN.md` and the work recorded in `docs/reports/`
since. The engineering reports, the specifications under `specs/` and the
ADRs are the detailed record; this section summarizes them.

### Language

- Safe code has no first-class references: access is second-class, through
  `view`/`edit`/`consume`/`out` parameter conventions and lexically scoped
  `with` bindings (`docs/ownership.md`, ADR-001, ADR-005).
- Static moves, single-source elision for returned accesses, and checked
  multi-access functions such as `get2` (ADR-003, ADR-004).
- Two-phase `edit` arguments: a call such as `v.push(v.get(0usize))`, whose
  other arguments only read the edited place, is accepted (ADR-016,
  `docs/reports/two-phase-edit.md`).
- Arenas, pools and generation-checked handles as library types (ADR-002,
  ADR-014).
- Monomorphized generics with interfaces and `where` clauses, function
  pointers and non-escaping closures (ADR-009, ADR-010,
  `docs/reports/closures.md`).
- `for x in xs`, `for i, x in xs`, and `for k, v in m` over maps through a
  cursor protocol (`docs/reports/for-index.md`, `docs/reports/for-cursor.md`).
- Traps instead of panics, checks on by default, and the trap kinds a
  function may raise visible to callers (ADR-006, ADR-008,
  `docs/reports/effect-trap-kinds.md`).
- Threads with `Send`/`Share` and scoped spawn, atomics and a memory model
  restricted from C++20 (ADR-007, `spec/memory-model.md`).
- Declared budgets: a function may state how many runtime bounds checks,
  deep copies and allocations it allows, and `check` and `build` enforce
  them (`docs/reports/declared-budgets.md`).
- Multi-module programs found from their imports, with an optional
  `kern.mod` manifest (`docs/reports/module-discovery.md`).
- The grammar, type system, memory model and formatting rules are in
  `spec/`; `spec/rules.lock` freezes every diagnostic code to its rule
  number.

### Compiler and analyses

- A Go compiler that emits LLVM IR, built and linked through clang, and a
  reference interpreter that is the backend's behavioural oracle; the test
  suite runs every fixture in both (`docs/reports/phase-2.md`,
  `docs/reports/program-differential.md`).
- An ownership checker with one diagnostic code per rule, and
  machine-applicable fixes for the mechanical diagnostics
  (`docs/reports/diagnostic-fixes.md`).
- A kern-level inliner and a proof pass that removes bounds checks it can
  prove, including guards through joins and loops, masks, remainders,
  non-strict guards and slice bounds (`docs/reports/inliner.md`,
  `docs/reports/prove-guards.md`, `docs/reports/prove-masks.md`,
  `docs/reports/prove-nonstrict.md`).
- For each runtime-checked bounds check, a reason it is not proven and a
  hint naming a rewrite the proof rules accept
  (`docs/reports/unproven-reasons.md`).
- DWARF line tables and `kern debug`, an lldb session with ownership state
  at the current line (`docs/reports/phase-5.md`).
- Native traps and faults report the kern function, file and span on Linux
  and macOS (`docs/reports/native-trap-loc.md`,
  `docs/reports/runtime-trap-file.md`).

### Standard library

- The prelude, `std/core.kern`: `Box`, `Vec`, `String`, `Pool`, `Arena`,
  `Shared`, `Mutex`, `Condvar`, `Atomic`, scoped threads and the raw-pointer
  core, written in kern over audited `unsafe` blocks
  (`docs/reports/containers-in-kern.md`, `docs/unsafe-audit.md`).
- Fallible allocation: every allocating operation has a `try_` form that
  returns `Err(OutOfMemory)`, and the effect analysis shows which code cannot
  trap `oom` (`docs/reports/fallible-alloc.md`,
  `docs/reports/fallible-runtime-objects.md`).
- Containers over a caller's allocator: `BoxIn`, `VecIn`, `ArenaIn`,
  `PoolIn` and `map.MapIn` (`docs/reports/alloc-containers.md`,
  `docs/reports/alloc-containers-more.md`, `docs/reports/map-in.md`,
  `docs/reports/in-forms.md`).
- In-place initialization: `new_with`, `alloc_with` and their `_ctx` forms
  (`docs/reports/in-place-init.md`, `docs/reports/init-context.md`).
- Imported modules: `std/str`, `std/map`, `std/io`, `std/json`,
  `std/process` and `std/time` (`docs/reports/std-library.md`,
  `docs/reports/std-gaps.md`, `docs/reports/std-files.md`,
  `docs/reports/std-process.md`, `docs/reports/std-time.md`,
  `docs/reports/std-timeouts.md`, `docs/reports/float-text.md`).

### Runtimes and targets

- Hosted: Linux (glibc 2.34 or newer) and macOS, on x86-64 and arm64, with a
  small C runtime compiled by clang and cached between builds
  (`runtime/hosted/`, `docs/reports/build-time.md`).
- Freestanding: bare-metal x86-64 and AArch64 images that boot under QEMU,
  with the library containers, floating-point text and a monotonic clock;
  using a hosted-only facility is the diagnostic `ENV_HOSTED_ONLY`
  (ADR-011, ADR-012, ADR-015, `docs/reports/freestanding-availability.md`,
  `docs/reports/freestanding-float.md`, `docs/reports/freestanding-clock.md`).
- Hosted kern functions keep the frame pointer; the measured cost is nil on
  arm64 macOS and about 1% on x86-64 under Rosetta
  (`docs/reports/x86-frame-pointers.md`).

### Tooling

- `kern version` and `kern --version` report the version, commit, Go
  toolchain, platform and the machine-API schema versions the binary answers
  (`specs/release-v0.1/SPEC.md`).
- The machine API: one JSON request and response per command, at
  `schema_version` 0.5, with 0.4, 0.3 and 0.2 requests answered in their own
  shapes (`tools/agent-api/schema.md`).
- `kern test`: `test_` functions in `*_test.kern` files, run in the
  interpreter and natively, at debug and release, with the four runs
  compared (`docs/reports/test-command.md`, `docs/reports/test-runs.md`).
- `kern lsp`: diagnostics, formatting, quick fixes, hover, definition,
  references, rename, completion, outline and signature help
  (`docs/reports/language-server.md`, `docs/editors.md`).
- `kern format`, idempotent and diagnostic-preserving (`spec/format.md`).
- Reproducible release builds for Linux and macOS on both architectures,
  with `SHA256SUMS`, published by `.github/workflows/release.yml` when a tag
  is pushed (`scripts/release.sh`, `docs/releasing.md`).

### Performance

- In the latest harness measurements, kern meets its throughput target (at
  least 0.90 of the C or Rust reference row) on all thirteen benchmark
  workloads and its memory target (at most 1.10) on twelve; btree uses 1.23
  times the memory, the width of its checked handles
  (`docs/reports/quiet-host-2026-10-31.md`, `bench/PROTOCOL.md`).
- The interpreter compiles instructions into ops at a function's first call
  and avoids most per-call allocations (`docs/reports/interp-compile.md`,
  `docs/reports/interp-call-path.md`, `docs/reports/interp-alloc.md`).

### Known limitations

- The compiler is written in Go; self-hosting is deferred
  (ADR-013). A kern lexer and parser written in kern exist as examples
  (`examples/selfhost`, `examples/selfhost_parser`).
- No Windows support; native builds need clang, and freestanding images
  need `ld.lld` and QEMU.
- Releases are checksummed, not signed (`docs/security-review.md`).
- The machine API is a local tool with the user's permissions: paths are
  opened as given and inputs have no size limits
  (`docs/security-review.md`).
- The agent experiment has not met its own threshold, and its fourth task
  suite has not been run (`bench/agent/REPORT.md`,
  `docs/reports/agent-tasks-v4.md`).
- The interpreter boxes integers of 256 and above, its largest remaining
  cost (`docs/reports/interp-alloc.md`).

### Stability

This is a 0.x release. A later minor version may change the language, the
standard library, the CLI and the machine API; a patch version does not
intend to. Diagnostic codes stay frozen to their rule numbers in
`spec/rules.lock`, the `schema_versions` list of `kern version` names the
machine-API versions a release answers, and any change to either is listed
here.

[Unreleased]: https://github.com/dshills/kern/compare/v0.2.0...HEAD
[v0.2.0]: https://github.com/dshills/kern/releases/tag/v0.2.0
[v0.1.0]: https://github.com/dshills/kern/releases/tag/v0.1.0
