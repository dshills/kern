package llvm_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/backend/llvm"
	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/lower"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

func lowerSrc(t *testing.T, src string) *ir.Module {
	t.Helper()
	prog := resolve.Resolve([]*source.File{{Name: "t.kern", Bytes: []byte(src)}})
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
	return mod
}

// A recording keeps one RuntimeRef per request, with the instruction that
// made it, before the declarations are deduplicated: kern_now_ns asked
// for in two functions at two spans is recorded twice. An extern "C"
// function and an LLVM intrinsic are not runtime entries
// (specs/freestanding-availability §1).
func TestEmitRecorded(t *testing.T) {
	src := `module t
extern "C" func abs(x: i32) -> i32
func a() -> u64 { return now_ns() }
func b() -> u64 { return now_ns() + 1u64 }
contract { requires "abs is C's" }
func c() -> i32 { unsafe { return abs(-3i32) } }
func main() -> i32 {
    print_u64(a() + b())
    return c()
}
`
	mod := lowerSrc(t, src)
	refs, errs := llvm.EmitRecorded(mod, llvm.Options{Triple: "aarch64-unknown-none-elf", Freestanding: true, StackCheck: true})
	if len(errs) > 0 {
		t.Fatal(errs[0])
	}
	var now []string
	for _, r := range refs {
		if !strings.HasPrefix(r.Symbol, "kern_") {
			t.Errorf("recorded %s, which is not a runtime entry", r.Symbol)
		}
		if r.Symbol == "kern_now_ns" {
			c, ok := r.Instr.(*ir.Call)
			if !ok || c.Intrinsic != "now_ns" || r.Func == nil || r.Block == nil {
				t.Errorf("kern_now_ns recorded at %+v", r)
				continue
			}
			now = append(now, r.Func.Name+"@"+src[c.Pos.Start:c.Pos.End])
		}
	}
	sort.Strings(now)
	if got, want := strings.Join(now, " "), "t.a@now_ns() t.b@now_ns()"; got != want {
		t.Errorf("kern_now_ns recorded at %q, want %q", got, want)
	}
}
