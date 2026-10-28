// Package interp executes the semantic IR directly (PLAN.md 1.2): the
// behavioral oracle for spec §4.1 execution semantics, §5.7 destruction and
// §6 traps. The runtime-provided library objects (Shared, Mutex, threads)
// are Go intrinsics; an Atomic is a struct of its cell updated under a lock;
// the containers are the prelude's kern code, run over the interpreter's
// byte-addressed raw memory.
package interp

import (
	"fmt"
	"sync"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/layout"
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

// seqRef is a window [lo, hi) over a sequence of slots: Go slots reached
// through their owner, or raw memory (an access opened by ptr_view_seq).
type seqRef struct {
	elems  func() []Value
	raw    *rawSeq
	lo, hi int
}

func (s *seqRef) length() int { return s.hi - s.lo }
func (s *seqRef) at(i int) ref {
	if s.raw != nil {
		return s.raw.at(s.lo + i)
	}
	return idxRef{elems: s.elems, i: s.lo + i}
}

// rawSeq is a run of elements of one type in the interpreter's raw memory.
type rawSeq struct {
	it   *Interp
	base uint64
	elem resolve.Type
}

func (r *rawSeq) at(i int) ref {
	return rawRef{it: r.it, addr: r.base + uint64(i)*uint64(layout.Of(r.elem).Size), t: r.elem}
}

// rawRef is a place in raw memory: a value of type t at addr in its §4.1
// layout, decoded on read and encoded on write (ptr_view, ptr_edit and the
// kern containers' elements). Moving out leaves the bytes in place.
type rawRef struct {
	it   *Interp
	addr uint64
	t    resolve.Type
}

func (r rawRef) get() Value { return r.it.decode(r.it.rawAt(r.addr, r.t, ast.Pos{}), r.t) }
func (r rawRef) set(v Value) {
	if v == nil {
		return // a move or drop out of raw memory: the bytes are dead, nothing to write
	}
	r.it.encode(r.it.rawAt(r.addr, r.t, ast.Pos{}), r.t, v)
}

// field returns the place of struct field i (or tuple element i) of a raw aggregate.
func (r rawRef) field(i int) ref {
	switch x := r.t.(type) {
	case *resolve.Named:
		li := layout.Of(x)
		return rawRef{it: r.it, addr: r.addr + uint64(li.Fields[i]), t: resolve.Subst(x.Decl.Fields[i].Type, typeArgs(x))}
	case *resolve.Tuple:
		li := layout.Of(x)
		return rawRef{it: r.it, addr: r.addr + uint64(li.Fields[i]), t: x.Elems[i]}
	}
	panic("interp: field of non-aggregate raw place")
}

// variantField returns the place of payload field i of variant tag.
func (r rawRef) variantField(tag, i int) ref {
	n := r.t.(*resolve.Named)
	vd := n.Decl.Variants[tag]
	offs := layout.VariantLayout(n, vd)
	fts := resolve.VariantFieldTypes(n, vd)
	return rawRef{it: r.it, addr: r.addr + uint64(offs[i]), t: fts[i]}
}

// elems returns the window over a raw array place.
func (r rawRef) elems() *seqRef {
	arr := r.t.(*resolve.Array)
	return &seqRef{raw: &rawSeq{it: r.it, base: r.addr, elem: arr.Elem}, lo: 0, hi: arr.Len}
}

// ---- library objects (runtime-provided) ----

// Shared is an atomically reference-counted immutable value.
type Shared struct {
	Val   Value
	Count uint64
}

// Mutex guards a value.
type Mutex struct {
	Val    Value
	Locked bool
	owner  uint64     // thread holding the lock (recursive acquisition traps)
	state  sync.Mutex // guards Locked and owner
	lock   sync.Mutex // the lock itself
}

// mutexRef is the slot of a Mutex's value: the ref behind the edit that
// lock returns, so that Condvar.wait can find the Mutex behind its guard.
type mutexRef struct{ m *Mutex }

func (r mutexRef) get() Value  { return r.m.Val }
func (r mutexRef) set(v Value) { r.m.Val = v }

// Condvar is a condition variable. Waiters block on the channel of the
// generation current when they released the lock; a notify closes it and
// opens the next one, so notify_one wakes every waiter, which the spurious
// wakeups the spec permits allow. mutex is the Mutex the first wait used:
// every later wait must use the same one (as the hosted runtime requires).
type Condvar struct {
	state sync.Mutex
	gen   chan struct{}
	mutex *Mutex
}

// Scope is a thread scope.
type Scope struct{ Joins []*Join }

// Thread is a spawned thread (Phase 1: run eagerly).
type Thread struct {
	Result Value
	Done   bool
	done   chan struct{} // closed when the thread's function returned or trapped
	trap   *Trap
}

// Join is a scoped thread handle.
type Join struct {
	Result Value
	Done   bool
	done   chan struct{}
	trap   *Trap
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
