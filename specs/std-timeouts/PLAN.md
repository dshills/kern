# Plan: timeouts for child processes and standard input

Implements `specs/std-timeouts/SPEC.md` v0.4 in two phases, one commit:
phase 1 alone would leave the generated hosted-only inventory stale
(`TestHostedOnlyInventory` fails on the new library functions).

## Phase 1: the runtime entry, the library, the tests

**Goals.** R1 to R5a and R7.

**Files and decisions.**
- `runtime/hosted/src/kern_rt.c`: `kern_io_read_timeout(fd, buf, n, ns)`
  after `kern_io_read`.
  - An empty `buf` is 0 before anything else; a negative `fd` is
    `-EBADF`.
  - Loop: the time left is `ns − (now − start)` in unsigned arithmetic,
    0 once spent ≥ `ns` (no overflow for any `ns`); `poll` with it
    rounded up to milliseconds and clamped to `INT_MAX`; `EINTR` loops;
    `poll` returning 0 is timed out only once spent ≥ `ns` (a clamped
    piece loops); `POLLNVAL` is `-EBADF`; any readiness reads.
  - So readiness found by any `poll` before the deadline reads, and a
    zero `ns` polls once (spec §2, "When data and the deadline meet").
  - One `read`, `EINTR` retried. Timed out is `KERN_TIMED_OUT`, −2^32.
- `compiler/backend/llvm/calls.go`: `io_read_timeout` calls
  `kern_io_read_timeout` as `i64 (i32, ptr, i64, i64)`.
- `compiler/interp/stdin.go` (new), `intrinsics.go`, `interp.go`:
  - `io_read_timeout`: an empty buffer is 0 first; the `u64`
    nanoseconds are clamped to Go's largest `time.Duration` (292 years,
    no timeout in practice, R5a).
  - Descriptor 0 goes through the stdin pump.
  - Another descriptor gets one non-blocking read attempt through
    `SyscallConn` (ready data wins even over a zero timeout; Go fails a
    read past its deadline without trying it), then a read deadline for
    one read, cleared on every return by a `defer`. A descriptor that
    takes no deadline (a regular file, which never blocks) is read
    directly. A deadline error is `timedOut` (−2^32).
  - **The pump's contract.** One per process, as `os.Stdin` is (two
    interpreters in one process share it, so a reader left blocked by
    an earlier program cannot take a later one's bytes), created once
    (`sync.Once`) by the first timed read of descriptor 0. A read that
    leaves bytes or an error behind re-wakes the next waiting reader. One goroutine reads descriptor 0 for the rest
    of the process; its bytes are appended to a buffer guarded by the
    pump's mutex. It reads ahead at most 64 KiB, each underlying read
    sized to the room left (at most 4 KiB): when full it waits on a
    condition variable that every read signals, so a writer that never
    stops cannot grow it without bound. A read takes at most
    its buffer's length from the front, in order. End of file is
    delivered as 0 to every later read; a read error is `-errno` after
    the pending bytes. Once a pump exists, `io_read` on descriptor 0
    takes from it instead of reading the descriptor.
- `compiler/resolve/prelude/io.kern` and `std/io.kern` (identical):
  `import "std/time"`; the `io_read_timeout` declaration; `timed_out()`;
  `read_stdin_timeout`.
- `compiler/resolve/prelude/process.kern` and `std/process.kern`
  (identical): `import "std/time"`; the three methods and the helpers
  `deadline_of` (saturating at the largest `u64`), `Timed`,
  `gather_timed` and `stop`.
  - `wait_timeout` closes the held stdin, then loops: `try_wait` (an
    error returns); `Some` returns after flushing inherited streams;
    otherwise `None` once `now_ns()` reaches the deadline, else sleep
    `min(pause, time left)`, `pause` starting at 1 ms and doubling to at
    most 10 ms. A zero duration checks once.
  - `Pipe.read_timeout`: the kind check (EBADF on a stdin pipe) first,
    then the fault slot (returned once and emptied), then the entry.
  - `output_timeout` takes its deadline (`deadline_of(d)`) first, at
    the call, as the spec's glossary defines it; spawning and starting
    the threads count against it, and `gather_timed` waits with the time
    left (zero once spent, which checks once).
  - `gather_timed` spawns both readers and the writer on scoped threads;
    a thread that cannot start stops the child and returns
    `ResourceExhausted`. Then the calling thread calls `wait_timeout`.
    When that is `None` or `Err`, `stop` runs, and the timed wait's
    error is kept over `stop`'s. The scope then joins every thread (the
    child is dead or ended, so its pipe ends are closed); after a
    deadline the threads' results are discarded with their output.
  - `stop` sends `SIGKILL`, then reaps with `wait_timeout` of the
    largest duration whether or not the kill succeeded (after a failed
    kill it waits for the child to end), and returns the kill's error,
    else the reap's.

