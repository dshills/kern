package lower

import (
	"testing"

	"github.com/dshills/kern/compiler/ast"

	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// TestTwoPhaseAccessBegin checks where a two-phase edit argument's access
// begins (specs/two-phase-edit R3): after the other arguments' instructions
// when the checker relied on its reservation (rec), at its own evaluation
// when it did not (plain), as before the change.
func TestTwoPhaseAccessBegin(t *testing.T) {
	src := `module m
struct C { n: u64 }
impl C {
    func set(edit self, n: u64) { self.n = n }
}
func rec(c: edit[C]) {
    c.set(c.n + 1u64)
}
func plain(c: edit[C], k: u64) {
    c.set(k + 1u64)
}
`
	prog := resolve.Resolve([]*source.File{{Name: "m.kern", Bytes: []byte(src)}})
	if len(prog.Diags) > 0 {
		t.Fatalf("resolve: %s", prog.Diags[0].Message)
	}
	if ds := check.Check(prog); len(ds) > 0 {
		t.Fatalf("check: %s", ds[0].Message)
	}
	// the records for each of this module's functions (the prelude, checked
	// with every program, has records of its own)
	recorded := map[string]int{}
	for decl, fd := range prog.Info.Funcs {
		if fd.Module != "m" {
			continue
		}
		ast.Walk(decl, func(n any) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				for k := range prog.Info.TwoPhase {
					if k.Call == c {
						recorded[fd.Name]++
					}
				}
			}
			return false
		})
	}
	if recorded["rec"] != 1 || recorded["plain"] != 0 {
		t.Fatalf("expected one recorded argument in rec and none in plain, got %v", recorded)
	}
	mod, ds := Lower(prog)
	if len(ds) > 0 {
		t.Fatalf("lower: %s", ds[0].Message)
	}
	if errs := ir.Validate(mod); len(errs) > 0 {
		t.Fatalf("validate: %v", errs[0])
	}
	// order is the positions, in the block holding the call to set, of the
	// receiver's edit access_begin and of the argument's add
	order := func(name string) (begin, add int) {
		f := mod.Func(name)
		if f == nil {
			t.Fatalf("no function %s", name)
		}
		begin, add = -1, -1
		for _, b := range f.Blocks {
			for k, in := range b.Instrs {
				switch i := in.(type) {
				case *ir.AccessBegin:
					if i.Edit && i.Place.Access != nil && len(i.Place.Path) == 0 && begin < 0 && k > 0 {
						begin = k
					}
				case *ir.Binary:
					if i.Op == "+" && add < 0 {
						add = k
					}
				}
			}
		}
		if begin < 0 || add < 0 {
			t.Fatalf("%s: access_begin at %d, add at %d:\n%s", name, begin, add, ir.Print(mod))
		}
		return begin, add
	}
	if b, a := order("m.rec"); b < a {
		t.Errorf("rec: the receiver's access begins (%d) before the argument is evaluated (%d)", b, a)
	}
	if b, a := order("m.plain"); b > a {
		t.Errorf("plain: the receiver's access begins (%d) after the argument (%d); unrecorded arguments keep their place", b, a)
	}
}
