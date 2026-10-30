# `kern test`: in-language tests run differentially

**Version:** 0.1 (2026-10-30). **Scope:** a new driver command `kern test`
(`cmd/kern`), the test-file rule of module discovery
(`compiler/project`), one library function in the core prelude
(`compiler/resolve/prelude/core.kern`, mirrored in `std/core.kern`),
the extra lowering roots and module field the command needs
(`compiler/lower`, `compiler/ir`, `compiler/opt`), the native entry
that selects a test (`compiler/backend/llvm`, `compiler/backend/native`),
the interpreter entry that runs one (`compiler/interp`), two diagnostic
codes (`compiler/diag`, `spec/rules.md`, `spec/rules.lock`), and the
documents (`README.md`, `spec/types.md`). **Language impact:** no
grammar change; one prelude function (`expect`); one file-naming rule
in module discovery (§2.1). The machine-API commands of the language
spec §16 are unchanged: `kern test`, like `run` and `build`, is a driver
command, and its result shape is defined here. **Related:**
`specs/initial/AI_Native_Systems_Language_SPEC.md` §6 (trap kinds and
records), §16 (the response envelope); `specs/module-discovery/SPEC.md`
(the file set of a program); `docs/reports/trap-file.md` (the record's
file); ADR-012 (the native stack-fault record has no span);
`tests/harness_test.go` (the fixture harness whose comparison rules
§4 R9 adopts).

**Review status.** speccritic (OpenAI gpt-6.1-sol, profile general,
with `spec/types.md` and `docs/reports/trap-file.md` as context): the
first draft was INVALID on one critical question (R14 promised identical
responses for tests whose output is nondeterministic; it is now limited
to deterministic tests), with warnings on outcome classification,
native build failures, ordering, output encoding and identity-only
tests, all folded in; the second VALID_WITH_GAPS, no critical finding,
its clarifications (stack spans, the timeout's domain, `SKIP` lines,
toolchain availability in R14, execution order, compilation count as
guidance) folded in. The remaining warnings are the preflight's flags on
R*n* labels and code identifiers.

**Glossary.** R*n*: requirement *n* of §4. Engine: the interpreter
(`kern run`) or the native build (`kern build` and running the
executable). Opt level: `debug` or `release`. Configuration: one
(engine, opt level) pair. Run: one execution of one test in one
configuration. Test file: a source file whose base name ends in
`_test.kern`. Test function: a top-level function of a test file whose
name begins with `test_` (§2.2). Qualified name: `<module>.<function>`,
as the IR names the function. Trap record: the JSON line
`{"trap": {"kind", "file", "span", "thread"}}` of spec §6. Entry files:
the files named on the command line, or the manifest's entry when none
is named (`specs/module-discovery/SPEC.md`). Directive: a comment line
of the form `//! expect: ...` (§2.3). Prelude: the core library every
program sees unqualified (`std/core.kern`).

## 0. Goals

A program's author, human or agent, declares tests in kern source next
to the code, and one command, without the compiler repository's Go
harness, runs every test in both engines and both opt levels, reports
each test's outcome as JSON a tool can act on, and reports any
disagreement between configurations as a distinct status, since the
language's pitch is that the interpreter and the native build execute
one semantics. Non-goals: benchmarks, fuzzing, coverage, test fixtures
with setup and teardown, parallel execution, freestanding targets.

## 1. Background

Fixtures under `tests/` are run by `tests/harness_test.go`, which builds
each one in the interpreter and natively in both opt levels and
compares outputs and trap records. Nothing of that is available to a
program outside the repository: `kern run` executes `main` once, in one
configuration, and there is no way to declare a test. `assert` is a
keyword (`unsafe assert Send for T`), so an assertion needs another
name.

## 2. Design

### 2.1 Test files

A test file belongs to the module its `module` line declares, like any
file, so its functions see the module's private names. Module discovery
leaves test files out of every module directory in every command except
`kern test`; a test file named explicitly on a command line is loaded
like any named file. `kern test` loads, in addition to what discovery
loads for the same entry files: every test file in the directory of each
entry file that declares the same module as that entry file, and every
test file of every module directory discovery enters; their imports are
discovered like any file's.

### 2.2 Test functions

A test function takes no parameters, has no type parameters and no
results, has a body, and is not `extern`. The tests of a program are the
test functions of the test files `kern test` loaded, ordered by file
name and then by position in the file, where a file's name is its
display name as the loader recorded it (the path as named, or as
discovery joined it from the project root) compared byte by byte; two
test functions of one file never share a position, so the order is
total.

### 2.3 Expectations

