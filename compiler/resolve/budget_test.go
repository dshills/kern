package resolve

import (
	"sort"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/source"
)

// budgetDiags resolves files and returns the BUDGET_INVALID diagnostics as
// "file:covered-text" strings, failing on any other diagnostic.
func budgetDiags(t *testing.T, files ...*source.File) []string {
	t.Helper()
	p := Resolve(files)
	byName := map[string][]byte{}
	for _, f := range files {
		byName[f.Name] = f.Bytes
	}
	var out []string
	for _, d := range p.Diags {
		if d.Code != "BUDGET_INVALID" {
			t.Fatalf("unexpected %s: %s", d.Code, d.Message)
		}
		if d.Rule != "7.6" {
			t.Fatalf("rule %q", d.Rule)
		}
		out = append(out, d.File+":"+string(byName[d.File][d.Span.Start:d.Span.End]))
	}
	return out
}

// Each semantic defect of R2 is one BUDGET_INVALID on the offending part:
// the name for an unknown or repeated item, the value for a suffixed or
// out-of-range one. An item with several defects reports the first, and
// every bad item of a file is reported.
func TestBudgetInvalid(t *testing.T) {
	cases := []struct {
		name, budget string
		want         []string
	}{
		{"valid", "bounds_checks 0; copies 2147483647; allocations 1_000", nil},
		{"unknown name", "loops 1", []string{"loops"}},
		{"repeat", "copies 0; allocations 0; copies 1", []string{"copies"}},
		{"suffix", "copies 0u64", []string{"0u64"}},
		{"above max", "allocations 2147483648", []string{"2147483648"}},
		{"far above u64", "allocations 99999999999999999999999", []string{"99999999999999999999999"}},
		{"unknown with suffix", "loops 0u8", []string{"loops"}},
		{"repeat with suffix", "copies 0; copies 1u8", []string{"copies"}},
		{"several", "loops 1; copies 0u64; copies 3; bounds_checks 4294967296", []string{"loops", "0u64", "copies", "4294967296"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := "module m\nbudget { " + c.budget + " }\nfunc f() { }\n"
			got := budgetDiags(t, &source.File{Name: "t.kern", Bytes: []byte(src)})
			var want []string
			for _, w := range c.want {
				want = append(want, "t.kern:"+w)
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("got %v, want %v", got, want)
			}
		})
	}
}

// The check covers methods and every module of the program, an imported
// one included, and runs once per declaration. (As for any declaration
// error, the resolver stops after the first module, in dependency order,
// whose declarations have errors; within it every bad item is reported.)
func TestBudgetInvalidEverywhere(t *testing.T) {
	const cleanLib = "module lib\nfunc F() -> u64 { return 1u64 }\n"
	const badLib = "module lib\nstruct S { a: u64 }\nimpl S {\n    budget { copies 0u64 }\n    func get(view self) -> u64 { return self.a }\n}\nbudget { copies 1; copies 2 }\nfunc F() -> u64 { return 1u64 }\n"
	const cleanApp = "module app\nimport \"lib\"\nfunc main() -> i32 { return i32(lib.F()) }\n"
	const badApp = "module app\nimport \"lib\"\nbudget { loops 1 }\nfunc main() -> i32 { return i32(lib.F()) }\n"
	cases := []struct{ app, lib, want string }{
		{badApp, cleanLib, "app/app.kern:loops"},
		{cleanApp, badLib, "app/lib/lib.kern:0u64,app/lib/lib.kern:copies"},
	}
	for _, c := range cases {
		got := budgetDiags(t, &source.File{Name: "app/app.kern", Bytes: []byte(c.app)}, &source.File{Name: "app/lib/lib.kern", Bytes: []byte(c.lib)})
		sort.Strings(got) // the resolver's early return leaves them unsorted
		if g := strings.Join(got, ","); g != c.want {
			t.Errorf("got %s, want %s", g, c.want)
		}
	}
}
