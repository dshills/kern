package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/analysis"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/opt"
	"github.com/dshills/kern/compiler/source"
)

// suitePrograms lists the program directories of the agent suites the
// sweep covers: every task's reference, start and natural solution.
func suitePrograms(t *testing.T) []string {
	t.Helper()
	var dirs []string
	for _, suite := range []string{"tasks", "tasks-budgeted"} {
		tasks, err := filepath.Glob(filepath.Join("..", "..", "bench", "agent", suite, "t*"))
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range tasks {
			for _, sub := range []string{"reference", "start", filepath.Join("calibration", "natural")} {
				if st, err := os.Stat(filepath.Join(task, sub)); err == nil && st.IsDir() {
					dirs = append(dirs, filepath.Join(task, sub))
				}
			}
		}
	}
	sort.Strings(dirs)
	return dirs
}

func kernFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".kern") {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	return files
}

// Every unproven bounds entry of every agent-suite program that checks,
// at both levels, gets one of the nine reasons and a hint, and every
// entry's access is located (specs/unproven-reasons R7, R8, §2).
func TestReasonsSuiteSweep(t *testing.T) {
	dirs := suitePrograms(t)
	if len(dirs) != 60 {
		t.Fatalf("%d suite program directories, want 60 (2 suites x 10 tasks x 3)", len(dirs))
	}
	total, programs, otherPlace := 0, 0, 0
	codes := map[string]int{}
	for _, dir := range dirs {
		var srcs []*source.File
		for _, f := range kernFiles(t, dir) {
			s, err := source.Load(f)
			if err != nil {
				t.Fatal(err)
			}
			srcs = append(srcs, s)
		}
		for _, level := range []string{"debug", "release"} {
			prog, mod, ds, err := buildProg(srcs, level, opt.OptionsFor(level))
			if err != nil {
				t.Fatalf("%s %s: %v", dir, level, err)
			}
			if len(ds) > 0 {
				continue // a start that does not check (the review task)
			}
			programs++
			snap := analysis.Snapshot(mod)
			reasons, st := analysis.ClassifyBounds(prog, srcs, snap, nil)
			if st.Unlocated != 0 {
				t.Errorf("%s %s: %d entries not located", dir, level, st.Unlocated)
			}
			text := map[string][]byte{}
			for _, s := range srcs {
				text[s.Name] = s.Bytes
			}
			for i, c := range snap.Result.Checks {
				r, ok := reasons[i]
				unproven := c.Kind == diag.CheckBounds && c.Label == diag.LabelRuntimeChecked
				if ok != unproven {
					t.Errorf("%s %s: entry %v: classified %v, unproven %v", dir, level, c.Loc, ok, unproven)
					continue
				}
				if !ok {
					continue
				}
				total++
				codes[r.Code]++
				if r.Code == analysis.ReasonSliceHighBound || r.Code == analysis.ReasonSliceLowBound {
					// the hint's guarantee is for a place that is a name or a
					// field path of a name (specs/prove-nonstrict C4)
					quoted := string(text[c.Loc.File][c.Loc.Span.Start:c.Loc.Span.End])
					if j := strings.IndexByte(quoted, '['); j < 0 || !placePath.MatchString(quoted[:j]) {
						otherPlace++
					}
				}
				if !slices.Contains(analysis.ReasonCodes, r.Code) || r.Hint == "" {
					t.Errorf("%s %s: entry %v: reason %q, hint %q", dir, level, c.Loc, r.Code, r.Hint)
				}
			}
		}
	}
	if programs < 100 || total < 100 {
		t.Errorf("only %d programs and %d unproven entries swept", programs, total)
	}
	t.Logf("%d programs, %d unproven entries: %v; %d slice entries whose place is not a name or a field path of a name", programs, total, codes, otherPlace)
}

// placePath is a name or a field path of a name.
var placePath = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)

// The fields reach the CLI's JSON for a suite start.
func TestReasonsSuiteCLI(t *testing.T) {
	files := kernFiles(t, filepath.Join("..", "..", "bench", "agent", "tasks", "t04_image_ops", "start"))
	code, out, _ := invoke(t, "", append([]string{"show-bounds-checks"}, files...)...)
	var resp struct {
		Result diag.ShowBoundsChecksResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil || code != 0 {
		t.Fatalf("exit %d, %v\n%s", code, err, out)
	}
	n := 0
	for _, c := range resp.Result.Checks {
		if c.Kind == diag.CheckBounds && c.Label == diag.LabelRuntimeChecked {
			n++
			if c.Reason == nil || c.Hint == nil || *c.Hint == "" {
				t.Errorf("%v: reason %v, hint %v", c.Loc, c.Reason, c.Hint)
			}
		}
	}
	if n == 0 {
		t.Error("the t04 start has no runtime-checked bounds check")
	}
}
