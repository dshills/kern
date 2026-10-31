// Package layout computes type sizes, alignments and field offsets per spec
// §4.1 for a 64-bit target: struct fields in declaration order with C ABI
// alignment and padding; unions as a u8/u16 discriminant at offset 0 followed
// by the largest payload at the payloads' maximum alignment; arrays as N
// contiguous elements; scalar accesses as one pointer and sequence accesses
// as a (pointer, length) pair. The runtime-provided library objects
// (Shared, Mutex, Atomic, threads, the system allocator) are pointer-sized
// opaque handles; Box, Vec, Pool and Arena are kern structs laid out like
// any other.
package layout

import (
	"sync"

	"github.com/dshills/kern/compiler/resolve"
)

// PtrSize is the pointer size of every supported target (x86-64, AArch64).
const PtrSize = 8

// MaxSize bounds every layout: a size that would exceed it saturates to
// MaxSize and the type is reported too large by the backend.
const MaxSize = 1 << 40

// memo caches named-type layouts by declaration identity and instantiation;
// resolved types are immutable, so the cache is global.
var memo sync.Map

type memoKey struct {
	decl *resolve.TypeDecl
	args string
}

// mulSat multiplies with saturation at MaxSize.
func mulSat(a, b int) int {
	if a == 0 || b == 0 {
		return 0
	}
	if a > MaxSize/b {
		return MaxSize
	}
	return a * b
}

// addSat adds with saturation at MaxSize.
func addSat(a, b int) int {
	if a > MaxSize-b {
		return MaxSize
	}
	return a + b
}

// Info is the layout of a type.
type Info struct {
	Size  int
	Align int
	// Fields are the struct field offsets, or nil.
	Fields []int
	// Payload is the union payload offset (after the discriminant), or 0.
	Payload int
	// TagSize is the union discriminant size (1 or 2), or 0.
	TagSize int
}

// Of computes the layout of t.
func Of(t resolve.Type) Info {
	switch x := t.(type) {
	case *resolve.Prim:
		switch x.Name {
		case "i8", "u8", "bool":
			return Info{Size: 1, Align: 1}
		case "i16", "u16":
			return Info{Size: 2, Align: 2}
		case "i32", "u32", "f32":
			return Info{Size: 4, Align: 4}
		}
		return Info{Size: 8, Align: 8}
	case *resolve.Unit:
		return Info{Size: 0, Align: 1}
	case *resolve.Func:
		if x.Closure {
			return Info{Size: 2 * PtrSize, Align: PtrSize} // function and environment
		}
		return Info{Size: PtrSize, Align: PtrSize}
	case *resolve.RawPtr:
		return Info{Size: PtrSize, Align: PtrSize}
	case *resolve.Access:
		if x.Seq {
			return Info{Size: 2 * PtrSize, Align: PtrSize}
		}
		return Info{Size: PtrSize, Align: PtrSize}
	case *resolve.Slice:
		return Info{Size: 2 * PtrSize, Align: PtrSize}
	case *resolve.Array:
		e := Of(x.Elem)
		return Info{Size: mulSat(e.Size, x.Len), Align: e.Align}
	case *resolve.Named:
		key := memoKey{decl: x.Decl, args: x.String()}
		if v, ok := memo.Load(key); ok {
			return v.(Info)
		}
		li := named(x)
		memo.Store(key, li)
		return li
	case *resolve.Tuple:
		// a tuple of accesses is returned in registers; its in-memory form is a struct of its elements
		var fields []resolve.Type
		fields = append(fields, x.Elems...)
		return structOf(fields)
	}
	return Info{Size: 0, Align: 1}
}

// Opaque reports whether a named type is a runtime-provided container whose
// in-memory representation is an opaque pointer-sized handle. Atomic[T] is
// not one: it is a struct of its cell, laid out as T, so that every atomic
// operation is an instruction on the cell itself.
func Opaque(n *resolve.Named) bool {
	if n.Decl.Module != "core" {
		return false
	}
	switch n.Decl.Name {
	case "Shared", "Mutex", "Condvar", "Scope", "Thread", "Join", "SystemAllocator", "thread", "trap", "testing":
		return true
	}
	return false
}

func named(n *resolve.Named) Info {
	if Opaque(n) {
		return Info{Size: PtrSize, Align: PtrSize}
	}
	args := typeArgs(n)
	switch n.Decl.Kind {
	case resolve.KindStruct:
		var fields []resolve.Type
		for _, f := range n.Decl.Fields {
			fields = append(fields, resolve.Subst(f.Type, args))
		}
		if n.Decl.Packed {
			return packedOf(fields)
		}
		return structOf(fields)
	case resolve.KindUnion:
		tagSize := 1
		if len(n.Decl.Variants) > 256 {
			tagSize = 2
		}
		payAlign, paySize := 1, 0
		for _, v := range n.Decl.Variants {
			var fields []resolve.Type
			for _, f := range v.Fields {
				fields = append(fields, resolve.Subst(f.Type, args))
			}
			vi := structOf(fields)
			if vi.Align > payAlign {
				payAlign = vi.Align
			}
			if vi.Size > paySize {
				paySize = vi.Size
			}
		}
		payload := alignUp(tagSize, payAlign)
		align := tagSize
		if payAlign > align {
			align = payAlign
		}
		return Info{Size: alignUp(addSat(payload, paySize), align), Align: align, Payload: payload, TagSize: tagSize}
	}
	return Info{Size: 0, Align: 1}
}

func structOf(fields []resolve.Type) Info {
	off, align := 0, 1
	var offs []int
	for _, f := range fields {
		fi := Of(f)
		off = alignUp(off, fi.Align)
		offs = append(offs, off)
		off = addSat(off, fi.Size)
		if fi.Align > align {
			align = fi.Align
		}
	}
	return Info{Size: alignUp(off, align), Align: align, Fields: offs}
}

// packedOf lays fields out without padding (a `packed` struct): alignment 1.
func packedOf(fields []resolve.Type) Info {
	off := 0
	var offs []int
	for _, f := range fields {
		offs = append(offs, off)
		off = addSat(off, Of(f).Size)
	}
	return Info{Size: off, Align: 1, Fields: offs}
}

// VariantLayout returns the field offsets of a union variant's payload,
// relative to the union's start. The slice is shared: callers only read it.
func VariantLayout(n *resolve.Named, v *resolve.VariantDecl) []int {
	key := variantKey{memoKey{decl: n.Decl, args: n.String()}, v}
	if offs, ok := variantMemo.Load(key); ok {
		return offs.([]int)
	}
	args := typeArgs(n)
	var fields []resolve.Type
	for _, f := range v.Fields {
		fields = append(fields, resolve.Subst(f.Type, args))
	}
	vi := structOf(fields)
	base := Of(n).Payload
	out := make([]int, len(vi.Fields))
	for i, o := range vi.Fields {
		out[i] = base + o
	}
	variantMemo.Store(key, out)
	return out
}

// variantMemo caches VariantLayout by the union's identity and
// instantiation (as memo does) and the variant: the interpreter asks for a
// variant's offsets at every read of a payload.
var variantMemo sync.Map

type variantKey struct {
	memoKey
	v *resolve.VariantDecl
}

func alignUp(n, a int) int {
	if a <= 1 {
		return n
	}
	return (n + a - 1) / a * a
}

func typeArgs(n *resolve.Named) map[string]resolve.Type {
	m := map[string]resolve.Type{}
	for i, tp := range n.Decl.TypeParams {
		if i < len(n.Args) {
			m[tp.Name] = n.Args[i]
		}
	}
	return m
}
