package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/analysis"
	"github.com/dshills/kern/compiler/backend/native"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/incremental"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/project"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// overBudget exceeds its bounds_checks budget at every level: pick's index
// is checked at run time.
const overBudget = `module main
budget { bounds_checks 0 }
func pick(xs: view[]u64, i: usize) -> u64 {
    return xs[i]
}
func main() -> i32 {
    a := [1u64, 2u64, 3u64]
    print_u64(pick(a[0usize:3usize], 1usize))
    println()
    return 0i32
}
`

// byLevel's helper index is runtime-checked at debug and proven at
// release (compiler/analysis TestBudgetLevel).
const byLevel = `module main
budget { bounds_checks 0 }
func pick(xs: view[]u64, i: usize) -> u64 {
    return xs[i]
}
func sum(xs: view[]u64) -> u64 {
    s := 0u64
    for i in 0usize..len(xs) {
        s = s + pick(xs, i)
    }
    return s
}
func main() -> i32 {
    a := [1u64, 2u64, 3u64, 4u64]
    print_u64(sum(a[0usize:4usize]))
    return 0i32
}
`

// writeProject writes files into a new directory and returns the path of
// the first name given.
func writeProject(t *testing.T, files ...string) string {
	t.Helper()
	dir := t.TempDir()
	for i := 0; i+1 < len(files); i += 2 {
		p := filepath.Join(dir, files[i])
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(files[i+1]), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, files[0])
}

func codesOf(resp diag.Response) []string {
	var out []string
	for _, d := range resp.Diagnostics {
		out = append(out, string(d.Code))
	}
	return out
}

// check and build enforce budgets (R5): exit 2 with BUDGET_EXCEEDED and
// its related spans; build writes no output.
func TestBudgetCheckAndBuild(t *testing.T) {
	main := writeProject(t, "main.kern", overBudget)
	code, resp, _ := runWith(t, []string{"check", main}, "")
	if code != 2 || resp.OK || len(resp.Diagnostics) != 1 || resp.Diagnostics[0].Code != "BUDGET_EXCEEDED" || len(resp.Diagnostics[0].Related) != 1 || resp.Diagnostics[0].Related[0].Label != "budget declared here" {
		t.Fatalf("check: exit %d %+v", code, resp)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"check", "--human", main}, strings.NewReader(""), &out, &errb); code != 2 ||
		strings.Count(out.String(), "\n") != 1 || !strings.Contains(out.String(), "[BUDGET_EXCEEDED rule 7.6]: function `pick` has 1 runtime-checked bounds check, budget 0: 4:12 `xs[i]`") {
		t.Fatalf("check --human: exit %d %q", code, out.String())
	}
	bin := filepath.Join(t.TempDir(), "out")
	for _, args := range [][]string{{"build", "-o", bin, main}, {"build", "--target=x86_64-unknown-linux-gnu", "-o", bin, main}, {"build", "--opt=release", "-o", bin, main}} {
		code, resp, _ := runWith(t, args, "")
		if code != 2 || fmt.Sprint(codesOf(resp)) != "[BUDGET_EXCEEDED]" {
			t.Fatalf("%v: exit %d %v", args, code, codesOf(resp))
		}
		if _, err := os.Stat(bin); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%v wrote %s", args, bin)
		}
	}
}

// The 12 locations of one function: the human line lists 10 and says how
// many more (R4).
func TestBudgetHumanManyLocations(t *testing.T) {
	var params, terms []string
	for i := 0; i < 12; i++ {
		params = append(params, fmt.Sprintf("i%d: usize", i))
		terms = append(terms, fmt.Sprintf("xs[i%d]", i))
	}
	main := writeProject(t, "m.kern", "module m\nbudget { bounds_checks 0 }\nfunc f(xs: view[]u64, "+strings.Join(params, ", ")+") -> u64 {\n    return "+strings.Join(terms, " + ")+"\n}\n")
	var out, errb bytes.Buffer
	if code := run([]string{"check", "--human", main}, strings.NewReader(""), &out, &errb); code != 2 || strings.Count(out.String(), "\n") != 1 ||
		strings.Count(out.String(), "`xs[") != 10 || !strings.HasSuffix(out.String(), "`xs[i9]` (test `i9 < len(xs)` on every path to it); and 2 more\n") {
		t.Fatalf("exit %d %q", code, out.String())
	}
}

