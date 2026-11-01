# Standard library: child processes and the environment

**Version:** 0.2 (2026-11-01; 0.1 the same day, amended during planning). **Scope:**
- a new embedded module `std/process` (`compiler/resolve/prelude/process.kern`,
  mirrored identically in `std/process.kern`, and added to
  `resolve.StdModules`) and its runtime entries in the hosted runtime, the
  interpreter and the LLVM backend;
- one change to existing behaviour in both engines: descriptors that
  `std/io` opens are close-on-exec (R6);
- fixtures under `tests/run/`, the README's library row, `spec/types.md` §1
  (the list of std modules), a dated note in
  `docs/decisions/ADR-013-self-hosting.md`, and the report
  `docs/reports/std-process.md`.

**Language impact:** none. No grammar, checker, IR or §16 change; library
additions only. **Follows:** ADR-013, whose last missing self-hosting
prerequisite is "file and process I/O" (process control); `specs/std-files/SPEC.md`
(the conventions `std/io` set).

**Review status.** speccritic (OpenAI gpt-6.1-sol, profile general, with
`specs/std-files/SPEC.md` and ADR-013 as context), five passes:
VALID_WITH_GAPS, no critical finding. Its one critical (a failing
interpreter output writer) and fourteen substantive warnings (validation
and spawn-error precedence, `output`'s error choice, EINTR and failed
waits, discarded environment edits, closed standard descriptors, empty
writes, fixture races and assumptions, a mandated spawn mechanism) are
folded into R2–R7 and §4. The rest are the preflight's prose flags
(errno names, requirement numbers, "may" in rationale).
Amended in 0.2 after plancritic found three requirements no
implementation could meet: the interpreter child's signal mask (R4),
duplicate names in the parent environment (glossary), and the
macOS pipe window (R6); and R7's `output` now runs every operation to
its end instead of cancelling on the first failure. speccritic on 0.2:
VALID_WITH_GAPS, preflight flags only.

**Glossary.**
- *Program*: the kern program calling `std/process`. *Child*: a process it
  starts.
- *Engine*: the interpreter or the native build. *Opt level*: debug or
  release. *Configuration*: an engine at an opt level (four in all).
- *Program's standard output*: what `print_str` and `io.write_stdout` write
  to in the running engine: file descriptor 1 natively; the interpreter's
  output writer in the interpreter (which `kern test` and the fixture
  harness capture). Likewise for standard error and standard input.
- *Parent environment*: the environment the program's process was started
  with, in its order, keeping only the first entry of a name that occurs
  more than once (Go's runtime drops later duplicates, so the native
  runtime drops them too). kern has no way to change it (§0).
- *Byte string*: a `view[]u8`; all names, arguments, paths and values are
  byte strings, not required to be UTF-8.
- *errno N*: `io.IoErr` built by `std/io`'s existing `err_of` mapping:
  `NotFound` for ENOENT, `Permission` for EACCES, `Exists` for EEXIST,
  `Other(N)` otherwise, with N the host's errno value.

## 0. Goals and non-goals

Goal: a hosted kern program can
- start a program with arguments, an environment and a working directory
  of its choosing;
- give the child the program's standard streams, `/dev/null`, or pipes;
- write to and read from those pipes, from any thread;
- wait for the child, poll it without blocking, or kill it;
- run a child to completion with its output captured, without deadlock
  for any output size;
- read a variable of its own environment.

Non-goals:
- changing the program's own environment or working directory (`setenv`
  is not thread-safe, and kern programs are multi-threaded);
- timeouts, sleeping and wall-clock time (a later `std/time`);
- shell parsing, globbing or quoting; process groups, sessions, terminals,
  resource limits, user or group changes; signal handlers in the program;
- sending signals other than SIGTERM and SIGKILL; reading a child's
  resource usage;
- merging a child's standard error into its standard output;
- listing the whole environment;
- Windows, and freestanding images (§1, constraint 5);
- killing children when the program traps or exits (§3 R8).

## 1. Constraints that shape the design

