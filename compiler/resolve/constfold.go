package resolve

import "github.com/dshills/kern/compiler/ast"

// ConstFolder evaluates constants' initializers at compile time and
// remembers every result, so that a constant built from other constants
// is folded once however many times it is named.
type ConstFolder struct {
	active map[*ConstDecl]bool // the constants being folded, for cycle detection
	done   map[*ConstDecl]foldResult
}

type foldResult struct {
	value any
	ok    bool
}

// NewConstFolder returns an empty folder.
func NewConstFolder() *ConstFolder {
	return &ConstFolder{active: map[*ConstDecl]bool{}, done: map[*ConstDecl]foldResult{}}
}

// Fold evaluates a constant's initializer when it is built from literals,
// zero(), other constants that fold, fields and literal-index elements of
// such constants, array literals and struct literals. A scalar folds as a
// thread_local initializer does (an int64, a float64, a bool, or nil for
// zero()); an array or a struct folds to a []any of its elements or of its
// fields in declaration order, with nil standing for the zero value at any
// depth. An initializer of any other form (a call, an operator, a
// conversion) does not fold, and the constant is then lowered as an
// expression at each use.
func (f *ConstFolder) Fold(m *Module, k *ConstDecl) (any, bool) {
	if r, ok := f.done[k]; ok {
		return r.value, r.ok
	}
	if f.active[k] {
		return nil, false // a cycle; the checker reports it
	}
	f.active[k] = true
	v, ok := f.foldExpr(m, k.Type, k.Value)
	delete(f.active, k)
	f.done[k] = foldResult{value: v, ok: ok}
	return v, ok
}

func (f *ConstFolder) foldExpr(m *Module, t Type, e ast.Expr) (any, bool) {
	if v, ok := globalInit(t, e); ok {
		return v, true
	}
	switch x := e.(type) {
	case *ast.Ident, *ast.FieldExpr, *ast.IndexExpr:
		if pt, v, ok := f.foldPlace(m, e); ok && Identical(pt, t) {
			return v, true
		}
	case *ast.ArrayLit:
		arr, ok := t.(*Array)
		if !ok || len(x.Elems) != arr.Len {
			return nil, false
		}
		out := make([]any, len(x.Elems))
		for i, el := range x.Elems {
			v, ok := f.foldExpr(m, arr.Elem, el)
			if !ok {
				return nil, false
			}
			out[i] = v
		}
		return out, true
	case *ast.StructLit:
		n, ok := t.(*Named)
		if !ok || n.Decl.Kind != KindStruct {
			return nil, false
		}
		out := make([]any, len(n.Decl.Fields))
		set := make([]bool, len(n.Decl.Fields))
		for _, fi := range x.Fields {
			idx := -1
			for i, f := range n.Decl.Fields {
				if f.Name == fi.Name {
					idx = i
				}
			}
			if idx < 0 || set[idx] {
				return nil, false
			}
			_, ft := FieldType(n, fi.Name)
			v, ok := f.foldExpr(m, ft, fi.Value)
			if !ok {
				return nil, false
			}
			out[idx] = v
			set[idx] = true
		}
		for _, s := range set {
			if !s {
				return nil, false
			}
		}
		return out, true
	}
	return nil, false
}

// foldPlace folds an expression that names a constant or a part of one: a
// constant of this module or of an imported one, a field of a struct
// constant, or an element of an array constant at a literal index. It
// returns the part's type and folded value; nil is the zero value.
func (f *ConstFolder) foldPlace(m *Module, e ast.Expr) (Type, any, bool) {
	switch x := e.(type) {
	case *ast.Ident:
		if k, ok := m.Consts[x.Name]; ok {
			v, ok := f.Fold(m, k)
			return k.Type, v, ok
		}
	case *ast.FieldExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			if im, ok := m.Imports[id.Name]; ok {
				if k, ok := im.Consts[x.Name]; ok {
					v, ok := f.Fold(im, k)
					return k.Type, v, ok
				}
				return nil, nil, false
			}
		}
		bt, bv, ok := f.foldPlace(m, x.X)
		if !ok {
			return nil, nil, false
		}
		n, ok := bt.(*Named)
		if !ok || n.Decl.Kind != KindStruct {
			return nil, nil, false
		}
		for i, f := range n.Decl.Fields {
			if f.Name == x.Name {
				_, ft := FieldType(n, x.Name)
				return ft, part(bv, i), true
			}
		}
	case *ast.IndexExpr:
		if len(x.Args) != 1 {
			return nil, nil, false
		}
		lit, ok := x.Args[0].(*ast.IntLit)
		if !ok || lit.Neg {
			return nil, nil, false
		}
		bt, bv, ok := f.foldPlace(m, x.X)
		if !ok {
			return nil, nil, false
		}
		arr, ok := bt.(*Array)
		if !ok || lit.Value >= uint64(arr.Len) {
			return nil, nil, false
		}
		return arr.Elem, part(bv, int(lit.Value)), true
	}
	return nil, nil, false
}

// part is element i of a folded aggregate; the zero aggregate's parts are zero.
func part(v any, i int) any {
	if elems, ok := v.([]any); ok {
		return elems[i]
	}
	return nil
}
