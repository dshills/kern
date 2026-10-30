package main

import (
	"math"
	"strings"
	"testing"
	"time"
)

func mkRow(w, lang, cfg, name string) *row {
	return &row{workload: w, lang: lang, cfg: cfg, name: name, bin: "/bin/" + lang + cfg + name}
}

// TestSchedule: rounds outermost, workloads in order, a workload's rows
// rotated left by the round number; unbuilt rows are not scheduled.
func TestSchedule(t *testing.T) {
	a, b, c := mkRow("w", "c", "release", "w"), mkRow("w", "rust", "release", "w"), mkRow("w", "kern", "release", "w")
	solo := mkRow("v", "c", "release", "v")
	skipped := mkRow("w", "kern", "debug", "w")
	skipped.skip = "not built"
	failed := mkRow("v", "kern", "release", "v")
	failed.buildErr = "boom"
	got := schedule([]string{"w", "v"}, []*row{a, b, c, skipped, solo, failed}, 4)
	var order []string
	for _, s := range got {
		order = append(order, s.r.lang)
	}
	want := "c rust kern c | rust kern c c | kern c rust c | c rust kern c"
	var parts []string
	for k := 0; k < 4; k++ {
		parts = append(parts, strings.Join(order[k*4:k*4+4], " "))
	}
	if g := strings.Join(parts, " | "); g != want {
		t.Errorf("schedule = %s\nwant       %s", g, want)
	}
	for i, s := range got {
		if s.round != i/4 {
			t.Errorf("slot %d in round %d, want %d", i, s.round, i/4)
		}
	}
}

func TestValidateRuns(t *testing.T) {
	if err := validateRuns(4); err == nil || !strings.Contains(err.Error(), "at least 5") {
		t.Errorf("validateRuns(4) = %v", err)
	}
	if err := validateRuns(5); err != nil {
		t.Errorf("validateRuns(5) = %v", err)
	}
}

func run(round int, ops, ns uint64, rss float64) runResult {
	return runResult{round: round, ops: ops, ns: ns, rss: rss}
}

// TestPairedRatios: only rounds where both rows have a positive measure
// pair, in round order.
func TestPairedRatios(t *testing.T) {
	kern := &row{runs: []runResult{run(0, 900, 1000, 10), run(1, 1000, 1000, 0), run(3, 1100, 1000, 12)}}
	ref := &row{runs: []runResult{run(0, 1000, 1000, 10), run(1, 1000, 1000, 10), run(2, 1000, 1000, 10), run(3, 1000, 1000, 10)}}
	tp := pairedRatios(kern, ref, throughput)
	if len(tp) != 3 || math.Abs(tp[0]-0.9) > 1e-9 || math.Abs(tp[1]-1) > 1e-9 || math.Abs(tp[2]-1.1) > 1e-9 {
		t.Errorf("throughput ratios %v", tp)
	}
	rss := pairedRatios(kern, ref, peakRSS)
	if len(rss) != 2 || math.Abs(rss[1]-1.2) > 1e-9 {
		t.Errorf("RSS ratios %v: the round with RSS 0 must not pair", rss)
	}
}

// TestGeoMeanCI against a hand computation: ratios 0.9, 1.0, 1.1, t(2) =
// 4.303.
func TestGeoMeanCI(t *testing.T) {
	xs := []float64{0.9, 1.0, 1.1}
	logs := []float64{math.Log(0.9), 0, math.Log(1.1)}
	m := (logs[0] + logs[1] + logs[2]) / 3
	ss := 0.0
	for _, l := range logs {
		ss += (l - m) * (l - m)
	}
	half := 4.303 * math.Sqrt(ss/2) / math.Sqrt(3)
	mean, lo, hi, ok := geoMeanCI(xs)
	if !ok || math.Abs(mean-math.Exp(m)) > 1e-12 || math.Abs(lo-math.Exp(m-half)) > 1e-12 || math.Abs(hi-math.Exp(m+half)) > 1e-12 {
		t.Errorf("geoMeanCI = %v [%v, %v] %v, want %v [%v, %v]", mean, lo, hi, ok, math.Exp(m), math.Exp(m-half), math.Exp(m+half))
	}
	if _, _, _, ok := geoMeanCI([]float64{1}); ok {
		t.Error("one value has no interval")
	}
	if _, _, _, ok := geoMeanCI(nil); ok {
		t.Error("no values have no interval")
	}
}

