package main

// Calibration of a task suite without a model (specs/agent-suite-v4):
// frozen functions, the natural solution's two-step validation, the
// listing of its runtime-checked sites, the audited idiom mapping and
// the mechanical checks on a task's programs.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// frozenText returns the source text of the top-level function name in
// files under dir: from the first line beginning `func <name>(` through
// the next line that is exactly `}`. In canonical form every brace inside
// a function is indented, so that line is the function's own closing
// brace. ok is false when no such function is found.
func frozenText(dir string, files []string, name string) (string, bool) {
	head := "func " + name + "("
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			continue
		}
		lines := strings.Split(string(b), "\n")
		for i, l := range lines {
			if !strings.HasPrefix(l, head) {
				continue
			}
			for j := i; j < len(lines); j++ {
				if lines[j] == "}" {
					return strings.Join(lines[i:j+1], "\n"), true
				}
			}
			return "", false
		}
	}
	return "", false
}

// frozenDiff checks the frozen functions of a solution in dir against the
// start's; it returns "" when every one is unchanged, else the failure.
func frozenDiff(startDir, dir string, files, names []string) string {
	for _, name := range names {
		want, ok := frozenText(startDir, files, name)
		if !ok {
			return fmt.Sprintf("function %s is missing from the starting sources", name)
		}
		got, ok := frozenText(dir, files, name)
		if !ok {
			return fmt.Sprintf("function %s is missing", name)
		}
		if got != want {
			return fmt.Sprintf("function %s was changed", name)
		}
	}
	return ""
}

// classifyNatural decides a natural solution's calibration (spec R5, R6):
// everything but the budget must pass (restOK), and its runtime checks
// must exceed the budget by at least 3. ok is false on a calibration
// error, with the reason.
func classifyNatural(restOK bool, restDetail string, runtime, budget int) (bool, string) {
	if !restOK {
		return false, "natural solution fails a check other than the budget: " + restDetail
	}
	if runtime <= budget {
		return false, fmt.Sprintf("natural solution meets the budget (%d runtime checks, at most %d): no discrimination", runtime, budget)
	}
	if runtime < budget+3 {
		return false, fmt.Sprintf("natural solution exceeds the budget by %d, less than 3", runtime-budget)
	}
	return true, fmt.Sprintf("fails on the budget alone (%d runtime checks, at most %d)", runtime, budget)
}

// naturalVerdict validates a task's natural solution in two independent
// steps: the hidden suite with the budget removed, then the budget.
func naturalVerdict(ctx context.Context, root, kernBin string, t *task) (bool, string) {
	rest := *t
	rest.Hidden.MaxRuntimeBoundsChecks = nil
	restOK, restDetail := hiddenPasses(ctx, root, kernBin, &rest, t.Natural)
	budget := 0
	if t.Hidden.MaxRuntimeBoundsChecks != nil {
		budget = *t.Hidden.MaxRuntimeBoundsChecks
	} else {
		return false, "the task sets no max_runtime_bounds_checks to calibrate against"
	}
	var files []string
	for _, f := range t.Files {
		files = append(files, filepath.Join(t.Natural, f))
	}
	return classifyNatural(restOK, restDetail, countRuntimeBoundsChecks(ctx, t.Natural, kernBin, files), budget)
}

// site is one runtime-checked bounds check: its enclosing function and
// its source text.
type site struct {
	Function string `json:"function"`
	Site     string `json:"site"`
}

var funcLine = regexp.MustCompile(`^\s*func (\w+)`)

