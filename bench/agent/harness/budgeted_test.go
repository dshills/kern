package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/dshills/kern/bench/agent/budgetize"
)

// kernForBudgets builds the kern binary into the test's temporary
// directory.
func kernForBudgets(t *testing.T, root string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "kern")
	if out, err := exec.Command("go", "build", "-o", bin, filepath.Join(root, "cmd", "kern")).CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// kernJSON runs a kern machine command on files in dir and decodes its
// response; code is the exit code.
func kernJSON(t *testing.T, kernBin, dir, cmd string, files []string) (int, map[string]json.RawMessage) {
	t.Helper()
	c := exec.Command(kernBin, append([]string{cmd}, files...)...)
	c.Dir = dir
	var out bytes.Buffer
	c.Stdout = &out
	code := 0
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("%s: %v", cmd, err)
		}
		code = ee.ExitCode()
	}
	var resp map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("%s in %s: %v\n%s", cmd, dir, err, out.String())
	}
	return code, resp
}

type wireDiag struct {
	Code    string `json:"code"`
	File    string `json:"file"`
	Span    struct{ Start, End int }
	Related []struct {
		Label      string
		Start, End int
	}
}

func diagsOf(t *testing.T, resp map[string]json.RawMessage) []wireDiag {
	t.Helper()
	var ds []wireDiag
	if err := json.Unmarshal(resp["diagnostics"], &ds); err != nil {
		t.Fatal(err)
	}
	return ds
}

