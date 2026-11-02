package ast_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/parser"
	"github.com/dshills/kern/compiler/source"
)

// every node type the parser produces has a form (specs/selfhost-parser R1):
// testdata/all_nodes.kern uses each, and its dump is pinned in
// testdata/all_nodes.dump.
func TestDumpAllNodes(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("testdata", "all_nodes.kern"))
	if err != nil {
		t.Fatal(err)
	}
	f, ds := parser.Parse(&source.File{Name: "all_nodes.kern", Bytes: src})
	if len(ds) > 0 {
		t.Fatalf("parse: %s", ds[0].Message)
	}
	got := ast.Dump(f)
	if os.Getenv("KERN_UPDATE_DUMP") != "" {
		if err := os.WriteFile(filepath.Join("testdata", "all_nodes.dump"), []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(filepath.Join("testdata", "all_nodes.dump"))
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("dump changed:\ngot  %s\nwant %s", got, want)
	}
	seen := map[string]bool{}
	for _, part := range strings.Split(got, "(")[1:] {
		if w := strings.FieldsFunc(part, func(r rune) bool { return r == ' ' || r == ')' }); len(w) > 0 {
			seen[w[0]] = true
		}
	}
	for _, name := range []string{"file", "import", "tp", "field", "struct", "variant", "union", "interface", "impl", "destructor", "param", "contract", "budget", "item", "func", "const", "global", "assert", "prim", "tname", "array", "slice", "access", "rawptr", "functype", "unit", "block", "var", "short", "assign", "with", "arm", "match", "for", "while", "if", "return", "break", "continue", "unsafe", "expr", "int", "float", "string", "bool", "ident", "fieldx", "index", "typearg", "slicex", "arg", "call", "binary", "unary", "try", "move", "closure", "copy", "take", "replace", "drop", "trap", "conv", "finit", "structlit", "arraylit", "range", "unitlit", "paren"} {
		if !seen[name] {
			t.Errorf("all_nodes.kern produces no %s node", name)
		}
	}
}

// every corpus file the Go parser accepts dumps to a non-empty string, the
// same each time (R1).
func TestDumpCorpus(t *testing.T) {
	n := 0
	for _, dir := range []string{"tests", "std", "examples", filepath.Join("bench", "kern"), filepath.Join("compiler", "resolve", "prelude")} {
		werr := filepath.Walk(filepath.Join("..", "..", dir), func(path string, info os.FileInfo, err error) error {
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
			bodyless := dir == "std" || strings.Contains(dir, "prelude")
			f, ds := parser.ParseWith(&source.File{Name: path, Bytes: src}, parser.Options{AllowBodyless: bodyless})
			if len(ds) > 0 {
				return nil
			}
			a, b := ast.Dump(f), ast.Dump(f)
			if a == "" || a != b {
				t.Errorf("%s: dump empty or not repeatable", path)
			}
			n++
			return nil
		})
		if werr != nil {
			t.Fatalf("walking %s: %v", dir, werr)
		}
	}
	if n < 400 {
		t.Errorf("only %d corpus files dumped", n)
	}
}
