# What kern's compiler tooling does for an AI agent, and what it does not

**Date:** 2026-11-01 (runs 15:04–17:50 CST); the 154 trials Wafer's credit cut short were re-run on 2026-11-02 at the same commit. **Suites:** 4
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
last run of the night and Wafer's credit ran out during it (below); its
remaining 42 trials were run on 2026-11-02.

## What ran

| run | model | suite | arms | trials | served | records |
|---|---|---|---|---|---|---|
| A | GLM-5.3 @ max | 4 | structured, queries, plain | 150 | 150 | `runs/20261101-151545` |
| B | Flash @ medium | 4 | structured, queries, plain | 150 | 150 | `runs/20261101-161059` |
| C | Flash @ medium | 4 | documented | 50 | 50 | `runs/20261101-163358` |
| D | Kimi-K3 @ medium | 4 | all four | 200 | 200 | `runs/20261101-165447` |
| E | Flash @ medium | 2 | all four | 200 | 200 | `runs/20261101-165417`, `-171254` |
| F | Flash @ medium | 4 | rules | 50 | 50 | `runs/20261101-174611` |

Budgets throughout: 20 iterations and 30 minutes per trial. Run directories
are git-ignored; the harness re-scores them with `-report-from`. In all,
843 trials and about 304 million tokens, 689 trials served on
2026-11-01. The 154 unserved trials were re-run on 2026-11-02 from a
checkout of 29f96c4 (below), all served: every run now has its full
count, 800 scored trials and about 364 million tokens in all (the 40
stale-prompt records below excluded).

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

`go run ./bench/agent/harness -redo-errors <run dir> -call-timeout 15m`
re-ran exactly those trials under their original model, effort, arm and
trial number once credit was added (2026-11-02), so the tables below
count every trial.

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

Success over all trials (complete since the 2026-11-02 re-runs), with the
median iterations of the successful trials and the tokens spent per
success (every trial's tokens, divided by the successes):

| model | arm | succeeded | median iterations | tokens per success |
|---|---|---|---|---|
| Flash | structured | 44 of 50 (88%) | 10 | 0.71M |
| | queries | 41 of 50 (82%) | 11 | 0.78M |
| | documented | 44 of 50 (88%) | 6.5 | 0.43M |
| | rules | 37 of 50 (74%) | 8 | 0.58M |
| | plain | 2 of 50 (4%) | 16 | 11.5M |
| Kimi-K3 | structured | 43 of 50 (86%) | 8 | 0.52M |
| | queries | 43 of 50 (86%) | 8 | 0.56M |
| | documented | 45 of 50 (90%) | 5 | 0.24M |
| | plain | 8 of 50 (16%) | 5 | 1.68M |
| GLM-5.3 @ max | structured | 33 of 50 (66%) | 15 | 1.14M |
| | queries | 28 of 50 (56%) | 15 | 1.33M |
| | plain | 6 of 50 (12%) | 17 | 4.66M |

How the failures ended:

| model | arm | said DONE, hidden tests failing | out of iterations or time |
|---|---|---|---|
| Flash | structured / queries / documented / rules / plain | 0 / 4 / 5 / 10 / 34 | 6 / 5 / 1 / 3 / 14 |
| Kimi-K3 | structured / queries / documented / plain | 2 / 0 / 5 / 39 | 5 / 7 / 0 / 3 |
| GLM-5.3 | structured / queries / plain | 1 / 1 / 24 | 16 / 21 / 20 |

Under the experiment's win rule (per task, more successes, then a lower
median iteration count; 5 trials per arm), the structured arm beats plain
on **9 of 10** tasks for Flash (the threshold is 8), 10 of 10 for Kimi-K3
and 9 of 10 for GLM-5.3. Pair by pair, per model (the harness's combined
decomposition counts a task as lost when any model lacks one of the two
arms, so it scores GLM's missing `documented` and `rules` arms as losses;
the counts here are per model):

| comparison | what it isolates | Flash | Kimi-K3 | GLM-5.3 |
|---|---|---|---|---|
| queries over plain | the semantic queries | 10 of 10 | 10 of 10 | 9 of 10 |
| documented over plain | a written account of the proofs | 9 of 10 | 10 of 10 | not run |
| rules over plain | the proof rules alone | 9 of 10 | not run | not run |
| structured over queries | JSON diagnostics | 4 of 10 | 5 of 10 | 8 of 10 |
| queries over documented | the query tool over the document | 3 of 10 | 2 of 10 | not run |
| documented over rules | the forms that stay checked and the rewrite advice | 6 of 10 | not run | not run |