1. **Threads.** Any kern program may be multi-threaded (scoped threads are
   in the prelude; interpreter threads are goroutines). Starting a child
   must therefore be safe while other threads run, allocate and hold locks
   (the mechanism is the plan's), and a descriptor created on one thread
   must not leak into a child spawned at the same moment on another, which
   is why R6 makes descriptors close-on-exec when they are created.
2. **No references.** A method cannot hand out an access to a field that
   outlives the call (spec §5.3). So a child's pipes are not reachable as
   `child.stdout` accesses: they are moved out of the `Child` as owned
   `Pipe` values (`take_stdout`). An owned pipe can also be moved into a
   scoped thread to be read concurrently.
3. **Destructors are static and traps run none** (ADR-006). A `Child` or
   `Pipe` going out of scope runs its destructor; a trap or `exit` runs no
   destructor, so a program that must not leave children running kills them
   in its trap handler or before it exits.
4. **Two engines, one behaviour** (`std/io`'s parity rule,
   `specs/std-files/SPEC.md` R3). The interpreter runs children with real
   OS processes. Its standard streams are writers that may be captured
   (glossary), so "inherit" must mean the program's standard streams *as
   the engine sees them*, not the interpreter process's descriptors.
5. **Hosted only.** As with `std/io`, a freestanding image has no runtime
   behind these entries. Calling `std/process` in a freestanding build
   fails the build in the same way calling `std/io` does; this spec adds
   no new freestanding diagnostic.
6. **Descriptor numbers differ by engine.** The interpreter numbers its
   descriptors in its own table (`std/io` today). A `Pipe`'s descriptor is a
   field (std modules export every declaration, `spec/types.md` §1) but its
   value is not part of the interface and may differ between engines; no
   fixture prints it.

## 2. Declarations

`std/process` imports `std/io` and uses `io.IoErr` for every error, so a
program matches the variants after `import "std/io"`.

```kern
module process
import "std/io"

// What a child's standard stream is connected to.
union Stdio { Inherit; Null; Piped }

// How a child ended: its exit code (0 to 255), or the signal that killed it.
union ExitStatus { Exited(code: i32); Signaled(signal: i32) }

// A finished child's status and captured output (Command.output).
struct Output { status: ExitStatus; stdout: Vec[u8]; stderr: Vec[u8] }

// An owned end of a pipe to or from a child. Closed by its destructor.
struct Pipe { /* private to the implementation */ }

// A description of a child to start; reusable.
struct Command { /* private to the implementation */ }

// A started child. Its destructor closes the pipes it still holds.
struct Child { /* private to the implementation */ }
```

Functions and methods:

```kern
func env(name: view[]u8) -> Option[Vec[u8]]

func Command.new(program: view[]u8) -> Command
func arg(edit self, a: view[]u8)                     // impl Command
func env_set(edit self, name: view[]u8, value: view[]u8)
func env_remove(edit self, name: view[]u8)
func env_clear(edit self)
func dir(edit self, path: view[]u8)
func stdin(edit self, s: Stdio)
func stdout(edit self, s: Stdio)
func stderr(edit self, s: Stdio)
func spawn(view self) -> Result[Child, io.IoErr]
func status(view self) -> Result[ExitStatus, io.IoErr]
func output(view self, input: view[]u8) -> Result[Output, io.IoErr]

func id(view self) -> u32                            // impl Child
func take_stdin(edit self) -> Result[Pipe, io.IoErr]
func take_stdout(edit self) -> Result[Pipe, io.IoErr]
func take_stderr(edit self) -> Result[Pipe, io.IoErr]
func try_wait(edit self) -> Result[Option[ExitStatus], io.IoErr]
func wait(own self) -> Result[ExitStatus, io.IoErr]
func terminate(edit self) -> Result[(), io.IoErr]
func kill(edit self) -> Result[(), io.IoErr]

func read(edit self, buf: edit[]u8) -> Result[usize, io.IoErr]   // impl Pipe
func read_all(edit self) -> Result[Vec[u8], io.IoErr]
func write_all(edit self, data: view[]u8) -> Result[(), io.IoErr]

func success(view self) -> bool                      // impl ExitStatus
```

The private fields, the runtime entries (names, arguments, return
encodings) and how the library divides work between kern code and runtime
entries are left to the plan, within the requirements below.

## 3. Requirements

**R1 — Building a command.** `Command.new(program)` describes a child
running `program` with no further arguments, the parent environment, the
program's working directory, and all three streams `Inherit`. The
methods record, in call order:
- `arg(a)` appends one argument. The child's argument vector is
  `program` (as given, before any search) followed by the arguments.
- `env_set(name, value)`, `env_remove(name)` and `env_clear()` change the
  child's environment (R3).
- `dir(path)` sets the child's working directory; a later call replaces an
  earlier one.
- `stdin(s)`, `stdout(s)`, `stderr(s)` set a stream; a later call replaces
  an earlier one.

None of these methods fails or touches the system; every validation
happens in `spawn` (R4). A `Command` is not consumed by `spawn`, `status`
or `output` and may be used again.

**R2 — Finding the program.**
- A `program` holding a `/` is used as given. A relative one is resolved
  against the child's working directory (the one `dir` set, else the
  program's).
- Otherwise the program is searched for in the directories of the
  **parent environment's** `PATH` (not the child's, even when `env_set`
  changes `PATH`), split at `:`, in order. An empty or relative element is
  skipped. When `PATH` is unset, the list is `/usr/bin:/bin`. The first
  directory `d` where `d/program` names a regular file (symbolic links
  followed) that the process may execute is used.