// runtimeSites lists the runtime-checked bounds checks of the sources in
// dir, in source order, each with its enclosing function.
func runtimeSites(ctx context.Context, kernBin, dir string, files []string) ([]site, error) {
	var paths []string
	for _, f := range files {
		paths = append(paths, filepath.Join(dir, f))
	}
	out, err := runCmdCtx(ctx, dir, kernBin, append([]string{"show-bounds-checks"}, paths...)...)
	if err != nil {
		return nil, fmt.Errorf("show-bounds-checks: %v", err)
	}
	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			Checks []struct {
				Loc struct {
					File string `json:"file"`
					Span struct {
						Start int `json:"start"`
						End   int `json:"end"`
					} `json:"span"`
				} `json:"loc"`
				Kind  string `json:"kind"`
				Label string `json:"label"`
			} `json:"checks"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil || !resp.OK {
		return nil, fmt.Errorf("show-bounds-checks gave no result")
	}
	texts := map[string][]byte{}
	type located struct {
		file  string
		start int
		s     site
	}
	var all []located
	for _, c := range resp.Result.Checks {
		if c.Kind != "bounds" || c.Label != "runtime-checked" {
			continue
		}
		src, ok := texts[c.Loc.File]
		if !ok {
			b, err := os.ReadFile(c.Loc.File)
			if err != nil {
				return nil, err
			}
			texts[c.Loc.File], src = b, b
		}
		if c.Loc.Span.Start < 0 || c.Loc.Span.End > len(src) || c.Loc.Span.Start > c.Loc.Span.End {
			return nil, fmt.Errorf("a check's span is outside %s", c.Loc.File)
		}
		fn := ""
		for _, l := range strings.Split(string(src[:c.Loc.Span.Start]), "\n") {
			if m := funcLine.FindStringSubmatch(l); m != nil {
				fn = m[1]
			}
		}
		all = append(all, located{c.Loc.File, c.Loc.Span.Start, site{Function: fn, Site: string(src[c.Loc.Span.Start:c.Loc.Span.End])}})
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].file != all[j].file {
			return all[i].file < all[j].file
		}
		return all[i].start < all[j].start
	})
	sites := make([]site, len(all))
	for i, l := range all {
		sites[i] = l.s
	}
	return sites, nil
}

// idioms are spec §1.2's names; "other" marks a site that does not count.
var idioms = map[string]bool{
	"transitive bound": true, "mirrored index": true, "midpoint": true, "byte into a table": true,
	"offset digit": true, "window": true, "last element": true, "grid": true, "modulo": true,
	"argmax": true, "returned index": true, "two pointers": true, "heap children": true,
	"length relation": true,
}

// mappedSite is one entry of calibration/idioms.json.
type mappedSite struct {
	Function string `json:"function"`
	Site     string `json:"site"`
	Idiom    string `json:"idiom"`
}

// auditIdioms checks a task's idiom mapping against the natural
// solution's sites (spec R1): the same sites with the same multiplicity,
// every idiom known or "other", and at least 6 qualifying sites over at
// least 3 idioms and 2 required functions.
func auditIdioms(sites []site, mapping []mappedSite, required []string) error {
	count := map[site]int{}
	for _, s := range sites {
		count[s]++
	}
	for _, m := range mapping {
		if !idioms[m.Idiom] && m.Idiom != "other" {
			return fmt.Errorf("unknown idiom %q for %s: %s", m.Idiom, m.Function, m.Site)
		}
		k := site{Function: m.Function, Site: m.Site}
		if count[k] == 0 {
			return fmt.Errorf("mapped site %s: %s is not a runtime-checked site of the natural solution", m.Function, m.Site)
		}
		count[k]--
	}
	for k, n := range count {
		if n > 0 {
			return fmt.Errorf("runtime-checked site %s: %s is not mapped", k.Function, k.Site)
		}
	}
	isRequired := map[string]bool{}
	for _, r := range required {
		isRequired[r[strings.LastIndex(r, ".")+1:]] = true
	}
	qualifying, kinds, fns := 0, map[string]bool{}, map[string]bool{}
	for _, m := range mapping {
		if m.Idiom == "other" {
			continue
		}
		qualifying++
		kinds[m.Idiom] = true
		if isRequired[m.Function] {
			fns[m.Function] = true
		}
	}
	switch {
	case qualifying < 6:
		return fmt.Errorf("%d qualifying sites, at least 6 needed", qualifying)
	case len(kinds) < 3:
		return fmt.Errorf("%d distinct idioms, at least 3 needed", len(kinds))
	case len(fns) < 2:
		return fmt.Errorf("qualifying sites in %d required functions, at least 2 needed", len(fns))
	}
	return nil
}

var stdImport = regexp.MustCompile(`(?m)^\s*import\s+"std/`)

// mechanicalChecks applies spec R4's line count and import rule to the
// start, reference and natural sources, the reference's run time, and
// for a review task the reference's added unsafe; nil when all hold.
func mechanicalChecks(ctx context.Context, kernBin string, t *task) []string {
	var errs []string
	dirs := map[string]string{"start": filepath.Join(t.Dir, "start"), "reference": filepath.Join(t.Dir, "reference"), "natural": t.Natural}
	for _, which := range []string{"start", "reference", "natural"} {
		for _, f := range t.Files {
			b, err := os.ReadFile(filepath.Join(dirs[which], f))
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s/%s: %v", which, f, err))
				continue
			}
			n := strings.Count(strings.TrimRight(string(b), "\n"), "\n") + 1
			if n < 60 || n > 160 {
				errs = append(errs, fmt.Sprintf("%s/%s has %d lines, not 60 to 160", which, f, n))
			}
			if stdImport.Match(b) {
				errs = append(errs, fmt.Sprintf("%s/%s imports a std module", which, f))
			}
		}
	}
	var files []string
	for _, f := range t.Files {
		files = append(files, filepath.Join(dirs["reference"], f))
	}
	start := time.Now()
	if _, err := runProgram(ctx, dirs["reference"], kernBin, files); err != nil {
		errs = append(errs, fmt.Sprintf("the reference does not run: %v", err))
	} else if d := time.Since(start); d > 6*time.Second {
		errs = append(errs, fmt.Sprintf("the reference's run took %v, more than 6s", d.Round(time.Millisecond)))
	}
	if t.Kind == "review" && unsafeLineCount(dirs["reference"], t.Files) <= unsafeLineCount(dirs["start"], t.Files) {
		errs = append(errs, "the review task's reference adds no unsafe lines")
	}
	return errs
}

