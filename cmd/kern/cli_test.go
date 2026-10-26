package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/format"
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
		// the command under test is the request's own; malformed requests go to check
		cmd := "check"
		var probe struct {
			Command string   `json:"command"`
			CLI     []string `json:"_cli"` // extra command-line flags for the fixture
		}
		if json.Unmarshal(b, &probe) == nil && probe.Command != "" && probe.Command != "frobnicate" {
			cmd = probe.Command
		}
		code, resp, _ := runWith(t, append([]string{cmd}, probe.CLI...), string(b))
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

// formatResult decodes a `format` response result (§16).
func formatResult(t *testing.T, resp diag.Response) []FormattedFile {
	t.Helper()
	b, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Files []FormattedFile `json:"files"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("result is not a format result: %v", err)
	}
	return r.Files
}

func TestFormatCommand(t *testing.T) {
	ok := filepath.Join("..", "..", "tests", "negative", "use_after_move_ok.kern")
	bad := filepath.Join("..", "..", "tests", "negative", "syntax.kern")
	src, err := source.Load(ok)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := format.Format(src)

	// success: convenience form
	code, resp, _ := runWith(t, []string{"format", ok}, "")
	if code != 0 || !resp.OK || len(resp.Diagnostics) != 0 {
		t.Fatalf("format: exit %d resp %+v", code, resp)
	}
	files := formatResult(t, resp)
	if len(files) != 1 || files[0].File != ok || files[0].Formatted != string(want) || !strings.HasSuffix(files[0].Formatted, "\n") {
		t.Fatalf("unexpected result %+v", files)
	}

	// success: machine-API request on stdin
	req := fmt.Sprintf(`{"schema_version":%q,"command":"format","files":[%q]}`, diag.SchemaVersion, ok)
	if code, resp, _ := runWith(t, []string{"format"}, req); code != 0 || !resp.OK || len(formatResult(t, resp)) != 1 {
		t.Fatalf("format via stdin: exit %d resp %+v", code, resp)
	}

	// failure: a file that does not parse
	code, resp, _ = runWith(t, []string{"format", bad}, "")
	if code != 2 || resp.OK || resp.Result != nil || len(resp.Diagnostics) != 1 || resp.Diagnostics[0].Code != "SYNTAX" {
		t.Fatalf("unparseable file: exit %d resp %+v", code, resp)
	}
	// failure: one bad file fails the whole request
	code, resp, _ = runWith(t, []string{"format", ok, bad}, "")
	if code != 2 || resp.OK || resp.Result != nil || len(resp.Diagnostics) != 1 || resp.Diagnostics[0].Code != "SYNTAX" {
		t.Fatalf("mixed request: exit %d resp %+v", code, resp)
	}
	// request/command mismatch
	req = fmt.Sprintf(`{"schema_version":%q,"command":"check","files":[%q]}`, diag.SchemaVersion, ok)
	if code, resp, _ := runWith(t, []string{"format"}, req); code != 2 || resp.Diagnostics[0].Code != "API_UNKNOWN_COMMAND" {
		t.Fatalf("mismatched command: exit %d resp %+v", code, resp)
	}
	// --human writes the formatted source itself
	var out, errb bytes.Buffer
	if code := run([]string{"format", "--human", ok}, strings.NewReader(""), &out, &errb); code != 0 || out.String() != string(want) {
		t.Fatalf("human: exit %d out %q", code, out.String())
	}
}

func TestFormatInternalError(t *testing.T) {
	saved := formatHook
	formatHook = func([]*source.File) ([]FormattedFile, []diag.Diagnostic, error) {
		return nil, nil, &InternalError{Err: errors.New("injected")}
	}
	defer func() { formatHook = saved }()
	ok := filepath.Join("..", "..", "tests", "negative", "use_after_move_ok.kern")
	code, resp, stderr := runWith(t, []string{"format", ok}, "")
	if code != 3 || resp.OK || len(resp.Diagnostics) != 1 || resp.Diagnostics[0].Code != "INTERNAL" {
		t.Fatalf("internal: exit %d resp %+v", code, resp)
	}
	if !strings.Contains(stderr, "injected") {
		t.Fatalf("stderr log missing wrapped error: %q", stderr)
	}
}

func TestRunEmitExplain(t *testing.T) {
	hello := filepath.Join("..", "..", "tests", "run", "hello.kern")
	var out, errb bytes.Buffer
	if code := run([]string{"run", hello}, strings.NewReader(""), &out, &errb); code != 0 || out.String() != "hello, kern\n7\n" {
		t.Fatalf("run: exit %d out %q err %q", code, out.String(), errb.String())
	}
	trap := filepath.Join("..", "..", "tests", "trap", "bounds.kern")
	out.Reset()
	errb.Reset()
	if code := run([]string{"run", trap}, strings.NewReader(""), &out, &errb); code != 134 || !strings.Contains(errb.String(), `"kind":"bounds"`) {
		t.Fatalf("trap: exit %d err %q", code, errb.String())
	}
	if code, resp, _ := runWith(t, []string{"run", "--opt=fast", hello}, ""); code != 2 || resp.Diagnostics[0].Code != "CLI_INVALID_FLAGS" {
		t.Fatalf("bad opt: exit %d resp %+v", code, resp)
	}
	if code, resp, _ := runWith(t, []string{"run", "--env=freestanding", hello}, ""); code != 2 || resp.Diagnostics[0].Code != "CLI_INVALID_FLAGS" {
		t.Fatalf("freestanding before Phase 3: exit %d resp %+v", code, resp)
	}
	if code, resp, _ := runWith(t, []string{"run", "--test-hooks", "--opt=release", hello}, ""); code != 2 || resp.Diagnostics[0].Code != "CLI_INVALID_FLAGS" {
		t.Fatalf("test-hooks in release: exit %d resp %+v", code, resp)
	}
	bad := filepath.Join("..", "..", "tests", "negative", "use_after_move.kern")
	if code, resp, _ := runWith(t, []string{"run", bad}, ""); code != 2 || resp.OK || resp.Diagnostics[0].Code != "OWN_USE_AFTER_MOVE" {
		t.Fatalf("run of rejected program: exit %d resp %+v", code, resp)
	}
	code, resp, _ := runWith(t, []string{"emit-ir", hello}, "")
	if code != 0 || !resp.OK {
		t.Fatalf("emit-ir: exit %d resp %+v", code, resp)
	}
	if irText, _ := resp.Result.(map[string]any)["ir"].(string); !strings.Contains(irText, "call intrinsic Vec.push") {
		t.Fatalf("emit-ir result lacks IR: %v", resp.Result)
	}
	src, _ := os.ReadFile(hello)
	req := fmt.Sprintf(`{"schema_version":"0.2","command":"explain-ownership","files":[%q],"span":{"file":%q,"start":0,"end":%d}}`, hello, hello, len(src))
	code, resp, _ = runWith(t, []string{"explain-ownership"}, req)
	if code != 0 || !resp.OK {
		t.Fatalf("explain-ownership: exit %d resp %+v", code, resp)
	}
	places, _ := resp.Result.(map[string]any)["places"].([]any)
	if len(places) == 0 {
		t.Fatalf("explain-ownership returned no places: %v", resp.Result)
	}
	found := false
	for _, p := range places {
		pm := p.(map[string]any)
		if pm["state"] == "owned" && pm["drop_at"] != nil {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an owned place with a drop point: %v", places)
	}
}

// TestPhase2Commands runs the §16 analysis commands of PLAN.md 2.5 on the
// vector example and checks their result shapes, labels and target handling.
func TestPhase2Commands(t *testing.T) {
	vec := filepath.Join("..", "..", "examples", "vector", "vector.kern")
	pool := filepath.Join("..", "..", "tests", "run", "pool_handles.kern")
	code, resp, _ := runWith(t, []string{"show-layout", vec}, "")
	if code != 0 || !resp.OK {
		t.Fatalf("show-layout: exit %d resp %+v", code, resp)
	}
	res := resp.Result.(map[string]any)
	if res["target"] == "" || res["target"] == nil {
		t.Fatalf("show-layout result lacks target: %v", res)
	}
	types, _ := res["types"].([]any)
	if len(types) == 0 {
		t.Fatalf("show-layout returned no types: %v", res)
	}
	for _, ty := range types {
		tm := ty.(map[string]any)
		if tm["size"].(float64) < 0 || tm["align"].(float64) < 1 {
			t.Fatalf("bad layout %v", tm)
		}
	}
	code, resp, _ = runWith(t, []string{"show-copies", "--target=x86_64-unknown-linux-gnu", vec}, "")
	if code != 0 || !resp.OK || resp.Result.(map[string]any)["target"] != "x86_64-unknown-linux-gnu" {
		t.Fatalf("show-copies: exit %d resp %+v", code, resp)
	}
	for _, c := range resp.Result.(map[string]any)["copies"].([]any) {
		cm := c.(map[string]any)
		if cm["label"] != "proven" || (cm["kind"] != "bitwise" && cm["kind"] != "explicit-deep") {
			t.Fatalf("bad copy %v", cm)
		}
	}
	code, resp, _ = runWith(t, []string{"show-allocations", vec}, "")
	if code != 0 || !resp.OK {
		t.Fatalf("show-allocations: exit %d resp %+v", code, resp)
	}
	sites, _ := resp.Result.(map[string]any)["sites"].([]any)
	if len(sites) == 0 {
		t.Fatalf("show-allocations found no sites in the vector example")
	}
	for _, s := range sites {
		sm := s.(map[string]any)
		if sm["allocator"] != "system" || (sm["label"] == "proven") != (sm["size"] != nil) {
			t.Fatalf("bad site %v", sm)
		}
	}
	code, resp, _ = runWith(t, []string{"show-bounds-checks", pool}, "")
	if code != 0 || !resp.OK {
		t.Fatalf("show-bounds-checks: exit %d resp %+v", code, resp)
	}
	kinds := map[string]bool{}
	for _, c := range resp.Result.(map[string]any)["checks"].([]any) {
		cm := c.(map[string]any)
		kinds[cm["kind"].(string)] = true
		if (cm["label"] == "proven") != (cm["proven_by"] != nil) {
			t.Fatalf("proven_by must be set iff proven: %v", cm)
		}
	}
	if !kinds["generation"] || !kinds["disjoint"] {
		t.Fatalf("expected generation and disjoint checks, got %v", kinds)
	}
	code, resp, _ = runWith(t, []string{"show-bounds-checks", vec}, "")
	if code != 0 || !resp.OK {
		t.Fatalf("show-bounds-checks: exit %d resp %+v", code, resp)
	}
	kinds = map[string]bool{}
	for _, c := range resp.Result.(map[string]any)["checks"].([]any) {
		kinds[c.(map[string]any)["kind"].(string)] = true
	}
	if !kinds["overflow"] || !kinds["bounds"] {
		t.Fatalf("expected overflow and bounds checks in the vector example, got %v", kinds)
	}
	req := fmt.Sprintf(`{"schema_version":"0.2","command":"show-layout","files":[%q],"target":"aarch64-apple-macosx"}`, vec)
	if code, resp, _ := runWith(t, []string{"show-layout", "--target=x86_64-unknown-linux-gnu"}, req); code != 2 || resp.Diagnostics[0].Code != "API_TARGET_CONFLICT" {
		t.Fatalf("target conflict: exit %d resp %+v", code, resp)
	}
	if code, resp, _ := runWith(t, []string{"show-layout", "--target=aarch64-apple-macosx"}, req); code != 0 || !resp.OK {
		t.Fatalf("agreeing targets: exit %d resp %+v", code, resp)
	}
	if _, err := exec.LookPath("clang"); err == nil {
		hello := filepath.Join("..", "..", "tests", "run", "hello.kern")
		code, resp, _ := runWith(t, []string{"emit-asm", hello}, "")
		if code != 0 || !resp.OK {
			t.Fatalf("emit-asm: exit %d resp %+v", code, resp)
		}
		if asm, _ := resp.Result.(map[string]any)["asm"].(string); !strings.Contains(asm, "run.main") {
			t.Fatalf("emit-asm result lacks the entry symbol")
		}
	}
}
