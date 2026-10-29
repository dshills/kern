package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/diag"
)

var versionRE = regexp.MustCompile(`"schema_version"\s*:\s*"0\.2"`)

// diagnosticsOf decodes a response's diagnostics as raw objects, so that
// the presence or absence of a key is visible.
func diagnosticsOf(t *testing.T, out string) (string, []map[string]any) {
	t.Helper()
	var resp struct {
		SchemaVersion string           `json:"schema_version"`
		Diagnostics   []map[string]any `json:"diagnostics"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("stdout is not one JSON response: %v\n%s", err, out)
	}
	return resp.SchemaVersion, resp.Diagnostics
}

// TestWireMessageVersions: a request at 0.2 is answered at 0.2 without
// messages, one at 0.3 and a command-line invocation at 0.3 with a
// non-empty message on every diagnostic, and an unknown version is refused
// at 0.3 (specs/diag-message/SPEC.md F4, F5, F12).
func TestWireMessageVersions(t *testing.T) {
	fixtures, _ := filepath.Glob(filepath.Join(testsRoot, "api", "*.json"))
	if len(fixtures) == 0 {
		t.Fatal("no api fixtures")
	}
	checked := 0
	for _, f := range fixtures {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var probe struct {
			SchemaVersion string   `json:"schema_version"`
			Command       string   `json:"command"`
			CLI           []string `json:"_cli"`
		}
		cmd := "check"
		if json.Unmarshal(b, &probe) == nil && probe.Command != "" && probe.Command != "frobnicate" {
			cmd = probe.Command
		}
		if probe.SchemaVersion != "0.2" {
			continue // a malformed or wrongly versioned fixture: its version is its point
		}
		req := string(b)
		checked++
		for _, v := range []string{"0.2", "0.3"} {
			r := versionRE.ReplaceAllString(req, `"schema_version": "`+v+`"`)
			_, out, _ := invoke(t, r, append([]string{cmd}, probe.CLI...)...)
			got, ds := diagnosticsOf(t, out)
			if got != v {
				t.Errorf("%s at %s: answered at %s", filepath.Base(f), v, got)
			}
			for _, d := range ds {
				msg, has := d["message"]
				if v == "0.2" && has {
					t.Errorf("%s at 0.2: diagnostic %v carries a message", filepath.Base(f), d["code"])
				}
				if v == "0.3" && (!has || msg == "") {
					t.Errorf("%s at 0.3: diagnostic %v has no message", filepath.Base(f), d["code"])
				}
			}
		}
	}
	if checked < 5 {
		t.Fatalf("only %d fixtures carried a 0.2 request", checked)
	}
	// a passing check and a format at 0.2 answer at 0.2; at 0.3, at 0.3
	hello := filepath.Join(testsRoot, "run", "hello.kern")
	for _, cmd := range []string{"check", "format", "emit-ir", "show-bounds-checks"} {
		for _, v := range []string{"0.2", "0.3"} {
			req := `{"schema_version":"` + v + `","command":"` + cmd + `","files":["` + hello + `"]}`
			code, out, _ := invoke(t, req, cmd)
			if got, _ := diagnosticsOf(t, out); code != 0 || got != v {
				t.Errorf("%s at %s: exit %d, answered at %s", cmd, v, code, got)
			}
		}
	}
	// a successful structured edit at 0.2 answers at 0.2 (the one success
	// writer that is not respond)
	src := filepath.Join(t.TempDir(), "e.kern")
	if err := os.WriteFile(src, []byte("module m\nfunc main() -> i32 { return 0i32 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	edit := `{"schema_version": "0.2","command":"apply-structured-edit","files":["` + src + `"],"edit":{"ops":[{"op":"insert","file":"` + src + `","at":0,"text":"// hi\n"}]}}`
	if code, out, _ := invoke(t, edit, "apply-structured-edit"); code != 0 {
		t.Errorf("apply-structured-edit at 0.2: exit %d %s", code, out)
	} else if got, _ := diagnosticsOf(t, out); got != "0.2" {
		t.Errorf("apply-structured-edit at 0.2 answered at %s", got)
	}
	// files on the command line: the newest version, with messages
	bad := filepath.Join(t.TempDir(), "uam.kern")
	if err := os.WriteFile(bad, []byte("module m\nfunc main() -> i32 {\n    v := Vec[u64].new()\n    w := v\n    v.push(1u64)\n    return 0i32\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, out, _ := invoke(t, "", "check", bad)
	got, ds := diagnosticsOf(t, out)
	if got != diag.SchemaVersion || len(ds) != 1 || ds[0]["code"] != "OWN_USE_AFTER_MOVE" || ds[0]["message"] != "v is used after being moved" {
		t.Errorf("command-line check: version %s, diagnostics %v", got, ds)
	}
	// an invocation at 0.2 leaves no trace in the next (no shared state)
	_, _, _ = invoke(t, `{"schema_version":"0.2","command":"check","files":["`+bad+`"]}`, "check")
	_, out, _ = invoke(t, "", "check", bad)
	if got, ds := diagnosticsOf(t, out); got != "0.3" || ds[0]["message"] == nil {
		t.Errorf("after a 0.2 request, a command-line check answered at %s with %v", got, ds)
	}
	// an unknown version
	_, out, _ = invoke(t, `{"schema_version":"0.4","command":"check","files":["x"]}`, "check")
	if got, ds := diagnosticsOf(t, out); got != "0.3" || ds[0]["code"] != "API_UNSUPPORTED_SCHEMA" || ds[0]["message"] == nil {
		t.Errorf("0.4: answered at %s with %v", got, ds)
	}
	// a failure before the version is known
	_, out, _ = invoke(t, `{"schema_version":"0.2","command":"check","files":["x"]} {"trailing":1}`, "check")
	if got, ds := diagnosticsOf(t, out); got != "0.3" || ds[0]["code"] != "API_MALFORMED_REQUEST" {
		t.Errorf("malformed: answered at %s with %v", got, ds)
	}
}

// TestHumanRenderingUnchanged pins the --human line of a rejecting fixture
// (specs/diag-message/SPEC.md F13).
func TestHumanRenderingUnchanged(t *testing.T) {
	code, out, _ := invoke(t, "", "check", "--human", filepath.Join(testsRoot, "negative", "ambiguous_source.kern"))
	want := "negative/ambiguous_source.kern:164-177: error [ACC_AMBIGUOUS_SOURCE rule 5.3.4]: function has 2 view/edit parameters; a returned access needs exactly one source\n"
	if code != 2 || !strings.HasSuffix(out, want) {
		t.Errorf("human rendering: exit %d\n%s", code, out)
	}
}
