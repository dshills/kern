package interp

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/resolve"
)

// TestUnreachableTrap drives a hand-built IR module whose switch is entered
// with an out-of-range discriminant: the default block traps with kind
// unreachable (PLAN.md 1.4; the source-level fixture arrives in Phase 2).
func TestUnreachableTrap(t *testing.T) {
	u32 := &resolve.Prim{Name: "u32"}
	f := &ir.Func{Name: "m.main", Rets: []resolve.Type{&resolve.Prim{Name: "i32"}}, Pos: ast.Pos{Start: 1, End: 2}}
	entry := &ir.Block{ID: 0, Name: "entry"}
	dflt := &ir.Block{ID: 1, Name: "unreachable"}
	ret := &ir.Block{ID: 2, Name: "ret"}
	tag := &ir.Value{ID: 0, Type: u32}
	entry.Instrs = []ir.Instr{&ir.Const{Dst: tag, Int: 7}}
	entry.Term = &ir.Switch{Tag: tag, Cases: map[int]*ir.Block{0: ret}, Default: dflt}
	dflt.Instrs = []ir.Instr{&ir.Trap{Kind: "unreachable", Pos: ast.Pos{Start: 3, End: 4}}}
	dflt.Term = &ir.Unreachable{}
	zero := &ir.Value{ID: 1, Type: &resolve.Prim{Name: "i32"}}
	ret.Instrs = []ir.Instr{&ir.Const{Dst: zero}}
	ret.Term = &ir.Ret{Vals: []*ir.Value{zero}}
	f.Blocks = []*ir.Block{entry, dflt, ret}
	f.Entry = entry
	f.NumValues = 2
	mod := &ir.Module{Name: "m", Entry: "m.main"}
	mod.Add(f)
	if errs := ir.Validate(mod); len(errs) > 0 {
		t.Fatalf("validate: %v", errs)
	}
	var out, errb bytes.Buffer
	it := New(mod, "hand.kern", &out, &errb)
	if status := it.Run(); status != 134 {
		t.Fatalf("status %d", status)
	}
	if !strings.Contains(errb.String(), `"kind":"unreachable"`) {
		t.Fatalf("record: %s", errb.String())
	}
	// the twin: in-range discriminant completes normally
	entry.Instrs = []ir.Instr{&ir.Const{Dst: tag, Int: 0}}
	it = New(mod, "hand.kern", &out, &errb)
	if status := it.Run(); status != 0 {
		t.Fatalf("twin status %d", status)
	}
}

func TestArithmeticSemantics(t *testing.T) {
	it := New(&ir.Module{}, "x", &bytes.Buffer{}, &bytes.Buffer{})
	// signed division truncates toward zero and remainder takes the dividend's sign (spec §4.1)
	q := it.binary("/", mkInt(tyI32, sint(-7)), mkInt(tyI32, 2), "")
	r := it.binary("%", mkInt(tyI32, sint(-7)), mkInt(tyI32, 2), "")
	if signedOf(q.(Int)) != -3 || signedOf(r.(Int)) != -1 {
		t.Fatalf("div/rem: %d %d", signedOf(q.(Int)), signedOf(r.(Int)))
	}
	// arithmetic right shift for signed, logical for unsigned
	s := it.binary(">>", mkInt(tyI8, sint(-128)), mkInt(tyU32, 1), "")
	if signedOf(s.(Int)) != -64 {
		t.Fatalf("signed shr: %d", signedOf(s.(Int)))
	}
	u := it.binary(">>", mkInt(tyU8, 128), mkInt(tyU32, 1), "")
	if u.(Int).Bits != 64 {
		t.Fatalf("unsigned shr: %d", u.(Int).Bits)
	}
	// truncate keeps the low bits in the target's signedness
	tr := it.conv(mkInt(tyI16, sint(-1)), "u8", true, false)
	if tr.(Int).Bits != 255 {
		t.Fatalf("truncate: %d", tr.(Int).Bits)
	}
	// saturating float conversion
	sat := it.conv(Float{V: 1e20, Ty: "f64"}, "i32", false, true)
	if signedOf(sat.(Int)) != 2147483647 {
		t.Fatalf("saturate: %d", signedOf(sat.(Int)))
	}
	trapped := func(fn func()) (kind string) {
		defer func() {
			if r := recover(); r != nil {
				kind = r.(*Trap).Kind
			}
		}()
		fn()
		return ""
	}
	if k := trapped(func() { it.checkOverflow("*", mkInt(tyI64, 1<<62), mkInt(tyI64, 4), ast.Pos{}) }); k != "overflow" {
		t.Fatalf("mul overflow: %q", k)
	}
	if k := trapped(func() { it.checkOverflow("/", mkInt(tyI8, 128), mkInt(tyI8, 255), ast.Pos{}) }); k != "overflow" {
		t.Fatalf("min/-1: %q", k)
	}
	if k := trapped(func() { it.checkOverflow("<<", mkInt(tyU8, 0x81), mkInt(tyU32, 1), ast.Pos{}) }); k != "overflow" {
		t.Fatalf("shift-out: %q", k)
	}
	if k := trapped(func() { it.checkConv(mkInt(tyI8, sint(-1)), "u16", ast.Pos{}) }); k != "conversion" {
		t.Fatalf("u16(i8(-1)): %q", k)
	}
}

// sint is the two's complement bit pattern of a negative constant.
func sint(v int64) uint64 { return uint64(v) }
