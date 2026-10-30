package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const canonMain = "func main() -> i32 {\n    if true {\n        print_u64(f(1u64))\n    }\n    return 0i32\n}"

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestFrozenText: the canonical main is extracted whole (an indented
// brace does not end it), an unchanged copy compares equal, and an
// edited or missing main fails naming the function.
func TestFrozenText(t *testing.T) {
	dir := t.TempDir()
	src := "module m\nfunc f(x: u64) -> u64 { return x }\n" + canonMain + "\nfunc g() { }\n"
	writeFiles(t, dir, map[string]string{"start/m.kern": src, "same/m.kern": src,
		"edited/m.kern":  strings.Replace(src, "f(1u64)", "f(2u64)", 1),
		"missing/m.kern": "module m\nfunc f(x: u64) -> u64 { return x }\n"})
	got, ok := frozenText(filepath.Join(dir, "start"), []string{"m.kern"}, "main")
	if !ok || got != canonMain {
		t.Fatalf("frozenText = %q %v, want the whole main", got, ok)
	}
	files, names := []string{"m.kern"}, []string{"main"}
	if d := frozenDiff(filepath.Join(dir, "start"), filepath.Join(dir, "same"), files, names); d != "" {
		t.Errorf("unchanged main: %s", d)
	}
	if d := frozenDiff(filepath.Join(dir, "start"), filepath.Join(dir, "edited"), files, names); d != "function main was changed" {
		t.Errorf("edited main: %q", d)
	}
	if d := frozenDiff(filepath.Join(dir, "start"), filepath.Join(dir, "missing"), files, names); d != "function main is missing" {
		t.Errorf("missing main: %q", d)
	}
}

func TestClassifyNatural(t *testing.T) {
	cases := []struct {
		restOK          bool
		runtime, budget int
		ok              bool
		why             string
	}{
		{false, 9, 0, false, "other than the budget"}, // the simultaneous case: the other failure is named
		{true, 3, 0, true, "budget alone"},
		{true, 0, 0, false, "no discrimination"},
		{true, 2, 0, false, "less than 3"},
	}
	for _, c := range cases {
		ok, why := classifyNatural(c.restOK, "output differs", c.runtime, c.budget)
		if ok != c.ok || !strings.Contains(why, c.why) {
			t.Errorf("classifyNatural(%v, %d, %d) = %v %q, want %v containing %q", c.restOK, c.runtime, c.budget, ok, why, c.ok, c.why)
		}
	}
}

func TestIdiomAudit(t *testing.T) {
	sites := []site{{"a", "xs[i]"}, {"a", "xs[i]"}, {"a", "ys[j]"}, {"b", "zs[k]"}, {"b", "t[x]"}, {"b", "u[y]"}, {"c", "v[z]"}}
	good := []mappedSite{{"a", "xs[i]", "midpoint"}, {"a", "xs[i]", "midpoint"}, {"a", "ys[j]", "argmax"}, {"b", "zs[k]", "modulo"},
		{"b", "t[x]", "grid"}, {"b", "u[y]", "grid"}, {"c", "v[z]", "other"}}
	req := []string{"m.a", "m.b"}
	if err := auditIdioms(sites, good, req); err != nil {
		t.Errorf("exact mapping: %v", err)
	}
	with := func(f func(m []mappedSite) []mappedSite) []mappedSite {
		return f(append([]mappedSite(nil), good...))
	}
	bad := map[string][]mappedSite{
		"not mapped":    with(func(m []mappedSite) []mappedSite { return m[:6] }),
		"not a runtime": with(func(m []mappedSite) []mappedSite { return append(m, mappedSite{"a", "q[r]", "grid"}) }),
		"unknown idiom": with(func(m []mappedSite) []mappedSite { m[0].Idiom = "vibes"; return m }),
		"at least 6":    with(func(m []mappedSite) []mappedSite { m[5].Idiom = "other"; return m }),
		"at least 3":    with(func(m []mappedSite) []mappedSite { m[2].Idiom = "midpoint"; m[3].Idiom = "grid"; return m }),
		"2 needed":      with(func(m []mappedSite) []mappedSite { return m }),
	}
	for want, m := range bad {
		r := req
		if want == "2 needed" {
			r = []string{"m.a"}
		}
		if err := auditIdioms(sites, m, r); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: auditIdioms = %v", want, err)
		}
	}
}

func TestLoadTasksNatural(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"t01_with", "t02_without"} {
		writeFiles(t, filepath.Join(dir, name), map[string]string{
			"task.md": "Kind: implementation\n", "hidden.json": `{"main":"m.kern","expected_output":"","frozen_functions":["main"]}`,
			"start/m.kern": "module m\n",
		})
	}
	if err := os.MkdirAll(filepath.Join(dir, "t01_with", "calibration", "natural"), 0o755); err != nil {
		t.Fatal(err)
	}
	tasks := loadTasks(dir)
	if len(tasks) != 2 || tasks[0].Natural != filepath.Join(dir, "t01_with", "calibration", "natural") || tasks[1].Natural != "" {
		t.Fatalf("Natural: %q %q", tasks[0].Natural, tasks[1].Natural)
	}
	if strings.Join(tasks[0].Hidden.FrozenFunctions, ",") != "main" {
		t.Errorf("frozen functions %v", tasks[0].Hidden.FrozenFunctions)
	}
}

// TestNaturalVerdictIntegration: a natural solution that both exceeds the
// budget and prints a wrong line is reported for the output, not the
// budget.
func TestNaturalVerdictIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the kern binary")
	}
	root, err := findRoot()
	if err != nil {
		t.Fatal(err)
	}
	kernBin := filepath.Join(t.TempDir(), "kern")
	if out, err := exec.Command("go", "build", "-o", kernBin, filepath.Join(root, "cmd", "kern")).CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	prog := func(out string) string {
		return "module m\nfunc pick(xs: view[]u64, a: usize, b: usize, c: usize) -> u64 {\n    return xs[a] + xs[b] + xs[c]\n}\nfunc main() -> i32 {\n    xs := [1u64, 2u64, 3u64]\n    print_u64(pick(xs[0usize:3usize], 0usize, 1usize, 2usize) + " + out + ")\n    println()\n    return 0i32\n}\n"
	}
	dir := filepath.Join(t.TempDir(), "t01")
	writeFiles(t, dir, map[string]string{
		"task.md": "Kind: optimization\n", "start/m.kern": prog("0u64"),
		"hidden.json":                `{"main":"m.kern","expected_output":"6\n","must_check_clean":true,"max_runtime_bounds_checks":0,"frozen_functions":["pick"]}`,
		"calibration/natural/m.kern": prog("1u64"),
	})
	tk := loadTasks(filepath.Dir(dir))[0]
	ok, why := naturalVerdict(context.Background(), root, kernBin, tk)
	if ok || !strings.Contains(why, "other than the budget") || !strings.Contains(why, "output") {
		t.Errorf("wrong output and over budget: %v %q", ok, why)
	}
	writeFiles(t, dir, map[string]string{"calibration/natural/m.kern": prog("0u64")})
	ok, why = naturalVerdict(context.Background(), root, kernBin, tk)
	if !ok || !strings.Contains(why, "budget alone") {
		t.Errorf("over budget only: %v %q", ok, why)
	}
}
