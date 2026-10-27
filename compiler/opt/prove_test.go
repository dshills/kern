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