**Follow-up, 2026-11-02: the requirement in `check`.** The plain arm on
the budgeted suite (`bench/agent/tasks-budgeted`, every function declaring
`budget { bounds_checks 0 }`, so `check` names every runtime-checked
location) succeeded 28 of 50 for Flash and 42 of 50 for Kimi-K3, against
2 and 8 here; see `docs/reports/declared-budgets.md`.

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
   suite 4 the agents without them fail 84 to 96% of the time, and most of
   those failures are not struggles: the agent writes a correct program,
   believes it is done and says so. Three model families behave the same
   way. This is the strongest result of the night, and it is the general
   claim behind kern's design: a requirement stated in terms of what the
   compiler does is unmeetable unless the compiler says what it does.
2. **The format of the diagnostics does not matter measurably for the
   medium-effort models.** JSON diagnostics with rule numbers and
   machine-applicable fixes beat the same compiler's plain-text
   diagnostics on 4 of 10 tasks for Flash and 5 of 10 for Kimi-K3, with
   success rates within a few points either way. GLM-5.3 at max effort is
   the exception: structured beat queries on 8 of 10 tasks, 33 against 28
   of 50, a gap the partial data did not show. One model at five trials
   per task is not enough to claim that structured diagnostics help, but
   it is no longer evidence that they never do.
3. **A written account did as well as the query tool, at about half the
   tokens.** With `docs/bounds-checks.md` and no tools, Flash and Kimi-K3
   matched or exceeded the query arms' success (88 and 90% against 82 to
   88%) and finished in fewer iterations (median 6.5 and 5 against 8 to
   11). The tool's value over a document, on these tasks, is not shown.
   This needs the caveat above: the document was written knowing the
   suite. The `rules` arm, which drops the tailored part (the list of
   forms that stay checked and the rewrite advice), succeeded 37 of 50
   (74%): far above plain (2 of 50) and below the full document (44 of
   50), which beat it on 6 of 10 tasks. The rules carry most of the
   effect; the advice adds the rest.
4. **Where the reference already lets an agent derive the requirement,
   the tooling adds little.** Suite 2's seven non-bounds tasks are solved
   by every arm, plain included. Copies, layout, ownership and moves follow
   rules `docs/ownership.md` states, and agents apply them.
5. **More reasoning effort did not help.** GLM-5.3 at max effort
   succeeded less often than Flash and Kimi-K3 at medium in the arms with
   the queries (66 and 56% against 82 to 88%).
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
  had not seen the suite might do worse; the `rules` arm (74% against the
  full document's 88%, Flash only) bounds how much of the effect came
  from tailoring.
- **Five trials per cell, single runs.** Differences of one success per
  task are within noise.
- **Run order.** The arms of runs B and C ran 30 minutes apart, and run A
  ran concurrently with the others, so Wafer's load differed between them.
  Wall-clock time is reported by the harness and not compared.

## What to change in kern

- **Link `docs/bounds-checks.md` from `docs/ownership.md`** and keep it in
  step with the proof pass: it is the cheapest way to give an agent the
  information this experiment found decisive. The link was added after the
  runs (2026-11-02), not during them, because `docs/ownership.md` is in
  every arm's prompt.
- **Give the query a plain-text form**: a `show-bounds-checks --human`
  listing would carry what the JSON query carries to a toolchain or an
  agent that reads text; the `queries` arm shows the JSON is not what
  matters.
- **Do not lead with structured diagnostics** as the agent advantage. The
  evidence here is for exposing what the compiler knows, in any form.

## Finishing the runs

Every arm's prompt includes `docs/ownership.md`, which since the runs
links `docs/bounds-checks.md` (one sentence; no arm can read the file
through it, but the plain arm's prompt now names it). So the 154 unserved
trials were re-run on 2026-11-02 from a checkout of 29f96c4, the commit
the runs used, after the owner added Wafer credit:

```bash
runs="$PWD/bench/agent/runs"
git worktree add ../kern-runs 29f96c4
cd ../kern-runs
go run ./bench/agent/harness -redo-errors "$runs/20261101-151545" -call-timeout 15m -parallel 6
go run ./bench/agent/harness -redo-errors "$runs/20261101-165447" -call-timeout 15m -parallel 6
go run ./bench/agent/harness -redo-errors "$runs/20261101-174611" -call-timeout 15m -parallel 6
```

All 154 were served (74 GLM, 38 Kimi-K3, 42 `rules`); the three ran at
once, GLM's taking about three and a half hours at max effort. Not run:
GLM `documented` and `rules` arms, and a Kimi-K3 or GLM `rules` arm, which
would complete the per-model decomposition above.

## Review

The harness changes (0c95054) were reviewed by prism with its default
settings (wafer/GLM-5.3): one high finding not adopted (replaying
reasoning items, which Wafer's Responses output does not carry), four
medium and the low ones adopted or answered in that commit's message.
The `rules` arm was reviewed with `--provider openai --model gpt-6.1-sol`
because Wafer had no credit left: no findings. The numbers in this
report were recomputed from the records after the last run ended.
