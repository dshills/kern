package resolve

import (
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/ast"
)

// armBinding returns the symbol of the nth (from 0) match arm binding
// called name in the program's own module, in source order.
func armBinding(t *testing.T, p *Program, name string, nth int) *Symbol {
	t.Helper()
	var found []*Symbol
	for _, f := range p.Main.Files {
		ast.Walk(f, func(n any) bool {
			mt, ok := n.(*ast.Match)
			if !ok {
				return false
			}
			for _, arm := range mt.Arms {
				for i, b := range arm.Binds {
					if b == name {
						if sym := p.Info.Decls[&arm.BindPos[i]]; sym != nil {
							found = append(found, sym)
						}
					}
				}
			}
			return false
		})
	}
	if nth >= len(found) {
		t.Fatalf("no arm binding %s number %d (found %d)", name, nth, len(found))
	}
	return found[nth]
}

// A match arm's Copy binding is a copy until the arm uses it as an edit,
// and each binding is decided on its own (specs/match-bindings M1, M2, M5).
func TestMatchCopyBindings(t *testing.T) {
	p := resolveSrc(t, `module m
union U { Two(a: u64, b: u64); None0 }
func f(u: edit[U], o: edit[Option[u64]], w: view[Option[u64]]) -> u64 {
    match u {
        Two(a, b) => { a = b + 1u64 }
        None0 => { }
    }
    match o {
        Some(n) => { return n }
        None => { }
    }
    match o {
        Some(n) => { n = 2u64 }
        None => { }
    }
    match w {
        Some(n) => { return n }
        None => { }
    }
    return 0u64
}
`)
	if len(p.Diags) != 0 {
		t.Fatalf("unexpected diagnostics:\n%s", codes(p))
	}
	a, b := armBinding(t, p, "a", 0), armBinding(t, p, "b", 0)
	if a.Kind != SymAccess || a.Conv != ast.ConvEdit || a.EditUse == nil || a.ArmCopy {
		t.Errorf("a, which the arm writes: kind %v conv %v edit use %v", a.Kind, a.Conv, a.EditUse)
	}
	if b.Kind != SymLocal || !b.ArmCopy || !b.ArmEdit || b.EditUse != nil {
		t.Errorf("b, which the arm reads: kind %v arm copy %v", b.Kind, b.ArmCopy)
	}
	read, written, viewed := armBinding(t, p, "n", 0), armBinding(t, p, "n", 1), armBinding(t, p, "n", 2)
	if read.Kind != SymLocal || written.Kind != SymAccess || written.Conv != ast.ConvEdit {
		t.Errorf("n read: %v, n written: %v %v", read.Kind, written.Kind, written.Conv)
	}
	if viewed.Kind != SymLocal || !viewed.ArmCopy || viewed.ArmEdit {
		t.Errorf("n of a view subject: kind %v, arm copy %v, editable %v", viewed.Kind, viewed.ArmCopy, viewed.ArmEdit)
	}
}

// The edit use recorded for a binding is the one that starts first in the
// source, in whatever order the resolver meets the uses (M7).
func TestMatchEditUseIsFirstInSource(t *testing.T) {
	src := `module m
func add(v: edit[u64], k: u64) -> u64 { v = v + k; return v }
func apply(f: closure()) { f() }
func f(o: edit[Option[Option[u64]]]) {
    match o {
        Some(inner) => {
            match inner {
                Some(x) => { apply(|| { x = add(x, 1u64) }) }
                None => { inner = Some(0u64) }
            }
            inner = None
        }
        None => { }
    }
}
`
	p := resolveSrc(t, src)
	if len(p.Diags) != 0 {
		t.Fatalf("unexpected diagnostics:\n%s", codes(p))
	}
	inner, x := armBinding(t, p, "inner", 0), armBinding(t, p, "x", 0)
	if inner.Kind != SymAccess || x.Kind != SymAccess || x.Derived != inner {
		t.Fatalf("inner %v, x %v, derived %v", inner.Kind, x.Kind, x.Derived == inner)
	}
	// x's first edit use is the assignment target `x`, which comes before
	// the edit argument `x` on the same line; inner's is that same span,
	// reached through the nested binding, before its own two assignments
	want := strings.Index(src, "x = add")
	if x.EditUse.Start != want || inner.EditUse.Start != want {
		t.Errorf("edit uses start at %d and %d, want %d", x.EditUse.Start, inner.EditUse.Start, want)
	}
}
