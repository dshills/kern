// Package resolve performs name and type resolution for kern programs and
// derives the Copy/Send/Share/Interior markers (spec §5.1, §5.4, §5.5,
// spec/types.md). Its output is consumed by compiler/check.
package resolve

import (
	"fmt"
	"strings"

	"github.com/dshills/kern/compiler/ast"
)

// Type is a resolved type.
type Type interface {
	String() string
}

// Prim is a primitive type.
type Prim struct{ Name string }

// Unit is the unit type ().
type Unit struct{}

// Bad is the type of an expression that failed to resolve; it is compatible
// with every type so that one error does not cascade.
type Bad struct{}

func (*Bad) String() string { return "<error>" }

// Array is [N]T.
type Array struct {
	Len  int
	Elem Type
}

// Slice is []T.
type Slice struct{ Elem Type }

// Access is view[T], edit[T], view[]T or edit[]T.
type Access struct {
	Edit bool
	Seq  bool
	Elem Type
}

// RawPtr is RawPtr[T].
type RawPtr struct{ Elem Type }

// FuncParam is one parameter of a function type.
type FuncParam struct {
	Conv ast.Conv
	Type Type
}

// Func is a function type; Closure marks the second-class form
// `closure(P...) -> R` (spec §4): a value that may capture its creator's
// locals, passable only as an argument, never stored or returned.
type Func struct {
	Params  []FuncParam
	Ret     []Type
	Closure bool
}

// Named is an instantiated struct or union type.
type Named struct {
	Decl *TypeDecl
	Args []Type
}

// TypeParamType is a type parameter used inside a generic body.
type TypeParamType struct {
	Name        string
	Constraints []*Constraint
	Owner       string // declaring function or type name, for identity
}

// Tuple is a tuple of accesses returned by a multi-access function.
type Tuple struct{ Elems []Type }

// Constraint is a built-in marker or an interface.
type Constraint struct {
	Marker string         // "Copy", "Send", "Share" or ""
	Iface  *InterfaceDecl // when Marker == ""
}

func (p *Prim) String() string   { return p.Name }
func (*Unit) String() string     { return "()" }
func (a *Array) String() string  { return fmt.Sprintf("[%d]%s", a.Len, a.Elem) }
func (s *Slice) String() string  { return "[]" + s.Elem.String() }
func (r *RawPtr) String() string { return "RawPtr[" + r.Elem.String() + "]" }
func (t *Tuple) String() string {
	parts := make([]string, len(t.Elems))
	for i, e := range t.Elems {
		parts[i] = e.String()
	}
	return "(" + strings.Join(parts, ", ") + ")"
}
func (a *Access) String() string {
	k := "view"
	if a.Edit {
		k = "edit"
	}
	if a.Seq {
		return k + "[]" + a.Elem.String()
	}
	return k + "[" + a.Elem.String() + "]"
}
func (f *Func) String() string {
	parts := make([]string, len(f.Params))
	for i, p := range f.Params {
		switch p.Conv {
		case ast.ConvOwn:
			parts[i] = "own " + p.Type.String()
		case ast.ConvOut:
			parts[i] = "out " + p.Type.String()
		default:
			parts[i] = p.Type.String()
		}
	}
	kw := "func"
	if f.Closure {
		kw = "closure"
	}
	s := kw + "(" + strings.Join(parts, ", ") + ")"
	if len(f.Ret) == 1 {
		s += " -> " + f.Ret[0].String()
	} else if len(f.Ret) > 1 {
		s += " -> " + (&Tuple{f.Ret}).String()
	}
	return s
}
func (n *Named) String() string {
	if len(n.Args) == 0 {
		return n.Decl.Name
	}
	parts := make([]string, len(n.Args))
	for i, a := range n.Args {
		parts[i] = a.String()
	}
	return n.Decl.Name + "[" + strings.Join(parts, ", ") + "]"
}
func (t *TypeParamType) String() string { return t.Name }

// IsInteger reports whether t is an integer primitive.
func IsInteger(t Type) bool {
	p, ok := t.(*Prim)
	if !ok {
		return false
	}
	switch p.Name {
	case "i8", "i16", "i32", "i64", "isize", "u8", "u16", "u32", "u64", "usize":
		return true
	}
	return false
}

// IsFloat reports whether t is f32 or f64.
func IsFloat(t Type) bool {
	p, ok := t.(*Prim)
	return ok && (p.Name == "f32" || p.Name == "f64")
}

// IsBool reports whether t is bool.
func IsBool(t Type) bool {
	p, ok := t.(*Prim)
	return ok && p.Name == "bool"
}

