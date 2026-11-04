package interp

import (
	"testing"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/resolve"
)

// newStruct and newUnion give exactly n field slots (length and capacity),
// all nil, for every size across the inline limits (specs/interp-alloc R5).
func TestNewAggregateShape(t *testing.T) {
	for n := 0; n <= 6; n++ {
		s := newStruct(n)
		u := newUnion(3, n)
		if len(s.Fields) != n || cap(s.Fields) != n {
			t.Errorf("newStruct(%d): len %d cap %d", n, len(s.Fields), cap(s.Fields))
		}
		if len(u.Fields) != n || cap(u.Fields) != n || u.Tag != 3 {
			t.Errorf("newUnion(3, %d): tag %d len %d cap %d", n, u.Tag, len(u.Fields), cap(u.Fields))
		}
		for i := 0; i < n; i++ {
			if s.Fields[i] != nil || u.Fields[i] != nil {
				t.Errorf("size %d: slot %d not nil", n, i)
			}
		}
	}
}

// A small aggregate is one allocation, a larger one two.
func TestNewAggregateAllocs(t *testing.T) {
	var sink any
	for _, c := range []struct{ n, want int }{{1, 1}, {2, 1}, {3, 1}, {4, 1}, {5, 2}} {
		got := testing.AllocsPerRun(100, func() { sink = newStruct(c.n) })
		if int(got) != c.want {
			t.Errorf("newStruct(%d): %v allocations, want %d", c.n, got, c.want)
		}
		got = testing.AllocsPerRun(100, func() { sink = newUnion(1, c.n) })
		if int(got) != c.want {
			t.Errorf("newUnion(1, %d): %v allocations, want %d", c.n, got, c.want)
		}
	}
	_ = sink
}

// A copy shares no field slot with its source, at every size, nested
// aggregates included; a slot reference taken before later writes keeps
// naming the same slot.
func TestCopyValueIndependent(t *testing.T) {
	for n := 1; n <= 6; n++ {
		src := newStruct(n)
		for i := range src.Fields {
			src.Fields[i] = mkInt(tyU64, uint64(i))
		}
		// the last slot holds a nested union
		inner := newUnion(1, 2)
		inner.Fields[0], inner.Fields[1] = mkInt(tyU64, 7), mkInt(tyU64, 8)
		src.Fields[n-1] = inner
		cp := copyValue(src).(*Struct)
		ref := slotRef{&cp.Fields[0]}
		cp.Fields[n-1].(*Union).Fields[1] = mkInt(tyU64, 100)
		if src.Fields[n-1].(*Union).Fields[1].(Int).Bits != 8 {
			t.Errorf("size %d: the nested union is shared", n)
		}
		cp.Fields[0] = mkInt(tyU64, 99)
		if n > 1 && src.Fields[0].(Int).Bits != 0 {
			t.Errorf("size %d: writing the copy changed the source", n)
		}
		if ref.get().(Int).Bits != 99 {
			t.Errorf("size %d: the slot reference moved", n)
		}
	}
}

// zeroOf builds a struct's zero value through newStruct: one slot per
// field, each the field's zero.
func TestZeroOfStructSlots(t *testing.T) {
	u64 := &resolve.Prim{Name: "u64"}
	decl := &resolve.TypeDecl{Kind: resolve.KindStruct, Fields: []*resolve.FieldDecl{{Name: "a", Type: u64}, {Name: "b", Type: u64}, {Name: "c", Type: u64}}}
	s, ok := zeroOf(&resolve.Named{Decl: decl}).(*Struct)
	if !ok || len(s.Fields) != 3 || cap(s.Fields) != 3 {
		t.Fatalf("zeroOf: %#v", s)
	}
	for i, f := range s.Fields {
		if v, ok := f.(Int); !ok || v.Bits != 0 {
			t.Errorf("field %d: %#v", i, f)
		}
	}
}

// putInt and getInt round-trip every size from 1 to 8 bytes, little-endian.
func TestIntBytes(t *testing.T) {
	const v = 0x0102030405060708
	for size := 1; size <= 8; size++ {
		b := make([]byte, 8)
		putInt(b, v, size)
		want := uint64(v) & (^uint64(0) >> (64 - 8*size))
		if got := getInt(b, size); got != want {
			t.Errorf("size %d: got %#x, want %#x", size, got, want)
		}
		if b[0] != 0x08 {
			t.Errorf("size %d: first byte %#x, not little-endian", size, b[0])
		}
		for i := size; i < 8; i++ {
			if b[i] != 0 {
				t.Errorf("size %d: byte %d written", size, i)
			}
		}
	}
}

// spanAt names the caller's span inside the library and the call site
// outside it, without copying either.
func TestSpanAtStable(t *testing.T) {
	site := &ast.Pos{Start: 3, End: 9}
	user := &frame{}
	if user.spanAt(site) != site {
		t.Error("outside the library: not the call site")
	}
	entered := &ast.Pos{Start: 1, End: 2}
	lib := &frame{span: entered}
	if lib.spanAt(site) != entered {
		t.Error("inside the library: not the caller's span")
	}
	if lib.at(ast.Pos{Start: 5, End: 6}) != *entered {
		t.Error("at: not the caller's span")
	}
}

// Every fast intrinsic is also an ordinary intrinsic, and the two give the
// same result.
func TestFastIntrinsicsRegistered(t *testing.T) {
	it := &Interp{}
	for name, f := range fastIntrinsics {
		slow, ok := intrinsics[name]
		if !ok {
			t.Errorf("%s: not registered", name)
			continue
		}
		switch name {
		case "ptr_null", "ptr_addr", "ptr_cast", "ptr_from_addr":
			a := []Value{mkInt(tyUsize, 4096)}
			fv := f(it, a, callCtx{})
			sv := slow(it, a, callCtx{})
			if len(sv) != 1 || sv[0] != fv {
				t.Errorf("%s: fast %v, slow %v", name, fv, sv)
			}
		}
	}
	if len(fastIntrinsics) != 12 {
		t.Errorf("%d fast intrinsics, want 12", len(fastIntrinsics))
	}
}
