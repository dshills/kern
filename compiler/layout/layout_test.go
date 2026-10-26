package layout

import (
	"testing"

	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

func TestLayouts(t *testing.T) {
	prog := resolve.Resolve([]*source.File{{Name: "l.kern", Bytes: []byte(`module l
struct A { a: u8; b: u64; c: u16 }
struct Page { id: u64; data: [4096]u8 }
union U { X(u8); Y(u64, u32); Z }
struct Handles { p: PoolId[A]; o: Option[u8] }
`)}})
	if len(prog.Diags) > 0 {
		t.Fatal(prog.Diags[0].Message)
	}
	ty := func(name string) *resolve.Named { return &resolve.Named{Decl: prog.Main.Types[name]} }
	a := Of(ty("A"))
	if a.Size != 24 || a.Align != 8 || a.Fields[0] != 0 || a.Fields[1] != 8 || a.Fields[2] != 16 {
		t.Fatalf("A: %+v", a)
	}
	p := Of(ty("Page"))
	if p.Size != 4104 || p.Align != 8 {
		t.Fatalf("Page: %+v", p)
	}
	u := Of(ty("U"))
	// tag u8 at 0, payload at 8 (max align 8), largest payload Y = 16 bytes -> 24
	if u.Size != 24 || u.Align != 8 || u.Payload != 8 || u.TagSize != 1 {
		t.Fatalf("U: %+v", u)
	}
	y := VariantLayout(ty("U"), prog.Main.Types["U"].Variants[1])
	if y[0] != 8 || y[1] != 16 {
		t.Fatalf("Y fields: %v", y)
	}
	opt := Of(&resolve.Named{Decl: prog.Core.Types["Option"], Args: []resolve.Type{&resolve.Prim{Name: "u8"}}})
	if opt.Size != 2 || opt.Align != 1 {
		t.Fatalf("Option[u8]: %+v", opt)
	}
	if v := Of(&resolve.Named{Decl: prog.Core.Types["Vec"], Args: []resolve.Type{&resolve.Prim{Name: "u8"}}}); v.Size != 8 {
		t.Fatalf("Vec handle: %+v", v)
	}
	if s := Of(&resolve.Access{Seq: true, Elem: &resolve.Prim{Name: "u8"}}); s.Size != 16 {
		t.Fatalf("seq access: %+v", s)
	}
}
