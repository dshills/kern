package interp

import (
	"testing"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/resolve"
)

// TestRawHeapRoundTrip encodes and decodes scalars, arrays and tuples in the
// §4.1 layout through the byte heap and checks allocation bookkeeping.
func TestRawHeapRoundTrip(t *testing.T) {
	it := New(&ir.Module{}, "t.kern", nil, nil)
	addr := it.heap.alloc(64, 8)
	if addr%8 != 0 || addr < heapBase {
		t.Fatalf("bad address %#x", addr)
	}
	u64 := &resolve.Prim{Name: "u64"}
	tup := &resolve.Tuple{Elems: []resolve.Type{&resolve.Prim{Name: "u8"}, u64, &resolve.Prim{Name: "f32"}, &resolve.Prim{Name: "bool"}}}
	val := &Struct{Fields: []Value{mkInt(tyU8, 7), mkInt(tyU64, 1<<40|5), mkFloat("f32", 1.5), Bool(true)}}
	b := it.rawAt(addr, tup, ast.Pos{})
	it.encode(b, tup, val)
	got := it.decode(b, tup).(*Struct)
	if got.Fields[0].(Int).Bits != 7 || got.Fields[1].(Int).Bits != 1<<40|5 || got.Fields[2].(Float).V != 1.5 || !bool(got.Fields[3].(Bool)) {
		t.Fatalf("tuple round trip: %+v", got.Fields)
	}
	arrT := &resolve.Array{Elem: &resolve.Prim{Name: "i16"}, Len: 3}
	arr := &Array{Elems: []Value{mkInt(tyI16, 0xFFFF), mkInt(tyI16, 2), mkInt(tyI16, 3)}}
	ab := it.rawAt(addr+24, arrT, ast.Pos{})
	it.encode(ab, arrT, arr)
	back := it.decode(ab, arrT).(*Array)
	if signedOf(back.Elems[0].(Int)) != -1 || back.Elems[2].(Int).Bits != 3 {
		t.Fatalf("array round trip: %+v", back.Elems)
	}
	if !it.heap.free(addr) || it.heap.free(addr) {
		t.Fatalf("free must succeed once")
	}
	if it.heap.bytes(heapBase+8<<20, 8) != nil {
		t.Fatalf("unmapped range must not be addressable")
	}
	if it.heap.alloc(1<<50, 8) != 0 {
		t.Fatalf("an unrepresentable request must fail")
	}
	big := it.heap.alloc(3<<20, 16)
	if big == 0 || it.heap.bytes(big, 3<<20) == nil || it.heap.bytes(addr+24, 6) == nil {
		t.Fatalf("a large allocation gets its own chunk and earlier chunks stay mapped")
	}
	zero := it.heap.alloc(0, 1)
	if zero == addr || zero == 0 {
		t.Fatalf("zero-size allocations are unique and non-null")
	}
}
