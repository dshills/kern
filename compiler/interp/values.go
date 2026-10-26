// Package interp executes the semantic IR directly (PLAN.md 1.2): the
// behavioral oracle for spec §4.1 execution semantics, §5.7 destruction and
// §6 traps, with the standard library's types provided as Go intrinsics.
package interp

import (
	"fmt"

	"github.com/dshills/kern/compiler/resolve"
)

// Value is a runtime value. Primitives are immutable Go values; aggregates
// are pointers so that places into them can be written through refs.
type Value any

// Int is an integer value of a named width; bits holds the two's complement
// pattern masked to the width.
type Int struct {
	Bits uint64
	Ty   string
}

// Float is f32 or f64.
type Float struct {
	V  float64
	Ty string
}

// Bool is bool.
type Bool bool

// Unit is ().
type Unit struct{}

// Struct is a struct value.
type Struct struct{ Fields []Value }

// Union is a tagged union value.
type Union struct {
	Tag    int
	Fields []Value
}

// Array is a fixed array.
type Array struct{ Elems []Value }

// FuncVal is a function value.
type FuncVal struct{ Name string }

// Access is an open access: a ref to a place (scalar) or a sequence window.
type Access struct {
	Ref  ref
	Seq  *seqRef // non-nil for sequence accesses
	Edit bool
}

// ref is an lvalue: a readable and writable slot.
type ref interface {
	get() Value
	set(Value)
}

// slotRef points at a Go slot.
type slotRef struct{ p *Value }

func (r slotRef) get() Value  { return *r.p }
func (r slotRef) set(v Value) { *r.p = v }

// idxRef points at an element of a Go slice through its owner, so that
// reallocation by the owner stays visible.
type idxRef struct {
	elems func() []Value
	i     int
}

func (r idxRef) get() Value  { return r.elems()[r.i] }
func (r idxRef) set(v Value) { r.elems()[r.i] = v }

// seqRef is a window [lo, hi) over a sequence of slots.
type seqRef struct {
	elems  func() []Value
	lo, hi int
}

func (s *seqRef) length() int  { return s.hi - s.lo }
func (s *seqRef) at(i int) ref { return idxRef{elems: s.elems, i: s.lo + i} }

// ---- library objects ----

// Box owns one heap value.
type Box struct {
	Val  Value
	Dead bool
}

// Vec is a growable vector.
type Vec struct {
	Items []Value
	Elem  resolve.Type
	Dead  bool
}

// PoolSlot is one Pool slot.
type PoolSlot struct {
	Val     Value
	Gen     uint32
	Live    bool
	Retired bool
}

// Pool is a slot pool with generation counters (spec §5.6).
type Pool struct {
	Slots    []*PoolSlot
	Free     []int
	Identity uint32
	Elem     resolve.Type
	Dead     bool
}

// Arena is an append-only slot container.
type Arena struct {
	Slots    []Value
	Identity uint32
	Dead     bool
}

// Shared is an atomically reference-counted immutable value.
type Shared struct {
	Val   Value
	Count uint64
}

// Mutex guards a value.
type Mutex struct {
	Val    Value
	Locked bool
}

// Atomic holds an atomic primitive.
type Atomic struct{ Val Value }

// Scope is a thread scope.
type Scope struct{ Joins []*Join }

// Thread is a spawned thread (Phase 1: run eagerly).
type Thread struct {
	Result Value
	Done   bool
}

// Join is a scoped thread handle.
type Join struct {
	Result Value
	Done   bool
	Taken  bool
}

// SysAlloc is the hosted allocator stand-in.
type SysAlloc struct{}

// ---- construction helpers ----

func mkInt(ty string, v uint64) Int { return Int{Bits: mask(ty, v), Ty: ty} }

// mkFloat builds a float, rounding f32 values to single precision.
func mkFloat(ty string, v float64) Float {
	if ty == "f32" {
		v = float64(float32(v))
	}
	return Float{V: v, Ty: ty}
}

func width(ty string) uint {
	switch ty {
	case "i8", "u8":
		return 8
	case "i16", "u16":
		return 16
	case "i32", "u32":
		return 32
	}
	return 64
}

func signed(ty string) bool { return ty[0] == 'i' }

func mask(ty string, v uint64) uint64 {
	w := width(ty)
	if w == 64 {
		return v
	}
	return v & (1<<w - 1)
}

// signedOf interprets an Int's bits as a signed value.
func signedOf(i Int) int64 {
	w := width(i.Ty)
	if w == 64 {
		return int64(i.Bits)
	}
	v := int64(i.Bits)
	if i.Bits&(1<<(w-1)) != 0 {
		v -= 1 << w
	}
	return v
}

// zeroOf builds the all-zero value of a type.
func zeroOf(t resolve.Type) Value {
	switch x := t.(type) {
	case *resolve.Prim:
		switch x.Name {
		case "bool":
			return Bool(false)
		case "f32", "f64":
			return Float{Ty: x.Name}
		}
		return Int{Ty: x.Name}
	case *resolve.Unit:
		return Unit{}
	case *resolve.RawPtr:
		return Int{Ty: "usize"}
	case *resolve.Func:
		return FuncVal{}
	case *resolve.Array:
		a := &Array{Elems: make([]Value, x.Len)}
		for i := range a.Elems {
			a.Elems[i] = zeroOf(x.Elem)
		}
		return a
	case *resolve.Named:
		args := typeArgs(x)
		switch x.Decl.Kind {
		case resolve.KindStruct:
			s := &Struct{}
			for _, f := range x.Decl.Fields {
				s.Fields = append(s.Fields, zeroOf(resolve.Subst(f.Type, args)))
			}
			return s
		case resolve.KindUnion:
			u := &Union{Tag: 0}
			if len(x.Decl.Variants) > 0 {
				for _, f := range x.Decl.Variants[0].Fields {
					u.Fields = append(u.Fields, zeroOf(resolve.Subst(f.Type, args)))
				}
			}
			return u
		}
	}
	return Unit{}
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

// copyValue duplicates a Copy value (aggregates are cloned so that value
// semantics hold); move-only values are returned as is (moved).
func copyValue(v Value) Value {
	switch x := v.(type) {
	case *Struct:
		n := &Struct{Fields: make([]Value, len(x.Fields))}
		for i, f := range x.Fields {
			n.Fields[i] = copyValue(f)
		}
		return n
	case *Union:
		n := &Union{Tag: x.Tag, Fields: make([]Value, len(x.Fields))}
		for i, f := range x.Fields {
			n.Fields[i] = copyValue(f)
		}
		return n
	case *Array:
		n := &Array{Elems: make([]Value, len(x.Elems))}
		for i, e := range x.Elems {
			n.Elems[i] = copyValue(e)
		}
		return n
	}
	return v
}

func fmtValue(v Value) string {
	switch x := v.(type) {
	case Int:
		if signed(x.Ty) {
			return fmt.Sprintf("%d", signedOf(x))
		}
		return fmt.Sprintf("%d", x.Bits)
	case Float:
		return fmt.Sprintf("%g", x.V)
	case Bool:
		if x {
			return "true"
		}
		return "false"
	case Unit:
		return "()"
	}
	return fmt.Sprintf("%T", v)
}