// --opt selects the level the counts are taken at, debug by default, and
// the cache path reports the same (R5).
func TestBudgetCheckLevel(t *testing.T) {
	main := writeProject(t, "main.kern", byLevel)
	cache := t.TempDir()
	for _, c := range []struct {
		args []string
		fail bool
	}{
		{[]string{"check", main}, true},
		{[]string{"check", "--opt=debug", main}, true},
		{[]string{"check", "--opt=release", main}, false},
		{[]string{"check", "--cache=" + cache, main}, true},
		{[]string{"check", "--cache=" + cache, "--opt=release", main}, false},
		{[]string{"check", "--cache=" + cache, main}, true},
	} {
		code, resp, _ := runWith(t, c.args, "")
		if c.fail != (code == 2) || c.fail != (fmt.Sprint(codesOf(resp)) == "[BUDGET_EXCEEDED]") {
			t.Errorf("%v: exit %d %v", c.args, code, codesOf(resp))
		}
	}
	bin := filepath.Join(t.TempDir(), "out")
	if code, resp, _ := runWith(t, []string{"build", "--opt=release", "-o", bin, main}, ""); code != 0 {
		t.Fatalf("release build: exit %d %v", code, codesOf(resp))
	}
}

// A budget is evaluated only for a program that checks clean (R6): with a
// type error, check and build report the type error alone.
func TestBudgetAfterCleanCheck(t *testing.T) {
	main := writeProject(t, "main.kern", strings.Replace(overBudget, "return 0i32", "return true", 1))
	for _, args := range [][]string{{"check", main}, {"build", "-o", filepath.Join(t.TempDir(), "out"), main}} {
		code, resp, _ := runWith(t, args, "")
		if code != 2 || fmt.Sprint(codesOf(resp)) != "[TYPE_MISMATCH]" {
			t.Errorf("%v: exit %d %v", args, code, codesOf(resp))
		}
	}
}

// check loads the program's test files, so a budget in one is enforced.
func TestBudgetInTestFile(t *testing.T) {
	main := writeProject(t, "main.kern", "module main\nfunc main() -> i32 { return 0i32 }\n",
		"main_test.kern", "module main\nbudget { bounds_checks 0 }\nfunc at(xs: view[]u64, i: usize) -> u64 { return xs[i] }\nfunc test_at() {\n    a := [1u64]\n    expect(at(a[0usize:1usize], 0usize) == 1u64)\n}\n")
	if code, resp, _ := runWith(t, []string{"check", main}, ""); code != 2 || fmt.Sprint(codesOf(resp)) != "[BUDGET_EXCEEDED]" || resp.Diagnostics[0].File != filepath.Join(filepath.Dir(main), "main_test.kern") {
		t.Fatalf("exit %d %+v", code, resp.Diagnostics)
	}
}

// No other command evaluates budgets (R5): each runs on an over-budget
// program as it would without the budget.
func TestBudgetNotEnforced(t *testing.T) {
	main := writeProject(t, "main.kern", overBudget, "main_test.kern", "module main\nfunc test_pick() {\n    a := [5u64, 6u64]\n    expect(pick(a[0usize:2usize], 1usize) == 6u64)\n}\n")
	var out, errb bytes.Buffer
	if code := run([]string{"run", main}, strings.NewReader(""), &out, &errb); code != 0 || out.String() != "2\n" {
		t.Fatalf("run: exit %d %q %s", code, out.String(), errb.String())
	}
	if code, resp, _ := runWith(t, []string{"test", main}, ""); code != 0 || !resp.OK {
		t.Fatalf("test: exit %d %+v", code, resp)
	}
	edit := fmt.Sprintf(`{"schema_version":"0.4","command":"apply-structured-edit","files":[%q],"edit":{"ops":[{"op":"insert","file":%q,"at":0,"text":"// x\n"}]}}`, main, main)
	cases := []struct {
		args  []string
		stdin string
		clang bool
	}{
		{[]string{"show-bounds-checks", main}, "", false},
		{[]string{"show-copies", main}, "", false},
		{[]string{"show-allocations", main}, "", false},
		{[]string{"show-layout", main}, "", false},
		{[]string{"show-effects", main}, "", false},
		{[]string{"explain-ownership", "--line=4", main}, "", false},
		{[]string{"emit-ir", main}, "", false},
		{[]string{"format", main}, "", false},
		{[]string{"apply-structured-edit"}, edit, false},
		{[]string{"emit-asm", main}, "", true},
	}
	for _, c := range cases {
		if c.clang && !native.Available() {
			continue
		}
		code, resp, _ := runWith(t, c.args, c.stdin)
		if code != 0 || !resp.OK {
			t.Errorf("%v: exit %d %v", c.args, code, codesOf(resp))
		}
	}
}

