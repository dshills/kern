# Declared budgets — implementation plan

**Spec:** `specs/declared-budgets/SPEC.md` v0.1 (R1 to R8, §5). **Branch:**
`main`, one commit per phase after prism review. **Gates for every
phase:** `go build ./...`, `go test ./...` (the root packages, `./tests`
and `./cmd/kern` included), `golangci-lint run ./...` clean, and for
phases touching the checker or CLI `go test -race ./compiler/... ./cmd/...`.

**Review status.** plancritic (openai gpt-6.1-sol, profile `davin-go`),
twelve rounds, ending EXECUTABLE_AS_IS, score 100, 39 of 39
requirements covered. The rounds corrected the spec too: §5's starts
(six of ten already have runtime bounds checks, so they fail their
budgets by design), R2's split between `SYNTAX` and `BUDGET_INVALID`
(the self-hosted parser must accept what the Go parser accepts), and
R4's primary span (entry V + 1).

## Phase dependencies

1 → 2 → 3 → 4 → 5 → 6. Phase 1 only adds syntax; no program changes
meaning until phase 4 wires the commands. Phase 6 needs phase 4 (it runs
`kern check` on generated files).

Known facts the plan relies on (checked while writing it):
- `contract` is a lexer keyword (`lexer.KwContract`); `budget` is not and
  must not become one: `tests/run/vecin_guard.kern` and
  `tests/run/vecin_edges.kern` declare `func budget(...)`.
- `parseDecl` dispatches on `KwFunc`/`KwContract`; `parseImpl` loops over
  items; `parseFunc` reads an optional contract and sets `FuncDecl.Pos`
  to it.
- `ast.Dump` prints absent children as `_`; `compiler/ast/testdata/all_nodes.kern`
  must use every node type and its dump is pinned (`KERN_UPDATE_DUMP=1`
  regenerates it).
- `tests/selfhost_parser_test.go` requires the kern parser in
  `examples/selfhost_parser` to accept exactly the files under `tests/`,
  `std/`, `examples/`, `bench/kern/` and the prelude that the Go parser
  accepts, with identical dumps. Any budget fixture under `tests/`
  therefore needs the self-hosted parser to handle budgets in the same
  phase.
- `tests/harness_test.go` `checkFiles` is resolve plus check; a reject
  fixture must produce exactly one diagnostic with its code and rule, and
  `TestRegistryCoverage` requires every `lang` code to have a reject
  fixture with an accepting twin. `TestRegistryInventory` requires every
  backquoted code in the spec and `docs/ownership.md` to be registered.
- `kern check` loads test files (`project.LoadTests`), may run through
  `incremental.Check` when a cache directory is set, otherwise
  `checkHook`; `Build(srcs, opt)` in `cmd/kern/run.go` is the show module.

## Phase 1 — Syntax (R1, the `SYNTAX` half of R2)

**Goals.** Parse, store, dump and format budget blocks in both parsers.

**Files.**
- `compiler/ast/ast.go`: `type Budget struct { Pos; Items []*BudgetItem }`,
  `type BudgetItem struct { Pos; Name string; NamePos Pos; Value string }`
  (`Value` is the literal's text, suffix included, so the resolver can
  judge it), `FuncDecl.Budget *Budget`.
