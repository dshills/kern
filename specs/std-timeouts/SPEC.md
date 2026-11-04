# Standard library: timeouts for child processes and standard input

**Version:** 0.4 (2026-11-04). **Review status.** speccritic
(gpt-6.1-sol, with `specs/std-process/SPEC.md` as context) round 1: two
critical findings, both resolved: a read after `poll` can block when
another reader drains the descriptor (the bound now holds for a sole
reader, §2), and a failing kill or reap after a deadline now has a
defined result (R4). Also folded in: the interpreter's stdin reader and
inherited stdin (§4), the fault hook's contract, large durations, an
observable reaping check, and §2 is informative. Round 2: a failing timed wait in
`output_timeout`, the fault hook's contract, the empty-buffer
precedence, descendants holding stdin; R7 is behavioural; the timeout
result is a fixed sentinel, not the host's `ETIMEDOUT`. Round 3
(VALID_WITH_GAPS, no critical): §2's contracts are normative, R7 is
qualified by §4's stdin difference, and the deadline boundary is
stated. **Scope:**
- `std/process` (`compiler/resolve/prelude/process.kern`, mirrored
  identically in `std/process.kern`): `Child.wait_timeout`,
  `Pipe.read_timeout` and `Command.output_timeout`;
- `std/io` (`compiler/resolve/prelude/io.kern`, mirrored in
  `std/io.kern`): `read_stdin_timeout`;
- one new runtime entry, `io_read_timeout`, in the hosted runtime
  (`runtime/hosted/src/kern_rt.c`), the interpreter
  (`compiler/interp`) and the LLVM backend's declarations;
- fixtures under `tests/run/`, the generated hosted-only inventory in
  `docs/reports/freestanding-availability.md`, and the report
  `docs/reports/std-timeouts.md`.

**Language impact:** none: library additions and one runtime entry.
**Follows:** `specs/std-process/SPEC.md` (its R-numbers are cited as
"process R*n*"), `specs/std-time/SPEC.md` (`Duration`).

**Glossary.**
- R*n*: requirement *n* of §3. *Engine*: the interpreter or the native
  build; *configuration*: an engine at an opt level (four in all).
- *d*: the `time.Duration` argument; the *deadline* is the monotonic
  clock's reading (`now_ns`) at the call plus *d*.
- *Ready*: a descriptor is ready when a read would not block: data is
  available, the writing end is closed (end of file), or the read would
  fail at once.
- errno names (`EBADF`, `EINTR`) are the host's. *Timed out*: the
  entry's result −2^32 (`-4294967296`), outside every errno value, so
  the library needs no host-specific timeout number.
- POSIX `poll`: the host system call that waits for a descriptor to be
  ready, with a timeout in milliseconds.

## 0. Goal and non-goals

A program must be able to bound how long it waits for a child process
or for input: wait for a child, read from a child's pipe, run a command
to completion, or read standard input, each with a duration after which
the call returns instead of blocking. A call that times out reports it
as a value (`None`), not as an error, and leaves the program able to
continue (kill the child, try again, give up).

Non-goals: timeouts on writes (`Pipe.write_all`), on file reads (a
regular file never blocks), cancellation from another thread, and a
`TimedOut` variant of `io.IoErr` (adding a variant would break every
exhaustive `match` on `IoErr` in existing programs).

## 1. Background

`Child.wait`, `Pipe.read`, `Pipe.read_all`, `Command.output` and
`io.read_stdin` block without bound. `Child.try_wait` exists (process
R5) and does not block. `std/time` has `Duration`, `Deadline` and
`sleep`. In the interpreter, child pipes are Go `*os.File` pipes, which
support read deadlines; standard input is the process's own descriptor 0.

## 2. Design

The signatures and every observable contract below (results, the order
of checks, error precedence, and what a call has done to the child and
its pipes when it returns) are normative. The mechanisms (`poll`, the
interpreter's read deadline and stdin reader, the polling interval, the
scoped threads) are the intended implementation and informative; §3
states how each contract is checked.

```kern
// std/process
impl Child {
    func wait_timeout(edit self, d: time.Duration) -> Result[Option[ExitStatus], io.IoErr]
}
impl Pipe {
    func read_timeout(edit self, buf: edit[]u8, d: time.Duration) -> Result[Option[usize], io.IoErr]
}
impl Command {
    func output_timeout(view self, input: view[]u8, d: time.Duration) -> Result[Option[Output], io.IoErr]
}
// std/io
func read_stdin_timeout(buf: edit[]u8, d: time.Duration) -> Result[Option[usize], IoErr]
// runtime entry
func io_read_timeout(fd: i32, buf: edit[]u8, ns: u64) -> i64
```

