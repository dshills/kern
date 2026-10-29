package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/diag"
)

// testsRoot is the absolute path of tests/, taken before any test changes
// the working directory.
var testsRoot = func() string {
	p, err := filepath.Abs(filepath.Join("..", "..", "tests"))
	if err != nil {
		panic(err)
	}
	return p
}()

// copyFixture copies a directory fixture under tests/ into a fresh
// temporary directory and returns that directory.
func copyFixture(t *testing.T, rel string) string {
	t.Helper()
	src := filepath.Join(testsRoot, rel)
	dst := filepath.Join(t.TempDir(), filepath.Base(rel))
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		r, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, r), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, r), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

// invoke runs the command line and returns its exit status, standard
// output and standard error.
func invoke(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

func response(t *testing.T, out string) diag.Response {
	t.Helper()
	var resp diag.Response
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("stdout is not one JSON response: %v\n%s", err, out)
	}
	return resp
}

func firstCode(t *testing.T, out string) string {
	t.Helper()
	resp := response(t, out)
	if len(resp.Diagnostics) == 0 {
		t.Fatalf("no diagnostic in %s", out)
	}
	return string(resp.Diagnostics[0].Code)
}

// TestDiscoveryRunsFixtures: every import_* fixture runs, builds and checks
// with only its entry file named (specs/module-discovery/SPEC.md A1, A6).
func TestDiscoveryRunsFixtures(t *testing.T) {
	_, clangErr := exec.LookPath("clang")
	for _, fx := range []string{"positive/import_call", "positive/import_iface", "positive/import_funcval", "positive/import_const_array"} {
		dir := copyFixture(t, fx)
		want, err := os.ReadFile(filepath.Join(dir, filepath.Base(fx)+".out"))
		if err != nil {
			t.Fatal(err)
		}
		t.Chdir(dir)
		if code, out, errb := invoke(t, "", "run", "app.kern"); code != 0 || out != string(want) {
			t.Errorf("%s: run exit %d\n%s%s", fx, code, out, errb)
		}
		if code, out, _ := invoke(t, "", "check", "app.kern"); code != 0 {
			t.Errorf("%s: check exit %d %s", fx, code, out)
		} else if files := response(t, out).Result.(map[string]any)["files"]; len(files.([]any)) != 2 {
			t.Errorf("%s: check lists %v, want the entry and the discovered file", fx, files)
		}
		if clangErr == nil {
			if code, out, _ := invoke(t, "", "build", "-o", "prog", "app.kern"); code != 0 {
				t.Errorf("%s: build exit %d %s", fx, code, out)
			} else if got, err := exec.Command("./prog").Output(); err != nil || string(got) != string(want) {
				t.Errorf("%s: built program: %v\n%s", fx, err, got)
			}
		}
	}
	// a trap inside the imported module names the module's file (A6)
	dir := copyFixture(t, "trap/import_trap")
	t.Chdir(dir)
	code, _, errb := invoke(t, "", "run", "app.kern")
	if code != 134 || !strings.Contains(errb, `"file":"lib/lib.kern"`) {
		t.Errorf("trap run: exit %d stderr %q", code, errb)
	}
	// machine-API locations name the discovered file too
	_, out, _ := invoke(t, "", "show-bounds-checks", "app.kern")
	if !strings.Contains(out, `"file":"lib/lib.kern"`) {
		t.Errorf("show-bounds-checks locations: %s", out)
	}
}

