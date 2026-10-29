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
			SchemaVersion string   `json:"schema_version"`
			Command       string   `json:"command"`
			CLI           []string `json:"_cli"` // extra command-line flags for the fixture
		}
		if json.Unmarshal(b, &probe) == nil && probe.Command != "" && probe.Command != "frobnicate" {
			cmd = probe.Command
		}
		code, resp, _ := runWith(t, append([]string{cmd}, probe.CLI...), string(b))
		if code != 2 || resp.OK || len(resp.Diagnostics) != 1 || string(resp.Diagnostics[0].Code) != want {
			t.Errorf("%s: exit %d, resp %+v", f, code, resp)
		}
		// a request at a known version is answered at that version; one whose
		// version is unknown or unreadable at the newest
		wantVersion := diag.SchemaVersion
		if probe.SchemaVersion == "0.2" && want != "API_UNSUPPORTED_SCHEMA" && want != "API_MALFORMED_REQUEST" {
			wantVersion = "0.2"
		}
		if resp.SchemaVersion != wantVersion {
			t.Errorf("%s: schema_version %q, want %q", f, resp.SchemaVersion, wantVersion)
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
	out.Reset()
	errb.Reset()
	// the freestanding emulation has no allocator, so no containers (rule 6.8):
	// a console-only program
	fsHello := filepath.Join("..", "..", "tests", "freestanding", "hello.kern")
	if code := run([]string{"run", "--env=freestanding", fsHello}, strings.NewReader(""), &out, &errb); code != 7 || !strings.HasSuffix(out.String(), "{\"halt\":{\"code\":7}}\n") || errb.Len() != 0 {
		t.Fatalf("freestanding emulation: exit %d out %q err %q", code, out.String(), errb.String())
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
	if irText, _ := resp.Result.(map[string]any)["ir"].(string); !strings.Contains(irText, "call Vec.push[T=u64]") {
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

// TestStructuredEdit covers §16 application semantics: rename expansion of
// a local, a function and a type, insert placement relative to replacements,
// the formatted flag, and the precedence of EDIT_INVALID_SPAN over
// EDIT_OVERLAP on the lowest failing op index.
func TestStructuredEdit(t *testing.T) {
	dir := t.TempDir()
	src := "module m\nstruct Cell { v: u64 }\nfunc twice(c: Cell) -> u64 { return c.v + c.v }\nfunc main() -> i32 {\n    total := 1u64\n    total = twice(Cell { v: total })\n    return i32(total)\n}\n"
	path := filepath.Join(dir, "m.kern")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	apply := func(ops string) (int, diag.Response) {
		req := fmt.Sprintf(`{"schema_version":"0.2","command":"apply-structured-edit","files":[%q],"edit":{"ops":[%s]}}`, path, ops)
		code, resp, _ := runWith(t, []string{"apply-structured-edit"}, req)
		return code, resp
	}
	span := func(s string) string {
		i := strings.Index(src, s)
		return fmt.Sprintf(`{"start":%d,"end":%d}`, i, i+len(s))
	}
	// rename the local `total` (declaration at its first occurrence), the function and the type in one request
	code, resp := apply(fmt.Sprintf(`{"op":"rename_symbol","file":%q,"span":%s,"new_name":"sum"},{"op":"rename_symbol","file":%q,"span":%s,"new_name":"double"},{"op":"rename_symbol","file":%q,"span":%s,"new_name":"Box2"}`, path, span("total"), path, span("twice"), path, span("Cell")))
	if code != 0 || !resp.OK {
		t.Fatalf("rename: exit %d resp %+v", code, resp)
	}
	res := resp.Result.(map[string]any)
	files := res["files"].([]any)
	got := files[0].(map[string]any)["source"].(string)
	want := "module m\nstruct Box2 { v: u64 }\nfunc double(c: Box2) -> u64 { return c.v + c.v }\nfunc main() -> i32 {\n    sum := 1u64\n    sum = double(Box2 { v: sum })\n    return i32(sum)\n}\n"
	if got != want {
		t.Fatalf("rename result:\n%s\n--- want\n%s", got, want)
	}
	if res["applied"].(float64) != 3 || len(resp.Diagnostics) != 0 || files[0].(map[string]any)["formatted"] != true {
		t.Fatalf("rename metadata: %+v diags %v", res, resp.Diagnostics)
	}
	// insert placement: an insert at k goes before a replacement starting at k and after one ending at k
	k := strings.Index(src, "1u64")
	code, resp = apply(fmt.Sprintf(`{"op":"replace_span","file":%q,"span":{"start":%d,"end":%d},"text":"2u64"},{"op":"insert","file":%q,"at":%d,"text":"/*a*/"},{"op":"insert","file":%q,"at":%d,"text":"/*b*/"},{"op":"delete_span","file":%q,"span":{"start":%d,"end":%d}}`, path, k, k+4, path, k, path, k, path, k-3, k))
	if code != 0 || !resp.OK {
		t.Fatalf("insert placement: exit %d resp %+v", code, resp)
	}
	got = resp.Result.(map[string]any)["files"].([]any)[0].(map[string]any)["source"].(string)
	if !strings.Contains(got, "total /*a*//*b*/2u64") {
		t.Fatalf("insert placement result: %q", got)
	}
	// precedence: op 2 is invalid and op 1 overlaps op 0; the invalid op has the higher index, so overlap at index 0 wins
	code, resp = apply(fmt.Sprintf(`{"op":"replace_span","file":%q,"span":{"start":0,"end":10},"text":"x"},{"op":"delete_span","file":%q,"span":{"start":5,"end":12}},{"op":"insert","file":%q,"at":9999,"text":"y"}`, path, path, path))
	if code != 2 || resp.Diagnostics[0].Code != "EDIT_OVERLAP" || *resp.Diagnostics[0].OpIndex != 0 {
		t.Fatalf("overlap precedence: exit %d resp %+v", code, resp)
	}
	// the same op both invalid and overlapping: EDIT_INVALID_SPAN wins
	code, resp = apply(fmt.Sprintf(`{"op":"replace_span","file":%q,"span":{"start":3,"end":3},"text":"x"},{"op":"delete_span","file":%q,"span":{"start":0,"end":12}}`, path, path))
	if code != 2 || resp.Diagnostics[0].Code != "EDIT_INVALID_SPAN" || *resp.Diagnostics[0].OpIndex != 0 {
		t.Fatalf("invalid precedence: exit %d resp %+v", code, resp)
	}
	// diagnostics of the subsequent check are returned with ok=true
	code, resp = apply(fmt.Sprintf(`{"op":"replace_span","file":%q,"span":%s,"text":"1u8"}`, path, span("1u64")))
	if code != 0 || !resp.OK || len(resp.Diagnostics) == 0 || resp.Diagnostics[0].Code != "TYPE_MISMATCH" {
		t.Fatalf("check after edit: exit %d resp %+v", code, resp)
	}
	// show-effects
	code, resp, _ = runWith(t, []string{"show-effects", path}, "")
	if code != 0 || !resp.OK {
		t.Fatalf("show-effects: exit %d resp %+v", code, resp)
	}
	fns := resp.Result.(map[string]any)["functions"].([]any)
	seen := map[string]map[string]any{}
	for _, f := range fns {
		fm := f.(map[string]any)
		seen[fm["name"].(string)] = fm
	}
	if seen["m.twice"]["traps"] != true || seen["m.twice"]["allocates"] != false || seen["m.twice"]["label"] != "proven" {
		t.Fatalf("effects of twice: %v", seen["m.twice"])
	}
}

// TestBoundsChecksAggregated: after inlining (release), a check inside a
// helper inlined at two call sites is reported once, with the proof the
// copies carry; a helper whose body is retained (taken by func_ref) keeps
// the weaker label when the retained body cannot prove it (PLAN.md 6.3).
func TestBoundsChecksAggregated(t *testing.T) {
	dir := t.TempDir()
	src := `module t
func rd(p: view[]u8, at: usize) -> u16 {
    return (u16(p[at]) << 8u32) | u16(p[at + 1usize])
}
func parse(pkt: view[]u8) -> u64 {
    if len(pkt) < 4usize { return 0u64 }
    return u64(rd(pkt, 0usize)) + u64(rd(pkt, 2usize))
}
func main() -> i32 {
    var b: [6]u8 = zero()
    print_u64(parse(b))
    println()
    return 0i32
}
`
	path := filepath.Join(dir, "agg.kern")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	count := func(opt string) (int, map[string]int) {
		code, resp, _ := runWith(t, []string{"show-bounds-checks", "--opt=" + opt, path}, "")
		if code != 0 || !resp.OK {
			t.Fatalf("show-bounds-checks --opt=%s: exit %d resp %+v", opt, code, resp)
		}
		checks := resp.Result.(map[string]any)["checks"].([]any)
		labels := map[string]int{}
		n := 0
		for _, c := range checks {
			cm := c.(map[string]any)
			if cm["kind"] != "bounds" {
				continue
			}
			n++
			labels[cm["label"].(string)]++
		}
		return n, labels
	}
	// the helper's two reads: one entry each in both configurations
	nDebug, lDebug := count("debug")
	nRelease, lRelease := count("release")
	if nDebug != 2 || nRelease != 2 {
		t.Fatalf("expected the helper's two bounds checks once each, got debug %d release %d", nDebug, nRelease)
	}
	if lDebug["proven"] != 0 {
		t.Fatalf("debug keeps the call: nothing proven, got %v", lDebug)
	}
	if lRelease["proven"] != 2 {
		t.Fatalf("release inlines the helper under the guard: both reads proven, got %v", lRelease)
	}
	// a retained body (the helper also taken as a function value) keeps the location runtime-checked
	src2 := strings.Replace(src, "func main() -> i32 {\n", "func apply(f: func(view[]u8, usize) -> u16, p: view[]u8) -> u16 { return f(p, 1usize) }\nfunc main() -> i32 {\n    var c: [6]u8 = zero()\n    print_u64(u64(apply(rd, c)))\n    println()\n", 1)
	path2 := filepath.Join(dir, "agg2.kern")
	if err := os.WriteFile(path2, []byte(src2), 0o644); err != nil {
		t.Fatal(err)
	}
	code, resp, _ := runWith(t, []string{"show-bounds-checks", "--opt=release", path2}, "")
	if code != 0 || !resp.OK {
		t.Fatalf("show-bounds-checks: exit %d resp %+v", code, resp)
	}
	n, labels := 0, map[string]int{}
	for _, c := range resp.Result.(map[string]any)["checks"].([]any) {
		cm := c.(map[string]any)
		if cm["kind"] == "bounds" {
			n++
			labels[cm["label"].(string)]++
		}
	}
	if n != 2 || labels["runtime-checked"] != 2 {
		t.Fatalf("a retained body keeps its reads runtime-checked: got %d entries %v", n, labels)
	}
}
