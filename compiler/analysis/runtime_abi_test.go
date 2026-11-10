package analysis_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/backend/llvm"
	"github.com/dshills/kern/compiler/backend/native"
	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/lower"
	"github.com/dshills/kern/compiler/opt"
	"github.com/dshills/kern/compiler/project"
	"github.com/dshills/kern/compiler/resolve"
)

// The checks of specs/runtime/SPEC.md area A over both runtimes: they
// compile without a warning (RA2), every entry the compiler declares is
// defined with the same types and extensions (RA3), a runtime of another
// ABI version does not link (RA4), and the static analyzer reports nothing
// (RA6).

const (
	hostedDir = "../../runtime/hosted/src"
	fsDir     = "../../runtime/freestanding/src"
)

var strictFlags = []string{"-std=c11", "-Wall", "-Wextra", "-Wshadow", "-Wmissing-prototypes", "-Wstrict-prototypes", "-Wvla", "-Werror"}

var fsTriples = []string{"aarch64-unknown-none-elf", "x86_64-unknown-none-elf"}

func needClang(t *testing.T) string {
	t.Helper()
	clang, err := exec.LookPath("clang")
	if err != nil {
		t.Skip("clang not on PATH")
	}
	return clang
}

// hostedTriples are the host's own triple and the same operating system
// on the other architecture. The second needs that architecture's system
// headers: always there on macOS (one SDK serves both), and on Linux only
// with the cross headers installed, so there it is checked when it
// compiles and skipped otherwise (CI runs both Linux architectures
// natively).
func hostedTriples() (host string, other string) {
	host = native.HostTriple()
	switch {
	case strings.Contains(host, "apple") && strings.HasPrefix(host, "arm64"), strings.Contains(host, "apple") && strings.HasPrefix(host, "aarch64"):
		return host, "x86_64-apple-macos14.4"
	case strings.Contains(host, "apple"):
		return host, "arm64-apple-macos14.4"
	case strings.HasPrefix(host, "aarch64"), strings.HasPrefix(host, "arm64"):
		return host, "x86_64-unknown-linux-gnu"
	}
	return host, "aarch64-unknown-linux-gnu"
}

// TestRuntimeWarningClean is RA2.
func TestRuntimeWarningClean(t *testing.T) {
	clang := needClang(t)
	run := func(name string, args ...string) {
		t.Helper()
		if out, err := exec.Command(clang, append(append([]string{}, strictFlags...), args...)...).CombinedOutput(); err != nil {
			t.Errorf("%s: %v\n%s", name, err, out)
		}
	}
	for _, o := range []string{"-O0", "-O2"} {
		for _, defs := range [][]string{nil, {"-DKERN_RELEASE"}, {"-DKERN_TEST_HOOKS"}, {"-DKERN_RELEASE", "-DKERN_TEST_HOOKS"}} {
			args := append([]string{"-fsyntax-only", o}, defs...)
			run(fmt.Sprintf("hosted %s %v", o, defs), append(args, filepath.Join(hostedDir, "kern_rt.c"))...)
		}
	}
	for _, tr := range fsTriples {
		for _, f := range []string{"rt.c", "f64text.c", fsBoard(tr)} {
			for _, defs := range [][]string{nil, {"-DKERN_RELEASE"}} {
				args := append([]string{"-target", tr, "-ffreestanding", "-fno-builtin", "-fsyntax-only", "-O1"}, defs...)
				run(fmt.Sprintf("freestanding %s %s %v", tr, f, defs), append(args, filepath.Join(fsDir, f))...)
			}
		}
	}
}

// abiType is one parameter or result: its LLVM type and its extension
// attribute ("", "zeroext" or "signext").
type abiType struct{ typ, ext string }

type abiSig struct {
	ret    abiType
	params []abiType
}

func (s abiSig) String() string {
	show := func(a abiType) string {
		if a.ext == "" {
			return a.typ
		}
		return a.typ + " " + a.ext
	}
	ps := make([]string, len(s.params))
	for i, p := range s.params {
		ps[i] = show(p)
	}
	return show(s.ret) + " (" + strings.Join(ps, ", ") + ")"
}

var (
	llFunc = regexp.MustCompile(`^(define|declare) (.*?)@([A-Za-z_][A-Za-z0-9_.]*)\((.*)\)`)
	llType = regexp.MustCompile(`^(void|ptr|double|float|i[0-9]+)$`)
)