// budgetAgreement runs check and show-bounds-checks on files in dir and
// checks spec §5: every diagnostic is BUDGET_EXCEEDED, their counted
// locations (primary and related spans but the budget item) are exactly
// the runtime-checked bounds checks, and check passes exactly when there
// are none. It returns how many there are.
func budgetAgreement(t *testing.T, kernBin, dir string, files []string) int {
	t.Helper()
	code, resp := kernJSON(t, kernBin, dir, "check", files)
	counted := map[string]bool{}
	for _, d := range diagsOf(t, resp) {
		if d.Code != "BUDGET_EXCEEDED" {
			t.Fatalf("%s: %s, not BUDGET_EXCEEDED", dir, d.Code)
		}
		counted[fmt.Sprintf("%s:%d-%d", d.File, d.Span.Start, d.Span.End)] = true
		for _, r := range d.Related {
			if r.Label != "budget declared here" {
				counted[fmt.Sprintf("%s:%d-%d", d.File, r.Start, r.End)] = true
			}
		}
	}
	_, show := kernJSON(t, kernBin, dir, "show-bounds-checks", files)
	var res struct {
		Checks []struct {
			Loc struct {
				File string
				Span struct{ Start, End int }
			}
			Kind, Label string
		}
	}
	if err := json.Unmarshal(show["result"], &res); err != nil {
		t.Fatal(err)
	}
	shown := map[string]bool{}
	for _, c := range res.Checks {
		if c.Kind == "bounds" && c.Label == "runtime-checked" {
			shown[fmt.Sprintf("%s:%d-%d", c.Loc.File, c.Loc.Span.Start, c.Loc.Span.End)] = true
		}
	}
	if fmt.Sprint(sortedKeys(counted)) != fmt.Sprint(sortedKeys(shown)) {
		t.Errorf("%s: check counts %v, show-bounds-checks lists %v", dir, sortedKeys(counted), sortedKeys(shown))
	}
	if (code == 0) != (len(shown) == 0) {
		t.Errorf("%s: check exits %d with %d runtime-checked bounds checks", dir, code, len(shown))
	}
	return len(shown)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestBudgetedSuite is spec §5's offline evaluation over
// bench/agent/tasks-budgeted.
func TestBudgetedSuite(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the kern binary")
	}
	root, err := findRoot()
	if err != nil {
		t.Fatal(err)
	}
	kernBin := kernForBudgets(t, root)
	tasks := loadTasks(filepath.Join(root, "bench", "agent", "tasks-budgeted"))
	if len(tasks) != 10 {
		t.Fatalf("%d tasks in the budgeted suite, want 10", len(tasks))
	}
	for _, tk := range tasks {
		t.Run(tk.Name, func(t *testing.T) {
			ref := filepath.Join(tk.Dir, "reference")
			if code, resp := kernJSON(t, kernBin, ref, "check", tk.Files); code != 0 {
				t.Fatalf("reference does not check: %s", resp["diagnostics"])
			}
			if ok, why := validateReference(t.Context(), root, kernBin, tk); !ok {
				t.Fatalf("reference: %s", why)
			}
			// a start that does not check without its budgets (the review
			// task) reports exactly its own errors: budgets come after a
			// clean check (R6)
			start := filepath.Join(tk.Dir, "start")
			plain := filepath.Join(root, "bench", "agent", "tasks", tk.Name, "start")
			if code, presp := kernJSON(t, kernBin, plain, "check", tk.Files); code != 0 {
				_, bresp := kernJSON(t, kernBin, start, "check", tk.Files)
				var pc, bc []string
				for _, d := range diagsOf(t, presp) {
					pc = append(pc, d.Code)
				}
				for _, d := range diagsOf(t, bresp) {
					bc = append(bc, d.Code)
				}
				if fmt.Sprint(pc) != fmt.Sprint(bc) || strings.Contains(fmt.Sprint(bc), "BUDGET") {
					t.Errorf("start: budgeted check reports %v, unbudgeted %v", bc, pc)
				}
			} else {
				budgetAgreement(t, kernBin, start, tk.Files)
			}
			nat := t.TempDir()
			for _, f := range tk.Files {
				b, err := os.ReadFile(filepath.Join(tk.Natural, f))
				if err != nil {
					t.Fatal(err)
				}
				if b, err = budgetize.Source(f, b); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(nat, f), b, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if n := budgetAgreement(t, kernBin, nat, tk.Files); n < 3 {
				t.Errorf("budgeted natural solution has %d runtime-checked bounds checks, fewer than 3", n)
			}
		})
	}
	if !dryRunTasks(root, kernBin, tasks) {
		t.Error("the dry run fails on the budgeted suite")
	}
	gen := filepath.Join(t.TempDir(), budgetize.SuiteName)
	if err := budgetize.Suite(filepath.Join(root, "bench", "agent", "tasks"), gen); err != nil {
		t.Fatal(err)
	}
	if diff := treeDiff(t, gen, filepath.Join(root, "bench", "agent", "tasks-budgeted")); diff != "" {
		t.Fatalf("bench/agent/tasks-budgeted is stale (go run ./bench/agent/tools/budgetize): %s", diff)
	}
}

// treeDiff is "" when the trees hold the same files with the same bytes.
func treeDiff(t *testing.T, a, b string) string {
	t.Helper()
	read := func(root string) map[string]string {
		out := map[string]string{}
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			bs, err := os.ReadFile(p)
			out[rel] = string(bs)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	x, y := read(a), read(b)
	for k, v := range x {
		if w, ok := y[k]; !ok {
			return k + " missing"
		} else if w != v {
			return k + " differs"
		}
	}
	for k := range y {
		if _, ok := x[k]; !ok {
			return k + " extra"
		}
	}
	return ""
}

func TestBudgetizeSource(t *testing.T) {
	src := `module m
struct S { a: u64 }
contract { requires "x > 0" }
func f(x: u64) -> u64 { return x }
impl S {
    func get(view self) -> u64 { return self.a }
    budget { copies 1 }
    func kept(view self) -> u64 { return self.a }
}
extern "C" func puts(s: RawPtr[u8]) -> i32
`
	got, err := budgetize.Source("m.kern", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := `module m
struct S { a: u64 }
contract { requires "x > 0" }
budget { bounds_checks 0 }
func f(x: u64) -> u64 { return x }
impl S {
    budget { bounds_checks 0 }
    func get(view self) -> u64 { return self.a }
    budget { copies 1 }
    func kept(view self) -> u64 { return self.a }
}
extern "C" func puts(s: RawPtr[u8]) -> i32
`
	if string(got) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	again, err := budgetize.Source("m.kern", got)
	if err != nil || string(again) != string(got) {
		t.Fatalf("not idempotent: %v\n%s", err, again)
	}
	if _, err := budgetize.Source("bad.kern", []byte("module m\nfunc f( {\n")); err == nil {
		t.Fatal("a file that does not parse was accepted")
	}
}

// writeTree writes files (path -> content) under root.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBudgetizeSuiteSafety(t *testing.T) {
	base := t.TempDir()
	from := filepath.Join(base, "tasks")
	writeTree(t, from, map[string]string{
		"t01/task.md":              "Do it.\n",
		"t01/hidden.json":          "{}\n",
		"t01/start/a.kern":         "module a\nfunc f() { }\n",
		"t01/reference/lib/l.kern": "module lib\nfunc G() { }\n",
		"t01/reference/a.kern":     "module a\nfunc f() { }\n",
		"t01/calibration/n.kern":   "module a\nfunc f() { }\n",
	})
	snapshot := func(root string) string {
		var b strings.Builder
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				c, _ := os.ReadFile(p)
				fmt.Fprintf(&b, "%s=%q\n", p, c)
			}
			return nil
		})
		return b.String()
	}
	before := snapshot(base)
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(from, alias); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "links", budgetize.SuiteName)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "elsewhere"), link); err != nil {
		t.Fatal(err)
	}
	for name, to := range map[string]string{
		"equal":                     from,
		"nested":                    filepath.Join(from, budgetize.SuiteName),
		"containing":                base,
		"wrongly named":             filepath.Join(base, "budgeted"),
		"through a symlinked alias": filepath.Join(alias, budgetize.SuiteName),
		"symlinked destination":     link,
	} {
		if err := budgetize.Suite(from, to); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// the refusals of a containing or equal destination are by name too, so
	// test the overlap rule on its own with a source named like a suite
	named := filepath.Join(base, "x", budgetize.SuiteName)
	writeTree(t, named, map[string]string{"t/task.md": "x\n"})
	if err := budgetize.Suite(named, filepath.Join(named, "t", budgetize.SuiteName)); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Errorf("nested in the source: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(base, "x")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(base, "links")); err != nil {
		t.Fatal(err)
	}
	if got := snapshot(base); got != before {
		t.Fatalf("a refused run modified the trees:\n%s\nwas\n%s", got, before)
	}
	to := filepath.Join(base, budgetize.SuiteName)
	if err := budgetize.Suite(from, to); err != nil {
		t.Fatal(err)
	}
	first := snapshot(to)
	if !strings.Contains(first, "budget { bounds_checks 0 }") || !strings.Contains(first, budgetize.Sentence) {
		t.Fatalf("generated:\n%s", first)
	}
	if b, err := os.ReadFile(filepath.Join(to, "t01", "reference", "lib", "l.kern")); err != nil || !strings.Contains(string(b), "budget { bounds_checks 0 }\nfunc G()") {
		t.Fatalf("a nested reference file was not budgeted: %q %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(to, "t01", "calibration", "n.kern")); err != nil || strings.Contains(string(b), "budget") {
		t.Fatalf("a calibration file was changed: %q %v", b, err)
	}
	if err := budgetize.Suite(from, to); err != nil {
		t.Fatal(err)
	}
	if snapshot(to) != first {
		t.Fatal("a second run differs")
	}
	for _, leftover := range []string{to + ".new", to + ".old"} {
		if _, err := os.Lstat(leftover); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s left behind", leftover)
		}
	}
	// a generation failure leaves the destination as it was
	writeTree(t, from, map[string]string{"t01/start/b.kern": "module a\nfunc f( {\n"})
	if err := budgetize.Suite(from, to); err == nil {
		t.Fatal("an unparsable start was accepted")
	}
	if snapshot(to) != first {
		t.Fatal("a failed run changed the destination")
	}
	if _, err := os.Lstat(to + ".new"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(".new left behind")
	}
	if err := os.WriteFile(to+".old", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := budgetize.Suite(from, to); err == nil || !strings.Contains(err.Error(), "remove it") {
		t.Fatalf("leftover .old: %v", err)
	}
	if err := os.Remove(to + ".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(to+".new", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := budgetize.Suite(from, to); err == nil || !strings.Contains(err.Error(), "remove it") {
		t.Fatalf("leftover .new: %v", err)
	}
	if _, err := os.Stat(to + ".new"); err != nil {
		t.Fatalf("a refused run removed another run's .new: %v", err)
	}
}