func TestVerdicts(t *testing.T) {
	for _, c := range []struct {
		lo, hi float64
		ok     bool
		want   string
	}{
		{0.90, 1.2, true, verdictMeets}, {0.95, 1.0, true, verdictMeets},
		{0.89, 0.95, true, verdictUndecided}, {0.80, 0.8999, true, verdictMisses},
		{0.95, 1.0, false, verdictUndecided},
	} {
		if got := throughputVerdict(c.lo, c.hi, c.ok); got != c.want {
			t.Errorf("throughputVerdict(%v, %v, %v) = %q, want %q", c.lo, c.hi, c.ok, got, c.want)
		}
	}
	for _, c := range []struct {
		lo, hi float64
		ok     bool
		want   string
	}{
		{0.9, 1.10, true, verdictMeets}, {1.0, 1.05, true, verdictMeets},
		{1.05, 1.15, true, verdictUndecided}, {1.1001, 1.3, true, verdictMisses},
		{1.10, 1.3, true, verdictUndecided}, {1.0, 1.05, false, verdictUndecided},
	} {
		if got := memoryVerdict(c.lo, c.hi, c.ok); got != c.want {
			t.Errorf("memoryVerdict(%v, %v, %v) = %q, want %q", c.lo, c.hi, c.ok, got, c.want)
		}
	}
}

func rounds(n int, ops uint64, ns func(int) uint64, rss float64) []runResult {
	var out []runResult
	for k := 0; k < n; k++ {
		out = append(out, run(k, ops, ns(k), rss))
	}
	return out
}

func TestTargetLine(t *testing.T) {
	c := mkRow("w", "c", "release", "w")
	rust := mkRow("w", "rust", "release", "w")
	kern := mkRow("w", "kern", "release", "w")
	debug := mkRow("w", "kern", "debug", "w")
	// C and Rust tie on throughput (C chosen), Rust has the lower RSS;
	// kern is steadily 0.95 of C with a drifting host
	c.runs = rounds(6, 1000, func(k int) uint64 { return 1000 + uint64(k)*100 }, 100<<20)
	rust.runs = rounds(6, 1000, func(k int) uint64 { return 1000 + uint64(k)*100 }, 90<<20)
	kern.runs = rounds(6, 950, func(k int) uint64 { return 1000 + uint64(k)*100 }, 95<<20)
	debug.runs = rounds(6, 10, func(int) uint64 { return 1000 }, 1)
	line := targetLine([]*row{c, rust, kern, debug})
	for _, want := range []string{"throughput kern/c paired ratio 0.95 [0.95, 0.95] over 6 rounds, meets the 10% target", "peak RSS kern/rust paired ratio 1.06 [1.06, 1.06] over 6 rounds, meets the 10% target"} {
		if !strings.Contains(line, want) {
			t.Errorf("target line %q lacks %q", line, want)
		}
	}
	// a kern row that measured in only one round: undecided, no interval
	kern.runs = kern.runs[:1]
	if line := targetLine([]*row{c, rust, kern}); !strings.Contains(line, "throughput kern/c over 1 paired rounds, undecided on the 10% target") {
		t.Errorf("one paired round: %q", line)
	}
	kern.runs = nil
	if line := targetLine([]*row{c, rust, kern}); !strings.Contains(line, "kern release row not measured; throughput reference c, peak RSS reference rust") {
		t.Errorf("unmeasured kern: %q", line)
	}
	if line := targetLine([]*row{kern}); !strings.Contains(line, "no C or Rust baseline") {
		t.Errorf("no baseline: %q", line)
	}
	// a noisy kern row whose interval straddles 0.90
	kern.runs = []runResult{run(0, 800, 1000, 1), run(1, 1000, 1000, 1), run(2, 850, 1000, 1), run(3, 990, 1000, 1), run(4, 870, 1000, 1)}
	c.runs = c.runs[:5]
	for k := range c.runs {
		c.runs[k].ns = 1000
	}
	if line := targetLine([]*row{c, kern}); !strings.Contains(line, "undecided on the 10% target") {
		t.Errorf("straddling interval: %q", line)
	}
}