- `compiler/parser/parser.go`: `parseDecl` and the `parseImpl` item loop
  treat `Ident` with text `budget` followed by `LBrace` (`peekKind(1)`)
  as the start of a function declaration; `parseFunc` reads
  `[contract] [budget] func`, `FuncDecl.Pos` starting at the first of
  them. `parseBudget`: `budget {`, then items `Ident IntLit` separated by
  `sep` (newline or `;`), trailing separator optional, `}`. Anything else
  is the usual `SYNTAX` error (`expected ..., found ...`). A `budget`
  block followed by anything but `func` is `SYNTAX` ("a budget block must
  precede a func declaration").
- `compiler/ast/dump.go`: `(budget (item Name Value)...)`, printed as a
  child of `func` right after the contract **only when present**, so
  every existing dump stays byte-identical; the comment table documents
  it. `compiler/ast/walk.go` visits the new nodes if it visits contracts.
- `compiler/ast/testdata/all_nodes.kern` gains a budgeted function (one
  top-level, one in an `impl`, one after a contract); regenerate
  `all_nodes.dump`.
- `compiler/format/format.go`: `budget(b)` prints `budget { a 0; b 1 }`
  on its own line before `func`, after a contract, and `budget { }` for
  an empty block; called from both `funcDecl` paths (top level and
  `impl`).
- `spec/format.md`: one rule for the block's layout.
- `spec/grammar.ebnf`: the productions of spec §3.
- `examples/selfhost_parser/syntax/{lex,parse,dump}.kern`: the same
  recognition (contextual, no new keyword), the same item grammar and
  errors, and the same dump; `selfhost_parser.out` updated if the
  example's expected output covers it.

**Tests.**
- `compiler/parser/parser_test.go`: both separators, trailing separator,
  empty block, with and without contract, inside `impl`; `budget` as a
  function name, a local and a field still parse; each `SYNTAX` case
  (budget before `struct`, `union`, `interface`, `const`,
  `thread_local`, `contract`, `extern`, `impl`, end of file;
  item `bounds_checks n`; `bounds_checks -1`; missing `}`).
- `compiler/format/format_test.go`: a round-trip matrix (parse, dump,
  format, parse, dump; both dumps equal) over each of the three item
  names × both separators × trailing separator or not × with and without
  a contract × top level and in `impl`, plus an empty block;
  idempotence on every case.
- `TestDumpAllNodes` (pinned dump), `TestSelfhostParser` (both parsers
  agree on the new testdata and fixtures).
- A positive fixture `tests/positive/budget_syntax.kern` (accepting,
  budgets that every count meets trivially, for example on functions
  with no indexing) holding every accepted form of the Go test matrix:
  each item name, `;` and newline separators, a trailing separator, an
  empty block, with and without a contract, top level and in `impl`,
  and `budget` as a function, local and field name. The self-hosted
  parser's corpus test and the formatter property tests then compare
  every form. It must not fail later phases: its values are met.

**Acceptance.** All gates pass; no existing dump, format output or
diagnostic changes (`go test ./...` passes unchanged golden files).

**Key decisions.** Contextual recognition at declaration starts only;
dump omits an absent budget instead of printing `_`, to keep every dump
and the self-hosted parser's existing output unchanged.

**Risks.** The self-hosted parser is kern code built natively by its test
(skipped without clang); run it locally, since it is the phase's main
cost. A `budget` identifier at the start of a statement is never at a
declaration start, so statement parsing is untouched; the tests above
pin that.

## Phase 2 — `BUDGET_INVALID` and the rule (the rest of R2, part of R7)

**Goals.** Semantic validation of items, the code, rule 7.6 in the spec.

**Files.**
- `compiler/diag/codes.go`: `"BUDGET_INVALID": {"7.6", CatLang, 0,
  "a budget item names an unknown kind, repeats one, or has a suffixed or out-of-range value"}`;
  `go generate ./compiler/diag` regenerates `spec/rules.md`;
  `spec/rules.lock` gains the line.
- The resolver (the declaration pass that already walks `FuncDecl`s;
  the exact file is found by locating where contracts are validated, or
  `compiler/resolve/decls.go` otherwise): for every budgeted function,
  one `BUDGET_INVALID` per item with an unknown name (span: the name),
  per repeated name after its first occurrence (span: the repeat), per
  suffixed value or value above 2^31 − 1 (span: the value). An item with
  several defects gets one diagnostic, for the first in that order (name,
  repeat, value). The checks
  run for every module, imported ones included, so every command that
  resolves reports them.
- `specs/initial/AI_Native_Systems_Language_SPEC.md` §7: the paragraph
  **7.6 Declared budgets** (spec §2 to R5 in normative prose, naming
  `BUDGET_INVALID` and `BUDGET_EXCEEDED`). Because
  `TestRegistryInventory` registers every backquoted code it reads, the
  paragraph names `BUDGET_EXCEEDED` only in phase 3; in this phase it
  states §2 and R2 and refers to "the budget diagnostic of phase 3" in
  plain words.
- `docs/ownership.md`: **Rule 7.6 — Declared budgets**, with
  `Diagnostics: BUDGET_INVALID` (extended in phase 3).
- Fixtures: `tests/negative/budget_invalid.kern` (`//! expect: reject
  BUDGET_INVALID rule=7.6`, exactly one bad item) and its twin
  `budget_invalid_ok.kern`.

**Tests.** A resolver unit test per R2 semantic case (unknown name,
repeat, `0u64`, `2147483648`, `2147483647` accepted, an unknown name
with a suffixed value giving one diagnostic), one diagnostic per item,
imported module reported; the fixture pair; registry tests.

**Acceptance.** Gates pass; `TestRegistryCoverage`,
`TestRegistryInventory` and `tests/rules_lock_test.go` pass.

**Risks.** The resolver may stop at the first error in some passes;
the tests assert several `BUDGET_INVALID` in one file are all reported.

## Phase 3 — Counting and `BUDGET_EXCEEDED` (R3, R4, the evaluation half of R6)

**Goals.** A library function that, given a resolved program and its
show module, returns the budget diagnostics; the fixture harness uses it.

**Files.**
- `compiler/analysis/budget.go`:
  - `HasBudgets(files []*ast.File) bool`: any `FuncDecl` with a non-nil
    `Budget` in the given files. Callers pass the parsed request files
    (after project discovery: the entry files, their imported user
    modules and, for `check`, test files), never the standard library or
    the prelude, so only a file of the request triggers lowering (R6).
  - `Budgets(prog *resolve.Program, srcs []*source.File, mod *ir.Module, target string) []diag.Diagnostic`:
    computes `BoundsChecks(mod)`, `Copies(mod, target)` and
    `Allocations(mod)` once each; for every budgeted declaration with a
    valid item (phase 2 has passed, so items are valid), counts the
    entries of spec §2 (same file name, `loc.span.start` within
    `[decl.Pos.Start, decl.Pos.End)` of the declaration; a budget or
    contract block at the front contains no entries, so starting the
    span at `Pos` is equivalent to starting at `func`), filters
    `bounds_checks` to `kind == "bounds" && label == "runtime-checked"`,
    and for each item over its value V emits `BUDGET_EXCEEDED` per R4:
    the counted entries sorted by (file, span start), span the entry at
    position V + 1 (`sorted[V]`), related the budget item then every
    other counted entry in that order, `Fix` nil (serialized `null`),
    message built by `budgetMessage`.
  - `budgetMessage`: `function \`NAME\` has N <noun>, budget V: ` then
    `line:col \`text\`` joined by `, `, at most 10, then `and K more`;
    nouns `runtime-checked bounds checks`, `copies`, `allocation sites`
    (singular for 1); text whitespace runs collapsed to one space, cut to
    40 code points plus `...`. Line and column are 1-based, column in
    code points, computed from `srcs` bytes.
  - Diagnostics sorted with `diag.Sort`.
  - Method names: an `impl` method is named `Type.method` as
    `show-*` labels and existing diagnostics name it (follow
    `analysis` naming helpers).
- `compiler/diag/codes.go`: `"BUDGET_EXCEEDED": {"7.6", CatLang, 0,
  "a function has more runtime-checked bounds checks, copies or allocation sites than its budget"}`;
  regenerate `spec/rules.md`; `spec/rules.lock`.
- `tests/harness_test.go` `checkFiles`: when the check is clean and
  `analysis.HasBudgets` over the fixture's files, lower (`lower.Lower`) and optimize
  (`opt.Run(mod, opt.OptionsFor("debug"))`) and append
  `analysis.Budgets(...)`. This mirrors `Build` without importing
  `cmd/kern`.
- Spec §7.6 paragraph: replace phase 2's plain-words reference with
  `BUDGET_EXCEEDED` and add R3, R4 and R6 in normative prose; R5 (which
  commands enforce) is added in phase 4 with the wiring.
  `docs/ownership.md` Rule 7.6: the same, and its Diagnostics line lists
  both codes.
- Fixtures: `tests/negative/budget_exceeded.kern` (one function with
  `budget { bounds_checks 0 }` and one midpoint index, exactly one
  diagnostic) and twin `budget_exceeded_ok.kern` (the same with the
  guard that proves it, per `docs/bounds-checks.md`).

**Tests** (`compiler/analysis/budget_test.go`, building programs through
resolve, check, lower and `opt.Run` at `debug` and `release`):
- each item exceeded and met; zero entries; count equal to value passes;
- R3 equality, over the programs that resolve and check cleanly (so a
  show module exists): the accepting budgeted fixtures, the
  `BUDGET_EXCEEDED` reject fixture and the unit cases; for each, the
  count equals the corresponding show result filtered by §2, at both
  levels. `SYNTAX` and `BUDGET_INVALID` cases are tested only through
  their expected diagnostics;
- the level difference: a pinned unit case whose filtered
  `show-bounds-checks` count differs between `debug` and `release` (the
  test asserts the difference first). If no such program exists, which
  is possible because a helper's own body stays in the module, the
  phase records that in the commit message and phase 4 asserts the
  level by the hook's received `opt` argument instead;
- attribution: a library `Vec.push` allocation in the caller; a helper's
  unproven index attributed to the helper with the helper inlined in
  release, and the caller's budget unaffected; a closure's index in the
  enclosing function; an uninstantiated generic with a budget passes;
  an instantiated one counts its instantiations' entries at its own
  locations; an `impl` method;
- R4: with budget 0 the span is the first entry, and with three entries
  against budget 1 it is the second; related order and labels; `fix` is
  `null` in the JSON encoding (a `diag.Write` round trip); message text
  for 1 entry, for 12 entries (10 listed and `and 2 more`), a span over
  40 code points with non-ASCII text, a span with a line break;
- several functions and items over budget in one program give several
  sorted diagnostics.

**Acceptance.** Gates pass; registry tests pass with both codes.

**Key decisions.** The evaluation takes the module as an argument, so
`check`, `build` and the tests share one implementation and the
lowering choice stays with the caller.

**Risks.** `loc.File` must equal the AST file name; the tests compare
across multi-file programs (a directory fixture) to catch a path
mismatch. Copies' `target` argument does not change which entries
exist; the equality test runs `Copies` with the host triple as well.

## Phase 4 — Commands (R5, R6)

**Goals.** `check` and `build` enforce; no other command does; no cost
without budgets.

**Files.**
- `cmd/kern/run.go`: `type lowerFunc func([]*source.File, string) (*ir.Module, []diag.Diagnostic, error)`
  and `budgetDiags(srcs []*source.File, prog *resolve.Program, opt, target string, lower lowerFunc) ([]diag.Diagnostic, error)`.
  The production callers (`check`, `build`) pass `Build`; tests pass a
  counting wrapper, so there is no new package-level state. `Build(srcs,
  opt)` takes no target, because the show module does not depend on
  one: the show queries pass `req.Target` only to `analysis.Copies`, and
  `budgetDiags` does the same through `analysis.Budgets(..., target)`,
  so counts and show results use the same target by construction.
  `budgetDiags` returns nil without calling `lower` when no request file
  has a budget (`analysis.HasBudgets` over the parsed `srcs`); otherwise
  `lower(srcs, opt)` and `analysis.Budgets(..., target)`, the
  target being the command's resolved `req.Target` (CLI, request or
  manifest, else the host). Result contract of `lower`, as `Build`
  defines it: diagnostics are resolution or checking errors, which
  cannot occur here because `budgetDiags` runs only after a clean check;
  if they do (the two runs disagree), `budgetDiags` returns them
  unchanged without evaluating budgets and the caller reports them like
  any diagnostics (exit 2). An error, or a nil module with neither, is
  returned as the `InternalError` the caller already handles (exit 3),
  as in `showBoundsChecks`, wrapping the cause as
  `fmt.Errorf("budget evaluation: lowering: %w", err)`. Tests with an
  injected `lower` cover each of the three outcomes and check
  `errors.Is` on the wrapped cause.
