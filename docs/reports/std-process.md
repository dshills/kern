# Standard library: child processes and the environment

**Date:** 2026-11-01. **Spec:** `specs/std-process/SPEC.md` (v0.2).
**Plan:** `specs/std-process/PLAN.md`. **Closes:** the last library
prerequisite of self-hosting in `docs/decisions/ADR-013-self-hosting.md`
(process control). **Commits:** 529a8a7, 85e68dc, 9e9b2d3, e60b935 (the
four phases), c61aace and a3f95a8 (two language fixes found on the way).

## What was added

- **`std/process`**, embedded and mirrored in `std/`:
  - `Command`: `new`, `arg`, `env_set`, `env_remove`, `env_clear`, `dir`,
    `stdin`/`stdout`/`stderr` (`Inherit`, `Null`, `Piped`), and `spawn`,
    `status`, `output`.
  - `Child`: `id`, `take_stdin`/`take_stdout`/`take_stderr`, `try_wait`,
    `wait`, `terminate`, `kill`; its destructor closes the pipes it still
    holds and neither waits nor kills.
  - `Pipe`: `read`, `read_all`, `write_all`; `ExitStatus` with
    `success`; `Output`; `env(name)`.
- **Runtime entries** `proc_*`: C in `runtime/hosted/src/kern_rt.c`, Go in
  `compiler/interp/process.go`, a `case` each in
  `compiler/backend/llvm/calls.go`.
- **A test hook**, `process.fail_next_io`, guarded in the entry itself.
- **`scripts/linux-check.sh`**: the std/process tests in an
  `ubuntu:24.04` container (glibc 2.39, arm64) before each commit.

## The design and why

- **One implementation where possible.** Recording a command, computing
  the child's environment (R3), R4's validation, the `Child` bookkeeping
  and `status`/`output` are kern code, so both engines run the same code.
  The runtime entries do only what needs the system: the spawn with R4's
  error order, R2's search (it sits between entering the directory and
  executing, so it cannot run in kern first), waits, signals and pipe
  I/O. Those exist twice and are held together by fixtures and Go tests
  that run both engines.
- **Native spawn** is `posix_spawn`, with absolute paths so that a
  relative `dir` is resolved once. A child keeps exactly descriptors 0 to
  2: `POSIX_SPAWN_CLOEXEC_DEFAULT` with an inherit action per open stream
  on macOS, `posix_spawn_file_actions_addclosefrom_np(3)` with
  `adddup2(fd, fd)` on Linux, which makes glibc 2.34 a build requirement
  (approved by the owner; in the README). Signals: the defaults, except
  those ignored at start.
- **Closed standard descriptors.** The native runtime fills each of 0 to
  2 that was closed at start with a close-on-exec `/dev/null`, before any
  thread exists, so no later descriptor lands there and a spawn needs no
  lock to tell a standard stream from a file another thread is opening.
  A child still gets such a stream closed.
- **The interpreter** spawns with `syscall.ForkExec` after marking every
  descriptor from 3 up close-on-exec (Go closes nothing in a child). When
  the program's output is a captured writer, a forwarding goroutine
  copies an inherited stream into it, and `wait` waits for what the child
  wrote, never for a grandchild that still holds the stream.
- **EPIPE, never SIGPIPE.** `F_SETNOSIGPIPE` on macOS; on Linux the write
  runs with SIGPIPE blocked and the signal it raised is taken back.
- **`output`** reads stdout and stderr on scoped threads, each given its
  `Pipe` by `own`, while the calling thread writes the input. Nothing is
  cancelled: each operation closes its own pipe end when it ends, and
  `first_err` picks write, stdout, stderr, then wait.

## Spec amendments (v0.2)

Each was found to be unimplementable or wrong as written and is noted in
the spec where it applies:
- the interpreter child's signal mask is the one `kern` was started with
  (Go restores it and cannot be told otherwise);
- the parent environment keeps the first entry of a repeated name (Go's
  runtime drops later ones);
- SIGCHLD is reset to its default at start (ignored, it makes children
  unwaitable), and SIGSEGV/SIGBUS, which both runtimes catch, reach a
  child at their default even when ignored at start;
- the macOS moment between `pipe` and its close-on-exec flag cannot reach
  a `std/process` child;
- `output` runs every operation to its end instead of cancelling on the
  first failure;
- a stream closed at start reads as end of file and takes writes in the
  program itself;
- `take_*` return `Result[Pipe, io.IoErr]` (EBADF when there is no pipe):
  an `Option[Pipe]` cannot be destructured, since `Pipe` has a destructor
  (`OWN_DESTRUCTURE_DESTRUCTOR`), while `?` moves a `Pipe` out of a
  `Result`;
