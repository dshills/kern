package tests

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dshills/kern/compiler/project"
)

// TestModuleNotFound is the reject/accept pair for MODULE_NOT_FOUND
// (specs/module-discovery/SPEC.md R25): the fixture harness names every
// file, so the code is reached through discovery on a temporary program
// instead of a committed fixture. The reject case imports a module with no
// directory; the accepting twin is the same program with the module
// present, which discovery resolves without a diagnostic.
func TestModuleNotFound(t *testing.T) {
	dir := t.TempDir()
	src := "module app\nimport \"lib\"\nfunc main() -> i32 {\n    print_u64(lib.One())\n    println()\n    return 0i32\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "app.kern"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	r := project.Load(dir, []string{"app.kern"})
	if len(r.Diags) != 1 || r.Diags[0].Code != "MODULE_NOT_FOUND" || r.Diags[0].File != "app.kern" {
		t.Fatalf("reject: want one MODULE_NOT_FOUND in app.kern, got %v", r.Diags)
	}
	if sp := r.Diags[0].Span; string(src[sp.Start:sp.End]) != `import "lib"` {
		t.Errorf("reject: span %v is %q, want the import declaration", sp, src[sp.Start:sp.End])
	}
	// the accepting twin: the module exists, and the program checks clean
	if err := os.MkdirAll(filepath.Join(dir, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lib", "lib.kern"), []byte("module lib\nfunc One() -> u64 { return 1u64 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r = project.Load(dir, []string{"app.kern"})
	if len(r.Diags) != 0 || len(r.Files) != 2 || r.Files[1].Name != filepath.Join("lib", "lib.kern") {
		t.Fatalf("accept: diagnostics %v, files %v", r.Diags, r.Names())
	}
	if ds := checkFiles(r.Files); len(ds) != 0 {
		t.Errorf("accept: the discovered program does not check clean: %v", render(ds))
	}
}
