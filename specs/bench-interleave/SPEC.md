# Benchmark harness: interleaved rounds and paired ratios

**Version:** 0.1 (2026-10-30). **Scope:** the benchmark harness
(`bench/harness`), its protocol (`bench/PROTOCOL.md`), tests for it, and
the report `docs/reports/bench-interleave.md`. **Language impact:**
none. **Related:** `specs/initial/AI_Native_Systems_Language_SPEC.md`
§2 (each benchmark report records "over at least 5 repeated runs with
95% confidence intervals"; the 10% throughput and memory targets) and
§9; `docs/reports/thread-pool-job-path.md` ("A second quiet run, one
hour later", where this change was proposed);
`docs/reports/quiet-harness-2026-10-28.md`.

**Review status.** speccritic (OpenAI gpt-6.1-sol, profile general,
with `docs/reports/thread-pool-job-path.md` as context): VALID_WITH_GAPS,
no critical finding; its warnings on unavailable pairings, the
reference tie, an unreadable load and non-positive measures are folded
in (R6a, R7, R3, the glossary). The rest are the preflight's flags on
prose.

**Glossary.** R*n*: requirement *n* of §3. Row: one (workload,
configuration, program) the harness builds and runs, as today. Run: one
execution of a row's binary. Round: one run of every built row of the
selected workloads. Reference row: the C or Rust release row of a
workload with the higher mean throughput (for the throughput target) or
the lower mean peak RSS (for the memory target), as today's target line
chooses it; on a tie C is chosen. Kern row: the workload's main kern program in release, as
today. Paired ratio: for one round, the kern row's measure divided by
the reference row's measure in the same round. Load: the host's
one-minute load average.

## 0. Goals and non-goals

Goal: a ratio between kern and its reference that a transient on the
host cannot move alone, and a verdict that says "undecided" when the
evidence does not decide. Two quiet runs of today's harness an hour
apart moved single workloads by up to 0.1 in both directions, because
each row's runs are back to back: a burst inside one row's window
moves that row alone, and the per-row confidence interval cannot see
it.

Non-goals: changing the workloads, their programs, the measures or the
10% targets; making the `-experiment` sections interleaved (they keep
their own runs); running the full harness as part of this change (a
timed run needs a quiet host and the bench slot); Linux-specific load
handling beyond reading the load as today.

## 1. Behaviour today

`bench/harness/main.go` builds a row and then runs it `-runs` times
(default 5) back to back, then moves to the next row. The report's
target line divides the kern row's mean throughput by the best
baseline's mean, and says "meets" or "does not meet".

## 2. Behaviour after

1. Every selected row is built first, with its static metrics, as
   today.
2. Measurement runs in `-runs` rounds. In round *k* (counting from 0),
   for each selected workload in the harness's workload order, each
   built row of that workload runs once, in the workload's row order
   rotated left by *k* modulo the number of its built rows. So a row
   that ran first in one round runs later in the next, and a burst
   spanning a few seconds lands on adjacent rows of the same round
   rather than on every run of one row.
3. Each run records its round. A failed run is recorded as today and
   contributes no measure for its round.
4. The failure-path runs (`KERN_BENCH_FAIL=1`) happen once per row
   after the rounds, as today.

## 3. Requirements

**R1 — Schedule.** The run order is exactly §2's: rounds outermost,
workloads in order inside a round, the rows of a workload rotated by
the round number. A pure function returns the schedule (a list of
(round, row) pairs) and is tested on 3 rows over 4 rounds and on a
workload with 1 row.

**R2 — Default repetitions.** `-runs` defaults to 10. Values below 5
are refused with an error, since the language spec requires at least 5
repeated runs.

**R3 — Paired throughput ratio.** For each workload with a kern row and
a reference row, the paired ratios are computed over the rounds in which
both rows succeeded and both measures are finite and above 0 (a round
with a zero or missing measure, such as a peak RSS the operating system
did not report, is left out of that ratio only). The report gives their
geometric mean and a 95%
confidence interval: the mean of the logarithms plus or minus Student's
t at n−1 degrees of freedom times their standard error, mapped back
with exp. With fewer than 2 paired rounds the interval is not given.

**R4 — Throughput verdict.** "meets" when the interval's lower bound is
at least 0.90; "does not meet" when its upper bound is below 0.90;
"undecided" otherwise, and also when fewer than 2 rounds pair.

**R5 — Memory.** The peak-RSS target is treated the same way: paired
ratios of the kern row's peak RSS to the memory reference row's, the
geometric mean and interval, and "meets" when the upper bound is at
most 1.10, "does not meet" when the lower bound is above 1.10,
"undecided" otherwise.

**R6a — Nothing to pair.** When a workload has no measured C or Rust
release row the target line says so, as today; when the kern row was
not measured it says so and names the reference; when fewer than 2
rounds pair, it gives the paired count and the verdict "undecided" with
no interval.

**R6 — Report.** The report's header states that the rows are measured
in interleaved rounds (§2), the number of rounds, and the load at the
start of every round as a minimum and maximum over the rounds. Each
workload's target line gives, for throughput and for memory: the
reference language, the paired ratio, its interval, the number of
paired rounds, and the verdict. The per-row table keeps its columns and
its per-row means and intervals.

**R7 — Optional load gate.** `-max-load X` (default 0, off) makes the
harness wait, before each round, until the load is at most X, polling
every 15 seconds, for at most `-load-wait` (default 30 minutes) per
round; a round that starts after the wait expires is marked in the
report with the load it started at. With the gate off the harness never
waits. When the load cannot be read, the gate does not wait (it logs a
warning once), and the report shows the load as "unavailable" for that
round and leaves it out of the minimum and maximum.

**R8 — Protocol.** `bench/PROTOCOL.md` describes the rounds, the
rotation, the paired ratio and its interval, the three verdicts, the
default of 10 rounds and the load gate, replacing the sentence that
says each row is run five times.

**R9 — Verification without a timed run.** Unit tests cover R1, the
statistics of R3 and R5 (a hand-computed case), the verdict boundaries
of R4 and R5 (exactly 0.90 and 1.10 included), and R2's refusal. A
smoke run of two small workloads with `-runs 5` and `-out` in the
scratch directory shows the schedule in its log and the new target
lines. It is taken only when no other harness or `kern build` timing is
running (the bench slot), and its numbers are not a measurement.

## 4. Acceptance criteria

1. `go test ./bench/harness` passes with the tests of R9.
2. The smoke run's report shows the header lines of R6 and, for each of
   its two workloads, a target line with paired ratio, interval, rounds
   and verdict.
3. `bench/REPORT.md` is not modified by this change; it changes at the
   next full run.
