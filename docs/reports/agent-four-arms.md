# What kern's compiler tooling does for an AI agent, and what it does not

**Date:** 2026-11-01 (runs 15:04–17:50 CST). **Suites:** 4
(`bench/agent/tasks`, bounds-check budgets) and 2 (`bench/agent/tasks-v2`,
copies, allocations, layout, ownership, moves, parallelism). **Models:**
three on Wafer's serverless Responses API: GLM-5.3 at max reasoning effort,
DeepSeek-V4.1-Flash at medium, Kimi-K3 at medium. **Harness:** 0c95054
plus the `rules` arm. **Authorized** by the owner ("run as many tests as you
want to try and show the value or lack of").

## The question, and why the old design could not answer it

Suite 4 asks for programs whose bounds checks the compiler must prove, with
a budget of zero runtime-checked checks. The plain arm of the original
experiment had text diagnostics and nothing else, and nothing it received
said which checks the compiler proves. A structured win there is close to
true by construction: one arm can see the requirement and the other cannot.

So the structured arm's advantage is split into its parts:

| arm | diagnostics | semantic queries | bounds-check document |
|---|---|---|---|
| `structured` | JSON (codes, rules, spans, fixes) | yes | no |
| `queries` | plain text | yes | no |
| `documented` | plain text | no | `docs/bounds-checks.md` |
| `rules` | plain text | no | its rules only (below) |
| `plain` | plain text | no | no |

Every arm also gets the same language reference (`docs/ownership.md`),
grammar, prelude and examples. `docs/bounds-checks.md` was written for this
run from the proof pass's own description (`compiler/opt/prove.go`, the
`proven_by` schema text) and checked against probe programs. It is a real
gap closed: nothing in the documentation said which checks are proven.

**Caveat on `documented`.** I wrote that document knowing suite 4's
idioms, and its list of forms that stay checked names them (midpoint,
`n - 1 - i`, `u8` into `[256]`, `x % len`, and so on), followed by a
one-line rewrite recipe. The `rules` arm removes both, keeping only what
proves a check (`bench/agent/docs/bounds-checks-rules.md`). It was the
last run of the night and Wafer's credit ran out during it (below), so it
has 8 trials, not 50.

## What ran

| run | model | suite | arms | trials | served | records |
|---|---|---|---|---|---|---|
| A | GLM-5.3 @ max | 4 | structured, queries, plain | 150 | 76 | `runs/20261101-151545` |
| B | Flash @ medium | 4 | structured, queries, plain | 150 | 150 | `runs/20261101-161059` |
| C | Flash @ medium | 4 | documented | 50 | 50 | `runs/20261101-163358` |
| D | Kimi-K3 @ medium | 4 | all four | 200 | 162 | `runs/20261101-165447` |
| E | Flash @ medium | 2 | all four | 200 | 200 | `runs/20261101-165417`, `-171254` |
| F | Flash @ medium | 4 | rules | 50 | 8 | `runs/20261101-174611` |

Budgets throughout: 20 iterations and 30 minutes per trial. Run directories
are git-ignored; the harness re-scores them with `-report-from`. In all,
843 trials and about 304 million tokens, 689 trials served.

