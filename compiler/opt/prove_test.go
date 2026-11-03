package opt

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/lower"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

const proveSource = `module t
func guard(data: view[]u8) -> u64 {
    total := 0u64
    i := 0usize
    while i < len(data) {
        total = total + u64(data[i])
        i = i + 1usize
    }
    return total
}
func pair(p: view[]u8) -> u32 {
    s := 0u32
    i := 0usize
    while i + 1usize < len(p) {
        s = wrapping_add(s, u32(p[i]))
        s = wrapping_add(s, u32(p[i + 1usize]))
        s = wrapping_add(s, u32(p[i + 2usize]))
        i = i + 2usize
    }
    if i < len(p) { s = wrapping_add(s, u32(p[i])) }
    return s
}
func strided(data: view[]u8, stride: usize) -> u64 {
    total := 0u64
    i := 0usize
    while i < len(data) {
        total = total + u64(data[i])
        total = total + u64(data[i + stride])
        i = i + stride
    }
    return total
}
func consts() -> u64 {
    var a: [4]u64 = zero()
    a[1usize] = 5u64
    i := 3usize
    x := a[i]
    return x + a[2usize]
}
func after(data: view[]u8, i: usize) -> u64 {
    if i < len(data) { return u64(data[i]) }
    return u64(data[i])
}
func stored(data: edit[]u8, i: usize) -> u64 {
    if i < len(data) {
        data[i] = 1u8
        j := i + 1usize
        return u64(data[j])
    }
    return 0u64
}
func wrapped(data: view[]u8, i: usize) -> u64 {
    j := wrapping_add(i, 1usize)
    if j < len(data) { return u64(data[i]) }
    return 0u64
}
func header(p: view[]u8) -> u64 {
    if len(p) < 4usize { return 0u64 }
    return u64(p[3usize]) + u64(p[4usize])
}
func header_ge(p: view[]u8) -> u64 {
    if len(p) >= 4usize { return u64(p[3usize]) + u64(p[4usize]) }
    return 0u64
}
func header_le(p: view[]u8) -> u64 {
    if len(p) <= 4usize { return 0u64 }
    return u64(p[4usize]) + u64(p[5usize])
}
func header_rev(p: view[]u8) -> u64 {
    if 4usize < len(p) { return u64(p[4usize]) + u64(p[5usize]) }
    return 0u64
}
func header_other(p: view[]u8) -> u64 {
    if 4usize < len(p) { return 0u64 }
    return u64(p[0usize])
}
func signed(p: view[]u8, i: i64) -> u64 {
    if i < 4i64 { return u64(p[usize(i)]) }
    return 0u64
}
struct Counter { idx: usize; pad: u64 }
unsafe assert Interior for Counter
contract { invariant "idx is only ever advanced by one through this view" }
func bump(c: view[Counter]) { unsafe { c.idx = c.idx + 1usize } }
func interior_local(a: view[]u8) -> u64 {
    var c: Counter = Counter { idx: 1usize, pad: 0u64 }
    i := c.idx
    if i < len(a) {
        x := a[i]
        bump(c)
        j := c.idx
        return u64(x) + u64(a[j])
    }
    return 0u64
}
func same(c: view[Counter]) -> view[Counter] { return c }
func interior_alias(a: view[]u8) -> u64 {
    var c: Counter = Counter { idx: 1usize, pad: 0u64 }
    i := c.idx
    if i < len(a) {
        x := a[i]
        with d := same(c) {
            bump(d)
        }
        j := c.idx
        return u64(x) + u64(a[j])
    }
    return 0u64
}
func tail(p: view[]u8) -> view[]u8 { return p[2usize : len(p)] }
func shorter(p: view[]u8) -> u64 {
    if len(p) < 4usize { return 0u64 }
    with q := tail(p) {
        return u64(q[3usize])
    }
}
func interior(a: view[]u8, c: view[Counter]) -> u64 {
    i := c.idx
    if i < len(a) {
        x := a[i]
        bump(c)
        j := c.idx
        return u64(x) + u64(a[j])
    }
    return 0u64
}
`

