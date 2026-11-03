package analysis_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/analysis"
	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/lower"
	"github.com/dshills/kern/compiler/opt"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// budgetProgram is a program built as the show queries build it.
type budgetProgram struct {
	prog *resolve.Program
	srcs []*source.File
	mod  *ir.Module
}

func buildBudgeted(t *testing.T, files map[string]string, level string) budgetProgram {
	t.Helper()
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var srcs []*source.File
	for _, n := range names {
		srcs = append(srcs, &source.File{Name: n, Bytes: []byte(files[n])})
	}
	prog := resolve.ResolveFor(srcs, level == "release")
	if len(prog.Diags) > 0 {
		t.Fatalf("resolve: %s", prog.Diags[0].Message)
	}
	if ds := check.Check(prog); len(ds) > 0 {
		t.Fatalf("check: %s", ds[0].Message)
	}
	mod, ds := lower.Lower(prog)
	if len(ds) > 0 {
		t.Fatalf("lower: %s", ds[0].Message)
	}
	if err := opt.Run(mod, opt.OptionsFor(level)); err != nil {
		t.Fatalf("opt: %v", err)
	}
	return budgetProgram{prog, srcs, mod}
}

func (p budgetProgram) budgets() []diag.Diagnostic {
	return analysis.Budgets(p.prog, p.srcs, p.mod, "", analysis.ClassifyBounds)
}

func budgetsAt(t *testing.T, src, level string) []diag.Diagnostic {
	t.Helper()
	return buildBudgeted(t, map[string]string{"m.kern": src}, level).budgets()
}

var budgetMsgRe = regexp.MustCompile("^function `([^`]+)` has ([0-9]+) (runtime-checked bounds checks?|cop(?:y|ies)|allocation sites?), budget ([0-9]+): ")

// counts maps "function/item" to the count each BUDGET_EXCEEDED reports.
func counts(t *testing.T, ds []diag.Diagnostic) map[string]int {
	t.Helper()
	items := map[string]string{"runtime-checked bounds check": "bounds_checks", "runtime-checked bounds checks": "bounds_checks", "copy": "copies", "copies": "copies", "allocation site": "allocations", "allocation sites": "allocations"}
	out := map[string]int{}
	for _, d := range ds {
		if d.Code != "BUDGET_EXCEEDED" || d.Rule != "7.6" {
			t.Fatalf("unexpected %s rule %s: %s", d.Code, d.Rule, d.Message)
		}
		m := budgetMsgRe.FindStringSubmatch(d.Message)
		if m == nil {
			t.Fatalf("message %q", d.Message)
		}
		n, _ := strconv.Atoi(m[2])
		out[m[1]+"/"+items[m[3]]] = n
	}
	return out
}

