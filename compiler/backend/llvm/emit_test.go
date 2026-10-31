package llvm_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/backend/llvm"
	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/lower"
	"github.com/dshills/kern/compiler/opt"
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

// ---- cross-file inlining (specs/cross-file-inlining/SPEC.md R5, R7) ----

// emitFiles lowers a multi-file program, runs the passes of the given
// configuration and emits it, with debug sources when debug is set.
func emitFiles(t *testing.T, files map[string]string, optLevel string, debug bool) string {
	t.Helper()
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var srcs []*source.File
	for _, n := range names {
		srcs = append(srcs, &source.File{Name: n, Bytes: []byte(files[n])})
	}
	prog := resolve.ResolveFor(srcs, optLevel == "release")
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
	if err := opt.Run(mod, opt.OptionsFor(optLevel)); err != nil {
		t.Fatalf("opt: %v", err)
	}
	lo := llvm.Options{Triple: "aarch64-apple-macosx"}
	if debug {
		lo.Sources = map[string][]byte{"std/core.kern": resolve.Prelude(optLevel == "release")}
		for _, f := range srcs {
			lo.Sources[f.Name] = f.Bytes
		}
	}
	text, errs := llvm.EmitWith(mod, lo)
	if len(errs) > 0 {
		t.Fatalf("emit: %v", errs[0])
	}
	return text
}

// crossFileProgram is a two-file program whose imported helper has a
// bounds check on line 12 of lib/lib.kern, a line app.kern does not have.
var crossFileProgram = map[string]string{
	"app.kern": `module app
import "lib"
func main() -> i32 {
    xs := [1u64, 2u64, 3u64]
    print_u64(lib.Pick(xs[0usize:3usize], 1usize))
    println()
    return 0i32
}
`,
	"lib/lib.kern": "module lib\n\n\n\n\n\n\n\n\n\nfunc Pick(xs: view[[]u64], i: usize) -> u64 {\n    return xs[i]\n}\n",
}

func TestEmitCrossFileTrapFile(t *testing.T) {
	text := emitFiles(t, crossFileProgram, "release", false)
	if strings.Contains(text, "call i64 @lib.Pick(") || strings.Contains(text, "define i64 @lib.Pick(") {
		t.Fatalf("lib.Pick must be inlined and not emitted\n%s", text)
	}
	// main's own check (the slice bounds) is proven, so the copied check
	// is the only trap edge, and it is in another file than main's: the
	// landing block takes the file by phi, naming lib's constant
	lib := regexp.MustCompile(`(@kern\.file\.\d+) = private constant \[13 x i8\] c"lib/lib\.kern\\00"`).FindStringSubmatch(text)
	if lib == nil {
		t.Fatalf("no file constant for lib/lib.kern\n%s", text)
	}
	phi := regexp.MustCompile(`%trap\.file = phi ptr (.*)`).FindStringSubmatch(text)
	if phi == nil {
		t.Fatalf("the trap landing block of main must take the file by phi\n%s", text)
	}
	if !strings.Contains(phi[1], lib[1]+",") {
		t.Fatalf("the phi must name lib's file: %s", phi[0])
	}
	if !strings.Contains(text, `c"app.kern\00"`) {
		t.Fatalf("missing file constant for app.kern\n%s", text)
	}
	if n := strings.Count(text, "%trap.file = phi"); n != 1 {
		t.Fatalf("expected one file phi (main's), got %d\n%s", n, text)
	}
	// a one-file program's landing blocks are as before: no phi for the
	// file, and a library function (Vec.get, with its own checks)
	// reports the file its caller passed
	one := emit(t, `module t
func pick(xs: view[[]u64], i: usize) -> u64 { return xs[i] }
func main() -> i32 {
    xs := [1u64, 2u64, 3u64]
    print_u64(pick(xs[0usize:3usize], 1usize))
    println()
    v := Vec[u64].new()
    v.push(4u64)
    print_u64(v.get(0usize))
    println()
    return 0i32
}
`)
	if strings.Contains(one, "%trap.file") {
		t.Fatalf("a one-file program takes no file phi\n%s", one)
	}
	if !strings.Contains(one, "i64 %trap.end, ptr %span.f)") {
		t.Fatalf("a library function's landing block must report its caller's file\n%s", one)
	}
}

