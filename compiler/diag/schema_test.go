package diag

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	schemaJSONPath = "../../tools/agent-api/schema.json"
	schemaMDPath   = "../../tools/agent-api/schema.md"
)

func loadSchemaDoc(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile(schemaJSONPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("schema.json is not valid JSON: %v", err)
	}
	return doc
}

func compileSchema(t *testing.T, fragment string) *jsonschema.Schema {
	t.Helper()
	abs, err := filepath.Abs(schemaJSONPath)
	if err != nil {
		t.Fatal(err)
	}
	sch, err := jsonschema.NewCompiler().Compile(abs + fragment)
	if err != nil {
		t.Fatalf("compile %s%s: %v", schemaJSONPath, fragment, err)
	}
	return sch
}

// roundTrip marshals v with encoding/json and decodes it the way the
// validator expects, so the test exercises the exact wire bytes.
func roundTrip(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	inst, err := jsonschema.UnmarshalJSON(strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return inst
}

func defs(t *testing.T, doc map[string]any, path ...string) map[string]any {
	t.Helper()
	cur := doc
	for _, p := range path {
		next, ok := cur[p].(map[string]any)
		if !ok {
			t.Fatalf("schema.json lacks object at %s", strings.Join(path, "/"))
		}
		cur = next
	}
	return cur
}

// TestSchemaJSONStructure checks that schema.json is a draft 2020-12 document
// with the envelope, request, diagnostic, edit and per-command result schemas.
func TestSchemaJSONStructure(t *testing.T) {
	doc := loadSchemaDoc(t)
	if doc["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("$schema = %v, want draft 2020-12", doc["$schema"])
	}
	d := defs(t, doc, "$defs")
	for _, k := range []string{"request", "response", "diagnostic", "structured_edit", "edit_op", "span", "loc", "label", "request_span", "results", "responses"} {
		if _, ok := d[k]; !ok {
			t.Errorf("$defs lacks %s", k)
		}
	}
	if v := defs(t, doc, "$defs", "schema_version")["const"]; v != SchemaVersion {
		t.Errorf("schema_version const %v != %s", v, SchemaVersion)
	}
	enum, _ := defs(t, doc, "$defs", "command")["enum"].([]any)
	if len(enum) != len(Commands) {
		t.Fatalf("command enum has %d entries, want %d", len(enum), len(Commands))
	}
	for i, c := range Commands {
		if enum[i] != c {
			t.Errorf("command enum[%d] = %v, want %s", i, enum[i], c)
		}
	}
	labels, _ := defs(t, doc, "$defs", "label")["enum"].([]any)
	if len(labels) != len(Labels) {
		t.Fatalf("label enum has %d entries, want %d", len(labels), len(Labels))
	}
	for i, l := range Labels {
		if labels[i] != string(l) {
			t.Errorf("label enum[%d] = %v, want %s", i, labels[i], l)
		}
	}
	ops := defs(t, doc, "$defs", "edit_ops", "$defs")
	for _, op := range []string{OpReplaceSpan, OpInsert, OpDeleteSpan, OpRenameSymbol} {
		if _, ok := ops[op]; !ok {
			t.Errorf("edit_ops lacks %s", op)
		}
	}
	results := defs(t, doc, "$defs", "results", "$defs")
	responses := defs(t, doc, "$defs", "responses", "$defs")
	for _, c := range Commands {
		if _, ok := results[c].(map[string]any); !ok {
			t.Errorf("results lacks a schema for %s", c)
		}
		if _, ok := responses[c].(map[string]any); !ok {
			t.Errorf("responses lacks a schema for %s", c)
		}
	}
	if len(results) != len(Commands) {
		t.Errorf("results has %d schemas, want %d", len(results), len(Commands))
	}
	diagProps := defs(t, doc, "$defs", "diagnostic", "properties")
	var got []string
	for k := range diagProps {
		got = append(got, k)
	}
	sort.Strings(got)
	if want := fieldsOf(reflect.TypeFor[Diagnostic]()); !reflect.DeepEqual(got, want) {
		t.Errorf("diagnostic properties %v, want %v", got, want)
	}
	// The whole document must compile as a 2020-12 schema.
	compileSchema(t, "")
}

// fieldsOf returns the sorted direct wire names of t.
func fieldsOf(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		if n, _, ok := jsonName(t.Field(i)); ok {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func sampleCheckResponse() Response {
	d := New("OWN_USE_AFTER_MOVE", "main.kern", Span{Start: 128, End: 134}, "value used after move")
	d.Related = []Related{{Label: "moved here", Start: 91, End: 97}}
	d.Fix = &StructuredEdit{Ops: []StructuredEditOp{{Op: OpReplaceSpan, File: "main.kern", Span: &Span{Start: 128, End: 134}, Text: "y"}}}
	return Response{
		SchemaVersion: SchemaVersion,
		OK:            false,
		Result:        CheckResult{Files: []string{"main.kern"}},
		Diagnostics:   []Diagnostic{d},
	}
}

// TestSampleResponsesValidate marshals responses built from the Go types and
// validates them against schema.json, both the generic envelope and the
// per-command specialisation.
func TestSampleResponsesValidate(t *testing.T) {
	envelope := compileSchema(t, "#/$defs/response")
	whole := compileSchema(t, "")
	str := func(s string) *string { return &s }
	samples := map[string]Response{
		"check": sampleCheckResponse(),
		"check-ok": {SchemaVersion: SchemaVersion, OK: true,
			Result: CheckResult{Files: []string{"a.kern"}}, Diagnostics: []Diagnostic{}},
		"check-api-failure": {SchemaVersion: SchemaVersion, OK: false, Result: nil,
			Diagnostics: []Diagnostic{New("API_NO_FILES", "", Span{}, "no files")}},
		"check-internal": {SchemaVersion: SchemaVersion, OK: false, Result: nil,
			Diagnostics: []Diagnostic{New("INTERNAL", "", Span{}, "boom")}},
		"format": {SchemaVersion: SchemaVersion, OK: true, Diagnostics: []Diagnostic{},
			Result: FormatResult{Files: []FormattedFile{{File: "a.kern", Formatted: "fn main() {}\n"}}}},
		"explain-ownership": {SchemaVersion: SchemaVersion, OK: true, Diagnostics: []Diagnostic{},
			Result: ExplainOwnershipResult{Places: []Place{{
				Loc: Loc{File: "a.kern", Span: Span{Start: 1, End: 2}}, Owner: Loc{File: "a.kern", Span: Span{Start: 1, End: 2}},
				State: StateOwned, Accesses: []AccessEntry{{Kind: "view", Span: Span{Start: 3, End: 4}, Root: Loc{File: "a.kern", Span: Span{Start: 1, End: 2}}}},
				DropAt: &Loc{File: "a.kern", Span: Span{Start: 9, End: 9}}, Contract: &Contract{Invariant: []string{"len <= cap"}, Requires: []string{}, Ensures: []string{}},
				Label: LabelProven}}}},
		"show-layout": {SchemaVersion: SchemaVersion, OK: true, Diagnostics: []Diagnostic{},
			Result: ShowLayoutResult{Target: "aarch64-apple-darwin", Types: []TypeLayout{{Name: "Vec", Size: 24, Align: 8, Fields: []FieldLayout{{Name: "len", Offset: 0, Size: 8}}}}}},
		"show-allocations": {SchemaVersion: SchemaVersion, OK: true, Diagnostics: []Diagnostic{},
			Result: ShowAllocationsResult{Sites: []AllocationSite{{Loc: Loc{File: "a.kern", Span: Span{Start: 1, End: 2}}, Allocator: "SystemAllocator", Size: nil, Label: LabelEstimated}}}},
		"show-copies": {SchemaVersion: SchemaVersion, OK: true, Diagnostics: []Diagnostic{},
			Result: ShowCopiesResult{Target: "x86_64-unknown-linux-gnu", Copies: []Copy{{Loc: Loc{File: "a.kern", Span: Span{Start: 1, End: 2}}, Type: "Point", Bytes: 16, Kind: CopyBitwise, Heap: false, Label: LabelProven}}}},
		"show-effects": {SchemaVersion: SchemaVersion, OK: true, Diagnostics: []Diagnostic{},
			Result: ShowEffectsResult{Target: "x86_64-unknown-linux-gnu", Functions: []FunctionEffects{
				{Name: "main", Allocates: true, Traps: false, Spawns: false, Unsafe: false, Label: LabelProven},
				{Name: "ffi", Allocates: true, Traps: true, Spawns: true, Unsafe: true, Label: LabelAssumedUnsafe}}}},
		"show-bounds-checks": {SchemaVersion: SchemaVersion, OK: true, Diagnostics: []Diagnostic{},
			Result: ShowBoundsChecksResult{Checks: []BoundsCheck{
				{Loc: Loc{File: "a.kern", Span: Span{Start: 1, End: 2}}, Kind: CheckBounds, Label: LabelProven, ProvenBy: str("loop_bound")},
				{Loc: Loc{File: "a.kern", Span: Span{Start: 5, End: 6}}, Kind: CheckOverflow, Label: LabelRuntimeChecked, ProvenBy: nil}}}},
		"emit-ir":  {SchemaVersion: SchemaVersion, OK: true, Diagnostics: []Diagnostic{}, Result: EmitIRResult{IR: "fn main {}"}},
		"emit-asm": {SchemaVersion: SchemaVersion, OK: true, Diagnostics: []Diagnostic{}, Result: EmitAsmResult{Target: "aarch64-apple-darwin", Asm: "ret"}},
		"apply-structured-edit": {SchemaVersion: SchemaVersion, OK: true, Diagnostics: []Diagnostic{},
			Result: ApplyEditResult{Files: []EditedFile{{File: "a.kern", Source: "fn main() {}\n", Formatted: true}}, Applied: 2}},
	}
	for name, resp := range samples {
		inst := roundTrip(t, resp)
		if err := envelope.Validate(inst); err != nil {
			t.Errorf("%s: envelope: %v", name, err)
		}
		if err := whole.Validate(inst); err != nil {
			t.Errorf("%s: top-level oneOf: %v", name, err)
		}
		cmd := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(name, "-ok"), "-api-failure"), "-internal")
		if err := compileSchema(t, "#/$defs/responses/$defs/"+cmd).Validate(inst); err != nil {
			t.Errorf("%s: responses/%s: %v", name, cmd, err)
		}
	}
}

// TestSchemaRejects checks the constraints §16 and PLAN.md §0 state in prose
// are enforced by schema.json.
func TestSchemaRejects(t *testing.T) {
	check := compileSchema(t, "#/$defs/responses/$defs/check")
	cases := map[string]func(r *Response){
		"lang code with empty file":   func(r *Response) { r.Diagnostics[0].File = "" },
		"op_index on non-EDIT code":   func(r *Response) { i := 0; r.Diagnostics[0].OpIndex = &i },
		"unknown severity":            func(r *Response) { r.Diagnostics[0].Severity = "fatal" },
		"ok=false without diagnostic": func(r *Response) { r.Diagnostics = []Diagnostic{} },
		"ok=true with null result":    func(r *Response) { r.OK = true; r.Result = nil },
		"wrong schema_version":        func(r *Response) { r.SchemaVersion = "0.1" },
		"diagnostic without message":  func(r *Response) { r.Diagnostics[0].Message = "" },
		"foreign result":              func(r *Response) { r.Result = EmitIRResult{IR: ""} },
		"extra result field":          func(r *Response) { r.Result = map[string]any{"files": []string{}, "extra": 1} },
		"fix op without span": func(r *Response) {
			r.Diagnostics[0].Fix.Ops[0].Op = OpDeleteSpan
			r.Diagnostics[0].Fix.Ops[0].Span = nil
		},
	}
	for name, mutate := range cases {
		resp := sampleCheckResponse()
		mutate(&resp)
		if err := check.Validate(roundTrip(t, resp)); err == nil {
			t.Errorf("%s: expected validation failure", name)
		}
	}
	// a fix is a StructuredEdit, so its ops marshal the exact §16 shape of
	// their kind (see TestRequestSchema): an empty replacement is a
	// replace_span with text "" and never a delete_span
}

// TestRequestSchema validates requests built from Request against schema.json.
func TestRequestSchema(t *testing.T) {
	req := compileSchema(t, "#/$defs/request")
	whole := compileSchema(t, "")
	at := 3
	good := []Request{
		{SchemaVersion: SchemaVersion, Command: "check", Files: []string{"a.kern"}},
		{SchemaVersion: SchemaVersion, Command: "check", Files: []string{"a.kern"}, Span: &RequestSpan{File: "other.kern", Start: 0, End: 1}},
		{SchemaVersion: SchemaVersion, Command: "explain-ownership", Files: []string{"a.kern"}, Target: "x86_64-unknown-linux-gnu", Span: &RequestSpan{File: "a.kern", Start: 0, End: 1}},
		{SchemaVersion: SchemaVersion, Command: "apply-structured-edit", Files: []string{"a.kern"}, Edit: &StructuredEdit{Ops: []StructuredEditOp{
			{Op: OpReplaceSpan, File: "a.kern", Span: &Span{Start: 0, End: 1}, Text: ""},
			{Op: OpInsert, File: "a.kern", At: &at, Text: "x"},
			{Op: OpDeleteSpan, File: "a.kern", Span: &Span{Start: 4, End: 5}},
			{Op: OpRenameSymbol, File: "a.kern", Span: &Span{Start: 6, End: 7}, NewName: "y"},
		}}},
	}
	for i, g := range good {
		inst := roundTrip(t, g)
		if err := req.Validate(inst); err != nil {
			t.Errorf("good[%d]: %v", i, err)
		}
		if err := whole.Validate(inst); err != nil {
			t.Errorf("good[%d]: top-level: %v", i, err)
		}
	}
	bad := []Request{
		{SchemaVersion: "0.1", Command: "check", Files: []string{"a.kern"}},
		{SchemaVersion: SchemaVersion, Command: "frobnicate", Files: []string{"a.kern"}},
		{SchemaVersion: SchemaVersion, Command: "check", Files: []string{}},
		{SchemaVersion: SchemaVersion, Command: "explain-ownership", Files: []string{"a.kern"}},
		{SchemaVersion: SchemaVersion, Command: "apply-structured-edit", Files: []string{"a.kern"}},
		{SchemaVersion: SchemaVersion, Command: "check", Files: []string{"a.kern"}, Edit: &StructuredEdit{Ops: []StructuredEditOp{{Op: OpRenameSymbol, File: "a.kern", Span: &Span{Start: 6, End: 7}, NewName: ""}}}},
	}
	for i, b := range bad {
		if err := req.Validate(roundTrip(t, b)); err == nil {
			t.Errorf("bad[%d]: expected validation failure", i)
		}
	}
	if _, err := (StructuredEditOp{Op: "explode"}).MarshalJSON(); err == nil {
		t.Error("unknown op marshalled")
	}
}

// TestSchemaMarkdownCurrent fails when the committed schema.md is stale.
func TestSchemaMarkdownCurrent(t *testing.T) {
	b, err := os.ReadFile(schemaMDPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != RenderSchemaMarkdown() {
		t.Fatalf("%s is stale: run `go generate ./compiler/diag`", schemaMDPath)
	}
	md := string(b)
	for _, c := range Commands {
		if !strings.Contains(md, "### "+c+"\n") {
			t.Errorf("schema.md lacks a section for %s", c)
		}
	}
}

// schemaFields collects every property name reachable from a schema node,
// following local $refs into $defs.
func schemaFields(doc map[string]any, node any, set map[string]bool, visited map[string]bool) {
	switch n := node.(type) {
	case map[string]any:
		if ref, ok := n["$ref"].(string); ok && strings.HasPrefix(ref, "#/") {
			if !visited[ref] {
				visited[ref] = true
				var cur any = doc
				for _, p := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
					cur = cur.(map[string]any)[p]
				}
				schemaFields(doc, cur, set, visited)
			}
		}
		if props, ok := n["properties"].(map[string]any); ok {
			for k, v := range props {
				set[k] = true
				schemaFields(doc, v, set, visited)
			}
		}
		for k, v := range n {
			if k != "properties" && k != "$ref" {
				schemaFields(doc, v, set, visited)
			}
		}
	case []any:
		for _, v := range n {
			schemaFields(doc, v, set, visited)
		}
	}
}

// TestResultFieldsMatchSchema asserts that the wire names of every Go result
// type and the property names of the corresponding result schema agree, in
// both directions.
func TestResultFieldsMatchSchema(t *testing.T) {
	doc := loadSchemaDoc(t)
	results := defs(t, doc, "$defs", "results", "$defs")
	for _, c := range Commands {
		set := map[string]bool{}
		schemaFields(doc, results[c], set, map[string]bool{})
		var inSchema []string
		for k := range set {
			inSchema = append(inSchema, k)
		}
		sort.Strings(inSchema)
		inGo := JSONFields(ResultTypes[c])
		if !reflect.DeepEqual(inGo, inSchema) {
			t.Errorf("%s: Go fields %v, schema fields %v", c, inGo, inSchema)
		}
	}
	// The request and diagnostic types must agree with their schemas too.
	for name, typ := range map[string]reflect.Type{"request": reflect.TypeFor[Request](), "diagnostic": reflect.TypeFor[Diagnostic]()} {
		set := map[string]bool{}
		schemaFields(doc, defs(t, doc, "$defs")[name], set, map[string]bool{})
		for _, f := range JSONFields(typ) {
			if !set[f] {
				t.Errorf("%s: Go field %s absent from schema.json", name, f)
			}
		}
	}
}

// TestWriteAtVersions checks the two wire versions (specs/diag-message/SPEC.md
// F3, F4): a 0.3 response carries every diagnostic's message and validates
// against schema.json; a 0.2 response says 0.2, carries exactly the 0.2
// fields of a diagnostic and no message; and neither write changes the
// caller's response.
func TestWriteAtVersions(t *testing.T) {
	resp := sampleCheckResponse()
	msg := resp.Diagnostics[0].Message
	if msg == "" {
		t.Fatal("the sample diagnostic has no message")
	}
	var out bytes.Buffer
	if err := WriteAt(&out, "0.3", resp); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["schema_version"] != "0.3" {
		t.Errorf("0.3 response says %v", got["schema_version"])
	}
	d := got["diagnostics"].([]any)[0].(map[string]any)
	if d["message"] != msg {
		t.Errorf("0.3 diagnostic message %v, want %q", d["message"], msg)
	}
	if err := compileSchema(t, "#/$defs/response").Validate(roundTrip(t, resp)); err != nil {
		t.Errorf("0.3 response does not validate: %v", err)
	}
	out.Reset()
	if err := WriteAt(&out, "0.2", resp); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["schema_version"] != "0.2" {
		t.Errorf("0.2 response says %v", got["schema_version"])
	}
	d = got["diagnostics"].([]any)[0].(map[string]any)
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if want := "code file fix op_index related rule severity span"; strings.Join(keys, " ") != want {
		t.Errorf("0.2 diagnostic fields %q, want %q", strings.Join(keys, " "), want)
	}
	if resp.Diagnostics[0].Message != msg || resp.SchemaVersion != SchemaVersion {
		t.Errorf("WriteAt changed the caller's response: %+v", resp)
	}
	for _, v := range []string{"0.2", "0.3"} {
		if !Supported(v) {
			t.Errorf("%s is not supported", v)
		}
	}
	if Supported("0.4") || Supported("") {
		t.Errorf("an unknown version is supported")
	}
}