// TestManifestProject: the manifest marks the root, supplies the entry and
// the settings, and is found by walking up (A2, A5, A7, R18, R23).
func TestManifestProject(t *testing.T) {
	dir := copyFixture(t, "positive/import_call")
	want, _ := os.ReadFile(filepath.Join(dir, "import_call.out"))
	// an empty manifest: the root is found from a file named from the parent directory
	if err := os.WriteFile(filepath.Join(dir, "kern.mod"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Dir(dir))
	if code, out, errb := invoke(t, "", "run", filepath.Join("import_call", "app.kern")); code != 0 || out != string(want) {
		t.Errorf("run from the parent: exit %d\n%s%s", code, out, errb)
	}
	// a manifest with an entry: no file named, from the root and from a subdirectory
	if err := os.WriteFile(filepath.Join(dir, "kern.mod"), []byte(`{"entry": "app.kern", "opt": "release", "output": "built"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if code, out, errb := invoke(t, "", "run"); code != 0 || out != string(want) {
		t.Errorf("run with no file: exit %d\n%s%s", code, out, errb)
	}
	if code, out, _ := invoke(t, "", "check"); code != 0 {
		t.Errorf("check with no file: exit %d %s", code, out)
	} else if files := response(t, out).Result.(map[string]any)["files"].([]any); len(files) != 2 || files[0] != "app.kern" || files[1] != filepath.Join("lib", "lib.kern") {
		t.Errorf("check files %v", files)
	}
	if code, out, _ := invoke(t, "", "format"); code != 0 {
		t.Errorf("format with no file: exit %d %s", code, out)
	} else if files := response(t, out).Result.(map[string]any)["files"].([]any); len(files) != 1 {
		t.Errorf("format rewrites the entry only, got %d files", len(files))
	}
	if _, clangErr := exec.LookPath("clang"); clangErr == nil {
		if code, out, _ := invoke(t, "", "build"); code != 0 {
			t.Errorf("build with no file and -o from the manifest: exit %d %s", code, out)
		} else if _, err := os.Stat(filepath.Join(dir, "built")); err != nil {
			t.Errorf("manifest output not written: %v", err)
		}
	}
	t.Chdir(filepath.Join(dir, "lib"))
	if code, out, _ := invoke(t, "", "check"); code != 0 {
		t.Errorf("check from a subdirectory: exit %d %s", code, out)
	}
	t.Chdir(dir)
	// explain-ownership accepts a span in the discovered file and rejects one in an unnamed file
	lib, _ := os.ReadFile(filepath.Join(dir, "lib", "lib.kern"))
	at := bytes.Index(lib, []byte("id"))
	req := `{"schema_version":"0.2","command":"explain-ownership","files":["app.kern"],"span":{"file":"lib/lib.kern","start":` + itoa(at) + `,"end":` + itoa(at+2) + `}}`
	if code, out, _ := invoke(t, req, "explain-ownership"); code != 0 {
		t.Errorf("explain-ownership in a discovered file: exit %d %s", code, out)
	}
	req = strings.Replace(req, `"file":"lib/lib.kern"`, `"file":"nowhere.kern"`, 1)
	if _, out, _ := invoke(t, req, "explain-ownership"); firstCode(t, out) != "API_INVALID_SPAN" {
		t.Errorf("explain-ownership outside the set: %s", out)
	}
	// the manifest's settings, and the flags that override them
	if code, out, _ := invoke(t, "", "check", "--opt=debug"); code != 0 {
		t.Errorf("--opt override: %d %s", code, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "kern.mod"), []byte(`{"entry": "app.kern", "target": "x86_64-unknown-linux-gnu", "env": "freestanding", "opt": "release"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, out, _ := invoke(t, `{"schema_version":"0.2","command":"check","files":[],"target":"aarch64-apple-macosx"}`, "check", "--target=x86_64-unknown-linux-gnu"); firstCode(t, out) != "API_TARGET_CONFLICT" {
		t.Errorf("API_TARGET_CONFLICT with a manifest: %s", out)
	}
	if code, out, _ := invoke(t, "", "check", "--env=hosted", "--target=aarch64-unknown-none"); code != 0 {
		t.Errorf("--env and --target override the manifest: %d %s", code, out)
	}
	if code, out, errb := invoke(t, "", "run", "--env=hosted", "--opt=debug"); code != 0 || out != string(want) {
		t.Errorf("run with overrides: exit %d\n%s%s", code, out, errb)
	}
}

func itoa(n int) string { return strings.TrimSpace(strings.Repeat(" ", 0) + jsonInt(n)) }

func jsonInt(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// TestProjectDiagnostics: the manifest's rejections, the missing project,
// and the module diagnostics through the command line (A3, A4).
func TestProjectDiagnostics(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if _, out, _ := invoke(t, "", "run"); firstCode(t, out) != "PROJECT_NOT_FOUND" {
		t.Errorf("run with nothing: %s", out)
	}
	if _, out, _ := invoke(t, "", "build", "-o", "x"); firstCode(t, out) != "PROJECT_NOT_FOUND" {
		t.Errorf("build with nothing: %s", out)
	}
	if _, out, _ := invoke(t, "", "check"); firstCode(t, out) != "API_NO_FILES" {
		t.Errorf("check with nothing: %s", out)
	}
	for src, field := range map[string]string{
		`{"optx": 1}`: "optx", `{"opt": 1}`: "opt", `{"opt": "fast"}`: "opt", `{"entry": "../x.kern"}`: "entry",
		`{"entry": []}`: "entry", `{"schema_version": "0.1"}`: "schema_version", `{"entry": "a.kern"} x`: "invalid character", `{`: "unexpected",
	} {
		if err := os.WriteFile("kern.mod", []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		_, out, _ := invoke(t, "", "run")
		resp := response(t, out)
		if len(resp.Diagnostics) != 1 || resp.Diagnostics[0].Code != "PROJECT_INVALID" {
			t.Errorf("%q: %s", src, out)
			continue
		}
		if _, out, _ = invoke(t, "", "check", "--human"); !strings.Contains(out, "PROJECT_INVALID") || !strings.Contains(out, field) {
			t.Errorf("%q: human output %q lacks the code or %q", src, out, field)
		}
	}
	if err := os.WriteFile("kern.mod", []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, out, _ := invoke(t, "", "run"); firstCode(t, out) != "PROJECT_INVALID" {
		t.Errorf("manifest without entry: %s", out)
	}
	// module diagnostics: two missing imports, a mismatch, and equivalence with naming every file
	if err := os.Remove("kern.mod"); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("app.kern", "module app\nimport \"nowhere\"\nimport \"elsewhere\"\nfunc main() -> i32 { return 0i32 }\n")
	// formatting needs no import: it works while a module is missing (A7)
	if code, out, _ := invoke(t, "", "format", "app.kern"); code != 0 {
		t.Errorf("format with a missing import: exit %d %s", code, out)
	}
	_, out, _ := invoke(t, "", "check", "app.kern")
	if resp := response(t, out); len(resp.Diagnostics) != 2 || resp.Diagnostics[0].Code != "MODULE_NOT_FOUND" || resp.Diagnostics[1].Code != "MODULE_NOT_FOUND" {
		t.Errorf("two missing imports: %s", out)
	}
	write("app.kern", "module app\nimport \"lib\"\nfunc main() -> i32 { return 0i32 }\n")
	write("lib/lib.kern", "module other\n")
	if _, out, _ := invoke(t, "", "check", "app.kern"); firstCode(t, out) != "MODULE_MISMATCH" {
		t.Errorf("wrong module name: %s", out)
	}
	write("lib/lib.kern", "module lib\nfunc Bad() -> u64 { return true }\nfunc Worse(x: u64) -> u64 { return x + \"s\" }\n")
	_, discovered, _ := invoke(t, "", "check", "app.kern")
	_, named, _ := invoke(t, "", "check", "app.kern", filepath.Join("lib", "lib.kern"))
	if discovered != named {
		t.Errorf("diagnostics differ between discovery and naming every file:\n%s\n%s", discovered, named)
	}
	if resp := response(t, discovered); len(resp.Diagnostics) == 0 || resp.Diagnostics[0].File != filepath.Join("lib", "lib.kern") {
		t.Errorf("the imported module's errors name its file: %s", discovered)
	}
}