**Trials not served.** "Served" excludes two kinds of failure that say
nothing about an agent:
- **Wafer credit.** From about 17:35 every request larger than the
  remaining balance was refused with HTTP 402 ("credit balance is
  approximately $1.98 — insufficient for the next request"); the balance
  reached about $0.05. That cut 67 of run A's trials (its last ones), 38
  of run D's (9 to 10 per arm, so the arms stay comparable) and 42 of run
  F's.
- **Call timeouts.** Run A used the harness's then-fixed 180 s limit on
  one model call. Seven GLM trials ended when a max-effort call ran past it
  five times. Later runs used 10 minutes and logged every call's duration;
  the slowest of 3,790 Flash calls took 329 s.

The harness scores an unserved trial as a failure, so for run A, run D and
run F the tables below count served trials only. Once credit is added,
`go run ./bench/agent/harness -redo-errors <run dir> -call-timeout 15m`
re-runs exactly those trials under their original model, effort, arm and
trial number.

**A defect found and fixed during the run.** Suite 2's t03 and t10 prompts
stated budgets of 4 and 5 runtime-checked bounds checks while their hidden
tests required 0 (f15f773 lowered the hidden budgets and left the prompts;
the same thing had happened to suite 3). Agents met the budget they were
told, replied DONE and failed. Both prompts now state 0, the dry run checks
every prompt against its hidden budget, the 40 records made under the
wrong prompts are kept as `*.stale-prompt` and not scored, and both tasks
were re-run.

## Results

### Suite 4: requirements the agent cannot see without help

Success over served trials, with the median iterations of the successful
trials and the tokens spent per success (every served trial's tokens,
divided by the successes):

| model | arm | succeeded | median iterations | tokens per success |
|---|---|---|---|---|
| Flash | structured | 44 of 50 (88%) | 10 | 0.71M |
| | queries | 41 of 50 (82%) | 11 | 0.78M |
| | documented | 44 of 50 (88%) | 6.5 | 0.43M |
| | rules | 6 of 8 | 8 | |
| | plain | 2 of 50 (4%) | 16 | 11.5M |
| Kimi-K3 | structured | 34 of 40 (85%) | 8 | 0.52M |
| | queries | 34 of 40 (85%) | 8 | 0.58M |
| | documented | 37 of 41 (90%) | 5 | 0.24M |
| | plain | 6 of 41 (15%) | 5 | 1.90M |
| GLM-5.3 @ max | structured | 14 of 23 (61%) | 14.5 | 1.18M |
| | queries | 13 of 24 (54%) | 15 | 1.48M |
| | plain | 2 of 29 (7%) | 12 | 7.88M |

How the failures ended:

| model | arm | said DONE, hidden tests failing | out of iterations or time |
|---|---|---|---|
| Flash | structured / queries / documented / plain | 0 / 4 / 5 / 34 | 6 / 5 / 1 / 14 |
| Kimi-K3 | structured / queries / documented / plain | 2 / 0 / 4 / 32 | 4 / 6 / 0 / 3 |
| GLM-5.3 | structured / queries / plain | 1 / 0 / 15 | 8 / 11 / 12 |

Under the experiment's win rule (per task, more successes, then a lower
median iteration count; 5 trials per arm), run B's structured arm beats
plain on **9 of 10** tasks (the threshold is 8), and run D's on 10 of 10
even with the unserved trials counted as failures. Run B with C, scored
pair by pair (from `-report-from` over both directories):

| comparison | what it isolates | tasks won |
|---|---|---|
| queries over plain | the semantic queries | 10 of 10 |
| documented over plain | a written account of the proofs | 9 of 10 |
| structured over queries | JSON diagnostics | 4 of 10 |
| queries over documented | the query tool over the document | 3 of 10 |

Kimi-K3's run gives the same four comparisons 10, 10, 3 and 2 of 10.

### Suite 2: requirements an agent can derive from the reference

| arm (Flash) | succeeded | median iterations | tokens per success |
|---|---|---|---|
| structured | 49 of 50 (98%) | 6 | 0.32M |
| queries | 48 of 50 (96%) | 7 | 0.34M |
| documented | 50 of 50 | 5.5 | 0.28M |
| plain | 40 of 50 (80%) | 5 | 0.45M |

Every one of plain's ten failures is on three tasks: t03 and t10 (bounds
budgets, 2 and 0 of 5) and t01 (a copy budget, 3 of 5). On the other seven
tasks (allocation sites, layout, an ownership conflict, an `unsafe` review,
a move trace, an LRU cache, a parallel count) plain succeeds 5 of 5, as
the other arms do (give or take one trial on the move trace), in a
similar number of iterations. The structured arm
wins 3 of 10 tasks under the win rule.

## What this shows

1. **Compiler facts an agent cannot otherwise obtain are decisive.** On
   suite 4 the agents without them fail 85 to 96% of the time, and most of
   those failures are not struggles: the agent writes a correct program,
   believes it is done and says so. Three model families behave the same
   way. This is the strongest result of the night, and it is the general
   claim behind kern's design: a requirement stated in terms of what the
   compiler does is unmeetable unless the compiler says what it does.