- `cmd/kern/main.go` (`check`): the check branch moves into
  `runCheck(req, wire, srcs, human, stdout, stderr, lower lowerFunc) int`,
  called by the dispatcher with `Build`; tests call `runCheck` with a
  counting `lower`. After the existing diagnostics (both the
  `checkHook` path and the `incremental.Check` path) and the test-file
  static diagnostics, when there are no errors: parse the request files
  (`parser.Parse`, no resolution) and test `HasBudgets` on the trees;
  only when it holds, resolve once (`resolve.ResolveFor`, at the
  command's level) and append `budgetDiags`. A request without budgets
  therefore costs one parse of its files and no resolution or lowering,
  on both the `checkHook` and the incremental path; a test on the
  incremental path with a counting `lower` asserts zero calls. Human and
  JSON rendering and exit codes are unchanged otherwise. (`HasBudgets`
  takes `[]*ast.File`, so the same trees serve both decisions.)
- `cmd/kern/build.go`: the sequence becomes load → resolve and check
  (stop with those diagnostics, exit 2, as today) → `budgetDiags` at the
  build's `--opt` and target (stop with them, exit 2, writing no output
  file; an internal error exits 3) → compile and write the output as
  today. A program with no budget skips the middle step, so its build
  is unchanged.
- `run`, `test`, the show queries and the rest: untouched.
- Spec §7.6 and `docs/ownership.md` Rule 7.6: add R5.

