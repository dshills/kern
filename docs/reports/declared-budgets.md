# Declared budgets: what was built, and the budgeted suite offline

**Date:** 2026-11-02. **Spec:** `specs/declared-budgets/SPEC.md`.
**Plan:** `specs/declared-budgets/PLAN.md`, six phases, each reviewed and
committed on `main` (b612273, 7bfe81e, ce4a3ac, 3846045, 860ef35, and
this phase). **No model was called:** the budgeted suite is evaluated
offline here, and a paid run is the owner's decision (below).

## Why

The four-arm experiment (`docs/reports/agent-four-arms.md`) found that on
suite 4 the plain arm, with text diagnostics and nothing else, failed 85
to 96% of its trials, mostly by replying DONE on a correct program that
still had runtime-checked bounds checks. Nothing it received said which
checks remained. The arms that succeeded were told what the compiler
knows, by a query tool or by a document. A budget declared in the source
puts that fact in the one tool every agent already uses: `kern check`
fails, and its message names every location.

## What was built

```kern
budget { bounds_checks 0; copies 1; allocations 0 }
func rank(xs: view[]u64, key: u64) -> usize { ... }
```

- **Syntax** (phase 1). A budget block goes between a function's contract
  block, if any, and `func`, at top level or in `impl`. `budget` is not
  a keyword. Both parsers accept it, the compiler's and the self-hosted
  one in `examples/selfhost_parser`, and the formatter prints it on one
  line.
- **`BUDGET_INVALID`** (phase 2, rule 7.6): an unknown item, an item
  given twice, a suffixed value or one above 2^31 − 1, one per item, from
  every command that resolves the program.
- **Counting and `BUDGET_EXCEEDED`** (phase 3). Each item counts the
  function's entries of `show-bounds-checks` (runtime-checked bounds
  checks only), `show-copies` or `show-allocations`, so a count is what
  the matching query reports for that function. Over budget is one error
  per item, at the first entry over the budget, and its message lists
  the locations as `line:column` and their source text, because the
  plain-text rendering prints no related spans.
- **Commands** (phase 4). `check` and `build` enforce (exit 2; `build`
  writes nothing) at their `--opt` level, `debug` by default; `run`,
  `test` and every query do not. A request without a budget block is
  only parsed once more: `check` output on the 452 budget-free files
  under `tests/` is byte-identical to the previous binary's.
- **Documentation** (phase 5): spec §7 paragraph 7.6,
  `docs/ownership.md` Rule 7.6, and "Declaring a budget" in
  `docs/bounds-checks.md` with a worked example.

## The budgeted suite

`bench/agent/tasks-budgeted` is generated from suite 4 by
`go run ./bench/agent/tools/budgetize` (package `bench/agent/budgetize`).
Every function of every start and reference file gets
`budget { bounds_checks 0 }`; each `task.md` gains the sentence "The
program declares its bounds-check budgets; `check` reports any function
that misses one."; `hidden.json` and `calibration/` are copied byte for
byte, so the hidden tests are suite 4's and removing a budget block does
not help a trial pass. The generated tree differs from suite 4 only by
those lines.

The offline test is `bench/agent/harness/budgeted_test.go`. It checks the
ten tasks:
- every reference checks clean with its budgets and prints its expected
  output;
- every start that checks clean without its budgets, and every natural
  solution with budgets added in a temporary directory, gets from
  `kern check` only `BUDGET_EXCEEDED` errors whose locations are exactly
  the runtime-checked bounds checks `kern show-bounds-checks` lists for
  the same files (as a set of file and span), and passes exactly when
  there are none;
- t10's start, which does not check without budgets (a review task with
  unsafe operations outside `unsafe`), reports exactly its own errors
  and no budget error, since budgets are evaluated only after a clean
  check (spec §5 was amended for it while building this phase: it had
  said every start reports budget errors only);
- the harness's dry-run validation passes on the new suite, and
  regenerating the suite into a temporary directory reproduces the
  committed tree byte for byte.

