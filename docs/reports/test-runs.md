# `kern test`: messages, setup and teardown, parallel runs, stopped locks

**Date:** 2026-10-31. **Spec:** `specs/test-runs/SPEC.md`. **Closes:**
three of the "Not done" items of `docs/reports/test-command.md`: parallel
runs, setup and teardown, and messages on `expect`; and the blocked
interpreter run.

## What changed

- **`expect_msg(cond, msg)`.** It is written in kern in the prelude. A
  failure writes `{"expect":{"message":...}}` to standard error, the
  message JSON-escaped by the kern code itself, then traps `explicit`.
  - It writes through the new `eprint_str`, an unbuffered standard-error
    write in both runtimes and the interpreter.
  - `kern test` reports the message as the run's `message`.
- **Setup and teardown.** A test file's `setup` and `teardown` run around
  each of its tests, in one program.
  - The lowering takes them as roots and lists them in
    `ir.Module.TestSetup`/`TestTeardown`.
  - The inliner keeps them as roots.
  - The native test entry calls them `noinline`; the interpreter runs the
    three through `RunEntries`, which shares the thread-locals.
  - A trap ends the sequence, so a trapping test has no teardown.
- **Parallel runs.** `--jobs=N` (default the CPU count). The native
  executables are built first, then a worker pool takes every
  (configuration, test) run. Results are filed back in configuration and
  test order, so the response matches a sequential one. The module's
  name index is built before the runs share it.
- **Stopped locks.** The interpreter's `Mutex` lock is a one-slot
  channel instead of a `sync.Mutex`. A waiter selects on it and on the
  program's termination channel, so `Stop` (or a trap on another thread)
  ends a thread blocked in `lock`, as it already ended one waiting for a
  join or a notification.

## Verification

- **`TestExpectMessage`.** A message with `"`, `\n`, `\t`, `\`, a NUL
  byte and `é` comes back verbatim in all four configurations. A passing
  `expect_msg` reports nothing.
- **`TestSetupTeardown`, interpreter and native.** The outputs are
  `setup test teardown` for a returning test, `setup test` for a trapping
  one, and only `plain` for another file's test. A trapping setup runs no
  test.
- **`TestSetupSignature`.** Both malformed hooks are `TEST_SIGNATURE`.
- **`TestParallelRuns`.** Twelve tests, four of them trapping, in all
  four configurations: `--jobs` 1 and 8 give deep-equal responses apart
  from `millis`. It passes under `go test -race`.
- **`TestInterpStopWakesLocks`.** Two threads deadlock on two mutexes;
  after `Stop` the run returns and the goroutine count returns to its
  starting value. With the old blocking lock the test fails.
- **Gates.** `golangci-lint run ./...` reports 0 issues. `go test ./...`
  passes except the ThreadSanitizer fixture `race_raw_ptr`, whose
  symbolizer hangs on this host (it fails the same way on 7e5f638).

## Reviews

- **prism**, two findings, both fixed:
  - A message longer than the 64 KiB stderr tail would have been lost.
    `expect_msg` now writes at most 4096 bytes of it, tested by
    `TestExpectMessageLong`.
  - A record following stderr output without a newline would not have
    been found. It is now found anywhere in a line.

## Still not done

- **Benchmarks** in `kern test`, and **freestanding** tests.
- **`args()[0]`** is the test's file in the interpreter and the
  executable's path natively. A test that prints it is still reported
  `mismatch`.
