# `kern test --timeout` stops a test's child processes

**Date:** 2026-11-01. **Spec:** `specs/test-timeout-children/SPEC.md`
v0.1. **Plan:** `specs/test-timeout-children/PLAN.md`. **Item:** the
seventh of the ten-item list (`docs/reports/std-process.md`: "`kern test
--timeout` stops a test without killing its children").

## Design

- **Native.** The test binary starts in a process group of its own
  (`Setpgid`), which the children it starts through `posix_spawn`
  inherit; `exec.Cmd.Cancel`, which Go calls at the timeout, sends
  SIGKILL to the whole group (`kill(-pid)`). Orphaned children are
  collected by the system's init.
- **Interpreter.** Tests run in-process, so the interpreter's child table
  now records which children it has reaped (set in `procWait`, the one
  path behind `wait` and `try_wait`, under the table's lock) and gains
  `KillChildren`: under the lock it marks the table killed and sends
  SIGKILL to every unreaped child, then collects each status on a
  goroutine (a killed child is otherwise a zombie that `kill(pid, 0)`
  still finds). A spawn that registers after the table is marked killed
  kills its own child, so a child started while the timeout fired does not
  escape. The runner calls it after stopping the interpreter.
- **The engine difference.** Interpreter children stay in `kern`'s
  process group (moving them would change what the terminal's signals
  reach), so their own descendants are not killed; a native test's are.

## Verification

- `TestTimeoutKillsChildren` (`compiler/testrun`): a test that starts
  `/bin/sleep 60`, writes its pid to a file and loops, under a 1 s
  timeout, in `interp/debug` and `native/debug`: the outcome is `timeout`
  and the child is gone within 2 s. With either kill removed, the test
  fails for that engine ("child ... still runs after the timeout").
- `TestKillChildrenSkipsReaped` (`compiler/interp`): an entry marked
  reaped carries the test process's own pid, so signalling it would end
  the test; the unreaped `sleep` is killed.
- Found while testing: a native run's standard output goes through the
  runtime's buffer, which is flushed at exit, so a timed-out native run
  reports none of it, while a timed-out interpreter run reports what it
  wrote. The test writes the pid to a file instead. Reporting a killed
  native run's buffered output is an open item.

## Risks and open items

- A native test in its own process group no longer receives the
  terminal's SIGINT; if `kern test` itself is killed while a native run is
  in flight, that run continues until its own end (each run is still
  bounded by the timeout only while `kern test` lives).
- The interpreter's grandchildren; descendants that leave the group.
- A killed native run's buffered standard output (above).

## Review

- **speccritic** (gpt-6.1-sol): no critical finding; folded in:
  signal-failure handling, the stop/kill ordering with a registration
  barrier, reaping killed children.
- **plancritic** (davin-go): no critical finding; the same points, plus
  the one wait path and the lock not held across `Wait4`.
- **prism** (default settings, medium effort): no findings.
- **realitycheck** PARTIALLY_ALIGNED, score 100, no warning;
  **verifier** offline: 19 low findings; the one on this change asks for
  a `KillChildren` test, which `TestKillChildrenSkipsReaped` is (its match
  is by name). clarion skipped. `golangci-lint` and
  `KERN_REQUIRE_QEMU=1 go test ./...` clean.
