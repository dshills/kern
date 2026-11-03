# Hosted-only runtime entries: implementation plan

**Spec:** `specs/freestanding-availability/SPEC.md` v0.2 (F1 to F3, R1 to
R7). **Branch:** `main`, one commit per phase after an internal review
(the owner chose internal review over prism on 2026-11-03). Each phase is
verified in a scratch worktree holding only its own diff before it is
committed.

**Toolchain.** The native tests need `clang` with both freestanding
targets, `ld.lld`, and `llvm-nm` (R3). Each skips with `t.Skip` naming
the missing tool, as the existing native tests do. This host has all
three, so the acceptance run executes them. The report states which
ran.

**Gates:**
- `go build ./...`;
- `go test ./...`, which includes `./tests`, `./cmd/kern` and
  `./bench/agent/...`;
- `golangci-lint run ./...` and `gofmt -l .` empty;
- `go test -race ./compiler/analysis/... ./compiler/backend/llvm/... ./cmd/kern/...`;
- `go test ./tests` before a documentation commit.

**Baseline.** The baseline compiler is built from the commit this work
starts on (7071ca3), the parent of phase 1's commit, into the scratchpad
as `kern-fa-base`. Every comparison uses the same target, level and
flags with both compilers.

Known facts (checked while writing the plan):
- **The emitter.**
  - `compiler/backend/llvm`'s `emitter.declare(name, sig)` is the one
    place a runtime entry is declared: `fnEmit.rt` calls it, and so do
    about 30 other sites. It also declares LLVM intrinsics (`llvm.*`),
    which are not runtime entries.
  - `EmitWith` declares `kern_trap` and, with `StackCheck`,
    `kern_stack_limit` directly.
  - `fnEmit` emits each block's instructions in one loop (`emit.go`,
    `for _, in := range b.Instrs`), where the current function, block and
    instruction are known.
  - `externDecl` declares a user's `extern "C"` functions. Those are the
    program's own obligation, not runtime entries.
- **The runtime sources.** `runtime/freestanding` embeds `rt.c`
  (`RuntimeC`), `start_aarch64.S` and `start_x86_64.S`.
  `native.FreestandingTriple` is `aarch64-unknown-none-elf`.
- **The CLI.**
  - `cmd/kern`'s `budgetDiags` shows the pattern: a `lowerFunc`
    dependency (`Build`, the show module with no library inlining),
    evaluated after checking.
  - `build` calls it before `buildHook`, and `check` calls it in
    `main.go`.
  - `run` lowers through `buildExec`, then runs the interpreter with
    `it.Freestanding`.
  - `kern test` refuses freestanding mode already.
- **The registry.**
  - Codes live in `compiler/diag/codes.go`; `go generate
    ./compiler/diag` writes `spec/rules.md`, and `spec/rules.lock`
    freezes code to rule.
  - A `lang` code needs a reject fixture with an accepting twin
    (`TestRegistryCoverage`).
  - The harness's `checkFiles` checks in hosted mode only.
- **The motivating failure.** `kern build --env freestanding` on a call
  to `time.now()` fails linking with `undefined symbol: kern_now_ns`.
  `kern run --env freestanding` runs it.

## Phase 1 — Recording, the provided set, the analysis

**Goals:** the analysis of F1 and F2 behind an exported function, and R3
and R4.

**Files:**
- `compiler/backend/llvm`:
  - `Options.Record bool`. When it is set, the emitter appends a
    `RuntimeRef{Symbol, Func, Block, Pos}` for every request to declare
    a runtime entry, made while an instruction is emitted. This happens
    at each request, before `declare`'s deduplication, so a symbol used
    in five functions is recorded five times. Module-level requests
    have a zero function. A *runtime entry* (spec §1) is a symbol that
    is:
    - declared through `declare`;
    - named `kern_…`;
    - not defined by the emitted module.

    `llvm.` intrinsics and any other declared name are not runtime
    entries.

    User `extern "C"` functions go through `externDecl`, never through
    `declare`, so they are not runtime entries. The emitter also appends
    a `FuncRef{Func, Block, Pos, Callee}` for each emitted call or
    `funcref` of a function of the module.
  - Each `RuntimeRef` and `FuncRef` also carries `Instr ir.Instr`, the IR
    instruction being emitted, which is nil for module-level requests.
    The analysis reads the callee's source-level name from it:
    - for an intrinsic call, `Call.Intrinsic` (`now_ns`);
    - for a library call, the callee function's name without its type
      arguments (`time.now`);
    - for a `funcref`, the referenced function's.

    A test checks a direct intrinsic diagnostic's name (`now_ns()` gives
    ``call to `now_ns` …``).
  - Tests: one symbol used at two spans in two functions gives two
    refs; an `extern "C"` function, an `llvm.` intrinsic and a
    non-`kern_` declaration give none.
  - The refs are returned by `EmitRecorded(mod, opts) (Recording, []error)`.
    `EmitWith` is unchanged when `Record` is false: its text is
    byte-identical. A test compares the text of both settings on the
    fixtures.
