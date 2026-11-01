package analysis_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/check"
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
    use(strings())
    use(allocin())
    v.push(3u64)
    return 0i32
}
`

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
	want := []string{
		"Box.try_new", "Box.try_new_with", "Box.try_new_with_ctx",
		"Vec.try_with_capacity", "Vec.try_reserve", "Vec.try_push", "Vec.try_extend", "Vec.try_resize",
		"String.try_from", "String.try_reserve", "String.try_push", "String.try_push_str",
		// specs/alloc-containers R5: every BoxIn and VecIn method except
		// the destructors, which only call release (walked here), and the
		// seed_len test hook
		"BoxIn.try_new_in", "BoxIn.get", "BoxIn.at", "BoxIn.allocator", "BoxIn.release",
		"VecIn.new_in", "VecIn.len", "VecIn.capacity", "VecIn.allocator", "VecIn.try_reserve",
		"VecIn.try_push", "VecIn.try_extend", "VecIn.pop", "VecIn.get", "VecIn.at",
		"VecIn.as_view", "VecIn.as_edit", "VecIn.clear", "VecIn.release",
	}
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
