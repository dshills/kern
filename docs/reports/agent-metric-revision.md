# Weighing successes before medians in the agent metric

**Status:** drafted and adopted by the owner on 2026-10-28; the changes
listed at the end are made. **Date:** 2026-10-28. **Evidence:** `bench/agent/REPORT.md` and the
records under `bench/agent/runs/20261028-104427/`.

## The rule as it stands

Spec §2.3: "agents given structured diagnostics and semantic queries (§7)
reach a passing hidden test suite in fewer iterations and less wall-clock
time than agents given plain text diagnostics for the same tasks, on at
least 8 of 10 tasks." PLAN.md 4.2 closes §13 accepted gap 7 with the
aggregation: a task is a win when, for every model, the structured arm
has at least as many successful trials and a strictly lower median
iteration count *and* a strictly lower median wall-clock time, over at
least 5 trials; a tie on either median is a loss. The harness applies
exactly that (`bench/agent/harness/report.go`, the condition after the
per-cell rows).

Successes enter the rule only as a floor. A task where the structured
arm turns four failures in five into none, at the same median, is a loss;
a task where both arms succeed every time and the structured arm's median
is one iteration lower is a win. The second matrix produced the first
case twice (t03 in both models, t10 in claude-opus-5-5) and the rule
could not count it.

## Proposed wording

Replace the §2.3 bullet with:

> **Primary agent metric:** within this language, agents given structured
> diagnostics and semantic queries (§7) reach a passing hidden test suite
> more reliably than agents given plain text diagnostics for the same
> tasks, and where both are equally reliable, in fewer iterations, on at
> least 8 of 10 tasks. Wall-clock time and token use are reported per
> task and are not part of the pass condition. Cross-language comparisons
> against Go and Rust are reported but are descriptive only, because
> training-data volume confounds them.

Replace the aggregation sentence in PLAN.md 4.2 with:

> A task is a win when, for every model tested, over at least 5 trials
> per arm, the structured-diagnostics arm is strictly better than the
> plain-text arm in the first of these that differs: the number of
> successful trials, then the median iteration count (a failed or
> timed-out trial scoring the budget maximum); a task where both are
> equal for any model is a loss, and a task where the plain arm is better
> at the first differing criterion for any model is a loss. Wall-clock time is recorded and
> reported per cell, not compared. The primary metric passes at 8 wins
> of 10 (§2.3).

The harness change is the one condition in `report.go`: compare
`successes` first, then `median(iters)`, and drop the wall-clock clause;
the report's preamble sentence and the research report's description of
the rule follow. The recorded run can be re-scored from its JSON records
without paying for another matrix; the harness would need a flag that
builds the report from an existing run directory, which it does not have
today (about twenty lines in `main.go`).

**Why wall-clock leaves the comparison.** It is the noisiest of the three
measures: on t06 the arms tie on every trial's iteration count in both
models and the wall medians differ by 0.7 s and 1.0 s, both in the
structured arm's favour, so a lexicographic rule that fell through to
wall-clock would count t06 as a win on provider latency. Wall-clock stays
in the report because the spec asks for it to be measured (§9); it just
stops deciding.

**Why ties still lose.** A rule under which a tie is a win would count
every task the plain arm already solves at the first attempt as evidence
for structured diagnostics, which it is not. The asymmetry is deliberate:
the structured arm must be better, not merely not worse.

## What the second matrix scores under it

| rule | wins of 10 | which |
|---|---|---|
| as it stands (medians of iterations and wall, both strict, successes a floor) | 0 | — |
| proposed (successes, then median iterations; every model strictly better) | 1 | t03 |
| proposed, but any model better and none worse | 1 | t03 |
| proposed, but falling through to wall-clock | 2 | t03, t06 (the latter on 0.7 s) |
| proposed, pooled over both models (10 trials per arm) | 2 | t03, t10 |
| pooled, falling through to wall-clock | 3 | t03, t06, t10 |

t10 is not a win under the per-model form because gpt-6.1-sol's plain arm
succeeded in all five trials at a median of 5 iterations against the
structured arm's 6; only claude-opus-5-5's plain arm failed there. The
pooled form counts it because the pooled successes are 10 against 6; it
also counts t06 on pooled wall-clock, which is the noise case again, so
pooling is not recommended as written.

## What the revision does not do

It does not make the experiment pass, and no rule that puts successes
first would, on this suite: eight of the ten tasks were solved by both
arms in every trial, mostly at the first attempt, and on those the
structured arm is at best equal and in gpt-6.1-sol's case usually slower,
because the model calls the queries whether or not the task bounds what
they measure. A threshold of 8 wins of 10 presumes a suite on which at
least eight tasks give the structured arm something to be better at. The
second suite gave it two.

So the decision has two parts, and the wording above settles only the
first:

1. **What a win is.** Successes before medians, as drafted.
2. **What the denominator is.** Either the threshold stays at 8 of 10 and
   the next suite is built so that the plain arm fails at least some
   trials on most tasks (the two tasks that worked are the pattern: a
   requirement the compiler alone can measure, in a program where the
   first version that runs does not meet it), or the metric is restated
   as a pooled reliability comparison across the suite (structured
   failures strictly fewer than plain failures, with no task and model
   where the structured arm has fewer successes, and median iterations
   not more than one higher on tasks both arms always solve). The second
   form scores the present matrix as a pass on reliability (0 failures
   against 10) and a fail on the cost clause (gpt-6.1-sol runs 2 to 5
   iterations over on four tasks), which is an accurate description of
   what happened, but it is a different metric from the one the spec
   has carried since v0.2, and changing it after seeing the data is the
   kind of move the report would have to disclose.

A third option is to leave the rule alone and record the second matrix as
it is now recorded: a reliability result the metric does not measure. The
research report already says that.

## Adopted

The owner adopted the wording above, which keeps the threshold at 8 of
10; the denominator question is left open as stated. Files changed: `specs/initial/AI_Native_Systems_Language_SPEC.md`
(§2.3 bullet; a note under §13 gap 7 saying the aggregation rule was
revised on 2026-10-28 and why), `specs/initial/PLAN.md` (4.2),
`bench/agent/harness/report.go` and `main.go` (the condition, the
preamble, a `-report-from` flag), `bench/agent/REPORT.md` (re-scored from
the recorded run with `go run ./bench/agent/harness -report-from
bench/agent/runs/20261028-104427`, the trials unchanged: 1 win of 10), `docs/research-report.md` (§4
and conclusion 3) and `README.md`. The first matrix's result is unchanged
under any variant: every trial succeeded and every median tied.