func TestEmitCrossFileDebugScope(t *testing.T) {
	text := emitFiles(t, crossFileProgram, "release", true)
	file := regexp.MustCompile(`!(\d+) = !DIFile\(filename: "lib\.kern", directory: "lib"\)`).FindStringSubmatch(text)
	if file == nil {
		t.Fatalf("no DIFile for lib/lib.kern\n%s", text)
	}
	lex := regexp.MustCompile(`!(\d+) = !DILexicalBlockFile\(scope: !\d+, file: !` + file[1] + `, discriminator: 0\)`).FindStringSubmatch(text)
	if lex == nil {
		t.Fatalf("no DILexicalBlockFile for lib/lib.kern\n%s", text)
	}
	if !regexp.MustCompile(`!DILocation\(line: 12, column: \d+, scope: !` + lex[1] + `\)`).MatchString(text) {
		t.Fatalf("the copied check's location must be line 12 of lib/lib.kern in that scope\n%s", text)
	}
	one := emitFiles(t, map[string]string{"t.kern": `module t
func pick(xs: view[[]u64], i: usize) -> u64 { return xs[i] }
func main() -> i32 {
    xs := [1u64, 2u64, 3u64]
    print_u64(pick(xs[0usize:3usize], 1usize))
    println()
    return 0i32
}
`}, "release", true)
	if strings.Contains(one, "DILexicalBlockFile") {
		t.Fatalf("a one-file program has no lexical block file\n%s", one)
	}
	// the toolchain accepts the module (compiled to an object, no link)
	clang, err := exec.LookPath("clang")
	if err != nil {
		t.Skip("clang not found")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "app.ll")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(clang, "-x", "ir", "-c", "-Wno-override-module", "-o", filepath.Join(dir, "app.o"), path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clang rejected the module: %v\n%s\n%s", err, out, text)
	}
}

// Runtime-raised traps take the user-level call's location
// (specs/runtime-trap-file/SPEC.md R2): in an imported module's function
// the module's file constant and the call's span; inside a library
// function the location its caller passed.
func TestEmitRuntimeTrapLocation(t *testing.T) {
	lib := "module lib\nfunc Locked() -> u64 {\n    m := Mutex[u64].new(3u64)\n    with g := m.lock() { return g }\n}\n"
	files := map[string]string{
		"app.kern":     "module app\nimport \"lib\"\nfunc main() -> i32 {\n    v := Vec[u64].new()\n    v.push(lib.Locked())\n    print_u64(v.get(0usize))\n    return 0i32\n}\n",
		"lib/lib.kern": lib,
	}
	text := emitFiles(t, files, "debug", false)
	sym := regexp.MustCompile(`(@kern\.file\.\d+) = private constant \[13 x i8\] c"lib/lib\.kern\\00"`).FindStringSubmatch(text)
	if sym == nil {
		t.Fatalf("no file constant for lib/lib.kern\n%s", text)
	}
	body := text[strings.Index(text, `define internal i64 @"lib.Locked"(`):]
	body = body[:strings.Index(body, "\n}\n")]
	newAt := strings.Index(lib, "Mutex[u64].new(3u64)")
	lockAt := strings.Index(lib, "m.lock()")
	// each call passes one pointer to a constant record of its span in
	// lib/lib.kern and that file's constant
	for name, span := range map[string][2]int{
		"kern_mutex_new":  {newAt, newAt + len("Mutex[u64].new(3u64)")},
		"kern_mutex_lock": {lockAt, lockAt + len("m.lock()")},
	} {
		m := regexp.MustCompile(`@` + name + `\([^)]*, ptr (@kern\.loc\.\d+)\)`).FindStringSubmatch(body)
		if m == nil {
			t.Errorf("lib.Locked's call to %s passes no location record\n%s", name, body)
			continue
		}
		want := fmt.Sprintf("%s = private constant { i64, i64, ptr } { i64 %d, i64 %d, ptr %s }", m[1], span[0], span[1], sym[1])
		if !strings.Contains(text, want) {
			t.Errorf("%s's record: want %q\n%s", name, want, text)
		}
	}
	// a library function passes on the location it was given, in a slot
	// filled from its span and file parameters
	if !strings.Contains(text, "store ptr %span.f, ptr") || !regexp.MustCompile(`call void @kern_sysalloc_alloc\([^)]*, ptr %a\d+\)`).MatchString(text) {
		t.Errorf("heap_try_alloc's allocator call must pass its caller's location\n%s", text)
	}
}