// countingLower is Build with a call counter.
func countingLower(n *int) lowerFunc {
	return func(srcs []*source.File, optLevel string) (*ir.Module, []diag.Diagnostic, error) {
		*n++
		return Build(srcs, optLevel)
	}
}

// checkDirect runs runCheck on paths as the dispatcher does, with lower.
func checkDirect(t *testing.T, paths []string, human bool, cacheDir string, lower lowerFunc) (int, string) {
	t.Helper()
	return checkDirectWith(t, paths, human, cacheDir, budgetDeps{lower: lower, classify: analysis.ClassifyBounds})
}

// checkDirectWith is checkDirect with both of the evaluation's dependencies.
func checkDirectWith(t *testing.T, paths []string, human bool, cacheDir string, deps budgetDeps) (int, string) {
	t.Helper()
	cwd, _ := os.Getwd()
	pr := project.LoadTests(cwd, paths)
	if len(pr.Diags) > 0 {
		return 2, pr.Diags[0].Message
	}
	req := Request{SchemaVersion: diag.SchemaVersion, Command: "check", Files: pr.Names(), Target: native.HostTriple()}
	cfg := incremental.Config{Opt: "debug", Env: "hosted", Target: req.Target}
	var out, errb bytes.Buffer
	code := runCheck(req, diag.SchemaVersion, pr.Files, human, cacheDir, cfg, freestandingTriple(""), &out, &errb, checkDeps{budget: deps, hosted: hostedDefaults()})
	return code, out.String()
}

// R6: a request without a budget block never lowers for budgets, on the
// direct and the cached path, in JSON and human output, over every
// fixture under tests/ that has none and a program importing std; a
// budgeted one lowers once.
func TestBudgetNoCostWithout(t *testing.T) {
	var paths []string
	root := filepath.Join("..", "..", "tests")
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".kern") && !project.TestFile(p) {
			if b, rerr := os.ReadFile(p); rerr == nil && !bytes.Contains(b, []byte("budget {")) && !bytes.Contains(b, []byte("budget{")) {
				paths = append(paths, p)
			}
		}
		return nil
	})
	if len(paths) < 100 {
		t.Fatalf("only %d fixtures", len(paths))
	}
	paths = append(paths, writeProject(t, "s.kern", "module main\nimport \"std/str\"\nfunc main() -> i32 {\n    s := str.String.new()\n    s.push(65u8)\n    return 0i32\n}\n"))
	calls := 0
	lower := countingLower(&calls)
	cache := t.TempDir()
	for _, p := range paths {
		checkDirect(t, []string{p}, false, "", lower)
		checkDirect(t, []string{p}, true, "", lower)
		checkDirect(t, []string{p}, false, cache, lower)
	}
	if calls != 0 {
		t.Fatalf("%d lowerings for requests without a budget", calls)
	}
	main := writeProject(t, "main.kern", overBudget)
	for _, c := range []string{"", cache} {
		before := calls
		if code, out := checkDirect(t, []string{main}, false, c, lower); code != 2 || !strings.Contains(out, "BUDGET_EXCEEDED") || calls != before+1 {
			t.Fatalf("cache %q: exit %d, %d calls, %s", c, code, calls-before, out)
		}
	}
}

