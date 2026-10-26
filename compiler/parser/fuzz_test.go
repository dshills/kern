package parser_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dshills/kern/compiler/parser"
	"github.com/dshills/kern/compiler/source"
)

// FuzzParse: the parser never panics and reports at least one diagnostic
// or a file for any input (PLAN.md 4.4 standing fuzz job). Seeds are the
// repository's fixtures.
func FuzzParse(f *testing.F) {
	for _, dir := range []string{"positive", "negative", "run", "trap"} {
		m, _ := filepath.Glob(filepath.Join("..", "..", "tests", dir, "*.kern"))
		for i, p := range m {
			if i > 20 {
				break
			}
			b, err := os.ReadFile(p)
			if err == nil {
				f.Add(string(b))
			}
		}
	}
	f.Add("module m\nfunc main() -> i32 { return 0i32 }\n")
	f.Fuzz(func(t *testing.T, src string) {
		file, ds := parser.Parse(&source.File{Name: "fuzz.kern", Bytes: []byte(src)})
		if file == nil && len(ds) == 0 {
			t.Fatalf("no file and no diagnostics")
		}
	})
}