// Identical reports structural type identity.
func Identical(a, b Type) bool {
	if _, ok := a.(*Bad); ok {
		return true
	}
	if _, ok := b.(*Bad); ok {
		return true
	}
	switch x := a.(type) {
	case *Prim:
		y, ok := b.(*Prim)
		return ok && x.Name == y.Name
	case *Unit:
		_, ok := b.(*Unit)
		return ok
	case *Array:
		y, ok := b.(*Array)
		return ok && x.Len == y.Len && Identical(x.Elem, y.Elem)
	case *Slice:
		y, ok := b.(*Slice)
		return ok && Identical(x.Elem, y.Elem)
	case *Access:
		y, ok := b.(*Access)
		return ok && x.Edit == y.Edit && x.Seq == y.Seq && Identical(x.Elem, y.Elem)
	case *RawPtr:
		y, ok := b.(*RawPtr)
		return ok && Identical(x.Elem, y.Elem)
	case *Func:
		y, ok := b.(*Func)
		if !ok || len(x.Params) != len(y.Params) || len(x.Ret) != len(y.Ret) || x.Closure != y.Closure {
			return false
		}
		for i := range x.Params {
			if x.Params[i].Conv != y.Params[i].Conv || !Identical(x.Params[i].Type, y.Params[i].Type) {
				return false
			}
		}
		for i := range x.Ret {
			if !Identical(x.Ret[i], y.Ret[i]) {
				return false
			}
		}
		return true
	case *Named:
		y, ok := b.(*Named)
		if !ok || x.Decl != y.Decl || len(x.Args) != len(y.Args) {
			return false
		}
		for i := range x.Args {
			if !Identical(x.Args[i], y.Args[i]) {
				return false
			}
		}
		return true
	case *TypeParamType:
		y, ok := b.(*TypeParamType)
		return ok && x.Name == y.Name && x.Owner == y.Owner
	case *Tuple:
		y, ok := b.(*Tuple)
		if !ok || len(x.Elems) != len(y.Elems) {
			return false
		}
		for i := range x.Elems {
			if !Identical(x.Elems[i], y.Elems[i]) {
				return false
			}
		}
		return true
	}
	return false
}

// Subst replaces type parameters by name using m.
func Subst(t Type, m map[string]Type) Type {
	if len(m) == 0 || t == nil {
		return t
	}
	switch x := t.(type) {
	case *TypeParamType:
		if r, ok := m[x.Name]; ok {
			return r
		}
		return x
	case *Array:
		return &Array{Len: x.Len, Elem: Subst(x.Elem, m)}
	case *Slice:
		return &Slice{Elem: Subst(x.Elem, m)}
	case *Access:
		return &Access{Edit: x.Edit, Seq: x.Seq, Elem: Subst(x.Elem, m)}
	case *RawPtr:
		return &RawPtr{Elem: Subst(x.Elem, m)}
	case *Func:
		f := &Func{Closure: x.Closure}
		for _, p := range x.Params {
			f.Params = append(f.Params, FuncParam{Conv: p.Conv, Type: Subst(p.Type, m)})
		}
		for _, r := range x.Ret {
			f.Ret = append(f.Ret, Subst(r, m))
		}
		return f
	case *Named:
		n := &Named{Decl: x.Decl}
		for _, a := range x.Args {
			n.Args = append(n.Args, Subst(a, m))
		}
		return n
	case *Tuple:
		tt := &Tuple{}
		for _, e := range x.Elems {
			tt.Elems = append(tt.Elems, Subst(e, m))
		}
		return tt
	}
	return t
}

// Unify binds type parameters in pattern (those named in params) so that it
// matches actual; it reports false on mismatch. Bindings accumulate in m.
func Unify(pattern, actual Type, params map[string]bool, m map[string]Type) bool {
	if _, ok := actual.(*Bad); ok {
		return true
	}
	if _, ok := pattern.(*Bad); ok {
		return true
	}
	switch p := pattern.(type) {
	case *TypeParamType:
		if bound, ok := m[p.Name]; ok {
			return Identical(bound, actual)
		}
		if params[p.Name] {
			m[p.Name] = actual
			return true
		}
		return Identical(pattern, actual)
	case *Array:
		a, ok := actual.(*Array)
		return ok && p.Len == a.Len && Unify(p.Elem, a.Elem, params, m)
	case *Slice:
		a, ok := actual.(*Slice)
		return ok && Unify(p.Elem, a.Elem, params, m)
	case *Access:
		a, ok := actual.(*Access)
		return ok && p.Edit == a.Edit && p.Seq == a.Seq && Unify(p.Elem, a.Elem, params, m)
	case *RawPtr:
		a, ok := actual.(*RawPtr)
		return ok && Unify(p.Elem, a.Elem, params, m)
	case *Func:
		a, ok := actual.(*Func)
		if !ok || len(p.Params) != len(a.Params) || len(p.Ret) != len(a.Ret) || p.Closure != a.Closure {
			return false
		}
		for i := range p.Params {
			if p.Params[i].Conv != a.Params[i].Conv || !Unify(p.Params[i].Type, a.Params[i].Type, params, m) {
				return false
			}
		}
		for i := range p.Ret {
			if !Unify(p.Ret[i], a.Ret[i], params, m) {
				return false
			}
		}
		return true
	case *Named:
		a, ok := actual.(*Named)
		if !ok || p.Decl != a.Decl || len(p.Args) != len(a.Args) {
			return false
		}
		for i := range p.Args {
			if !Unify(p.Args[i], a.Args[i], params, m) {
				return false
			}
		}
		return true
	case *Tuple:
		a, ok := actual.(*Tuple)
		if !ok || len(p.Elems) != len(a.Elems) {
			return false
		}
		for i := range p.Elems {
			if !Unify(p.Elems[i], a.Elems[i], params, m) {
				return false
			}
		}
		return true
	}
	return Identical(pattern, actual)
}