2. **The format of the diagnostics does not matter measurably.** JSON
   diagnostics with rule numbers and machine-applicable fixes beat the same
   compiler's plain-text diagnostics on 4 of 10 and 3 of 10 tasks, with
   success rates within a few points either way. Nothing here supports the
   claim that structured diagnostics by themselves help an agent.
3. **A written account did as well as the query tool, at about half the
   tokens.** With `docs/bounds-checks.md` and no tools, Flash and Kimi-K3
   matched or exceeded the query arms' success (88 and 90% against 82 to
   88%) and finished in fewer iterations (median 6.5 and 5 against 8 to
   11). The tool's value over a document, on these tasks, is not shown.
   This needs the caveat above: the document was written knowing the
   suite. The `rules` arm, which drops the tailored part, got 6 of 8 before
   the credit ran out; it suggests the rules alone carry most of the
   effect, but 8 trials do not establish it.
4. **Where the reference already lets an agent derive the requirement,
   the tooling adds little.** Suite 2's seven non-bounds tasks are solved
   by every arm, plain included. Copies, layout, ownership and moves follow
   rules `docs/ownership.md` states, and agents apply them.
5. **More reasoning effort did not help.** GLM-5.3 at max effort
   succeeded less often than Flash and Kimi-K3 at medium in the arms with
   the queries (61 and 54% against 82 to 88%).
   Its failures with the queries were all spent iterations: it kept writing
   probe programs to learn which loop forms are proven until the 20
   iterations ran out (the replies record this). Its calls were also slow
   enough to exceed a 180 s limit.

## What the evidence does not cover

- **No comparison with another language.** Every arm uses kern. Whether an
  agent does better in kern than in a language it was trained on is a
  different experiment, confounded by training data (the harness report
  says so).
- **Suite 4 was built to need the bounds information**; it measures how
  much a missing fact costs, not how often real work needs that fact.
- **One document, written by the experimenter.** A document by someone who
  had not seen the suite might do worse; the `rules` arm was meant to
  measure that and is incomplete.
- **Five trials per cell, single runs.** Differences of one success per
  task are within noise.
- **Run order.** The arms of runs B and C ran 30 minutes apart, and run A
  ran concurrently with the others, so Wafer's load differed between them.
  Wall-clock time is reported by the harness and not compared.

## What to change in kern

- **Link `docs/bounds-checks.md` from `docs/ownership.md`** and keep it in
  step with the proof pass: it is the cheapest way to give an agent the
  information this experiment found decisive. The link was not added
  tonight, because `docs/ownership.md` is in every arm's prompt and
  changing it mid-experiment would have changed the plain arm.
- **Give the query a plain-text form**: a `show-bounds-checks --human`
  listing would carry what the JSON query carries to a toolchain or an
  agent that reads text; the `queries` arm shows the JSON is not what
  matters.
- **Do not lead with structured diagnostics** as the agent advantage. The
  evidence here is for exposing what the compiler knows, in any form.

## Finishing the runs

After adding Wafer credit, in order:

```bash
go run ./bench/agent/harness -redo-errors bench/agent/runs/20261101-151545 -call-timeout 15m -parallel 6
go run ./bench/agent/harness -redo-errors bench/agent/runs/20261101-165447 -call-timeout 15m -parallel 6
go run ./bench/agent/harness -redo-errors bench/agent/runs/20261101-174611 -call-timeout 15m -parallel 6
```

Each writes `bench/agent/REPORT.md` unless `-out` is given; the committed
report is suite 2's from 2026-10-28. Run A then needs a GLM `documented`
and `rules` run to complete its decomposition, and run F a full 50 trials
to settle the caveat on `documented`.

## Review

The harness changes (0c95054) were reviewed by prism with its default
settings (wafer/GLM-5.3): one high finding not adopted (replaying
reasoning items, which Wafer's Responses output does not carry), four
medium and the low ones adopted or answered in that commit's message.
The `rules` arm was reviewed with `--provider openai --model gpt-6.1-sol`
because Wafer had no credit left: no findings. The numbers in this
report were recomputed from the records after the last run ended.
