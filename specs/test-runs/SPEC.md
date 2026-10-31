# `kern test`: messages, setup and teardown, parallel runs, stopped locks

**Version:** 0.1 (2026-10-31). **Scope:**
- `compiler/testrun` and `cmd/kern/test.go`;
- the core prelude (`expect_msg`, `eprint_str`) with its interpreter,
  backend and runtime support;
- the lowering and module roots of setup and teardown;
- the interpreter's `Mutex`;
- the documents `spec/types.md` §6, `README.md` and
  `specs/test-command/SPEC.md`.

**Language impact:** two prelude functions, and the names `setup` and
`teardown` in test files. **Follows:** `specs/test-command/SPEC.md`,
whose "out of scope" and risks this takes up.

## 1. Requirements

**R1 — Messages.**
- `expect_msg(cond: bool, msg: view[]u8)` returns when `cond` is true.
  Otherwise it writes `{"expect":{"message":"<msg>"}}` and a newline to
  standard error, then traps `explicit` as `expect` does: the record
  names the call.
- The message is a JSON string: `"` and `\` are escaped, and bytes below
  0x20 become `\u00XX`.
- Only the first 4096 bytes of the message are written. Escaped, they
  stay within the 64 KiB of standard error that `kern test` keeps of a
  run. A cut inside a UTF-8 sequence is reported with U+FFFD.
- `kern test` reports the last such line before a run's trap record as
  that run's `message`. The key is absent for any other run.
- `eprint_str(s: view[]u8)` writes bytes to standard error at once (the
  console when freestanding).

**R2 — Setup and teardown.**
- A test file's top-level functions named `setup` and `teardown` take a
  test function's signature; otherwise `TEST_SIGNATURE`.
- For each test of that file, a run executes `setup`, the test, then
  `teardown`, in one program sharing its thread-locals.
- A trap or an exit in any of them ends the run with that outcome, so a
  trapping test runs no teardown and a trapping setup runs no test. This
  follows ADR-006: a trap runs no cleanup.
- Tests of other files are unaffected.
- Natively the three are called `noinline` from the test entry, so each
  keeps its own location marker.

**R3 — Parallel runs.**
- `--jobs=<n>` (1 to 256; default the number of CPUs; anything else is
  `CLI_INVALID_FLAGS`) is how many runs proceed at once.
- Native executables are built before any run.
- The response is the same for every `n`, apart from the runs' `millis`:
  tests and runs keep their order.

**R4 — Stopped runs end.** When a timed-out interpreter run is stopped,
each of its threads ends as at a block boundary. This includes threads
blocked in `Mutex.lock` (also the relock after a `Condvar.wait`), not
only those at a block boundary or waiting for a join or a notification.

**R5 — Tests.**
- A message with quotes, a backslash, control bytes and UTF-8 is reported
  verbatim in every configuration.
- Setup and teardown order and output; a trapping test skips teardown; a
  trapping setup skips the test; another file is unaffected; bad
  signatures are reported.
- `--jobs` 1 and 8 give equal responses, apart from times.
- A run deadlocked in two locks across two threads ends after Stop, and
  leaves no goroutine behind; it fails with the old blocking lock.
