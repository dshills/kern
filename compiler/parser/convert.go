package parser

import "github.com/dshills/kern/compiler/ast"

// Converter reinterprets expressions parsed inside `[...]` as types, for the
// resolver's handling of generic instantiations such as f[u64](x). It never
// reports errors: an unconvertible expression yields nil.
type Converter struct{}

// ExprToType converts e to a type expression or returns nil.
func (Converter) ExprToType(e ast.Expr) ast.TypeExpr {
	switch x := e.(type) {
	case *ast.Ident:
		if primNames[x.Name] {
			return &ast.PrimType{Pos: x.Pos, Name: x.Name}
		}
		return &ast.TypeName{Pos: x.Pos, Name: x.Name}
	case *ast.FieldExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			return &ast.TypeName{Pos: x.Pos, Pkg: id.Name, Name: x.Name}
		}
	case *ast.TypeArgExpr:
		return x.Type
	case *ast.IndexExpr:
		var id *ast.Ident
		pkg := ""
		switch base := x.X.(type) {
		case *ast.Ident:
			id = base
		case *ast.FieldExpr:
			recv, ok := base.X.(*ast.Ident)
			if !ok {
				return nil
			}
			pkg = recv.Name
			id = &ast.Ident{Pos: base.Pos, Name: base.Name}
		default:
			return nil
		}
		if pkg == "" && id.Name == "RawPtr" && len(x.Args) == 1 {
			el := Converter{}.ExprToType(x.Args[0])
			if el == nil {
				return nil
			}
			return &ast.RawPtrType{Pos: x.Pos, Elem: el}
		}
		tn := &ast.TypeName{Pos: x.Pos, Pkg: pkg, Name: id.Name}
		for _, a := range x.Args {
			at := Converter{}.ExprToType(a)
			if at == nil {
				return nil
			}
			tn.Args = append(tn.Args, at)
		}
		return tn
	case *ast.UnitLit:
		return &ast.UnitType{Pos: x.Pos}
	}
	return nil
}
