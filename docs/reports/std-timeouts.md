# Timeouts for child processes and standard input

**Date:** 2026-11-04. **Spec:** `specs/std-timeouts/SPEC.md` v0.4
(speccritic: three rounds, no critical finding left), plan
`specs/std-timeouts/PLAN.md` (plancritic EXECUTABLE_WITH_CLARIFICATIONS,
no critical finding; its remaining warning, a test seam for the cleanup
failures, was declined as the plan explains).

## API

```kern
// std/process
func wait_timeout(edit self, d: time.Duration) -> Result[Option[ExitStatus], io.IoErr]               // Child
func read_timeout(edit self, buf: edit[]u8, d: time.Duration) -> Result[Option[usize], io.IoErr]     // Pipe
func output_timeout(view self, input: view[]u8, d: time.Duration) -> Result[Option[Output], io.IoErr] // Command
// std/io
func read_stdin_timeout(buf: edit[]u8, d: time.Duration) -> Result[Option[usize], IoErr]
```

A timeout is `None`, not an error, so no `match` on `io.IoErr` changes.
The functions behave as follows:

- `wait_timeout` leaves the child running when it times out.
- `output_timeout` kills and reaps the child, and discards what it
  wrote.
- The two reads consume nothing when they time out.

**Runtime entry.** The one new entry is `io_read_timeout(fd, buf, ns)`.

- Natively, `kern_io_read_timeout` calls `poll`, then one `read`.
- In the interpreter, a pipe first gets a non-blocking attempt and then
  a read deadline. Standard input goes through a reader goroutine with
  64 KiB of read-ahead.
- Timed out is −2^32 in both engines, not the host's `ETIMEDOUT` (60 on
  macOS, 110 on Linux).

**Library.** `wait_timeout` polls `try_wait`, sleeping 1 ms and doubling
up to 10 ms. `output_timeout` takes its deadline at the call and runs
the writer and both readers on scoped threads while the calling thread
waits.

## Tests

- **Fixtures** (`tests/run/`, in the interpreter and natively at debug
  and release; `std_timeouts_pipe` natively at debug only, as a
  `test-hooks` fixture):
  - `std_timeouts_pipe` covers timed-out, delayed, end-of-file and
    empty reads; a fault returned once; the stdin-pipe EBADF ahead of
    the fault slot; the raw entry's empty buffer on a closed
    descriptor; ready data at a zero timeout; and the largest duration.
  - `std_timeouts_wait` covers a timed-out wait then kill (signal 9),
    exit 3 twice, a zero duration, the held stdin closed for `cat`, and
    the largest duration.
  - `std_timeouts_output` covers `sleep 5` timing out, both streams
    with exit 4, 1 MiB through `cat`, and a missing program.
- **`cmd/kern`**, both engines, each with a pipe the test holds:
  - `TestReadStdinTimeout`: `None`, then `abc`, then a plain
    `read_stdin` that gets `def` (nothing lost), then end of file.
  - `TestOutputTimeoutReaps`: after the timeout, `ps` shows no child of
    the program, neither running nor a zombie.
- **`compiler/interp`**: `TestStdinPump` (the pump's contract) and
  `TestStdinPumpBounded` (1 MiB written while nothing reads; at most
  64 KiB is held), both under `-race`.
- **`compiler/analysis`**: `TestHostedOnlyTimeouts` checks that a
  freestanding call to each of the four functions is `ENV_HOSTED_ONLY`.
  The regenerated inventory lists the three process functions the
  corpus compiles. `read_stdin_timeout` is in no corpus program, so the
  inventory does not list it; that test covers it.

## Measured return times

Each timed-out call used a 200 ms timeout, with three runs per
configuration, on a loaded host (load average about 25):

| configuration | `read_timeout` | `wait_timeout` | `output_timeout` | `read_stdin_timeout` |
|---|---|---|---|---|
| interpreter, debug | 200.2–202.3 ms | 200.2–202.8 ms | 203.0–203.4 ms | 201.1–202.0 ms |
| interpreter, release | 202.1–202.2 ms | 202.0–202.1 ms | 203.0–212.0 ms | 202.1–202.2 ms |
| native, debug | 200.4–202.0 ms | 200.9–201.6 ms | 202.6–205.2 ms | 200.6–202.0 ms |
| native, release | 202.0 ms | 200.6–202.9 ms | 203.2–203.9 ms | 201.4–202.0 ms |

`output_timeout`'s deadline is taken at the call, so spawning the child
and starting three threads count against it. A first version took the
deadline after spawning and returned at about 230 ms in the interpreter.
What remains past 200 ms is the kill and the reap.

## Limits (spec §4)

- **The interpreter's standard input.** After a timed read of standard
  input, the interpreter keeps reading descriptor 0 in the background.
  A child started afterwards with inherited standard input competes with
  that reader for the remaining bytes. Natively there is no such reader.
- **A competing reader.** When another reader drains the same descriptor
  between `poll` and `read`, that read blocks. The bound holds for a
  sole reader, which a `Pipe` always is.
- **A descendant holding a pipe.** A descendant that keeps the child's
  stdout or stderr open, or holds its stdin without reading it, keeps
  `output_timeout` waiting past its deadline.
- **Untested failure paths.** The cleanup failures after a deadline (a
  failing timed wait, kill or reap) have no test. A real child cannot
  produce them, and the library has no fault hook for them.

## Not run

`scripts/linux-check.sh`, the Linux gate for `std/process`, needs Docker,
which was not running on the host. The native entry uses only POSIX
`poll` and `read`, but it has not been compiled or run on Linux.

## Not done

Timeouts on writes and on file reads, and cancellation from another
thread, are non-goals.
