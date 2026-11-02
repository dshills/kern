# Declared budgets: bounds checks, copies and allocations that `check` enforces

**Version:** 0.1 (2026-11-02). **Scope:** the grammar (`spec/grammar.ebnf`),
the parser, AST and formatter (`compiler/parser`, `compiler/ast`,
`compiler/format`), a budget evaluation over the lowered module
(`compiler/analysis`), the `check`, `build`, `run` and `test` commands
(`cmd/kern`), the diagnostic registry (`compiler/diag/codes.go`, the
generated `spec/rules.md`, `spec/rules.lock`), the language specification
(`specs/initial/AI_Native_Systems_Language_SPEC.md` §7), the reference
(`docs/ownership.md`, `docs/bounds-checks.md`), tests, and an offline
evaluation of a budgeted copy of agent suite 4. **Language impact:** one
optional declaration block before a function; programs without one are
unaffected. **Wire impact:** two new diagnostic codes; no new result type
and no schema version.
**Related:** `docs/reports/agent-four-arms.md` (the experiment this
answers); `specs/agent-suite-v4/SPEC.md` (the suite, its natural
solutions and calibration); spec §16 (`show-bounds-checks`,
`show-copies`, `show-allocations`).

**Review status.** speccritic (openai gpt-6.1-sol, with
`specs/agent-suite-v4/SPEC.md` as context), four rounds, ending
VALID_WITH_GAPS with no critical finding. Round one's critical finding
(the natural solutions must check cleanly and run for the suite's
calibration, so they cannot carry budgets) is resolved in §5; the
warnings on separators, coexisting errors, the 10-entry limit, snippet
whitespace and truncation, and the calibration mapping are folded in, and
`run`/`test` no longer enforce budgets (§0.1). Kept as written: R6's "does
not lower", an observable cost property the tests assert; the preflight
warnings that read R1 to R8 as undefined acronyms.

**Glossary.** R1 to R8: the requirements of §4. Budget block: the declaration
of §3. Budget item: one line of it. Budgeted function: a function
declaration with a budget block. Entry: one element of the `checks`,
`copies` or `sites` array of a show query's result. Show module: the IR
module `Build(files, opt)` returns, the one the three show queries report
on. Runtime bounds check: an entry of `show-bounds-checks` with `kind`
`bounds` and `label` `runtime-checked`. Abbreviations: AST, abstract syntax
tree; IR, intermediate representation; LSP, the language server; CLI, the
`kern` command line. In the rendering `file:start-end: error [CODE rule
R]: message`, CODE is the diagnostic code and R its rule number.

## 0. Goals

A requirement stated in terms of what the compiler does ("no
runtime-checked bounds checks in this function", "no copies", "no
allocation") is written in the source, next to the function it
constrains, and `kern check` reports a violation as an ordinary error
that names each offending location. An agent, a reviewer or a build then
learns of a missed requirement through the tool it already uses, with no
query tool and no document.

The four-arm experiment found that agents without the compiler's
bounds-check facts wrote correct programs, believed them finished and
said so, and failed 85 to 96% of suite 4's trials. A budget turns that
silent failure into a check error.

## 0.1 Non-goals

- Transitive budgets. A function's budget counts the entries located in
  its own declaration, not those of the user functions it calls (§2).
- Budgets on other kinds of entries: `generation`, `disjoint` and
  `overflow` checks, traps, effects, stack size, code size.
- Module-level or program-level budgets.
- Language server diagnostics for budgets (§7, open question 1).
- A fix: the compiler does not rewrite code to meet a budget.
- Proving more checks. The proof pass is unchanged; a budget only reports
  what it proves.

- Budget enforcement in `run` and `test` (R5): they execute the program,
  and an agent working toward a budget needs to see the output of a
  program that does not meet it yet.

## 1. Background

The three show queries already compute every fact a budget needs, over
the show module. `show-bounds-checks` lists each bounds, generation,
disjoint and overflow check with `label` `proven` or `runtime-checked`;
`show-copies` lists aggregate copies and explicit deep copies;
`show-allocations` lists allocation sites. Each entry's `loc` is a source
location in user code: the entries of library functions and of inlined
copies are reported at the user-level location that performs them, and a
location reported more than once is listed once (a location is
`runtime-checked` when any occurrence is). The agent harness scores suite
4's budgets from exactly these results (`max_runtime_bounds_checks`,
`max_copies`, `max_allocations` in `hidden.json`).

`kern check` today resolves and checks; it does not lower. `build`, `run`
and `test` lower. The plain-text rendering (`check --human`) prints one
line per diagnostic, `file:start-end: error [CODE rule R]: message`, and
does not print related spans, so a plain-text reader sees only what the
message says.

## 2. Attribution