**Tests.**
- `tests/run/std_timeouts_pipe.kern` (R1, R2, R5a, under `test-hooks`
  for the faults), `std_timeouts_wait.kern` (R3, R5a) and
  `std_timeouts_output.kern` (R4), each with `.out`. Besides the spec's
  cases, `std_timeouts_pipe` covers:
  - the raw entry's empty-buffer result on a closed descriptor (0) and
    EBADF with a buffer;
  - a fault returned once and the next timed read normal;
  - the stdin-pipe EBADF before the stdin fault slot is taken (a
    following write takes it);
  - ready data returned by a zero timeout;
  - ordinary reads after timed ones on the same pipe (the deadline was
    cleared).

  No case depends on data arriving at the deadline: every child sleeps
  at least 0.8 s past a 200 ms timeout, or its data is ready long before
  the read.
- `compiler/interp/stdin_test.go`: `TestStdinPump`, the pump's contract
  on an `io.Pipe` (a timed read with nothing pending, order and
  splitting, untimed reads, zero timeout, end of file repeated, an error
  after pending bytes), and `TestStdinPumpBounded` (1 MiB written while
  nothing reads: at most 64 KiB is held, then all of it arrives in
  order), run under `-race`.
- The failure paths of `output_timeout` after a deadline (a failing
  timed wait, kill or reap) cannot be produced with a real child the
  program started, and `std/process` has no fault hook for waits or
  signals; they are checked by review against spec §2, not by a test
  (a seam for them would be a second fault hook in the library, out of
  proportion to three lines of precedence logic).
  The success path's error rules are `output`'s (`first_err`), covered
  by `std_timeouts_output`'s cases and the existing `output` fixtures.
- `cmd/kern/timeouts_test.go`: `TestReadStdinTimeout` (R5) and
  `TestOutputTimeoutReaps` (R4's reaping), each in both engines with a
  pipe the test holds.
  - Synchronization is by a marker file the program writes, polled for
    at most 20 s.
  - A 60 s watchdog kills the program, and a cleanup registered right
    after `Start` kills it and closes its input on every path, so no
    program outlives its test; files are in `t.TempDir()`.
- `compiler/analysis/hosted_test.go`: `TestHostedOnlyTimeouts`, an
  `ENV_HOSTED_ONLY` at a call to each of the four functions at both
  levels (R6).

**Key decisions.** A fixed sentinel for timed out instead of the host's
`ETIMEDOUT` (60 on macOS, 110 on Linux). `wait_timeout` polls
`try_wait` instead of adding a runtime wait: the same code in both
engines, no signal handling, at most 10 ms late.

**Acceptance.**
- The three fixtures pass in the harness's configurations (the
  interpreter, and native debug and release; debug only for the
  `test-hooks` fixture, as spec §3 states). The existing `std_process_*`
  and std/io fixtures (`tests/run/std_io.kern`) and their Go
  tests pass unchanged (R7).
- The new Go tests pass; `go test -race ./compiler/interp ./cmd/kern`
  passes.
- `diff std/io.kern compiler/resolve/prelude/io.kern` and the same for
  `process.kern` print nothing.
- `go test ./...`, `golangci-lint run ./...` and `gofmt -l` are clean.

**Risks.** Timing on a loaded host: fixtures bound only the upper limit
(3 s for a 200 ms timeout). The pump changes how the interpreter reads
descriptor 0 after a timed read; `io_read` on descriptor 0 without a pump
is unchanged.

## Phase 2: documents and the inventory

**Goals.** R6 and R8.

**Files.**
- `docs/reports/freestanding-availability.md`: regenerate the inventory
  (`go test ./compiler/analysis -run TestHostedOnlyInventory
  -update-hosted-inventory`); each new function appears with
  `kern_io_read_timeout` or `kern_now_ns`.
- `docs/reports/std-timeouts.md` (new): the API; the fixtures and tests;
  measured return times of each timed-out call in each configuration
  (200 ms timeouts, three runs); and the limits the spec records: the
  interpreter's stdin reader and inherited stdin, a competing reader of
  the same descriptor voiding the bound, and a descendant holding the
  child's stdin, stdout or stderr open keeping `output_timeout` past its
  deadline.

**Acceptance.** `TestHostedOnlyInventory` passes without the update
flag; `go test ./tests` (with the documentation lint) passes.

**Risks.** None beyond stale prose.