func equalCounts(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

const allZero = "budget { bounds_checks 0; copies 0; allocations 0 }\n"

// attributionSrc holds one function per attribution rule of spec §7.6,
// each with every budget at 0, so each exceeded item reports its count.
const attributionSrc = `module main
struct P { x: u64; y: u64 }
` + allZero + `func copies(ps: view[[4]P]) -> u64 {
    p := ps[0usize]
    q := ps[1usize]
    return p.x + q.y
}
` + allZero + `func allocs() -> u64 {
    b := Box.new(7u64)
    v := Vec[u64].new()
    v.push(1u64)
    v.push(2u64)
    return 1u64
}
` + allZero + `func apply(f: closure(usize) -> u64, i: usize) -> u64 { return f(i) }
` + allZero + `func closure(xs: view[]u64, i: usize) -> u64 {
    return apply(|j: usize| xs[j], i)
}
` + allZero + `func gen[T: Copy](xs: view[]T, i: usize) -> T { return xs[i] }
` + allZero + `func unused[T: Copy](xs: view[]T, i: usize) -> T { return xs[i] }
impl P {
    ` + allZero + `    func pick(view self, xs: view[]u64, i: usize) -> u64 { return xs[i] + self.x }
}
` + allZero + `func main() -> i32 {
    a := [P { x: 1u64, y: 2u64 }, P { x: 1u64, y: 2u64 }, P { x: 1u64, y: 2u64 }, P { x: 1u64, y: 2u64 }]
    xs := [1u64, 2u64, 3u64]
    s := copies(a) + allocs() + closure(xs[0usize:3usize], 0usize) + gen[u64](xs[0usize:3usize], 1usize) + a[0usize].pick(xs[0usize:3usize], 2usize)
    print_u64(s)
    return 0i32
}
`

// Entries belong to the declaration whose span holds them (spec §7.6): a
// library call that allocates counts in its caller, a closure's index in
// the enclosing function, a generic function counts its instantiations'
// entries at its own locations and an uninstantiated one has none, and a
// method is named Type.method.
func TestBudgetAttribution(t *testing.T) {
	want := map[string]int{
		"copies/copies":         2,
		"allocs/allocations":    3,
		"closure/bounds_checks": 1,
		"gen/bounds_checks":     1,
		"P.pick/bounds_checks":  1,
	}
	for _, level := range []string{"debug", "release"} {
		got := counts(t, budgetsAt(t, attributionSrc, level))
		if !equalCounts(got, want) {
			t.Errorf("%s: got %v, want %v", level, got, want)
		}
	}
}

// A helper's unproven index belongs to the helper, also where the release
// inliner copied it into the caller and dropped the helper; the caller's
// budget does not count it.
func TestBudgetInlinedHelper(t *testing.T) {
	src := `module main
budget { bounds_checks 0 }
func pick(xs: view[]u64, i: usize) -> u64 {
    return xs[i]
}
budget { bounds_checks 0 }
func pick_any(xs: view[]u64) -> u64 {
    s := 0u64
    for i in 0usize..len(xs) {
        s = s + pick(xs, i + 1usize)
    }
    return s
}
func main() -> i32 {
    a := [1u64, 2u64, 3u64, 4u64]
    print_u64(pick_any(a[0usize:4usize]))
    return 0i32
}
`
	p := buildBudgeted(t, map[string]string{"m.kern": src}, "release")
	if f := p.mod.Func("main.pick"); f == nil || !f.Inlined {
		t.Fatalf("pick was not inlined in release, so the test proves nothing")
	}
	got := counts(t, p.budgets())
	if want := map[string]int{"pick/bounds_checks": 1}; !equalCounts(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// levelSrc's helper index is runtime-checked at debug, and proven at
// release, where the inliner copies the helper into a loop bounded by
// the slice's length and the helper itself is no longer reported.
const levelSrc = `module main
budget { bounds_checks 0 }
func pick(xs: view[]u64, i: usize) -> u64 {
    return xs[i]
}
budget { bounds_checks 0 }
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

// Budgets are counted at the command's level (R3): the same program
// exceeds a budget at debug and meets it at release.
func TestBudgetLevel(t *testing.T) {
	runtime := func(level string) int {
		n := 0
		for _, c := range analysis.BoundsChecks(buildBudgeted(t, map[string]string{"m.kern": levelSrc}, level).mod).Checks {
			if c.Kind == diag.CheckBounds && c.Label == diag.LabelRuntimeChecked {
				n++
			}
		}
		return n
	}
	if d, r := runtime("debug"), runtime("release"); d != 1 || r != 0 {
		t.Fatalf("show-bounds-checks no longer differs between levels: debug %d, release %d runtime-checked", d, r)
	}
	if got := counts(t, budgetsAt(t, levelSrc, "debug")); !equalCounts(got, map[string]int{"pick/bounds_checks": 1}) {
		t.Fatalf("debug: %v", got)
	}
	if ds := budgetsAt(t, levelSrc, "release"); len(ds) != 0 {
		t.Fatalf("release: %v", ds)
	}
}

// A count at or under the value passes, one over it fails, and a budget
// on a function with no entries passes at 0.
func TestBudgetThreshold(t *testing.T) {
	body := "func f(xs: view[]u64, i: usize, j: usize) -> u64 { return xs[i] + xs[j] }\n"
	for _, c := range []struct {
		budget string
		fails  bool
	}{
		{"bounds_checks 3", false},
		{"bounds_checks 2", false},
		{"bounds_checks 1", true},
		{"bounds_checks 0", true},
		{"copies 0; allocations 0", false},
		{"", false},
	} {
		ds := budgetsAt(t, "module m\nbudget { "+c.budget+" }\n"+body, "debug")
		if (len(ds) > 0) != c.fails {
			t.Errorf("budget { %s }: %v", c.budget, ds)
		}
	}
	if ds := budgetsAt(t, "module m\n"+allZero+"func g(a: u64) -> u64 { return a }\n", "debug"); len(ds) != 0 {
		t.Errorf("no entries: %v", ds)
	}
}

// R4: the span is entry V + 1 in source order, related lists the budget
// item and then every other counted entry, the fix is null on the wire.
func TestBudgetExceededShape(t *testing.T) {
	src := "module m\nbudget { bounds_checks 1 }\nfunc f(xs: view[]u64, i: usize, j: usize, k: usize) -> u64 {\n    return xs[i] + xs[j] + xs[k]\n}\n"
	ds := budgetsAt(t, src, "debug")
	if len(ds) != 1 {
		t.Fatalf("got %v", ds)
	}
	d := ds[0]
	at := func(s string) diag.Span {
		i := strings.Index(src, s)
		return diag.Span{Start: i, End: i + len(s)}
	}
	if d.Span != at("xs[j]") || d.File != "m.kern" {
		t.Fatalf("span %v, want the second entry %v", d.Span, at("xs[j]"))
	}
	item, first, third := at("bounds_checks 1"), at("xs[i]"), at("xs[k]")
	want := []diag.Related{
		{Label: "budget declared here", Start: item.Start, End: item.End},
		{Label: "runtime-checked bounds check", Start: first.Start, End: first.End},
		{Label: "runtime-checked bounds check", Start: third.Start, End: third.End},
	}
	if fmt.Sprint(d.Related) != fmt.Sprint(want) {
		t.Fatalf("related %v, want %v", d.Related, want)
	}
	if d.Message != "function `f` has 3 runtime-checked bounds checks, budget 1: 4:12 `xs[i]` (test `i < len(xs)` on every path to it); 4:20 `xs[j]` (test `j < len(xs)` on every path to it); 4:28 `xs[k]` (test `k < len(xs)` on every path to it)" {
		t.Fatalf("message %q", d.Message)
	}
	var buf bytes.Buffer
	if err := diag.Write(&buf, diag.Response{OK: false, Diagnostics: ds}); err != nil {
		t.Fatal(err)
	}
	var back struct {
		Diagnostics []map[string]json.RawMessage `json:"diagnostics"`
	}
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	if fix := string(back.Diagnostics[0]["fix"]); fix != "null" {
		t.Fatalf("fix %s", fix)
	}
	if ds := budgetsAt(t, strings.Replace(src, "bounds_checks 1", "bounds_checks 0", 1), "debug"); len(ds) != 1 || ds[0].Span != first {
		t.Fatalf("budget 0: %v, want the span of the first entry", ds)
	}
}

// The message quotes each location: whitespace runs, line breaks included,
// become one space; text past 40 code points is cut and followed by ...;
// columns count code points; past 10 locations it says how many more. A
// bounds_checks location carries its hint, and locations are separated by
// "; " (specs/unproven-reasons R4).
func TestBudgetMessage(t *testing.T) {
	doc := "module m\nbudget { bounds_checks 0 }\nfunc at_mid(xs: view[]u64) -> u64 {\n    mid := (0usize + len(xs)) / 2usize\n    return xs[mid]\n}\n"
	if ds := budgetsAt(t, doc, "debug"); len(ds) != 1 || ds[0].Message != "function `at_mid` has 1 runtime-checked bounds check, budget 0: 5:12 `xs[mid]` (test `mid < len(xs)` on every path to it)" {
		t.Fatalf("docs/ownership.md example: %v", ds)
	}
	long := "module m\nbudget { bounds_checks 0 }\nfunc f(xs: view[]u64, índice_ñandú_muy_largo_para_la_prueba_xyz: usize, i: usize) -> u64 {\n    ñ := 1u64\n    return ñ + xs[índice_ñandú_muy_largo_para_la_prueba_xyz] + xs[\n        i\n    ]\n}\n"
	ds := budgetsAt(t, long, "debug")
	want := "function `f` has 2 runtime-checked bounds checks, budget 0: 5:16 `xs[índice_ñandú_muy_largo_para_la_prueba...` (test `índice_ñandú_muy_largo_para_la_prueba_xy... < len(xs)` on every path to it); 5:64 `xs[ i ]` (test `i < len(xs)` on every path to it)"
	if len(ds) != 1 || ds[0].Message != want {
		t.Fatalf("got %v\nwant %q", ds, want)
	}
	var terms []string
	for i := 0; i < 12; i++ {
		terms = append(terms, fmt.Sprintf("xs[i%d]", i))
	}
	var params []string
	for i := 0; i < 12; i++ {
		params = append(params, fmt.Sprintf("i%d: usize", i))
	}
	many := "module m\nbudget { bounds_checks 0 }\nfunc f(xs: view[]u64, " + strings.Join(params, ", ") + ") -> u64 {\n    return " + strings.Join(terms, " + ") + "\n}\n"
	ds = budgetsAt(t, many, "debug")
	if len(ds) != 1 {
		t.Fatalf("got %v", ds)
	}
	if !strings.HasPrefix(ds[0].Message, "function `f` has 12 runtime-checked bounds checks, budget 0: 4:12 `xs[i0]` (test `i0 < len(xs)` on every path to it); ") ||
		!strings.HasSuffix(ds[0].Message, "`xs[i9]` (test `i9 < len(xs)` on every path to it); and 2 more") || strings.Count(ds[0].Message, "`xs[") != 10 {
		t.Fatalf("message %q", ds[0].Message)
	}
}

// Several functions and items over budget give one diagnostic each,
// sorted by location.
func TestBudgetSeveral(t *testing.T) {
	src := "module m\nbudget { allocations 0; bounds_checks 0 }\nfunc a(xs: view[]u64, i: usize) -> u64 {\n    b := Box.new(1u64)\n    return xs[i]\n}\nbudget { bounds_checks 0; allocations 0 }\nfunc b(xs: view[]u64, i: usize) -> u64 {\n    v := Box.new(2u64)\n    return xs[i]\n}\n"
	ds := budgetsAt(t, src, "debug")
	if len(ds) != 4 {
		t.Fatalf("got %d: %v", len(ds), ds)
	}
	for i := 1; i < len(ds); i++ {
		if ds[i-1].Span.Start > ds[i].Span.Start {
			t.Fatalf("not sorted: %v", ds)
		}
	}
	got := counts(t, ds)
	if want := map[string]int{"a/allocations": 1, "a/bounds_checks": 1, "b/allocations": 1, "b/bounds_checks": 1}; !equalCounts(got, want) {
		t.Fatalf("got %v", got)
	}
}

// R3: every count equals the matching show result filtered by the
// declaration's span, at both levels, over the budgeted fixtures, the unit
// programs and a two-module program (whose IR locations must name the
// same files as the AST). Each budget item is set to its count (it must
// pass) and to one less (it must report exactly that many entries).
func TestBudgetEqualsShow(t *testing.T) {
	programs := map[string]map[string]string{
		"attribution": {"m.kern": attributionSrc},
		"level":       {"m.kern": levelSrc},
		"two modules": {
			"app/app.kern":     "module app\nimport \"lib\"\nbudget { bounds_checks 0; allocations 0 }\nfunc main() -> i32 {\n    b := Box.new(1u64)\n    xs := [1u64, 2u64]\n    return i32(lib.Pick(xs[0usize:2usize], 1usize))\n}\n",
			"app/lib/lib.kern": "module lib\n" + allZero + "func Pick(xs: view[]u64, i: usize) -> u64 {\n    v := Vec[u64].new()\n    v.push(xs[i])\n    return xs[i]\n}\n",
		},
	}
	for _, path := range []string{"positive/budget_syntax.kern", "negative/budget_exceeded.kern", "negative/budget_exceeded_ok.kern", "negative/budget_invalid_ok.kern"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "tests", path))
		if err != nil {
			t.Fatal(err)
		}
		programs[path] = map[string]string{path: string(b)}
	}
	for name, files := range programs {
		for _, level := range []string{"debug", "release"} {
			p := buildBudgeted(t, files, level)
			shown := map[string][]diag.Loc{}
			for _, c := range analysis.BoundsChecks(p.mod).Checks {
				if c.Kind == diag.CheckBounds && c.Label == diag.LabelRuntimeChecked {
					shown["bounds_checks"] = append(shown["bounds_checks"], c.Loc)
				}
			}
			for _, c := range analysis.Copies(p.mod, "aarch64-apple-darwin").Copies {
				shown["copies"] = append(shown["copies"], c.Loc)
			}
			for _, s := range analysis.Allocations(p.mod).Sites {
				shown["allocations"] = append(shown["allocations"], s.Loc)
			}
			checked := 0
			for _, m := range p.prog.Modules {
				for _, f := range m.Files {
					for _, fn := range budgetedFuncs(f) {
						for _, it := range fn.decl.Budget.Items {
							n := 0
							for _, l := range shown[it.Name] {
								if l.File == f.Name && l.Span.Start >= fn.decl.Start && l.Span.Start < fn.decl.End {
									n++
								}
							}
							saved := it.Value
							it.Value = strconv.Itoa(n)
							if c := counts(t, p.budgets())[fn.name+"/"+it.Name]; c != 0 {
								t.Errorf("%s %s: %s/%s at its count %d reported %d", name, level, fn.name, it.Name, n, c)
							}
							if n > 0 {
								it.Value = strconv.Itoa(n - 1)
								if c := counts(t, p.budgets())[fn.name+"/"+it.Name]; c != n {
									t.Errorf("%s %s: %s/%s counts %d, show reports %d", name, level, fn.name, it.Name, c, n)
								}
							}
							it.Value = saved
							checked++
						}
					}
				}
			}
			if checked == 0 {
				t.Errorf("%s: no budget item compared", name)
			}
		}
	}
}

type budgetedFunc struct {
	name string
	decl *ast.FuncDecl
}

func budgetedFuncs(f *ast.File) []budgetedFunc {
	var out []budgetedFunc
	add := func(name string, d *ast.FuncDecl) {
		if d.Budget != nil {
			out = append(out, budgetedFunc{name, d})
		}
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			add(d.Name, d)
		case *ast.ImplDecl:
			for _, fn := range d.Funcs {
				add(d.Target.(*ast.TypeName).Name+"."+fn.Name, fn)
			}
		}
	}
	return out
}

