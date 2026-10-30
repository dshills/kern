package main

// Interleaved rounds and paired ratios (specs/bench-interleave/SPEC.md):
// the run schedule, the pairing of kern's runs with its reference's in the
// same round, their geometric mean and interval, the verdicts and the
// load gate.

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// slot is one run of the schedule: a row in a round.
type slot struct {
	round int
	r     *row
}

// built reports a row that was built and can run.
func built(r *row) bool { return r.skip == "" && r.buildErr == "" && r.bin != "" }

// schedule orders the runs (spec R1): rounds outermost; inside a round the
// workloads in order; a workload's built rows in their planned order,
// rotated left by the round number.
func schedule(workloads []string, rows []*row, rounds int) []slot {
	var out []slot
	for k := 0; k < rounds; k++ {
		for _, w := range workloads {
			var ws []*row
			for _, r := range rows {
				if r.workload == w && built(r) {
					ws = append(ws, r)
				}
			}
			for i := range ws {
				out = append(out, slot{round: k, r: ws[(i+k)%len(ws)]})
			}
		}
	}
	return out
}

// validateRuns refuses fewer than 5 rounds (spec R2: the language spec's
// "at least 5 repeated runs").
func validateRuns(n int) error {
	if n < 5 {
		return fmt.Errorf("-runs must be at least 5 (the language spec requires at least 5 repeated runs), got %d", n)
	}
	return nil
}

// pairedRatios divides the kern row's measure by the reference row's in
// every round where both have a run with a finite measure above 0 (spec
// R3), in round order.
func pairedRatios(kern, ref *row, measure func(runResult) float64) []float64 {
	byRound := func(r *row) map[int]float64 {
		m := map[int]float64{}
		for _, x := range r.runs {
			v := measure(x)
			if v > 0 && !math.IsInf(v, 0) && !math.IsNaN(v) {
				m[x.round] = v
			}
		}
		return m
	}
	k, f := byRound(kern), byRound(ref)
	var rounds []int
	for round := range k {
		if _, ok := f[round]; ok {
			rounds = append(rounds, round)
		}
	}
	sort.Ints(rounds)
	out := make([]float64, len(rounds))
	for i, round := range rounds {
		out[i] = k[round] / f[round]
	}
	return out
}

// geoMeanCI is the geometric mean of positive ratios and its 95% interval:
// the mean of the logarithms plus or minus Student's t at n-1 degrees of
// freedom times their standard error, mapped back with exp (spec R3). ok is
// false below 2 values; the mean is still given for 1.
func geoMeanCI(xs []float64) (mean, lo, hi float64, ok bool) {
	n := len(xs)
	if n == 0 {
		return 0, 0, 0, false
	}
	logs := make([]float64, n)
	for i, x := range xs {
		logs[i] = math.Log(x)
	}
	m, half := meanCI(logs)
	if n < 2 {
		return math.Exp(m), 0, 0, false
	}
	return math.Exp(m), math.Exp(m - half), math.Exp(m + half), true
}

// Verdicts (spec R4, R5).
const (
	verdictMeets     = "meets"
	verdictMisses    = "does not meet"
	verdictUndecided = "undecided on"
)

// throughputVerdict: kern must reach 0.90 of the reference.
func throughputVerdict(lo, hi float64, ok bool) string {
	switch {
	case !ok:
		return verdictUndecided
	case lo >= 0.90:
		return verdictMeets
	case hi < 0.90:
		return verdictMisses
	}
	return verdictUndecided
}

// memoryVerdict: kern's peak RSS must be at most 1.10 of the reference.
func memoryVerdict(lo, hi float64, ok bool) string {
	switch {
	case !ok:
		return verdictUndecided
	case hi <= 1.10:
		return verdictMeets
	case lo > 1.10:
		return verdictMisses
	}
	return verdictUndecided
}

// loadGate waits before a round until the host's load is at most max
// (spec R7); every dependency is injected so tests need no real waiting.
type loadGate struct {
	max        float64
	wait, poll time.Duration
	read       func() (float64, bool)
	sleep      func(time.Duration)
	now        func() time.Time
	warn       func(string)
	warned     bool
}

// before returns the load the round starts at (ok false when it cannot be
// read) and whether the wait expired with the load still above max.
func (g *loadGate) before() (load float64, ok, expired bool) {
	load, ok = g.read()
	if !ok {
		g.unreadable()
		return 0, false, false
	}
	if g.max <= 0 {
		return load, true, false
	}
	deadline := g.now().Add(g.wait)
	for load > g.max {
		if !g.now().Before(deadline) {
			return load, true, true
		}
		g.sleep(g.poll)
		if load, ok = g.read(); !ok {
			g.unreadable()
			return 0, false, false
		}
	}
	return load, true, false
}

// unreadable warns, once over all rounds, that the load cannot be read.
func (g *loadGate) unreadable() {
	if !g.warned && g.warn != nil {
		g.warn("the load average cannot be read: rounds are not gated and report it as unavailable")
	}
	g.warned = true
}

// oneMinuteLoad reads the first figure of loadAverage.
func oneMinuteLoad() (float64, bool) {
	f := strings.Fields(loadAverage())
	if len(f) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(f[0], 64)
	return v, err == nil
}

// roundLoad is the load a round started at.
type roundLoad struct {
	load           float64
	known, expired bool
}

// loadSummary renders the rounds' starting loads for the report header
// (spec R6, R7).
func loadSummary(loads []roundLoad, max float64) string {
	lo, hi := math.Inf(1), math.Inf(-1)
	var unknown, expired []string
	for i, l := range loads {
		if !l.known {
			unknown = append(unknown, strconv.Itoa(i+1))
			continue
		}
		lo, hi = math.Min(lo, l.load), math.Max(hi, l.load)
		if l.expired {
			expired = append(expired, fmt.Sprintf("%d (%.2f)", i+1, l.load))
		}
	}
	var b strings.Builder
	if len(unknown) < len(loads) {
		fmt.Fprintf(&b, "one-minute load at the start of each round: %.2f to %.2f", lo, hi)
	} else {
		b.WriteString("one-minute load at the start of each round: unavailable")
	}
	if len(unknown) > 0 && len(unknown) < len(loads) {
		fmt.Fprintf(&b, "; unavailable in rounds %s", strings.Join(unknown, ", "))
	}
	if max > 0 {
		fmt.Fprintf(&b, "; load gate %.2f", max)
		if len(expired) > 0 {
			fmt.Fprintf(&b, ", wait expired before rounds %s", strings.Join(expired, ", "))
		}
	}
	return b.String()
}
