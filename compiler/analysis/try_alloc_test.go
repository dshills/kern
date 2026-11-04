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
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// tryAllocProgram calls every fallible allocation form at T = u64 (no
// destructor), E = () and C = u64, every BoxIn and VecIn method at A =
// SystemAllocator, and Vec.push, the walk's control.
const tryAllocProgram = `module m
import "std/str"
import "std/map"
func init_n(n: out u64) -> Result[(), ()] {
    n = 1u64
    return Ok(())
}
func init_c(n: out u64, c: view[u64]) -> Result[(), ()] {
    n = c
    return Ok(())
}
func boxes() -> Result[(), AllocErr] {
    b := Box[u64].try_new(1u64)?
    return Ok(())
}
func boxes_with() -> Result[(), AllocOrInit[()]] {
    c := 2u64
    b := Box[u64].try_new_with(init_n)?
    d := Box[u64].try_new_with_ctx(init_c, c)?
    return Ok(())
}
func strings() -> Result[(), AllocErr] {
    s := str.String.try_from("ab")?
    s.try_reserve(8usize)?
    s.try_push(99u8)?
    s.try_push_str("de")?
    return Ok(())
}
func allocin() -> Result[(), AllocErr] {
    b := BoxIn[u64, SystemAllocator].try_new_in(1u64, SystemAllocator.new())?
    with g := b.get() { n := g }
    with g := b.at() { g = 2u64 }
    with a := b.allocator() { }
    b.release()
    v := VecIn[u64, SystemAllocator].new_in(SystemAllocator.new())
    src := Vec[u64].new()
    v.try_reserve(4usize)?
    v.try_push(1u64)?
    v.try_extend(src.as_view())?
    n := v.len() + v.capacity()
    with a := v.allocator() { }
    with g := v.get(0usize) { m := g }
    with g := v.at(0usize) { g = 3u64 }
    with s := v.as_view() { }
    with s := v.as_edit() { }
    o := v.pop()
    v.clear()
    v.release()
    return Ok(())
}
func maps() -> Result[(), AllocErr] {
    m := map.Map[u64, u64].try_new()?
    o := m.try_insert(1u64, 2u64)?
    m.try_reserve(4usize)?
    return Ok(())
}
func mapins() -> Result[(), AllocErr] {
    m := map.MapIn[u64, u64, SystemAllocator].new_in(SystemAllocator.new())
    m.try_reserve(4usize)?
    o := m.try_insert(1u64, 2u64)?
    k := 1u64
    n := m.len()
    with a := m.allocator() { }
    b := m.contains(1u64)
    with g := m.get(1u64) { x := g }
    with g := m.at(1u64) { g = 3u64 }
    match m.find(k) {
        Some(s) => {
            with g := m.key_at(s) { x := g }
            with g := m.value_at(s) { x := g }
            with g := m.value_at_mut(s) { g = 4u64 }
        }
        None => { }
    }
    for kk, vv in m { x := kk }
    with kk, vv := m.cursor_entry_mut(m.cursor_first()) { vv = 5u64 }
    r := m.remove(1u64)
    m.release()
    return Ok(())
}
// informs calls the construct-in-place forms and get2 of ArenaIn and
// PoolIn (specs/in-forms R5)
func informs() -> Result[(), AllocOrInit[()]] {
    c := 3u64
    a := ArenaIn[u64, SystemAllocator].new_in(SystemAllocator.new())
    x := a.try_alloc_with(init_n)?
    y := a.try_alloc_with_ctx(init_c, c)?
    p := PoolIn[u64, SystemAllocator].new_in(SystemAllocator.new())
    i := p.try_alloc_with(init_n)?
    j := p.try_alloc_with_ctx(init_c, c)?
    with g, h := p.get2(i, j) { g = h }
    return Ok(())
}
func controls() {
    b := Box.new(1u64)
    m := map.Map[u64, u64].new()
    o := m.insert(1u64, 2u64)
    m.reserve(8usize)
}
func use_with(r: Result[(), AllocOrInit[()]]) {
    match r {
        Ok(u) => { }
        Err(e) => { }
    }
}
func use(r: Result[(), AllocErr]) {
    match r {
        Ok(u) => { }
        Err(e) => { }
    }
}
func with_cap() -> Result[(), AllocErr] {
    w := Vec[u64].try_with_capacity(4usize)?
    return Ok(())
}
func main() -> i32 {
    v := Vec[u64].new()
    src := Vec[u64].new()
    use(v.try_reserve(4usize))
    use(v.try_push(1u64))
    use(v.try_extend(src.as_view()))
    use(v.try_resize(8usize, 2u64))
    use(with_cap())
    use(boxes())
    use_with(boxes_with())
    use_with(informs())
    use(strings())
    use(allocin())
    use(maps())
    use(mapins())
    controls()
    v.push(3u64)
    return 0i32
}
`