**`io_read_timeout(fd, buf, ns)`** waits until `fd` is ready or `ns`
nanoseconds pass, then reads once: the bytes read (at most `len(buf)`),
0 at end of file or for an empty `buf` (checked first: an empty `buf`
is 0 even on a closed descriptor), timed out (−2^32) when `fd` was not
ready in time (nothing is read), or `-errno`. Natively it calls `poll`
with the remaining time rounded up to whole milliseconds (a wait longer
than `INT_MAX` ms is taken in `INT_MAX` ms pieces), retrying `EINTR` with
the time left, then `read` (retrying `EINTR`). Readiness is not a
reservation: when another reader of the same descriptor (another thread
of the program reading standard input, or another process sharing it)
drains it between `poll` and `read`, the read blocks until data or end
of file. The time bound therefore holds when the program is the
descriptor's only reader, which a `Pipe` always is (it is `nocopy` and
owned by one thread at a time). In the
interpreter, a descriptor that supports deadlines (a pipe) gets a read
deadline of `ns` for one `Read`, and a deadline error is timed out;
standard input is read through a single reader goroutine that the first
timed read starts, which every later read of descriptor 0 (timed or not)
then takes its bytes from, so a timed-out read never loses input (it
reads ahead at most 64 KiB, then waits for the program to read); any
other descriptor (a regular file, which never blocks) is read directly.

**When data and the deadline meet.** A timed read returns data when the
descriptor was found ready at any check before the deadline passed; it
returns timed out only when no check before the deadline found it
ready. Data that arrives after the deadline passed may be returned or
not (both conform), so a test never depends on that boundary.

**`read_stdin_timeout(buf, d)`** is `io_read_timeout(0, buf, d)`:
`Some(n)` for a read of *n* bytes (0 at end of file), `None` on
timeout, `Err` otherwise.

**`Pipe.read_timeout(buf, d)`** is the same on the pipe's descriptor,
with `Pipe.read`'s checks in `Pipe.read`'s order: a stdin pipe (kind 0)
is `Err(Other(EBADF))` before anything else; then a fault armed with
`process.fail_next_io(kind, code)` for this pipe's kind (the hook
`std/process` already has for its fixtures) is consumed and returned as
`Err(io.err_of(code))`; then the timed read. The hook's contract, from
`std/process`: `fail_next_io(kind: u8, code: i32)`, available only under
`--test-hooks`, arms one slot per pipe kind (0 a write to a child's
stdin, 1 a read of its stdout, 2 of its stderr), replacing what that slot
held; the next pipe operation of that kind on any pipe (`read`,
`read_all`, `write_all`, and now `read_timeout`) takes the slot, empties
it, and returns `Err(io.err_of(code))` instead of making its call.

**`Child.wait_timeout(d)`** closes the stdin pipe the child still holds
(as `wait` does, process R5, so a child reading to end of file can
end), then returns `Some(status)` once the child has ended (reaping it,
as `try_wait` does) or `None` when the deadline passes first; the child
keeps running after `None`. It checks with `try_wait` and sleeps
between checks: 1 ms, doubling up to 10 ms, and never past the deadline;
so it returns within 10 ms (plus scheduling) of the child's end or of
the deadline. A `d` of zero checks once. The deadline is `now_ns() + d`,
saturating at the largest `u64` (a duration that large never expires).