// The spec's example: each location of a bounds_checks item carries its
// hint (specs/unproven-reasons R4).
func TestBudgetMessageHints(t *testing.T) {
	src := "module rank\n\nbudget { bounds_checks 0 }\nfunc rank(xs: view[]u64, key: u64) -> usize {\n    if len(xs) == 0usize || xs[len(xs) - 1usize] < key { return len(xs) }\n    lo := 0usize\n    hi := len(xs)\n    while lo < hi {\n        mid := (lo + hi) / 2usize\n        if xs[mid] < key { lo = mid + 1usize } else { hi = mid }\n    }\n    return lo\n}\n"
	ds := budgetsAt(t, src, "debug")
	want := "function `rank` has 2 runtime-checked bounds checks, budget 0: 5:29 `xs[len(xs) - 1usize]` (no guard establishes `len(xs) - 1usize < len(xs)` on every path to it, and the rules do not derive it from guards on the parts of `len(xs) - 1usize`: put `len(xs) - 1usize` in a local and test it below `len(xs)` just before the access); 10:12 `xs[mid]` (test `mid < len(xs)` on every path to it)"
	if len(ds) != 1 || ds[0].Message != want {
		t.Fatalf("got %v\nwant %q", ds, want)
	}
}

// A copies or allocations message is unchanged by R4: no hint, ", "
// between locations.
func TestBudgetMessageCopiesUnchanged(t *testing.T) {
	src := "module m\nstruct P { x: u64; y: u64 }\nbudget { copies 0 }\nfunc two(ps: view[[4]P]) -> u64 {\n    p := ps[0usize]\n    q := ps[1usize]\n    return p.x + q.y\n}\n"
	ds := budgetsAt(t, src, "debug")
	want := "function `two` has 2 copies, budget 0: 5:10 `ps[0usize]`, 6:10 `ps[1usize]`"
	if len(ds) != 1 || ds[0].Message != want {
		t.Fatalf("got %v\nwant %q", ds, want)
	}
}

