# Benchmark harness: interleaved rounds and paired ratios

**Date:** 2026-10-30. **Spec:** `specs/bench-interleave/SPEC.md`.
**Plan:** `specs/bench-interleave/PLAN.md`. **Proposed in:**
`docs/reports/thread-pool-job-path.md`, "A second quiet run, one hour
later".

## The problem

Two quiet runs of the harness an hour apart moved single workloads by up
to 0.1 of their target ratio, in both directions, and the C or Rust row
moved as often as kern's. The harness ran each row's five repetitions
back to back, then the next row. A burst of load inside one row's window
therefore moved that row alone, and the ratio with it. The per-row 95%
interval describes five runs inside that window, so it could not see the
burst. Interleaved by hand on the same host, btree came out at 1.01
where the harness had said 0.87, and lock_free_queue at 0.93 where it
had said 0.85.

## What changed

- **Interleaved rounds.** Every row is built first. Then `-runs`
  rounds (default now 10; fewer than 5 is refused, since the language
  spec asks for at least 5) each run every row once. They go workload
  after workload, with a workload's rows in an order rotated by one
  each round. The rows compared with each other run seconds apart in
  every round, and no row is always first.
- **Paired ratios.** Each round in which kern's release row and its
  reference both ran gives a ratio. The reference is the best C or Rust
  row, with C on a tie. The target line gives the ratios' geometric mean
  and a 95% interval, computed from the mean of their logarithms ±
  Student's t times the standard error.
- **Three verdicts.**
  - Throughput meets the target when the interval's lower bound is at
    least 0.90, and misses it when the upper bound is below 0.90.
  - Peak RSS meets the target when the upper bound is at most 1.10, and
    misses it when the lower bound is above 1.10.
  - Anything else, including fewer than two paired rounds, is
    **undecided**.

  This replaces the rule that read a ratio within 0.1 of the target as
  undecided by eye.
- **Load.** The header gives the one-minute load at the start of each
  round as a range. `-max-load X` makes the harness wait before each
  round until the load is at most X, for at most `-load-wait` (default
  30 minutes); a round whose wait expired is named in the header. An
  unreadable load is reported as unavailable, with one warning.
- **Unchanged:** the per-row tables (columns, means and intervals), the
  failure-path runs (once per row after the rounds), and the
  `-experiment` sections. `bench/PROTOCOL.md` describes the rounds,
  statistics and verdicts in place of "runs each row five times".

## Verification

- **Unit tests** (`bench/harness/rounds_test.go`, the package's first)
  cover:
  - the exact schedule for 3 rows over 4 rounds plus a one-row
    workload, with unbuilt rows left out;
  - pairing that leaves out failed runs and a zero RSS;
  - the interval against a hand computation, ratios 0.9, 1.0 and 1.1
    with t = 4.303;
  - the verdicts at exactly 0.90 and 1.10 and just either side;
  - the refusal of `-runs 4`;
  - every variant of the target line: met, undecided by spread, one
    paired round, an unmeasured kern row, no baseline, and the C/Rust
    tie;
  - the load gate with a fake clock: off, already low, falling after two
    polls, timing out, and unreadable at the first read or mid-wait,
    warning once;
  - a pinned per-row table line.
- **A smoke run.** `go run ./bench/harness -only vector,ring_buffer
  -runs 5 -out <scratch>` finished in 6 s. It ran with the bench slot
  free but the host loaded, with a one-minute load of 14. Its log shows
  each workload's rows rotating:
  - round 1 ran C, Rust, kern release, kern debug;
  - round 2 ran Rust, kern release, kern debug, C;
  - round 3 started with kern release.

  Its report has the new header lines and paired target lines such as
  `throughput kern/c paired ratio 1.04 [1.01, 1.07] over 5 rounds,
  meets the 10% target`. These numbers are not a measurement.
- **Not regenerated.** `bench/REPORT.md` is left as it is. A report
  meant to be compared is taken with the full harness on a quiet host,
  which is the bench slot's and the owner's to schedule.

## Reviews

- **speccritic:** VALID_WITH_GAPS with no critical finding. Its warnings
  about unavailable pairings, the reference tie, an unreadable load and
  non-positive measures were folded in.
- **plancritic:** EXECUTABLE_WITH_CLARIFICATIONS, score 86. Folded in.
- **prism:** one low finding, that a load read failing mid-wait gave no
  warning; fixed and tested. The second pass was clean.
- **realitycheck:** PARTIALLY_ALIGNED, score 100, no violations.
- **verifier:** only an entry for a stale worktree under `.claude/`,
  outside this change.
- **Gates:** `golangci-lint run ./...` reports 0 issues; `go test ./...`
  is green.
