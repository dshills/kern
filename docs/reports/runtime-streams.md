# Runtime, phase 2: process lifecycle and standard streams

**Date:** 2026-11-09. **Spec:** `specs/runtime/SPEC.md` v0.6, area B (RB1 to
RB5). **Plan:** `specs/runtime/PLAN.md`, phase 2. **Hosts:** macOS 27.2 on
arm64 and Ubuntu 24.04 on arm64 in the `kern-linux-check` container.

## What changed

- **Standard output has a policy (RB2).** It was flushed only when 64 KiB
  had accumulated or the program ended, so a prompt written before reading
  standard input was not seen. It is now also flushed:
  - after a write that holds a newline, when descriptor 1 is a terminal
    (one `isatty` at startup);
  - before the program reads standard input (`io.read_stdin`,
    `read_stdin_timeout`);
  - by `io.flush_stdout()`.
  The existing flushes stay: a full buffer, a child inheriting the stream,
  exit and a trap.
- **Writes are robust (RB3).** `kern_write_all` gave up on any write that
  did not make progress, an interrupted one included, and dropped the rest.
  It now retries `EINTR`, continues a short write, and waits with `poll`
  when the descriptor is non-blocking and full. Any other failure drops the
  bytes and is remembered: the first failed write to standard output is
  kept until `io.flush_stdout() -> Result[(), IoErr]` returns and clears
  it. `print_str` and its kin still never fail visibly.
- **`SIGPIPE` is left alone**, and that is now a stated rule: with the
  default disposition a write to a pipe nobody reads ends the process, as
  for any Unix program; with the signal ignored the write is the recorded
  failure.
- **`io.is_terminal(s: Stream) -> bool`** with `union Stream { Stdin;
  Stdout; Stderr }` (RB4).
- **Configuration (RB1).** `kern_start` reads `KERN_BACKTRACE`,
  `KERN_STACK_BYTES` and `KERN_ALLOC_REPORT` once. The later phases that
  use them find them parsed. A value outside the accepted form is ignored
  and named in one line, `{"runtime":{"ignored":"NAME"}}`; an unset or
  empty variable is silent; a set-user-ID or set-group-ID process
  (`AT_SECURE` on Linux, `issetugid()` on macOS) reads none of them.
- **Exit (RB5)** needed no code: it is now tested.

The interpreter does not buffer its output, so its bytes are already in
order; it mirrors the two new calls. `io.flush_stdout` reports the first
failed write of the interpreter's output writer, and `io.is_terminal` asks
the system about the writer's file (`isatty` by reading the terminal
settings, since `/dev/null` is a character device too).

## Requirement to test

| Clause | Test |
|---|---|
| RB1: accepted values of each variable; the boundaries of `KERN_STACK_BYTES` | `TestRuntimeConfigurationVariables` (21 values) |
| RB1: an unaccepted value is ignored with one line; several give one line each | same |
| RB1: unset and empty are silent | same |
| RB1: a privileged process ignores every variable | same, through the test hook `KERN_TEST_PRIVILEGED` (the real condition needs a set-user-ID binary) |
| RB2: flushed before a read of standard input | `TestStdoutFlushedBeforeStdinRead` |
| RB2: a line reaches a terminal at once | `TestStdoutLineBufferedOnTerminal/terminal` (a pseudo-terminal) |
| RB2: a pipe is buffered until a flush | `TestStdoutLineBufferedOnTerminal/pipe` |
| RB2: flushed at exit, by `exit` and by a trap | `TestExitBehaviour` |
| RB2: a program started with standard output closed | `TestStdoutClosedAtStart` |
| RB2: the interpreter's bytes and order | every output fixture, which the harness runs in both engines |
| RB3: a non-blocking descriptor is waited for | `TestStdoutNonBlockingPipe` (1,000,000 bytes, none lost) |
| RB3: no loss across stop and continue | `TestStdoutSurvivesStopAndContinue` |
| RB3: a failure is recorded, returned once, then cleared | `TestStdoutBrokenPipe/ignored` |
| RB3: `SIGPIPE` at its default ends the process | `TestStdoutBrokenPipe/default_disposition` |
| RB3: `flush_stdout` is `Ok` when nothing failed | `tests/run/std_io_streams.kern` |
| RB4: `is_terminal` | `tests/run/std_io_streams.kern` (false under the harness, both engines); `TestStdoutLineBufferedOnTerminal/terminal` exercises the terminal case through the buffering it selects |
| RB5: `exit(code)` and a return from `main`: low 8 bits, flushed output | `TestExitBehaviour` (0, 3, 7, 255, 256, 300, -1) |
| RB5: `exit` runs no destructor; a return does | same (`destroyed` printed or not) |
| RB5: a trap ends by `SIGABRT` after the flushed output | same |
| RB5: a running thread delays nothing | same (`thread`) |

## Deviations and open points

- **The recorded failure is standard output's.** RB3's text says "a write
  to a standard stream ... is recorded". Standard error is unbuffered and
  written by `eprint_str` and the trap handler, which have no way to report
  a failure and no later call to carry it; `Stderr.write` of RG4 (phase 7)
  will return the system's result directly. The retry rules apply to both
  streams.
- **An interrupted write (`EINTR`) is not provoked by a test.** With no
  handler installed, the systems restart the write themselves; the
  stop-and-continue test covers the nearest observable case. The retry is
  three lines in `kern_write_all`, exercised by phase 9's signal handlers.
- **`is_terminal(Stdin)`** has no fixture: the harness does not fix what
  standard input is.

## Verification

`go test ./...` passes on macOS; the phase's Go tests, its fixture and the
area-A checks pass in the Linux container; `golangci-lint run ./...`
reports no issue.