// Budgets classifies once, over the exceeded bounds_checks items'
// locations only, and not at all without one (R5).
func TestBudgetClassifiesOnlyExceededBounds(t *testing.T) {
	var calls, entries int
	counting := func(prog *resolve.Program, srcs []*source.File, snap *analysis.BoundsSnapshot, want func(diag.BoundsCheck) bool) (map[int]analysis.Reason, analysis.ClassifyStats) {
		calls++
		r, st := analysis.ClassifyBounds(prog, srcs, snap, want)
		entries += st.Entries
		return r, st
	}
	for _, tc := range []struct {
		name, src      string
		calls, entries int
	}{
		{"met", "module m\nbudget { bounds_checks 1 }\nfunc f(xs: view[]u64, i: usize) -> u64 { return xs[i] }\nfunc g(xs: view[]u64, i: usize) -> u64 { return xs[i] }\n", 0, 0},
		{"copies only", "module m\nstruct P { x: u64; y: u64 }\nbudget { copies 0; bounds_checks 5 }\nfunc two(ps: view[[4]P], i: usize) -> u64 {\n    p := ps[0usize]\n    return p.x + ps[i].y\n}\n", 0, 0},
		// two functions over: both items' locations, in one call; the
		// unbudgeted g's location is not classified
		{"exceeded", "module m\nbudget { bounds_checks 0 }\nfunc f(xs: view[]u64, i: usize) -> u64 { return xs[i] }\nbudget { bounds_checks 0 }\nfunc h(xs: view[]u64, i: usize, j: usize) -> u64 { return xs[i] + xs[j] }\nfunc g(xs: view[]u64, i: usize) -> u64 { return xs[i] }\n", 1, 3},
	} {
		calls, entries = 0, 0
		p := buildBudgeted(t, map[string]string{"m.kern": tc.src}, "debug")
		analysis.Budgets(p.prog, p.srcs, p.mod, "", counting)
		if calls != tc.calls || entries != tc.entries {
			t.Errorf("%s: %d calls over %d entries, want %d over %d", tc.name, calls, entries, tc.calls, tc.entries)
		}
	}
}