// parseABI reads one `define` or `declare` line of LLVM IR.
func parseABI(line string) (kind, name string, sig abiSig, internal, ok bool) {
	m := llFunc.FindStringSubmatch(line)
	if m == nil {
		return "", "", abiSig{}, false, false
	}
	one := func(toks []string) abiType {
		var a abiType
		for _, tok := range toks {
			switch {
			case tok == "zeroext" || tok == "signext":
				a.ext = tok
			case llType.MatchString(tok) && a.typ == "":
				a.typ = tok
			}
		}
		return a
	}
	pre := strings.Fields(m[2])
	for _, tok := range pre {
		if tok == "internal" || tok == "private" {
			internal = true
		}
	}
	sig.ret = one(pre)
	if strings.TrimSpace(m[4]) != "" {
		depth, start := 0, 0
		params := m[4]
		for i := 0; i <= len(params); i++ {
			if i == len(params) || (params[i] == ',' && depth == 0) {
				sig.params = append(sig.params, one(strings.Fields(params[start:i])))
				start = i + 1
				continue
			}
			switch params[i] {
			case '(', '{', '[':
				depth++
			case ')', '}', ']':
				depth--
			}
		}
	}
	return m[1], m[3], sig, internal, true
}

// runtimeDefs compiles C sources to LLVM IR with clang and returns the
// external functions they define.
func runtimeDefs(t *testing.T, clang string, args []string, srcs ...string) (map[string]abiSig, error) {
	t.Helper()
	defs := map[string]abiSig{}
	for _, src := range srcs {
		out, err := exec.Command(clang, append(append([]string{"-S", "-emit-llvm", "-O0", "-o", "-"}, args...), src)...).CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("clang %v %s: %v\n%s", args, src, err, out)
		}
		for line := range strings.SplitSeq(string(out), "\n") {
			if kind, name, sig, internal, ok := parseABI(line); ok && kind == "define" && !internal {
				defs[name] = sig
			}
		}
	}
	return defs, nil
}

