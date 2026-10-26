package parser

import (
	"testing"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/source"
)

const sample = `//! phase: 1
module storage
import "std/thread"

struct Page { id: u64; data: [4096]u8 }
union Result2[T, E] { Ok(T); Err(E) }
union Expr { Num(i64); Add(lhs: u32, rhs: u32); Nil }

interface Ord { func less(view self, other: view[Self]) -> bool }

impl Ord for u64 {
    func less(view self, other: view[u64]) -> bool { return true }
}

impl Page {
    destructor(edit self) { }
    func first(view self) -> view[]u8 { return self.data[0:8] }
}

contract {
    invariant "slots are valid"
    requires "a != b"
}
func get2[T](p: edit[Pool[T]], a: PoolId[T], b: PoolId[T]) -> (edit[T], edit[T]) {
    unsafe { return p.raw_two(a, b) }
}

func lock(m: view[Mutex[u64]]) -> edit[u64] release unlock { return m.inner }

extern "C" func puts(s: RawPtr[u8]) -> i32

func checksum(data: view[]u8) -> u64 {
    total := 0u64
    for b in data { total = total + u64(b) }
    var x: Page
    init_page(out x)?
    with l, r := get2(p, a, b) {
        l.count = MIN
    }
    match parse(x) {
        Ok(v) => { return v }
        Err(e) => return 0u64
        _ => {}
    }
    if total > 3 && !done { total = total - 1 } else if total == 0 { return 1 } else { }
    while i < n { i = i + 1 }
    s := thread.scope()
    f := Page { id: 1, data: buf }
    v := Vec[u64].new(copy(alloc))
    y := truncate[u8](total)
    z := move x
    drop(z)
    t := take(f.id)
    r := replace(f.id, 2u64)
    q := a[1:]
    w := a[:3]
    k := items[i]
    g := f[u64](1)
    return total
}
`

func TestParseSample(t *testing.T) {
	f, ds := Parse(&source.File{Name: "sample.kern", Bytes: []byte(sample)})
	if len(ds) != 0 {
		t.Fatalf("diagnostics: %v", ds[0].Message)
	}
	if f.Module != "storage" || len(f.Imports) != 1 || f.Imports[0].Path != "std/thread" {
		t.Fatalf("header wrong: %+v", f)
	}
	if len(f.Decls) != 10 {
		t.Fatalf("decls: got %d", len(f.Decls))
	}
	fn := f.Decls[9].(*ast.FuncDecl)
	if fn.Name != "checksum" || len(fn.Body.Stmts) != 21 {
		t.Fatalf("checksum: %s with %d stmts", fn.Name, len(fn.Body.Stmts))
	}
	g2 := f.Decls[6].(*ast.FuncDecl)
	if g2.Contract == nil || len(g2.Ret) != 2 || g2.TypeParams[0].Name != "T" {
		t.Fatalf("get2 wrong: %+v", g2)
	}
	lk := f.Decls[7].(*ast.FuncDecl)
	if lk.Release != "unlock" {
		t.Fatalf("release wrong: %q", lk.Release)
	}
	ex := f.Decls[8].(*ast.FuncDecl)
	if ex.Extern != "C" || ex.Body != nil {
		t.Fatalf("extern wrong")
	}
	if _, ok := fn.Body.Stmts[4].(*ast.With); !ok {
		t.Fatalf("with wrong: %T", fn.Body.Stmts[4])
	}
	m := fn.Body.Stmts[5].(*ast.Match)
	if len(m.Arms) != 3 || !m.Arms[2].Wild || m.Arms[0].Binds[0] != "v" {
		t.Fatalf("match wrong")
	}
	if _, ok := fn.Body.Stmts[9].(*ast.ShortDecl).Value.(*ast.StructLit); !ok {
		t.Fatalf("struct literal wrong: %T", fn.Body.Stmts[9].(*ast.ShortDecl).Value)
	}
}

func TestSyntaxError(t *testing.T) {
	_, ds := Parse(&source.File{Name: "bad.kern", Bytes: []byte("module m\nfunc f( {\n")})
	if len(ds) != 1 || ds[0].Code != "SYNTAX" {
		t.Fatalf("expected one SYNTAX diagnostic, got %v", ds)
	}
}

func TestCompositeTypeArgsAndEdgeCases(t *testing.T) {
	src := "module m\nfunc f() {\n    b := Box[[]u8].new(x)\n    v := Vec[view[u8]].new()\n    if i32(Point { x: 1 }.x) > 0 { }\n}\n"
	f, ds := Parse(&source.File{Name: "c.kern", Bytes: []byte(src)})
	if len(ds) != 0 {
		t.Fatalf("diagnostics: %v", ds[0].Message)
	}
	fn := f.Decls[0].(*ast.FuncDecl)
	call := fn.Body.Stmts[0].(*ast.ShortDecl).Value.(*ast.CallExpr)
	ix := call.Fn.(*ast.FieldExpr).X.(*ast.IndexExpr)
	if _, ok := ix.Args[0].(*ast.TypeArgExpr); !ok {
		t.Fatalf("slice type argument not wrapped: %T", ix.Args[0])
	}
	if _, ds := Parse(&source.File{Name: "d.kern", Bytes: []byte("module m\nfunc f() { x := Box[g().T[u8]] { } }\n")}); len(ds) != 1 || ds[0].Code != "SYNTAX" {
		t.Fatalf("expected one SYNTAX diagnostic, got %v", ds)
	}
}