- No match is errno ENOENT (`NotFound`). Both engines apply this rule
  themselves, so they find the same file.

**R3 — The child's environment.** It is computed when `spawn` runs:
1. Start from the parent environment's entries in their order, or from no
   entries when `env_clear` was called at any point. `env_clear` also
   discards every `env_set` and `env_remove` recorded before it.
2. Apply the recorded `env_set` and `env_remove` calls in order.
   `env_set(n, v)` replaces every entry named `n` by one entry `n=v` at
   the position of the first, or appends it when there was none.
   `env_remove(n)` deletes every entry named `n`.

A name is valid when it is non-empty and holds no `=` and no NUL; a value
is valid when it holds no NUL.

**R4 — Spawning.** `spawn` starts the child and returns a `Child`, or an
error and no child:
- **Validation**, before anything is started. The checks run in this
  order and the first failure is returned:
  1. `program` empty or holding a NUL: errno EINVAL;
  2. `program` of 4096 bytes or more: errno ENAMETOOLONG, as in `std/io`;
  3. an argument holding a NUL, the first in argument order: errno EINVAL;
  4. an invalid environment name or value (R3) among the edits retained
     by R3 step 1 (edits discarded by `env_clear` are not checked), in
     call order: errno EINVAL;
  5. a `dir` path holding a NUL: errno EINVAL;
  6. a `dir` path of 4096 bytes or more: errno ENAMETOOLONG.
- **Errors from the system** are reported as their errno: a program not
  found (R2), not executable (EACCES), not an executable format (ENOEXEC),
  an argument list too long (E2BIG), a `dir` that does not exist (ENOENT)
  or is not a directory (ENOTDIR). When several apply, the error is the
  first of: (s) setting up the streams, stdin then stdout then stderr
  (opening `/dev/null`, creating a pipe: the errno, typically EMFILE);
  (a) entering `dir` (ENOENT, ENOTDIR, EACCES); (b) finding the
  program (R2, ENOENT); (c) executing the found file (the errno the system
  reports). Both engines check in that order, so they report the same
  error. When `spawn` fails, every descriptor it created is closed and no
  child remains.
