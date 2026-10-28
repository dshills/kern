# Agent benchmark, second task suite

**Date:** 2026-10-28. **Follows:** the full matrix on the first suite
(0 wins of 10, every trial succeeding in both arms) and the research
report's conclusion that the next experiment needs tasks the plain arm
fails. **Result:** the matrix on this suite is `bench/agent/REPORT.md`
and the last section of this report. **Plan:** PLAN.md 4.2, spec §2.3
and §9.

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

A second pilot on the tightened tasks (t01, t03, t10; claude-opus-5-5;
one trial per arm; records under `bench/agent/runs/20261028-103735`):

| task | arm | success | iterations | wall (s) | final measure |
|---|---|---|---|---|---|
| t01_copy_budget | structured | yes | 3 | 16 | 0 copies |
| t01_copy_budget | plain | yes | 3 | 25 | 0 copies |
| t03_proven_checks | structured | yes | 4 | 47 | 2 runtime checks |
| t03_proven_checks | plain | no | 20 (max) | 1800 (max) | 10 runtime checks |
| t10_tlv_decoder | structured | yes | 10 | 108 | 4 runtime checks |
| t10_tlv_decoder | plain | no | 20 (max) | 1800 (max) | 7 runtime checks |

On both bounds-check tasks the plain arm wrote a program with the right
output, saw a clean check and a correct run, and said DONE at its second
iteration with 10 and 7 runtime-checked bounds checks against budgets of
4 and 5: with nothing to measure the requirement by, its first guess was
its answer. The structured arm called `show_bounds_checks` after every
rewrite and converged in 4 and 10 iterations. t01 is a tie on iterations
(3 and 3), which the rule scores as a loss, although the structured arm
was faster; a copy budget is reasoned about correctly from the reference
alone by this model.

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

## The matrix

The full matrix ran on 2026-10-28 (`go run ./bench/agent/harness`, both
models, both arms, 5 trials per cell, 200 trials, records under
`bench/agent/runs/20261028-104427/`; the table is `bench/agent/REPORT.md`).
The primary metric records **0 wins of 10**, as on the first suite, but
for the opposite reason.

**Successes.** The structured arm passed all 100 of its trials. The plain
arm passed 90: it failed t03 on 4 of 5 GPT trials and 2 of 5 Claude
trials, and t10 on 4 of 5 Claude trials. Every one of the ten failures has
the same shape, the one the suite was written to produce: the program
checks cleanly and prints the expected output, the agent replies DONE
(after 2 to 9 iterations), and the hidden count of runtime-checked bounds
checks is over the budget. The plain arm has no way to count what the
pass proved, so it stops at the first version that runs. The structured
arm called `show_bounds_checks` and rewrote until the count fit. Those
are the two tasks whose requirement the compiler alone can measure and
whose starting program is furthest from meeting it; on the other eight,
both arms met the budget at or near the first attempt.

**Iterations and wall-clock.** The rule asks for strictly fewer median
iterations and strictly less median wall-clock time in both models, with
ties losing, so the structured arm's extra tool calls count against it
wherever they do not avert a failure. The query tools were used in 37 of
Claude's 50 structured trials and 39 of GPT's. Claude's medians tie on
seven tasks (3 iterations in both arms on six of them), beat the plain
arm on t08 (4 against 5) and t10 (5 against the scored 20), and lose on
t07 (6 against 4, where the structured trials took one more rewrite
cycle each; only two of the five called a query, `explain_ownership`
once apiece, so the difference there is trial-to-trial variance in a
task both arms solve, not query cost). GPT's structured
medians are one to five iterations higher than its plain medians on seven
tasks (t02 is 9 against 5, t08 is 10 against 5), tie on t06, and win on
t03 (7 against the scored 20) and t07 (8 against 9). No task is a win in
both models, which is what the rule requires.

**Reading.** The suite does what the first could not: the plain arm now
fails, and only where the requirement needs a measurement the text
diagnostics do not carry. What the matrix shows is that the queries buy
reliability on those tasks (a failure rate of 10 of 100 goes to 0 of 100)
and cost speed on the rest, because a model that is given a measuring
tool uses it even when the task does not need it, and each use is an
iteration. The primary metric as written in spec §9 measures speed and
counts a tie as a loss, so it cannot register the reliability gain, and
under it the experiment fails. Whether the metric should weigh successes
before medians is a specification question and is left to the owner; the
report records the result under the rule as it stands. The token medians
in the report show the same trade: the structured arm's trials are
longer where it queried (GPT's t08 at 296 k input tokens against 140 k)
and no shorter where it did not.

Two cautions. The failure shape depends on the agent declaring DONE on
the strength of `run` alone; a plain-arm agent that distrusted its own
count of indexed reads could rewrite defensively and pass, and Claude did
so on three t03 trials. And the queries' cost is partly the prompt's: the
structured system prompt names the five queries without saying when they
are worth calling, so the models call them on sight. A prompt that said
"query only what the task bounds" would narrow the iteration gap, and
would also be a different experiment.
