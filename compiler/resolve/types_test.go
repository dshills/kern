package resolve

import "testing"

func TestIdenticalAndSubst(t *testing.T) {
	u64 := &Prim{Name: "u64"}
	tp := &TypeParamType{Name: "T", Owner: "f"}
	vec := &TypeDecl{Kind: KindStruct, Name: "Vec", Module: "core", TypeParams: []*TypeParamType{tp}, Fields: []*FieldDecl{{Name: "phantom", Type: tp}}, NoCopy: true}
	a := &Named{Decl: vec, Args: []Type{tp}}
	b := Subst(a, map[string]Type{"T": u64})
	if !Identical(b, &Named{Decl: vec, Args: []Type{u64}}) {
		t.Fatalf("subst: %s", b)
	}
	if Identical(a, b) {
		t.Fatal("T and u64 instantiations must differ")
	}
	if !Identical(&Access{Seq: true, Elem: u64}, &Access{Seq: true, Elem: u64}) || Identical(&Access{Edit: true, Elem: u64}, &Access{Elem: u64}) {
		t.Fatal("access identity")
	}
	if !Identical(&Bad{}, u64) || !Identical(u64, &Bad{}) {
		t.Fatal("Bad must be compatible with everything")
	}
	if (&Func{Params: []FuncParam{{Conv: 1, Type: u64}}, Ret: []Type{u64}}).String() != "func(u64) -> u64" {
		t.Fatalf("func string: %s", (&Func{Params: []FuncParam{{Conv: 1, Type: u64}}, Ret: []Type{u64}}).String())
	}
}

func TestUnify(t *testing.T) {
	u64 := &Prim{Name: "u64"}
	tp := &TypeParamType{Name: "T", Owner: "f"}
	m := map[string]Type{}
	params := map[string]bool{"T": true}
	if !Unify(&Array{Len: 4, Elem: tp}, &Array{Len: 4, Elem: u64}, params, m) || !Identical(m["T"], u64) {
		t.Fatal("array unify")
	}
	if Unify(&Array{Len: 4, Elem: tp}, &Array{Len: 5, Elem: u64}, params, m) {
		t.Fatal("length mismatch must fail")
	}
	// already-bound parameter must compare against its binding
	if Unify(tp, &Prim{Name: "u8"}, params, m) {
		t.Fatal("bound T=u64 must not unify with u8")
	}
	if !Unify(&Func{Params: []FuncParam{{Type: tp}}, Ret: []Type{tp}}, &Func{Params: []FuncParam{{Type: u64}}, Ret: []Type{u64}}, params, map[string]Type{}) {
		t.Fatal("func unify")
	}
}

func TestMarkers(t *testing.T) {
	u64 := &Prim{Name: "u64"}
	raw := &RawPtr{Elem: u64}
	if !IsCopy(u64) || !IsCopy(raw) || !IsSend(u64) || IsSend(raw) || IsShare(raw) || !IsShare(u64) || IsInterior(u64) {
		t.Fatal("primitive markers")
	}
	if !IsInteger(u64) || IsInteger(&Prim{Name: "f32"}) || !IsFloat(&Prim{Name: "f64"}) || !IsBool(&Prim{Name: "bool"}) || IsBool(u64) {
		t.Fatal("predicates")
	}
	box := &TypeDecl{Kind: KindStruct, Name: "Box", Module: "core", TypeParams: []*TypeParamType{{Name: "T", Owner: "Box"}}, NoCopy: true, Destructor: nil}
	box.Fields = []*FieldDecl{{Name: "inner", Type: box.TypeParams[0]}}
	bu := &Named{Decl: box, Args: []Type{u64}}
	br := &Named{Decl: box, Args: []Type{raw}}
	if IsCopy(bu) || !IsSend(bu) || IsSend(br) || !IsShare(bu) || IsShare(br) {
		t.Fatal("box markers")
	}
	withDtor := &TypeDecl{Kind: KindStruct, Name: "R", Fields: []*FieldDecl{{Name: "x", Type: u64}}}
	if !IsCopy(&Named{Decl: withDtor}) {
		t.Fatal("struct of Copy fields is Copy")
	}
	withDtor.NoCopy = true
	if IsCopy(&Named{Decl: withDtor}) {
		t.Fatal("nocopy opts out")
	}
	tpSend := &TypeParamType{Name: "T", Constraints: []*Constraint{{Marker: "Send"}}}
	if !IsSend(tpSend) || IsShare(tpSend) || IsCopy(tpSend) {
		t.Fatal("type parameter markers follow constraints")
	}
	// Mutex[T] is Share iff T is Send; Shared[T] is Send iff T is Send and Share
	prog := Resolve(nil)
	mutex := &Named{Decl: prog.Core.Types["Mutex"], Args: []Type{bu}}
	shared := &Named{Decl: prog.Core.Types["Shared"], Args: []Type{br}}
	if !IsShare(mutex) || !IsInterior(mutex) || IsSend(shared) || IsShare(shared) {
		t.Fatal("library marker rules")
	}
	if !HasDestructor(&Named{Decl: prog.Core.Types["Box"], Args: []Type{u64}}) || HasDestructor(bu) {
		t.Fatal("the prelude Box has a destructor; the local stand-in does not")
	}
}
