# Plan: std/process (child processes and the environment)

**Spec:** `specs/std-process/SPEC.md` v0.2. **Profile:** davin-go.
**Review status.** plancritic (davin-go, with the spec), ten passes:
EXECUTABLE_AS_IS, 100, all 40 requirements covered. Folded in on the
way: relative `dir` resolved once, macOS inherit actions under
`CLOEXEC_DEFAULT`, the interpreter's descriptor sweep and forwarding
barrier, `output`'s failure protocol and fault hook, the Linux gate.
**Platform decision:** native Linux builds require glibc 2.34 or newer
(shared design, step 5). Approved by the owner on 2026-11-01.
**Order:** five phases. Each ends with `golangci-lint run ./...` (0
issues) and `go test ./...` green on the macOS host, the Linux gate
below from Phase 2 on, then `/phase-review` (prism with openai
gpt-6.1-sol, realitycheck, verifier; no clarion, kern keeps no
`docs/clarion-meta.json`) and one commit on main. Nothing is pushed;
remote CI runs whenever the owner next pushes.

**Linux gate.** `scripts/linux-check.sh` (new, Phase 2): runs
`go test -json ./tests -run '^TestFixtures$/run/std_process'` and
`'^TestFixtures$/ffi/process'`, then
`go test -json ./tests -run '^TestProcess'` and `go test -json
./compiler/interp -run '^TestProc'`, as separate commands (a `|` at the
top of one `-run` pattern would not reach the subtests). It fails when a
command exits non-zero, when any event is `fail`, or when a test the
current phase owns has no `pass` event: the script takes the expected
names as arguments (each phase's acceptance lists them, e.g.
`TestFixtures/run/std_process_env.kern`, and
`TestProcessSpawnOrder`) and checks them with `jq` over the events. All
of this runs in an
`ubuntu:24.04` container through the host's Docker (OrbStack), with Go
from `go.mod`'s version and the distribution's `clang` (glibc 2.39), on
the host's architecture (arm64), the repository mounted read-only and a
scratch build cache. It stands in for CI's two Linux jobs before a
commit; x86-64 Linux is covered by CI after the owner's push.
Every module edit is made in `compiler/resolve/prelude/process.kern` and
copied byte for byte to `std/process.kern` (`prelude_sync_test.go`
checks it).

## Design shared by every phase

**Where the logic lives.** Everything that can be written once is kern
code in `std/process`, so both engines run the same code:
- R1's recording (a `Command` holds `Vec`s of byte strings and an
  ordered list of environment edits);
- R3's environment computation, from a snapshot of the parent
  environment read through two intrinsics;
- R4's validation steps 1 to 6, in the spec's order;
- R5's bookkeeping (`take_*`, the remembered status, `kill` after reap);
- R7's `status` and `output`.

The runtime entries (C in `runtime/hosted/src/kern_rt.c`, Go in
`compiler/interp/intrinsics.go`) do only what needs the system: R4's
spawn with its (s), (a), (b), (c) order, R2's search (it sits between
(a) and (c), so it cannot run in kern before the spawn call), waits,
signals and pipe I/O.

**Intrinsics.** Module-level bodiless functions of a std module are
intrinsics by bare name (`lower.intrinsicName`), so every new one is
prefixed `proc_` to stay clear of the prelude's names. Each gets a
`case` in `compiler/backend/llvm/calls.go` (the `io_*` pattern:
`fe.seqParts` for views, `zext` for `bool`) calling `kern_proc_*`, and
an entry in the interpreter's intrinsic map. Every one returns a
non-negative result or `-errno`, as `std/io`'s do.

| intrinsic | result | does |
|---|---|---|
| `proc_env_count() -> usize` | entries | size of the start-time environment snapshot |
| `proc_env_at(i: usize, buf: edit[]u8) -> usize` | entry length | copies entry `i` (`name=value`) as `io_arg` copies an argument |
| `proc_spawn(prog: view[]u8, argv: view[]u8, argc: usize, envp: view[]u8, envc: usize, dir: view[]u8, has_dir: bool, search: view[]u8, modes: view[]u8, fds: edit[]i32) -> i64` | handle | R4: argv and envp are NUL-terminated strings packed back to back; `search` is the `:`-separated directory list; `modes` is 3 bytes (0 Inherit, 1 Null, 2 Piped); `fds` receives the three parent pipe ends or -1 |
| `proc_pid(h: i64) -> u32` | pid | R5 `id` |
| `proc_wait(h: i64, block: bool, st: edit[]i32) -> i32` | 1 ended, 0 running | `st[0]` 0 Exited / 1 Signaled, `st[1]` code or signal; EINTR retried |
| `proc_signal(h: i64, sig: i32) -> i32` | 0 | `kill(2)`; EINTR retried |
| `proc_release(h: i64)` | none | frees the interpreter's table entry; no-op natively |
| `proc_flush_inherited(h: i64)` | none | the interpreter's completion barrier for inherited captured streams (below); no-op natively, where the child writes the real descriptor |
| `proc_read(fd: i32, buf: edit[]u8) -> i64` | bytes | `read(2)`, EINTR retried |
| `proc_write(fd: i32, data: view[]u8) -> i64` | bytes | `write(2)`, EINTR retried, EPIPE without SIGPIPE (Phase 3) |
| `proc_close(fd: i32) -> i32` | 0 | `close(2)` |
| `proc_fault_arm(kind: u8, code: i32)`, `proc_fault_take(kind: u8) -> i32` | none, old code | the fault hook of Phase 4: arm a slot; atomically take and clear it |
| `proc_errno(which: u8) -> i32` | errno | the host's EINVAL (0), ENAMETOOLONG (1), EBADF (2), EPIPE (3) or EIO (4, for the fault fixture): the kern code builds the first three as `io.IoErr.Other` values and compares a pipe write's error with the fourth, so no errno number is written into kern code |