An entry belongs to a function declaration when the entry's `loc.file`
is the declaration's file and the entry's `loc.span.start` lies within
the declaration's span, from the first byte of `func` to the closing `}`
of its body. Kern has no nested function declarations, so an entry
belongs to at most one declaration; entries inside a closure belong to
the declaration containing the closure.

The following consequences of this rule are required and tested (R8):
- A call to a library function that allocates is an allocation entry at
  the call, in the caller.
- A bounds check in a user helper is an entry at the helper's index
  expression and belongs to the helper, whether or not the release
  inliner copies the helper into the caller; when an inlined copy is
  unproven the location is `runtime-checked` (§1), in the helper.
- A generic function's entries are those of its instantiations, at its
  own locations; a generic function the program never instantiates has
  no entries.

## 3. Syntax

```ebnf
func_decl     = [ contract ] [ budget ] "func" ident ...   (* the rest unchanged *)
budget        = "budget" "{" [ budget_item { sep budget_item } [ sep ] ] "}" ;
budget_item   = ident int_lit ;   (* ident: bounds_checks, copies or allocations (R2) *)
```

The parser must recognize `budget` as a block only at the start of a
declaration (top level or inside `impl`) when the next token is `{`; no
other construct begins that way, so `budget` is not reserved: `budget`,
`copies` and the other item names remain ordinary identifiers everywhere
else (a function named `budget` exists in
`tests/run/vecin_guard.kern` and keeps working). The integer literal has
no type suffix. The separator after the last item is optional, and an
empty block is accepted (it constrains nothing). Example:

```kern
budget { bounds_checks 0; allocations 0 }
func rank(xs: view[]u64, key: u64) -> usize { ... }
```

## 4. Requirements

**R1. Parsing.** The parser accepts the block of §3 and records each item
with its name, value and span on the function declaration. The AST dump
shows the block. The formatter prints it on its own line before `func`
(after a `contract` block), items in source order separated by `; ` with
no separator after the last, and formatting is idempotent. A block that
contains a comment is laid out one item per line instead, as any brace body
containing a comment (`spec/format.md`), so the comment keeps its place.

**R2. Well-formedness.** What the grammar of §3 rejects is a `SYNTAX`
error, as for any other declaration: a budget block before anything but
`func` (a `contract`, struct, union, interface, `impl`, constant,
thread-local or `extern` declaration, or the end of the file), and an
item that is not an identifier followed by an integer literal (`n`,
`-1`). This keeps the parser's acceptance a property of the grammar, which
the self-hosted parser (`examples/selfhost_parser`) must reproduce on
every file under `tests/`. Each of the following is `BUDGET_INVALID`, one
diagnostic per offending item, reported by every command that resolves
the program:
- an item name other than `bounds_checks`, `copies` and `allocations`;
- the same item twice in one block;
- a value with a type suffix (`0u64`), or one that exceeds 2^31 − 1.

**R3. Counting.** For a budgeted function F and each item in its block,
the count is the number of entries belonging to F (§2) in the show
module for the command's files, optimization level and target:
- `bounds_checks`: runtime bounds checks of `show-bounds-checks`;
- `copies`: entries of `show-copies`;
- `allocations`: entries of `show-allocations`.

The count equals, by construction, what a client gets by running the
corresponding show query with the same files, `--opt` and target and
filtering its entries by §2; a test enforces the equality (R8).

**R4. Violation.** When an item's count exceeds its value, the command
reports one `BUDGET_EXCEEDED` error for that item of that function:
- `span`: the first entry over budget: with value V, the entry V + 1 in
  source order;
- `related`: the budget item (label `budget declared here`) and every
  other entry counted, in source order (label `runtime-checked bounds
  check`, `copy` or `allocation site`);
- `message`: the function, the item, the count and the value, then each
  counted entry as `line:column` with the source text of its span
  (each run of whitespace, line breaks included, replaced by one space,
  then, when longer than 40 Unicode code points, its first 40 followed
  by three ASCII dots `...` not counted in the 40), at
  most 10
  entries followed by `and N more`. Example: ``function `rank` has 2
  runtime-checked bounds checks, budget 0: 14:12 `xs[mid]`, 19:12
  `xs[lo]` ``;
- `fix`: null.

A count at or under its value reports nothing. The message carries the
locations because the plain-text rendering prints no related spans (§1).

**R5. Commands.** `check` and `build` must evaluate budgets and fail
with exit code 2 on a `BUDGET_EXCEEDED`; `build` then writes no output
file. Every other command must not evaluate them: `run` and `test` (§0.1),
the show queries, `emit-ir`, `emit-asm`, `explain-ownership`,
`show-layout`, `show-effects`, `format` and `apply-structured-edit`, so a
client can always run and inspect a program that misses its budget.
`--opt` selects the level the counts are taken at; the default stays
`debug`.

