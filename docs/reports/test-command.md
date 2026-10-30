# `kern test`: in-language tests run differentially

**Date:** 2026-10-30. **Spec:** `specs/test-command/SPEC.md` v0.1.
**Plan:** `specs/test-command/PLAN.md`. **Item:** the seventh of the
ten-item list ("a test command: an in-language way to declare a test,
run it under both backends and compare trap records, without cloning
the compiler repo").

## Before

Tests of kern code existed only as fixtures under `tests/`, run by the
Go harness (`tests/harness_test.go`), which builds each program in the
interpreter and natively in both opt levels and compares outputs and
trap records. A program outside the repository had `kern run`, which
executes `main` once in one configuration, and no way to declare a test.

## What a test is

No grammar change. A test file is a file whose name ends in
`_test.kern`; it declares the module it tests and sees its private
names. A test function is a top-level `test_` function of a test file
with no parameters, type parameters or results. A test is expected to
return; a `//! expect: trap <kind>` line directly above it (the fixture
harness's header form) says it must trap with that kind. The assertion
is `expect(cond: bool)`, added to the prelude, because `assert` is a
keyword (`unsafe assert Send for T`): it traps `explicit`, and since
every trap raised inside std/core reports the user-level call, the
record's file and span are those of the `expect(...)` call. User
declarations shadow prelude names, so no existing `expect` could
break (there is none in the repository).

Module discovery leaves test files out of a module directory in every
command but `kern test`, which loads them, plus the test files of each
entry file's directory that declare the entry file's module (another
program's test file in the same directory is not loaded).

## How it runs

For each selected opt level the program is resolved, checked, lowered
with the test functions as roots (`lower.LowerWith`), and optimized,
before any test runs. The IR module lists the tests (`ir.Module.Tests`,
printed as `test <name>` lines), and the inliner's dead-function marking
treats them like the entry, without which every uncalled test would be
marked `Inlined` and dropped (`TestInlineKeepsTestRoots` shows both).
Then, configuration by configuration (interpreter debug, interpreter
release, native debug, native release) and test by test:

- **Interpreter:** a fresh `interp.Interp` per run, entered at the test
  (`RunEntry`), with the test's file as the program's file and identity
  tags as in `kern run`. A run past the timeout is stopped: `Interp.Stop` sets a flag every
  thread checks at each basic block and wakes a thread waiting for a
  join, so a loop on any thread ends within the one-second grace period
  (`TestInterpStop`); a thread blocked in a lock or a condition wait is
  left blocked and idle. The output captures are frozen first, so later
  writes are discarded.
- **Native:** one executable per opt level (`native.BuildModule`, the
  back half of `native.Build`), whose `@main` parses the test's index
  from `argv[1]` with `strtol`, requiring a leading digit and the whole
  argument consumed (anything else exits 2 and runs nothing), sets the test's file and an argument list of the program's
  name alone, starts the runtime and calls the test; one process per run,
  killed at the timeout.

Output is captured in bounded memory: the first 65,537 bytes of standard
output with its length and SHA-256, and the end of standard error.
Outcomes are classified from what the run left (`return`, `trap`,
`exit`, `crash`, `timeout`), and two runs agree under the fixture
harness's rules: outcome, the whole output (by length and digest), an exit status, and a
trap's kind, file and span, except the span of a `stack` trap (ADR-012)
and the record's thread. A test is `pass`, `fail` (the runs agree and
miss the expectation), `mismatch` (two runs disagree), `timeout`, or
`skipped` (an `identity` trap expected and only `release` selected:
identity tags exist only in debug builds, so such a test runs in debug
configurations only).

The response is the usual envelope at `schema_version` 0.3 with a result
of configurations, skipped engines, tests (name, location, expectation,
status, every run) and a summary; exit 0 when every test passes, 1 when
one does not, 2 on diagnostics or a flag error, 3 on an internal error
(a native build or process start that fails). `--human` prints one line
per test and a summary. Two diagnostic codes are new: `TEST_SIGNATURE`
(P.3) and `TEST_DIRECTIVE` (P.4).

## What changed, by package

| Package | Change |
|---|---|
| `compiler/project` | `TestFile`; test files out of module directories; `LoadTests` |
| `compiler/resolve` (prelude), `std/core.kern` | `expect` |
| `compiler/ir` | `Module.Tests`, printed |
| `compiler/lower` | `LowerWith(prog, Options{Tests})` |
| `compiler/opt` | entry and tests as roots of the dead-function marking |
| `compiler/interp` | `RunEntry` |
| `compiler/backend/llvm`, `native` | the test-selecting `@main`; `BuildModule` |
| `compiler/testrun` (new) | finding tests and directives, running, classifying, comparing |
| `cmd/kern` | `kern test` |
| `compiler/diag`, `spec/rules.*` | `TEST_SIGNATURE`, `TEST_DIRECTIVE` |
| `tests` | `expect_false`, `expect_true` fixtures; a project example is one directory fixture and test files are never fixtures |
| `examples/testing` | a program with a module and tests of both expectations |
| docs | README (`test` row and a Tests section), `spec/types.md` §6, the module-discovery note |

## Measured

**Other commands unchanged (R13).** `scripts/capture-ll.sh` with this
change against the capture of 5a3bac6: every one of the 218 baseline
modules is byte-identical, the same twelve programs fail to build for
the host, and the only new modules are the two `expect` fixtures in both
opt levels (four files). `bench/REPORT.md`'s checksums are unchanged
(`go test ./...`).

**Wall time** of `kern test` on `examples/testing` (four tests, three in
an imported module), three runs each, at a one-minute load of 8.6 while
the test suite ran:

| selection | wall time |
|---|---|
| default (four configurations) | 0.47–0.48 s |
| `--engine=native` (two builds, eight processes) | 0.47–0.48 s |
| `--engine=interp` | under 0.01 s |

The two clang invocations are the whole cost; the interpreter half is
the fast path for an edit loop.

**Coverage.** Ten CLI tests (`cmd/kern/test_test.go`: a mixed program
with every status but `timeout` and `skipped`, every field of the
response, the human form and a repeated run equal but for `millis`;
flags and their refusals, including `--engine=native` and the default
skip without clang; a freestanding manifest; signature and directive
diagnostics; a library-only program with tests in two modules and
another module's test file left out; timeouts in both engines; the
identity rule; no tests; an injected internal error; the example), ten
unit tests in `compiler/testrun` (order and expectations, each
directive and signature error, classification, agreement, status
precedence, a failing clang, an interpreter timeout whose later output
is discarded), discovery tests for each clause, the inliner root test,
and the two fixtures through the harness in every configuration.

## Review

- **speccritic** (gpt-6.1-sol, profile general, with `spec/types.md` and
  `docs/reports/trap-file.md`): INVALID on the first draft, one critical
  question (R14 promised identical responses for nondeterministic tests;
  now limited to deterministic tests and an unchanged toolchain), with
  warnings on outcome classification, native build failures, ordering,
  output encoding and identity-only tests, all folded in; then
  VALID_WITH_GAPS, no critical finding, its clarifications folded in.
- **plancritic** (davin-go): EXECUTABLE_WITH_CLARIFICATIONS twice (79,
  86), no critical finding, full coverage on the second. Folded in:
  compilation of every opt level before any run; the capture buffer
  closed at a timeout; the byte-identity corpus fixed to the baseline's
  file list with new modules reported apart; the internal-error contract
  and its tests; the runner's options computed and validated by the CLI
  alone; an all-passing example, with `fail`, `mismatch`, `timeout` and
  `skipped` covered by the CLI tests.
- **prism** (gpt-6.1-sol, staged diff), first run: one high and four
  medium findings, all fixed. High: a timed-out interpreter run kept
  running and using the CPU; the interpreter now stops cooperatively
  (above). Medium: `atoi` ran test 0 for a malformed index (now
  `strtol` with full-consumption and leading-digit checks,
  `TestNativeIndexStrict`); an unreadable test file in the entry
  directory was skipped silently (now `API_FILE_NOT_FOUND`,
  `TestTestFileUnreadable`); output was captured unbounded (now the
  bounded capture, `TestCaptureBounded`); a directory fixture of test
  files only would panic the harness (now a descriptive failure).
  The second run raised two medium findings, both fixed: a test file
  whose `stat` failed (a dangling link) was still skipped silently (now
  reported, `TestTestFileDangling`), and the standard-error tail copied
  a large write whole before trimming it (now only the write's own tail
  is kept). The third run is clean.
- **realitycheck** (this change's spec and plan): PARTIALLY_ALIGNED, score 100, no
  violation; the partial statuses are the tool's truncated code index
  (it could not read every function body), not drift.
- **verifier** (offline, the touched packages): 78 recommendations, all "exported symbol
  has no corresponding test" by name matching; the new exported
  functions it lists (`Find`, `LoadTests`, `TestFile`, `LowerWith`,
  `BuildModule`) are each exercised by the tests above under other
  names, and the rest predate this change.
- `golangci-lint run ./...` 0 issues (two staticcheck selector
  simplifications fixed); `go test ./...` green.

The fixture harness first failed on the example: it globbed every
`examples/*/*.kern` as a single-file program, so `app.kern` lost its
import and `app_test.kern` had no header. An example holding a
`kern.mod` is now one directory fixture, and test files are never
fixtures. `scripts/capture-ll.sh` printed a zsh "no matches" warning
when its output directory was new (from the cleanup line added in the
cross-file review); it now uses a null glob.

## Not done

- Parallel runs, setup and teardown, benchmarks, messages on `expect`,
  freestanding targets (spec §3).
- `kern check` does not check test files (they are not loaded); `kern
  test` reports their diagnostics.
- A timed-out interpreter run blocked in a lock or a condition wait
  stays blocked, idle, until the command exits.
- The interpreter's `args()[0]` is the test's file and the native one
  the executable's path, so a test printing it is reported `mismatch`.
