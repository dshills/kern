package parser

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dshills/kern/compiler/source"
)

// TestParsePrologueMatchesParse compares the prologue parser with the full
// parser on every fixture whose full parse succeeds (module and imports),
// and on malformed prologues (the SYNTAX diagnostic's code and span).
func TestParsePrologueMatchesParse(t *testing.T) {
	var files []string
	_ = filepath.WalkDir(filepath.Join("..", "..", "tests"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Ext(p) == ".kern" {
			files = append(files, p)
		}
		return nil
	})
	if len(files) == 0 {
		t.Fatal("no fixtures found")
	}
	compared := 0
	for _, name := range files {
		f, err := source.Load(name)
		if err != nil {
			t.Fatal(err)
		}
		full, ds := Parse(f)
		pro, pds := ParsePrologue(f)
		if len(ds) > 0 {
			if full != nil || (len(pds) > 0 && pds[0].Code != "SYNTAX") {
				t.Errorf("%s: full parse fails, prologue reports %v", name, pds)
			}
			continue
		}
		if len(pds) > 0 {
			t.Errorf("%s: prologue parse fails where the full parse succeeds: %v", name, pds)
			continue
		}
		if pro.Module != full.Module || len(pro.Imports) != len(full.Imports) {
			t.Errorf("%s: prologue %q/%d imports, full %q/%d", name, pro.Module, len(pro.Imports), full.Module, len(full.Imports))
			continue
		}
		for i, imp := range pro.Imports {
			if imp.Path != full.Imports[i].Path || imp.Pos != full.Imports[i].Pos {
				t.Errorf("%s: import %d differs: %+v vs %+v", name, i, imp, full.Imports[i])
			}
		}
		compared++
	}
	if compared == 0 {
		t.Fatal("no fixture compared")
	}
	for _, src := range []string{
		"func main() -> i32 { return 0i32 }\n",    // no module line
		"module m\nimport \"lib\n",                // unterminated import string
		"module m\nimport \"lib\" import \"x\"\n", // import without a newline
		"module\nimport \"lib\"\n",                // module without a name
	} {
		f := &source.File{Name: "bad.kern", Bytes: []byte(src)}
		_, ds := Parse(f)
		_, pds := ParsePrologue(f)
		if len(ds) == 0 || len(pds) == 0 {
			t.Errorf("%q: full %d diagnostics, prologue %d; both must fail", src, len(ds), len(pds))
			continue
		}
		if ds[0].Code != pds[0].Code || ds[0].Span != pds[0].Span {
			t.Errorf("%q: full %s %v, prologue %s %v", src, ds[0].Code, ds[0].Span, pds[0].Code, pds[0].Span)
		}
	}
}