// Hosted functions carry the location marker the fault handler reads
// (specs/native-trap-loc/SPEC.md): the kern text section, the magic word
// and the index of the function's declaration in the table the entry
// hands the runtime; -1 for a library function. The entry wrapper carries
// main's index. A freestanding build has neither.
func TestEmitFunctionMarkers(t *testing.T) {
	src := "module t\nfunc f(n: u64) -> u64 { return n + 1u64 }\nfunc main() -> i32 {\n    v := Vec[u64].new()\n    v.push(f(1u64))\n    return 0i32\n}\n"
	lowerSrc := func() *ir.Module {
		f := &source.File{Name: "t.kern", Bytes: []byte(src)}
		prog := resolve.Resolve([]*source.File{f})
		if ds := check.Check(prog); len(prog.Diags)+len(ds) > 0 {
			t.Fatalf("check failed")
		}
		mod, ds := lower.Lower(prog)
		if len(ds) > 0 {
			t.Fatalf("lower: %s", ds[0].Message)
		}
		return mod
	}
	text := emit(t, src)
	const sec = `section "__TEXT,__kern_text,regular,pure_instructions" prefix { i64, i64 } { i64 2396846615841359179, i64 `
	fm := regexp.MustCompile(`define internal i64 @"t\.f"\([^)]*\) #0 ` + regexp.QuoteMeta(sec) + `(\d+) \}`).FindStringSubmatch(text)
	mm := regexp.MustCompile(`define internal i32 @"t\.main"\(\) #0 ` + regexp.QuoteMeta(sec) + `(\d+) \}`).FindStringSubmatch(text)
	if fm == nil || mm == nil {
		t.Fatalf("user functions carry no marker\n%s", text)
	}
	if !strings.Contains(text, sec+"-1 }") {
		t.Errorf("no library function carries the marker -1\n%s", text)
	}
	if !strings.Contains(text, "define i32 @main(i32 %argc, ptr %argv) "+sec+mm[1]+" }") || !strings.Contains(text, "call void @kern_set_fn_locs(ptr @kern.fn_locs, i64 2)") {
		t.Errorf("the entry wrapper must carry main's marker and hand over the table\n%s", text)
	}
	table := regexp.MustCompile(`@kern\.fn_locs = private constant \[2 x \{ i64, i64, ptr \}\] \[(.*)\]`).FindStringSubmatch(text)
	if table == nil {
		t.Fatalf("no location table\n%s", text)
	}
	entries := strings.Split(table[1], "}, {")
	fAt := strings.Index(src, "func f")
	fEnd := strings.Index(src, "}\n") + 1
	i, _ := strconv.Atoi(fm[1])
	if !strings.Contains(entries[i], fmt.Sprintf("i64 %d, i64 %d, ptr @kern.file.", fAt, fEnd)) {
		t.Errorf("f's entry %q, want its declaration %d-%d", entries[i], fAt, fEnd)
	}
	if elf, errs := llvm.Emit(lowerSrc(), "x86_64-unknown-linux-gnu"); len(errs) > 0 || !strings.Contains(elf, `section "kern_text" prefix`) {
		t.Errorf("an ELF target uses the kern_text section (errs %v)", errs)
	}
	free, errs := llvm.EmitWith(lowerSrc(), llvm.Options{Triple: "x86_64-unknown-none", Freestanding: true, StackCheck: true})
	if len(errs) > 0 || strings.Contains(free, "prefix") || strings.Contains(free, "kern_set_fn_locs") {
		t.Errorf("a freestanding build carries no marker (errs %v)", errs)
	}
}
