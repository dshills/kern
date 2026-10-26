package tests

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dshills/kern/compiler/source"
)

// FuzzCheck: resolution and checking never panic on any input (PLAN.md 4.4
// standing fuzz job); the full pipeline either reports diagnostics or
// accepts the program.
func FuzzCheck(f *testing.F) {
	for _, dir := range []string{"positive", "negative", "run"} {
		m, _ := filepath.Glob(filepath.Join(dir, "*.kern"))
		for i, p := range m {
			if i > 25 {
				break
			}
			if b, err := os.ReadFile(p); err == nil {
				f.Add(string(b))
			}
		}
	}
	f.Fuzz(func(t *testing.T, src string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic: %v\n%s", r, src)
			}
		}()
		checkFiles([]*source.File{{Name: "fuzz.kern", Bytes: []byte(src)}})
	})
}
