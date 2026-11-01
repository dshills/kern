package interp

import (
	"bytes"
	"testing"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/resolve"
)

// The destroy-plan cache stops growing at its cap, so fresh type values
// cannot grow it without bound, and a type's plan is reused
// (specs/interp-destroy-plans R3).
func TestDestroyPlanCap(t *testing.T) {
	var out, errb bytes.Buffer
	it := New(&ir.Module{}, "t.kern", &out, &errb)
	d := &resolve.TypeDecl{Kind: resolve.KindStruct, Name: "S", NoCopy: true}
	stable := &resolve.Named{Decl: d}
	first := it.planOf(stable)
	if again := it.planOf(stable); again != first {
		t.Error("a type's plan is not reused")
	}
	for i := 0; i < maxDestroyPlans+1000; i++ {
		it.planOf(&resolve.Named{Decl: d})
	}
	if len(it.plans) != maxDestroyPlans {
		t.Errorf("cache holds %d plans, want the cap %d", len(it.plans), maxDestroyPlans)
	}
	if c := it.child(); c.plans != nil {
		t.Error("a child thread shares its parent's cache")
	}
}

// buildPlan records each shape's work: an array's element, a destructor's
// name, a struct's field types, a union's variants' field types, and Copy.
func TestBuildPlanShapes(t *testing.T) {
	u64 := &resolve.Prim{Name: "u64"}
	s := &resolve.TypeDecl{Kind: resolve.KindStruct, Name: "S", NoCopy: true, Destructor: &ast.Destructor{}}
	s.Fields = []*resolve.FieldDecl{{Name: "a", Type: u64}, {Name: "b", Type: u64}}
	sn := &resolve.Named{Decl: s}
	if p := buildPlan(sn); p.copy || p.dtor != "S.destructor" || len(p.fields) != 2 {
		t.Errorf("struct plan %+v", p)
	}
	arr := &resolve.Array{Elem: sn, Len: 2}
	if p := buildPlan(arr); p.copy || p.elem != sn {
		t.Errorf("array plan %+v", p)
	}
	un := &resolve.TypeDecl{Kind: resolve.KindUnion, Name: "U", NoCopy: true}
	un.Variants = []*resolve.VariantDecl{{Name: "A"}, {Name: "B", Fields: []*resolve.FieldDecl{{Name: "x", Type: sn}}}}
	if p := buildPlan(&resolve.Named{Decl: un}); p.copy || len(p.variants) != 2 || len(p.variants[1]) != 1 || len(p.variants[0]) != 0 {
		t.Errorf("union plan %+v", p)
	}
	if p := buildPlan(u64); !p.copy {
		t.Errorf("u64 plan %+v", p)
	}
}
