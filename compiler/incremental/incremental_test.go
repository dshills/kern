package incremental

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/source"
)

// a three-module program: app imports geo and util; geo imports util
const utilSrc = `module util
const Scale: u64 = 3u64
struct Pair { A: u64; B: u64 }
func Twice(x: u64) -> u64 { return x + x }
func Pick[T: Copy](a: T, b: T, first: bool) -> T { if first { return a } return b }
contract { requires "nothing" }
func Raw(x: u64) -> u64 { return x }
`

const geoSrc = `module geo
import "util"
struct Point { X: u64; Y: u64 }
func Norm(p: view[Point]) -> u64 { return util.Twice(p.X) + p.Y * util.Scale }
func Make(x: u64) -> Point { return Point { X: x, Y: util.Pick(x, 1u64, x > 1u64) } }
`

const appSrc = `module app
import "geo"
import "util"
func main() -> i32 {
    p := geo.Make(4u64)
    n := geo.Norm(p)
    v := Vec[u64].new()
    v.push(n)
    v.push(util.Twice(n))
    total := 0u64
    for x in v.as_view() { total = total + x }
    return i32(total % 100u64)
}
`

func files(util, geo, app string) []*source.File {
	return []*source.File{
		{Name: "util/util.kern", Bytes: []byte(util)},
		{Name: "geo/geo.kern", Bytes: []byte(geo)},
		{Name: "app.kern", Bytes: []byte(app)},
	}
}

func same(t *testing.T, a, b []diag.Diagnostic) bool {
	t.Helper()
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func TestIncrementalMatchesFull(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Target: "aarch64-apple-macosx", Opt: "debug", Env: "hosted"}
	fs := files(utilSrc, geoSrc, appSrc)
	got, st, err := Check(dir, fs, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !same(t, got, full(fs)) || st.Hits != 0 || len(got) != 0 {
		t.Fatalf("first run: hits %d diags %v", st.Hits, got)
	}
	got, st, err = Check(dir, fs, cfg)
	if err != nil || st.Hits != 3 || !same(t, got, full(fs)) {
		t.Fatalf("second run should hit every module: %v %+v", err, st)
	}
}

// TestRandomizedEdits applies 1,000 random edits to the three modules and
// checks that incremental and full checking agree after every one.
func TestRandomizedEdits(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Target: "x86_64-unknown-linux-gnu", Opt: "release", Env: "hosted"}
	rng := rand.New(rand.NewSource(42))
	cur := []string{utilSrc, geoSrc, appSrc}
	hits := 0
	for i := 0; i < 1000; i++ {
		k := rng.Intn(3)
		cur[k] = mutate(rng, cur[k])
		fs := files(cur[0], cur[1], cur[2])
		got, st, err := Check(dir, fs, cfg)
		if err != nil {
			t.Fatal(err)
		}
		want := full(fs)
		if !same(t, got, want) {
			t.Fatalf("edit %d diverged:\n--- incremental\n%v\n--- full\n%v\n--- sources\n%s\n%s\n%s", i, got, want, cur[0], cur[1], cur[2])
		}
		hits += st.Hits
	}
	t.Logf("cache hits over 1000 edits: %d module hits", hits)
	if hits == 0 {
		t.Fatalf("the cache never hit")
	}
}

// mutate makes one random edit that may or may not change behaviour or
// validity: a literal, an operator, an identifier, a deleted or duplicated
// line, or a reverted previous edit.
func mutate(rng *rand.Rand, src string) string {
	lines := strings.Split(src, "\n")
	switch rng.Intn(6) {
	case 0: // change a literal
		return strings.Replace(src, fmt.Sprintf("%du64", 1+rng.Intn(4)), fmt.Sprintf("%du64", rng.Intn(9)), 1)
	case 1: // swap an operator
		ops := []string{"+", "*", "-", "/"}
		return strings.Replace(src, " "+ops[rng.Intn(4)]+" ", " "+ops[rng.Intn(4)]+" ", 1)
	case 2: // rename an identifier (may break resolution)
		names := []string{"Twice", "Scale", "Norm", "total", "Pair", "Pick"}
		from := names[rng.Intn(len(names))]
		to := from
		if rng.Intn(3) == 0 {
			to = from + "2"
		}
		return strings.Replace(src, from, to, 1)
	case 3: // delete a body line
		if len(lines) > 3 {
			k := 2 + rng.Intn(len(lines)-3)
			if strings.HasPrefix(strings.TrimSpace(lines[k]), "    ") || strings.Contains(lines[k], ":=") {
				return strings.Join(append(lines[:k:k], lines[k+1:]...), "\n")
			}
		}
		return src
	case 4: // duplicate a line
		k := rng.Intn(len(lines))
		out := append([]string{}, lines[:k+1]...)
		out = append(out, lines[k])
		return strings.Join(append(out, lines[k+1:]...), "\n")
	default: // whitespace only: interface unchanged
		return src + "\n"
	}
}

