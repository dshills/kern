# Agent benchmark, fourth task suite

**Date:** 2026-10-30. **Spec:** `specs/agent-suite-v4/SPEC.md`.
**Plan:** `specs/agent-suite-v4/PLAN.md`. **Follows:** the third suite
(`docs/reports/agent-tasks-v3.md`), whose design idioms the proof pass of
`docs/reports/prove-guards.md` made provable. **Status:** written and
validated offline with `go run ./bench/agent/harness -dry-run`; **not
run.** No model was called, by the owner's standing decision that matrix
runs are theirs to order.

## Where a plain-arm agent still cannot know

The second matrix separated the arms only on bounding runtime-checked
bounds checks. The plain arm failed 10 of 100 trials there. Every one of
those failures was a correct program the agent had declared done. The
reason is in the reference both arms receive: `docs/ownership.md` says
nothing about which bounds checks the compiler proves, so a bounds budget
is the one requirement a plain-arm agent cannot derive. Before designing
anything, I measured whether that still holds after the proof pass grew,
and whether any other analysis could serve instead.

**Bounds checks: still undecidable by reading.** Fourteen common
idioms stay runtime-checked with the compiler at 54b0a39 (spec §1.2,
measured on probe programs). Each is safe by reasoning the pass does not
do:
- arithmetic: a midpoint, `n - 1 - i`, a window `i + k`;
- a value's range: a `u8` into a 256-entry table, `c - 48` under a
  digit test, `i % len`;
- a relation between variables: insertion sort's `j <= i`, argmax's
  `best`, an index a helper returned, a heap's checked child, two
  pointers, `len(ps) == len(xs) + 1`;
- the length's sign: `xs[len(xs) - 1]` under `len(xs) > 0`;
- a grid's shape: `g[y * w + x]` under `x < w && y < h`.

Each is proven by one rewrite: compute the index into a local and compare
that local against `len()` of the indexed place just before the access.
What an agent sees as obviously safe is exactly what it does not guard,
so what the compiler leaves checked is invisible without the query.

**The other analyses do not serve:**
- **Overflow checks** are never proven, so a reader can count them.
- **Copies, layout, effects and ownership** follow rules the reference
  states. The second matrix's tasks on them did not separate the arms.
- **Allocation sites** turned out to be incomplete (below), so a budget
  on them would measure the analysis's blind spot.

## Defects found on the way, not fixed here

- **`show-allocations` misses allocations inside std modules.** It
  matches a fixed table of core operations
  (`compiler/analysis/analysis.go`, `allocating`). Calls into `std/str`
  and `std/map` that allocate, such as `String.new`, `push_u64`,
  `Map.new` and `insert`, are not reported, and neither is
  `Vec.extend`.
- **`show-bounds-checks` misses library-internal checks.** A check
  inside `Vec.get` is not attributed to the call, so a loop over
  `v.get(i)` shows no check at all.
- **The third suite's prompts disagreed with its hidden suites.**
  f15f773 lowered every `hidden.json` budget to 0 but left eight prompts
  stating 1 to 6, counting `main`'s slices. A run would have failed
  agents that met the stated bound. Fixed here: the suite moved to
  `bench/agent/tasks-v3/` and its prompts state the hidden budget. Its
  dry run still passes.

## The suite

Ten tasks under `bench/agent/tasks/`, in the kinds §9 asks for. Every
budget is the reference's count, which is 0 in every task, since
`main`'s slices are proven now. The table counts runtime-checked bounds
checks.

| task | kind | start | reference | natural | qualifying sites by idiom |
|---|---|---|---|---|---|
| t01_sorted_set | implementation | 0 | 0 | 9 | transitive bound 5, midpoint 2, two pointers 2 |
| t02_byte_stats | implementation | 0 | 0 | 6 | argmax 2, byte into a table 1, last element 1, offset digit 1, transitive bound 1 |
| t03_ring_log | implementation | 0 | 0 | 7 | modulo 4, last element 1, length relation 1, window 1 |
| t04_image_ops | optimization | 15 | 0 | 14 | grid 12, mirrored index 1, modulo 1 |
| t05_range_sums | optimization | 9 | 0 | 8 | length relation 4, transitive bound 2, midpoint 1, window 1 |
| t06_text_tools | optimization | 8 | 0 | 7 | last element 2, mirrored index 2, two pointers 2, modulo 1 |
| t07_heap_queue | debugging | 9 | 0 | 9 | transitive bound 3, heap children 2, last element 2 (+2 other) |
| t08_top_scores | debugging | 9 | 0 | 9 | returned index 4, argmax 3, transitive bound 2 |
| t09_matrix_helpers | refactoring | 17 | 0 | 8 | transitive bound 4, grid 2, mirrored index 2 |
| t10_buffer_review | review | rejected | 0 | 6 | byte into a table 2, last element 2, modulo 2 |

An implementation task's start is stubs, so it fails on output. The
budget bites once the stubs are filled.

The two debugging tasks plant one-character or one-line bugs:
- **t07:** the larger child is picked in `sift_down`.
- **t08:** `argmax_from` starts its search at 0 instead of `from`.

