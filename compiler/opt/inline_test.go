package opt

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
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
	// a trap record names the file by the path the module was loaded from,
	// a different temporary directory for each module: compare base names
	rec := recordDir.ReplaceAllString(errb.String(), `"file":"`)
	return out.String() + rec, status
}

var recordDir = regexp.MustCompile(`"file":"[^"]*/`)

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

// TestInlineReturnInsideUnsafe inlines callees that return from inside an
// unsafe block, at depth one and two, into a short-circuit condition inside
// the caller's own unsafe block: the copy must close the callee's open
// regions before the branch to the join, or the join's depth disagrees
// between the short-circuit path and the inlined path.
func TestInlineReturnInsideUnsafe(t *testing.T) {
	src := `module t
contract { requires "x is small" }
func bump(x: u64) -> u64 { unsafe { return x + 1u64 } }
contract { requires "x is small" }
func deep(x: u64) -> u64 {
    unsafe {
        if x == 0u64 { return 7u64 }
        unsafe { return x * 2u64 }
    }
}
contract { requires "nothing" }
func main() -> i32 {
    a := 3u64
    unsafe {
        if a != 0u64 && bump(a) > 2u64 { print_u64(deep(a)) } else { print_u64(deep(0u64)) }
    }
    println()
    print_u64(deep(0u64))
    println()
    return 0i32
}
`
	_, inl := same(t, src)
	if got := calls(inl.Func("t.main")); len(got) != 0 {
		t.Fatalf("main still calls %v", got)
	}
}

// TestInlineLoopCallee: a callee with a loop is inlined like any other
// within MaxCalleeSize; the copied loop runs as the call did (the first
// inliner excluded loops, docs/reports/inliner-loops.md).
func TestInlineLoopCallee(t *testing.T) {
	src := `module t
func count(n: u64) -> u64 {
    i := 0u64
    while i < n { i = i + 1u64 }
    return i
}
func main() -> i32 {
    print_u64(count(3u64) + count(5u64))
    println()
    return 0i32
}
`
	_, inl := same(t, src)
	if got := calls(inl.Func("t.main")); len(got) != 0 {
		t.Fatalf("main still calls %v", got)
	}
	if !hasLoop(inl.Func("t.main")) {
		t.Fatal("the copied loop should be in main")
	}
}

