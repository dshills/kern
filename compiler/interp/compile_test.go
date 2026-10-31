package interp

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/lower"
	"github.com/dshills/kern/compiler/opt"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// lowerFor lowers files through the exec pipeline of one configuration,
// or reports why it cannot.
func lowerFor(files []*source.File, level string) (*ir.Module, string) {
	prog := resolve.ResolveFor(files, level == "release")
	if len(prog.Diags) > 0 {
		return nil, prog.Diags[0].Message
	}
	if ds := check.Check(prog); len(ds) > 0 {
		return nil, ds[0].Message
	}
	mod, ds := lower.Lower(prog)
	if len(ds) > 0 {
		return nil, ds[0].Message
	}
	if err := opt.Run(mod, opt.ExecOptionsFor(level)); err != nil {
		return nil, err.Error()
	}
	return mod, ""
}

var recordDir = regexp.MustCompile(`"file":"[^"]*/`)

// runBoth runs a module compiled and through exec alone and fails on any
// difference in output, trap record or status (specs/interp-compile R2).
func runBoth(t *testing.T, what, file string, mod *ir.Module, hooks bool) {
	t.Helper()
	run := func(generic bool) (string, string, int) {
		var out, errb bytes.Buffer
		it := New(mod, file, &out, &errb)
		it.TestHooks = hooks
		it.genericExec = generic
		status := it.Run()
		return out.String(), recordDir.ReplaceAllString(errb.String(), `"file":"`), status
	}
	co, ce, cs := run(false)
	go_, ge, gs := run(true)
	if co != go_ || ce != ge || cs != gs {
		t.Errorf("%s: compiled and generic differ\n--- compiled (status %d)\n%s%s\n--- generic (status %d)\n%s%s", what, cs, co, ce, gs, go_, ge)
	}
}

// TestCompiledMatchesGenericFixtures runs every run and trap fixture both
// ways, in both configurations.
func TestCompiledMatchesGenericFixtures(t *testing.T) {
	var paths []string
	for _, dir := range []string{"run", "trap"} {
		m, err := filepath.Glob(filepath.Join("..", "..", "tests", dir, "*.kern"))
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, m...)
	}
	if len(paths) < 100 {
		t.Fatalf("found %d fixtures", len(paths))
	}
	ran := 0
	for _, path := range paths {
		f, err := source.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(f.Bytes)
		if strings.Contains(text, "//! native-only") || strings.Contains(text, "//! env: freestanding") || strings.Contains(text, "//! c-file") {
			continue
		}
		hooks := strings.Contains(text, "//! test-hooks")
		for _, level := range []string{"debug", "release"} {
			if hooks && level == "release" {
				continue // test hooks are a debug-build facility
			}
			mod, why := lowerFor([]*source.File{f}, level)
			if mod == nil || mod.Entry == "" {
				_ = why // fixtures that do not lower here are covered elsewhere
				continue
			}
			runBoth(t, filepath.Base(path)+" "+level, path, mod, hooks)
			ran++
		}
	}
	if ran < 150 {
		t.Fatalf("only %d fixture runs", ran)
	}
}

// TestCompiledMatchesGenericGenerated runs generated programs both ways:
// every integer operator on signed and unsigned types of several widths,
// the checks that trap (overflow, shift, division, bounds), and calls to
// user functions, library functions (Vec) and intrinsics.
func TestCompiledMatchesGenericGenerated(t *testing.T) {
	r := rand.New(rand.NewSource(11))
	types := []string{"u8", "u16", "u32", "u64", "i8", "i16", "i32", "i64", "usize", "isize"}
	ops := []string{"+", "-", "*", "/", "%", "<<", ">>", "&", "|", "^"}
	cmps := []string{"==", "!=", "<", "<=", ">", ">="}
	dir := t.TempDir()
	traps := 0
	for n := 0; n < 60; n++ {
		ty := types[r.Intn(len(types))]
		lit := func() string {
			v := r.Intn(300) - 40
			if ty == "u8" || ty == "i8" {
				v = r.Intn(120) - 10 // within both 8-bit ranges
			}
			if ty[0] == 'u' && v < 0 {
				v = -v
			}
			return fmt.Sprintf("%d%s", v, ty)
		}
		var sb strings.Builder
		sb.WriteString("module t\n")
		fmt.Fprintf(&sb, "func f(p: %s, b: %s) -> %s {\n    var a: %s = p\n", ty, ty, ty, ty)
		for k := 0; k < 4; k++ {
			o := ops[r.Intn(len(ops))]
			rhs := "b"
			if o == "<<" || o == ">>" {
				rhs = fmt.Sprintf("u32(b %% 9%s)", ty)
				if r.Intn(4) == 0 {
					rhs = "u32(b)" // may exceed the width: a shift trap
				}
			}
			fmt.Fprintf(&sb, "    a = wrapping_add(a, a %s %s)\n", o, rhs)
		}
		c := cmps[r.Intn(len(cmps))]
		fmt.Fprintf(&sb, "    if a %s b { return a }\n    return b\n}\n", c)
		sb.WriteString("func main() -> i32 {\n    v := Vec[u64].new()\n")
		fmt.Fprintf(&sb, "    var x: %s = %s\n    var y: %s = %s\n", ty, lit(), ty, lit())
		sb.WriteString("    var i: u64 = 0u64\n    while i < 6u64 {\n")
		sb.WriteString("        x = f(x, y)\n        v.push(u64(i))\n")
		sb.WriteString("        y = wrapping_add(y, x)\n        i = i + 1u64\n    }\n")
		sb.WriteString("    print_u64(v.get(usize(r % 6u64)))\n    println()\n")
		fmt.Fprintf(&sb, "    var arr: [4]%s = zero()\n    arr[usize(%d)] = x\n", ty, r.Intn(5)) // index 4 traps
		sb.WriteString("    return 0i32\n}\n")
		src := strings.Replace(sb.String(), "usize(r % 6u64)", fmt.Sprintf("usize(%du64 %% 6u64)", r.Intn(50)), 1)
		path := filepath.Join(dir, fmt.Sprintf("g%d.kern", n))
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := source.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, level := range []string{"debug", "release"} {
			mod, why := lowerFor([]*source.File{f}, level)
			if mod == nil {
				t.Fatalf("program %d does not lower (%s):\n%s", n, why, src)
			}
			var errb bytes.Buffer
			it := New(mod, path, &bytes.Buffer{}, &errb)
			if it.Run() != 0 {
				traps++
			}
			runBoth(t, fmt.Sprintf("generated %d %s", n, level), path, mod, false)
		}
	}
	if traps == 0 {
		t.Fatalf("no generated program trapped")
	}
}
