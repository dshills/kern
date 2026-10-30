package analysis_test

import (
	"os"
	"path/filepath"
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

// No copy of a Page built in place in a box or a pool slot, and a caller
// of an initializer method has unknown effects (specs/in-place-init R2, R6).
func TestInPlaceInitCopiesAndEffects(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "tests", "adversarial", "large_object_out.kern"))
	if err != nil {
		t.Fatal(err)
	}
	// a main that reaches every path, so that release (whose inliner marks
	// uncalled functions dead) reports on them too
	prog := string(src) + `func main() -> i32 {
    p := Pool[Page].new()
    match pooled(p) {
        Ok(id) => print_u64(p.get(id).id)
        Err(e) => print_str("failed")
    }
    match boxed() {
        Ok(v) => print_u64(v)
        Err(e) => print_str("failed")
    }
    print_u64(make())
    return 0i32
}
`
	for _, level := range []string{"debug", "release"} {
		mod := lowerFiles(t, map[string]string{"large_object_out.kern": prog}, level)
		for _, c := range analysis.Copies(mod, "host").Copies {
			if c.Type == "Page" {
				t.Errorf("%s: a copy of %s at %+v", level, c.Type, c.Loc)
			}
		}
		found := false
		for _, f := range analysis.Effects(mod, "host").Functions {
			if f.Name == "adversarial.main" {
				found = true
				if f.Label != diag.LabelUnknown || !f.Allocates || !f.Traps {
					t.Errorf("%s: effects of main %+v, want unknown with every effect", level, f)
				}
			}
		}
		if !found {
			t.Errorf("%s: no effects entry for adversarial.main", level)
		}
	}
}

// No copy of a 4,096-byte Page built in place from a context by each
// _with_ctx method, and a function calling ptr_init_ctx has unknown
// effects (specs/init-context/SPEC.md R6, R9).
func TestInitContextCopiesAndEffects(t *testing.T) {
	prog := `module ctx
struct Page { id: u64; data: [4096]u8 }
struct Spec { id: u64 }
union IoErr { Short }
func fill(p: out Page, s: view[Spec]) -> Result[(), IoErr] {
    p.id = s.id
    p.data = zero()
    return Ok(())
}
func boxed(s: view[Spec]) -> Result[u64, IoErr] {
    b := Box[Page].new_with_ctx(fill, s)?
    return Ok(b.get().id)
}
func pooled(pool: edit[Pool[Page]], s: view[Spec]) -> Result[PoolId[Page], AllocOrInit[IoErr]] {
    return pool.alloc_with_ctx(fill, s)
}
func arena(a: edit[Arena[Page]], s: view[Spec]) -> Result[ArenaId[Page], AllocOrInit[IoErr]] {
    return a.alloc_with_ctx(fill, s)
}
contract { requires "p addresses storage for one Page that holds no live value" }
func direct(p: RawPtr[Page], s: view[Spec]) -> Result[(), IoErr] {
    unsafe { return ptr_init_ctx(p, fill, s) }
}
func main() -> i32 {
    s := Spec { id: 3u64 }
    p := Pool[Page].new()
    match pooled(p, s) {
        Ok(id) => print_u64(p.get(id).id)
        Err(e) => print_str("failed")
    }
    r := Arena[Page].new()
    match arena(r, s) {
        Ok(id) => print_u64(r.get(id).id)
        Err(e) => print_str("failed")
    }
    match boxed(s) {
        Ok(v) => print_u64(v)
        Err(e) => print_str("failed")
    }
    return 0i32
}
`
	for _, level := range []string{"debug", "release"} {
		mod := lowerFiles(t, map[string]string{"ctx.kern": prog}, level)
		for _, c := range analysis.Copies(mod, "host").Copies {
			if c.Type == "Page" {
				t.Errorf("%s: a copy of %s at %+v", level, c.Type, c.Loc)
			}
		}
		found := false
		for _, f := range analysis.Effects(mod, "host").Functions {
			if f.Name == "ctx.direct" {
				found = true
				if f.Label != diag.LabelUnknown || !f.Allocates || !f.Traps || !f.Spawns {
					t.Errorf("%s: effects of direct %+v, want unknown with every effect", level, f)
				}
			}
		}
		// release's inliner marks the uncalled direct dead; debug reports it
		if !found && level == "debug" {
			t.Errorf("%s: no effects entry for ctx.direct", level)
		}
	}
}

// Calls into std/str, std/map and Vec.extend that may allocate are sites;
// constructors that allocate nothing are not; Box.new keeps its proven
// table site (specs/alloc-sites-std §4 item 1).
func TestLibraryAllocationSites(t *testing.T) {
	src := `module app
import "std/str"
import "std/map"
func only_str() -> usize {
    s := str.String.new()
    s.push_u64(42u64)
    return s.len()
}
func only_map() -> usize {
    m := map.Map[u64, u64].new()
    m.insert(3u64, 4u64)
    return m.len()
}
func only_extend(v: edit[Vec[u64]], xs: view[]u64) { v.extend(xs) }
func boxed() -> u64 {
    b := Box[u64].new(7u64)
    return b.get()
}
func pure(xs: view[]u64) -> u64 {
    var s: u64 = 0u64
    for x in xs { s = s + x }
    return s
}
func main() -> i32 {
    var v: Vec[u64] = Vec[u64].new()
    a := [1u64, 2u64]
    only_extend(v, a[0usize:2usize])
    print_u64(u64(only_str() + only_map()) + pure(a[0usize:2usize]) + boxed())
    println()
    return 0i32
}
`
	text := func(s diag.AllocationSite) string { return src[s.Loc.Span.Start:s.Loc.Span.End] }
	for _, level := range []string{"debug", "release"} {
		mod := lowerFiles(t, map[string]string{"app.kern": src}, level)
		got := map[string]diag.AllocationSite{}
		for _, s := range analysis.Allocations(mod).Sites {
			got[text(s)] = s
		}
		for _, call := range []string{"s.push_u64(42u64)", "map.Map[u64, u64].new()", "m.insert(3u64, 4u64)", "v.extend(xs)"} {
			s, ok := got[call]
			if !ok || s.Label != diag.LabelUnknown || s.Size != nil || s.Allocator != "system" {
				t.Errorf("%s: %s: site %+v (found %v), want unknown with no size", level, call, s, ok)
			}
		}
		for _, call := range []string{"str.String.new()", "Vec[u64].new()"} {
			if _, ok := got[call]; ok {
				t.Errorf("%s: %s allocates nothing but is a site", level, call)
			}
		}
		if s, ok := got["Box[u64].new(7u64)"]; !ok || s.Label != diag.LabelProven || s.Size == nil || *s.Size != 8 {
			t.Errorf("%s: Box.new site %+v (found %v), want proven with size 8", level, s, ok)
		}
		// the five above and nothing else: none in pure, none for the empty
		// constructors
		if len(got) != 5 {
			t.Errorf("%s: %d sites, want 5: %v", level, len(got), got)
		}
	}
}
