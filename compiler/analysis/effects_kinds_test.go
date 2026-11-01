package analysis_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/analysis"
	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/lower"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// lowerPlain resolves, checks and lowers one file without optimization, so
// that no function is inlined away.
func lowerPlain(t *testing.T, src string) *ir.Module {
	t.Helper()
	prog := resolve.ResolveFor([]*source.File{{Name: "m.kern", Bytes: []byte(src)}}, false)
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
	return mod
}

func effectsOf(t *testing.T, mod *ir.Module, name string) diag.FunctionEffects {
	t.Helper()
	for _, f := range analysis.Effects(mod, "").Functions {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("no effects for %s", name)
	return diag.FunctionEffects{}
}

// Each source of trap kinds in specs/effect-trap-kinds §2 yields exactly its
// row's kinds, a caller the union of its callees', and traps holds iff the
// list is non-empty (R2, R3).
func TestTrapKindsPerSource(t *testing.T) {
	all := []string{"bounds", "overflow", "div_zero", "shift", "conversion", "generation", "vacant", "identity", "disjoint", "oom", "explicit", "unreachable"}
	cases := []struct {
		name, body string
		want       []string
	}{
		{"bounds", "func f(xs: view[]u64, i: usize) -> u64 { return xs[i] }", []string{"bounds"}},
		{"add", "func f(a: u64, b: u64) -> u64 { return a + b }", []string{"overflow"}},
		{"unsigned div", "func f(a: u64, b: u64) -> u64 { return a / b }", []string{"div_zero"}},
		{"signed rem", "func f(a: i64, b: i64) -> i64 { return a % b }", []string{"overflow", "div_zero"}},
		{"shl", "func f(a: u64, n: u32) -> u64 { return a << n }", []string{"overflow", "shift"}},
		{"shr", "func f(a: u64, n: u32) -> u64 { return a >> n }", []string{"shift"}},
		{"conversion", "func f(a: u64) -> u32 { return u32(a) }", []string{"conversion"}},
		{"trap", "func f() { trap() }", []string{"explicit"}},
		{"Mutex.lock", "func f(m: view[Mutex[u64]]) { with g := m.lock() { g = 1u64 } }", []string{"explicit"}},
		{"Shared.clone", "func f(s: view[Shared[u64]]) -> Shared[u64] { return s.clone() }", []string{"overflow"}},
		{"SystemAllocator.alloc", "contract { requires \"x\" }\nfunc f() {\n    a := SystemAllocator.new()\n    unsafe { r := a.alloc(8usize, 8usize) }\n}", []string{"explicit"}},
		{"Shared.new", "func f() { s := Shared.new(1u64) }", []string{"oom"}},
		{"Mutex.new", "func f() { m := Mutex.new(1u64) }", []string{"oom"}},
		{"Condvar.new", "func f() { c := Condvar.new() }", []string{"oom"}},
		{"thread.spawn", "func w(x: u64) -> u64 { return x }\nfunc f() -> Result[u64, SpawnErr] {\n    t := thread.spawn(w, 1u64)?\n    return Ok(t.join())\n}", []string{"oom"}},
		{"thread.scope", "func f() {\n    with s := thread.scope() { }\n}", []string{"oom"}},
		{"scope.spawn", "func w(x: u64) -> u64 { return x }\nfunc f() -> Result[u64, SpawnErr] {\n    with s := thread.scope() {\n        j := s.spawn(w, 1u64)?\n        return Ok(j.get())\n    }\n}", []string{"oom"}},
		{"function value", "func f(g: func(u64) -> u64) -> u64 { return g(1u64) }", all},
		{"no trap", "func f(a: u64) -> u64 { return a }", []string{}},
	}
	for _, c := range cases {
		mod := lowerPlain(t, "module m\n"+c.body+"\n")
		e := effectsOf(t, mod, "m.f")
		if !reflect.DeepEqual(e.TrapKinds, c.want) {
			t.Errorf("%s: trap_kinds %v, want %v", c.name, e.TrapKinds, c.want)
		}
		if e.Traps != (len(e.TrapKinds) > 0) {
			t.Errorf("%s: traps %v with trap_kinds %v", c.name, e.Traps, e.TrapKinds)
		}
	}
	// a caller has the union of its callees' kinds, in §6 order
	mod := lowerPlain(t, "module m\nfunc a(xs: view[]u64, i: usize) -> u64 { return xs[i] }\nfunc b() { m := Mutex.new(1u64) }\nfunc f(xs: view[]u64) -> u64 {\n    b()\n    return a(xs, 0usize)\n}\n")
	if got := effectsOf(t, mod, "m.f").TrapKinds; strings.Join(got, " ") != "bounds oom" {
		t.Errorf("union: %v", got)
	}
	// an Unreachable terminator lists unreachable
	um := &ir.Module{Funcs: []*ir.Func{{Name: "u", Blocks: []*ir.Block{{Term: &ir.Unreachable{}}}}}}
	if got := effectsOf(t, um, "u").TrapKinds; strings.Join(got, " ") != "unreachable" {
		t.Errorf("unreachable: %v", got)
	}
}
