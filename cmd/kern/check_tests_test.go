package main

import (
	"strings"
	"testing"
)

// TestCheckLoadsTestFiles: kern check loads the program's test files as
// kern test does and reports their errors, the same with --cache; a
// project without test files is checked as before (specs/editor-nav R5).
func TestCheckLoadsTestFiles(t *testing.T) {
	inProject(t, map[string]string{
		"kern.mod":          `{"entry": "app.kern"}`,
		"app.kern":          "module app\nfunc twice(x: u64) -> u64 { return x * 2u64 }\nfunc main() -> i32 { return 0i32 }\n",
		"app_test.kern":     "module app\nfunc test_twice() { expect(twice(2u64) == 4u64) }\n",
		"app_bad_test.kern": "module app\nfunc test_param(x: u64) { expect(nope) }\n",
	})
	codes := func(out string) string {
		var cs []string
		for _, d := range response(t, out).Diagnostics {
			cs = append(cs, string(d.Code)+"@"+d.File)
		}
		return strings.Join(cs, " ")
	}
	want := "TEST_SIGNATURE@app_bad_test.kern NAME_UNRESOLVED@app_bad_test.kern" // sorted by position
	code, out, _ := invoke(t, "", "check")
	if code != 2 || codes(out) != want {
		t.Errorf("check: exit %d, %s, want %s\n%s", code, codes(out), want, out)
	}
	cache := t.TempDir()
	for i := 0; i < 2; i++ { // a miss, then a hit
		code, out, _ = invoke(t, "", "check", "--cache="+cache)
		if code != 2 || codes(out) != want {
			t.Errorf("check --cache (run %d): exit %d, %s, want %s", i+1, code, codes(out), want)
		}
	}
	// the files of the result include the test files
	if files := response(t, out).Result; !strings.Contains(strings.Join(anyStrings(files), " "), "app_test.kern") {
		t.Errorf("result files %v lack the test file", files)
	}
}

func TestCheckWithoutTestFilesUnchanged(t *testing.T) {
	inProject(t, map[string]string{
		"kern.mod":   `{"entry": "app.kern"}`,
		"app.kern":   "module app\nimport \"lib\"\nfunc main() -> i32 { return 0i32 }\n",
		"lib/l.kern": "module lib\nfunc F() -> u64 { return 1u64 }\n",
	})
	code, out, _ := invoke(t, "", "check")
	if want := `{"schema_version":"0.3","ok":true,"result":{"files":["app.kern","lib/l.kern"]},"diagnostics":[]}` + "\n"; code != 0 || out != want {
		t.Errorf("check: exit %d\n%s\nwant\n%s", code, out, want)
	}
}

// anyStrings flattens the files of a decoded check result.
func anyStrings(result any) []string {
	m, _ := result.(map[string]any)
	fs, _ := m["files"].([]any)
	var out []string
	for _, f := range fs {
		s, _ := f.(string)
		out = append(out, s)
	}
	return out
}