- **Streams.** `Inherit` connects the child's stream to the program's
  (glossary). When the program's descriptor 0, 1 or 2 is closed, an
  inherited stream is closed in the child too, and a pipe or `/dev/null`
  set up for another stream never takes its place in the child. (A
  stream closed when the program started reads as end of file and takes
  writes in the program itself, in both engines: Go's runtime reopens it
  on `/dev/null` in the interpreter, and the native runtime holds its
  slot with a close-on-exec `/dev/null` so that no later descriptor lands
  there; the native child still gets it closed.) `Null` connects it to `/dev/null`, opened for reading
  (stdin) or writing (stdout, stderr). `Piped` creates a pipe whose child
  end is the stream and whose other end the `Child` holds until taken.
- **Inherit in the interpreter.** When the program's standard output or
  error is a captured writer, a child's inherited stream reaches that
  writer: everything the child writes to it before it exits is in the
  writer when `wait` or `status` returns. When the writer itself fails
  (it never does for the fixture harness's or `kern test`'s buffers), the
  interpreter keeps reading the child's stream and discards the rest of
  it, so the child never blocks on it; no operation reports the failure,
  and the program's own later writes to that stream report it as they do
  today. The program's standard input is descriptor 0 in both engines
  (the interpreter reads `os.Stdin` directly), so an inherited stdin is
  descriptor 0 itself and nothing is forwarded.
- **Ordering.** Output the program wrote to its standard output or error
  before `spawn` precedes, in that stream, anything the child writes to an
  inherited stream: `spawn` flushes the runtime's buffered standard output
  in both engines, after validation and before starting the child. A
  failed flush does not stop or fail the spawn (the bytes are lost, as at
  `exit` today).
- **Descriptors.** The child inherits exactly descriptors 0, 1 and 2.
  This holds while other threads of the program open files and spawn
  children concurrently.