// abiUnits are the corpus of the hosted-only inventory plus the
// concurrency and FFI fixtures, which reach the thread entries.
func abiUnits(t *testing.T) [][]string {
	t.Helper()
	units := corpusUnits(t)
	for _, pat := range []string{"tests/concurrency/*.kern", "tests/ffi/*.kern", "tests/freestanding/*.kern"} {
		m, _ := filepath.Glob(filepath.Join("..", "..", pat))
		sort.Strings(m)
		for _, f := range m {
			if !strings.HasSuffix(f, "_test.kern") {
				units = append(units, []string{f})
			}
		}
	}
	// entries no fixture can reach: eprint_str writes to standard error,
	// which the harness reserves for the trap record
	extra := filepath.Join(t.TempDir(), "abi_extra.kern")
	if err := os.WriteFile(extra, []byte("module t\nfunc main() -> i32 {\n    eprint_str(\"x\")\n    return 0i32\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	units = append(units, []string{extra})
	return units
}

// fsBoard is the default board file of a freestanding target (specs/runtime
// RJ1): the checks of the runtime's sources cover it too.
func fsBoard(triple string) string {
	if strings.HasPrefix(triple, "x86_64") {
		return "board_q35.c"
	}
	return "board_virt.c"
}

// compilerDecls lowers the corpus and returns every declaration of a
// runtime entry the emitter writes, for a hosted or freestanding target.
func compilerDecls(t *testing.T, triple string, freestandingMode bool) map[string]abiSig {
	t.Helper()
	cwd, _ := os.Getwd()
	decls := map[string]abiSig{}
	lowered := 0
	units := abiUnits(t)
	for _, u := range units {
		pr := project.Load(cwd, u)
		if len(pr.Diags) > 0 {
			continue
		}
		prog := resolve.Resolve(pr.Files)
		if len(prog.Diags) > 0 || len(check.Check(prog)) > 0 {
			continue
		}
		mod, ds := lower.Lower(prog)
		if len(ds) > 0 || opt.Run(mod, opt.OptionsFor("debug")) != nil {
			continue
		}
		// the last unit, the inline program, is emitted as a build that
		// names a stack size and asks for backtraces, each of which
		// declares one more entry
		var stack uint64
		backtrace := false
		if u[0] == units[len(units)-1][0] {
			stack, backtrace = 1<<20, true
		}
		text, errs := llvm.EmitWith(mod, llvm.Options{Triple: triple, Freestanding: freestandingMode, StackCheck: freestandingMode, StackBytes: stack, Backtrace: backtrace})
		if len(errs) > 0 {
			continue // a program the target cannot hold (hosted-only entries are still declared below when it emits)
		}
		lowered++
		for line := range strings.SplitSeq(text, "\n") {
			kind, name, sig, _, ok := parseABI(line)
			if !ok || kind != "declare" || !isEntryName(name) {
				continue
			}
			if old, seen := decls[name]; seen && old.String() != sig.String() {
				t.Errorf("%s is declared as %s and as %s", name, old, sig)
			}
			decls[name] = sig
		}
	}
	if lowered < 200 {
		t.Fatalf("only %d corpus programs emitted for %s", lowered, triple)
	}
	t.Logf("%s: %d programs emitted, %d runtime entries declared", triple, lowered, len(decls))
	return decls
}

func isEntryName(name string) bool {
	switch name {
	case "print_u64", "print_i64", "print_bool", "print_str", "eprint_str", "println":
		return true
	}
	return strings.HasPrefix(name, "kern_")
}

// unreferenced are the entries the runtimes define and the corpus never
// declares, each with the reason it exists.
var unreferencedHosted = map[string]string{}

var unreferencedFreestanding = map[string]string{}

// TestRuntimeABI is RA3: every declaration the compiler emits of a runtime
// entry matches the runtime's definition in result type, parameter count,
// parameter types and extension attributes.
func TestRuntimeABI(t *testing.T) {
	clang := needClang(t)
	compare := func(label string, decls, defs map[string]abiSig, mustDefine bool, unreferenced map[string]string) {
		t.Helper()
		names := make([]string, 0, len(decls))
		for n := range decls {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			def, ok := defs[n]
			if !ok {
				if mustDefine {
					t.Errorf("%s: the compiler declares %s, which the runtime does not define", label, n)
				}
				continue
			}
			if got, want := decls[n].String(), def.String(); got != want {
				t.Errorf("%s: %s is declared %s, defined %s", label, n, got, want)
			}
		}
		for n := range defs {
			if _, declared := decls[n]; declared || !isEntryName(n) {
				continue
			}
			if _, listed := unreferenced[n]; !listed {
				t.Errorf("%s: the runtime defines %s, which no corpus program declares and the test does not list", label, n)
			}
		}
		for n := range unreferenced {
			if _, declared := decls[n]; declared {
				t.Errorf("%s: %s is listed as unreferenced but the corpus declares it", label, n)
			}
		}
	}
	host, other := hostedTriples()
	for i, tr := range []string{host, other} {
		defs, err := runtimeDefs(t, clang, []string{"-target", tr, "-DKERN_TEST_HOOKS"}, filepath.Join(hostedDir, "kern_rt.c"))
		if err != nil {
			if i == 1 && runtime.GOOS == "linux" {
				t.Logf("skipping %s: its system headers are not installed (%v)", tr, strings.SplitN(err.Error(), "\n", 2)[0])
				continue
			}
			t.Fatal(err)
		}
		if len(defs) < 80 {
			t.Fatalf("%s: only %d definitions read from the hosted runtime", tr, len(defs))
		}
		compare("hosted "+tr, compilerDecls(t, tr, false), defs, true, unreferencedHosted)
	}
	for _, tr := range fsTriples {
		defs, err := runtimeDefs(t, clang, []string{"-target", tr, "-ffreestanding", "-fno-builtin"}, filepath.Join(fsDir, "rt.c"), filepath.Join(fsDir, "f64text.c"))
		if err != nil {
			t.Fatal(err)
		}
		// a freestanding emission still declares the hosted-only entries of
		// a program that uses them (the checker rejects it earlier), so a
		// declaration the runtime lacks is not an error here
		compare("freestanding "+tr, compilerDecls(t, tr, true), defs, false, unreferencedFreestanding)
	}
}

// TestParseABI pins the IR reader on the forms clang and the emitter write.
func TestParseABI(t *testing.T) {
	cases := []struct{ line, kind, name, sig string }{
		{`define void @print_bool(i8 noundef zeroext %0) #0 {`, "define", "print_bool", "void (i8 zeroext)"},
		{`define dso_local i32 @kern_io_open(ptr noundef %0, i64 noundef %1, i8 noundef zeroext %2) #0 {`, "define", "kern_io_open", "i32 (ptr, i64, i8 zeroext)"},
		{`define noundef zeroext i8 @f(i16 noundef signext %x, double %y) {`, "define", "f", "i8 zeroext (i16 signext, double)"},
		{`declare void @kern_trap(i32, i64, i64, ptr) noreturn cold`, "declare", "kern_trap", "void (i32, i64, i64, ptr)"},
		{`declare zeroext i8 @kern_q(i8 zeroext)`, "declare", "kern_q", "i8 zeroext (i8 zeroext)"},
		{`define ptr @kern_sysalloc_new() #0 {`, "define", "kern_sysalloc_new", "ptr ()"},
		{`define void @g(ptr noundef nonnull align 8 dereferenceable(16) %0, ptr noundef captures(none) %1) {`, "define", "g", "void (ptr, ptr)"},
	}
	for _, c := range cases {
		kind, name, sig, _, ok := parseABI(c.line)
		if !ok || kind != c.kind || name != c.name || sig.String() != c.sig {
			t.Errorf("%s\n  got %v %s %s %s", c.line, ok, kind, name, sig)
		}
	}
	if _, _, _, internal, ok := parseABI(`define internal void @kern_die() #1 {`); !ok || !internal {
		t.Errorf("an internal definition is not recognized")
	}
}

// TestRuntimeABIVersion is RA4: a program does not link against a runtime
// whose ABI symbol has another name.
func TestRuntimeABIVersion(t *testing.T) {
	clang := needClang(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "p.kern")
	if err := os.WriteFile(src, []byte("module p\nfunc main() -> i32 { return 0i32 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	pr := project.Load(cwd, []string{src})
	prog := resolve.Resolve(pr.Files)
	if len(pr.Diags) > 0 || len(prog.Diags) > 0 || len(check.Check(prog)) > 0 {
		t.Fatal("the test program does not check")
	}
	mod, ds := lower.Lower(prog)
	if len(ds) > 0 {
		t.Fatal(ds)
	}
	text, errs := llvm.EmitWith(mod, llvm.Options{Triple: native.HostTriple()})
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if !strings.Contains(text, "@kern_rt_abi_2") {
		t.Fatalf("the program does not reference the ABI symbol")
	}
	ll := filepath.Join(dir, "p.ll")
	if err := os.WriteFile(ll, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := filepath.Join(hostedDir, "kern_rt.c")
	link := func(extra ...string) ([]byte, error) {
		args := append([]string{"-Wno-override-module", "-o", filepath.Join(dir, "p"), ll, rt, "-lpthread", "-lm"}, extra...)
		return exec.Command(clang, args...).CombinedOutput()
	}
	if out, err := link(); err != nil {
		t.Fatalf("the program does not link against its own runtime: %v\n%s", err, out)
	}
	out, err := link("-Dkern_rt_abi_2=kern_rt_abi_other")
	if err == nil {
		t.Fatalf("the program linked against a runtime of another ABI version")
	}
	if !strings.Contains(string(out), "kern_rt_abi_2") {
		t.Fatalf("the link failure does not name the ABI symbol:\n%s", out)
	}
}

// TestRuntimeStaticAnalysis is RA6: clang's static analyzer reports
// nothing in either runtime.
func TestRuntimeStaticAnalysis(t *testing.T) {
	clang := needClang(t)
	analyze := func(name string, args ...string) {
		t.Helper()
		out, err := exec.Command(clang, append([]string{"--analyze", "-Xanalyzer", "-analyzer-output=text"}, args...)...).CombinedOutput()
		if err != nil && strings.Contains(string(out), "unknown argument") {
			t.Skipf("the static analyzer is unavailable: %s", out)
		}
		if err != nil || strings.Contains(string(out), "warning:") {
			t.Errorf("%s: %v\n%s", name, err, out)
		}
	}
	dir := t.TempDir()
	for _, defs := range [][]string{nil, {"-DKERN_TEST_HOOKS"}, {"-DKERN_RELEASE"}} {
		analyze(fmt.Sprintf("hosted %v", defs), append(append([]string{"-o", filepath.Join(dir, "h.plist")}, defs...), filepath.Join(hostedDir, "kern_rt.c"))...)
	}
	for _, tr := range fsTriples {
		for _, f := range []string{"rt.c", "f64text.c", fsBoard(tr)} {
			analyze("freestanding "+tr+" "+f, "-target", tr, "-ffreestanding", "-fno-builtin", "-o", filepath.Join(dir, "f.plist"), filepath.Join(fsDir, f))
		}
	}
}