t10's start is rejected because it does two raw operations outside
`unsafe` and leaves three contracts missing.

`main` gives every required function at least two inputs with different
results. t09's helpers `at` and `put` are the exception: they are new,
so they are reached only through the nine functions `main` calls.

## How the discrimination was checked

Each task has a **natural solution** (`calibration/natural/`), built by
spec R5's inspectable rules:
- guards only where correctness needs them;
- no index hoisted for the analysis's sake;
- no index loop turned into a `for` unless the position is unused;
- written from the prompt and start before any measurement.

The dry run now checks four things per task:
- the reference passes;
- the start fails;
- the natural solution passes every hidden check except the budget,
  validated with the budget removed so that no failure can hide behind
  another;
- the natural solution exceeds the budget by at least 3.

It also checks the audited idiom mapping (`calibration/idioms.json`,
every runtime-checked site mapped to an idiom). The audit requires at
least 6 qualifying sites, over at least 3 idioms, in at least 2 required
functions. It also checks each program's size, that no program imports
from `std/`, the reference's run time, and that the review task's
reference adds `unsafe`. All ten tasks pass every check.

**Frozen drivers.** `hidden.json` gained `frozen_functions`. The source
text of `main` and of every printing helper it uses must equal the
start's, so a solution cannot print the expected lines from a rewritten
driver. The second and third suites left that hole open and accepted it
in their reports. The frozen helpers are those that the start, reference
and natural solution all leave unchanged, and each prompt names them.

## What the calibration does and does not show

- **The natural solutions are written by a model that knew the idioms.**
  Three parallel workers wrote the tasks. R5's rules make every guard
  and every local inspectable, and I reviewed the solutions against
  them. What the calibration shows is that an agent which guards only
  what correctness needs fails at 6 to 14 sites per task. It does not
  show how often a plain-arm agent behaves that way. Only a matrix
  measures that.
- **A defensive plain arm can still pass.** An agent told "no
  runtime-checked bounds checks" that guards every index it writes
  meets the budget, as three of Claude's t03 trials did in the second
  matrix. Spreading the sites over many functions and over idioms that
  read as safe is what makes that luck harder. It does not make it
  impossible.
- **The optimization natural solutions stay near their starts.** For
  t04 to t06 the start already guards what a careful programmer
  guards. The natural solution's one change is a clamped loop bound
  rewritten as two conditions, so it sits one check below the start.
  That models an agent that fixes the visible problem and trusts the
  rest.
- **Thin margins.**
  - t02 and t10 have exactly the minimum of 6 qualifying sites.
  - t03's two sites `buf[log.next]` and `buf[log.next - 1usize]` are
    mapped as modulo. They are safe because every store to `next` is a
    remainder of `len(buf)`, a judgment a stricter reviewer could call
    "other", which would drop t03 to 5.
  - t07 maps two sites as "other": `h[0usize]` after `if len(h) ==
    0usize { return }`, and a computed slice. The first is itself an
    unproven habit (a positive-length test proves it, an early return
    on the zero test does not), which the catalog could add.
- **Two worker deviations.** In t09 the start and prompt gained two
  functions after the first natural solution measured too few sites,
  and the natural solution was rewritten the same mechanical way. In
  t10 one loop in `main` was rewritten (same output) because it alone
  was runtime-checked, so start, reference and natural solution share
  the new `main`. Both breach the plan's "prompt frozen before the
  natural solution" order; both are recorded here rather than hidden.

## The cheapest check, when the owner wants one

One task, one model, one trial per arm, two trials in all:

```
go run ./bench/agent/harness -tasks t01_sorted_set -models anthropic:claude-opus-5-5 -trials 1
```

t01 has nine sites over three idioms in five functions and starts from
stubs, so the plain arm's first program is its natural solution. A full
matrix (200 trials) is what the win rule needs.

## Reviews

- **speccritic:** INVALID on the first draft, on one critical finding:
  "natural" had no objective test, which R5's rules answer. Then
  VALID_WITH_GAPS with no critical finding.
- **plancritic:** three rounds of EXECUTABLE_WITH_CLARIFICATIONS, all
  folded in.
- **prism:** two passes. Fixed: t10's reference contracts did not
  allow the freed, zero-capacity block that `main` exercises. Left open
  by decision:
  - **Commented copies of a frozen function.** The concern is a copy of
    a frozen function hidden in a block comment. Kern has only `//` line
    comments, so a commented line can never begin `func main(`.
  - **Overflow on extreme inputs.** `i + k` before `% len` in t04 and
    t06, and a two-pointer sum in t01, can overflow for arguments near
    the type's maximum. Kern traps on overflow, so such inputs trap
    rather than answer wrongly. The frozen drivers never supply them,
    and the tasks' contracts are about indexing, not range.
- **realitycheck:** PARTIALLY_ALIGNED, score 100, no violations.
- **verifier:** no recommendation for the harness package. Its only
  entries name two stale worktrees under `.claude/`, outside this
  change.
- **Gates:** `golangci-lint run ./...` reports 0 issues and
  `go test ./...` is green. Both dry runs exit 0: the new suite's, and
  the third suite's with `-tasks-dir bench/agent/tasks-v3`.