- `runtime/freestanding/provided.go`: `Provided() map[string]bool`, the
  external symbols defined by the embedded `rt.c` and startup sources.
  The supported grammar is the files' own style:
  - **C function definition:** a line at column 0 not starting with
    `static`, `extern` or `#`, of the form `type name(params)`, whose
    declaration ends in `{` (on that line or the next). A prototype,
    ending in `;`, is a declaration and is not counted.
  - **C object definition:** a line at column 0 not starting with
    `static` or `extern`, of the form `type name` followed by `=`, `[`
    or `;`. A tentative definition counts.
  - **Assembly:** a name given by `.global`/`.globl` and defined by a
    label `name:` in the same file. A `.global` without the label is
    not counted.

  A table test pins each case on short snippets, with and without
  `static`/`extern`.
- `compiler/analysis/hosted.go`:
  `HostedOnly(mod *ir.Module, triple string) (HostedResult, error)`.
  The module is already lowered at the command's optimization level, so
  the level is in the module. The emission options are those a
  freestanding build uses: `Freestanding: true`, `StackCheck: true` and
  the triple. `HostedResult` is
  `{Diagnostics []diag.Diagnostic; Library map[string][]string; Entries []string}`:
  - `Library` is, per library function, the sorted hosted-only entries
    it reaches;
  - `Entries` is the union over every reaching call.

  R4 and R7 read those two fields.

  **Failures.** An emission error, or a reference to a function the
  module lacks, is returned as an error wrapped with the context
  `hosted-only evaluation: …`. `cmd/kern` reports it as an internal
  error (exit code 3, as `internal()` does) and does not build or run.
  An injected-failure test in phase 2, through the `analyze`
  dependency, shows that `buildHook` and the interpreter are not
  reached. The steps:
  1. It emits the module in freestanding mode with `Record`, for the
     triple.
  2. A *hosted-only entry* is a recorded symbol not in `Provided()`.
  3. It builds the call graph from the recorded call and funcref edges,
     and computes, for each library function, the set of hosted-only
     entries it reaches through library calls (a fixed point over the
     graph, which handles recursion).
  4. A reaching call is an edge from a non-library function whose block
     is in a user file (`Block.FileIn`) to an intrinsic or library
     callee with a non-empty set, plus a hosted-only entry recorded
     directly in such a block.
  5. Results are grouped by (file, span) with the union of entries, and
     the F1 message is built.
  6. The output is sorted.
- `compiler/analysis/hosted_test.go`:
  - R1's three programs, debug and release, give exactly the expected
    diagnostics;
  - a closure of the program's own whose body calls `time.now()` gives
    the diagnostic at that call inside the closure, and none at the
    closure's reference or at the call through the function value;
  - an uncalled function of the program that calls `time.now()` is
    reported (roots);
  - a destructor of a program type that calls `time.now()` is reported;
  - `import "std/time"` with no call gives nothing (library
    reachability);
  - `if false { … time.now() … }` is reported (F2: not decided by the
    optimizer);
  - a program calling only console output and the allocator gives none;
  - a function value of `time.now` passed to a function of the program
    gives the diagnostic at the reference;
  - a recursive library cycle terminates;
  - a call merged from two instantiations gives one diagnostic with the
    union.
- `runtime/freestanding/provided_test.go` (R3): `Provided()` contains
  ADR-011's table symbols. Where `clang` and `llvm-nm` exist, it equals
  `llvm-nm --defined-only --extern-only` of `rt.c` and the startup
  object compiled for each target.
- `compiler/analysis/hosted_link_test.go` (R4): where `ld.lld` exists,
  each of R1's programs is built natively at `--opt=debug` for both
  targets through `native.Build`. The guard of phase 2 lives in
  `cmd/kern`, before `native.Build` is called, so `native.Build` stays
  unguarded and no bypass is needed. The test
  collects the `undefined symbol:` lines and checks they equal
  `HostedOnly`'s entries. The programs print the hosted call's result,
  so no optimization deletes it.

**Steps:**
1. Probe: emit R1's programs with `Record` and list the refs, to confirm
   the positions and edges are as described.
2. Implement in the order llvm, provided, analysis. Write the tests with
   each.
3. Run the gates in the phase worktree. Review and commit.

**Acceptance:**
- the unit tests and R3 pass;
- R4 passes on both targets where `ld.lld` exists, and reports skipped
  otherwise;
- with `Record` false, the emitter's text is unchanged on every fixture
  (byte comparison in a test);
- the gates are clean.

