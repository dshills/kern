# `kern test --timeout` stops a test's child processes

**Version:** 0.1 (2026-11-01). **Scope:** the test runner
(`compiler/testrun/run.go`) and the interpreter's child-process table
(`compiler/interp/process.go`, `interp.go`); tests. **Language impact:**
none.
**Related:** `specs/test-command/SPEC.md` (the timeout: default 10 s,
`--timeout=<seconds>`, outcome `timeout`); `docs/reports/std-process.md`
("`kern test --timeout` stops a test without killing its children");
`specs/std-process/SPEC.md` (children are real processes in both
engines).

**Review status.** speccritic (gpt-6.1-sol): no critical finding; folded
in: signal-failure handling, the stop/kill ordering with a registration
barrier, reaping (a killed child is otherwise a zombie that `kill(pid,
0)` still finds). plancritic: no critical finding, the same points.

**Glossary.** R*n*: requirement *n* of §4. Run: one test in one
configuration (engine and opt level). Child: a process a run started
through `std/process`. Descendant: a child, or a process a descendant
started. Process group: the POSIX group a signal sent to `-pgid`
reaches. Reaped: a child whose exit status the run collected with
`wait` or `try_wait`. Abbreviations: pid, process identifier;
SIGKILL, the uncatchable kill signal.

## 0. Goals

When a run times out, nothing it started keeps running: today the
runner abandons the run (the native test process is killed, the
interpreter is stopped) but the run's children continue, holding files,
ports or CPU after `kern test` has reported. Non-goals: cleaning up
children of a run that ends normally (a test that leaves a child running
is the test's choice), and processes that leave the run's process group
on their own (`setsid`).

## 1. Background

`runNative` runs the compiled test binary with `exec.CommandContext` and
a timeout; on expiry Go kills that one process. Its children, started
with `posix_spawn`, are in the same process group as `kern` and survive.
`runInterp` runs the test in-process on a goroutine; on expiry it stops
the interpreter (`Interp.Stop`) and returns, and the children it started
with `syscall.ForkExec` survive. The interpreter keeps a table of started
children by handle, without recording which it has reaped.

## 2. Design

- **Native.** The test binary starts in a new process group of its own
  (its pid is the group id), which its children and their descendants
  inherit. On timeout the runner sends SIGKILL to the whole group
  instead of the one process.
- **Interpreter.** Each child-table entry records whether the child has
  been reaped (set where `wait`/`try_wait` collects its status). On
  timeout the runner calls a new `Interp.KillChildren`, which sends
  SIGKILL to every child the run started and has not reaped, under the
  table's lock (a reaped pid may be reused by an unrelated process, so it
  is never signalled). The interpreter's children are not moved to their
  own groups, so their own descendants are not reached: a documented
  difference from the native engine.
- **Ordering.** The runner stops the interpreter, then calls
  `KillChildren`. `KillChildren` first marks the table killed (under its
  lock); a spawn that registers a child after that (one already inside
  `ForkExec` when the timeout came) sends that child SIGKILL itself, so no
  child started by the run escapes.
- **Reaping.** A killed child of the interpreter would stay a zombie until
  its parent, the `kern` process, collects it, so `KillChildren` collects
  the status of each child it signals (in the background; if the
  abandoned run's own `wait` collects it first, the second collection
  fails harmlessly). A native test's children are reparented to the
  system's init when their parent dies and collected there.
- **Signal failures.** Sending SIGKILL fails with `ESRCH` when the target
  is already gone, which is the goal; any other failure (none is expected
  for the runner's own children) is ignored, every other child is still
  signalled, and the run is still reported as `timeout`.
- Normal runs are unchanged: no signal is sent unless the run timed out.

## 3. Out of scope

Children of normally ending runs; descendants that change process group;
the interpreter's grandchildren; Windows.

## 4. Requirements

**R1. Native timeout kills the group.** *Verified by:* a
`compiler/testrun` test (skipped without clang) running, under a 1 s
timeout in `native/debug`, a test that starts `/bin/sleep 60`, writes its
pid to standard output (unbuffered, `io.write_stdout`) and loops forever:
the run's outcome is `timeout`, and within 2 s of `Execute` returning
the child no longer exists (`kill(pid, 0)` fails with `ESRCH`).

**R2. Interpreter timeout kills the run's children.** *Verified by:* the
same test in `interp/debug`.

**R3. Reaped children are not signalled.** *Verified by:* an
interpreter unit test: a child that a run waited for is marked reaped,
and `KillChildren` skips it.

**R4. Normal runs unchanged.** *Verified by:* `go test ./...` (the
existing `kern test` and fixture tests).

**R5. Documents.** `docs/reports/std-process.md`'s "Not done" item points
here; `specs/test-command/SPEC.md`'s timeout paragraph gains a sentence
on children; `docs/reports/test-timeout-children.md` records the design,
the engine difference and the review.

## 5. Acceptance criteria

`golangci-lint run ./...` and `KERN_REQUIRE_QEMU=1 go test ./...` clean.

## 6. Risks

- **Interrupting `kern test`.** A test binary in its own group no longer
  receives the terminal's SIGINT; if `kern test` itself is killed while a
  native run is in flight, that run finishes on its own. The report
  records this; the runner already bounds every run by its timeout.
- **pid reuse.** A child reaped between the lock and the signal is the
  only window; the table marks reaping under the same lock.