// fallibleForms are the fallible methods whose own code, and (but for the
// _with constructors, which call the caller's initializer and are listed
// apart) whose effect summaries, reach no oom trap: the try_ forms
// (specs/fallible-alloc), every BoxIn and VecIn method except the
// destructors, which only call release, and the seed_len test hook
// (specs/alloc-containers R5), the Map forms (specs/effect-trap-kinds),
// every MapIn method (specs/map-in) and PoolIn.get2 (specs/in-forms).
var fallibleForms = []string{
	"Box.try_new",
	"Vec.try_with_capacity", "Vec.try_reserve", "Vec.try_push", "Vec.try_extend", "Vec.try_resize",
	"String.try_from", "String.try_reserve", "String.try_push", "String.try_push_str",
	"BoxIn.try_new_in", "BoxIn.get", "BoxIn.at", "BoxIn.allocator", "BoxIn.release",
	"VecIn.new_in", "VecIn.len", "VecIn.capacity", "VecIn.allocator", "VecIn.try_reserve",
	"VecIn.try_push", "VecIn.try_extend", "VecIn.pop", "VecIn.get", "VecIn.at",
	"VecIn.as_view", "VecIn.as_edit", "VecIn.clear", "VecIn.release",
	"Map.try_new", "Map.try_insert", "Map.try_reserve",
	// every MapIn method but the destructor (specs/map-in R6): each and
	// each_mut call the caller's closure, so their summaries are element
	// code's and are left out as the _with constructors' are
	"MapIn.new_in", "MapIn.len", "MapIn.allocator", "MapIn.state", "MapIn.key_ptr", "MapIn.val_ptr",
	"MapIn.slot", "MapIn.try_rebuild", "MapIn.free_table", "MapIn.try_reserve", "MapIn.try_insert",
	"MapIn.place", "MapIn.contains", "MapIn.get", "MapIn.at", "MapIn.find", "MapIn.check_slot",
	"MapIn.key_at", "MapIn.value_at", "MapIn.value_at_mut", "MapIn.remove", "MapIn.live_from",
	"MapIn.cursor_first", "MapIn.cursor_next", "MapIn.cursor_end", "MapIn.cursor_entry",
	"MapIn.cursor_entry_mut", "MapIn.release", "map.probe_in",
	"PoolIn.get2",
}

// withForms call the caller's initializer through a function value, so
// their effect summaries are element code's (label unknown) and only their
// own code is checked for oom: Box's (specs/fallible-alloc) and the In
// containers' (specs/in-forms R5).
var withForms = []string{
	"Box.try_new_with", "Box.try_new_with_ctx",
	"ArenaIn.try_alloc_with", "ArenaIn.try_alloc_with_ctx",
	"PoolIn.try_alloc_with", "PoolIn.try_alloc_with_ctx",
}

