package lower

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// acceptedFixtures lists every single-file fixture and example that the
// checker accepts.
func acceptedFixtures(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, pat := range []string{"../../tests/positive/*.kern", "../../tests/negative/*_ok.kern", "../../tests/adversarial/*.kern", "../../examples/*/*.kern"} {
		m, _ := filepath.Glob(pat)
		out = append(out, m...)
	}
	sort.Strings(out)
	var accepted []string
	for _, p := range out {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "//! expect: accept") {
			accepted = append(accepted, p)
		}
	}
	return accepted
}

func TestLowerAccepted(t *testing.T) {
	for _, path := range acceptedFixtures(t) {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			f, err := source.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			prog := resolve.Resolve([]*source.File{f})
			if len(prog.Diags) > 0 {
				t.Fatalf("resolve: %s", prog.Diags[0].Message)
			}
			if ds := check.Check(prog); len(ds) > 0 {
				t.Fatalf("check: %s", ds[0].Message)
			}
			mod, ds := Lower(prog)
			if len(ds) > 0 {
				t.Fatalf("lower: %s", ds[0].Message)
			}
			text := ir.Print(mod)
			if strings.Contains(text, "<no terminator>") {
				t.Fatalf("block without terminator:\n%s", text)
			}
		})
	}
}

func TestValidateAccepted(t *testing.T) {
	for _, path := range acceptedFixtures(t) {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			f, err := source.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			prog := resolve.Resolve([]*source.File{f})
			if len(prog.Diags) > 0 {
				t.Skip("resolve diagnostics")
			}
			if ds := check.Check(prog); len(ds) > 0 {
				t.Skip("check diagnostics")
			}
			mod, _ := Lower(prog)
			if errs := ir.Validate(mod); len(errs) > 0 {
				t.Fatalf("validator: %v\n%s", errs[0], ir.Print(mod))
			}
		})
	}
}