// TestTargetedInvalidation changes, in an imported module, one generic body,
// one constant, one marker-affecting field, one layout-affecting field, one
// contract block and one transitive effect, and asserts that the dependents
// are re-checked while an unrelated change (a function body) is not.
func TestTargetedInvalidation(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Target: "aarch64-apple-macosx", Opt: "debug", Env: "hosted"}
	base := files(utilSrc, geoSrc, appSrc)
	if _, _, err := Check(dir, base, cfg); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		util string
		dep  bool // dependents must be re-checked
	}{
		{"generic body", strings.Replace(utilSrc, "if first { return a }", "if !first { return a }", 1), true},
		{"constant", strings.Replace(utilSrc, "Scale: u64 = 3u64", "Scale: u64 = 4u64", 1), true},
		{"marker-affecting field", strings.Replace(utilSrc, "struct Pair { A: u64; B: u64 }", "struct Pair { A: u64; B: Vec[u64] }", 1), true},
		{"layout-affecting field", strings.Replace(utilSrc, "struct Pair { A: u64; B: u64 }", "struct Pair { A: u8; B: u64 }", 1), true},
		{"contract block", strings.Replace(utilSrc, `requires "nothing"`, `requires "x fits"`, 1), true},
		{"transitive effect", strings.Replace(utilSrc, "func Raw(x: u64) -> u64 { return x }", "func Raw(x: u64) -> u64 { v := Vec[u64].new(); v.push(x); return x }", 1), true},
		{"body only", strings.Replace(utilSrc, "return x + x", "return x * 2u64", 1), false},
	}
	for _, c := range cases {
		fs := files(c.util, geoSrc, appSrc)
		got, st, err := Check(dir, fs, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if !same(t, got, full(fs)) {
			t.Fatalf("%s: diverged", c.name)
		}
		// util is re-checked; its dependents only when its interface
		// changed, and otherwise served from their entries
		rechecked := strings.Join(st.Rechecked, ",")
		if c.dep && (st.Hits != 0 || rechecked != "app,geo,util") {
			t.Fatalf("%s: every module must be re-checked, got %+v", c.name, st)
		}
		if !c.dep && (st.Hits != 2 || rechecked != "util") {
			t.Fatalf("%s: only util must be re-checked, got %+v", c.name, st)
		}
		// after the change, checking again hits everything; a change that
		// left util's interface alone makes geo and app hit from before
		_, st2, _ := Check(dir, fs, cfg)
		if st2.Hits != 3 {
			t.Fatalf("%s: second run should hit, got %+v", c.name, st2)
		}
		// dependents: geo's and app's entries under the old util interface
		// must not be reused when the interface changed
		geoKeyChanged := ifaceChanged(t, dir, base, fs, cfg)
		if geoKeyChanged != c.dep {
			t.Fatalf("%s: dependents re-keyed = %v, want %v", c.name, geoKeyChanged, c.dep)
		}
	}
}

// ifaceChanged reports whether util's interface hash differs between two
// versions of the sources, i.e. whether dependents are re-keyed.
func ifaceChanged(t *testing.T, dir string, a, b []*source.File, cfg Config) bool {
	t.Helper()
	ha := ifaceHashOf(t, dir, a, cfg, "util")
	hb := ifaceHashOf(t, dir, b, cfg, "util")
	return ha != hb
}

func ifaceHashOf(t *testing.T, dir string, fs []*source.File, cfg Config, mod string) string {
	t.Helper()
	_, st, err := Check(dir, fs, cfg)
	if err != nil {
		t.Fatal(err)
	}
	h, ok := st.Ifaces[mod]
	if !ok {
		t.Fatalf("no interface hash for %s", mod)
	}
	return h
}

// TestConfigurations alternates two targets and the four opt/env
// configurations without editing sources: entries are distinct per
// configuration and every run agrees with the full check.
func TestConfigurations(t *testing.T) {
	dir := t.TempDir()
	fs := files(utilSrc, geoSrc, appSrc)
	want := full(fs)
	for round := 0; round < 2; round++ {
		for _, target := range []string{"aarch64-apple-macosx", "x86_64-unknown-linux-gnu"} {
			for _, opt := range []string{"debug", "release"} {
				for _, env := range []string{"hosted", "freestanding"} {
					got, st, err := Check(dir, fs, Config{Target: target, Opt: opt, Env: env})
					if err != nil || !same(t, got, want) {
						t.Fatalf("%s/%s/%s: %v", target, opt, env, err)
					}
					if (round == 0) == (st.Hits == 3) {
						t.Fatalf("%s/%s/%s round %d: hits %d", target, opt, env, round, st.Hits)
					}
				}
			}
		}
	}
}

// TestPerModuleReuse: an edit to one module re-checks that module alone
// when its interface is unchanged, the others served from their entries,
// with the full check's diagnostics, an introduced error included; a
// module importing the embedded library is cached like any other.
func TestPerModuleReuse(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Target: "aarch64-apple-macosx", Opt: "debug", Env: "hosted"}
	ioApp := strings.Replace(appSrc, "import \"util\"\n", "import \"util\"\nimport \"std/io\"\n", 1)
	if _, _, err := Check(dir, files(utilSrc, geoSrc, ioApp), cfg); err != nil {
		t.Fatal(err)
	}
	_, st, err := Check(dir, files(utilSrc, geoSrc, ioApp), cfg)
	if err != nil || st.Hits != 3 {
		t.Fatalf("a module importing std/io must hit: %+v %v", st, err)
	}
	for _, c := range []struct{ name, app string }{
		{"body", strings.Replace(ioApp, "total % 100u64", "total % 99u64", 1)},
		{"error", strings.Replace(ioApp, "v.push(n)\n", "v.push(n)\n    w := v\n    v.push(1u64)\n", 1)},
	} {
		fs := files(utilSrc, geoSrc, c.app)
		got, st, err := Check(dir, fs, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if !same(t, got, full(fs)) {
			t.Fatalf("%s: diverged from the full check:\n%v\n%v", c.name, got, full(fs))
		}
		if st.Hits != 2 || strings.Join(st.Rechecked, ",") != "app" {
			t.Fatalf("%s: only app must be re-checked, got %+v", c.name, st)
		}
		if c.name == "error" && len(got) == 0 {
			t.Fatalf("the edit must introduce an error")
		}
	}
}