// plainName drops the type-argument lists of a lowered function's name, so
// Vec[u64].try_push and Vec.try_push[u64] are both Vec.try_push.
func plainName(name string) string {
	var b strings.Builder
	depth := 0
	for _, r := range name {
		switch {
		case r == '[':
			depth++
		case r == ']':
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// oomReach walks the statically resolved calls from root (calls through a
// function value, an initializer, are not followed) and returns the chain
// to the first function holding a trap of kind oom, or nil.
func oomReach(mod *ir.Module, root string) []string {
	seen := map[string]bool{}
	var walk func(name string, path []string) []string
	walk = func(name string, path []string) []string {
		if seen[name] {
			return nil
		}
		seen[name] = true
		f := mod.Func(name)
		if f == nil {
			return nil
		}
		// a copy per level, so sibling calls never share a backing array
		path = append(append([]string(nil), path...), plainName(name))
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				switch i := in.(type) {
				case *ir.Trap:
					if i.Kind == "oom" {
						return path
					}
				case *ir.Call:
					if i.Fn == nil && i.Intrinsic == "" && i.Callee != "" {
						if p := walk(i.Callee, path); p != nil {
							return p
						}
					}
				}
			}
		}
		return nil
	}
	return walk(root, nil)
}

// No fallible allocation form reaches a trap of kind oom through library
// code (specs/fallible-alloc/SPEC.md R4). The program is lowered without
// optimization, so every library call is still a call. Vec.push must reach
// one, or the walk could pass without seeing a trap at all.
func TestTryFormsReachNoOOMTrap(t *testing.T) {
	prog := resolve.ResolveFor([]*source.File{{Name: "m.kern", Bytes: []byte(tryAllocProgram)}}, false)
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
	roots := map[string][]string{}
	for _, f := range mod.Funcs {
		n := plainName(f.Name)
		roots[n] = append(roots[n], f.Name)
	}
	if len(roots["Vec.push"]) == 0 {
		t.Fatal("Vec.push not lowered")
	}
	for _, name := range roots["Vec.push"] {
		if oomReach(mod, name) == nil {
			t.Errorf("control: no oom trap reached from %s", name)
		}
	}
	want := append(append([]string{}, withForms...), fallibleForms...)
	for _, w := range want {
		if len(roots[w]) == 0 {
			var names []string
			for n := range roots {
				if strings.Contains(n, ".try_") {
					names = append(names, n)
				}
			}
			sort.Strings(names)
			t.Errorf("%s not lowered (lowered try_ functions: %s)", w, strings.Join(names, ", "))
			continue
		}
		for _, name := range roots[w] {
			if p := oomReach(mod, name); p != nil {
				t.Errorf("%s reaches an oom trap: %s", name, strings.Join(p, " -> "))
			}
		}
	}
}

// No fallible form's show-effects summary lists oom, while the trapping
// forms' summaries do (specs/effect-trap-kinds R4).
func TestFallibleFormsListNoOOM(t *testing.T) {
	mod := lowerPlain(t, tryAllocProgram)
	// every lowered function of each plain name (several instantiations
	// may share one) is checked
	byName := map[string][][]string{}
	for _, f := range analysis.Effects(mod, "").Functions {
		n := plainName(f.Name)
		byName[n] = append(byName[n], f.TrapKinds)
	}
	find := func(w string) [][]string {
		var out [][]string
		for n, ks := range byName {
			if n == w || strings.HasSuffix(n, "."+w) {
				out = append(out, ks...)
			}
		}
		return out
	}
	hasOOM := func(ks []string) bool {
		for _, k := range ks {
			if k == "oom" {
				return true
			}
		}
		return false
	}
	for _, w := range fallibleForms {
		all := find(w)
		if len(all) == 0 {
			t.Errorf("%s has no effect summary", w)
		}
		for _, ks := range all {
			if hasOOM(ks) {
				t.Errorf("%s lists oom: %v", w, ks)
			}
		}
	}
	// the _with forms call the caller's initializer through a function
	// value, so their summaries are every kind with label unknown (element
	// code); their own code is TestTryFormsReachNoOOMTrap's
	isWith := map[string]bool{}
	for _, w := range withForms {
		isWith[w] = true
	}
	seenWith := map[string]bool{}
	for _, f := range analysis.Effects(mod, "").Functions {
		n := plainName(f.Name)
		if isWith[n] {
			seenWith[n] = true
			if f.Label != diag.LabelUnknown {
				t.Errorf("%s: label %s, want unknown", n, f.Label)
			}
		}
	}
	for _, w := range withForms {
		if !seenWith[w] {
			t.Errorf("%s has no effect summary", w)
		}
	}
	for _, w := range []string{"Box.new", "Vec.push", "Map.insert", "Map.reserve"} {
		all := find(w)
		if len(all) == 0 {
			t.Errorf("control %s has no effect summary", w)
		}
		for _, ks := range all {
			if !hasOOM(ks) {
				t.Errorf("control %s does not list oom: %v", w, ks)
			}
		}
	}
}
