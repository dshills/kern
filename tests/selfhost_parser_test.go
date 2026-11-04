package tests

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/backend/native"
	"github.com/dshills/kern/compiler/lexer"
	"github.com/dshills/kern/compiler/parser"
	"github.com/dshills/kern/compiler/project"
	"github.com/dshills/kern/compiler/source"
)

// selfhostRejects are syntax errors both parsers must reject
// (specs/selfhost-parser R3).
var selfhostRejects = map[string]string{
	"unterminated string":      "module m\nfunc f() { s := \"abc }\n",
	"unknown escape":           "module m\nfunc f() { s := \"a\\qb\" }\n",
	"missing brace":            "module m\nfunc f() { x := 1\n",
	"integer past u64":         "module m\nconst K: u64 = 18446744073709551616\n",
	"array length suffix":      "module m\nstruct S { a: [4usize]u8 }\n",
	"truncate non-primitive":   "module m\nfunc f() { x := truncate[Foo](y) }\n",
	"struct literal type":      "module m\nfunc f() { x := a.b.C { y: 1 } }\n",
	"interface before for":     "module m\nimpl [u8] for T { }\n",
	"non-ASCII outside string": "module m\nfunc f() { x := 1 \u00a7 2 }\n",
	"budget before struct":     "module m\nbudget { copies 0 }\nstruct S { a: u64 }\n",
	"budget before contract":   "module m\nbudget { copies 0 }\ncontract { requires \"x\" }\nfunc f() { }\n",
	"budget before extern":     "module m\nbudget { copies 0 }\nextern \"C\" func puts(s: RawPtr[u8]) -> i32\n",
	"budget before const":      "module m\nbudget { copies 0 }\nconst K: u64 = 1\n",
	"budget at end of file":    "module m\nbudget { copies 0 }\n",
	"budget before destructor": "module m\nstruct S { a: u64 }\nimpl S {\n    budget { copies 0 }\n    destructor(edit self) { }\n}\n",
	"budget on interface":      "module m\ninterface I {\n    budget { copies 0 }\n    func f(view self)\n}\n",
	"budget twice":             "module m\nbudget { copies 0 }\nbudget { allocations 0 }\nfunc f() { }\n",
	"budget name value":        "module m\nbudget { bounds_checks n }\nfunc f() { }\n",
	"budget negative":          "module m\nbudget { bounds_checks -1 }\nfunc f() { }\n",
	"budget float":             "module m\nbudget { bounds_checks 1.5 }\nfunc f() { }\n",
	"budget two on a line":     "module m\nbudget { copies 0 allocations 0 }\nfunc f() { }\n",
	"budget missing brace":     "module m\nbudget { copies 0\nfunc f() { }\n",
}

// The kern parser of examples/selfhost_parser accepts exactly the corpus
// files the Go parser accepts and prints ast.Dump of the Go parser's tree
// for each (specs/selfhost-parser R2-R4).
func TestSelfhostParser(t *testing.T) {
	if !native.Available() {
		t.Skip("clang not available")
	}
	dir := filepath.Join("..", "examples", "selfhost_parser")
	bin := selfhostBin(t, dir)
	run := func(path string, bodyless bool) (bool, string) {
		args := []string{path}
		if bodyless {
			args = append(args, "bodyless")
		}
		cmd := exec.Command(bin, args...)
		var out bytes.Buffer
		cmd.Stdout = &out
		err := cmd.Run()
		var ee *exec.ExitError
		switch {
		case err == nil:
			return true, out.String()
		case errors.As(err, &ee) && ee.ExitCode() == 1 && out.String() == "error\n":
			return false, ""
		}
		t.Fatalf("%s: %v, output %q", path, err, out.String())
		return false, ""
	}
	goParse := func(name string, src []byte, bodyless bool) (bool, string) {
		f, ds := parser.ParseWith(&source.File{Name: name, Bytes: src}, parser.Options{AllowBodyless: bodyless})
		if len(ds) > 0 {
			return false, ""
		}
		return true, ast.Dump(f)
	}
	compared, accepted, skipped := 0, 0, 0
	check := func(path string, src []byte, bodyless bool) {
		if nonASCIIIdent(src) {
			skipped++
			return
		}
		compared++
		gok, gdump := goParse(path, src, bodyless)
		kok, kdump := run(path, bodyless)
		if gok != kok {
			t.Errorf("%s: Go parser accepts %v, kern parser %v", path, gok, kok)
			return
		}
		if gok {
			accepted++
			if gdump != kdump {
				t.Errorf("%s: dumps differ at byte %d:\n go   %s\n kern %s", path, firstDiff(gdump, kdump), around(gdump, firstDiff(gdump, kdump)), around(kdump, firstDiff(gdump, kdump)))
			}
		}
	}
	for _, d := range []string{"tests", "std", "examples", filepath.Join("bench", "kern"), filepath.Join("compiler", "resolve", "prelude")} {
		bodyless := d == "std" || strings.Contains(d, "prelude")
		_ = filepath.Walk(filepath.Join("..", d), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".kern") {
				return nil
			}
			src, rerr := os.ReadFile(path)
			if rerr != nil {
				t.Fatal(rerr)
			}
			check(path, src, bodyless)
			return nil
		})
	}
	rejectDir := t.TempDir()
	for name, src := range selfhostRejects {
		path := filepath.Join(rejectDir, strings.ReplaceAll(name, " ", "_")+".kern")
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		gok, _ := goParse(path, []byte(src), false)
		kok, _ := run(path, false)
		if gok || kok {
			t.Errorf("%s: Go parser accepts %v, kern parser %v; both must reject", name, gok, kok)
		}
	}
	// the embedded sample (R4): the fixture's expected output is its dump
	cmd := exec.Command(bin)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("no-argument run: %v", err)
	}
	want, err := os.ReadFile(filepath.Join(dir, "selfhost_parser.out"))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(want) {
		t.Errorf("the sample's dump differs from selfhost_parser.out")
	}
	// and selfhost_parser.out is the Go parser's dump of the sample, the
	// string literal in main.kern that begins with "module sample"
	mainSrc, err := os.ReadFile(filepath.Join(dir, "main.kern"))
	if err != nil {
		t.Fatal(err)
	}
	mf, ds := parser.Parse(&source.File{Name: "main.kern", Bytes: mainSrc})
	if len(ds) > 0 {
		t.Fatalf("main.kern: %s", ds[0].Message)
	}
	var sample string
	ast.Walk(mf, func(n any) bool {
		if s, ok := n.(*ast.StringLit); ok && strings.HasPrefix(s.Value, "module sample") {
			sample = s.Value
			return true
		}
		return false
	})
	if sample == "" {
		t.Fatal("no sample in main.kern")
	}
	if ok, d := goParse("sample.kern", []byte(sample), false); !ok || d != string(want) {
		t.Errorf("selfhost_parser.out is not the Go parser's dump of the sample")
	}
	t.Logf("compared %d files (%d accepted, %d rejected), skipped %d with non-ASCII identifiers", compared, accepted, compared-accepted, skipped)
	if compared < 400 {
		t.Errorf("only %d corpus files compared", compared)
	}
}