**Tests** (`cmd/kern/cli_test.go` or a new `budget_test.go`):
- `check` on an over-budget file: exit 2, JSON `BUDGET_EXCEEDED` with
  related spans; `check --human` prints one line whose message lists
  every location (and the first 10 for a function with 12);
- `check --opt=release` counts at release, and `check` without `--opt`
  at `debug`: on phase 3's level-difference case, with a budget between
  the two counts, so the outcomes differ (or, if phase 3 found no such
  case, by the `opt` argument the hook receives);
- `check` with a cache directory (incremental path) reports the same;
- `build` fails, exit 2, and the `-o` path does not exist afterwards;
  `build` of a budgeted program with a type error reports only the type
  error; `build --target` of another triple counts with that target;
- non-enforcement, over the same over-budget program, a table of every
  other command: `run` executes and prints its output; `test` runs its
  test functions (a `*_test.kern` beside it); `show-bounds-checks`,
  `show-copies`, `show-allocations`, `show-layout`, `show-effects`,
  `explain-ownership` (with a span), `emit-ir` and
  `apply-structured-edit` (a no-op edit) return `ok: true`; `format`
  succeeds; `emit-asm` too, skipped without clang. None reports
  `BUDGET_EXCEEDED`;
- `check` on a budgeted program that also has a type error reports the
  type error only;
