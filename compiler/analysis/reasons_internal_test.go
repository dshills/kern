package analysis

import (
	"testing"

	"github.com/dshills/kern/compiler/ast"
)

// simple holds for a literal and for a name or a field path of a name,
// alone or plus or minus a literal, and for nothing else
// (specs/prove-nonstrict C2).
func TestSimpleBound(t *testing.T) {
	id := func(n string) ast.Expr { return &ast.Ident{Name: n} }
	lit := func(v uint64) *ast.IntLit { return &ast.IntLit{Value: v, Suffix: "usize"} }
	call := &ast.CallExpr{Fn: id("f")}
	for _, tc := range []struct {
		name string
		e    ast.Expr
		want bool
	}{
		{"n", id("n"), true},
		{"s.n", &ast.FieldExpr{X: id("s"), Name: "n"}, true},
		{"n + 1usize", &ast.Binary{Op: "+", L: id("n"), R: lit(1)}, true},
		{"n - 2usize", &ast.Binary{Op: "-", L: id("n"), R: lit(2)}, true},
		{"(n)", &ast.Paren{X: id("n")}, true},
		{"3usize", lit(3), true},
		{"f()", call, false},
		{"a + b", &ast.Binary{Op: "+", L: id("a"), R: id("b")}, false},
		{"f().n", &ast.FieldExpr{X: call, Name: "n"}, false},
		{"-2", &ast.IntLit{Value: 2, Neg: true}, false},
		{"omitted", nil, false},
	} {
		if got := simple(tc.e); got != tc.want {
			t.Errorf("simple(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}