- **Signals.** The child starts with the default disposition for every
  signal, except those that were already ignored when the program
  started, which stay ignored. SIGSEGV and SIGBUS, which the runtime
  handles itself in both engines (the native fault handler, Go's
  runtime), are not kept ignored: a child gets their default, since an
  exec resets a handled signal and spawning cannot set one to ignored.
  SIGCHLD is not one of those exceptions either: a
  program started with SIGCHLD ignored (or with `SA_NOCLDWAIT`) would
  have its children reaped by the system, so `wait` could report no
  status and `kill` could reach a reused process ID. The program's
  SIGCHLD is therefore reset to its default when it starts (natively by
  the runtime; Go's runtime does it in the interpreter), and children get
  the default too. Natively its signal mask is empty. In the
  interpreter it is the mask `kern` was started with, because Go
  restores that mask in a child and offers no way to set another. The
  two agree whenever `kern` is started with no signal blocked, which is
  how the fixture harness and `kern test` run.

**R5 — A child's life.**
- `id()` is the child's process ID. It is not otherwise used by the
  library and may be printed by no fixture.
- `take_stdin`, `take_stdout`, `take_stderr` return the stream's pipe end
  the first time for a `Piped` stream, and errno EBADF afterwards and for
  a stream that is not `Piped`. (A `Result`, not an `Option`: a `Pipe`
  has a destructor, so an `Option[Pipe]` cannot be destructured
  (`OWN_DESTRUCTURE_DESTRUCTOR`), while `?` moves a `Pipe` out of a
  `Result`; amended in Phase 3.)
- `try_wait()` returns `Ok(Some(status))` when the child has ended (and
  reaps it), `Ok(None)` when it is still running, without blocking. A
  system call interrupted by a signal (EINTR) is retried by `try_wait`,
  `wait`, `terminate`, `kill` and every pipe operation, and never
  reported. Any other system error is returned as its errno; after a
  failed `try_wait` the `Child` is unchanged. Once
  it has returned a status, later `try_wait` calls and `wait` return that
  same status without asking the system again.
- `wait()` consumes the child. It first closes the stdin pipe end if the
  `Child` still holds it, then blocks until the child ends, reaps it and
  returns its status; it then closes the pipe ends still held. When the
  system refuses the wait (any errno other than EINTR), `wait` closes the
  pipe ends still held and returns the errno; the child is not reaped,
  and there is nothing left to retry with (the `Child` was consumed). A child
  writing more to a still-held stdout or stderr pipe than the pipe holds
  can therefore never end, and `wait` never returns; `output` (R7) is the
  way to capture both streams.
- `terminate()` sends SIGTERM, `kill()` sends SIGKILL. Once the child has
  been reaped (a `try_wait` status), they send nothing and return `Ok(())`,
  since the reaped process ID may already belong to another process.
  Before that the process still exists (an ended child is not reaped until
  `try_wait` or `wait`), so the signal cannot miss; a system error is
  returned as its errno.
- `ExitStatus`: `Exited(c)` when the child called `exit(c)` or returned
  from `main` (c is 0 to 255); `Signaled(s)` when signal `s` ended it.
  `success()` is true exactly for `Exited(0)`.
- The `Child` destructor closes the pipe ends it still holds. It neither
  waits for nor kills the child: a child never waited for is reaped by no
  one until the program exits. (Decision: blocking or killing in a
  destructor would hide a cost or lose work; this matches Rust's and Go's
  standard libraries.)

**R6 — Pipes and descriptors.**
- `Pipe.read(buf)` reads at most `len(buf)` bytes and returns how many;
  0 means the child closed its end (end of file) or `buf` is empty.
  `read_all` reads to end of file. `write_all` writes all of `data`,
  retrying short writes, as `std/io`'s `write_all` does.
- Reading from a stdin pipe end or writing to a stdout or stderr pipe end
  is errno EBADF. This check comes first, so it applies to an empty `buf`
  or `data` too. After it, `write_all` of empty `data` returns `Ok(())`
  without a system call, whether or not the child end is still open.
- Writing to a pipe whose child end is closed is errno EPIPE. It does not
  terminate the program, in either engine. Writes to the program's own
  standard streams behave as they do today (this spec does not change what
  happens when the program's standard output is a closed pipe).
- The `Pipe` destructor closes its descriptor. Closing errors are ignored.
- Every descriptor that `std/process` or `std/io` creates is close-on-exec,
  and no `std/process` spawn on another thread can inherit it at any
  moment, including between its creation and the flag being set where
  the platform cannot create it with the flag (pipes on macOS). For `std/io`'s `io_open` and `io_temp` this changes
  nothing a kern program can observe except that its children no longer
  inherit those files.
- `Pipe`, `Child` and `Command` are `Send` (spec types §3 derives this
  from their fields), so they may be moved to another thread.

**R7 — Running to completion.**
- `status()` is `spawn` then `wait`, with each `Piped` stream treated as
  `Null`. A spawn error is returned as is.
- `output(input)` ignores the command's three stream settings and pipes
  all three. It writes `input` to the child's stdin and closes it, and
  reads stdout and stderr to end of file, all concurrently, so that no
  input or output size can deadlock it. It then waits and returns the
  status and both outputs. An EPIPE while writing `input` (the child
  stopped reading) is not an error: the remaining input is dropped and
  `output` carries on. A failure does not stop the other operations: a
  failed write ends the writing and closes stdin at once, a failed read
  ends that stream's reading and closes its pipe end, and the other
  operations run on to their end; then `output` waits for the child.
  Each pipe end is closed only by the operation using it, so no
  operation fails because another one closed something. When any of the
  four (writing `input`, reading stdout, reading stderr, waiting)
  failed, `output` returns one error, the first failed one in that
  order, whatever order the failures happened in. A spawn error is
  returned as is. A child that ignores a closed stdin or stdout and
  never ends keeps `output` from returning, as R5 documents for `wait`.
- Both behave alike in all four configurations, including when called
  from several threads at once.

**R8 — Traps and exit.** `exit` and a trap run no destructors (ADR-006):
pipes are closed by the operating system when the program ends, and
children keep running. `std/process` adds no runtime cleanup at trap or
exit time. Under `kern test`, a test stopped by `--timeout` leaves its
children running; this is documented in the report, not handled.

**R9 — The environment.** `env(name)` returns the value of the first
entry named `name` in the parent environment, or `None` when there is none
or the name is invalid (R3). In the interpreter the parent environment is
the environment `kern` was started with, which is what the native program
inherits under `kern run`.

## 4. Tests

All fixtures run in the four configurations through the fixture harness
and give identical output. The harness runs them with no signal ignored
(Go's test runner starts with default dispositions), so SIGTERM ends a
child. Children are `/bin/sh -c '...'` and standard
POSIX utilities (`cat`, `printf`, `head`, `kill`, `pwd`, `sleep`, `env`, `true`),
which exist on the macOS and Linux CI hosts. No fixture prints a process
ID, a descriptor number or an `Other` errno value (they differ by host);
fixtures print `Other` as `other`.

1. **`std_process_run`**: `status` of `true` (`Exited(0)`, success) and of
   `sh -c 'exit 3'` (`Exited(3)`, not success); `sh -c 'kill -9 $$'`
   (`Signaled(9)`); a program not on `PATH` (`NotFound`); `program`
   `/nonexistent/x` (`NotFound`); a `dir` that does not exist; a NUL in an
   argument, an empty program and an env name holding `=` (each `other`);
   an inherited `printf` whose output appears between two lines the
   program prints before `spawn` and after `wait`.
2. **`std_process_pipes`**: `cat` with stdin and stdout piped, written and
   read back; `take_stdout` twice (EBADF the second time); `read` on a
   stdin pipe end (`other`); a write to the child's
   stdin returning EPIPE without ending the program (`other`), where the
   child is `sh -c 'exec 0<&-; printf r; sleep 1'` with stdout piped and
   the program reads the `r` before writing, so the child's stdin is
   known to be closed; `try_wait`
   on `cat` (`None`), then closing its stdin and `wait` (`Exited(0)`);
   `kill` on a `cat` then `wait` (`Signaled(9)`), and `terminate`
   (`Signaled(15)`); `kill` after `try_wait` returned a status (`Ok`).
3. **`std_process_output`**: `output` of
   `sh -c 'head -c 1048576 /dev/zero >&2; cat'` given 1 MiB of input, so
   that the child fills stderr before it reads any input and an `output`
   that wrote all input before reading would deadlock; it prints both byte
   counts and a checksum of stdout (equal to the input's). `output` of
   `true` given 1 MiB of input (EPIPE ignored, status `Exited(0)`).
4. **`std_process_env`**: `env("PATH")` is `Some` (only that is printed;
   the fixture harness always runs with `PATH` set, as it finds `clang`
   through it),
   `env` of a name no host sets (`KERN_STD_PROCESS_UNSET`) and of `A=B` are
   `None`;
   `env_set`, `env_remove` and `env_clear` checked with
   `sh -c 'printf "%s" "$X"'`, and `env_clear` then two `env_set` calls
   checked with `/usr/bin/env` (exactly those two entries, in order); `env_set` of `PATH` to an empty directory
   not affecting the search (R2); `dir` then `pwd`, and a relative
   `program` (`./tool` created in a temporary directory) resolved against
   `dir`.
5. **`std_process_threads`**: four scoped threads, each spawning 25
   children with piped output while the program holds an open file and
   another child's pipes; every status and output correct. Each child is
   `ls /dev/fd`, whose listing must equal that of a first `ls /dev/fd`
   child started before the program opened anything else (what `ls`
   itself opens differs: one descriptor on Linux, two on macOS), so a
   leak of any descriptor number is caught.
6. **`TestStdModulesCheck`** also covers `std/process`.

## 5. Documents

- README: the library row and the std module list name `std/process`.
- `spec/types.md` §1: the std module list gains `"std/process"` and the
  example name `process.Command`.
- ADR-013: a dated consequence note that process control now exists, so
  every library prerequisite of self-hosting is met.
- `docs/reports/std-process.md`: what was added, what was found, how it
  was verified, the review findings, and what is not done (at least R8's
  timeout note and anything only verified on one OS).