func TestInlineStaysCall(t *testing.T) {
	cases := map[string]struct{ src, callee string }{
		"loop into loop": {`module t
func count(n: u64) -> u64 {
    i := 0u64
    while i < n { i = i + 1u64 }
    return i
}
func main() -> i32 {
    total := 0u64
    k := 0u64
    while k < 3u64 { total = total + count(k); k = k + 1u64 }
    print_u64(total)
    println()
    return 0i32
}
`, "t.count"},
		"closure param": {`module t
func apply(f: closure(u64) -> u64, x: u64) -> u64 { return f(x) }
func main() -> i32 {
    k := 3u64
    print_u64(apply(|x| x + k, 4u64))
    println()
    return 0i32
}
`, "t.apply"},
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
		// two returns of an access cannot define the call's destination
		// directly, and an access cannot go through a result local
		// (specs/inline-access)
		"access returned twice": {`module t
struct Box2 { v: u64; w: u64 }
func pick(b: view[Box2], first: bool) -> view[u64] {
    if first { return b.v }
    return b.w
}
func main() -> i32 {
    b := Box2 { v: 3u64, w: 4u64 }
    print_u64(pick(b, true))
    println()
    return 0i32
}
`, "t.pick"},
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
// stays within the budget and the pass terminates quickly. Every level is
// eligible (5 as lowered), so each grows by one copy of its predecessor
// within MaxGrowth; main, which calls the last level once, holds that
// level's copy outside its budget and its own budget on top.
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
	bound := MaxGrowth + MaxCalleeSize + 8
	for _, f := range mod.Funcs {
		limit := bound
		if f.Name == "t.main" {
			limit += size(mod.Func(fmt.Sprintf("t.f%d", depth)))
		}
		if s := size(f); s > limit {
			t.Fatalf("%s grew to size %d, limit %d", f.Name, s, limit)
		}
	}
}

// TestInlineThenProve: after inlining, a length guard in the caller proves
// the byte reader's checks at constant offsets (PLAN.md 6.4).
func TestInlineThenProve(t *testing.T) {
	src := `module t
func rd(p: view[]u8, at: usize) -> u16 {
    return (u16(p[at]) << 8u32) | u16(p[at + 1usize])
}
func parse(pkt: view[]u8) -> u64 {
    if len(pkt) < 4usize { return 0u64 }
    return u64(rd(pkt, 0usize)) + u64(rd(pkt, 2usize)) + u64(rd(pkt, 3usize))
}
func main() -> i32 {
    v := Vec[u8].new()
    v.push(1u8)
    v.push(2u8)
    v.push(3u8)
    v.push(4u8)
    v.push(5u8)
    v.push(6u8)
    print_u64(parse(v.as_view()))
    println()
    print_u64(parse(v.as_view()))
    println()
    return 0i32
}
`
	// parse is small as lowered, so it is inlined into main at both sites
	// (with rd inside it); the checks are read from main, where the proof
	// pass sees them, one run of six per site. The bytes come from a Vec
	// so that only the guard, not a constant array length, proves them.
	mod := lowerSrc(t, src)
	if err := Run(mod, Options{Inline: true}); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, b := range mod.Func("t.main").Blocks {
		for _, in := range b.Instrs {
			if cb, ok := in.(*ir.CheckBounds); ok && cb.Index != nil {
				if cb.Prov.Label == "proven" {
					got = append(got, cb.Prov.ProvenBy)
				} else {
					got = append(got, "runtime")
				}
			}
		}
	}
	// offsets 0, 1, 2, 3 are covered by the guard; 4 is not
	site := "dominated_by_check dominated_by_check dominated_by_check dominated_by_check dominated_by_check runtime"
	want := site + " " + site
	if g := strings.Join(got, " "); g != want {
		t.Fatalf("checks %q, want %q\n%s", g, want, ir.Print(mod))
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

// TestInlineOrderKeepsLimit: mid calls two small helpers and is itself
// small as lowered, but grows past MaxCalleeSize once they are inlined
// into it, which happens before main is visited (callees first). The
// callee is judged by its size as lowered, so main still inlines it and
// all three end up inlined.
func TestInlineOrderKeepsLimit(t *testing.T) {
	src := `module t
func lo(a: u64, b: u64) -> u64 {
    x := a * 3u64 + b
    y := x ^ (a << 2u32)
    return y + (b >> 1u32)
}
func hi(a: u64, b: u64) -> u64 {
    x := a + b * 5u64
    y := x & (b | 1u64)
    return y - (a >> 3u32)
}
func mid(a: u64, b: u64) -> u64 {
    p := lo(a, b)
    q := hi(b, a)
    return p + q
}
func main() -> i32 {
    print_u64(mid(1u64, 2u64))
    println()
    print_u64(mid(5u64, 7u64))
    println()
    return 0i32
}
`
	plain, inl := same(t, src)
	if n := size(plain.Func("t.mid")); n > MaxCalleeSize {
		t.Fatalf("mid is %d as lowered, the case needs it within %d", n, MaxCalleeSize)
	}
	if n := size(inl.Func("t.mid")); n <= MaxCalleeSize {
		t.Fatalf("mid is %d with its callees inlined, the case needs it past %d", n, MaxCalleeSize)
	}
	if got := calls(inl.Func("t.main")); len(got) != 0 {
		t.Fatalf("main still calls %v\n%s", got, ir.Print(inl))
	}
	for _, name := range []string{"t.lo", "t.hi", "t.mid"} {
		if !inl.Func(name).Inlined {
			t.Errorf("%s should be marked Inlined", name)
		}
	}
}

// bigSrc is a program with one straight-line helper well past
// MaxCalleeSize; sites is the number of calls main makes to it, and ref
// also takes it as a function value.
func bigSrc(sites int, ref bool) string {
	var sb strings.Builder
	sb.WriteString("module t\nfunc big(a: u64) -> u64 {\n    x := a\n")
	for i := 0; i < 24; i++ {
		fmt.Fprintf(&sb, "    x = (x << 1u32) ^ (x >> 3u32) ^ %du64\n", i+11)
	}
	sb.WriteString("    return x\n}\n")
	if ref {
		sb.WriteString("func apply(f: func(u64) -> u64, n: u64) -> u64 { return f(n) }\n")
	}
	sb.WriteString("func main() -> i32 {\n")
	for i := 0; i < sites; i++ {
		fmt.Fprintf(&sb, "    print_u64(big(%du64))\n    println()\n", i+1)
	}
	if ref {
		sb.WriteString("    print_u64(apply(big, 9u64))\n    println()\n")
	}
	sb.WriteString("    return 0i32\n}\n")
	return sb.String()
}

// TestInlineSingleSite: a callee past MaxCalleeSize is inlined at its
// only call site, since the copy replaces the body; the same callee with
// two sites, or one site and a func_ref, stays a call.
func TestInlineSingleSite(t *testing.T) {
	if n := size(lowerSrc(t, bigSrc(1, false)).Func("t.big")); n <= MaxCalleeSize {
		t.Fatalf("big is %d, the case needs it past %d", n, MaxCalleeSize)
	}
	_, inl := same(t, bigSrc(1, false))
	if got := calls(inl.Func("t.main")); len(got) != 0 {
		t.Fatalf("the only site should be inlined, main still calls %v", got)
	}
	if !inl.Func("t.big").Inlined {
		t.Fatalf("big should be marked Inlined")
	}
	for name, src := range map[string]string{"two sites": bigSrc(2, false), "one site and a func_ref": bigSrc(1, true)} {
		_, inl := same(t, src)
		if got := calls(inl.Func("t.main")); len(got) == 0 {
			t.Errorf("%s: big should stay a call\n%s", name, ir.Print(inl))
		}
		if inl.Func("t.big").Inlined {
			t.Errorf("%s: big must not be marked Inlined", name)
		}
	}
	defer func(v bool) { InlineSingleSite = v }(InlineSingleSite)
	InlineSingleSite = false
	_, inl = same(t, bigSrc(1, false))
	if got := calls(inl.Func("t.main")); len(got) != 1 {
		t.Fatalf("with the rule off, big should stay a call, main calls %v", got)
	}
}

// TestInlineSingleSiteOutsideBudget: a single-site copy is not charged
// against the caller's growth budget (the module does not grow by it),
// so a budget that admits only the two small helper sites still sees
// the big callee inlined before them and the helpers after it.
func TestInlineSingleSiteOutsideBudget(t *testing.T) {
	src := bigSrc(1, false)
	src = strings.Replace(src, "    return 0i32\n}\n", `    var buf: [4]u8 = zero()
    buf[1usize] = 2u8
    print_u64(u64(rd(buf, 0usize)))
    println()
    print_u64(u64(rd(buf, 2usize)))
    println()
    return 0i32
}
func rd(p: view[]u8, at: usize) -> u16 {
    return (u16(p[at]) << 8u32) | u16(p[at + 1usize])
}
`, 1)
	plain := lowerSrc(t, src)
	var first *ir.Call
	for _, b := range plain.Func("t.main").Blocks {
		for _, in := range b.Instrs {
			if c, ok := in.(*ir.Call); ok && c.Callee == "t.rd" && first == nil {
				first = c
			}
		}
	}
	defer func(v int) { MaxGrowth = v }(MaxGrowth)
	MaxGrowth = 2 * growthOf(first, plain.Func("t.rd"))
	if g := growthOf(first, plain.Func("t.big")); g <= MaxGrowth {
		t.Fatalf("big grows the caller by %d, the case needs it past the budget %d", g, MaxGrowth)
	}
	_, inl := same(t, src)
	if got := calls(inl.Func("t.main")); len(got) != 0 {
		t.Fatalf("big and both rd sites should be inlined, main still calls %v", got)
	}
}

// TestInlineSingleSiteChainBounded: a chain of functions each calling its
// predecessor once, with some arithmetic of its own, is inlined level by
// level; MaxCallerSize stops a function from taking a single-site copy
// past it, so no function grows beyond that plus the ordinary budget and
// the retained IR stays linear in the chain's length.
func TestInlineSingleSiteChainBounded(t *testing.T) {
	var sb strings.Builder
	// (named c, not f: f32 would be the float type)
	sb.WriteString("module t\nfunc c0(a: u64) -> u64 { return a ^ 5u64 }\n")
	const depth = 40
	for i := 1; i <= depth; i++ {
		fmt.Fprintf(&sb, "func c%d(a: u64) -> u64 {\n    x := (a << 1u32) ^ (a >> 2u32) ^ %du64\n    y := (x << 3u32) ^ (x >> 1u32)\n    r := c%d(y)\n    return r ^ x\n}\n", i, i+3, i-1)
	}
	fmt.Fprintf(&sb, "func main() -> i32 {\n    print_u64(c%d(7u64))\n    println()\n    return 0i32\n}\n", depth)
	src := sb.String()
	defer func(a, b int) { MaxCallerSize, MaxGrowth = a, b }(MaxCallerSize, MaxGrowth)
	MaxCallerSize, MaxGrowth = 160, 48
	_, inl := same(t, src)
	total := 0
	for _, f := range inl.Funcs {
		s := size(f)
		total += s
		if s > MaxCallerSize+MaxGrowth {
			t.Fatalf("%s grew to size %d, limit %d", f.Name, s, MaxCallerSize+MaxGrowth)
		}
	}
	if total > (depth+2)*(MaxCallerSize+MaxGrowth) {
		t.Fatalf("retained IR is %d over %d functions", total, len(inl.Funcs))
	}
	// the chain is still collapsed as far as the bound allows: main holds
	// more than one level
	if n := size(inl.Func("t.main")); n < 2*size(lowerSrc(t, src).Func("t.c1")) {
		t.Fatalf("main is %d, expected it to hold more than one level of the chain", n)
	}
}

// ---- cross-file inlining (specs/cross-file-inlining/SPEC.md) ----

// sameFiles lowers a multi-file program twice, inlines one copy, and
// compares the interpreter's output of both; it returns the inlined module.
func sameFiles(t *testing.T, files map[string]string) *ir.Module {
	t.Helper()
	plain := lowerFiles(t, files)
	inl := lowerFiles(t, files)
	Inline(inl)
	validate(t, inl)
	wantOut, wantStatus := run(t, plain)
	gotOut, gotStatus := run(t, inl)
	if gotOut != wantOut || gotStatus != wantStatus {
		t.Fatalf("inlined program differs: status %d vs %d\n--- plain\n%s--- inlined\n%s\n%s", gotStatus, wantStatus, wantOut, gotOut, ir.Print(inl))
	}
	return inl
}

// checkText returns, for every check or trap of f, the source text its
// position denotes in its block's effective file, keyed by the base name
// of that file: the pair (file, position) must identify the source.
func checkText(t *testing.T, files map[string]string, f *ir.Func) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, b := range f.Blocks {
		for _, in := range b.Instrs {
			var pos ast.Pos
			switch i := in.(type) {
			case *ir.CheckOverflow:
				pos = i.Pos
			case *ir.CheckBounds:
				pos = i.Pos
			case *ir.Trap:
				pos = i.Pos
			default:
				continue
			}
			file := b.FileIn(f)
			var name, src string
			for n, s := range files {
				if strings.HasSuffix(file, "/"+n) {
					name, src = n, s
				}
			}
			if name == "" {
				t.Fatalf("block %s of %s is in %q, not a file of the program", b.Name, f.Name, file)
			}
			if pos.End > len(src) {
				t.Fatalf("position %d-%d of a check in %s is past the end of %s", pos.Start, pos.End, f.Name, name)
			}
			out[name] = append(out[name], src[pos.Start:pos.End])
		}
	}
	return out
}

func hasText(xs []string, sub string) bool {
	for _, x := range xs {
		if strings.Contains(x, sub) {
			return true
		}
	}
	return false
}

// blockFiles returns each block's File keyed by name (R3 assertions).
func blockFiles(f *ir.Func) map[string]string {
	out := map[string]string{}
	for _, b := range f.Blocks {
		out[b.Name] = b.File
	}
	return out
}

func TestInlineCrossFile(t *testing.T) {
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
	mod := sameFiles(t, files)
	main := mod.Func("t.main")
	if got := calls(main); len(got) != 0 {
		t.Fatalf("a callee in another file must be inlined, got calls %v\n%s", got, ir.Print(mod))
	}
	if !mod.Func("t.twice").Inlined {
		t.Fatal("twice has no other reference and must be marked Inlined")
	}
	texts := checkText(t, files, main)
	if !hasText(texts["b.kern"], "n * 2u64") {
		t.Fatalf("the copied overflow check must be in a block naming b.kern: %v\n%s", texts, ir.Print(mod))
	}
	bf := blockFiles(main)
	if main.Entry.File != "" || bf["inline.join"] != "" {
		t.Fatalf("the call's block and the continuation stay in the caller's file: %v", bf)
	}
	if !strings.Contains(ir.Print(mod), "[file ") {
		t.Fatalf("the printer must show the copied block's file\n%s", ir.Print(mod))
	}
}

func TestInlineImportedModule(t *testing.T) {
	files := map[string]string{
		"app.kern": `module app
import "lib"
func main() -> i32 {
    print_u64(lib.Twice(4u64))
    println()
    return 0i32
}
`,
		"lib/lib.kern": `module lib
func Twice(n: u64) -> u64 { return n * 2u64 }
`,
	}
	mod := sameFiles(t, files)
	main := mod.Func("app.main")
	if got := calls(main); len(got) != 0 {
		t.Fatalf("a callee in an imported module must be inlined, got calls %v\n%s", got, ir.Print(mod))
	}
	if !mod.Func("lib.Twice").Inlined {
		t.Fatal("lib.Twice has no other reference and must be marked Inlined")
	}
	texts := checkText(t, files, main)
	if !hasText(texts["lib/lib.kern"], "n * 2u64") {
		t.Fatalf("the copied overflow check must be in a block naming lib/lib.kern: %v", texts)
	}
}

func TestInlineCrossFileOutParamStaysCall(t *testing.T) {
	files := map[string]string{
		"a.kern": `module t
func main() -> i32 {
    var v: u64
    fill(out v)
    print_u64(v)
    println()
    return 0i32
}
`,
		"b.kern": `module t
func fill(o: out u64) { o = 7u64 }
`,
	}
	mod := sameFiles(t, files)
	if got := calls(mod.Func("t.main")); len(got) != 1 || got[0] != "t.fill" {
		t.Fatalf("an out-parameter callee stays a call whatever its file, got %v", got)
	}
}

func TestInlineCrossFileLibraryStaysCall(t *testing.T) {
	files := map[string]string{
		"a.kern": `module t
func main() -> i32 {
    print_u64(first())
    println()
    return 0i32
}
`,
		"b.kern": `module t
func first() -> u64 {
    v := Vec[u64].new()
    v.push(4u64)
    return v.get(0usize)
}
`,
	}
	mod := sameFiles(t, files)
	for _, callee := range calls(mod.Func("t.main")) {
		if strings.HasPrefix(callee, "Vec.") {
			return
		}
	}
	t.Fatalf("library calls stay calls after a cross-file copy\n%s", ir.Print(mod))
}

func TestInlineCrossFileChain(t *testing.T) {
	files := map[string]string{
		"a.kern": `module t
func inc(n: u64) -> u64 { return n + 1u64 }
`,
		"b.kern": `module t
func twice_inc(n: u64) -> u64 { return inc(n) * 2u64 }
`,
		"c.kern": `module t
func main() -> i32 {
    print_u64(twice_inc(4u64))
    println()
    return 0i32
}
`,
	}
	mod := sameFiles(t, files)
	main := mod.Func("t.main")
	if got := calls(main); len(got) != 0 {
		t.Fatalf("both levels must be inlined, got %v\n%s", got, ir.Print(mod))
	}
	texts := checkText(t, files, main)
	if !hasText(texts["a.kern"], "n + 1u64") || !hasText(texts["b.kern"], "inc(n) * 2u64") {
		t.Fatalf("each copied check must name the file it came from: %v\n%s", texts, ir.Print(mod))
	}
}

func TestInlineCrossFileRoundTrip(t *testing.T) {
	files := map[string]string{
		"a.kern": `module t
func inc(n: u64) -> u64 { return n + 1u64 }
func main() -> i32 {
    print_u64(twice_inc(4u64))
    println()
    return 0i32
}
`,
		"b.kern": `module t
func twice_inc(n: u64) -> u64 { return inc(n) * 2u64 }
`,
	}
	mod := sameFiles(t, files)
	main := mod.Func("t.main")
	if got := calls(main); len(got) != 0 {
		t.Fatalf("both levels must be inlined, got %v\n%s", got, ir.Print(mod))
	}
	texts := checkText(t, files, main)
	if !hasText(texts["a.kern"], "n + 1u64") || !hasText(texts["b.kern"], "inc(n) * 2u64") {
		t.Fatalf("each copied check must name the file it came from: %v\n%s", texts, ir.Print(mod))
	}
	// the block holding inc's check came back to its own file: File empty
	for _, b := range main.Blocks {
		for _, in := range b.Instrs {
			if c, ok := in.(*ir.CheckOverflow); ok && c.Op == "add" && b.File != "" {
				t.Fatalf("a copy returning to its own file carries no File, got %q", b.File)
			}
		}
	}
}

func TestInlineSameFileNoBlockFile(t *testing.T) {
	_, inl := same(t, helperSrc)
	for _, f := range inl.Funcs {
		for _, b := range f.Blocks {
			if b.File != "" {
				t.Fatalf("a one-file program has no block file, %s has %q", f.Name, b.File)
			}
		}
	}
}

func TestLowerLeavesBlockFileEmpty(t *testing.T) {
	mod := lowerFiles(t, map[string]string{
		"a.kern": "module t\nfunc main() -> i32 { return i32(twice(1u64)) }\n",
		"b.kern": "module t\nfunc twice(n: u64) -> u64 { return n * 2u64 }\n",
	})
	for _, f := range mod.Funcs {
		for _, b := range f.Blocks {
			if b.File != "" {
				t.Fatalf("lowering sets no block file, %s has %q", f.Name, b.File)
			}
		}
	}
}

func TestValidateBlockFile(t *testing.T) {
	files := map[string]string{
		"a.kern": "module t\nfunc main() -> i32 { return i32(twice(1u64)) }\n",
		"b.kern": "module t\nfunc twice(n: u64) -> u64 { return n * 2u64 }\n",
	}
	mod := lowerFiles(t, files)
	Inline(mod)
	validate(t, mod)
	main := mod.Func("t.main")
	var copied *ir.Block
	for _, b := range main.Blocks {
		if b.File != "" {
			copied = b
		}
	}
	if copied == nil {
		t.Fatal("no copied block carries a file")
	}
	saved := copied.File
	copied.File = "nowhere.kern"
	errs := ir.Validate(mod)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "not declared by any function") {
		t.Fatalf("an undeclared block file must be the one error, got %v", errs)
	}
	copied.File = main.File // another function's file: declared
	if errs := ir.Validate(mod); len(errs) > 0 {
		t.Fatalf("a declared file validates, got %v", errs)
	}
	copied.File = saved
}

func TestInlineCrossFileDeterministic(t *testing.T) {
	files := map[string]string{
		"a.kern": `module t
func inc(n: u64) -> u64 { return n + 1u64 }
func main() -> i32 {
    print_u64(twice_inc(4u64) + twice_inc(5u64))
    println()
    return 0i32
}
`,
		"b.kern": `module t
func twice_inc(n: u64) -> u64 { return inc(n) * 2u64 }
`,
	}
	var prints []string
	for i := 0; i < 2; i++ {
		mod := lowerFiles(t, files)
		Inline(mod)
		prints = append(prints, labelDir.ReplaceAllString(ir.Print(mod), `[file `))
	}
	if prints[0] != prints[1] {
		t.Fatalf("two runs differ:\n%s\n---\n%s", prints[0], prints[1])
	}
}

// labelDir strips the temporary directory from a printed block's file.
var labelDir = regexp.MustCompile(`\[file [^\]]*/`)

func TestPrintBlockFile(t *testing.T) {
	mod := lowerFiles(t, map[string]string{
		"a.kern": "module t\nfunc main() -> i32 { return i32(twice(1u64)) }\n",
		"b.kern": "module t\nfunc twice(n: u64) -> u64 { return n * 2u64 }\n",
	})
	Inline(mod)
	text := ir.Print(mod)
	labels := regexp.MustCompile(`(?m)^(bb\d+\.\S+)( \[file (\S+)\])?:$`).FindAllStringSubmatch(text, -1)
	withFile, without := 0, 0
	for _, l := range labels {
		if l[2] != "" {
			withFile++
			if !strings.HasSuffix(l[3], "/b.kern") {
				t.Fatalf("label %q names a file other than b.kern", l[0])
			}
		} else {
			without++
		}
	}
	if withFile == 0 || without == 0 {
		t.Fatalf("expected labels with and without a file, got %d and %d\n%s", withFile, without, text)
	}
}

// A test function nothing calls is a root (specs/test-command/SPEC.md R7):
// listed in the module's Tests, it is not marked Inlined; otherwise it
// would be, like any function nothing references.
func TestInlineKeepsTestRoots(t *testing.T) {
	files := map[string]string{"t.kern": "module t\nfunc test_alone() { print_u64(1u64) }\nfunc main() -> i32 { return 0i32 }\n"}
	plain := lowerFiles(t, files)
	Inline(plain)
	if !plain.Func("t.test_alone").Inlined {
		t.Fatal("without the Tests list an unreferenced function is marked Inlined")
	}
	mod := lowerFiles(t, files)
	mod.Tests = []string{"t.test_alone"}
	Inline(mod)
	validate(t, mod)
	if mod.Func("t.test_alone").Inlined {
		t.Fatal("a test root must not be marked Inlined")
	}
	if !strings.Contains(ir.Print(mod), "\ntest t.test_alone\n") {
		t.Fatalf("the printer lists the test roots\n%s", ir.Print(mod))
	}
}

// TestInlineLibrary (specs/inline-library): small library bodies are
// copied into user functions after the proof pass, functions that write
// through a raw pointer stay calls, and every instruction of a copy
// in user code takes the call site's position, so a trap inside it
// reports the user-level call.
func TestInlineLibrary(t *testing.T) {
	src := "module t\nfunc main() -> i32 {\n    v := Vec[u64].new()\n    v.push(1u64)\n    print_u64(u64(v.len()))\n    print_u64(v.get(0usize))\n    expect(v.len() == 2usize)\n    return 0i32\n}\n"
	build := func(lib bool) *ir.Module {
		prog := resolve.ResolveFor([]*source.File{{Name: "t.kern", Bytes: []byte(src)}}, true)
		if ds := check.Check(prog); len(prog.Diags)+len(ds) > 0 {
			t.Fatalf("check: %v %v", prog.Diags, ds)
		}
		mod, ds := lower.Lower(prog)
		if len(ds) > 0 {
			t.Fatal(ds)
		}
		if err := Run(mod, Options{Inline: true, InlineLibrary: lib}); err != nil {
			t.Fatal(err)
		}
		return mod
	}
	calls := func(mod *ir.Module) map[string]bool {
		out := map[string]bool{}
		for _, b := range mod.Func("t.main").Blocks {
			for _, in := range b.Instrs {
				if c, ok := in.(*ir.Call); ok && c.Intrinsic == "" {
					out[baseName(c.Callee)] = true
				}
			}
		}
		return out
	}
	before, after := calls(build(false)), calls(build(true))
	for _, name := range []string{"Vec.new", "Vec.len", "Vec.get", "core.expect"} {
		if !before[name] || after[name] {
			t.Errorf("%s: called before %v, after %v; want copied", name, before[name], after[name])
		}
	}
	// a function that writes through a raw pointer stays a call, for
	// LLVM to inline with its receiver noalias (docs/reports/native-ab.md)
	if !after["Vec.push"] {
		t.Errorf("Vec.push must stay a call: %v", after)
	}
	// every copied trap is at the call it was copied for: expect's, and
	// Vec.get's bounds trap
	span := func(call string) [2]int { // the call to the end of its line
		at := strings.Index(src, call)
		return [2]int{at, strings.Index(src[at:], ")\n") + at + 1}
	}
	get := strings.Index(src, "v.get(")
	sites := map[[2]int]bool{span("expect("): true, {get, get + len("v.get(0usize)")}: true}
	seen := map[[2]int]bool{}
	for _, b := range build(true).Func("t.main").Blocks {
		for _, in := range b.Instrs {
			if tr, ok := in.(*ir.Trap); ok {
				p := [2]int{tr.Pos.Start, tr.Pos.End}
				if !sites[p] {
					t.Errorf("copied trap at %d-%d, at none of the calls %v", p[0], p[1], sites)
				}
				seen[p] = true
			}
		}
	}
	if !seen[span("expect(")] {
		t.Errorf("no copied trap at the expect call: %v", seen)
	}
}

// TestLibraryInlinerOptions: the library inliner runs only where a
// release program executes; the show-* commands' options never include
// it, so their user-level facts read library calls as calls
// (specs/inline-library R1).
func TestLibraryInlinerOptions(t *testing.T) {
	for _, lvl := range []string{"debug", "release"} {
		if OptionsFor(lvl).InlineLibrary {
			t.Errorf("OptionsFor(%s) includes the library inliner", lvl)
		}
		if got := ExecOptionsFor(lvl); got.InlineLibrary != (lvl == "release") || got.Inline != OptionsFor(lvl).Inline {
			t.Errorf("ExecOptionsFor(%s) = %+v", lvl, got)
		}
	}
}

// A function returning a projection of its parameter is copied: the copy
// defines the call's destination, which the caller's access_end ends,
// and the program's output is the same (specs/inline-access).
func TestInlineAccessReturn(t *testing.T) {
	src := `module t
struct Box2 { v: u64; w: u64 }
func at(b: view[Box2]) -> view[u64] { return b.v }
func at_mut(b: edit[Box2]) -> edit[u64] { return b.w }
func main() -> i32 {
    b := Box2 { v: 3u64, w: 4u64 }
    print_u64(at(b))
    with w := at_mut(b) { w = w + 10u64 }
    print_u64(b.w)
    println()
    return 0i32
}
`
	_, inl := same(t, src)
	for _, callee := range calls(inl.Func("t.main")) {
		if callee == "t.at" || callee == "t.at_mut" {
			t.Fatalf("%s should be copied\n%s", callee, ir.Print(inl))
		}
	}
}
