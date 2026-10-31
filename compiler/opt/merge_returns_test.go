package opt

import (
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/ir"
)

// merged lists the functions of mod that gained a merged ret block.
func merged(mod *ir.Module) []string {
	var out []string
	for _, f := range mod.Funcs {
		for _, b := range f.Blocks {
			if b.Name == "ret.merge" {
				out = append(out, f.Name)
			}
		}
	}
	return out
}

// sameMerged merges a copy of the program and checks the interpreter
// agrees with the original on output and status.
func sameMerged(t *testing.T, src string) *ir.Module {
	t.Helper()
	plain := lowerSrc(t, src)
	mod := lowerSrc(t, src)
	MergeReturns(mod)
	validate(t, mod)
	wantOut, wantStatus := run(t, plain)
	gotOut, gotStatus := run(t, mod)
	if gotOut != wantOut || gotStatus != wantStatus {
		t.Fatalf("merged program differs: status %d vs %d\n--- plain\n%s--- merged\n%s\n%s", gotStatus, wantStatus, wantOut, gotOut, ir.Print(mod))
	}
	return mod
}

// The arena_ast shape: two returns that call the same function with
// unions that differ only in their tag become one call, so the inliner
// copies the callee once.
func TestMergeReturnsTag(t *testing.T) {
	src := `module t
union Node { Leaf(u64); Add(l: u64, r: u64); Mul(l: u64, r: u64) }
struct Acc { n: u64; sum: u64 }
func weight(e: Node) -> u64 {
    match e {
        Leaf(v) => return v
        Add(l, r) => return l + r
        Mul(l, r) => return l * r
    }
}
func put(a: edit[Acc], e: Node) -> u64 {
    a.n = a.n + 1u64
    a.sum = a.sum + weight(e)
    return a.n
}
func make(a: edit[Acc], x: u64) -> u64 {
    l := x + 1u64
    r := x + 2u64
    if x % 2u64 == 0u64 { return put(a, Add(l, r)) }
    return put(a, Mul(l, r))
}
func main() -> i32 {
    var a: Acc = Acc { n: 0u64, sum: 0u64 }
    var x: u64 = 0u64
    while x < 6u64 {
        make(a, x)
        x = x + 1u64
    }
    print_u64(a.n); print_str(" "); print_u64(a.sum); println()
    return 0i32
}
`
	mod := sameMerged(t, src)
	if got := merged(mod); len(got) != 1 || got[0] != "t.make" {
		t.Fatalf("merged %v, want [t.make]\n%s", got, ir.Print(mod))
	}
	if got := calls(mod.Func("t.make")); strings.Join(got, ",") != "t.put" {
		t.Fatalf("t.make calls %v after merging, want one call to t.put", got)
	}
	// through the release pipeline the one call is the one copy of put:
	// one store to a.n where, without the pass, each return had its own
	for _, c := range []struct {
		o    Options
		want int
	}{{ExecOptionsFor("release"), 1}, {Options{Inline: true, InlineLibrary: true}, 2}} {
		mod := lowerSrc(t, src)
		if err := Run(mod, c.o); err != nil {
			t.Fatal(err)
		}
		stores := 0
		for _, b := range mod.Func("t.make").Blocks {
			for _, in := range b.Instrs {
				if s, ok := in.(*ir.Store); ok && len(s.Place.Path) > 0 && s.Place.Path[len(s.Place.Path)-1].Name == "n" {
					stores++
				}
			}
		}
		if stores != c.want || len(calls(mod.Func("t.make"))) != 0 {
			t.Fatalf("t.make with %+v: %d stores to a.n and calls %v, want %d and none\n%s", c.o, stores, calls(mod.Func("t.make")), c.want, ir.Print(mod))
		}
	}
}

// Returns that differ in a constant argument merge as well.
func TestMergeReturnsConst(t *testing.T) {
	mod := sameMerged(t, `module t
func scale(x: u64, k: u64) -> u64 { return x * k + 1u64 }
func pick(x: u64) -> u64 {
    if x > 3u64 { return scale(x, 3u64) }
    return scale(x, 5u64)
}
func main() -> i32 {
    print_u64(pick(2u64)); print_str(" "); print_u64(pick(7u64)); println()
    return 0i32
}
`)
	if got := merged(mod); len(got) != 1 || got[0] != "t.pick" {
		t.Fatalf("merged %v, want [t.pick]", got)
	}
}

