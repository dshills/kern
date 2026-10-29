package opt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	Prove(mod)
	if errs := ir.Validate(mod); len(errs) > 0 {
		t.Fatalf("validator: %v", errs[0])
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