// TestProve checks the proof facts the lowering records on bounds checks
// (ADR-008 invariant 4): which checks it proves and, as importantly, which
// it leaves runtime-checked.
func TestProve(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prove.kern")
	if err := os.WriteFile(path, []byte(proveSource), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := source.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	prog := resolve.Resolve([]*source.File{f})
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
	Prove(mod)
	if errs := ir.Validate(mod); len(errs) > 0 {
		t.Fatalf("validator: %v", errs[0])
	}
	want := map[string]string{
		"t.guard":   "dominated_by_check",
		"t.pair":    "dominated_by_check dominated_by_check runtime dominated_by_check",
		"t.strided": "dominated_by_check runtime",
		"t.consts":  "constant constant constant",
		"t.after":   "dominated_by_check runtime",
		"t.stored":  "dominated_by_check runtime",
		"t.wrapped": "runtime", // a wrapping addition proves nothing about its operand
		// `!(len < 4)` proves index 3 and not 4; so does `len >= 4`
		"t.header":    "dominated_by_check runtime",
		"t.header_ge": "dominated_by_check runtime",
		// `!(len <= 4)` proves 4 and not 5; so does `4 < len`
		"t.header_le":  "dominated_by_check runtime",
		"t.header_rev": "dominated_by_check runtime",
		// `!(4 < len)` bounds len from above: nothing about an index
		"t.header_other": "runtime",
		// a signed comparison proves nothing
		"t.signed": "runtime",
		// a call that receives a view of a local may write it (Interior)
		"t.interior":       "dominated_by_check runtime",
		"t.interior_local": "dominated_by_check runtime", // a view of a local passed to a call forgets the local
		"t.interior_alias": "dominated_by_check runtime", // so does a projection a call returned from that view
		"t.shorter":        "runtime",                    // a returned projection is not the argument's place
	}
	for name, exp := range want {
		fn := mod.Func(name)
		if fn == nil {
			t.Fatalf("no function %s", name)
		}
		var got []string
		for _, b := range fn.Blocks {
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
		if g := strings.Join(got, " "); g != exp {
			t.Errorf("%s: checks %q, want %q\n%s", name, g, exp, ir.Print(mod))
		}
	}
}

// proveWith lowers a source and runs the pass, returning the module.
func proveWith(t *testing.T, src string) *ir.Module {
	t.Helper()
	mod := lowered(t, src)
	Prove(mod)
	if errs := ir.Validate(mod); len(errs) > 0 {
		t.Fatalf("validator: %v", errs[0])
	}
	return mod
}

// runWith lowers a source and runs the pipeline o selects, returning the
// module.
func runWith(t *testing.T, src string, o Options) *ir.Module {
	t.Helper()
	mod := lowered(t, src)
	if err := Run(mod, o); err != nil {
		t.Fatal(err)
	}
	return mod
}

// lowered resolves, checks and lowers a source.
func lowered(t *testing.T, src string) *ir.Module {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "prove.kern")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := source.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	prog := resolve.Resolve([]*source.File{f})
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

// checkLabels renders a function's bounds checks (index and slice) in
// block order: the proof fact, or "runtime".
func checkLabels(mod *ir.Module, name string) string {
	fn := mod.Func(name)
	if fn == nil {
		return "<no function>"
	}
	var got []string
	for _, b := range fn.Blocks {
		for _, in := range b.Instrs {
			if cb, ok := in.(*ir.CheckBounds); ok {
				if cb.Prov.Label == "proven" {
					got = append(got, cb.Prov.ProvenBy)
				} else {
					got = append(got, "runtime")
				}
			}
		}
	}
	return strings.Join(got, " ")
}

const formsSource = `module t
struct Ring { buf: [8]u32; head: usize }
func cached(xs: view[]u64) -> u64 {
    n := len(xs)
    var i: usize = 0usize
    var s: u64 = 0u64
    while i < n { s = s + xs[i]; i = i + 1usize }
    return s
}
func join_store(tab: edit[]u8, v: u8) {
    var i: usize = 0usize
    while i < len(tab) {
        c := v
        if c > 9u8 { c = 9u8 }
        tab[i] = c
        i = i + 1usize
    }
}
func range_loop(xs: view[]u64) -> u64 {
    var s: u64 = 0u64
    for i in 0usize..len(xs) { s = s + xs[i] }
    return s
}
func both(a: view[]u64, b: view[]u64) -> u64 {
    var s: u64 = 0u64
    var i: usize = 0usize
    while i < len(a) && i < len(b) { s = s + a[i] * b[i]; i = i + 1usize }
    return s
}
func either(xs: view[]u8, i: usize) -> bool {
    if i >= len(xs) || xs[i] == 0u8 { return true }
    return false
}
func window(xs: view[]u64) -> u64 {
    var s: u64 = 0u64
    var i: usize = 0usize
    while i + 2usize <= len(xs) { s = s + xs[i] + xs[i + 1usize] + xs[i + 2usize]; i = i + 2usize }
    return s
}
func exactly4(xs: view[]u8) -> u8 {
    if len(xs) == 4usize { return xs[3usize] + xs[4usize] }
    return 0u8
}
func same_len(a: view[]u64, b: view[]u64) -> u64 {
    var s: u64 = 0u64
    if len(a) == len(b) {
        var i: usize = 0usize
        while i < len(a) { s = s + a[i] * b[i]; i = i + 1usize }
    }
    return s
}
func ring_get(r: view[Ring], idx: usize) -> u32 {
    if idx < 8usize { return r.buf[idx] }
    return 0u32
}
func stride6(xs: view[]u8, i: usize) -> u8 {
    if i * 6usize + 5usize < len(xs) { return xs[i * 6usize + 5usize] }
    return 0u8
}
func classify(tab: view[]u8, b: u8) -> u8 {
    if usize(b) < len(tab) { return tab[usize(b)] }
    return 0u8
}
func prev(xs: view[]u64, i: usize) -> u64 {
    if i < len(xs) { if i >= 1usize { return xs[i - 1usize] } }
    return 0u64
}
func put2(dst: edit[]u8, i: usize, a: u8, b: u8) {
    if i + 1usize < len(dst) { dst[i] = a; dst[i + 1usize] = b }
}
func fill(xs: edit[]u8, v: u8) { var i: usize = 0usize; while i < len(xs) { xs[i] = v; i = i + 1usize } }
func after_call(xs: edit[]u8, i: usize) -> u8 {
    if i < len(xs) { fill(xs, 1u8); return xs[i] }
    return 0u8
}
func sum(xs: view[]u64) -> u64 { var s: u64 = 0u64; for x in xs { s = s + x }; return s }
func slices(xs: view[]u64, i: usize) -> u64 {
    var s: u64 = 0u64
    if i + 2usize <= len(xs) { s = s + sum(xs[i:i + 2usize]) }
    if i < len(xs) { s = s + sum(xs[i:len(xs)]) }
    var a: [4]u64 = zero()
    s = s + sum(a[1usize:3usize]) + sum(a[0usize:5usize])
    return s
}
func lockstep(a: view[]u64, b: view[]u64) -> u64 {
    var s: u64 = 0u64
    var i: usize = 0usize
    while i < len(a) { s = s + a[i] * b[i]; i = i + 1usize }
    return s
}
func wrapped(xs: view[]u8, i: usize) -> u8 {
    if i + 1usize < len(xs) { return xs[wrapping_add(i, 1usize)] }
    return 0u8
}
func wrapped_same(xs: view[]u8, i: usize) -> u8 {
    if wrapping_add(i, 1usize) < len(xs) { return xs[wrapping_add(i, 1usize)] }
    return 0u8
}
func not_equal(xs: view[]u8, i: usize) -> u8 {
    if i != len(xs) { if i < 100usize { return xs[i] } }
    return 0u8
}
func main() -> i32 { return 0i32 }
`

// TestProveForms pins the proof of every form of specs/prove-guards/SPEC.md
// F1–F9 and the non-proof of the forms its §3 excludes.
func TestProveForms(t *testing.T) {
	mod := proveWith(t, formsSource)
	want := map[string]string{
		"t.cached":     "dominated_by_check",
		"t.join_store": "dominated_by_check",
		"t.range_loop": "dominated_by_check",
		"t.both":       "dominated_by_check dominated_by_check",
		"t.either":     "dominated_by_check",
		// `i + 2 <= len` proves i and i + 1, not i + 2
		"t.window":     "dominated_by_check dominated_by_check runtime",
		"t.exactly4":   "dominated_by_check runtime",
		"t.same_len":   "dominated_by_check dominated_by_check",
		"t.ring_get":   "dominated_by_check",
		"t.stride6":    "dominated_by_check",
		"t.classify":   "dominated_by_check",
		"t.prev":       "dominated_by_check",
		"t.put2":       "dominated_by_check dominated_by_check",
		"t.fill":       "dominated_by_check",
		"t.after_call": "dominated_by_check",
		// the two guarded slices, then the two literal slices of a [4] array
		// (the second runs past the array)
		"t.slices":       "dominated_by_check dominated_by_check constant runtime",
		"t.lockstep":     "dominated_by_check runtime",
		"t.wrapped":      "runtime",
		"t.wrapped_same": "dominated_by_check",
		"t.not_equal":    "runtime",
	}
	for name, exp := range want {
		if got := checkLabels(mod, name); got != exp {
			t.Errorf("%s: checks %q, want %q", name, got, exp)
		}
	}
}

const solverSource = `module t
func stored_in_loop(xs: view[]u64, n: usize) -> u64 {
    var s: u64 = 0u64
    var i: usize = 0usize
    var j: usize = n
    if j < len(xs) {
        while i < 3usize {
            s = s + xs[j]
            j = j + 1usize
            i = i + 1usize
        }
    }
    return s
}
func nested(xs: view[]u64, m: view[]u64) -> u64 {
    var s: u64 = 0u64
    var i: usize = 0usize
    while i < len(xs) {
        var j: usize = 0usize
        while j < i {
            s = s + xs[j] + xs[i]
            j = j + 1usize
        }
        i = i + 1usize
    }
    return s
}
func cached(xs: view[]u64) -> u64 {
    n := len(xs)
    var i: usize = 0usize
    var s: u64 = 0u64
    while i < n { s = s + xs[i]; i = i + 1usize }
    return s
}
func direct(xs: view[]u64) -> u64 {
    var i: usize = 0usize
    var s: u64 = 0u64
    while i < len(xs) { s = s + xs[i]; i = i + 1usize }
    return s
}
func straight(xs: view[]u64, i: usize) -> u64 {
    if i < len(xs) { return xs[i] }
    return 0u64
}
func main() -> i32 { return 0i32 }
`

// TestProveSolver checks the fixed point: a fact round one holds
// optimistically and the back edge removes must not survive, nested loops
// keep the outer guard, and the pessimistic fallback past the round bound
// stays sound while still reading edge facts.
func TestProveSolver(t *testing.T) {
	mod := proveWith(t, solverSource)
	for name, exp := range map[string]string{
		// xs[j] is guarded before the loop, but j changes on the back edge
		"t.stored_in_loop": "runtime",
		// xs[j] needs j < i < len(xs), a transitive step the pass does not
		// take; xs[i]: the outer guard holds in the inner loop
		"t.nested":   "runtime dominated_by_check",
		"t.cached":   "dominated_by_check",
		"t.direct":   "dominated_by_check",
		"t.straight": "dominated_by_check",
	} {
		if got := checkLabels(mod, name); got != exp {
			t.Errorf("%s: checks %q, want %q", name, got, exp)
		}
	}
	// the fallback: a header starts from no facts, so a cached length no
	// longer reaches the body, while an edge fact on the loop's own
	// comparison and straight-line code still prove
	defer func(n int) { maxRounds = n }(maxRounds)
	maxRounds = 1
	mod = proveWith(t, solverSource)
	for name, exp := range map[string]string{
		"t.stored_in_loop": "runtime",
		"t.cached":         "runtime",
		"t.direct":         "dominated_by_check",
		"t.straight":       "dominated_by_check",
	} {
		if got := checkLabels(mod, name); got != exp {
			t.Errorf("fallback %s: checks %q, want %q", name, got, exp)
		}
	}
}

const masksSource = `module t
struct Q { ring: [2048]u64; head: usize; len: usize }
func direct(q: edit[Q], b: u64) { q.ring[(q.head + q.len) & 2047usize] = b }
func local(a: view[[1024]u64], x: usize) -> u64 { j := x & 1023usize; return a[j] }
func conv(a: view[[1024]u64], x: u64) -> u64 { return a[usize(x & 1023u64)] }
func off(a: view[[9]u64], x: usize) -> u64 { return a[(x & 7usize) + 1usize] }
func eq(a: view[[8]u64], x: usize) -> u64 { return a[x & 8usize] }
func over(a: view[[8]u64], x: usize) -> u64 { return a[(x & 7usize) + 1usize] }
func guarded(p: view[]u64, x: usize) -> u64 {
    if len(p) >= 1024usize { return p[x & 1023usize] }
    return 0u64
}
func param(a: view[[8]u64], x: usize, m: usize) -> u64 { return a[x & m] }
func zero(a: view[[1]u64], x: usize) -> u64 { return a[x & 0usize] }
func both(a: view[[8]u64], x: usize) -> u64 {
    if x < 8usize { return a[x & 7usize] }
    return 0u64
}
func neg(a: view[[8]u64], x: usize) -> u64 { return a[(x & 7usize) - 1usize] }
func big(a: view[[8]u64], x: usize) -> u64 { return a[x & 4611686018427387904usize] }
func u8mask(a: view[[16]u64], x: u8) -> u64 { return a[usize(x & 15u8)] }
func u32mask(a: view[[16]u64], x: u32) -> u64 { return a[usize(x & 15u32)] }
`

// TestProveMasks: an index bounded by a constant mask is proven against an
// array's length (constant) or a guarded lower bound (dominated_by_check),
// and nowhere else (specs/prove-masks).
func TestProveMasks(t *testing.T) {
	mod := proveWith(t, masksSource)
	want := map[string]string{
		"t.direct":  "constant",
		"t.local":   "constant",
		"t.conv":    "constant",
		"t.off":     "constant",
		"t.eq":      "runtime", // x & 8 can be 8, the length
		"t.over":    "runtime", // (x & 7) + 1 can be 8
		"t.guarded": "dominated_by_check",
		"t.param":   "runtime", // a mask by a value that is not a constant
		"t.zero":    "constant",
		"t.both":    "constant", // the mask rule is tried before the guard rule
		"t.neg":     "constant", // (x & 7) - 1 is at most 6
		"t.big":     "runtime",  // the largest admitted constant, far past the length
		"t.u8mask":  "constant",
		"t.u32mask": "constant",
	}
	for name, exp := range want {
		if got := checkLabels(mod, name); got != exp {
			t.Errorf("%s: checks %q, want %q", name, got, exp)
		}
	}
}

// TestProveMaskFixtures pins the labels of the masked-index functions of
// the two run fixtures, which the harness executes in both engines
// (specs/prove-masks R4).
func TestProveMaskFixtures(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", "tests", "run", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	proven := proveWith(t, read("prove_forms.kern"))
	for _, fn := range []string{"mask_ring", "mask_local", "mask_conv", "mask_off"} {
		if got := checkLabels(proven, "run."+fn); got != "constant" {
			t.Errorf("%s: %q, want constant", fn, got)
		}
	}
	if got := checkLabels(proven, "run.mask_guard"); got != "dominated_by_check" {
		t.Errorf("mask_guard: %q, want dominated_by_check", got)
	}
	not := proveWith(t, read("prove_forms_not.kern"))
	for _, fn := range []string{"mask_len", "mask_past", "mask_var", "mask_call"} {
		if got := checkLabels(not, "run."+fn); got != "runtime" {
			t.Errorf("%s: %q, want runtime", fn, got)
		}
	}
}

const boundsSource = `module t
func rem(a: view[[8]u64], x: usize) -> u64 { return a[x % 8usize] }
func rem9(a: view[[8]u64], x: usize) -> u64 { return a[x % 9usize] }
func remoff(a: view[[8]u64], x: usize) -> u64 { return a[(x % 7usize) + 1usize] }
func remmask(a: view[[8]u64], x: usize) -> u64 { return a[(x % 100usize) & 7usize] }
func remconv(a: view[[256]u64], x: u64) -> u64 { return a[usize(x % 256u64)] }
func remlen(xs: view[]u64, i: usize) -> u64 { return xs[i % len(xs)] }
func bump(v: edit[usize]) { v = v + 1usize }
func ring(a: view[[8]u64], n: usize) -> u64 {
    var s: u64 = 0u64
    var w: usize = 0usize
    for k in 0usize..n {
        s = s + a[w]
        w = (w + 1usize) & 7usize
    }
    return s
}
func ringwide(a: view[[8]u64], n: usize) -> u64 {
    var s: u64 = 0u64
    var w: usize = 0usize
    for k in 0usize..n {
        s = s + a[w]
        w = (w + 1usize) & 15usize
    }
    return s
}
func ringinit(a: view[[8]u64], n: usize, h: usize) -> u64 {
    var s: u64 = 0u64
    var w: usize = h
    for k in 0usize..n {
        s = s + a[w]
        w = (w + 1usize) & 7usize
    }
    return s
}
func ringcopy(a: view[[8]u64], n: usize) -> u64 {
    var s: u64 = 0u64
    var w: usize = 0usize
    var v: usize = 0usize
    for k in 0usize..n {
        s = s + a[v]
        w = (w + 3usize) % 8usize
        v = w
    }
    return s
}
func join(a: view[[8]u64], x: usize, c: bool) -> u64 {
    var p: usize = 0usize
    if c { p = x & 3usize } else { p = x % 8usize }
    return a[p]
}
func joinwide(a: view[[8]u64], x: usize, c: bool) -> u64 {
    var p: usize = 0usize
    if c { p = x & 3usize } else { p = x }
    return a[p]
}
func edited(a: view[[8]u64], x: usize) -> u64 {
    var w: usize = x & 7usize
    bump(w)
    return a[w]
}
func lenmask(xs: view[]u64, i: usize) -> u64 {
    if len(xs) == 0usize { return 0u64 }
    m := len(xs) - 1usize
    return xs[i & m]
}
func guardmask(xs: view[]u64, i: usize, m: usize) -> u64 {
    if m < len(xs) { return xs[i & m] }
    return 0u64
}
func lenwhole(xs: view[]u64, i: usize) -> u64 { return xs[i & len(xs)] }
func lenoff(xs: view[]u64, i: usize) -> u64 {
    if len(xs) == 0usize { return 0u64 }
    return xs[(i & (len(xs) - 1usize)) + 1usize]
}
func byte(a: view[[256]u64], b: u8) -> u64 { return a[usize(b)] }
`

// TestProveBounds: remainders by a literal, bounds a local keeps across
// joins and loops, and masks by a value below the length are proven; a
// value's type range and a remainder by a length are not
// (specs/prove-bounds).
func TestProveBounds(t *testing.T) {
	mod := proveWith(t, boundsSource)
	want := map[string]string{
		"t.rem":       "constant",
		"t.rem9":      "runtime", // x % 9 can be 8
		"t.remoff":    "constant",
		"t.remmask":   "constant",
		"t.remconv":   "constant",
		"t.remlen":    "runtime", // a fourth-suite idiom, kept checked
		"t.ring":      "constant",
		"t.ringwide":  "runtime", // the bound 15 reaches past 8
		"t.ringinit":  "runtime", // the first value is unbounded
		"t.ringcopy":  "constant",
		"t.join":      "constant",
		"t.joinwide":  "runtime", // one path stores an unbounded value
		"t.edited":    "runtime", // the edit forgets the bound
		"t.lenmask":   "dominated_by_check",
		"t.guardmask": "dominated_by_check",
		"t.lenwhole":  "runtime", // x & len can be len
		"t.lenoff":    "runtime", // (x & (len - 1)) + 1 can be len
		"t.byte":      "runtime", // a u8's range is not a bound
	}
	for name, exp := range want {
		if got := checkLabels(mod, name); got != exp {
			t.Errorf("%s: checks %q, want %q", name, got, exp)
		}
	}
}

// TestProveBoundsFixtures pins the labels of the bounds functions of the
// two run fixtures, which the harness executes in both engines
// (specs/prove-bounds).
func TestProveBoundsFixtures(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", "tests", "run", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	proven := proveWith(t, read("prove_forms.kern"))
	for fn, want := range map[string]string{"rem_at": "constant", "ring_sum": "constant", "join_pick": "constant", "len_mask": "dominated_by_check", "guard_mask": "dominated_by_check"} {
		if got := checkLabels(proven, "run."+fn); got != want {
			t.Errorf("%s: %q, want %s", fn, got, want)
		}
	}
	not := proveWith(t, read("prove_forms_not.kern"))
	for _, fn := range []string{"rem_past", "rem_len", "ring_from", "join_wide", "len_whole"} {
		if got := checkLabels(not, "run."+fn); got != "runtime" {
			t.Errorf("%s: %q, want runtime", fn, got)
		}
	}
}

// TestProveBoundsShared: a chain of ands of a value with itself is
// searched once per value, not once per path (specs/prove-bounds).
func TestProveBoundsShared(t *testing.T) {
	var b strings.Builder
	b.WriteString("module t\nfunc chain(a: view[[8]u64], x0: usize) -> u64 {\n")
	for k := 1; k <= 60; k++ {
		fmt.Fprintf(&b, "    x%d := x%d & x%d\n", k, k-1, k-1)
	}
	b.WriteString("    return a[x60]\n}\n")
	start := time.Now()
	mod := proveWith(t, b.String())
	if got := checkLabels(mod, "t.chain"); got != "runtime" {
		t.Errorf("chain: %q, want runtime", got)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("the bound search took %v", d)
	}
}

// sliceOutcomes renders a function's slice checks in block order as the
// label and the recorded high-bound outcome, "runtime/high" for example.
func sliceOutcomes(mod *ir.Module, name string) string {
	fn := mod.Func(name)
	if fn == nil {
		return "<no function>"
	}
	var got []string
	for _, b := range fn.Blocks {
		for _, in := range b.Instrs {
			if cb, ok := in.(*ir.CheckBounds); ok && cb.Index == nil {
				label, high := "runtime", "nohigh"
				if cb.Prov.Label == "proven" {
					label = cb.Prov.ProvenBy
				}
				if cb.HighProven {
					high = "high"
				}
				if cb.HighNegative {
					high += "/neg"
				}
				got = append(got, label+"/"+high)
			}
		}
	}
	return strings.Join(got, " ")
}

const sliceHighSource = `module t
func count(xs: view[]u64) -> usize { return len(xs) }
func high_unproven(h: view[]u64, n: usize) -> usize { return count(h[0usize:n]) }
func low_unproven(a: view[]u64, s: usize) -> usize { return count(a[s:len(a)]) }
func proven(xs: view[]u64, i: usize) -> usize {
    if i < len(xs) { return count(xs[i:len(xs)]) }
    return 0usize
}
func both_unproven(a: view[]u64, s: usize, e: usize) -> usize { return count(a[s:e]) }
func main() -> i32 { return 0i32 }
`

// TestProveSliceHighProven: the proof pass records whether a slice
// check's high bound is proven, whatever its low bound
// (specs/unproven-reasons R2).
func TestProveSliceHighProven(t *testing.T) {
	mod := proveWith(t, sliceHighSource)
	for name, want := range map[string]string{
		"t.high_unproven": "runtime/nohigh",
		"t.low_unproven":  "runtime/high",
		"t.proven":        "dominated_by_check/high",
		"t.both_unproven": "runtime/nohigh",
	} {
		if got := sliceOutcomes(mod, name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
}

// TestProveSliceHighProvenInlined: a copy the inliner makes of a check
// the proof pass has already marked (library inlining runs after Prove in
// release execution) keeps the recorded high-bound outcome.
func TestProveSliceHighProvenInlined(t *testing.T) {
	mod := lowerSrc(t, `module t
func tail(a: view[]u64, s: usize) -> usize { with t := a[s:len(a)] { return len(t) } }
func head(h: view[]u64, n: usize) -> usize {
    if n < len(h) { with s := h[0usize:n + 1usize] { return len(s) } }
    return 0usize
}
func cut(h: view[]u64, n: usize) -> usize { with s := h[0usize:n] { return len(s) } }
func main() -> i32 {
    var a: [4]u64 = zero()
    x := tail(a, 1usize) + head(a, 2usize) + cut(a, 3usize)
    return i32(x)
}
`)
	Prove(mod)
	want := "runtime/high dominated_by_check/high runtime/nohigh"
	callees := sliceOutcomes(mod, "t.tail") + " " + sliceOutcomes(mod, "t.head") + " " + sliceOutcomes(mod, "t.cut")
	if callees != want {
		t.Fatalf("callees before inlining: %q, want %q", callees, want)
	}
	Inline(mod)
	validate(t, mod)
	if got := sliceOutcomes(mod, "t.main"); got != want {
		t.Errorf("main: %q, want %q (were tail, head and cut inlined?)\n%s", got, want, ir.Print(mod))
	}
}

const nonStrictSource = `module t
func idx(h: view[]u64, n: usize) -> u64 {
    if n <= len(h) { return h[n - 1usize] }
    return 0u64
}
func idx2(h: view[]u64, n: usize) -> u64 {
    if n <= len(h) { return h[n - 2usize] }
    return 0u64
}
func at(h: view[]u64, n: usize) -> u64 {
    if n <= len(h) { return h[n] }
    return 0u64
}
func sl(h: view[]u64, n: usize) -> usize {
    if n <= len(h) { with r := h[0usize:n] { return len(r) } }
    return 0usize
}
func slfrom(h: view[]u64, i: usize, n: usize) -> usize {
    if i <= n && n <= len(h) { with r := h[i:n] { return len(r) } }
    return 0usize
}
func low(a: view[]u64, s: usize, e: usize) -> usize {
    if s <= e && e <= len(a) { with r := a[s:e] { return len(r) } }
    return 0usize
}
func lowplus(a: view[]u64, s: usize, e: usize) -> usize {
    if s <= e && e + 1usize <= len(a) { with r := a[s:e + 1usize] { return len(r) } }
    return 0usize
}
func lowlen(a: view[]u64, s: usize) -> usize {
    if s <= len(a) { with r := a[s:len(a)] { return len(r) } }
    return 0usize
}
func lowlit(a: view[]u64, e: usize) -> usize {
    if 1usize <= e && e <= len(a) { with r := a[1usize:e] { return len(r) } }
    return 0usize
}
func lit2(h: view[]u64) -> usize {
    if 2usize <= len(h) { with r := h[0usize:2usize] { return len(r) } }
    return 0usize
}
func zero(h: view[]u64) -> usize { with r := h[0usize:0usize] { return len(r) } }
func arr(a: view[[4]u64]) -> usize { with r := a[0usize:2usize] { return len(r) } }
func last(xs: view[]u64) -> u64 {
    if len(xs) > 0usize { return xs[len(xs) - 1usize] }
    return 0u64
}
func pop(h: view[]u64) -> usize {
    if len(h) == 0usize { return 0usize }
    x := h[len(h) - 1usize]
    with r := h[0usize:len(h) - 1usize] { return len(r) + usize(x) }
}
func neg(h: view[]u64, n: usize) -> usize {
    if n <= len(h) { with r := h[0usize:n - 1usize] { return len(r) } }
    return 0usize
}
func lowonly(a: view[]u64, s: usize, e: usize) -> usize {
    if e <= len(a) { with r := a[s:e] { return len(r) } }
    return 0usize
}
func one(h: view[]u64) -> usize { with r := h[0usize:1usize] { return len(r) } }
func wrap(xs: view[]u64, x: usize) -> u64 {
    if x - 4611686018427387904usize - 4611686018427387904usize <= len(xs) { return xs[x] }
    return 0u64
}
func full(xs: view[]u64, i: usize) -> u64 {
    with r := xs[i:len(xs)] { if len(r) == 0usize { return 0u64 } }
    return xs[len(xs) - 1usize]
}
func main() -> i32 { return 0i32 }
`

// TestProveNonStrict: G1 to G4 of specs/prove-nonstrict prove their forms
// with their labels, and the non-goals of its §0 stay runtime-checked.
func TestProveNonStrict(t *testing.T) {
	mod := proveWith(t, nonStrictSource)
	for name, want := range map[string]string{
		"t.idx":     "dominated_by_check",
		"t.idx2":    "dominated_by_check",
		"t.at":      "runtime",
		"t.sl":      "dominated_by_check",
		"t.slfrom":  "dominated_by_check",
		"t.low":     "dominated_by_check",
		"t.lowplus": "dominated_by_check",
		"t.lowlen":  "dominated_by_check",
		"t.lowlit":  "dominated_by_check",
		"t.lit2":    "dominated_by_check", // proven before this change: label kept
		"t.zero":    "constant",
		"t.arr":     "constant", // proven before this change: label kept
		"t.last":    "runtime",
		"t.pop":     "runtime runtime",
		"t.neg":     "runtime",
		"t.lowonly": "runtime",
		"t.one":     "runtime",
		"t.full":    "runtime runtime", // no fact past a passed slice check
		"t.wrap":    "runtime",         // the guard's offset is MinInt64: off - 1 would wrap
	} {
		if got := checkLabels(mod, name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	for name, want := range map[string]string{
		"t.sl":      "dominated_by_check/high",
		"t.neg":     "runtime/nohigh/neg",
		"t.pop":     "runtime/nohigh/neg",
		"t.lowonly": "runtime/high",
		"t.one":     "runtime/nohigh",
	} {
		if got := sliceOutcomes(mod, name); got != want {
			t.Errorf("%s slice: %q, want %q", name, got, want)
		}
	}
}

// TestProveNonStrictEdges: every edge that establishes a non-strict
// relation (specs/prove-nonstrict G1) covers the index one below its
// left side and not the left side itself; a signed comparison covers
// nothing.
func TestProveNonStrictEdges(t *testing.T) {
	src := `module t
func le(h: view[]u64, n: usize) -> u64 {
    if n <= len(h) { return h[n - 1usize] + h[n] }
    return 0u64
}
func ge(h: view[]u64, n: usize) -> u64 {
    if len(h) >= n { return h[n - 1usize] + h[n] }
    return 0u64
}
func gt(h: view[]u64, n: usize) -> u64 {
    if n > len(h) { return 0u64 }
    return h[n - 1usize] + h[n]
}
func lt(h: view[]u64, n: usize) -> u64 {
    if len(h) < n { return 0u64 }
    return h[n - 1usize] + h[n]
}
func signed(h: view[]u64, i: i64) -> u64 {
    if i <= i64(len(h)) { return h[usize(i) - 1usize] }
    return 0u64
}
func main() -> i32 { return 0i32 }
`
	mod := proveWith(t, src)
	for _, name := range []string{"le", "ge", "gt", "lt"} {
		if got := checkLabels(mod, "t."+name); got != "dominated_by_check runtime" {
			t.Errorf("%s: %q, want %q", name, got, "dominated_by_check runtime")
		}
	}
	if got := checkLabels(mod, "t.signed"); got != "runtime" {
		t.Errorf("signed: %q, want runtime", got)
	}
}

// TestProveNonStrictInlined: a release-inlined copy of a marked slice
// check keeps HighNegative, as it keeps HighProven.
func TestProveNonStrictInlined(t *testing.T) {
	mod := lowerSrc(t, `module t
func neg(h: view[]u64, n: usize) -> usize {
    if n <= len(h) { with r := h[0usize:n - 1usize] { return len(r) } }
    return 0usize
}
func main() -> i32 {
    var a: [4]u64 = zero()
    return i32(neg(a, 2usize))
}
`)
	Prove(mod)
	if got := sliceOutcomes(mod, "t.neg"); got != "runtime/nohigh/neg" {
		t.Fatalf("callee before inlining: %q", got)
	}
	Inline(mod)
	validate(t, mod)
	if got := sliceOutcomes(mod, "t.main"); got != "runtime/nohigh/neg" {
		t.Errorf("main: %q, want runtime/nohigh/neg (was neg inlined?)\n%s", got, ir.Print(mod))
	}
}

// TestProveNonStrictFixtures pins the specs/prove-nonstrict functions of
// the two fixture files at both levels (R4).
func TestProveNonStrictFixtures(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", "tests", "run", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	proven := map[string]string{
		"ns_index": "dominated_by_check", "ns_index_gt": "dominated_by_check",
		"ns_slice": "dominated_by_check", "ns_low": "dominated_by_check",
		"ns_low_plus": "dominated_by_check", "ns_low_len": "dominated_by_check",
		"ns_zero": "constant",
	}
	not := map[string]string{
		"nsn_last": "runtime", "nsn_pop": "runtime runtime", "nsn_neg": "runtime",
		"nsn_at": "runtime", "nsn_low": "runtime", "nsn_one": "runtime",
	}
	for _, level := range []string{"debug", "release"} {
		for file, want := range map[string]map[string]string{"prove_forms.kern": proven, "prove_forms_not.kern": not} {
			src := read(file)
			mod := runWith(t, src, OptionsFor(level))
			for fn, w := range want {
				if got := labelsWithin(mod, src, fn); got != w {
					t.Errorf("%s %s %s: %q, want %q", level, file, fn, got, w)
				}
			}
		}
	}
}

// labelsWithin renders the bounds checks whose source position lies in
// function fn's declaration, as show-bounds-checks reports them: over the
// functions that are emitted (not Inlined), one entry per position in
// source order, runtime when any copy is (release inlining moves a
// callee's checks into its callers, at the callee's positions).
func labelsWithin(mod *ir.Module, src, fn string) string {
	start := strings.Index(src, "\nfunc "+fn+"(")
	if start < 0 {
		return "<no function>"
	}
	end := strings.Index(src[start+1:], "\nfunc ")
	if end < 0 {
		end = len(src)
	} else {
		end += start + 1
	}
	labels := map[int]string{}
	for _, f := range mod.Funcs {
		if f.Inlined {
			continue
		}
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				cb, ok := in.(*ir.CheckBounds)
				if !ok || cb.Pos.Start < start || cb.Pos.Start >= end {
					continue
				}
				l := "runtime"
				if cb.Prov.Label == "proven" {
					l = cb.Prov.ProvenBy
				}
				if _, seen := labels[cb.Pos.Start]; !seen || l == "runtime" {
					labels[cb.Pos.Start] = l
				}
			}
		}
	}
	pos := make([]int, 0, len(labels))
	for p := range labels {
		pos = append(pos, p)
	}
	sort.Ints(pos)
	got := make([]string, len(pos))
	for i, p := range pos {
		got[i] = labels[p]
	}
	return strings.Join(got, " ")
}

// sub reports the int64 overflows the proof pass's offset arithmetic must
// not wrap through (specs/prove-nonstrict G1, G3).
func TestSub(t *testing.T) {
	for _, tc := range []struct {
		a, b, want int64
		ok         bool
	}{
		{5, 1, 4, true},
		{-5, 1, -6, true},
		{math.MinInt64, 1, 0, false},
		{math.MinInt64 + 1, 1, math.MinInt64, true},
		{math.MaxInt64, -1, 0, false},
		{0, math.MinInt64, 0, false},
		{-1, math.MinInt64, math.MaxInt64, true},
	} {
		got, ok := sub(tc.a, tc.b)
		if ok != tc.ok || ok && got != tc.want {
			t.Errorf("sub(%d, %d) = %d, %v; want %d, %v", tc.a, tc.b, got, ok, tc.want, tc.ok)
		}
	}
}

// TestProveParenFixtures: guards inside a parenthesized condition prove
// their checks, at both levels (specs/paren-conditions R2).
func TestProveParenFixtures(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "tests", "run", "prove_forms.kern"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, level := range []string{"debug", "release"} {
		mod := runWith(t, src, OptionsFor(level))
		for _, fn := range []string{"paren_not", "paren_and", "paren_mixed"} {
			if got := labelsWithin(mod, src, fn); got != "dominated_by_check" {
				t.Errorf("%s %s: %q, want dominated_by_check", level, fn, got)
			}
		}
	}
}