// What the pass leaves alone: returns whose checks report different
// positions (a trap must name its own expression), returns that share no
// call (nothing would be copied twice), and returns that differ in more
// than a tag or a constant.
func TestMergeReturnsLeaves(t *testing.T) {
	for name, src := range map[string]string{
		"check positions": `module t
func g(x: u64) -> u64 { return x * 2u64 }
func f(x: u64, c: bool) -> u64 {
    if c { return g(x + 1u64) }
    return g(x + 1u64)
}
func main() -> i32 { print_u64(f(18446744073709551615u64, false)); println(); return 0i32 }
`,
		"no call": `module t
func f(c: bool) -> u64 {
    if c { return 1u64 }
    return 2u64
}
func main() -> i32 { print_u64(f(true)); println(); return 0i32 }
`,
		"other operands": `module t
func g(x: u64) -> u64 { return x * 2u64 }
func f(x: u64, y: u64, c: bool) -> u64 {
    if c { return g(x) }
    return g(y)
}
func main() -> i32 { print_u64(f(1u64, 2u64, true)); println(); return 0i32 }
`,
	} {
		t.Run(name, func(t *testing.T) {
			mod := sameMerged(t, src)
			if got := merged(mod); len(got) != 0 {
				t.Fatalf("merged %v, want none\n%s", got, ir.Print(mod))
			}
		})
	}
}

// The trap that a merged block can still raise reports the position the
// original block's check carried: the check positions agree or the
// blocks were not merged, and the release pipeline's trap record matches
// the unoptimized one.
func TestMergeReturnsTrapSpan(t *testing.T) {
	src := `module t
func g(x: u64, k: u64) -> u64 { return x + k }
func f(x: u64, c: bool) -> u64 {
    if c { return g(x, 1u64) }
    return g(x, 2u64)
}
func main() -> i32 { print_u64(f(18446744073709551614u64, false)); println(); return 0i32 }
`
	plain := lowerSrc(t, src)
	mod := lowerSrc(t, src)
	if err := Run(mod, ExecOptionsFor("release")); err != nil {
		t.Fatal(err)
	}
	wantOut, wantStatus := run(t, plain)
	gotOut, gotStatus := run(t, mod)
	if gotOut != wantOut || gotStatus != wantStatus || !strings.Contains(gotOut, "overflow") {
		t.Fatalf("release pipeline: status %d %q, want status %d %q (an overflow trap)", gotStatus, gotOut, wantStatus, wantOut)
	}
}

// A load stays behind every instruction that may write its local: a
// write to the local itself, a write through any access (one opened
// before the block may alias it), a call, and a release function.
func TestMergeReturnsLoadBarriers(t *testing.T) {
	l, other := &ir.Local{ID: 0}, &ir.Local{ID: 1}
	acc, v := &ir.Value{ID: 0}, &ir.Value{ID: 1}
	for _, c := range []struct {
		in   ir.Instr
		want bool
	}{
		{&ir.Store{Place: ir.Place{Local: l}, Src: v}, true},
		{&ir.Store{Place: ir.Place{Local: other}, Src: v}, false},
		{&ir.Store{Place: ir.Place{Access: acc}, Src: v}, true},
		{&ir.Move{Dst: v, Src: ir.Place{Access: acc}}, true},
		{&ir.Drop{Place: ir.Place{Access: acc}}, true},
		{&ir.Init{Place: ir.Place{Access: acc}}, true},
		{&ir.Call{Callee: "g"}, true},
		{&ir.AccessBegin{Dst: v, Place: ir.Place{Local: l}}, true},
		{&ir.AccessBegin{Dst: v, Place: ir.Place{Access: acc}}, false},
		{&ir.AccessEnd{Access: acc, Release: "rel"}, true},
		{&ir.AccessEnd{Access: acc}, false},
		{&ir.Load{Dst: v, Place: ir.Place{Access: acc}}, false},
	} {
		if got := touches(c.in, l); got != c.want {
			t.Errorf("touches(%T %+v) = %v, want %v", c.in, c.in, got, c.want)
		}
	}
}