- R6: `runCheck` with a counting `lower` (JSON and `--human`), on every
  fixture
  under `tests/` that has no budget and on a program importing a `std`
  module: zero calls. Byte-for-byte compatibility is verified once
  against a real pre-change executable: before editing, build `kern`
  at the phase-3 commit and record, for every fixture under `tests/`
  without a budget, stdout and exit code of `check` and `check --human`
  (a script in the scratchpad, not committed); after the phase, rerun
  with the new binary and diff. The commit message states the number of
  files compared and that the diff is empty. The existing
  `TestHumanRenderingUnchanged` and JSON golden tests keep passing;
- `check` on a project whose test file (`*_test.kern`) holds a budgeted
  function counts it (check loads test files).

**Acceptance.** Gates pass, race included.

**Risks.** Resolving twice in the incremental path costs time; measure
`kern check` on `bench/kern` before and after (no budgets: no change
expected beyond noise; the hook counter is the real assertion).

## Phase 5 — Reference and documentation (rest of R7)

**Files.**
- `docs/bounds-checks.md`: a section "Declaring a budget" — a
  `bounds_checks` budget turns these rules into a check error naming
  each runtime-checked location; the example from the spec.
- `docs/ownership.md` Rule 7.6: final text (attribution, commands, the
  `--opt` level), consistent with the spec paragraph.
- `docs/editors.md`: one line that the language server does not report
  budgets yet (spec §7 question 1).

**Tests.** `go test ./tests` (citations, docs lint, registry inventory).

**Acceptance.** Gates pass; `realitycheck` on spec/plan against the
changed packages reports no drift.

## Phase 6 — Budgeted suite and offline evaluation (spec §5)