// selfhostBin builds examples/selfhost_parser natively at release.
func selfhostBin(t *testing.T, dir string) string {
	t.Helper()
	pr := project.Load(dir, []string{"main.kern"})
	if len(pr.Diags) > 0 {
		t.Fatalf("project: %s", pr.Diags[0].Message)
	}
	bin := filepath.Join(t.TempDir(), "selfhost_parser")
	if ds, err := native.Build(pr.Files, bin, native.Options{Opt: "release"}); err != nil || len(ds) > 0 {
		t.Fatalf("build: %v %v", err, ds)
	}
	return bin
}

// With spans, the kern parser prints ast.DumpSpans of the Go parser's tree
// for every corpus file the Go parser accepts: every node's span and every
// extra position, byte for byte (specs/selfhost-spans R3). No file is
// skipped.
func TestSelfhostSpans(t *testing.T) {
	if !native.Available() {
		t.Skip("clang not available")
	}
	bin := selfhostBin(t, filepath.Join("..", "examples", "selfhost_parser"))
	compared, rejected := 0, 0
	for _, d := range []string{"tests", "std", "examples", filepath.Join("bench", "kern"), filepath.Join("compiler", "resolve", "prelude")} {
		bodyless := d == "std" || strings.Contains(d, "prelude")
		werr := filepath.Walk(filepath.Join("..", d), func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".kern") {
				return nil
			}
			src, rerr := os.ReadFile(path)
			if rerr != nil {
				t.Fatal(rerr)
			}
			f, ds := parser.ParseWith(&source.File{Name: path, Bytes: src}, parser.Options{AllowBodyless: bodyless})
			if len(ds) > 0 {
				rejected++
				return nil
			}
			args := []string{path, "spans"}
			if bodyless {
				args = append(args, "bodyless")
			}
			out, xerr := exec.Command(bin, args...).Output()
			if xerr != nil {
				t.Errorf("%s: kern parser: %v", path, xerr)
				return nil
			}
			compared++
			want, got := ast.DumpSpans(f), string(out)
			if want != got {
				i := firstDiff(want, got)
				t.Errorf("%s: span dumps differ at byte %d:\n go   %s\n kern %s", path, i, around(want, i), around(got, i))
			}
			return nil
		})
		if werr != nil {
			t.Fatalf("walking %s: %v", d, werr)
		}
	}
	t.Logf("compared the span dumps of %d files (%d rejected by the Go parser, not compared)", compared, rejected)
	if compared < 400 {
		t.Errorf("only %d corpus files compared", compared)
	}
}

// nonASCIIIdent reports whether the Go lexer finds an identifier holding a
// byte at or above 0x80, which the kern lexer does not support.
func nonASCIIIdent(src []byte) bool {
	toks, _ := lexer.Lex(src)
	for _, tk := range toks {
		if tk.Kind == lexer.Ident {
			for i := 0; i < len(tk.Text); i++ {
				if tk.Text[i] >= 0x80 {
					return true
				}
			}
		}
	}
	return false
}

func firstDiff(a, b string) int {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	return i
}

func around(s string, i int) string {
	lo, hi := i-60, i+60
	if lo < 0 {
		lo = 0
	}
	if hi > len(s) {
		hi = len(s)
	}
	return s[lo:hi]
}
