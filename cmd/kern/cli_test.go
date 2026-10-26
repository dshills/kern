package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/source"
)

func runWith(t *testing.T, args []string, stdin string) (int, diag.Response, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, strings.NewReader(stdin), &out, &errb)
	var resp diag.Response
	if out.Len() > 0 {
		if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
			t.Fatalf("stdout is not one JSON response: %v\n%s", err, out.String())
		}
		if n := bytes.Count(bytes.TrimSpace(out.Bytes()), []byte("\n")); n != 0 {
			t.Fatalf("stdout carries more than one JSON document")
		}
	}
	return code, resp, errb.String()
}

// TestAPIFixtures runs every request fixture under tests/api and expects the
// diagnostic named by the file stem with exit code 2 and ok=false.
func TestAPIFixtures(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("..", "..", "tests", "api", "*.json"))
	if len(files) == 0 {
		t.Fatal("no api fixtures")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		want := strings.ToUpper(strings.TrimSuffix(filepath.Base(f), ".json"))
		code, resp, _ := runWith(t, []string{"check"}, string(b))
		if code != 2 || resp.OK || len(resp.Diagnostics) != 1 || string(resp.Diagnostics[0].Code) != want {
			t.Errorf("%s: exit %d, resp %+v", f, code, resp)
		}
		if resp.SchemaVersion != diag.SchemaVersion {
			t.Errorf("%s: schema_version %q", f, resp.SchemaVersion)
		}
	}
}

func TestCLIContracts(t *testing.T) {
	ok := filepath.Join("..", "..", "tests", "negative", "use_after_move_ok.kern")
	bad := filepath.Join("..", "..", "tests", "negative", "use_after_move.kern")
	if code, resp, _ := runWith(t, []string{"check", ok}, ""); code != 0 || !resp.OK || len(resp.Diagnostics) != 0 {
		t.Fatalf("accepting file: exit %d resp %+v", code, resp)
	}
	if code, resp, _ := runWith(t, []string{"check", bad}, ""); code != 2 || resp.OK || len(resp.Diagnostics) != 1 || resp.Diagnostics[0].Code != "OWN_USE_AFTER_MOVE" {
		t.Fatalf("rejecting file: exit %d resp %+v", code, resp)
	}
	if code, resp, _ := runWith(t, []string{"check", "--bogus", ok}, ""); code != 2 || resp.Diagnostics[0].Code != "CLI_INVALID_FLAGS" {
		t.Fatalf("bad flag: exit %d resp %+v", code, resp)
	}
	if code, _, _ := runWith(t, nil, ""); code != 2 {
		t.Fatalf("no command: exit %d", code)
	}
	if code, resp, _ := runWith(t, []string{"check"}, `{"schema_version":"0.2","command":"check","files":["x"]} {"trailing":1}`); code != 2 || resp.Diagnostics[0].Code != "API_MALFORMED_REQUEST" {
		t.Fatalf("trailing data: exit %d resp %+v", code, resp)
	}
	if code, resp, _ := runWith(t, []string{"frobnicate"}, ""); code != 2 || resp.Diagnostics[0].Code != "API_UNKNOWN_COMMAND" {
		t.Fatalf("unknown command before stdin: exit %d resp %+v", code, resp)
	}
}

func TestInternalError(t *testing.T) {
	saved := checkHook
	checkHook = func([]*source.File) ([]diag.Diagnostic, error) {
		return nil, &InternalError{Err: errors.New("injected")}
	}
	defer func() { checkHook = saved }()
	ok := filepath.Join("..", "..", "tests", "negative", "use_after_move_ok.kern")
	code, resp, stderr := runWith(t, []string{"check", ok}, "")
	if code != 3 || resp.OK || len(resp.Diagnostics) != 1 || resp.Diagnostics[0].Code != "INTERNAL" {
		t.Fatalf("internal: exit %d resp %+v", code, resp)
	}
	if !strings.Contains(stderr, "injected") {
		t.Fatalf("stderr log missing wrapped error: %q", stderr)
	}
}