**`Command.output_timeout(input, d)`** is `output` (process R7) with a
deadline, taken at the call: spawning the child and starting the
threads count against `d`. It spawns the child with all three streams piped, writes
`input` and reads stdout and stderr on scoped threads, and waits for
the child on the calling thread with `wait_timeout` until the deadline.
If the child ends in time, the threads are joined and the result is
`Ok(Some(output))`, with `output`'s error rules. If the deadline passes,
the child is killed (`SIGKILL`) and reaped, the threads are joined (the
child's pipe ends are closed by its death, so the readers see end of
file and the writer `EPIPE`), and the result is `Ok(None)`; what the
child wrote is discarded. If the kill fails (it cannot for a child the
program started and has not reaped, but the call reports it), the call
waits for the child to end as `output` does, joins the threads, and
returns `Err` of the kill's error; the time bound does not hold then.
If the reap after a successful kill fails, the child is dead, so its
pipes are closed: the threads are joined and the result is `Err` of the
wait's error. A failure of the threads' own operations after a deadline
is discarded with their output. A descendant that keeps any of the child's pipe
ends open (stdout or stderr, which keep a reader waiting, or stdin
without reading it, which keeps the writer blocked once the pipe is
full) holds the threads past the deadline: documented, not handled.
If the timed wait itself fails (`try_wait`'s error), the call treats it
as a deadline: it kills and reaps the child, joins the threads, and
returns `Err` of the wait's error; a failing kill or reap then adds
nothing (the first error is reported).

## 3. Requirements and acceptance criteria

Each requirement is an acceptance criterion, checked by the fixture
named, unless it says otherwise, in the configurations the fixture
harness runs: the interpreter, and natively at debug and release (a
`test-hooks` fixture natively at debug only, since release builds have no
test hooks). Timing
tests use a child that sleeps (`sleep 5`) against a timeout of 200 ms,
and check only bounds that hold on a loaded host: a call that must time
out returns `None` in under 3 s; a call that must not, returns its
result.

**R1. `io_read_timeout`.** Ready data is returned at once; with no
data and the writer still open, timed out (−2^32) after about `ns`; end of
file is 0; an empty `buf` is 0 without waiting; a closed descriptor is
`-EBADF`. A timed-out read consumes nothing: the next read returns the
bytes that arrive later. Checked by `tests/run/std_timeouts_pipe.kern`.

**R2. `Pipe.read_timeout`.** A child that prints after sleeping:
`read_timeout` with 200 ms returns `None`; a later `read_timeout` with
10 s returns `Some(n)` with the child's bytes; after the child exits,
`Some(0)`. On a stdin pipe it is `Err(Other(EBADF))`, and an armed
fault is returned. Fixture `std_timeouts_pipe.kern`.

**R3. `Child.wait_timeout`.** A child that sleeps 5 s: `None` within
3 s, after which `kill` and `wait` reap it with signal 9. A child that
exits 3: `Some` with code 3, also when called again after the reap.
`d` of zero on a running child is `None` at once. A child that reads
stdin to end of file ends within the timeout because its held stdin is
closed. Fixture `tests/run/std_timeouts_wait.kern`.

**R4. `Command.output_timeout`.** `sh -c "printf hi; printf err >&2;
exit 4"` with 10 s returns `Some` with stdout `hi`, stderr `err` and
code 4; `cat` with 1 MiB of input returns it all; `sleep 5` with
200 ms returns `None` within 3 s, and the child is not left running
(checked by `TestOutputTimeoutReaps` in `cmd/kern` in both engines: a
program calls it, prints a line and then blocks reading standard input;
when the line arrives the test lists the program's child processes with
`pgrep -P` and requires none, neither running nor a zombie, then closes
the program's input). A spawn failure is `Err` as in `output`.
Fixture `tests/run/std_timeouts_output.kern`.

**R5. `read_stdin_timeout`.** With standard input a pipe that stays
open and empty, `None` after the timeout; then, once the test writes
`abc` and closes it, the next timed read returns `Some(3)` and the one
after `Some(0)`; a plain `io.read_stdin` after a timed-out read returns
all later bytes, none lost. Checked natively and in the interpreter by
`TestReadStdinTimeout` in `cmd/kern`, which runs a compiled program and
`kern run` with a pipe it controls (fixtures cannot hold stdin open).

**R5a. Large durations.** A duration of `u64` maximum nanoseconds
behaves as no timeout for all four functions (no overflow, no immediate
`None`): checked for `read_timeout` on a pipe that receives data after
100 ms and for `wait_timeout` on a child that exits after 100 ms, in the
fixtures above.

**R6. Freestanding.** All four functions reach `kern_io_read_timeout` or
`kern_now_ns`, which the freestanding runtime does not define, so they
are rejected with `ENV_HOSTED_ONLY` there; the inventory is regenerated.

**R7. No change to existing functions.** In a program that never calls
`read_stdin_timeout`, the observable behaviour of every existing
`std/io` and `std/process` function is unchanged (after a call to it,
the interpreter differs as §4 records): the
existing io and process fixtures and tests pass unchanged; the prelude copies stay identical to
the `std/` files (`TestStdMirror` or the existing mirror check).

**R8. Documents.** The report `docs/reports/std-timeouts.md` records
the API, the fixtures, measured return times of the timed-out calls in
each configuration, and what is not done.

## 4. Failure modes

- A host where `poll` on a pipe is unreliable (old macOS on some
  descriptors): pipes and terminals are supported on macOS and Linux;
  the fixtures use pipes only.
- A loaded host makes a timed-out call return late; the fixtures bound
  only the upper limit loosely (3 s for 200 ms) and never require a
  call to take at least its timeout, except that `None` must not be
  returned while ready data exists.
- A child that ignores `SIGKILL` cannot exist; a child stopped by a
  signal is killed too.
- **Interpreter standard input.** Once a timed read of standard input
  has started the interpreter's reader, the interpreter keeps reading
  descriptor 0 in the background: bytes it has read belong to the
  program's later reads, and a child started afterwards with inherited
  standard input competes with that reader for the rest. A native
  program has no such reader. Programs that pass input to a child should
  pipe it (`Stdio` piped); the report records this difference.
- A competing reader of the same descriptor voids the time bound (§2).