// TestLoadGate with a fake reader, clock and sleeper.
func TestLoadGate(t *testing.T) {
	clock := time.Unix(0, 0)
	newGate := func(max float64, loads ...float64) (*loadGate, *int, *[]string) {
		i, warnings := 0, []string{}
		g := &loadGate{max: max, wait: time.Minute, poll: 15 * time.Second,
			read: func() (float64, bool) {
				if loads[i] < 0 {
					return 0, false
				}
				v := loads[i]
				if i < len(loads)-1 {
					i++
				}
				return v, true
			},
			sleep: func(d time.Duration) { clock = clock.Add(d) },
			now:   func() time.Time { return clock },
			warn:  func(m string) { warnings = append(warnings, m) }}
		return g, &i, &warnings
	}
	if g, _, _ := newGate(0, 20); true {
		start := clock
		if load, ok, exp := g.before(); load != 20 || !ok || exp || clock != start {
			t.Errorf("gate off: %v %v %v, waited %v", load, ok, exp, clock.Sub(start))
		}
	}
	if g, _, _ := newGate(3.5, 2); true {
		if load, ok, exp := g.before(); load != 2 || !ok || exp {
			t.Errorf("low load: %v %v %v", load, ok, exp)
		}
	}
	if g, _, _ := newGate(3.5, 9, 6, 3); true {
		start := clock
		if load, ok, exp := g.before(); load != 3 || !ok || exp || clock.Sub(start) != 30*time.Second {
			t.Errorf("falling load: %v %v %v after %v", load, ok, exp, clock.Sub(start))
		}
	}
	if g, _, _ := newGate(3.5, 9); true {
		start := clock
		if load, ok, exp := g.before(); load != 9 || !ok || !exp || clock.Sub(start) < time.Minute {
			t.Errorf("timeout: %v %v %v after %v", load, ok, exp, clock.Sub(start))
		}
	}
	g, _, warnings := newGate(3.5, -1)
	for r := 0; r < 2; r++ {
		if _, ok, exp := g.before(); ok || exp {
			t.Errorf("unreadable load, round %d: ok %v expired %v", r, ok, exp)
		}
	}
	if len(*warnings) != 1 {
		t.Errorf("%d warnings for an unreadable load over two rounds, want 1", len(*warnings))
	}
	// a read that fails while waiting also warns, and the round starts
	g, _, warnings = newGate(3.5, 9, -1)
	if _, ok, exp := g.before(); ok || exp || len(*warnings) != 1 {
		t.Errorf("failure while waiting: ok %v expired %v, %d warnings", ok, exp, len(*warnings))
	}
}

func TestLoadSummary(t *testing.T) {
	got := loadSummary([]roundLoad{{load: 2.5, known: true}, {known: false}, {load: 4.25, known: true, expired: true}}, 3.5)
	want := "one-minute load at the start of each round: 2.50 to 4.25; unavailable in rounds 2; load gate 3.50, wait expired before rounds 3 (4.25)"
	if got != want {
		t.Errorf("loadSummary = %q\nwant %q", got, want)
	}
	if got := loadSummary([]roundLoad{{known: false}}, 0); got != "one-minute load at the start of each round: unavailable" {
		t.Errorf("all unavailable: %q", got)
	}
}

// TestRowTableLine pins a per-row table line: rounds change when rows run,
// not how a row's runs are summarized (spec R6).
func TestRowTableLine(t *testing.T) {
	r := mkRow("vector", "c", "release", "vector")
	r.runs = []runResult{run(0, 1000, 1000, 1<<20), run(1, 1000, 1000, 1<<20)}
	for i := range r.runs {
		r.runs[i].p50, r.runs[i].p99, r.runs[i].checksum = 5, 9, 42
	}
	r.text, r.compile, r.allocs, r.copies, r.places = 1234, 7*time.Millisecond, "n/a", "n/a", "n/a"
	report := writeReport(t.TempDir(), toolchain{}, []string{"vector"}, []*row{r}, 5, "", "")
	want := "| c | 1 | 1000.0 ± 0.0 | 5 ± 0 | 9 ± 0 | 1.0 ± 0.0 | 1234 | 7ms | 0 | n/a | n/a | n/a | no | n/a | 42 |"
	if !strings.Contains(report, want) {
		t.Errorf("row line missing; report:\n%s", report)
	}
}
