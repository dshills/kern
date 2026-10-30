# Plan: `kern test`

**Spec:** `specs/test-command/SPEC.md` v0.1. **Profile:** davin-go.
**Order:** the phases depend on each other in order. Each ends with
`golangci-lint run ./...` and `go test ./...` green; the change lands as
one commit after Phase 4 unless the owner asks for more.

**Before Phase 1 (done, 2026-10-30):** the LLVM IR of every single-file
program was captured with the compiler at 5a3bac6 into the session's
scratch (`ll-after/` of the cross-file work, 218 modules, 12 programs
that do not build for the host listed in `failed.txt`), by
`scripts/capture-ll.sh`, for R13. That capture's file list is the
comparison corpus: Phase 4 compares exactly those 218 modules and that
failure list; the fixtures this change adds (`expect_false`,
`expect_true`) appear only in the new capture, are listed separately in
the report, and are validated by the fixture harness, not by the
comparison. To repeat: `git worktree add <dir>
5a3bac6`, build `./cmd/kern` there, run the script with that binary into
one directory and with the new binary into another, `diff -r`.

## Phase 1: language and IR pieces (R1, R2, R5, R7 mechanism)

**Files.** `compiler/project/discover.go` (+ test), `compiler/resolve/prelude/core.kern`,
`std/core.kern`, `compiler/ir/ir.go`, `compiler/ir/print.go`,
`compiler/lower/lower.go`, `compiler/opt/inline.go`,
`compiler/interp/interp.go`, `compiler/backend/llvm/emit.go`,
`compiler/backend/native/build.go`, `compiler/diag/codes.go` (+ generated
`spec/rules.md`, `spec/rules.lock`), `tests/trap/expect_false.kern`,
`tests/run/expect_true.kern` (+ `.out`).

**Steps.**

1. Discovery: `project.TestFile(name)`; `moduleFiles` leaves test files
   out unless loading for tests; `project.LoadTests` loads the entry
   directories' test files that declare an entry file's module (joining
   the entry files, so no alias check applies to them) and every entered
   module directory's test files (R1, R2). A candidate test file whose
   prologue does not parse is loaded so its error is reported.
2. Prelude: `func expect(cond: bool) { if !cond { trap_kind(explicit) } }`
   after `trap_kind`, copied to `std/core.kern` (R5). User declarations
   shadow prelude names (checked: a user `size_of` compiles), so no
   existing program breaks.
3. IR: `Module.Tests []string`, printed as `test <name>` lines after
   `entry`; the inliner's `callSites` and `markInlined` treat the entry
   and the tests as roots (one helper `roots`).
4. Lowering: `lower.LowerWith(prog, Options{Tests})` requests each test
   declaration as a root and records the IR names in order; `Lower` is
   `LowerWith` with no tests.
5. Interpreter: `RunEntry(name)`; `Run` is `RunEntry(mod.Entry)`.
6. Backend: when `mod.Tests` is non-empty, `@main` reads `atoi(argv[1])`,
   switches to the selected test's block, which calls `kern_set_file`
   with the test's file, `kern_set_args(1, argv)`, `kern_start`, the test,
   and returns 0; a missing or unknown index returns 2. Program functions
   are internal when there is an entry or tests.
   `native.BuildModule(mod, srcs, out, opts)` is `Build`'s back half;
   tests are hosted only.
7. Codes: `TEST_SIGNATURE` (P.3) and `TEST_DIRECTIVE` (P.4), category
   cli; `go generate ./compiler/diag`; the lock file.
8. Tests: `compiler/project` tests for R1 and each clause of R2;
   `compiler/opt` test that an uncalled test function is not `Inlined`;
   the two `expect` fixtures (harness: both engines, both opt levels).

**Acceptance.** Package tests green; `go test ./...` green.

## Phase 2: the runner (R3, R4, R6–R10, R12, R14)

**Files.** `compiler/testrun/find.go`, `compiler/testrun/run.go`,
`compiler/testrun/*_test.go`.

**Interface.** `testrun.Options{Configs []Config; Skipped []Skip;
Timeout time.Duration; Filter *regexp.Regexp}`: the CLI (Phase 3)
validates every input and computes them: the configurations in R6's
order after `--opt` and `--engine` and the clang lookup, the skipped
engines, the timeout (default 10 s; `--timeout` a decimal integer of
seconds from 1 to 3600), the compiled `--run` expression (nil for
none). The runner takes them as given and reads no flag, environment
variable or global; the CLI calls it through `testHook`, the one seam
its tests replace to inject an internal error.

**Steps.**

1. `Find(prog, srcs)`: the test files of the program's user modules,
   sorted by display name; per top-level function, the directive lines
   above it (contiguous comment lines, the one beginning `//! expect:`),
   `TEST_DIRECTIVE` for a malformed, repeated or misplaced directive,
   `TEST_SIGNATURE` for a bad signature (R3, R4); qualified names
   `<module>.<function>`.