**Entry contracts.** These are raw entries like `std/io`'s `io_*`: std
modules export every declaration, so a program can call them directly,
and each entry validates everything memory safety depends on, in both
engines, returning EINVAL for malformed input without starting
anything:
- `argv` must hold exactly `argc` NUL bytes and end with one; `envp`
  likewise with `envc` (zero counts need an empty view);
- `modes` must be exactly 3 bytes, each 0, 1 or 2; `fds` at least 3
  elements, and only the first 3 are written; `st` at least 2;
- `prog`, `dir` and `search` are bounded by their view lengths, never
  by a NUL;
- `proc_env_at` copies at most `len(buf)` bytes and returns the full
  length (the caller retries with a larger buffer, as `io_arg`'s
  callers do).
Ownership is not validated: a handle is valid from `proc_spawn` until
`proc_release` (the `Child` destructor, the only caller in the library),
and natively, where the handle is the pid, a forged handle reaches
`waitpid` or `kill` on that pid, as a forged descriptor reaches `close`
in `io_close`. In the interpreter an unknown handle returns `-ESRCH`
from `proc_wait` and `proc_signal`, 0 from `proc_pid`, and is ignored by
`proc_release` and `proc_flush_inherited`; `proc_release` of a released
handle is a no-op in both engines. `tests/run/std_process_raw.kern`
calls `proc_spawn` with each malformed shape (a count that disagrees with
the NULs, a missing final NUL, a 2-byte `modes`, a mode of 3, a 2-element
`fds`) and requires `other` (EINVAL) for each in all four
configurations.

The handle is the pid natively and a key of a new `sharedState.procs`
table in the interpreter (the pid, and a `sync.WaitGroup` for the
forwarding goroutines of Phase 2). The interpreter's pipe ends live in the
existing `sharedState.files` table (keys from 3 up), so they never
collide with 0 to 2.

**Types** (fields private by convention; std modules export everything):
- `struct Pipe nocopy { fd: i32; kind: u8 }` (0 the child's stdin, so
  writable; 1 stdout; 2 stderr) with
  `destructor(edit self)` calling `proc_close` when `fd >= 0`.
- `struct Child nocopy { h: i64; fds: [3]i32; reaped: bool; st: [2]i32 }`
  with a destructor that closes the `fds` still `>= 0` and calls
  `proc_release`.
- `struct Command { program: Vec[u8]; args: Vec[Vec[u8]]; edits: Vec[EnvEdit]; cleared: bool; dir: Option[Vec[u8]]; modes: [3]u8 }`,
  `union EnvEdit { Set(name: Vec[u8], value: Vec[u8]); Remove(name: Vec[u8]) }`.
  `new` sets `cleared` to false. `env_clear` empties `edits` and sets
  `cleared` to true for good (no method resets it), so discarded edits
  are gone, step 4 of R4 checks only the retained ones, and the child's
  environment starts empty (R3 step 1).

**Native spawn** (`kern_proc_spawn`), in R4's order:
1. `kern_flush()` (validation already ran in kern); its result is
   ignored, so a failed flush neither stops nor fails the spawn (R4).