// budgetDiags hands lower's diagnostics back unchanged and reports a
// lowering error or a missing module as an internal error wrapping the
// cause.
func TestBudgetLowerOutcomes(t *testing.T) {
	srcs := []*source.File{{Name: "main.kern", Bytes: []byte(overBudget)}}
	cause := errors.New("boom")
	failing := func([]*source.File, string) (*ir.Module, []diag.Diagnostic, error) { return nil, nil, cause }
	if _, err := budgetDiags(srcs, "debug", "", budgetDeps{lower: failing, classify: analysis.ClassifyBounds}); !errors.Is(err, cause) || !errors.As(err, new(*InternalError)) || !strings.Contains(err.Error(), "budget evaluation: lowering: boom") {
		t.Errorf("error: %v", err)
	}
	d := diag.New("TYPE_MISMATCH", "main.kern", diag.Span{}, "x")
	disagreeing := func([]*source.File, string) (*ir.Module, []diag.Diagnostic, error) {
		return nil, []diag.Diagnostic{d}, nil
	}
	if ds, err := budgetDiags(srcs, "debug", "", budgetDeps{lower: disagreeing, classify: analysis.ClassifyBounds}); err != nil || len(ds) != 1 || ds[0].Code != "TYPE_MISMATCH" {
		t.Errorf("diagnostics: %v %v", ds, err)
	}
	empty := func([]*source.File, string) (*ir.Module, []diag.Diagnostic, error) { return nil, nil, nil }
	if _, err := budgetDiags(srcs, "debug", "", budgetDeps{lower: empty, classify: analysis.ClassifyBounds}); !errors.As(err, new(*InternalError)) {
		t.Errorf("no module: %v", err)
	}
	got := ""
	levels := func(_ []*source.File, optLevel string) (*ir.Module, []diag.Diagnostic, error) {
		got = optLevel
		return Build(srcs, optLevel)
	}
	if _, err := budgetDiags(srcs, "release", "", budgetDeps{lower: levels, classify: analysis.ClassifyBounds}); err != nil || got != "release" {
		t.Errorf("level %q: %v", got, err)
	}
}

// A function is found for renaming past its budget block, which may hold
// the same word as its name (review finding: the search started at the
// declaration, inside the block).
func TestBudgetRenameFunction(t *testing.T) {
	for _, name := range []string{"budget", "copies", "bounds_checks"} {
		src := "module m\ncontract { requires \"" + name + " holds\" }\nbudget { bounds_checks 0; copies 0 }\nfunc " + name + "(x: u64) -> u64 { return x }\nfunc g() -> u64 { return " + name + "(1u64) }\n"
		path := writeProject(t, "m.kern", src)
		at := strings.Index(src, "func "+name) + len("func ")
		req := fmt.Sprintf(`{"schema_version":"0.4","command":"apply-structured-edit","files":[%q],"edit":{"ops":[{"op":"rename_symbol","file":%q,"span":{"start":%d,"end":%d},"new_name":"limit"}]}}`, path, path, at, at+len(name))
		code, resp, _ := runWith(t, []string{"apply-structured-edit"}, req)
		if code != 0 || !resp.OK {
			t.Fatalf("%s: exit %d %v", name, code, codesOf(resp))
		}
		got := resp.Result.(map[string]any)["files"].([]any)[0].(map[string]any)["source"].(string)
		if !strings.Contains(got, "func limit(x: u64)") || !strings.Contains(got, "return limit(1u64)") || !strings.Contains(got, "budget { bounds_checks 0; copies 0 }") {
			t.Fatalf("%s: renamed to\n%s", name, got)
		}
	}
}