**R6. Evaluation order and cost.** Budgets are evaluated only when the
program resolves and checks with no error diagnostic, since the show
module exists only then. A `BUDGET_EXCEEDED` therefore never appears
beside a resolution or checking error; several `BUDGET_EXCEEDED` errors
(for several items or functions) appear together, sorted as other
diagnostics are. When no file of the request contains a budget
block, `check` does not lower, and its output and exit code are
byte-identical to today's. `build` takes the counts from the show module
even where the module it compiles differs (release execution's library
inlining, `buildExec`). (Non-normative: the simplest implementation
lowers once more in release `build`, only for a program with a budget
block.)

**R7. Specification and reference.** Spec §7 gains a paragraph, rule
**7.6 Declared budgets**, stating §2 to R5; both codes carry rule 7.6
with category `lang` (a rejecting fixture under `tests/negative/` and an
accepting twin for each), and `spec/rules.lock` lists them.
`spec/grammar.ebnf` gains the productions of §3. `docs/ownership.md`
gains **Rule 7.6** restating it, and `docs/bounds-checks.md` says that a
`bounds_checks` budget turns its rules into a check error.

**R8. Tests.** At least:
- parse, dump and format round trips for every item, both separators,
  with and without `contract`, and inside `impl`;
- each R2 case, both the `SYNTAX` and the `BUDGET_INVALID` ones, and
  `budget` used as an ordinary identifier;
- the self-hosted parser accepting and dumping every budget form the Go
  parser does (its existing corpus test, over the new fixtures);
- each item exceeded and met, at `debug` and at `release`;
- the R3 equality: for every fixture with a budget, the count equals the
  filtered show-query result at both levels;
- attribution (§2): a library allocation in the caller, a helper's
  check in the helper with the helper inlined in release, a closure's
  entries in its enclosing function, an uninstantiated generic;
- R4's message: cut text, the 10-entry limit with `and N more`, and
  `check --human` printing the first 10 locations of a function with
  more;
- R5: `build` fails and writes nothing; `run` executes and
  `show-bounds-checks` answers for the same over-budget program;
- R6: `check` on a file with no budget block does not lower (asserted
  through the existing lowering hook or a counter), and its output on the
  existing fixtures is unchanged.

## 5. Evaluation

A copy of suite 4, `bench/agent/tasks-budgeted`, is produced by a script
from `bench/agent/tasks`: every function of every start and reference
file gets `budget { bounds_checks 0 }`, and each `task.md`
gains one sentence saying the program declares its budgets and `check`
reports a function that misses one. `hidden.json` is unchanged, so the
hidden tests are the same and removing a budget block does not help a
trial pass.

The natural solutions (`calibration/natural/`) are copied unchanged, with
no budget blocks, so the suite's calibration (which requires them to
check cleanly, run and print the expected output) holds as before.

Six of suite 4's starts (the rewrite and debugging tasks t04 to t09)
already contain runtime bounds checks (15, 9, 8, 9, 9 and 17 on
2026-11-02); t01 to t03 are stubs with none, and t10's start does not
check at all (it is the review task's starting defect). In the budgeted suite
those six starts fail `kern check` from the first call, which is the
intended effect: the agent is told every site to fix before it edits.

Offline, with no model calls, a Go test over the generated suite checks,
for each of the ten tasks (the test fails if it finds a different
number):
- every reference passes `kern check` with its budgets and still prints
  the expected output;
- t10's start, a review task whose start does not check without its
  budgets (unsafe operations outside `unsafe`), reports exactly its own
  errors and no budget error, since budgets follow a clean check (R6);
- every other start, and every natural solution with the script's budget
  blocks added in a temporary directory, gets from `kern check` exactly
  the runtime bounds checks `show-bounds-checks` lists for the same
  files, as the counted locations (file and span) of its
  `BUDGET_EXCEEDED` errors, and no other diagnostic: clean when there
  are none, failing when there are any. Every natural solution has at
  least 3 (suite 4's calibration), so each fails, and a plain-arm agent
  writing it would be told each site;
- the harness's dry-run validation passes on the new suite.

A paid run (plain arm, the models of the four-arm report) is the owner's
call and is not part of this specification. The report the evaluation
writes, `docs/reports/declared-budgets.md`, states the offline results
and, if a paid run happens, compares its plain arm with the four-arm
report's plain and `documented` arms.

## 6. Acceptance criteria

- R1 to R8 hold, with their tests passing under `go test ./...` and
  `go test -race ./compiler/... ./cmd/...`; `golangci-lint run ./...` is
  clean.
- `go test ./tests` passes, including the rules-lock and fixture
  registry tests.
- §5's offline checks pass for all ten tasks.

## 7. Open questions

1. **Language server.** `kern lsp` calls `check.Check` directly and would
   need to lower budgeted files on every change. Deferred until a
   measurement shows lowering one file is cheap enough at typing speed.
2. **Warnings.** A budget could be a warning in `check` and an error only
   in `build`. Rejected for 0.1: the experiment's failures were agents
   stopping on a clean check, and a warning is what they would skip.
