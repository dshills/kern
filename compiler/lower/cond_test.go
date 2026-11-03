package lower

import (
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// lowerText resolves, checks and lowers one source.
func lowerText(t *testing.T, src string) *ir.Module {
	t.Helper()
	prog := resolve.Resolve([]*source.File{{Name: "c.kern", Bytes: []byte(src)}})
	if len(prog.Diags) > 0 {
		t.Fatalf("resolve: %s", prog.Diags[0].Message)
	}
	if ds := check.Check(prog); len(ds) > 0 {
		t.Fatalf("check: %s", ds[0].Message)
	}
	mod, ds := Lower(prog)
	if len(ds) > 0 {
		t.Fatalf("lower: %s", ds[0].Message)
	}
	if errs := ir.Validate(mod); len(errs) > 0 {
		t.Fatalf("validate: %v", errs[0])
	}
	return mod
}

const parenSource = `module c
func not_and(i: usize, j: usize, n: usize) -> u64 {
    if !(i < n && j < n) { return 0u64 }
    return 1u64
}
func and(i: usize, j: usize, n: usize) -> u64 {
    if (i < n && j < n) { return 1u64 }
    return 0u64
}
func twice(i: usize, n: usize) -> u64 {
    if ((i < n)) { return 1u64 }
    return 0u64
}
func twice_bare(i: usize, n: usize) -> u64 {
    if i < n { return 1u64 }
    return 0u64
}
func mixed(i: usize, j: usize, k: usize, n: usize) -> u64 {
    var c: u64 = 0u64
    while i < n && (j < n || k < n) { c = c + 1u64; return c }
    return c
}
func cmp(i: usize, n: usize) -> u64 {
    if (i < n) { return 1u64 }
    return 0u64
}
func cmp_bare(i: usize, n: usize) -> u64 {
    if i < n { return 1u64 }
    return 0u64
}
func main() -> i32 { return 0i32 }
`

// TestCondBrParens: a parenthesized condition, and a parenthesized operand
// of `&&`, `||` or `!` in one, is lowered as branches, one per comparison,
// with no value-position short circuit; a parenthesized comparison is
// lowered exactly as the comparison (specs/paren-conditions P1, R1).
func TestCondBrParens(t *testing.T) {
	mod := lowerText(t, parenSource)
	for name, comparisons := range map[string]int{"not_and": 2, "and": 2, "twice": 1, "mixed": 3, "cmp": 1} {
		fn := mod.Func("c." + name)
		if fn == nil {
			t.Fatalf("no function %s", name)
		}
		condBrs := 0
		for _, b := range fn.Blocks {
			if strings.HasPrefix(b.Name, "sc.") {
				t.Errorf("%s: value-position short circuit block %s\n%s", name, b.Name, ir.Print(mod))
			}
			if _, ok := b.Term.(*ir.CondBr); ok {
				condBrs++
			}
		}
		if condBrs != comparisons {
			t.Errorf("%s: %d conditional branches, want %d\n%s", name, condBrs, comparisons, ir.Print(mod))
		}
	}
	print := func(name string) string {
		var b strings.Builder
		for _, fn := range mod.Funcs {
			if fn.Name == "c."+name {
				one := &ir.Module{Funcs: []*ir.Func{fn}}
				b.WriteString(ir.Print(one))
			}
		}
		return strings.ReplaceAll(b.String(), "c."+name+"(", "c.F(")
	}
	for _, pair := range [][2]string{{"cmp", "cmp_bare"}, {"twice", "twice_bare"}} {
		if a, b := print(pair[0]), print(pair[1]); a != b {
			t.Errorf("%s is not lowered as %s:\n%s\n%s", pair[0], pair[1], a, b)
		}
	}
}