A test is expected to return unless a directive says otherwise. The
directive lines of a function are the comment lines directly above its
`func` line, contiguous with it and with each other (no blank line).
Among them, at most one may begin with `//! expect:`; its text after
the colon, trimmed, is `return` or `trap <kind>` with `<kind>` one of
the trap kinds of spec §6 (`bounds`, `overflow`, `div_zero`, `shift`,
`conversion`, `generation`, `vacant`, `identity`, `disjoint`, `oom`,
`stack`, `explicit`, `unreachable`).

### 2.4 Assertion

The prelude gains `func expect(cond: bool)`: it returns when `cond` is
true and traps with kind `explicit` otherwise. As for every trap raised
inside the core library, the record names the file and span of the
user-level `expect(...)` call.

### 2.5 Running

For each opt level selected, `kern test` compiles the program with every
test function reachable beside `main`, and runs each test in each
selected engine (the number of compilations and executables is
implementation guidance, not a requirement):

- **Interpreter:** a fresh interpreter instance per run, whose entry is
  the test function, with identity tags on in `debug` and off in
  `release` (as `kern run`), and whose program file (the file a
  runtime-raised trap names) is the test function's file.
- **Native:** one executable per opt level, built as `kern build` would
  build it, whose entry runs the test the runner selects; one process
  per run. The executable's program file is the selected test's file,
  and the program's arguments (std/io `args()`) are the executable's
  name alone.

Runs are made configuration by configuration in the order of R6, and
within a configuration test by test in the order of §2.2. A run is
limited to a timeout (default 10 seconds; `--timeout=<seconds>` takes a
decimal integer from 1 to 3600, and any other value fails with
`CLI_INVALID_FLAGS`);
at expiry a native process is killed, and an interpreter run is
stopped: each of its threads ends at its next basic block, and a thread
blocked in a lock or a condition wait, which runs no block until woken,
is left blocked and idle after a one-second grace period.

## 3. Out of scope

