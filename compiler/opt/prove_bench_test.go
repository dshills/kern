package opt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/lower"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// lowerPath lowers one source file to a fresh module (the proof pass
// mutates provenance, so a benchmark lowers again per iteration).
func lowerPath(b *testing.B, path string) *ir.Module {
	b.Helper()
	f, err := source.Load(path)
	if err != nil {
		b.Fatal(err)
	}
	prog := resolve.Resolve([]*source.File{f})
	if len(prog.Diags) > 0 {
		b.Fatalf("resolve: %s", prog.Diags[0].Message)
	}
	if ds := check.Check(prog); len(ds) > 0 {
		b.Fatalf("check: %s", ds[0].Message)
	}
	mod, ds := lower.Lower(prog)
	if len(ds) > 0 {
		b.Fatalf("lower: %s", ds[0].Message)
	}
	return mod
}

// BenchmarkProveLexer times the pass on the largest example workload
// (specs/prove-guards/PLAN.md, the compile-time gate). Each iteration
// lowers its own module outside the timer, so one module is live at a
// time and the pass sees fresh provenance.
func BenchmarkProveLexer(b *testing.B) {
	path := filepath.Join("..", "..", "examples", "selfhost", "lexer.kern")
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		mod := lowerPath(b, path)
		b.StartTimer()
		Prove(mod)
	}
}

// BenchmarkProveSequentialGuards times the pass on a function of 200
// consecutive guarded accesses, each `if` joining back before the next:
// the join-heavy, loop-free shape whose cost is the layered state's delta
// joins.
func BenchmarkProveSequentialGuards(b *testing.B) {
	var sb strings.Builder
	sb.WriteString("module t\nfunc f(xs: view[]u64, i: usize) -> u64 {\n    s := 0u64\n")
	for k := 0; k < 200; k++ {
		fmt.Fprintf(&sb, "    if i + %dusize < len(xs) { s = wrapping_add(s, xs[i + %dusize]) }\n", k, k)
	}
	sb.WriteString("    return s\n}\nfunc main() -> i32 { return 0i32 }\n")
	dir := b.TempDir()
	path := filepath.Join(dir, "guards.kern")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		mod := lowerPath(b, path)
		b.StartTimer()
		Prove(mod)
	}
}
