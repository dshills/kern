# Agent benchmark, second task suite

**Date:** 2026-10-28. **Follows:** `bench/agent/REPORT.md` (the full matrix
on the first suite: 0 wins of 10, every trial succeeding in both arms) and
the research report's conclusion that the next experiment needs tasks the
plain arm fails. **Plan:** PLAN.md 4.2, spec §2.3 and §9.

## Why the first suite could not separate the arms

Reading the first suite's ten tasks and the harness together:

- **The tasks were 15 to 40 lines and their fix was visible on reading.**
  Both models solved every task first time; the medians (3 iterations for
  Opus, 5 for GPT) are the models' habitual loop (read, write, check, run,
  DONE), not a property of the task. A win needs a strictly lower median,
  so a task both arms solve in one attempt is a loss by construction.
- **Where a task did bound something only a query measures (copies in
  t09, allocation sites in t05), the prompt named the cause and the site**
  ("`summarize` takes it by value"; "every iteration creates a fresh `Vec`
  and a `Box`"), so no query was needed to find it.
- **The plain arm's text already carries the code and rule number**
  (`error [ACC_CONFLICT rule 5.4.4]: ...`), so on diagnostics the arms
  differ only in related spans and fixes; the structured arm's JSON carries
  no `message` (schema 0.2), so on the message itself the plain arm has
  more. The queries are the only place the structured arm can gain.
- Both arms see the same `run` output and trap record, so a task whose
  failure is visible at run time (a trap, a wrong number) separates nothing.

## What the second suite does differently

Every task states a requirement in terms of what the compiler's analyses
report, in a program large enough (60 to 130 lines) that the sites are not
all visible at a glance, and the prompt states the requirement and the rule
behind it but not the sites. The hidden suite checks the requirement
through the same analyses. An agent with the queries measures; an agent
without them reasons from the language reference and cannot verify, so its
first "DONE" is a guess.

| task | kind | what the hidden suite bounds | start | reference |
|---|---|---|---|---|
| t01_copy_budget | refactoring | `show-copies` entries ≤ 1 | 6 copies of a 272-byte `Sample` (two by-value parameters, two by-value returns, a `for` over an array of samples, an element assignment); `main` reports two windows so a stub cannot print constants | 0 |
| t02_alloc_sites | optimization | `show-allocations` sites ≤ 1 | 4 (a `Vec` per word, its `push`, a `clone`, a `Box`) | 0 |
| t03_proven_checks | optimization | runtime-checked bounds checks ≤ 4 | 8 (`n := len(buf)` guards, `<=` guards, unguarded helpers) | 2 (the two slices in `main`) |
| t04_layout | optimization | `show-layout` sizes: `Entry` ≤ 48, `Header` ≤ 24 | 64 and 32 | 48 and 24 |
| t05_conflict_trace | debugging | check clean, sites ≤ 7, copies ≤ 4 | `PARAM_OWN_REQUIRED`, then an `ACC_CONFLICT` whose live binding is a `with` on `self.items.at(i)` around a call to `self.restock` | — |
| t06_unsafe_review | review | check clean | two wrappers with raw operations outside `unsafe`, then four `UNSAFE_MISSING_CONTRACT` | — |
| t07_move_trace | debugging | check clean, exact drop order | `OWN_CONDITIONAL_MOVE`, `OWN_USE_AFTER_MOVE`, three `OWN_PARTIAL_MOVE` (struct fields, a `with`-bound `Option`) | — |
| t08_lru_cache | implementation | sites ≤ 4 | three `TODO` methods of an LRU cache over `Pool[Entry]` | 4 |
| t09_parallel_count | implementation | sites ≤ 8, copies ≤ 3 | `TODO`: four scoped threads over four views, uneven split | 8 and 3 (the scope, four spawns, `fill`/`main`; three `SpawnErr` copies in `main`) |
| t10_tlv_decoder | implementation | runtime-checked bounds checks ≤ 5 | `TODO`: a type-length-value decoder | 4 (the four slices in `main`) |

The two bounds-check tasks rest on what the proof pass accepts
(`compiler/opt/prove.go`): a comparison of the index, or the index plus a
constant, against `len(x)` of the same sequence that dominates the access;
a constant index under a dominating length check; a `for` loop. What it
does not accept is what the structured arm sees and the plain arm must
discover by reasoning: a length copied into a local (`n := len(buf)`) is
not the same length, `<=` establishes nothing, facts do not survive a
join (`a && b` in a loop condition) or a call, and a store through an
edit access forgets what the guard established, so a guarded sequence of
writes proves only its first.

## Pilot, and the tightening it forced

A first pilot (t03, claude-opus-5-5, one trial per arm) succeeded in both
arms in 3 iterations, the plain arm in 20 s and the structured arm in
25 s. The plain arm met the budget at its first attempt by folding `rd16`
and `frame_ok` into the two loops, so that every index sat directly under
the loop guard and none of the pass's limits applied; the prompt had
stated the three proof forms, and the required functions named only the
three that `main` calls. The structured arm made the same rewrite and
spent its extra seconds on two `show_bounds_checks` calls.

So the prompts no longer state any rule (the proof forms in t03 and t10,
what counts as an allocation site in t02, Rule 4.1.5 in t04, "copies
where it only needs to look" in t01): the language reference every arm
receives is the rule's only source. And where the pass's limits are the
point, the helpers are required with their signatures: t03 keeps
`rd16(buf, at)` and `frame_ok(buf, at)` called as they are, so a proof
must be established inside each helper; t10's start declares `rd16`,
`rd32` and `byte_sum(buf, start, end)` as stubs that `decode` must read
through, where the `[start, end)` loop is the join case.

The kinds follow §9 (implementation ×3, debugging ×2, refactoring,
optimization ×3, unsafe-contract review), as the first suite did.

## Harness changes

- `hidden.json` gains `max_runtime_bounds_checks` (entries of
  `show-bounds-checks` of kind `bounds` labelled `runtime-checked`) and
  `max_type_size` (a list of `{type, bytes}` checked against
  `show-layout`) and `required_functions` (names that must appear among
  the functions `show-effects` lists, so that a solution cannot delete the
  functions a task asks to keep and print the expected lines directly)
  and `must_spawn` (functions `show-effects` must report as spawning, so
  that the parallel task cannot be solved sequentially);
  `no_unsafe_added` is now enforced (the line count of `unsafe` may not
  exceed the starting sources').
- The structured arm gains `show_bounds_checks` and `show_layout` beside
  `explain_ownership`, `show_copies` and `show_allocations`: the §7 query
  set the tasks' requirements are stated in. The plain arm is unchanged
  (text diagnostics, `run`, the files).
- `-tasks-dir` selects the suite; the first suite is kept under
  `bench/agent/tasks-v1/` so its result stays reproducible. `-dry-run`
  now prints what the analyses measure for the reference and the starting
  sources of every task, which is how the budgets above were set.

Every reference solution passes its hidden suite and every starting source
fails it (`go run ./bench/agent/harness -dry-run`).

## What this does not change

The plain arm's diagnostics still carry the code and rule number, and the
structured arm's JSON still carries no message: the ablation stays
conservative against its own hypothesis on the diagnostic text. Whether
the wire diagnostic should carry `message` is an open schema decision
(`docs/reports/diagnostic-fixes.md`).

## Accepted

A solution could keep every required function as a stub and print the
expected lines from `main`. The harness's extra hidden programs cannot
guard against that here: a kern module is a directory, so a hidden
program that replaces the task's one file replaces the solution with it.
The mitigation is in the tasks themselves: `main` exercises the required
functions on more than one input (two windows in t01, two texts in t02,
two streams in t03, four buffers in t10, fourteen cache operations in
t08), so a stub that returns a constant prints the wrong lines. A model
that hard-codes eleven lines per input instead of fixing the program is
not a behaviour the first matrix showed, and the transcripts are
recorded, so such a trial would be visible.

## Not done

The matrix has not been run on this suite: 10 tasks × 2 models × 2 arms ×
5 trials is 200 model-driven trials, a cost decision for the owner. A
pilot of one task, one model and one trial per arm
(`go run ./bench/agent/harness -tasks t03_proven_checks -models anthropic:claude-opus-5-5 -trials 1`)
would show whether the plain arm's first DONE fails the budget as
designed before the full run is paid for.
