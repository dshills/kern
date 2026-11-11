package resolve

import (
	"testing"

	"github.com/dshills/kern/compiler/ast"
)

// forLoops returns the element loops of the program's own module in source
// order.
func forLoops(p *Program) []*ast.For {
	var out []*ast.For
	for _, f := range p.Main.Files {
		ast.Walk(f, func(n any) bool {
			if l, ok := n.(*ast.For); ok {
				out = append(out, l)
			}
			return false
		})
	}
	return out
}

// A loop variable of a Copy element is a copy until the body uses it as an
// edit, and an inner loop's variable is derived from the outer one it
// iterates (specs/for-bindings F1, F2, F6).
func TestForCopyVariables(t *testing.T) {
	p := resolveSrc(t, `module m
func f(xs: edit[]u64, grid: edit[][2]u64) -> u64 {
    var sum: u64 = 0u64
    for x in xs { sum = sum + x }
    for x in xs { x = sum }
    for row in grid {
        for c in row { c = 0u64 }
    }
    for i in 0u64..3u64 { i = i + 1u64 }
    return sum
}
`)
	if len(p.Diags) != 0 {
		t.Fatalf("unexpected diagnostics:\n%s", codes(p))
	}
	loops := forLoops(p)
	if len(loops) != 5 {
		t.Fatalf("%d loops", len(loops))
	}
	sym := func(i int) *Symbol { return p.Info.Decls[loops[i]] }
	if s := sym(0); s.Kind != SymLocal || !s.CopyBound || !s.CopyEdit {
		t.Errorf("read: kind %v, copy bound %v", s.Kind, s.CopyBound)
	}
	if s := sym(1); s.Kind != SymAccess || s.Conv != ast.ConvEdit || s.EditUse == nil {
		t.Errorf("written: kind %v conv %v", s.Kind, s.Conv)
	}
	row, c := sym(2), sym(3)
	if c.Kind != SymAccess || row.Kind != SymAccess || c.Derived != row || row.EditUse == nil {
		t.Errorf("nested: row %v, c %v, derived %v", row.Kind, c.Kind, c.Derived == row)
	}
	if s := sym(4); s.Kind != SymLocal || s.CopyBound {
		t.Errorf("a range loop's variable: kind %v, copy bound %v", s.Kind, s.CopyBound)
	}
}