// calibrate runs every calibration check of a task with a natural
// solution and prints the results; it returns false on any error.
func calibrate(ctx context.Context, root, kernBin string, t *task) bool {
	ok := true
	report := func(format string, args ...any) { fmt.Printf("%s: "+format+"\n", append([]any{t.Name}, args...)...) }
	fmt.Printf("%s: natural measures %s\n", t.Name, measure(ctx, kernBin, t, t.Natural))
	good, why := naturalVerdict(ctx, root, kernBin, t)
	report("natural solution %s", why)
	ok = ok && good
	sites, err := runtimeSites(ctx, kernBin, t.Natural, t.Files)
	if err != nil {
		report("CALIBRATION ERROR listing sites: %v", err)
		return false
	}
	for _, s := range sites {
		report("  runtime-checked %s: %s", s.Function, s.Site)
	}
	if b, err := os.ReadFile(filepath.Join(t.Dir, "calibration", "idioms.json")); err == nil {
		var mapping []mappedSite
		if err := json.Unmarshal(b, &mapping); err != nil {
			report("CALIBRATION ERROR idioms.json: %v", err)
			ok = false
		} else if err := auditIdioms(sites, mapping, t.Hidden.RequiredFunctions); err != nil {
			report("CALIBRATION ERROR idiom audit: %v", err)
			ok = false
		} else {
			report("idiom audit passes (%d sites mapped)", len(mapping))
		}
	} else {
		report("CALIBRATION ERROR no calibration/idioms.json")
		ok = false
	}
	for _, e := range mechanicalChecks(ctx, kernBin, t) {
		report("CALIBRATION ERROR %s", e)
		ok = false
	}
	if !good {
		report("CALIBRATION ERROR %s", why)
	}
	return ok
}