- the descriptor fixtures compare with an `ls` baseline (macOS's `ls`
  opens two descriptors of its own, Linux's one).

## Found on the way

- **`kern_start`'s probe pipe** could take the place of a closed
  descriptor 0, so a child "inherited" a pipe as its stdin.
- **Go 1.22 and later reopen closed descriptors 0 to 2 on `/dev/null`**
  at start, so in the interpreter the program's stdin is never closed;
  the closed-stdin tests expect `open` there and `closed` natively.
- **`os.File.Fd()` switches a descriptor to blocking**, which breaks read
  deadlines; the interpreter reads descriptors through `SyscallConn`.
- **A redirection probe** (`: <&0`) opens a file onto a free descriptor 0
  and sees it open; the tests use `[ -e /dev/fd/0 ]`, which only stats.
- **Linux raises SIGPIPE on a short write too**: when the reader goes
  away during a blocked write, the call returns the bytes already written
  and still raises the signal. The Linux gate caught it.
- **`kern run` runs in the interpreter**; native checks build a binary.
- **Language gotchas**: `take`, `out` and `truncate` are keywords; struct
  fields are private even in std modules; string literals have no `\x`
  escape; tuples exist only for multi-access returns.
- **Two language bugs**, fixed as their own commits: the checker rejected
  every `edit` argument to `Scope.spawn`, which Rule 5.5.3 allows
  (c61aace); and a failed native spawn leaked its `own` arguments, which
  the same rule says are destroyed (a3f95a8, with `testing.fail_next_spawn`).
- **`std_io` fails inside the Linux gate** because it writes into the
  working directory and the gate mounts the repository read-only; it
  passes on a writable copy.

## Verification

- **Fixtures**, in every configuration each runs in: `std_process_env`,
  `_run`, `_raw` (malformed raw calls refused), `_pipes`, `_output` (the
  deadlock trap), `_first_err` (all 16 cases), `_output_faults` (each
  injected fault, two at once, and a reader thread that cannot start),
  `_threads` (100 children compared with a baseline while the program
  holds a file and another child's pipes, `output` and `status` at once,
  a `Command` and a `Child` across threads), and
  `ffi/process_cloexec_stdout`.
- **`tests/process_test.go`** runs a program as the process under test in
  both engines: spawn-error order, relative `dir`, leaked and closed
  descriptors, environment parity with duplicate names, signals ignored
  at start (SIGHUP, SIGCHLD), the fault hook's guard, freestanding builds.
- **`compiler/interp/process_test.go`**: the wait failure path, the
  forwarding barrier and its states (sequenced with a hook, not timed),
  file and pollable-pipe writers, the descriptor sweep, and that no
  fixture leaves a child unwaited.
- **Repeated runs**: the threads fixture 20 times on macOS and on Linux;
  the output and pipes fixtures 15 times on Linux after the SIGPIPE fix.
- **Gates**: `golangci-lint run ./...` 0 issues and `go test ./...` green
  on the macOS host at every commit; the Linux gate's 28 tests pass.

## Reviews

- **speccritic** on the spec (five passes, then on v0.2): no critical
  finding left; the remaining flags are its prose checks.
- **plancritic** on the plan (ten passes): EXECUTABLE_AS_IS, 100.
- **prism** (openai gpt-6.1-sol) on each phase, every finding fixed or
  answered: SIGCHLD auto-reaping, a quadratic environment scan, empty
  environment entries; `wait` not closing stdin, the host's output file
  made blocking, a descriptor leak, a spawn-versus-open race (first fixed
  with a lock, which could deadlock on a FIFO open, then with the
  start-time placeholders), an abandoned test watchdog, unbounded test
  launches, the Linux gate's test filter and result cache; a reader spawn
  failure that could hang `output`, a too-broad SIGPIPE drain, a failed
  `ls` counted as a match. Two were declined with reasons: SIGSEGV and
  SIGBUS are not kept ignored in children (both engines agree, and spawn
  cannot set a signal to ignored; now in R4), and a non-blocking
  inherited stdin is the program's own descriptor, passed as Unix does.
- **realitycheck**: 100, no violation or drift, after Phase 4.

## Not done

- **`kern test --timeout`** stops a test without killing its children
  (R8): they keep running.
- **The interpreter child's signal mask** is the mask `kern` was started
  with, not an empty one, when whatever started `kern` blocked signals.
- **x86-64 Linux** is covered only by CI after a push; the Linux gate runs
  arm64. macOS is the host's (macos-latest in CI after a push).
- **A second reader thread that cannot start**, after the first started:
  `output` closes stdin before the scope joins, which is reasoned, not
  tested; `testing.fail_next_spawn` fails only the next spawn, the stdout
  reader, which the faults fixture covers.
- **`show-effects`** does not label the `proc_*` entries; nothing in the
  spec asks for it.
- The spec's non-goals stand: no changing the program's own environment
  or directory, no timeouts or sleep (a later `std/time`; *2026-11-01: `std/time` exists, `specs/std-time`, with sleep and deadlines; timeouts in `std/process` remain out of scope*), no shell
  parsing, process groups or terminals, no merged stderr, no Windows.