// A cursor loop whose Copy variable the body writes is rebuilt as the _mut
// loop, record by record as rebindCursor's comment has it; one whose
// variables are read is left alone (specs/for-bindings F7).
func TestForCursorRebuild(t *testing.T) {
	const decl = `module m
import "std/map"
struct Pairs { left: Vec[u64]; right: Vec[u64] }
impl Pairs {
    func cursor_first(view self) -> usize { return 0usize }
    func cursor_next(view self, at: usize) -> usize { return at + 1usize }
    func cursor_end(view self) -> usize { return self.left.len() }
    contract { invariant "distinct vectors" }
    func cursor_entry(view self, at: usize) -> (view[u64], view[u64]) {
        unsafe { return ptr_view2(self.left.ptr_at(at), self.right.ptr_at(at)) }
    }
    contract { invariant "distinct vectors" }
    func cursor_entry_mut(edit self, at: usize) -> (edit[u64], edit[u64]) {
        unsafe { return ptr_edit2(self.left.ptr_at(at), self.right.ptr_at(at)) }
    }
}
union Held { One(m: Trio); Zero }
struct Trio { a: u64 }
impl Trio {
    func cursor_first(view self) -> usize { return 0usize }
    func cursor_next(view self, at: usize) -> usize { return at + 1usize }
    func cursor_end(view self) -> usize { return 1usize }
    func cursor_get(view self, at: usize) -> view[u64] { return self.a }
    func cursor_get_mut(edit self, at: usize) -> edit[u64] { return self.a }
}
`
	cases := []struct {
		name, fn string
		getter   string
		// for each with name: is it the loop's own variable (promoted), and
		// its permission
		own  []bool
		conv []ast.Conv
		kept int // copy statements left in front of the body
	}{
		{"neither written", "func f(p: edit[Pairs]) -> u64 { var s: u64 = 0u64\n for l, r in p { s = s + l + r }\n return s }", "cursor_entry", []bool{false, false}, []ast.Conv{ast.ConvView, ast.ConvView}, 2},
		{"the first written", "func f(p: edit[Pairs]) { for l, r in p { l = r } }", "cursor_entry_mut", []bool{true, false}, []ast.Conv{ast.ConvEdit, ast.ConvEdit}, 1},
		{"the second written", "func f(p: edit[Pairs]) { for l, r in p { r = l } }", "cursor_entry_mut", []bool{false, true}, []ast.Conv{ast.ConvEdit, ast.ConvEdit}, 1},
		{"both written", "func f(p: edit[Pairs]) { for l, r in p { l = 0u64\n r = 1u64 } }", "cursor_entry_mut", []bool{true, true}, []ast.Conv{ast.ConvEdit, ast.ConvEdit}, 0},
		{"a map's value", "func f(p: edit[map.Map[u64, u64]]) { for k, v in p { v = v + k } }", "cursor_entry_mut", []bool{false, true}, []ast.Conv{ast.ConvView, ast.ConvEdit}, 1},
		{"a map read", "func f(p: edit[map.Map[u64, u64]]) -> u64 { var s: u64 = 0u64\n for k, v in p { s = s + v }\n return s }", "cursor_entry", []bool{false, false}, []ast.Conv{ast.ConvView, ast.ConvView}, 2},
	}
	for _, k := range cases {
		p := resolveSrc(t, decl+k.fn+"\n")
		if len(p.Diags) != 0 {
			t.Errorf("%s: unexpected diagnostics:\n%s", k.name, codes(p))
			continue
		}
		var get *ast.With
		for _, l := range forLoops(p) {
			if b := l.Cursor(); b != nil {
				ast.Walk(b, func(n any) bool {
					if w, ok := n.(*ast.With); ok && get == nil {
						if call, ok := w.Subject.(*ast.CallExpr); ok && p.Info.CursorCalls[call] {
							get = w
						}
					}
					return false
				})
			}
		}
		if get == nil {
			t.Errorf("%s: no cursor getter", k.name)
			continue
		}
		call := get.Subject.(*ast.CallExpr)
		if name := call.Fn.(*ast.FieldExpr).Name; name != k.getter {
			t.Errorf("%s: calls %s, want %s", k.name, name, k.getter)
		}
		if info := p.Info.Calls[call]; info == nil || info.Fn == nil || info.Fn.Name != k.getter {
			t.Errorf("%s: the call is recorded as %+v", k.name, info)
		}
		if view := p.Info.CursorViews[get]; view != (k.getter == "cursor_entry") {
			t.Errorf("%s: cursor view %v", k.name, view)
		}
		copies := 0
		for _, st := range get.Body.Stmts {
			if sd, ok := st.(*ast.ShortDecl); ok && p.Info.Decls[sd] != nil && len(sd.Name) > 0 {
				if id, ok := sd.Value.(*ast.Ident); ok && len(id.Name) > 1 && id.Name[0] == '#' {
					copies++
				}
			}
		}
		if copies != k.kept {
			t.Errorf("%s: %d copy statements, want %d", k.name, copies, k.kept)
		}
		for i := range k.own {
			sym := p.Info.Decls[&get.NamePos[i]]
			if sym == nil {
				t.Errorf("%s: name %d is bound to nothing", k.name, i)
				continue
			}
			if own := sym.EditUse != nil; own != k.own[i] {
				t.Errorf("%s: name %d bound to the variable itself: %v", k.name, i, own)
			}
			if sym.Conv != k.conv[i] {
				t.Errorf("%s: name %d has permission %v, want %v", k.name, i, sym.Conv, k.conv[i])
			}
			if k.own[i] && sym.Kind != SymAccess {
				t.Errorf("%s: name %d is a %v", k.name, i, sym.Kind)
			}
		}
	}
	// a cursor loop over an arm's Copy binding: the variable is derived
	// from the binding, and writing it promotes both
	p := resolveSrc(t, decl+"func f(h: edit[Held]) {\n match h {\n One(m) => { for x in m { x = 1u64 } }\n Zero => { }\n }\n}\n")
	if len(p.Diags) != 0 {
		t.Fatalf("over an arm's binding: unexpected diagnostics:\n%s", codes(p))
	}
	m := armBinding(t, p, "m", 0)
	var x *Symbol
	for _, l := range forLoops(p) {
		ast.Walk(l.Cursor(), func(n any) bool {
			if w, ok := n.(*ast.With); ok {
				if s := p.Info.Decls[w]; s != nil && s.Name == "x" {
					x = s
				}
			}
			return false
		})
	}
	if x == nil || x.Derived != m || m.Kind != SymAccess || m.Conv != ast.ConvEdit || x.Kind != SymAccess {
		t.Errorf("over an arm's binding: x %+v, m kind %v", x, m.Kind)
	}
}
