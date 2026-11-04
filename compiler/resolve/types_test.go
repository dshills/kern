package resolve

import (
	"testing"

	"github.com/dshills/kern/compiler/source"
)

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

// BoxIn and VecIn hold their element out of line and their allocator
// inline (specs/alloc-containers §2): Send and Share hold iff both the
// element and the allocator have them, Interior iff either has it, and
// neither container is ever Copy.
func TestAllocContainerMarkers(t *testing.T) {
	prog := Resolve(nil)
	u64 := &Prim{Name: "u64"}
	raw := &RawPtr{Elem: &Prim{Name: "u8"}}
	atomicDecl := prog.Core.Types["Atomic"]
	if atomicDecl == nil {
		t.Fatal("Atomic not declared")
	}
	atomic := &Named{Decl: atomicDecl, Args: []Type{u64}}
	type mcase struct {
		name       string
		is         func(Type) bool
		have, lack Type
	}
	cases := []mcase{
		{"Send", IsSend, u64, raw},
		{"Share", IsShare, u64, raw},
		{"Interior", IsInterior, atomic, u64},
	}
	for _, cname := range []string{"BoxIn", "VecIn", "ArenaIn", "PoolIn"} {
		decl := prog.Core.Types[cname]
		if decl == nil {
			t.Fatalf("%s not declared", cname)
		}
		for _, c := range cases {
			for _, pair := range []struct {
				elem, alloc Type
				label       string
			}{{c.have, c.have, "both"}, {c.have, c.lack, "element only"}, {c.lack, c.have, "allocator only"}, {c.lack, c.lack, "neither"}} {
				n := &Named{Decl: decl, Args: []Type{pair.elem, pair.alloc}}
				want := pair.label == "both"
				if c.name == "Interior" {
					want = pair.label != "neither"
				}
				if got := c.is(n); got != want {
					t.Errorf("%s %s with %s: got %v, want %v", cname, c.name, pair.label, got, want)
				}
				if IsCopy(n) {
					t.Errorf("%s with %s is Copy", cname, pair.label)
				}
			}
		}
	}
}

// MapIn holds its keys and values out of line and its allocator inline
// (specs/map-in): Send and Share hold iff the key, the value and the
// allocator all have them, Interior iff any has it, and it is never Copy.
func TestMapInMarkers(t *testing.T) {
	src := &source.File{Name: "m.kern", Bytes: []byte("module m\nimport \"std/map\"\nfunc main() -> i32 { return 0i32 }\n")}
	prog := Resolve([]*source.File{src})
	if len(prog.Diags) > 0 {
		t.Fatalf("resolve: %s", prog.Diags[0].Message)
	}
	var decl *TypeDecl
	for _, m := range prog.Modules {
		if d := m.Types["MapIn"]; d != nil {
			decl = d
		}
	}
	if decl == nil {
		t.Fatal("MapIn not declared")
	}
	u64 := &Prim{Name: "u64"}
	raw := &RawPtr{Elem: &Prim{Name: "u8"}}
	atomic := &Named{Decl: prog.Core.Types["Atomic"], Args: []Type{u64}}
	for _, c := range []struct {
		name       string
		is         func(Type) bool
		have, lack Type
	}{{"Send", IsSend, u64, raw}, {"Share", IsShare, u64, raw}, {"Interior", IsInterior, atomic, u64}} {
		for mask := 0; mask < 8; mask++ {
			args := make([]Type, 3)
			n := 0
			for i := range args {
				args[i] = c.lack
				if mask&(1<<i) != 0 {
					args[i] = c.have
					n++
				}
			}
			want := n == 3
			if c.name == "Interior" {
				want = n > 0
			}
			mt := &Named{Decl: decl, Args: args}
			if got := c.is(mt); got != want {
				t.Errorf("MapIn %s with %d of 3 having it (mask %03b): got %v, want %v", c.name, n, mask, got, want)
			}
			if IsCopy(mt) {
				t.Errorf("MapIn is Copy (mask %03b)", mask)
			}
		}
	}
}