The dry run passes from the command line too:
`go run ./bench/agent/harness -dry-run -tasks-dir bench/agent/tasks-budgeted`
(exit 0, and suite 4's own dry run still passes). Its size rule (60 to
160 lines per file) now leaves out lines that hold only a budget block,
which three budgeted files would otherwise have exceeded (`mat.kern`
reference, `buf.kern` start and reference); suite 4 has no such lines,
so its calibration is unchanged.

### Measured, 2026-11-02

Locations are the runtime-checked bounds checks `kern check` names, at
`debug`. The spec's §5 counts, taken earlier the same day with
`show-bounds-checks`, are in brackets.

| task | kind | start: locations (errors) | natural solution: locations (errors) |
|---|---|---|---|
| t01 sorted_set | implementation | 0, passes | 9 (5) |
| t02 byte_stats | implementation | 0, passes | 6 (5) |
| t03 ring_log | implementation | 0, passes | 7 (6) |
| t04 image_ops | optimization | 15 (4) [15] | 14 (4) |
| t05 range_sums | optimization | 9 (5) [9] | 8 (5) |
| t06 text_tools | optimization | 8 (5) [8] | 7 (5) |
| t07 heap_queue | debugging | 9 (4) [9] | 9 (4) |
| t08 top_scores | debugging | 9 (5) [9] | 9 (5) |
| t09 matrix_helpers | refactoring | 17 (9) [17] | 8 (7) |
| t10 buffer_review | review | does not check (unsafe errors), no budget error | 6 (6) |

Every natural solution, the program a plain agent typically wrote and
declared done in the four-arm runs, now fails `check` with at least 6
named locations. The first `check` a plain-arm agent would run on a
natural t02 solution prints:

```
stats.kern:339-355: error [BUDGET_EXCEEDED rule 7.6]: function `histogram` has 1 runtime-checked bounds check, budget 0: 10:9 `counts[usize(b)]`
stats.kern:690-713: error [BUDGET_EXCEEDED rule 7.6]: function `digit_counts` has 1 runtime-checked bounds check, budget 0: 20:13 `digits[usize(c - 48u8)]`
stats.kern:1070-1082: error [BUDGET_EXCEEDED rule 7.6]: function `most_frequent` has 1 runtime-checked bounds check, budget 0: 31:24 `counts[best]`
stats.kern:1432-1444: error [BUDGET_EXCEEDED rule 7.6]: function `top_digit` has 1 runtime-checked bounds check, budget 0: 42:24 `digits[best]`
stats.kern:2241-2265: error [BUDGET_EXCEEDED rule 7.6]: function `final_run` has 2 runtime-checked bounds checks, budget 0: 70:13 `data[len(data) - 1usize]`, 73:25 `data[i - 1usize]`
```

What this shows offline is only that the plain arm can no longer finish
with a clean `check` on a program that misses the hidden budget, and
that it is told each site. It does not show that an agent then knows how
to make a site proven; in the four-arm runs that knowledge came from the
query tool's labels or from `docs/bounds-checks.md`, neither of which the
plain arm has.

## A paid run

Whether to run it is the owner's call; nothing here spent model credit.
The comparison it would make: the plain arm on the budgeted suite
against the four-arm report's plain arm (2 of 50 for DeepSeek-V4.1-Flash,
6 of 41 for Kimi-K3) and `documented` arm (44 of 50, 37 of 41) on suite
4, same models, same budgets (20 iterations, 30 minutes). It needs Wafer
credit, which ran out on 2026-11-01:

```bash
go run ./bench/agent/harness -tasks-dir bench/agent/tasks-budgeted -arms plain -models wafer:DeepSeek-V4.1-Flash,wafer:Kimi-K3 -effort medium -trials 5 -parallel 6 -call-timeout 15m -out docs/reports/declared-budgets-run.md
```

If it runs, this report gains the budgeted plain arm's successes and
median iterations per task beside those two arms.
