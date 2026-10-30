package analysis_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/analysis"
	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/lower"
	"github.com/dshills/kern/compiler/opt"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

func lowerFiles(t *testing.T, files map[string]string, optLevel string) *ir.Module {
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
	prog := resolve.ResolveFor(srcs, optLevel == "release")
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
	if err := opt.Run(mod, opt.OptionsFor(optLevel)); err != nil {
		t.Fatalf("opt: %v", err)
	}
	return mod
}

// A check the inliner copied from an imported module's function is
// reported at that module's file and span (specs/cross-file-inlining R8).
func TestBoundsChecksCrossFile(t *testing.T) {
	lib := "module lib\nfunc Pick(xs: view[[]u64], i: usize) -> u64 {\n    return xs[i]\n}\n"
	files := map[string]string{
		"app.kern": `module app
import "lib"
func main() -> i32 {
    xs := [1u64, 2u64, 3u64]
    print_u64(lib.Pick(xs[0usize:3usize], 1usize))
    println()
    return 0i32
}
`,
		"lib/lib.kern": lib,
	}
	mod := lowerFiles(t, files, "release")
	if !mod.Func("lib.Pick").Inlined {
		t.Fatal("lib.Pick must be inlined for the test to mean anything")
	}
	res := analysis.BoundsChecks(mod)
	found := false
	for _, c := range res.Checks {
		if c.Loc.File != "lib/lib.kern" {
			continue
		}
		if c.Kind != diag.CheckBounds {
			continue
		}
		if text := lib[c.Loc.Span.Start:c.Loc.Span.End]; !strings.Contains(text, "xs[i]") {
			t.Fatalf("the check in lib/lib.kern denotes %q, not the index", text)
		}
		found = true
	}
	if !found {
		t.Fatalf("no bounds check located in lib/lib.kern: %+v", res.Checks)
	}
}
