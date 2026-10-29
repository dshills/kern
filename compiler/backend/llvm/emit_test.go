package llvm_test

import (
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/backend/llvm"
	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/lower"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

func emit(t *testing.T, src string) string {
	t.Helper()
	f := &source.File{Name: "t.kern", Bytes: []byte(src)}
	prog := resolve.Resolve([]*source.File{f})
	if len(prog.Diags) > 0 {
		t.Fatalf("resolve: %s", prog.Diags[0].Message)
	}
	if ds := check.Check(prog); len(ds) > 0 {
		t.Fatalf("check: %s", ds[0].Message)
	}
	mod, ds := lower.Lower(prog)
	if len(ds) > 0 {
		t.Fatalf("lower: %s", ds[0].Message)
	}
	if errs := ir.Validate(mod); len(errs) > 0 {
		t.Fatalf("validate: %v", errs[0])
	}
	text, errs := llvm.Emit(mod, "aarch64-apple-macosx")
	if len(errs) > 0 {
		t.Fatalf("emit: %v", errs[0])
	}
	return text
}

// TestEmitShapes checks the backend's contract-bearing output shapes: the
// aliasing attributes of §5.4, sequence accesses as two scalars, stack probes
// on every function, thread-local globals, strict floating point (no
// fast-math flags) and exported extern symbols.
func TestEmitShapes(t *testing.T) {
	text := emit(t, `module t
struct P { x: u64; y: u64 }
thread_local depth: u32 = 0u32
func sum(xs: view[[]u64], p: view[P], q: edit[P], m: view[Mutex[u64]]) -> u64 {
    total := p.x + q.y
    for x in xs { total = total + x }
    q.x = total
    return total
}
func fl(a: f64, b: f64) -> f64 { return a * b + 1.0f64 }
extern "C" func kern_entry(x: u64) -> u64 { depth = depth + 1u32; return x }
func main() -> i32 { return 0i32 }
`)
	for _, want := range []string{
		"ptr noalias readonly %p0.p, i64 %p0.n", // sequence view: two scalars with attributes
		"ptr noalias readonly %p1",              // view of a plain struct
		"ptr noalias %p2",                       // edit
		"ptr %p3",                               // view of an Interior type: no attributes
		"attributes #0 = { \"probe-stack\"=\"inline-asm\" }",
		"thread_local global i32 0",
		"define i64 @kern_entry(i64 %p0) #0",
		"fmul double",
		"declare void @kern_trap(i32, i64, i64, ptr)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("emitted text lacks %q\n%s", want, text)
		}
	}
	for _, bad := range []string{"fast ", "nnan", "ninf", "contract "} {
		if strings.Contains(text, bad) {
			t.Errorf("emitted text carries fast-math flag %q", bad)
		}
	}
	if strings.Contains(text, "ptr noalias %p3") || strings.Contains(text, "readonly %p3") {
		t.Errorf("Interior view carries aliasing attributes")
	}
}

// TestEmitLargeAggregates keeps big arrays in memory: no aggregate register
// of a megabyte type, memset for zero and memcpy for copies.
func TestEmitLargeAggregates(t *testing.T) {
	text := emit(t, `module t
func f(n: u64) -> u8 {
    var buf: [1048576]u8 = zero()
    buf[usize(n)] = 1u8
    other := copy(buf)
    return other[0]
}
func main() -> i32 { return 0i32 }
`)
	if !strings.Contains(text, "llvm.memset.p0.i64(ptr %") || !strings.Contains(text, "llvm.memcpy.p0.p0.i64(ptr %") {
		t.Fatalf("expected memset and memcpy for a 1 MiB array:\n%s", text)
	}
	if strings.Contains(text, "store [1048576 x i8]") || strings.Contains(text, "load [1048576 x i8]") {
		t.Fatalf("1 MiB aggregate moved through a register:\n%s", text)
	}
}