**Files.**
- `bench/agent/budgetize/budgetize.go` (package `budgetize`, importable):
  `Source(name string, src []byte) ([]byte, error)` inserts
  `budget { bounds_checks 0 }` on its own line before the `func` keyword
  of every function declaration without a budget (top level and in
  `impl`, after any contract), using the Go parser's positions, then
  formats the result; a file that does not parse is an error. `Suite(from,
  to string) error` copies every task of `from` to `to`, applies `Source`
  to every `.kern` file of `start/` and `reference/`, appends to
  `task.md` the sentence "The program declares its bounds-check budgets;
  `check` reports any function that misses one.", and copies
  `hidden.json` and `calibration/` byte for byte. Safety, with no option:
  it canonicalizes `from` (`filepath.Abs`, then `filepath.EvalSymlinks`)
  and `to` as `filepath.Join(canonical parent of to, filepath.Base(to))`,
  and refuses, before writing anything, when those two are equal or
  either contains the other (decided with `filepath.Rel`, not string
  prefixes; siblings such as `bench/agent/tasks` and
  `bench/agent/tasks-budgeted` pass), or
  when `filepath.Base(to)` is not `tasks-budgeted` (tests use
  `t.TempDir()/tasks-budgeted`), when `to` is a symbolic link, or when
  `to + ".new"` or `to + ".old"` already exists (left by a failed run;
  the error says to remove it). It generates into `to + ".new"`; on
  success it renames an existing `to` to `to + ".old"`, renames
  `.new` to `to`, and removes `.old`. If the second rename fails it
  renames `.old` back and returns the error; if that also fails it
  returns both errors (`errors.Join`, each wrapped with the operation
  and both paths, `%w`) and leaves `.old` in place for the user. Every
  other error is wrapped with the operation and the file it concerns. Any
  generation error removes `.new` and leaves `to` untouched. This is a
  developer tool run by hand, so no further recovery is attempted. Idempotent:
  `Source` of its own output is unchanged, and a second `Suite` run
  produces identical files.
- `bench/agent/tools/budgetize/main.go`: `go run ./bench/agent/tools/budgetize`
  calls `Suite("bench/agent/tasks", "bench/agent/tasks-budgeted")` from
  the repository root; flags `-from`, `-to` override them.
- `bench/agent/tasks-budgeted/` (generated, committed).
- `bench/agent/harness/budgeted_test.go` (package `main`, so it calls
  the harness's own dry-run validation function directly):
  - it finds exactly ten tasks in `bench/agent/tasks-budgeted` and fails
    otherwise;
  - every reference checks clean with `kern check` and prints its
    expected output;
  - every start, and every natural solution after copying it to a
    temporary directory and applying `budgetize.Source`: run
    `kern check` and `kern show-bounds-checks` on the same files; assert
    every diagnostic is `BUDGET_EXCEEDED`, and that their counted
    locations (primary plus related entries, excluding the budget item)
    equal, as a set of file and span, the `runtime-checked` `bounds`
    entries; assert `check` exits 0 exactly when that set is empty;
  - every natural solution has at least 3 such locations (so it fails);
  - `budgetize.Source` unit tests: a contract, an `impl` method, an
    already-budgeted function left alone, idempotence, a parse error;
  - `budgetize.Suite` safety tests: equal, nested and wrongly named
    destinations, a destination reached through a symlinked parent that
    aliases the source, and a symlinked destination are refused with
    neither tree modified; a sibling destination (the default layout, in
    a temporary directory) succeeds, and a second run is byte-identical; a generation failure
    (an unparsable start in a temporary suite) leaves an existing
    destination unchanged and no `.new` behind;
  - the dry-run validation passes on the new suite;
  - `budgetize.Suite("bench/agent/tasks", t.TempDir()+"/tasks-budgeted")`
    produces a tree byte-identical to the committed
    `bench/agent/tasks-budgeted`, so the committed suite is current; the
    test never writes the committed tree (in-place regeneration is the
    developer's `go run ./bench/agent/tools/budgetize`).
- Harness: `-dry-run -tasks-dir bench/agent/tasks-budgeted` also passes
  from the command line, recorded in the report.
- `docs/reports/declared-budgets.md`: what was built, the offline
  results per task (each start's and natural solution's number of
  counted locations, as measured, with the spec §5 background counts
  for comparison;
  first message as an example), and that a paid plain-arm run is the
  owner's decision, with its command. If such a run happens, the report
  gains its results: the budgeted plain arm's successes and median
  iterations per task, compared with the four-arm report's plain and
  `documented` arms for the same model.

**Acceptance.** Gates pass; the dry run passes; the report exists.

**Risks.** Six starts fail their budgets by design (spec §5); the test
compares them with the show query, not with the dated counts, which the
report lists. The tool path is `bench/agent/tools`,
outside the self-hosted parser's corpus (`bench/kern`), so the generated
`.kern` files do not enter it.

## Review and commit (each phase)

`prism review staged` with default settings (Wafer; if Wafer still has no
credit, `--provider openai --model gpt-6.1-sol`, said in the commit
message), fix high findings, commit on `main`. Not pushed unless the
owner asks.