2. (s) for each stream in order: `Null` opens `/dev/null` with
   `O_CLOEXEC`; `Piped` makes a pipe (`pipe2(O_CLOEXEC)` on Linux; `pipe`
   plus `FD_CLOEXEC` on macOS, safe there because of step 5's flag). Any
   new descriptor below 3 is moved with `fcntl(F_DUPFD_CLOEXEC, 3)`, so
   none can take the place of a closed 0, 1 or 2.
3. (a) when `has_dir`: `stat` the path (its errno, typically ENOENT or
   ENOTDIR for a missing component); a result that is not `S_ISDIR` is
   ENOTDIR, checked before anything else about it; then `access(X_OK)`
   (EACCES).
4. (b) resolve, to an absolute path whenever a relative one is
   involved, so that nothing is resolved twice. An absolute `prog` is
   used as is. A relative `prog` holding `/` becomes `cwd/prog`, where
   `cwd` is `dir` when it is absolute, else `getcwd()/dir` (or
   `getcwd()` alone without `dir`); `getcwd` is called only in these
   cases and a failure there is its errno, reported as (a). A relative
   `dir` is likewise made absolute for `addchdir_np` (this gives R2's
   "relative to the child's working directory" without depending on how
   a platform orders `chdir` and `exec` inside `posix_spawn`, and
   `addchdir_np` is then given the absolute `cwd`); otherwise R2's search over
   `search`: skip empty and relative elements, take the first
   `d/prog` that `stat`s as a regular file and passes `access(X_OK)`;
   ENOENT when none does.
5. (c) `posix_spawn` with file actions: `dup2` each Null or pipe child end
   onto 0, 1 or 2; `addchdir_np` with the absolute `cwd` when `has_dir`;
   attributes: `SETSIGMASK` (empty), `SETSIGDEF` (every signal except
   those in the ignored-at-start set of Phase 1). Descriptors:
   - macOS: `POSIX_SPAWN_CLOEXEC_DEFAULT`, which closes in the child every
     descriptor no file action names, so each `Inherit` stream whose
     parent descriptor is open (`fcntl(F_GETFD)` succeeds) also gets
     `posix_spawn_file_actions_addinherit_np(fd)`, which inherits it even
     when it is marked close-on-exec; a closed one gets no action and
     stays closed.
   - Interpreter: `ForkExec` clears `FD_CLOEXEC` on each `Files[i]` that
     is already descriptor `i`, so the same holds there.
   - Test: `tests/ffi/process_cloexec_stdout` (native, with a C helper
     calling `fcntl(1, F_SETFD, FD_CLOEXEC)`) spawns an inherited
     `printf x` and requires the `x` in its output; `TestProcInheritFile`
     sets the flag on its temporary file first for the interpreter.
   - Linux: `posix_spawn_file_actions_addclosefrom_np(3)`, which closes
     every descriptor from 3 up in the child whatever its flags. This
     makes glibc 2.34 (August 2021) a platform prerequisite of native
     Linux builds: the hosted runtime fails to compile with an `#error`
     naming it under `!__GLIBC_PREREQ(2, 34)`, named in the README's
     build requirements next to `clang`. CI's ubuntu-24.04
     images and the Linux gate have 2.39. No older-glibc mechanism exists
     that closes descriptors from 3 up inside `posix_spawn`, and a
     flags-only fallback would not meet R4, so none is offered. The
     owner approved the prerequisite (this plan's header), and Phase 2 itself adds the prerequisite to the README's
     build requirements in the same commit as the `#error`. An `Inherit`
     stream whose descriptor is open gets
     `posix_spawn_file_actions_adddup2(fd, fd)`, which glibc (2.29 and
     later, as POSIX.1-2024 specifies) turns into clearing `FD_CLOEXEC`
     in the child, so a standard descriptor the program marked
     close-on-exec is still inherited; a closed one gets no action and
     stays closed.

   So the guarantee does not rest on close-on-exec flags at all, and a
   descriptor the program inherited without the flag, or one C code in an
   FFI fixture opened, reaches no child either. The flags (Phase 1, step
   2 here) still keep other spawners, such as `system(3)` in C code,
   from inheriting `std/io`'s and `std/process`'s descriptors.
6. Close the child ends in the parent; on any failure close every
   descriptor made in step 2 and return `-errno`.

**Interpreter spawn**: the same steps in Go over `syscall.ForkExec`
(not `os.StartProcess`, so the pid is waited with `syscall.Wait4` and
`WNOHANG` works), with the same absolute `cwd` and program path.
- **Streams.** `Files[i]` is the child end for Null and Piped. For
  Inherit, stdin is descriptor 0 (the interpreter reads `os.Stdin`
  directly); stdout and stderr are the configured writer's own
  descriptor (`f.Fd()`) when the `lockedWriter` wraps an `*os.File`,
  whatever its number, so an `interp.New` given a file receives the
  child's output in that file; and forwarding (below) otherwise. A
  descriptor that is closed (`fcntl(F_GETFD)` fails) is given as
  `^uintptr(0)`, closed in the child. `TestProcInheritFile` in
  `compiler/interp` gives `interp.New` a temporary `*os.File` (descriptor
  above 2) as stdout and checks that a child's inherited output lands in
  it.
  The table keys of `sharedState.files` are not OS descriptors; what
  matters is the OS descriptor behind each `os.File`. `ForkExec` places
  `Files` by first moving any entry whose descriptor is below its target
  out of the way and then `dup2`ing each onto its slot, closing the slots
  given `^uintptr(0)`, so a pipe end the OS numbered 0 can never become
  the child's closed stdin. In the parent, every `os.Pipe` and
  `/dev/null` end whose OS descriptor is below 3 is moved up with
  `fcntl(F_DUPFD_CLOEXEC, 3)` before it is used, as natively, so the
  interpreter's own reads of descriptor 0 (`io_read`) never see a pipe.
- **Descriptors.** Go creates its own descriptors close-on-exec and
  `ForkExec` passes only `Files`, but it closes nothing else, so a
  descriptor the host process inherited without the flag would leak, and
  Go offers no child-local close-from. The interpreter enforces R4
  itself: each `proc_spawn`, under a mutex shared by all spawns of the
  process, lists `/dev/fd` and sets `FD_CLOEXEC` on every descriptor
  from 3 up that lacks it, then spawns. It fails closed: when `/dev/fd`
  cannot be listed, or `fcntl` fails with anything but EBADF (a
  descriptor closed meanwhile, which can no longer leak), the spawn
  returns that errno and starts nothing. The sweep runs on every spawn
  rather than once, so a descriptor the host gains later (cgo, an
  embedder's own `syscall` calls) is covered too. It changes only the
  host's flags, never its descriptors; the effect is that the host's own
  later children no longer inherit them either. This host-wide side
  effect is an accepted design decision, stated in the `interp` package
  documentation and in the report: the alternative, spawning through a
  helper process that closes descriptors before exec, costs an extra
  process per spawn and a second binary to ship, for a case (a host that
  deliberately passes inherited descriptors to its own children while
  running the interpreter) no current embedder has. This is the only enforcement point (no sweep in
  `cmd/kern`), so `interp.New` used directly and the `kern` binary behave
  alike. Scope: R4's "while other threads of the program open files"
  is met because every descriptor a kern program can create in the
  interpreter is made by Go with close-on-exec set atomically, and kern
  has no cgo. A descriptor an embedder creates without the flag on its
  own thread, concurrently with a spawn, is outside the program and not
  covered; the `proc_spawn` comment says so.
- **Signals.** `ForkExec` resets the signals Go handles to the default
  and leaves those ignored at start ignored, as R4 requires; the mask is
  the one `kern` started with (R4 as amended).
- **Inherit into a captured writer.** When the program's stdout or
  stderr is a captured writer (the `lockedWriter` wraps anything other
  than `*os.File`), Inherit makes a pipe and a goroutine copies it into
  the `lockedWriter`. Reaping and the completion barrier are separate:
  `proc_wait` (both `block` values) only reaps and reports, so `try_wait`
  never waits for forwarding; the barrier is a separate intrinsic,
  `proc_flush_inherited(h)`, that `wait` and `status` call after the
  status is known, including when `try_wait` reaped the child earlier.
  The goroutine is the pipe's only reader and owns the read end; it
  closes it when it stops. Its states, each change made under the
  handle's mutex:
  - `forwarding`: it reads through `os.File` (the poller) and writes each
    chunk to the writer before reading again;
  - `flushing`: entered when it sees the `flush` request before a read
    (it has no chunk in flight then). It first clears the deadline
    (`SetReadDeadline(time.Time{})`, since `RawConn.Read` checks an
    expired deadline before calling its function), then measures the
    unread bytes once with `ioctl(FIONREAD)` and reads exactly that many
    with raw non-blocking reads (`SyscallConn().Read` whose function
    calls `syscall.Read` and always returns `true`, so the poller never
    waits). A short read continues with the remainder; EINTR is retried;
    EAGAIN before the count is reached (bytes another reader took, which
    cannot happen while the goroutine is the only reader) ends the drain
    early; end of file or any other error moves to `done`. It writes the
    drained bytes, closes the `flushed` channel and returns to
    `forwarding` for anything a background grandchild still writes;
  - `done`: entered on end of file or on any read error other than the
    deadline's timeout (which only means "check for a request"). It
    closes the read end and the `done` channel, and, if a request is
    pending, `flushed` too.

  `proc_flush_inherited(h)` is idempotent (a `sync.Once` per handle).
  Lock order: under the handle's mutex it sets the request flag and then
  calls `SetReadDeadline(time.Now())`, which makes a blocked `Read`
  return a timeout without consuming data, and releases the mutex; then
  it waits until `flushed` or `done` is closed. The goroutine checks the
  flag under the same mutex, and when it is set clears the deadline
  before releasing it. So the deadline is always set before the reader
  can clear it, and the raw drain never runs under an expired deadline.
  If the goroutine checked (flag unset) just before the request, its next
  `Read` returns the timeout, it checks again and finds the flag. A
  timeout seen while the flag is unset, or during the drain (which runs
  with no deadline and so cannot time out), only leads back to the
  check. A goroutine that reached
  `done` before the request (the child and every descendant closed the
  stream) satisfies it at once; one that reaches `done` during the
  flush satisfies it then.

  The child was reaped before step 2, so every byte it wrote is either
  already written by the goroutine or among the unread bytes measured in
  step 3. Bytes written after that measurement, by a descendant, are not
  waited for, so the barrier is bounded by one pipe's capacity plus the
  writer's own speed, and a writing grandchild cannot extend it. A
  writer error makes the goroutine keep reading and discard (R4).

## Phase 1: module skeleton, the environment, close-on-exec (R6 part, R9)

**Steps.**
1. Add `"process"` to `resolve.StdModules` and to the embedded prelude
   file list; create `prelude/process.kern` with the module header
   comment, `import "std/io"`, the `Stdio`, `ExitStatus`, `Output`
   declarations and `ExitStatus.success`, and copy it to
   `std/process.kern`.
2. `proc_env_count` and `proc_env_at`. Native: `kern_set_args`'s
   neighbour `kern_env_init`, called from `kern_start`, copies `environ`
   into a runtime array, keeping only the first entry of each name (the
   glossary's parent environment), so later changes by C code in an FFI
   fixture do not alter the snapshot. Interpreter: a package-level
   `var startEnv = syscall.Environ()` in `compiler/interp`, evaluated
   during package initialisation, before `main` or any test can call
   `os.Setenv` (no non-test Go code in the repository calls it); Go has
   already dropped later duplicates, so both engines see the same list.
3. `env(name)` in kern: invalid name (empty, `=`, NUL) is `None`;
   otherwise the first snapshot entry whose bytes before the first `=`
   equal `name`.
4. Native: record the signals ignored at start in `kern_start`
   (`sigaction(sig, NULL, &old)` for 1 to `NSIG - 1`, before the runtime
   installs its handlers) into a `sigset_t` for Phase 2, leaving SIGCHLD
   out, and reset SIGCHLD to `SIG_DFL` with no flags (clearing
   `SA_NOCLDWAIT`), so children stay waitable (R4 as amended; found by
   prism in Phase 1's review).
5. Close-on-exec audit of every descriptor the runtime creates: in
   `kern_io_open`, `O_CLOEXEC` on both `open` calls; in `kern_io_temp`,
   `mkostemp(tmpl, O_CLOEXEC)` instead of `mkstemp` (macOS 10.12, glibc
   2.7); `opendir` already opens close-on-exec on both libcs; the
   `kern_probe` pipe is made in `kern_start` before any thread exists and
   then flagged. `std/process`'s own descriptors are atomic on Linux
   (`pipe2`, `O_CLOEXEC`, `F_DUPFD_CLOEXEC`). macOS has no `pipe2`, so a
   pipe there is flagged right after `pipe`; the window cannot reach a
   `std/process` child because of `POSIX_SPAWN_CLOEXEC_DEFAULT` (shared
   design, step 5), which is what R6 asks ("no window in which another
   thread's `spawn` can inherit it"). The interpreter's `os.OpenFile`,
   `os.CreateTemp` and `os.Pipe` already set the flag atomically.
6. Freestanding (spec §1 constraint 5): a Go test in
   `tests/process_test.go`, `TestProcessFreestanding`, builds with
   `--env=freestanding` one program calling `io.read_file` and one
   calling `process.env`, and requires both to fail with the same kind of
   error (probe it first: an undefined-symbol link error is expected).
7. Fixture `tests/run/std_process_env.kern` (first part of spec §4 item
   4): `env("PATH")` is `Some`, `env("KERN_STD_PROCESS_UNSET")` and
   `env("A=B")` are `None`.

**Acceptance.** `TestStdModulesCheck` covers `process`; the env fixture
passes in the four configurations; `TestProcessFreestanding` passes;
`prelude_sync_test.go` green.

8. Probe, before Phase 2 builds on it: a scratch program in the module
   declares `Pipe`, `Child` and `Command` with their planned fields and
   destructors and moves one of each by `own` into a scoped `spawn`, in
   both engines. R6 requires all three to be `Send`; if the checker
   refuses one, that is a language finding reported to the owner before
   Phase 2 starts, not worked around.

**Risks.** `syscall.Environ()` and C `environ` must list the same
entries in the same order, with duplicates dropped the same way.
Phase 2's `TestProcessEnvParity` checks it directly.

## Phase 2: spawning, waiting, signals, inherited and null streams (R1–R5, R8)

**Steps.**
1. `Command`: `new`, `arg`, `env_set`, `env_remove`, `env_clear`, `dir`,
   `stdin`, `stdout`, `stderr` (R1; no system calls).
2. `child_env(view self) -> Vec[u8]` in kern: R3's two steps over the
   snapshot (or nothing after `env_clear`), packed NUL-terminated, with
   its count.
3. `spawn(view self)`: R4's validation steps 1 to 6 (each a small
   function returning `Option[io.IoErr]`, with EINVAL and ENAMETOOLONG
   taken from the host through `proc_errno`, since ENAMETOOLONG differs
   between macOS (63) and Linux (36)); the `search` list from the snapshot's
   `PATH` (or `/usr/bin:/bin` when unset); `proc_spawn`; a `Child` from
   the handle and `fds`.
4. Native `kern_proc_spawn` and interpreter `proc_spawn` per the shared
   design, Inherit and Null only in this phase (`Piped` returns EINVAL
   until Phase 3; no fixture uses it yet).
5. `Child.id`, `try_wait`, `wait(own self)`, `terminate`, `kill`, and
   the destructor, over `proc_pid`, `proc_wait`, `proc_signal`,
   `proc_release`. `try_wait` stores the status in `st` and sets
   `reaped` only on a status; on an error it returns before touching any
   field, so the `Child` is unchanged (R5). `wait` returns the stored
   status when `reaped`. When `proc_wait` fails inside `wait`, `wait`
   returns the errno and the consumed `Child`'s destructor closes the
   descriptors still held and calls `proc_release`, so the handle is
   gone and nothing is reaped (R5). `terminate` and `kill` return
   `Ok(())` without a call when `reaped`. A Go unit test in
   `compiler/interp` covers the failure path: `proc_wait` on a handle
   whose pid was already reaped behind its back returns `-ECHILD`, and a
   `try_wait` fixture-free check shows the handle's fields unchanged.
   The same file's `TestProcForwarding` covers the barrier with
   `interp.New` given a captured writer. A child `sh -c '(cat "$F"
   >/dev/null; printf late) & printf early'`, with `F` a FIFO the test
   made, leaves a grandchild holding the inherited stream until the test
   writes to the FIFO: `wait` must return, with `early` in the writer,
   while the test still holds the FIFO closed, so no timing threshold is
   involved (a 10 s watchdog only turns a hang into a failure, and
   cleanup always writes to the FIFO). The grandchild, reparented when
   its parent exits, cannot be reaped by the test; it signals its own
   end by writing `late` and then a line to a second FIFO the test reads
   during cleanup, so the test knows it has exited before removing the
   FIFOs. Three more cases cover the state machine: forwarding already
   `done` before `wait` (a child that exits with no descendants, `wait`
   called after a delay); `done` reached during the flush (end of file
   while the measured bytes are drained); and `try_wait` reaping first,
   then `wait` running the barrier once (a second barrier call returns at
   once). These cases are sequenced, not timed: the forwarding goroutine
   calls a package-private `procHook func(state string)` (nil outside
   tests) on each transition and before each blocking read, and the
   tests use it with channels to hold the goroutine at a chosen point,
   e.g. blocked in a read with child bytes still unread when the flush
   request arrives (the deadline-interrupt case), just after a flag
   check that found no request and before its `Read` (the request then
   arrives and the `Read` must time out and lead to the drain), or
   between `done` and the request. Watchdogs only turn a hang into a failure. A writer
   that sleeps 10 ms per call still receives all 1 MiB a child writes
   before `wait` returns; a writer that fails on its first call leaves
   `wait` returning normally; and `try_wait` on a running child that is
   blocked writing returns `None` at once.
6. `status(view self)`: a copy of the modes with `Piped` turned into
   `Null`, then spawn and wait.
7. Fixtures: `tests/run/std_process_run.kern` (spec §4 item 1) and the
   rest of `std_process_env.kern` (item 4: `env_set`, `env_remove`,
   `env_clear` with `/usr/bin/env`, `PATH` override not affecting the
   search, `dir` plus `pwd`, a relative `./tool` written with
   `io.write_file` into `io.make_temp_dir` and made executable by
   running `/bin/chmod 755` through `status`). A kern program cannot
   change its own working directory, so relative `dir` values are
   covered by `TestProcessRelativeDir` (step 8). Errors print as the
   `std_dirs` fixture prints them (`other` for `Other`).
8. Go tests in `tests/process_test.go`, each running a small generated
   kern program as the process under test, in the interpreter (`kern
   run`) and natively (the binary `kern build` makes), started with
   `syscall.ForkExec` so the test controls that process's working
   directory, descriptors, environment and signals, and requiring the
   expected result from both:
   - `TestProcessSpawnOrder`, R4's error order: a `dir` that is a file
     with a missing program gives ENOTDIR; a missing program with a
     non-executable regular file earlier on the search list gives
     ENOENT; a non-executable `./x` gives EACCES.
   - `TestProcessRelativeDir`: `kern` started in a temporary directory
     holding `a/b/tool`; `dir("a")` with program `b/tool`, and
     `dir("a")` with program `./b/tool`, both run `tool` once (a doubled
     `a/a/b/tool` would be ENOENT).
   - `TestProcessDescriptors`, two runs. First, `kern` started with an
     extra descriptor 3 that has no close-on-exec flag
     (`exec.Cmd.ExtraFiles`); the child `ls /dev/fd` lists what an `ls
     /dev/fd` started with descriptors 0 to 2 only lists (macOS's `ls`
     opens two descriptors of its own, Linux's one). Second, `kern` started with
     descriptor 0 closed (`syscall.ForkExec` with `Files[0] =
     ^uintptr(0)`); the child, with `stdin(Inherit)`, is
     `sh -c 'if [ -e /dev/fd/0 ]; then echo open; else echo closed; fi'`,
     which opens nothing (`test -e` only stats; a redirection probe opens
     a file onto the free descriptor 0 and sees it open, as found in
     Phase 2), and must print `closed` natively. In the interpreter it
     prints `open`: Go's runtime reopens a closed descriptor 0 to 2 on
     `/dev/null` at start (Go 1.22 and later), so the program's stdin is
     open and R4 hands that on. (The piped variant is added in Phase 3.)
   - `TestProcessEnvParity`: `kern` started through `syscall.ForkExec`
     (which, unlike `os/exec`, does not collapse duplicate names) with a
     controlled environment `PATH=<the test's>`, `A=1`, `B=2`, `A=3`,
     `C=`; the child `/usr/bin/env` with no environment edits must list
     exactly `PATH=...`, `A=1`, `B=2`, `C=` in that order in both engines,
     and `env("A")` must be `1`.
   - `TestProcessSignals`: `kern` started with SIGHUP ignored
     (`signal.Ignore` in a helper process that then execs `kern`); the
     child `sh -c 'kill -HUP $$; echo alive'` prints `alive`, and with
     default dispositions it is `Signaled(1)`. A third run starts `kern`
     with SIGCHLD ignored: a child `true` still reports `Exited(0)` from
     `wait` (not ECHILD) in both engines.

**Acceptance.** Both fixtures pass in the four configurations and the
`TestProcess*` tests in both engines, on the host and through the Linux
gate. `TestProcNoZombies` in `compiler/interp` runs the two fixtures
through `interp.New` in the test process (so their children are the test
process's own) and then requires `syscall.Wait4(-1, WNOHANG)` to return
ECHILD: every child a fixture started was waited for. The package's
tests do not run in parallel, so no other test's children interfere.

**Risks.**
- `posix_spawn_file_actions_addchdir_np` needs macOS 10.15 and glibc
  2.29; CI's macos-latest and ubuntu-24.04 images have both. A missing
  declaration fails the runtime build, which `go test` reports at once.
- Exec-failure reporting: glibc's `posix_spawn` returns the exec errno
  since 2.24 and macOS's always has; Go's `ForkExec` does too. The order
  test checks the three that matter.
- Inherit forwarding in the interpreter is new code on the output path
  of every fixture that spawns; the run fixture's before/after lines
  around an inherited `printf` check ordering.

## Phase 3: pipes (R4 Piped, R5 take, R6)

**Steps.**
1. `Pipe.read`, `read_all`, `write_all` over `proc_read` and
   `proc_write`: the direction check first (EBADF, R6), then empty
   `data` returning `Ok(())` without a call, then the retry loop of
   `std/io`'s `write_all`.
2. `Child.take_stdin`, `take_stdout`, `take_stderr`: move `fds[i]` into
   a `Pipe` and set it to -1, returning `Result[Pipe, io.IoErr]` (EBADF
   when there is none): an `Option[Pipe]` cannot be destructured, which
   the fixture showed, so the spec's R5 was amended.
3. `Piped` in both spawn implementations (step 2 of the native spawn;
   `os.Pipe` in the interpreter, the parent end stored in `files`).
   `wait` closes `fds[0]` first, waits, then closes the rest (R5).
4. EPIPE without SIGPIPE natively: on macOS set `F_SETNOSIGPIPE` on the
   parent's stdin write end at creation; on Linux `kern_proc_write`
   blocks SIGPIPE in the calling thread (`pthread_sigmask`), writes, and
   on EPIPE consumes the SIGPIPE it raised with `sigtimedwait` and a zero
   timeout unless one was already pending, then restores the mask. The
   interpreter needs nothing: Go returns EPIPE for descriptors other than
   1 and 2.
5. Fixture `tests/run/std_process_pipes.kern` (spec §4 item 2), the
   EPIPE case synchronised on the child's `r` as the spec says.
6. `TestProcessDescriptors` gains the piped variant: with `kern`'s
   descriptor 0 closed, a child with `stdout(Piped)` and `stdin(Inherit)`
   still prints `closed` (the pipe did not land on 0, in the child or in
   the parent), in both engines.

**Acceptance.** The pipes fixture and the piped `TestProcessDescriptors`
run pass in the four configurations (both engines for the test), on the
macOS host and through the Linux gate.

**Risks.** The Linux SIGPIPE path is the only platform-specific
signal code; the EPIPE fixture fails (the program dies with SIGPIPE) if
it is wrong, on Linux only, which is why the Linux gate must pass before
Phase 3 is committed.

## Phase 4: running to completion and threads (R7, R6 Send)

**Steps.**
1. `output(view self, input)`, with R7's failure protocol (the `Send`
   probe of Phase 1, step 8, has established that pipes move into
   scoped threads):
   - spawn with all three streams `Piped` (a spawn error is returned);
     take the three pipes;
   - in a `thread.scope()`, `spawn` two readers, each given its `Pipe` by
     `own`, reading to end of file or to its first error, then dropping
     (closing) its pipe and returning `Result[Vec[u8], io.IoErr]`;
   - the calling thread writes `input`; EPIPE ends the write without an
     error (R7); any other error is recorded and ends the write; either
     way the calling thread then drops (closes) stdin;
   - `get` both joins, then `wait`.
   No operation is cancelled: each runs to its own end, and each pipe is
   closed only by the thread using it, as R7 requires. The result is
   `first_err(write, out, err, wait)`, a separate kern function applying
   R7's precedence.
2. `tests/run/std_process_first_err.kern` calls `process.first_err`
   directly (std modules export every declaration) with every
   combination of present and absent errors in the four positions and
   prints the chosen one, since a reader or non-EPIPE write failure
   cannot be staged with real children alone.
   Failure paths of the whole protocol are covered by fault injection.
   The hook's contract:
   - `process.fail_next_io(kind: u8, code: i32)` arms slot `kind` (a
     `Pipe`'s `kind`: 0 stdin writes, 1 stdout reads, 2 stderr reads)
     with `code` through a new intrinsic `proc_fault_arm(kind, code)`.
     The guard sits in the intrinsic itself, not only in the wrapper,
     since std modules export every declaration: the interpreter's
     `proc_fault_arm` traps `explicit` unless `it.TestHooks`, as
     `testing.require_hooks` does, and natively `kern_proc_fault_arm` is
     defined only in the test-hooks build of the runtime, next to the
     existing `kern_test_*` entries, so a program calling it without
     `--test-hooks` fails to link. `tests/negative`-style checks mirror
     the existing ones for `require_hooks`: a fixture calling
     `process.proc_fault_arm` directly without hooks traps in the
     interpreter and fails to build natively, and with hooks it arms.
   - `Pipe.read` and `Pipe.write_all` call `proc_fault_take(self.kind)`
     before their system call. It atomically exchanges the slot with 0
     (an `atomic.Int32` per slot in the interpreter, `__atomic_exchange_n`
     natively) and returns the old value; a non-zero value is returned as
     that errno instead of making the call. So an armed fault is consumed
     exactly once, by the first operation of that kind in the process,
     and nothing is left armed afterwards. Unarmed, the cost is one
     atomic load per pipe operation.
   - Slots are process-wide; the fault fixture runs one `output` at a
     time, which is what associates a fault with an invocation.

   `tests/run/std_process_output_faults.kern` (header `test-hooks: true`,
   as the `fail_next_alloc` fixtures have, so it runs where hooks exist)
   uses one controlled child for every case:
   `sh -c 'trap "" PIPE; cat >/dev/null; printf out; printf err >&2; printf done >"$D/marker"'`
   with `D` a temporary directory set by `env_set`. Ignoring SIGPIPE
   makes a write to a closed pipe fail with EPIPE instead of killing the
   shell, so the child always reaches its last command. Per case (code is
   the host's EIO from `proc_errno`), the fixture prints the returned
   error and whether the marker exists:
   - stdin fault: the write fails at once, stdin is closed, `cat` sees end
     of file; error `other` (the injected one), marker present;
   - stdout fault: the first read fails, the stdout reader closes its
     end, `printf out` fails with EPIPE, stderr is read normally; error
     injected, marker present;
   - stderr fault: the same for stderr;
   - stdout and stderr faults together: error is the stdout one (R7's
     precedence); marker present.
   Each case removes the marker before the next.
3. Fixtures `tests/run/std_process_output.kern` (spec §4 item 3) and
   `tests/run/std_process_threads.kern` (item 5). The threads fixture,
   before starting its threads, opens a file with `io.io_open` and spawns
   a `cat` (the *holder*) with stdin and stdout piped, and keeps the file
   and both of the holder's pipes open, on the main thread, until every
   thread has joined; only then does it close them and wait for the
   holder. Four scoped threads, each given an owned `Command` (so
   `Command` crosses threads), spawn 25 `ls /dev/fd` children each with
   stdout piped, read each child's pipe on that thread, and check the
   listing equals the one a first `ls /dev/fd` child printed before the
   program opened anything else (what `ls` itself opens differs by
   system). Two more threads run the convenience
   methods concurrently with them: one calls `output` 10 times on
   `sh -c 'cat; printf e >&2'` with inputs `in0` to `in9` and checks each
   result's stdout is its own input and stderr is `e`; the other calls
   `status` of `sh -c 'exit N'` for N from 1 to 10 and checks each
   `Exited(N)`. The `Child` crossing is a separate,
   short-lived `true`: a fifth thread is given it by `own` and waits for
   it (`Exited(0)`), so it never touches the holder.

**Acceptance.** The output, fault and threads fixtures pass in every
configuration they run in, `TestProcNoZombies` is extended to the three,
and the threads fixture also passes 20 times in a row natively in release
(`go test ./tests -run '^TestFixtures$/run/std_process_threads' -count=20`).

**Risks.**
- `ls` might open more than one descriptor on some platform; the macOS
  host and the Linux gate are checked in this phase.
- The `/dev/fd` sweep runs inside `proc_spawn`, so the fixture harness's
  in-process interpreter is covered like the `kern` binary;
  `TestProcessDescriptors` checks the binary, and `TestProcSweep` in
  `compiler/interp` checks `interp.New` used directly with a descriptor
  opened by `syscall.Open` without `O_CLOEXEC`.

## Phase 5: documents and gates (spec §5)

**Steps.**
1. README: the library row and std module list name `std/process` in one
   clause.
2. `spec/types.md` §1: `"std/process"` in the module list, and
   `process.Command` among the example names.
3. ADR-013: a dated consequence note: process control exists, so every
   library prerequisite of self-hosting is met.
4. `docs/reports/std-process.md`: what was added, the shared design and
   why (one implementation of R3 and R4's validation, two of R2 and the
   spawn), what was found, verification, review findings, and "Not
   done": R8's `--timeout` note, the interpreter's child signal mask
   (R4 as amended), x86-64 Linux being covered only by CI after a push,
   and anything else verified on one OS only.
5. `go test ./tests` before the doc commit (`TestCitations` lints the
   documents).
6. Gates (mandatory): `golangci-lint run ./...` 0 issues, `go test ./...`
   green. Advisory: prism, realitycheck with this spec and plan, verifier
   over `tests/process_test.go` and `compiler/interp`'s process tests. Findings are fixed or justified in
   the report.

**Acceptance.** Mandatory gates pass; advisory findings recorded.

## Requirement map

| Requirement | Phase |
|---|---|
| R1 | 2 |
| R2 | 2 (search in both spawns, order test) |
| R3 | 2 |
| R4 validation, errors, Inherit, Null, ordering, descriptors, signals | 2 (close-on-exec of `std/io` in 1) |
| R4 Piped | 3 |
| R5 | 2 (life cycle), 3 (`take_*`, `wait` closing stdin) |
| R6 | 1 (`io_open`), 3 (pipes, EPIPE), 4 (Send) |
| R7 | 2 (`status`), 4 (`output`) |
| R8 | 2 (nothing added), 5 (report) |
| R9 | 1 |
| §4 tests | 1 to 4 |
| §5 documents | 5 |
