package project

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// tree writes files (relative to dir) and returns dir.
func tree(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func codes(r Result) string {
	var out []string
	for _, d := range r.Diags {
		out = append(out, string(d.Code))
	}
	return strings.Join(out, ",")
}

const app = "module app\nimport \"lib\"\nfunc main() -> i32 { return 0i32 }\n"
const lib = "module lib\nfunc F() -> u64 { return 1u64 }\n"

func TestFindRoot(t *testing.T) {
	cwd := tree(t, t.TempDir(), map[string]string{
		"proj/kern.mod":            "",
		"proj/sub/deep/app.kern":   app,
		"proj/sub/deep/lib/x.kern": lib,
		"loose/app.kern":           app,
	})
	if root, m, ok := FindRoot(cwd, []string{"proj/sub/deep/app.kern"}); !ok || root != "proj" || m != filepath.Join("proj", "kern.mod") {
		t.Errorf("ancestor root: %q %q %v", root, m, ok)
	}
	if root, _, ok := FindRoot(cwd, []string{"loose/app.kern"}); ok || root != "." {
		t.Errorf("fallback root: %q %v", root, ok)
	}
	if root, _, ok := FindRoot(filepath.Join(cwd, "proj", "sub"), nil); !ok || root != ".." {
		t.Errorf("walk up from cwd: %q %v", root, ok)
	}
	if _, _, ok := FindRoot(filepath.Join(cwd, "loose"), nil); ok {
		t.Errorf("no manifest anywhere must not be found")
	}
	abs := filepath.Join(cwd, "proj", "sub", "deep", "app.kern")
	if root, _, ok := FindRoot(cwd, []string{abs}); !ok || root != filepath.Join(cwd, "proj") {
		t.Errorf("absolute named file keeps an absolute root: %q %v", root, ok)
	}
	// a relative working directory still walks above itself
	t.Chdir(filepath.Join(cwd, "proj", "sub"))
	if root, _, ok := FindRoot(".", nil); !ok || root != ".." {
		t.Errorf("relative cwd: %q %v", root, ok)
	}
	if root, _, ok := FindRoot("deep", []string{"app.kern"}); !ok || root != filepath.Join("..", "..") {
		t.Errorf("relative cwd with a named file: %q %v", root, ok)
	}
	// a kern.mod that is a directory is not a manifest
	bad := tree(t, t.TempDir(), map[string]string{"kern.mod/x": "", "app.kern": app})
	if _, _, ok := FindRoot(bad, []string{"app.kern"}); ok {
		t.Errorf("a directory named kern.mod is not a manifest")
	}
}

func TestParseManifest(t *testing.T) {
	ok := map[string]bool{"": true, " \n\t": true, "{}": true, `{"entry": "a.kern"}`: true, `{"entry": ["a.kern", "b/c.kern"], "opt": "release", "env": "freestanding", "target": "aarch64-unknown-none", "output": "out/prog", "schema_version": "0.2"}`: true,
		`{"opt": "debug", "opt": "release"}`: true}
	for src := range ok {
		if m, ds := ParseManifest("kern.mod", []byte(src)); len(ds) > 0 || m == nil {
			t.Errorf("%q: unexpected %v", src, ds)
		}
	}
	if m, _ := ParseManifest("kern.mod", []byte(`{"opt": "fast", "opt": "release"}`)); m == nil || m.Opt == nil || *m.Opt != "release" {
		t.Errorf("the last occurrence of a field is the one validated and kept: %+v", m)
	}
	bad := map[string]string{
		`{"optx": 1}`:                  "optx",
		`{"opt": 1}`:                   "\"opt\" must be a string",
		`{"opt": null}`:                "\"opt\" must be a string",
		`{"opt": "fast"}`:              "\"opt\" must be",
		`{"env": "bare"}`:              "\"env\" must be",
		`{"entry": "../x.kern"}`:       "\"entry\"",
		`{"entry": "/x.kern"}`:         "\"entry\"",
		`{"entry": []}`:                "at least one",
		`{"entry": 3}`:                 "\"entry\"",
		`{"entry": null}`:              "\"entry\"",
		`{"schema_version": "0.1"}`:    "\"schema_version\"",
		`{"output": "/abs"}`:           "\"output\"",
		`{"entry": "a.kern"} x`:        "invalid character",
		`{`:                            "unexpected",
		`[1]`:                          "must hold a JSON object",
		`null`:                         "must hold a JSON object",
		`"s"`:                          "must hold a JSON object",
		`{"entry": "a.kern"} {"a": 1}`: "trailing",
	}
	for src, want := range bad {
		m, ds := ParseManifest("kern.mod", []byte(src))
		if m != nil || len(ds) != 1 || ds[0].Code != "PROJECT_INVALID" {
			t.Errorf("%q: want one PROJECT_INVALID, got %+v %v", src, m, ds)
			continue
		}
		if !strings.Contains(ds[0].Message, want) {
			t.Errorf("%q: message %q lacks %q", src, ds[0].Message, want)
		}
	}
}

func TestValidImportPath(t *testing.T) {
	for p, want := range map[string]bool{"lib": true, "lib/inner": true, "a.b": true, "": false, "/lib": false, "lib/": false, "./lib": false, "lib/../x": false, "a//b": false, "std/str": false, "a\\b": false, "c:lib": false, "a\x01b": false, "a\x7fb": false} {
		if got := ValidImportPath(p); got != want {
			t.Errorf("ValidImportPath(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestLoadDiscovers(t *testing.T) {
	cwd := tree(t, t.TempDir(), map[string]string{
		"app.kern":         "module app\nimport \"lib\"\nimport \"lib/inner\"\nfunc main() -> i32 { return 0i32 }\n",
		"lib/b.kern":       "module lib\nimport \"lib/inner\"\n",
		"lib/a.kern":       "module lib\nimport \"util\"\n",
		"lib/README.md":    "not a module file",
		"lib/sub/x.kern":   "module sub\n",                   // a subdirectory is not part of lib
		"lib/inner/i.kern": "module inner\nimport \"lib\"\n", // a cycle
		"util/u.kern":      "module util\nimport \"std/str\"\n",
	})
	r := Load(cwd, []string{"app.kern"})
	if codes(r) != "" {
		t.Fatalf("diagnostics: %v", r.Diags)
	}
	want := []string{"app.kern", "lib/a.kern", "lib/b.kern", "lib/inner/i.kern", "util/u.kern"}
	if got := strings.Join(r.Names(), " "); got != strings.Join(want, " ") {
		t.Errorf("files %v, want %v", r.Names(), want)
	}
	if r.Entry != 1 || r.Root != "." || r.Manifest != nil {
		t.Errorf("entry %d root %q manifest %v", r.Entry, r.Root, r.Manifest)
	}
	// naming every file adds nothing, whatever the spelling, and the
	// entry files keep their given names
	all := Load(cwd, []string{"app.kern", "lib/a.kern", "./lib//b.kern", "lib/inner/i.kern", "util/u.kern"})
	if codes(all) != "" || len(all.Files) != 5 || all.Entry != 5 || all.Files[2].Name != "./lib//b.kern" {
		t.Errorf("fully named: %v %v entry=%d", codes(all), all.Names(), all.Entry)
	}
	// a file named twice is read twice; a file both named and imported once
	twice := Load(cwd, []string{"app.kern", "lib/a.kern", "lib/a.kern"})
	if n := strings.Count(strings.Join(twice.Names(), " "), "a.kern"); n != 2 || len(twice.Files) != 6 {
		t.Errorf("named twice: %v", twice.Names())
	}
	// an entry file's directory imported by another module: the siblings
	// join, the named file keeps its name
	entryInLib := Load(cwd, []string{"lib/a.kern"})
	if codes(entryInLib) != "" || strings.Join(entryInLib.Names(), " ") != "lib/a.kern util/u.kern" {
		t.Errorf("entry in a module directory: %v %v", codes(entryInLib), entryInLib.Names())
	}
	fromInner := Load(cwd, []string{"lib/inner/i.kern"})
	if codes(fromInner) != "" || strings.Join(fromInner.Names(), " ") != "lib/inner/i.kern lib/a.kern lib/b.kern util/u.kern" {
		t.Errorf("entry imported back: %v %v", codes(fromInner), fromInner.Names())
	}
}

func TestLoadFromProjectRoot(t *testing.T) {
	cwd := tree(t, t.TempDir(), map[string]string{
		"proj/kern.mod":     `{"entry": "src/app.kern", "opt": "release"}`,
		"proj/src/app.kern": "module app\nimport \"lib\"\n",
		"proj/lib/lib.kern": lib,
	})
	// from the file: the root is found by walking up, imports resolve under it
	r := Load(cwd, []string{"proj/src/app.kern"})
	if codes(r) != "" || strings.Join(r.Names(), " ") != "proj/src/app.kern proj/lib/lib.kern" || r.Root != "proj" || r.Manifest == nil || *r.Manifest.Opt != "release" {
		t.Errorf("from file: %v %v root=%q manifest=%v", codes(r), r.Names(), r.Root, r.Manifest)
	}
	// from the project directory with no file: the manifest's entry
	r = Load(filepath.Join(cwd, "proj"), nil)
	if codes(r) != "" || strings.Join(r.Names(), " ") != "src/app.kern lib/lib.kern" || r.Entry != 1 {
		t.Errorf("from root: %v %v entry=%d", codes(r), r.Names(), r.Entry)
	}
	// from a subdirectory with no file: walk up
	r = Load(filepath.Join(cwd, "proj", "lib"), nil)
	if codes(r) != "" || strings.Join(r.Names(), " ") != "../src/app.kern ../lib/lib.kern" {
		t.Errorf("from subdirectory: %v %v", codes(r), r.Names())
	}
	// an array entry
	tree(t, cwd, map[string]string{"proj2/kern.mod": `{"entry": ["a.kern", "b.kern"]}`, "proj2/a.kern": "module m\n", "proj2/b.kern": "module m\n"})
	r = Load(filepath.Join(cwd, "proj2"), nil)
	if codes(r) != "" || strings.Join(r.Names(), " ") != "a.kern b.kern" || r.Entry != 2 {
		t.Errorf("array entry: %v %v", codes(r), r.Names())
	}
	// no file, no manifest; a manifest without entry
	if r := Load(filepath.Join(cwd, "proj", "src"), []string{}); codes(r) != "" {
		t.Errorf("walk up from src: %v", codes(r))
	}
	if r := Load(t.TempDir(), nil); codes(r) != "PROJECT_NOT_FOUND" {
		t.Errorf("no manifest: %v", codes(r))
	}
	tree(t, cwd, map[string]string{"proj3/kern.mod": "{}"})
	if r := Load(filepath.Join(cwd, "proj3"), nil); codes(r) != "PROJECT_INVALID" || !strings.Contains(r.Diags[0].Message, "entry") {
		t.Errorf("manifest without entry: %v", r.Diags)
	}
	tree(t, cwd, map[string]string{"proj4/kern.mod": "{", "proj4/app.kern": "module app\n"})
	if r := Load(cwd, []string{"proj4/app.kern"}); codes(r) != "PROJECT_INVALID" {
		t.Errorf("invalid manifest on the walk: %v", r.Diags)
	}
	if r := Load(cwd, []string{"proj/src/missing.kern"}); codes(r) != "API_FILE_NOT_FOUND" {
		t.Errorf("missing named file: %v", codes(r))
	}
}

func TestLoadFailures(t *testing.T) {
	cwd := tree(t, t.TempDir(), map[string]string{
		"app.kern":        "module app\nimport \"nowhere\"\nimport \"../up\"\nimport \"empty\"\nimport \"wrong\"\nimport \"nowhere\"\n",
		"empty/notes.txt": "",
		"wrong/w.kern":    "module other\n",
		"other.kern":      "module other\nimport \"nowhere\"\n",
		"bad/b.kern":      "module bad\nimport \"wrong\" import \"x\"\n", // a malformed prologue: no import is followed
		"up/u.kern":       "module up\n",
	})
	r := Load(cwd, []string{"app.kern", "other.kern"})
	if got := codes(r); got != "MODULE_NOT_FOUND,MODULE_NOT_FOUND,MODULE_NOT_FOUND,MODULE_NOT_FOUND,MODULE_NOT_FOUND,MODULE_MISMATCH" {
		t.Fatalf("codes %s\n%v", got, r.Diags)
	}
	// the missing module is reported at each import naming it, in each file
	if r.Diags[0].File != "app.kern" || r.Diags[0].Span.Start == 0 || r.Diags[3].File != "app.kern" || r.Diags[4].File != "other.kern" {
		t.Errorf("locations: %v", r.Diags)
	}
	for i, want := range []string{"nowhere", "not a relative module path", "no .kern file", "nowhere", "nowhere"} {
		if !strings.Contains(r.Diags[i].Message, want) {
			t.Errorf("diag %d: %q lacks %q", i, r.Diags[i].Message, want)
		}
	}
	if m := r.Diags[5]; m.File != "wrong/w.kern" || !strings.Contains(m.Message, `module "other", expected "wrong"`) {
		t.Errorf("mismatch: %+v", m)
	}
	// the wrong module's file is still in the set, so later stages see it
	if strings.Join(r.Names(), " ") != "app.kern other.kern wrong/w.kern" {
		t.Errorf("files %v", r.Names())
	}
	// a prologue syntax error: SYNTAX, no imports followed, discovery continues
	r = Load(cwd, []string{"bad/b.kern", "app.kern"})
	if got := codes(r); !strings.HasPrefix(got, "SYNTAX,MODULE_NOT_FOUND") {
		t.Errorf("syntax then discovery: %s", got)
	}
	// an unreadable module file
	if err := os.Chmod(filepath.Join(cwd, "wrong", "w.kern"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(cwd, "wrong", "w.kern"), 0o644) })
	r = Load(cwd, []string{"other.kern"})
	if os.Getuid() != 0 && runtime.GOOS != "windows" { // root reads anything; Windows keeps the file readable
		w := Load(cwd, []string{"app.kern"})
		found := false
		for _, d := range w.Diags {
			if d.Code == "MODULE_NOT_FOUND" && strings.Contains(d.Message, "cannot read wrong/w.kern") {
				found = true
			}
		}
		if !found {
			t.Errorf("unreadable module file not reported: %v", w.Diags)
		}
	}
	_ = r
}

func TestNamedFileInImportedDirectory(t *testing.T) {
	cwd := tree(t, t.TempDir(), map[string]string{
		"app.kern":   "module app\nimport \"lib\"\n",
		"lib/x.kern": "module other\n",
		"lib/y.kern": "module lib\n",
	})
	// named or discovered, a file of an imported directory must declare the alias
	named := Load(cwd, []string{"app.kern", "lib/x.kern"})
	found := Load(cwd, []string{"app.kern"})
	if codes(named) != "MODULE_MISMATCH" || codes(found) != "MODULE_MISMATCH" || named.Diags[0].File != "lib/x.kern" {
		t.Errorf("named %v, discovered %v", named.Diags, found.Diags)
	}
	// Entries stops after the entry files, whatever the imports
	e := Entries(cwd, []string{"app.kern"})
	if codes(e) != "" || len(e.Files) != 1 {
		t.Errorf("entries: %v %v", codes(e), e.Names())
	}
}

func TestAbsoluteEntryWithManifest(t *testing.T) {
	cwd := tree(t, t.TempDir(), map[string]string{
		"proj/kern.mod":     `{"opt": "release"}`,
		"proj/app.kern":     "module app\nimport \"lib\"\n",
		"proj/lib/lib.kern": lib,
	})
	r := Load(cwd, []string{filepath.Join(cwd, "proj", "app.kern")})
	if codes(r) != "" || r.Manifest == nil || r.Root != filepath.Join(cwd, "proj") || r.Files[1].Name != filepath.Join(cwd, "proj", "lib", "lib.kern") {
		t.Errorf("absolute entry: %v root=%q manifest=%v files=%v", codes(r), r.Root, r.Manifest, r.Names())
	}
}