- Changes to §16 or to the grammar.
- Running tests in parallel, test setup or teardown, benchmarks.
- A trap kind of its own for a failed assertion (`explicit` is used).
- Messages on `expect` (the record's span locates the call).
- Freestanding targets and `--test-hooks`.
- `kern check` and the other commands reporting errors in test files:
  they do not load them (§2.1); `kern test` does.

## 4. Requirements

Each requirement names how it is verified.

**R1. Test files in discovery.** In every command other than `kern
test`, the files of a module directory are its `.kern` files whose base
name does not end in `_test.kern`; a named file is loaded whatever its
name. *Verified by:* a `compiler/project` test with a module directory
holding `lib.kern` and `lib_test.kern` (only the first is loaded by
`Load`); a test that naming `lib/lib_test.kern` explicitly loads it.

**R2. The test file set.** `kern test` loads the entry files, the test
files of each entry file's directory that declare that entry file's
module (a test file there declaring another module is not loaded), the
test files of every module directory discovery enters (each must declare
the directory's module, `MODULE_MISMATCH` otherwise, as for other
files), and the modules every loaded file imports. *Verified by:*
`compiler/project` tests for each clause, including a test file in the
entry directory with another module (not loaded) and a test file that
imports a module nothing else imports (that module is loaded).

**R3. Test functions and their signature.** A top-level function of a
test file whose name begins with `test_` is a test function. If it has
a parameter, a type parameter, a result, no body or an `extern` ABI,
`kern test` reports `TEST_SIGNATURE` (rule P.3, category cli) at the
function's span, one diagnostic per such function. Functions of a test
file not named `test_*`, and methods, are ordinary helpers. *Verified
by:* a CLI test with one test function of each invalid shape (five
diagnostics, `ok` false, exit 2, no test run) and one with only valid
ones.

**R4. Directive.** The expectation of a test function is read from its
directive lines (§2.3). `kern test` reports `TEST_DIRECTIVE` (rule P.4,
category cli) at the directive line's span when a directive's text is
neither `return` nor `trap <kind>` with a §6 kind, when a function has
more than one `//! expect:` line among its directive lines, or when a
test file has an `//! expect:` line among the directive lines of a
function that is not a test function. A `//!` line that does not begin
with `//! expect:` is not a directive. *Verified by:* a CLI test with
each malformed case (unknown kind, unknown word, two directives, a
directive on a helper), and one with `return`, `trap bounds` and no
directive accepted.

**R5. `expect`.** The prelude declares `func expect(cond: bool)`. A call
with `true` returns; with `false` it traps `explicit`, and the record's
file and span are the file and span of the `expect(...)` call, in both
engines and both opt levels. `std/core.kern` and the embedded prelude
stay identical. *Verified by:* a fixture `tests/trap/expect_false.kern`
(`//! expect: trap explicit`) with its accepting twin
`tests/run/expect_true.kern`, compared by the fixture harness in every
configuration; the existing prelude-sync test.

**R6. Configurations.** By default `kern test` runs the four
configurations in the order interpreter-debug, interpreter-release,
native-debug, native-release. `--opt=debug|release` keeps that level's
configurations only; `--engine=interp|native` keeps that engine's only;
the two combine. The manifest's `opt` is not applied. When the native
engine is selected by default and clang is not found, the native
configurations are left out and listed in the result's `skipped` with
the reason; when `--engine=native` is given and clang is not found,
`kern test` fails with `CLI_INVALID_FLAGS`. `--env=freestanding`, a
manifest `env` of `freestanding`, and `--test-hooks` fail with
`CLI_INVALID_FLAGS`. A test whose expectation is `trap identity` runs in
`debug` configurations only (identity tags exist only there, spec
§5.6). *Verified by:* CLI tests of each flag combination (the
configurations listed in the result), of `--engine=native` with clang
hidden from `PATH`, and of an identity-trap test (two runs, both debug).

**R7. Isolation and reachability.** Each run must start from the
program's initial state: globals and thread-locals at their
initializers, no output written. Every test function, in the main
module or an imported one, must be compiled and runnable in every
configuration whether or not anything calls it; the native executable
must run exactly the test the runner selects and exit when that test's
execution ends (returning from the test is status 0). The program's
`main`, if any, is compiled but not run. *Implementation guidance
(non-normative):* the lowering takes the test functions as roots beside
`main`, the IR module lists them so that the inliner's dead-function
marking keeps them, and the native backend emits an entry that selects
one by an index argument. *Verified by:* a CLI test whose
two tests each increment a thread-local and `expect` it to be 1 (both
pass in all four configurations); a test of an imported module's test
function (`lib/lib_test.kern`) that passes; a test in a program without
`main`; a `compiler/opt` test that a test function nothing calls is not
marked `Inlined`.

**R8. Outcome of a run.** A run's `status` is the interpreter's exit
status, or the native process's exit status, or 128 plus the signal
number when the process ended by a signal, or -1 for a run that timed
out. Its outcome is decided in this order: `timeout` when the run
reached the timeout; `trap` when the status is 134 and the run's
standard error holds a trap record (a line that parses as the §6
record; the last such line is used); `crash` when the native process
ended by a signal, or the status is 134 without a record; `return` when
the status is 0; `exit` otherwise (the program called `exit(n)` with
`n` not 0; `exit(0)` is indistinguishable from `return`). The run's standard output is captured up to the end of the run, or up
to the moment of the timeout for a timed-out run (later writes of a
stopping interpreter run are discarded), in bounded memory: its first
65,537 bytes, and its length and SHA-256 digest; of standard error, the
last 65,536 bytes or more, where the trap record is. The native
executable runs a test only when its argument is a decimal index the
whole argument spells, naming a test; otherwise it exits 2 and runs
nothing. *Verified by:* CLI tests of a
test that returns, one that traps `bounds`, one that calls `exit(3)`,
and one that loops forever under `--timeout=1`.

**R9. Agreement.** Two runs of one test agree when their outcomes are equal and their
standard outputs are byte-identical (equal length and SHA-256 digest), and, for `exit`,
their statuses are equal, and, for `trap`, their kinds are equal and, unless the kind is `stack`, their files and
spans are equal (ADR-012: the native stack-fault record has no location,
and the response reports the span and file the record carries, `{"start":
0, "end": 0}` and the program's file natively; the file exception was
added by `specs/runtime-trap-file/SPEC.md` R7). The record's `thread` field is
not compared. These are the fixture harness's rules. *Verified by:* unit
tests of the comparison over each field, and a CLI test of a test whose
output differs by opt level (it prints `container_identity()`, 1 or more
in debug and 0 in release) and is reported `mismatch`.

**R10. Test status.** A test with no run (its expectation is `trap
identity` and no `debug` configuration is selected) has status
`skipped`. Otherwise its status is `timeout` if any of its runs timed
out; else `mismatch` if any two of its runs disagree; else `pass` if the
common outcome meets the expectation (`return` for an expected return;
`trap` with the expected kind for `trap <kind>`); else `fail`.
*Verified by:* the CLI tests of R7–R9 assert the status of each case; a
test expecting `trap bounds` that traps `bounds` passes, one expecting
`trap bounds` that returns fails, and one expecting `return` whose
`expect(false)` traps fails.

**R11. Response.** `kern test` writes one JSON response in the §16
envelope at `schema_version` 0.3 to standard output. On diagnostics
(any resolve, check or test diagnostic, or a flag error) `ok` is false,
`result` is null, nothing runs, and the exit status is 2; an internal
error exits 3 as for every command. Otherwise `result` is
`{"configurations": [config], "skipped": [{"engine": string, "reason":
string}], "tests": [test], "summary": {"pass": int, "fail": int, "mismatch": int, "timeout": int, "skipped": int}}`, where `config = {"engine": "interp"
| "native", "opt": "debug" | "release"}`, `test = {"name": string
(qualified), "loc": loc (the span of the function's declaration in
its file),
"expect": {"outcome": "return"} | {"outcome": "trap", "kind": string},
"status": "pass" | "fail" | "mismatch" | "timeout" | "skipped", "runs": [run]}` and
`run = {"engine", "opt", "outcome": "return" | "trap" | "exit" | "crash"
| "timeout", "status": int, "trap": {"kind": string, "file": string,
"span": span} | null, "stdout": string, "stdout_truncated": bool,
"millis": int}`, where `stdout` holds the first 65,536 bytes of the
captured output with each byte sequence that is not valid UTF-8 replaced
by U+FFFD (agreement, R9, compares the whole captured bytes) and
`stdout_truncated` says whether bytes were left out; `runs` follow
the configuration order and include every run made. `ok` is true and the exit status 0 when every test passes or is
skipped, including when there is none; otherwise `ok` is false and the
exit status 1. A failure to build the native executable (clang reports
an error) or to start its process is an internal error (exit 3, one
`INTERNAL` diagnostic naming the step), and no further run is made. `--human` writes instead
one line per test (`PASS`, `FAIL`, `MISMATCH`, `TIMEOUT` or `SKIP`, the
name, nothing more for `PASS` and `SKIP`,
and for a test that did not pass the first run that disagrees or misses
the expectation, with its trap as `file:line:col kind`) and a summary
line, with the same exit status. *Verified by:* a CLI test decoding the
response of a mixed program and checking every field; a `--human` test.

**R12. Filter.** `--run=<regexp>` (Go `regexp` syntax) keeps the tests
whose qualified name the expression matches; the others are not run and
not listed. An expression that does not compile fails with
`CLI_INVALID_FLAGS`. *Verified by:* a CLI test selecting one of two
tests, and one with `--run=(`.

**R13. Other commands unchanged.** For a program with no test file,
every other command's output is unchanged; in particular the LLVM IR of
every single-file program under `bench/kern`, `tests/run`, `tests/trap`
and `tests/positive` is byte-identical before and after this change in
both opt levels (`scripts/capture-ll.sh`), and `bench/REPORT.md`'s
checksums are unchanged. *Verified by:* the capture and `diff -r`;
`go test ./...`.

**R14. Determinism.** Tests are listed in the order of §2.2 and each
test's runs in configuration order, in every invocation. For a program
whose tests are deterministic (their output and outcome do not depend
on thread scheduling, the clock, the environment or a timeout), two invocations with the same flags on the same files, with the same
native toolchain available or not, produce the same response except for
`millis`. *Verified by:* a CLI test comparing
two responses with `millis` removed.

**R15. Documents.** `README.md` describes `kern test` with an example;
`spec/types.md` states the test-file rule of §2.1, test functions,
directives and `expect`; `spec/rules.md` (generated) and
`spec/rules.lock` list `TEST_SIGNATURE` (P.3) and `TEST_DIRECTIVE`
(P.4); `specs/module-discovery/SPEC.md` notes the exclusion of §2.1.
*Verified by:* review; the registry tests of `tests/` (every code the
reference names is registered).

## 5. Acceptance criteria

`golangci-lint run ./...` and `go test ./...` are clean (covering R1–R12,
R14 and R13's checksums); R13's LLVM IR capture is byte-identical with
the same set of programs failing to build before and after; an example project outside `tests/` (`examples/testing/`, a program
with a library module whose tests cover both expectations, `return` and
`trap <kind>`, and all pass) runs under `kern test` with the documented
response, the `fail`, `mismatch`, `timeout` and `skipped` statuses being
covered by the CLI tests of R7–R10; and the documents of R15 are
updated. A report `docs/reports/test-command.md` records the design, the
measurements (the wall time of `kern test` on the example) and the
review.

## 6. Risks

- **Blocked interpreter runs.** A timed-out interpreter run whose thread
  is blocked in a lock or a condition wait stays blocked (idle, not
  using the CPU) until the command exits.
- **Native build time.** Two clang invocations per `kern test` (one per
  opt level) dominate the wall time of small programs; `--engine=interp`
  is the fast path.
- **Nondeterministic tests.** A test whose output depends on thread
  scheduling, the clock or identity tags is reported `mismatch`; R9
  states the rule so the author can tell.
- **Discovery change.** A project that keeps a non-test module file
  named `*_test.kern` loses it from its builds; no file in this
  repository is so named.