// Functions outside test files are counted over the program the show
// queries see, without its test files: a generic instantiated only by a
// test has no entries, as show-bounds-checks reports (review finding).
// A budget in a test file is still counted, over the program with tests,
// and both kinds are reported together.
func TestBudgetTestFilesNotCounted(t *testing.T) {
	main := writeProject(t, "main.kern", "module main\nbudget { bounds_checks 0 }\nfunc pick[T: Copy](xs: view[]T, i: usize) -> T { return xs[i] }\nfunc main() -> i32 { return 0i32 }\n",
		"main_test.kern", "module main\nfunc test_pick() {\n    a := [1u64, 2u64]\n    expect(pick[u64](a[0usize:2usize], 1usize) == 2u64)\n}\n")
	if code, resp, _ := runWith(t, []string{"check", main}, ""); code != 0 {
		t.Fatalf("check: exit %d %v", code, codesOf(resp))
	}
	_, show, _ := runWith(t, []string{"show-bounds-checks", main}, "")
	if checks := show.Result.(map[string]any)["checks"].([]any); len(checks) != 0 {
		t.Fatalf("show-bounds-checks lists %v", checks)
	}
	both := writeProject(t, "main.kern", overBudget,
		"main_test.kern", "module main\nbudget { bounds_checks 0 }\nfunc at(xs: view[]u64, i: usize) -> u64 { return xs[i] }\nfunc test_at() {\n    a := [1u64]\n    expect(at(a[0usize:1usize], 0usize) == 1u64)\n}\n")
	code, resp, _ := runWith(t, []string{"check", both}, "")
	if code != 2 || len(resp.Diagnostics) != 2 {
		t.Fatalf("exit %d %+v", code, resp.Diagnostics)
	}
	if f0, f1 := filepath.Base(resp.Diagnostics[0].File), filepath.Base(resp.Diagnostics[1].File); f0 != "main.kern" || f1 != "main_test.kern" {
		t.Fatalf("files %s, %s", f0, f1)
	}
	calls := 0
	if code, _ := checkDirect(t, []string{both}, false, "", countingLower(&calls)); code != 2 || calls != 2 {
		t.Fatalf("exit %d, %d lowerings, want 2 (the program, then with its tests)", code, calls)
	}
}

// countingClassify is ClassifyBounds with a call counter.
func countingClassify(n *int) analysis.Classifier {
	return func(prog *resolve.Program, srcs []*source.File, snap *analysis.BoundsSnapshot, want func(diag.BoundsCheck) bool) (map[int]analysis.Reason, analysis.ClassifyStats) {
		*n++
		return analysis.ClassifyBounds(prog, srcs, snap, want)
	}
}

// check classifies only for an exceeded bounds_checks item, once, on the
// direct and the cached path, and its message carries the hint
// (specs/unproven-reasons R4, R5). build takes the same evaluation
// (budgetDiags with budgetDefaults); run and test evaluate no budget.
func TestBudgetClassifyCost(t *testing.T) {
	cache := t.TempDir()
	for _, tc := range []struct {
		name, src string
		calls     int
	}{
		{"no budget", "module main\nfunc pick(xs: view[]u64, i: usize) -> u64 { return xs[i] }\nfunc main() -> i32 { return 0i32 }\n", 0},
		{"met", "module main\nbudget { bounds_checks 1 }\nfunc pick(xs: view[]u64, i: usize) -> u64 { return xs[i] }\nfunc main() -> i32 { return 0i32 }\n", 0},
		{"copies only", "module main\nstruct P { x: u64; y: u64 }\nbudget { copies 0; bounds_checks 3 }\nfunc two(ps: view[[4]P], i: usize) -> u64 {\n    p := ps[0usize]\n    return p.x + ps[i].y\n}\nfunc main() -> i32 { return 0i32 }\n", 0},
		{"exceeded", overBudget, 1},
	} {
		for _, c := range []string{"", cache} {
			calls := 0
			main := writeProject(t, "main.kern", tc.src)
			code, out := checkDirectWith(t, []string{main}, false, c, budgetDeps{lower: Build, classify: countingClassify(&calls)})
			if calls != tc.calls {
				t.Errorf("%s (cache %q): %d classifications, want %d", tc.name, c, calls, tc.calls)
			}
			if tc.calls > 0 && (code != 2 || !strings.Contains(out, "(test `i < len(xs)` on every path to it)")) {
				t.Errorf("%s: exit %d, no hint in %s", tc.name, code, out)
			}
		}
	}
}