**Risks:**
- **Positions.** A recorded position is the call instruction's `Pos`,
  which lowering sets for calls written in the source. A call the
  compiler generates with no position, such as an implicit drop that
  calls a library destructor, is attributed to the declaration span of
  the function containing it (spec F1, amended to say so). A test drops
  a library value whose destructor reaches a hosted entry, if `std/` has
  one (found in step 1's probe); otherwise the fallback is covered by a
  unit test on a constructed module.
- **Parsing C.** The parser handles only the file's own style. R3's
  `llvm-nm` comparison catches drift.

## Phase 2 — Registry, CLI, fixtures, documents

**Goals:** F3, R1, R2, R5, R6 and R7.

**Files:**
- `compiler/diag/codes.go`:
  `"ENV_HOSTED_ONLY": {"6.8", CatLang, 3, "a freestanding program calls a function that needs the hosted runtime"}`.
  Then run `go generate ./compiler/diag` and update `spec/rules.lock`.
- `cmd/kern`:
  - a `hostedDeps{lower lowerFunc; analyze func(*ir.Module, string) ([]diag.Diagnostic, error)}`
    with defaults, as `budgetDeps`;
  - `hostedDiags(srcs, optLevel, triple, deps)`, evaluated in
    freestanding mode only, after budgets, by `check` (`main.go`),
    `build` (before `buildHook`) and `run` (before the interpreter).
    The triple is `--target`, else the manifest's, else
    `native.FreestandingTriple`.
- `tests/harness_test.go`: `checkFiles` takes the fixture header's env.
  For `freestanding`, it also runs the hosted-only evaluation, so a
  reject fixture gets its code from the same evaluation as `check`.
- `tests/freestanding/hosted_only.kern`, a reject fixture (`//! env:
  freestanding`, `//! code: ENV_HOSTED_ONLY`) that calls `time.now()`
  and `now_ns()`, and its twin `hosted_only_ok.kern`, which uses console
  output only.
- `docs/ownership.md` Rule 6.8: F1 to F3.
- `docs/decisions/ADR-011-freestanding-runtime-facilities.md`: a note
  under the table.
- `docs/reports/std-time.md`: the open item is marked resolved.
- `docs/reports/freestanding-availability.md`: the report, with R7's
  list generated by `TestHostedOnlyInventory`. That test in
  `compiler/analysis` runs the analysis over the corpus of spec R7 at
  `--opt=debug` for each target separately, merging
  `HostedResult.Library` across programs per target. With `-update` it
  writes the lists between markers in the report: one list when the two
  targets' lists are equal (said so), and one per target, labelled by
  triple, otherwise. Without it, it fails when the report's list differs, so a
  normal test run keeps the report current.

**Steps:**
1. **The registry.** Add the code, generate, update the lock, and run
   only `go test ./tests -run 'RulesLock|TestRegistryInventory'` and
   `go test ./compiler/diag`. `TestRegistryCoverage` needs the reject
   fixture of step 3 and runs there.
2. **The CLI.** Wire `check`, `build` and `run`, with `cmd/kern` tests:
   - R1's programs through each command at both levels, with exit code
     2 and the exact diagnostic;
   - `build` writes no file at `-o`, and `buildHook` (replaced by a
     counting fake) is never called, which also covers the archive path
     without `ld.lld`;
   - the interpreter never runs (stdout holds only the diagnostic
     response);
   - a project whose `kern.mod` sets `env: freestanding` is evaluated
     with no `--env` flag;
   - a program with a type error and a `time.now()` call gives only the
     type error;
   - the same programs in hosted mode: `run` output unchanged, and
     `build` succeeding where clang exists (R1's hosted builds);
   - a counting test that shows hosted `check` never enters the
     evaluation (R6).
3. **The harness and fixtures.** Add them, then run
   `TestFixtures/freestanding/hosted_only` and `TestRegistryCoverage`,
   now that the code, the CLI evaluation, the env-aware harness and the
   fixture pair all exist.
4. **R2.**
   - Every `tests/freestanding/*.kern` of the baseline corpus is
     accepted by `check --env freestanding`, and builds and runs as
     before (the harness). A newly rejected baseline program stops the
     phase: it is a false rejection to report, not a fixture to change.
   - The freestanding workloads' images, where `ld.lld` exists, are
     compared with `kern-fa-base`'s: the three workloads of
     `bench/kern/` with freestanding mains (ring_buffer,
     page_frame_allocator, buffer_pool, found by their `//! env:
     freestanding` or project manifests) at both levels.
   - `check` on the budget-free hosted files under `tests/` is
     byte-identical to `kern-fa-base`'s (R6).
5. **R7.** Generate the inventory, write the documents and the report,
   and run `go test ./tests`.
6. Run the gates in the phase worktree. Review and commit.

**Acceptance:**
- R1, R2, R5 and R6 hold, as measured;
- the inventory is in the report;
- the gates are clean.

**Risks:**
- **A freestanding fixture that relied on the interpreter's emulation**
  of a hosted entry would now be rejected. R2's run finds it, and the
  phase stops there (step 4): such a rejection needs a decision on the
  spec before the fixture or the rule changes.
- **Which workloads are freestanding.** If no `bench/kern` workload is
  built freestanding, R2's image comparison covers
  `tests/freestanding/` alone, and the report says so.
