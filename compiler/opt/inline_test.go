package opt

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/interp"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/lower"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// lowerFiles lowers one module given as named sources.
func lowerFiles(t *testing.T, files map[string]string) *ir.Module {
	t.Helper()
	dir := t.TempDir()
	var srcs []*source.File
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	// deterministic file order
	for i := range names {
		for j := i + 1; j < len(names); j++ {
			if names[j] < names[i] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	for _, name := range names {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(files[name]), 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := source.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		srcs = append(srcs, f)
	}
	prog := resolve.Resolve(srcs)
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
	if errs := ir.Validate(mod); len(errs) > 0 {
		t.Fatalf("validator before inlining: %v", errs[0])
	}
	return mod
}

func lowerSrc(t *testing.T, src string) *ir.Module {
	t.Helper()
	return lowerFiles(t, map[string]string{"t.kern": src})
}

// run executes a module in the interpreter and returns its output and status.
func run(t *testing.T, mod *ir.Module) (string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	it := interp.New(mod, "t.kern", &out, &errb)
	status := it.Run()
	return out.String() + errb.String(), status
}

// calls lists the direct callees of a function, in order.
func calls(f *ir.Func) []string {
	var out []string
	for _, b := range f.Blocks {
		for _, in := range b.Instrs {
			if c, ok := in.(*ir.Call); ok && c.Fn == nil && c.Intrinsic == "" {
				out = append(out, c.Callee)
			}
		}
	}
	return out
}

func validate(t *testing.T, mod *ir.Module) {
	t.Helper()
	if errs := ir.Validate(mod); len(errs) > 0 {
		t.Fatalf("validator after inlining: %v\n%s", errs[0], ir.Print(mod))
	}
}

// same inlines a copy of the program and checks the interpreter agrees
// with the original on output and status.
func same(t *testing.T, src string) (*ir.Module, *ir.Module) {
	t.Helper()
	plain := lowerSrc(t, src)
	inl := lowerSrc(t, src)
	Inline(inl)
	validate(t, inl)
	wantOut, wantStatus := run(t, plain)
	gotOut, gotStatus := run(t, inl)
	if gotOut != wantOut || gotStatus != wantStatus {
		t.Fatalf("inlined program differs: status %d vs %d\n--- plain\n%s--- inlined\n%s\n%s", gotStatus, wantStatus, wantOut, gotOut, ir.Print(inl))
	}
	return plain, inl
}

const helperSrc = `module t
func rd(p: view[]u8, at: usize) -> u16 {
    return (u16(p[at]) << 8u32) | u16(p[at + 1usize])
}
func main() -> i32 {
    var buf: [4]u8 = zero()
    buf[0usize] = 1u8
    buf[1usize] = 2u8
    buf[3usize] = 7u8
    print_u64(u64(rd(buf, 0usize)))
    println()
    print_u64(u64(rd(buf, 2usize)))
    println()
    return 0i32
}
`

func TestInlineHelper(t *testing.T) {
	plain, inl := same(t, helperSrc)
	main := inl.Func("t.main")
	if got := calls(main); len(got) != 0 {
		t.Fatalf("main still calls %v\n%s", got, ir.Print(inl))
	}
	// the copied checks keep the callee's positions
	want := map[ast.Pos]bool{}
	for _, b := range plain.Func("t.rd").Blocks {
		for _, in := range b.Instrs {
			if cb, ok := in.(*ir.CheckBounds); ok {
				want[cb.Pos] = true
			}
		}
	}
	got := map[ast.Pos]int{}
	for _, b := range main.Blocks {
		for _, in := range b.Instrs {
			if cb, ok := in.(*ir.CheckBounds); ok {
				got[cb.Pos]++
			}
		}
	}
	for p := range want {
		if got[p] != 2 { // two call sites
			t.Errorf("check at %v copied %d times, want 2", p, got[p])
		}
	}
	if !inl.Func("t.rd").Inlined {
		t.Errorf("rd should be marked Inlined")
	}
	if inl.Func("t.main").Inlined {
		t.Errorf("main must not be marked Inlined")
	}
}

func TestInlineOwnParamDestructor(t *testing.T) {
	src := `module t
struct Tok nocopy { id: u64 }
impl Tok {
    destructor(edit self) {
        print_str("drop ")
        print_u64(self.id)
        println()
    }
}
func consume(tk: own Tok, extra: u64) -> u64 {
    return tk.id + extra
}
func main() -> i32 {
    a := Tok { id: 5u64 }
    print_u64(consume(a, 1u64))
    println()
    b := Tok { id: 9u64 }
    x := consume(b, 0u64)
    print_u64(x)
    println()
    return 0i32
}
`
	_, inl := same(t, src)
	if got := calls(inl.Func("t.main")); len(got) != 0 {
		t.Fatalf("main still calls %v", got)
	}
	out, _ := run(t, inl)
	if strings.Count(out, "drop 5") != 1 || strings.Count(out, "drop 9") != 1 {
		t.Fatalf("each token must be destroyed once:\n%s", out)
	}
}

func TestInlineAccessParamReborrow(t *testing.T) {
	src := `module t
struct Inner { x: u64; y: u64 }
struct Pair { a: Inner; b: Inner }
func sum(p: view[Pair]) -> u64 {
    i := p.a
    return i.x + p.b.y
}
func bump(p: edit[Pair]) {
    p.a.x = p.a.x + 10u64
}
func main() -> i32 {
    var q: Pair = Pair { a: Inner { x: 1u64, y: 2u64 }, b: Inner { x: 3u64, y: 4u64 } }
    bump(q)
    print_u64(sum(q))
    println()
    return 0i32
}
`
	_, inl := same(t, src)
	if got := calls(inl.Func("t.main")); len(got) != 0 {
		t.Fatalf("main still calls %v", got)
	}
}

func TestInlineTwoReturnPaths(t *testing.T) {
	src := `module t
func pick(a: u64, b: u64, first: bool) -> u64 {
    if first { return a }
    return b
}
func early(n: u64) -> u64 {
    if n == 0u64 { return 100u64 }
    m := n * 2u64
    return m
}
func main() -> i32 {
    print_u64(pick(1u64, 2u64, true))
    println()
    print_u64(pick(1u64, 2u64, false))
    println()
    print_u64(early(0u64))
    println()
    print_u64(early(21u64))
    println()
    return 0i32
}
`
	_, inl := same(t, src)
	if got := calls(inl.Func("t.main")); len(got) != 0 {
		t.Fatalf("main still calls %v", got)
	}
}

func TestInlineStaysCall(t *testing.T) {
	cases := map[string]struct{ src, callee string }{
		"loop": {`module t
func count(n: u64) -> u64 {
    i := 0u64
    while i < n { i = i + 1u64 }
    return i
}
func main() -> i32 {
    print_u64(count(3u64))
    println()
    return 0i32
}
`, "t.count"},
		"out param": {`module t
struct Cell { x: u64 }
func fill(o: out Cell) { o.x = 7u64 }
func main() -> i32 {
    var v: Cell
    fill(out v)
    print_u64(v.x)
    println()
    return 0i32
}
`, "t.fill"},
		"self recursion": {`module t
func fact(n: u64) -> u64 {
    if n <= 1u64 { return 1u64 }
    return n * fact(n - 1u64)
}
func main() -> i32 {
    print_u64(fact(5u64))
    println()
    return 0i32
}
`, "t.fact"},
		"mutual recursion": {`module t
func even(n: u64) -> bool {
    if n == 0u64 { return true }
    return odd(n - 1u64)
}
func odd(n: u64) -> bool {
    if n == 0u64 { return false }
    return even(n - 1u64)
}
func main() -> i32 {
    print_bool(even(4u64))
    println()
    return 0i32
}
`, "t.even"},
		"access return": {`module t
struct Box2 { v: u64 }
func at(b: view[Box2]) -> view[u64] { return b.v }
func main() -> i32 {
    b := Box2 { v: 3u64 }
    print_u64(at(b))
    println()
    return 0i32
}
`, "t.at"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var inl *ir.Module
			if name == "out param" {
				// the interpreter cannot yet run a program with an out
				// parameter (it panics resolving the out storage), so this
				// case checks the IR only
				inl = lowerSrc(t, c.src)
				Inline(inl)
				validate(t, inl)
			} else {
				_, inl = same(t, c.src)
			}
			found := false
			for _, callee := range calls(inl.Func("t.main")) {
				if callee == c.callee {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s should stay a call\n%s", c.callee, ir.Print(inl))
			}
			if inl.Func(c.callee).Inlined {
				t.Fatalf("%s must not be marked Inlined", c.callee)
			}
		})
	}
}

func TestInlineReleaseFunctionKept(t *testing.T) {
	src := `module t
struct Guard { v: u64; held: u64 }
impl Guard {
    func lock(view self) -> view[u64] release unlock { return self.v }
    func unlock(view self) { }
}
func main() -> i32 {
    g := Guard { v: 5u64, held: 0u64 }
    with x := g.lock() {
        print_u64(x)
        println()
    }
    return 0i32
}
`
	_, inl := same(t, src)
	if inl.Func("Guard.unlock").Inlined {
		t.Fatalf("a release function referenced only through Release must not be marked Inlined\n%s", ir.Print(inl))
	}
}

func TestInlineLibraryStaysCall(t *testing.T) {
	src := `module t
func main() -> i32 {
    v := Vec[u64].new()
    v.push(4u64)
    print_u64(v.get(0usize))
    println()
    return 0i32
}
`
	_, inl := same(t, src)
	for _, callee := range calls(inl.Func("t.main")) {
		if strings.HasPrefix(callee, "Vec.") {
			return
		}
	}
	t.Fatalf("library calls must stay calls\n%s", ir.Print(inl))
}

func TestInlineCrossFileStaysCall(t *testing.T) {
	files := map[string]string{
		"a.kern": `module t
func main() -> i32 {
    print_u64(twice(4u64))
    println()
    return 0i32
}
`,
		"b.kern": `module t
func twice(n: u64) -> u64 { return n * 2u64 }
`,
	}
	mod := lowerFiles(t, files)
	Inline(mod)
	validate(t, mod)
	if got := calls(mod.Func("t.main")); len(got) != 1 || got[0] != "t.twice" {
		t.Fatalf("a callee in another file must stay a call, got %v", got)
	}
}

func TestInlineIndirectStaysCall(t *testing.T) {
	src := `module t
func twice(n: u64) -> u64 { return n * 2u64 }
func apply(f: func(u64) -> u64, n: u64) -> u64 { return f(n) }
func main() -> i32 {
    print_u64(apply(twice, 4u64))
    println()
    return 0i32
}
`
	_, inl := same(t, src)
	// apply is inlined into main; the indirect call inside it stays, and
	// twice, taken by func_ref, is not marked Inlined
	indirect := 0
	for _, b := range inl.Func("t.main").Blocks {
		for _, in := range b.Instrs {
			if c, ok := in.(*ir.Call); ok && c.Fn != nil {
				indirect++
			}
		}
	}
	if indirect != 1 {
		t.Fatalf("expected the indirect call in main, found %d\n%s", indirect, ir.Print(inl))
	}
	if inl.Func("t.twice").Inlined {
		t.Fatalf("a function taken by func_ref must not be marked Inlined")
	}
}

func TestInlineThresholds(t *testing.T) {
	g := lowerSrc(t, helperSrc).Func("t.rd")
	n := size(g)
	defer func(a, b int) { MaxCalleeSize, MaxGrowth = a, b }(MaxCalleeSize, MaxGrowth)
	MaxCalleeSize = n - 1
	inl := lowerSrc(t, helperSrc)
	Inline(inl)
	if got := calls(inl.Func("t.main")); len(got) != 2 {
		t.Fatalf("size %d above the limit %d must stay a call, got %v", n, MaxCalleeSize, got)
	}
	MaxCalleeSize = n
	inl = lowerSrc(t, helperSrc)
	Inline(inl)
	if got := calls(inl.Func("t.main")); len(got) != 0 {
		t.Fatalf("size %d at the limit must inline, got %v", n, got)
	}
	// the growth budget admits the first site exactly and refuses the second
	inl = lowerSrc(t, helperSrc)
	var first *ir.Call
	for _, b := range inl.Func("t.main").Blocks {
		for _, in := range b.Instrs {
			if c, ok := in.(*ir.Call); ok && c.Callee == "t.rd" && first == nil {
				first = c
			}
		}
	}
	MaxGrowth = growthOf(first, inl.Func("t.rd"))
	Inline(inl)
	validate(t, inl)
	if got := calls(inl.Func("t.main")); len(got) != 1 {
		t.Fatalf("the growth budget should leave one call, got %v", got)
	}
	if inl.Func("t.rd").Inlined {
		t.Fatalf("a callee with a remaining call must not be marked Inlined")
	}
}

// TestInlineChainBounded: a chain of empty functions each calling its
// predecessor twice would double the caller's blocks at every level if
// only instructions counted; the size metric counts blocks, so the growth
// stays within the budget and the pass terminates quickly.
func TestInlineChainBounded(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("module t\nfunc f0() { }\n")
	const depth = 24
	for i := 1; i <= depth; i++ {
		fmt.Fprintf(&sb, "func f%d() {\n    f%d()\n    f%d()\n}\n", i, i-1, i-1)
	}
	fmt.Fprintf(&sb, "func main() -> i32 {\n    f%d()\n    return 0i32\n}\n", depth)
	mod := lowerSrc(t, sb.String())
	Inline(mod)
	validate(t, mod)
	for _, f := range mod.Funcs {
		if s := size(f); s > MaxGrowth+MaxCalleeSize+8 {
			t.Fatalf("%s grew to size %d", f.Name, s)
		}
	}
}

func TestInlineDeterministic(t *testing.T) {
	a := lowerSrc(t, helperSrc)
	b := lowerSrc(t, helperSrc)
	Inline(a)
	Inline(b)
	if ir.Print(a) != ir.Print(b) {
		t.Fatalf("two runs differ")
	}
	_ = fmt.Sprint
}