2. `Execute(srcs []*source.File, o Options) (*Result,
   []diag.Diagnostic, error)`: the error, when non-nil, is a
   `*testrun.InternalError` wrapping the failing step with its context
   (`native build (release): clang failed: ...`, `native run of
   app.test_x: ...`); the CLI turns it into exactly one `INTERNAL`
   diagnostic and exit 3, and no further run is made. Compilation
   precedes every run: first, for each selected opt level in order,
   `resolve.ResolveFor`, `check.Check`, `Find`, the filter (R12),
   `lower.LowerWith` and `opt.Run`, returning the first level's
   diagnostics (or a later level's, if only it has some) before any run
   starts; assert `mod.Tests` equals the qualified names. Then, as a
   separate loop, configurations in order (R6) and within each the tests
   in order: interpreter
   runs (fresh `interp.New` per run, program file = the test's file,
   identity tags on in debug, `RunEntry` in a goroutine with the timeout;
   output captured in a mutex-guarded buffer that is snapshotted and
   closed at the timeout in one critical section, after which the
   abandoned run's writes are discarded, neither changing the result nor
   accumulating); native runs (one `BuildModule` per level into a temporary
   directory, one process per test with `exec.CommandContext`, status
   128+signal for a signal, `WaitDelay` set). A test expecting `trap
   identity` is not run in release (R6, R10).
3. Outcome classification and agreement exactly as R8 and R9; status as
   R10; the response types of R11 (`Result`, `TestResult`, `Run`,
   `Config`, `Skip`, `Summary`) with JSON tags; `stdout` truncated at
   65,536 bytes with invalid UTF-8 replaced, comparison on raw bytes.
4. Unit tests: directive parsing (every R4 case), signature (each R3
   shape), classification (each R8 rule, including 134 without a record
   and a signal), agreement (each R9 field, the stack exception, thread
   ignored), status precedence (R10).

**Acceptance.** The unit tests pass.

## Phase 3: the command (R6, R11, R12) and end-to-end tests

**Files.** `cmd/kern/test.go`, `cmd/kern/main.go` (dispatch, usage),
`cmd/kern/test_test.go`, `examples/testing/` (a library module and its
tests).

**Steps.**

1. `kern test [--opt=] [--engine=] [--run=] [--timeout=] [--human]
   [files...]`: flags validated (`CLI_INVALID_FLAGS`); `project.LoadTests`;
   the manifest's `env` of `freestanding` refused; native selected by
   default only when clang is found (else listed in `skipped`), and
   `--engine=native` without clang refused; the response (R11) with exit
   0, 1, 2 or 3; `--human` lines.
2. CLI tests (`test_test.go`, programs written to temporary
   directories): R3 (five bad shapes), R4 (four bad directives, three
   good), R6 (flag combinations listed; clang hidden from `PATH` by an
   empty `PATH` for the native refusal and the default skip), R7
   (thread-local isolation; an imported module's test; no `main`), R8
   (return, trap bounds, exit 3, timeout with `--timeout=1`), R9
   (`container_identity()` printed: mismatch), R10 (pass and fail of
   each expectation), R11 (every field of a mixed response; `--human`),
   R12 (selection, bad regexp), R14 (two responses equal without
   `millis`).
3. `examples/testing/`: `kern.mod`, `app.kern` (a small main), `stack/`
   module with a bounded stack and `stack_test.kern` (passing tests, an
   expected `bounds` trap, an expected `overflow` trap); a CLI test runs
   it and expects all pass. The other statuses are not in the example
   (it would then exit 1): the CLI tests of step 2 cover `fail`,
   `mismatch`, `timeout` and `skipped`.
4. Internal errors: a CLI test replaces `testHook` with one returning a
   `*testrun.InternalError` and asserts one `INTERNAL` diagnostic, exit
   3; a `testrun` test builds against a clang that fails (a `PATH` whose
   `clang` is a script exiting 1) and asserts an `*InternalError` naming
   the native build and no run in the result.

**Acceptance.** `go test ./...` green; `kern test` on the example from
its directory prints a passing response.

## Phase 4: documents, identity check, review (R13, R15)

**Files.** `README.md`, `spec/types.md`, `specs/module-discovery/SPEC.md`
(a note), `docs/reports/test-command.md`.

**Steps.**

1. Documents of R15; the report (design, the example's wall time with
   and without native, review).
2. R13: `scripts/capture-ll.sh` with the new compiler into a fresh
   directory; then, for every `.ll` file of the 5a3bac6 capture,
   `cmp` it with the file of the same name in the new capture (every
   one must be identical and present); `diff` the two `failed.txt`
   (empty); and list the new capture's `.ll` files absent from the
   baseline (expected: exactly the `expect_false` and `expect_true`
   modules, each in both opt levels), reported separately.
3. Review: `golangci-lint`, `go test ./...`, `prism review staged`,
   `realitycheck check` (spec and plan of this change), `verifier
   analyze` (the new and touched packages); clarion skipped (no
   `docs/clarion-meta.json`).
4. Commit `feat(cli): kern test, in-language tests run in both engines
   and opt levels`, ask to push.

**Acceptance.** Reviews clean or justified in the report; commit made.
