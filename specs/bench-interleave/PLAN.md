# Plan: benchmark harness interleaved rounds

**Spec:** `specs/bench-interleave/SPEC.md` v0.1. **Profile:** davin-go.
**Review status.** plancritic (davin-go, with the spec):
EXECUTABLE_WITH_CLARIFICATIONS, 86, no critical finding; folded in: an
injectable load gate with deterministic tests, the gate commands, the
unchanged per-row tables, unreadable loads in the report, and the
`-experiment` sections left as they are.
**Order:** three phases; each ends with `golangci-lint run ./...` and
`go test ./bench/harness` green. One commit after Phase 3. No full
timed harness run.

## Phase 1: schedule and statistics (R1–R5, pure functions)

**Files.** `bench/harness/rounds.go` (new), `bench/harness/rounds_test.go`
(new).

**Steps.**

1. `schedule(workloads []string, rows []*row, rounds int) []slot`, with
   `slot{round int; r *row}`: for k in 0..rounds-1, for each workload in
   order, its built rows (no skip, no build error) in planRows order,
   rotated left by k modulo their count (R1).
2. `pairedRatios(kern, ref *row, measure func(runResult) float64)
   []float64`: rounds in which both rows have a run with a finite
   measure above 0 (R3).
3. `geoMeanCI(xs []float64) (mean, lo, hi float64, ok bool)`: the mean
   of logs ± t(n−1)·sd/√n, exponentiated; ok is false below 2 values
   (R3). Uses the existing `tTable`.
4. `throughputVerdict(lo, hi float64, ok bool) string` and
   `memoryVerdict(lo, hi float64, ok bool) string` per R4 and R5,
   boundaries inclusive as the spec states.
5. `runResult` gains `round int`.
6. Tests: the schedule on 3 rows × 4 rounds (exact order) and a
   1-row workload; pairing with a failed run and a zero RSS left out;
   `geoMeanCI` against a hand-computed case (ratios 0.9, 1.0, 1.1: geo
   mean and interval computed independently in the test with
   `math.Log`, `math.Exp` and t = 4.303); verdicts at 0.90 and 1.10
   exactly, just inside, just outside, and not ok.

**Acceptance.** `go test ./bench/harness` green.

## Phase 2: wiring, flags, report, protocol (R2, R6–R8)

**Files.** `bench/harness/main.go`, `bench/harness/report.go`,
`bench/PROTOCOL.md`.

**Steps.**

1. Flags: `-runs` default 10, values below 5 refused (`fatalf` with the
   reason); `-max-load` (float, 0 off) and `-load-wait` (duration,
   default 30m).
2. `main`: build every row first (the existing loop without its run
   part), then run the schedule; before each round, the load gate. The
   gate is a struct `loadGate{max float64; wait, poll time.Duration;
   read func() (float64, bool); sleep func(time.Duration); now func()
   time.Time; warned bool}` whose `before() (load float64, ok, expired
   bool)` waits while the load read is above `max` (polling every
   `poll`, 15 s in production) until `wait` has passed; an unreadable
   load returns at once with ok false and logs one warning across all
   rounds; `max` 0 never waits. The round's starting load (or
   "unavailable") is recorded. Each run's `round` is set. Then
   the failure-path runs per row as before. Progress logs name the
   round and row.
3. `targetLine` uses `pairedRatios`, `geoMeanCI` and the verdicts:
   `Targets: throughput kern/<ref> paired ratio R [lo, hi] over N
   rounds, <verdict> the 10% target; peak RSS kern/<ref> R [lo, hi]
   over N rounds, <verdict> the 10% target.` with R6a's variants. The
   reference rows are chosen as today with C on ties.
4. Header lines (R6): "rows measured in N interleaved rounds (each
   round runs every row once, rotated)", the starting load's minimum and
   maximum over the rounds whose load was read (rounds with an
   unavailable load are counted and named, and left out of the
   extrema), plus each round whose gate expired with its load. The
   per-row tables are produced by the same code as today from `r.runs`
   (their columns, means and intervals unchanged); a test renders one
   row's table line before and after and compares. The `-experiment`
   sections (`aliasingExperiment`, `separations`) are called after the
   rounds exactly as now, with their own runs.
5. `bench/PROTOCOL.md`: the paragraph that says "runs each row five
   times" is replaced by §2 of the spec, R3–R5's statistics and
   verdicts, the default of 10 and the gate.
6. A refusal test for `-runs 4` through a small `validateRuns(n int)
   error` helper (R2); a test of `targetLine` on synthetic rows for
   each verdict and R6a variant; `TestLoadGate` with a fake reader,
   clock and sleeper and no real sleeping: gate off, load already low,
   load that falls after two polls, a timeout (expired true), an
   unreadable load (ok false, warning logged once over two rounds).

**Acceptance.** `go test ./bench/harness` green; `go vet` clean.

## Phase 3: smoke run, report (R9)

1. Bench slot: proceed only when `pgrep -fl 'bench/harness|kern
   build|bench/\.build|/exe/harness' | grep -v 'zsh -c'` is empty.
2. `go run ./bench/harness -only vector,ring_buffer -runs 5 -out
   <scratch>/smoke.md 2> <scratch>/smoke.log`: the log shows the rotated
   order; the report shows R6's header lines and two target lines with
   paired ratios. The numbers are not a measurement (loaded host).
3. `docs/reports/bench-interleave.md`: the problem, the design, the
   tests, the smoke run's evidence and why `bench/REPORT.md` was not
   regenerated.
4. Mandatory gates (blocking): `golangci-lint run ./...` with 0 issues;
   `go test ./...` green. Advisory reviews (findings fixed or justified
   in the report), using the tools on the development host with the
   OpenAI key the earlier changes used: `prism review staged --provider
   openai --model gpt-6.1-sol --format json`; `realitycheck check .
   --spec specs/bench-interleave/SPEC.md --plan
   specs/bench-interleave/PLAN.md --provider openai --model gpt-6.1-sol
   --format json` with unrelated directories ignored; `verifier analyze
   -root . -mode offline -spec … -plan … -include 'bench/harness/**'
   -format text`.

**Acceptance.** Smoke run as described; mandatory gates pass; advisory
findings recorded.

## Risks

- **Longer runs.** Ten rounds double the measurement time of five; the
  build phase is unchanged. Acceptable for a report made a few times a
  week.
- **Rotation and caches.** A row that follows a memory-heavy row may run
  with a colder cache; rotation spreads that effect over every row
  instead of fixing it on one.
