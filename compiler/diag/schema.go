// Machine-API request and result types for schema_version 0.2 (spec §7, §16).
//
// The JSON tags below are the wire names of §16. tools/agent-api/schema.json
// is the normative machine-readable copy of §16; tools/agent-api/schema.md is
// rendered from these types by RenderSchemaMarkdown and must stay in step with
// it (compiler/diag/schema_test.go enforces both).
//
//go:generate go run ./genschema ../../tools/agent-api/schema.md
package diag

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Label is the provenance label of a reported fact (§7). It is derived from
// the IR provenance tags, never from heuristics.
type Label string

// The five §16 labels.
const (
	LabelProven         Label = "proven"
	LabelRuntimeChecked Label = "runtime-checked"
	LabelAssumedUnsafe  Label = "assumed-unsafe"
	LabelEstimated      Label = "estimated"
	LabelUnknown        Label = "unknown"
)

// Labels lists the five labels in §16 order.
var Labels = []Label{LabelProven, LabelRuntimeChecked, LabelAssumedUnsafe, LabelEstimated, LabelUnknown}

// Commands lists the eleven §7 machine-API commands in §7 order.
var Commands = []string{
	"check",
	"format",
	"explain-ownership",
	"show-layout",
	"show-allocations",
	"show-copies",
	"show-effects",
	"show-bounds-checks",
	"emit-ir",
	"emit-asm",
	"apply-structured-edit",
}

// Structured-edit operation kinds (§16).
const (
	OpReplaceSpan  = "replace_span"
	OpInsert       = "insert"
	OpDeleteSpan   = "delete_span"
	OpRenameSymbol = "rename_symbol"
)

// RequestSpan is the request's span: a half-open byte range in one of the
// request files. It is required by explain-ownership and ignored by every
// other command (§7).
type RequestSpan struct {
	File  string `json:"file"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

// Request is the §7 machine-API request, one JSON document on standard input.
type Request struct {
	SchemaVersion string          `json:"schema_version"`
	Command       string          `json:"command"`
	Files         []string        `json:"files"`
	Target        string          `json:"target,omitempty"`
	Span          *RequestSpan    `json:"span,omitempty"`
	Edit          *StructuredEdit `json:"edit,omitempty"`
}

// StructuredEdit is the §16 structured edit: the edit of an
// apply-structured-edit request, and the fix a diagnostic carries, which an
// apply-structured-edit request can send back as it is.
type StructuredEdit struct {
	Ops []StructuredEditOp `json:"ops"`
}

// StructuredEditOp is one of the four §16 operations. Op selects the shape:
// replace_span carries span and text; insert carries at and text;
// delete_span carries span; rename_symbol carries span and new_name.
// MarshalJSON emits exactly the fields of the selected shape.
type StructuredEditOp struct {
	Op      string `json:"op"`
	File    string `json:"file"`
	Span    *Span  `json:"span,omitempty"`
	At      *int   `json:"at,omitempty"`
	Text    string `json:"text,omitempty"`
	NewName string `json:"new_name,omitempty"`
}

// MarshalJSON writes the §16 shape of the operation's kind, so that text is
// present (even when empty) for replace_span and insert and absent otherwise.
func (op StructuredEditOp) MarshalJSON() ([]byte, error) {
	switch op.Op {
	case OpReplaceSpan:
		return json.Marshal(struct {
			Op   string `json:"op"`
			File string `json:"file"`
			Span *Span  `json:"span"`
			Text string `json:"text"`
		}{op.Op, op.File, op.Span, op.Text})
	case OpInsert:
		return json.Marshal(struct {
			Op   string `json:"op"`
			File string `json:"file"`
			At   *int   `json:"at"`
			Text string `json:"text"`
		}{op.Op, op.File, op.At, op.Text})
	case OpDeleteSpan:
		return json.Marshal(struct {
			Op   string `json:"op"`
			File string `json:"file"`
			Span *Span  `json:"span"`
		}{op.Op, op.File, op.Span})
	case OpRenameSymbol:
		return json.Marshal(struct {
			Op      string `json:"op"`
			File    string `json:"file"`
			Span    *Span  `json:"span"`
			NewName string `json:"new_name"`
		}{op.Op, op.File, op.Span, op.NewName})
	}
	return nil, fmt.Errorf("diag: unknown structured-edit op %q", op.Op)
}

// Loc is a span within a named file (§16 common types).
type Loc struct {
	File string `json:"file"`
	Span Span   `json:"span"`
}

// CheckResult is the result of check: diagnostics carry all findings.
type CheckResult struct {
	Files []string `json:"files"`
}

// FormatResult is the result of format.
type FormatResult struct {
	Files []FormattedFile `json:"files"`
}

// FormattedFile is one formatted request file.
type FormattedFile struct {
	File      string `json:"file"`
	Formatted string `json:"formatted"`
}

// ExplainOwnershipResult is the result of explain-ownership: every place that
// intersects the request span.
type ExplainOwnershipResult struct {
	Places []Place `json:"places"`
}

// Place is one explain-ownership place.
type Place struct {
	Loc      Loc           `json:"loc"`
	Owner    Loc           `json:"owner"`
	State    string        `json:"state"`
	Accesses []AccessEntry `json:"accesses"`
	DropAt   *Loc          `json:"drop_at"`
	Contract *Contract     `json:"contract"`
	Label    Label         `json:"label"`
}

// Place states (§16).
const (
	StateOwned         = "owned"
	StateMoved         = "moved"
	StateUninitialized = "uninitialized"
	StateAccess        = "access"
)

// AccessEntry is one access of a place.
type AccessEntry struct {
	Kind string `json:"kind"`
	Span Span   `json:"span"`
	Root Loc    `json:"root"`
}

// Contract is a contract block as source text.
type Contract struct {
	Invariant []string `json:"invariant"`
	Requires  []string `json:"requires"`
	Ensures   []string `json:"ensures"`
}

// ShowLayoutResult is the result of show-layout.
type ShowLayoutResult struct {
	Target string       `json:"target"`
	Types  []TypeLayout `json:"types"`
}

// TypeLayout is the layout of one type for the target.
type TypeLayout struct {
	Name   string        `json:"name"`
	Size   int           `json:"size"`
	Align  int           `json:"align"`
	Fields []FieldLayout `json:"fields"`
}

// FieldLayout is the placement of one field.
type FieldLayout struct {
	Name   string `json:"name"`
	Offset int    `json:"offset"`
	Size   int    `json:"size"`
}

// ShowAllocationsResult is the result of show-allocations.
type ShowAllocationsResult struct {
	Sites []AllocationSite `json:"sites"`
}

// AllocationSite is one allocation site.
type AllocationSite struct {
	Loc       Loc    `json:"loc"`
	Allocator string `json:"allocator"`
	Size      *int   `json:"size"`
	Label     Label  `json:"label"`
}

// ShowCopiesResult is the result of show-copies.
type ShowCopiesResult struct {
	Target string `json:"target"`
	Copies []Copy `json:"copies"`
}

// Copy is one copy site.
type Copy struct {
	Loc   Loc    `json:"loc"`
	Type  string `json:"type"`
	Bytes int    `json:"bytes"`
	Kind  string `json:"kind"`
	Heap  bool   `json:"heap"`
	Label Label  `json:"label"`
}

// Copy kinds (§16).
const (
	CopyBitwise      = "bitwise"
	CopyExplicitDeep = "explicit-deep"
)

// ShowEffectsResult is the result of show-effects.
type ShowEffectsResult struct {
	Target    string            `json:"target"`
	Functions []FunctionEffects `json:"functions"`
}

// FunctionEffects is the effect summary of one function.
type FunctionEffects struct {
	Name      string `json:"name"`
	Allocates bool   `json:"allocates"`
	Traps     bool   `json:"traps"`
	Spawns    bool   `json:"spawns"`
	Unsafe    bool   `json:"unsafe"`
	Label     Label  `json:"label"`
}

// ShowBoundsChecksResult is the result of show-bounds-checks.
type ShowBoundsChecksResult struct {
	Checks []BoundsCheck `json:"checks"`
}

// BoundsCheck is one runtime check or proven-away check.
type BoundsCheck struct {
	Loc      Loc     `json:"loc"`
	Kind     string  `json:"kind"`
	Label    Label   `json:"label"`
	ProvenBy *string `json:"proven_by"`
}

// Check kinds (§16).
const (
	CheckBounds     = "bounds"
	CheckGeneration = "generation"
	CheckDisjoint   = "disjoint"
	CheckOverflow   = "overflow"
)

// EmitIRResult is the result of emit-ir.
type EmitIRResult struct {
	IR string `json:"ir"`
}

// EmitAsmResult is the result of emit-asm.
type EmitAsmResult struct {
	Target string `json:"target"`
	Asm    string `json:"asm"`
}

// ApplyEditResult is the result of apply-structured-edit.
type ApplyEditResult struct {
	Files   []EditedFile `json:"files"`
	Applied int          `json:"applied"`
}

// EditedFile is the new source of one changed file.
type EditedFile struct {
	File      string `json:"file"`
	Source    string `json:"source"`
	Formatted bool   `json:"formatted"`
}

// ResultTypes maps each command to the Go type of its result object.
var ResultTypes = map[string]reflect.Type{
	"check":                 reflect.TypeFor[CheckResult](),
	"format":                reflect.TypeFor[FormatResult](),
	"explain-ownership":     reflect.TypeFor[ExplainOwnershipResult](),
	"show-layout":           reflect.TypeFor[ShowLayoutResult](),
	"show-allocations":      reflect.TypeFor[ShowAllocationsResult](),
	"show-copies":           reflect.TypeFor[ShowCopiesResult](),
	"show-effects":          reflect.TypeFor[ShowEffectsResult](),
	"show-bounds-checks":    reflect.TypeFor[ShowBoundsChecksResult](),
	"emit-ir":               reflect.TypeFor[EmitIRResult](),
	"emit-asm":              reflect.TypeFor[EmitAsmResult](),
	"apply-structured-edit": reflect.TypeFor[ApplyEditResult](),
}

// Notes carries the §16 prose that the type signatures cannot express. Keys
// are a command name (a note on the whole result) or `Type.json_name` (a note
// on one field). They are rendered into tools/agent-api/schema.md.
var Notes = map[string]string{
	"check":                 "Diagnostics carry all findings. Proven by construction; no `label`.",
	"format":                "Formatting per the normative rules in `spec/format.md`. Required properties: `format(format(x)) == format(x)`, and `check(format(x))` yields the same diagnostic codes as `check(x)`. Proven by construction; no `label`.",
	"explain-ownership":     "One place for every place that intersects the request `span` (`API_MISSING_SPAN` when the request has none, `API_INVALID_SPAN` when it does not name one of `files` or violates the span rules).",
	"show-layout":           "Sizes, alignments and offsets are for the request target. Proven by construction; no `label`.",
	"show-allocations":      "Every allocation site in the request files' user code: a call of an allocating runtime operation or of a core container operation known to allocate (`Box.new` is unconditional, `proven`, with the stored size; `Vec.push`, `Vec.with_capacity`, `Pool.alloc`, `Arena.alloc` and a deep `clone` are conditional, `unknown`), and any other call into library code whose effect summary allocates (`unknown`, `size` `null`: the callee may or may not allocate on a given call). Library code's own allocations are attributed to the user-level call; constructors that allocate nothing (`Vec.new`, `Pool.new`, `Arena.new`) are not sites.",
	"show-copies":           "Every copy for the request target.",
	"show-effects":          "Effects are transitive over statically resolved callees. `true` means the effect is possible; `false` means the compiler proved it impossible and is only emitted with label `proven`. A call through a function pointer or into FFI makes every effect `true` with label `unknown` (FFI: `assumed-unsafe`).",
	"show-bounds-checks":    "Every `check_*` instruction of the IR, kept or replaced by a recorded proof fact. One entry per source location and kind: the IR occurrences of a location (generic instantiations, the copies the release inliner makes in each caller, and the retained callee while it is still reachable) are aggregated, and the entry is `proven` with the first proven occurrence's `proven_by` only when every occurrence is proven, `runtime-checked` otherwise. The result is computed for the `--opt` level the command runs with: `release` inlines small helpers into their callers before the proof pass, `debug` keeps every call. `show-copies` and `show-allocations` likewise report a location once however many occurrences it has.",
	"emit-ir":               "Textual semantic IR for the request files. Proven by construction; no `label`.",
	"emit-asm":              "Native assembly for the request target. Proven by construction; no `label`.",
	"apply-structured-edit": "The new source of every changed file plus the diagnostics of a `check` on the result. Coordinate, overlap and symbol failures are atomic and set `ok=false` with one `EDIT_OVERLAP`, `EDIT_UNRESOLVED_SYMBOL` or `EDIT_INVALID_SPAN` diagnostic naming the offending op by `op_index`. Once the ops are applied the edit has succeeded (`ok=true`, `applied` = number of ops) even if the result does not parse or does not check. Proven by construction; no `label`.",

	"Request.schema_version":    "Always `\"0.2\"`; anything else is `API_UNSUPPORTED_SCHEMA`.",
	"Request.command":           "One of the eleven commands.",
	"Request.files":             "Source file paths; an empty array is `API_NO_FILES`, an unreadable file `API_FILE_NOT_FOUND`.",
	"Request.target":            "LLVM-style target triple; when absent, the host target is used. Every result is computed for that target.",
	"Request.span":              "Names one of `files`. Required by `explain-ownership` (`API_MISSING_SPAN` otherwise) and ignored, never validated, by every other command.",
	"Request.edit":              "The structured edit applied by `apply-structured-edit`.",
	"RequestSpan.file":          "One of the request `files`.",
	"RequestSpan.start":         "Byte offset, inclusive.",
	"RequestSpan.end":           "Byte offset, exclusive.",
	"StructuredEditOp.op":       "`replace_span`, `insert`, `delete_span` or `rename_symbol`.",
	"StructuredEditOp.span":     "`replace_span`, `delete_span`, `rename_symbol`: the non-empty span `[start, end)` (an empty span is `EDIT_INVALID_SPAN`; use `insert`).",
	"StructuredEditOp.at":       "`insert` only: the offset whose original byte the text is placed before.",
	"StructuredEditOp.text":     "`replace_span` and `insert`: the replacement or inserted text.",
	"StructuredEditOp.new_name": "`rename_symbol` only: renames the declaration at `span` and every resolved use of it across the request files (`EDIT_UNRESOLVED_SYMBOL` if `span` is not a resolvable declaration).",
	"Response.ok":               "`false` on failure, with a non-empty `diagnostics` array and exit code 2 (user error) or 3 (internal error).",
	"Response.result":           "The command's result object, or `null` on failure before a result exists.",
	"Diagnostic.code":           "Stable code; every code maps to one numbered rule in the language reference (`spec/rules.md`).",
	"Diagnostic.rule":           "The violated rule, `section.bullet`.",
	"Diagnostic.file":           "Empty only for `API_*` and `INTERNAL` diagnostics that have no source location, with `span` `{0, 0}`.",
	"Diagnostic.related":        "Secondary locations in the same file.",
	"Diagnostic.fix":            "A machine-applicable structured edit, or `null`.",
	"Diagnostic.op_index":       "Zero-based index into the request's `edit.ops` for `EDIT_*` diagnostics; `null` otherwise.",
	"Diagnostic.message":        "The compiler's one-sentence message for the diagnostic, the text the human rendering prints; present from `schema_version` 0.3 and absent in a 0.2 response. Not stable across compiler versions, unlike `code` and `rule`.",
	"Span.start":                "Byte offset, inclusive.",
	"Span.end":                  "Byte offset, exclusive.",
	"Place.state":               "`owned`, `moved`, `uninitialized` or `access`.",
	"Place.drop_at":             "Where the static drop occurs, or `null`.",
	"Place.contract":            "The contract block in scope, or `null`.",
	"AccessEntry.kind":          "`view`, `edit`, `consume` or `init`.",
	"AccessEntry.root":          "The root place of the access path.",
	"TypeLayout.size":           "Bytes.",
	"TypeLayout.align":          "Bytes.",
	"FieldLayout.offset":        "Bytes from the start of the enclosing type.",
	"AllocationSite.size":       "Static size in bytes, or `null` when runtime-dependent.",
	"Copy.bytes":                "The inline size of the type per `show-layout`.",
	"Copy.kind":                 "`bitwise` or `explicit-deep`.",
	"Copy.heap":                 "`true` iff the copy also duplicates owned heap storage, whose size is runtime-dependent and not reported.",
	"FunctionEffects.allocates": "Possible unless proven impossible (`false` only with label `proven`).",
	"FunctionEffects.traps":     "Possible unless proven impossible (`false` only with label `proven`).",
	"FunctionEffects.spawns":    "Possible unless proven impossible (`false` only with label `proven`).",
	"FunctionEffects.unsafe":    "Possible unless proven impossible (`false` only with label `proven`).",
	"BoundsCheck.kind":          "`bounds`, `generation`, `disjoint` or `overflow`.",
	"BoundsCheck.label":         "`runtime-checked` or `proven`.",
	"BoundsCheck.proven_by":     "Non-null iff `label` is `proven`: `loop_bound`, `dominated_by_check` or `constant`. `constant` covers a literal index into an array whose length its type gives, and an index bounded by a constant mask (`x & c`, through conversions, plus a constant offset) below such a length; a masked index below a length a guard bounded from below is `dominated_by_check`.",
	"ApplyEditResult.files":     "Every changed file.",
	"ApplyEditResult.applied":   "Number of ops applied.",
	"EditedFile.formatted":      "`true` when the new source parses and is returned formatted; `false` when it is returned verbatim with the parse diagnostics.",
	"EditedFile.source":         "The new source text.",
}

// RenderSchemaMarkdown renders tools/agent-api/schema.md from the types in
// this file. The output is deterministic so a test can diff it against the
// committed copy.
func RenderSchemaMarkdown() string {
	r := &mdRenderer{seen: map[reflect.Type]bool{}, hold: map[reflect.Type]bool{}}
	r.p("# kern machine API (`schema_version` %s)\n\n", SchemaVersion)
	r.p("Generated from `compiler/diag/schema.go` by `go generate ./compiler/diag`; do not edit.\n")
	r.p("This is the human-readable copy of spec §16; `tools/agent-api/schema.json` is the normative JSON Schema and the two must agree.\n\n")
	r.p("Every command reads one JSON request on standard input and writes one JSON response on standard output (§7). ")
	r.p("Failure: `ok=false`, a non-empty `diagnostics` array, and exit code 2 for user errors or 3 for internal errors.\n\n")

	r.p("## Common types\n\n")
	r.p("- `span`: `{\"start\": int, \"end\": int}`, byte offsets, half-open.\n")
	r.p("- `loc`: `{\"file\": string, \"span\": span}`.\n")
	r.p("- `label`: one of %s; derived from the IR provenance tags, never from heuristics.\n\n", quoteLabels())
	r.section(reflect.TypeFor[Span]())
	r.section(reflect.TypeFor[Loc]())

	r.p("## Request\n\n")
	r.hold[reflect.TypeFor[StructuredEdit]()] = true
	r.section(reflect.TypeFor[Request]())
	r.p("## Structured edit\n\n")
	r.p("`edit` in the request, and the `fix` of a diagnostic, is `{\"ops\": [op]}`. Ops apply to the files as they were before the edit; ")
	r.p("two non-insert ops in one file conflict iff their spans intersect, and an insert at `k` conflicts with a non-insert span `[s, e)` iff `s < k < e`. ")
	r.p("Application is all-or-nothing (`EDIT_OVERLAP`, `EDIT_UNRESOLVED_SYMBOL`, `EDIT_INVALID_SPAN`).\n\n")
	delete(r.hold, reflect.TypeFor[StructuredEdit]())
	r.section(reflect.TypeFor[StructuredEdit]())

	r.p("## Response\n\n")
	r.section(reflect.TypeFor[Response]())
	r.section(reflect.TypeFor[Diagnostic]())
	r.section(reflect.TypeFor[Related]())

	r.p("## Commands\n\n")
	r.p("| Command | `result` type | `target` in result | `label` |\n|---|---|---|---|\n")
	for _, c := range Commands {
		t := ResultTypes[c]
		r.p("| `%s` | `%s` | %s | %s |\n", c, t.Name(), yesNo(hasField(t, "target")), yesNo(usesLabel(t)))
	}
	r.p("\n")
	for _, c := range Commands {
		r.p("### %s\n\n", c)
		if n := Notes[c]; n != "" {
			r.p("%s\n\n", n)
		}
		r.section(ResultTypes[c])
	}
	return r.sb.String()
}

type mdRenderer struct {
	sb   strings.Builder
	seen map[reflect.Type]bool
	hold map[reflect.Type]bool // types rendered later under their own heading
}

func (r *mdRenderer) p(format string, args ...any) {
	fmt.Fprintf(&r.sb, format, args...)
}

// section renders the table of t and then, once each, every struct type its
// fields reference.
func (r *mdRenderer) section(t reflect.Type) {
	if r.seen[t] {
		return
	}
	r.seen[t] = true
	r.p("#### `%s`\n\n| Field | JSON | Type | Notes |\n|---|---|---|---|\n", t.Name())
	var nested []reflect.Type
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, opt, ok := jsonName(f)
		if !ok {
			continue
		}
		note := Notes[t.Name()+"."+name]
		ft := f.Type
		if opt {
			note = strings.TrimSpace("Optional. " + note)
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem() // an omitted optional field is absent, not null
			}
		}
		r.p("| `%s` | `%s` | %s | %s |\n", f.Name, name, jsonType(ft), note)
		nested = append(nested, structTypes(f.Type)...)
	}
	r.p("\n")
	for _, n := range nested {
		if !r.hold[n] {
			r.section(n)
		}
	}
}

// jsonName returns the wire name of a struct field, whether it is optional
// (omitempty), and false for fields that are not part of the wire format.
func jsonName(f reflect.StructField) (name string, optional, ok bool) {
	tag := f.Tag.Get("json")
	if tag == "-" || f.PkgPath != "" {
		return "", false, false
	}
	parts := strings.Split(tag, ",")
	name = parts[0]
	if name == "" {
		name = f.Name
	}
	for _, p := range parts[1:] {
		if p == "omitempty" {
			optional = true
		}
	}
	return name, optional, true
}

// jsonType renders the §16 type expression for a Go type.
func jsonType(t reflect.Type) string {
	switch t {
	case reflect.TypeFor[Label]():
		return "label"
	case reflect.TypeFor[Span]():
		return "span"
	case reflect.TypeFor[Loc]():
		return "loc"
	case reflect.TypeFor[Code]():
		return "string"
	}
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Int:
		return "int"
	case reflect.Bool:
		return "bool"
	case reflect.Pointer:
		return jsonType(t.Elem()) + " \\| null"
	case reflect.Slice:
		return "[" + jsonType(t.Elem()) + "]"
	case reflect.Struct:
		return "`" + t.Name() + "`"
	case reflect.Interface:
		return "object \\| null"
	}
	return t.String()
}

// structTypes returns the named struct types reachable from t through
// pointers and slices (not through interfaces).
func structTypes(t reflect.Type) []reflect.Type {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
		t = t.Elem()
	}
	if t.Kind() == reflect.Struct && t.Name() != "" {
		return []reflect.Type{t}
	}
	return nil
}

func hasField(t reflect.Type, jsonField string) bool {
	for i := 0; i < t.NumField(); i++ {
		if n, _, ok := jsonName(t.Field(i)); ok && n == jsonField {
			return true
		}
	}
	return false
}

// usesLabel reports whether a label field is reachable from t.
func usesLabel(t reflect.Type) bool {
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type) bool
	walk = func(t reflect.Type) bool {
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
			t = t.Elem()
		}
		if t == reflect.TypeFor[Label]() {
			return true
		}
		if t.Kind() != reflect.Struct || seen[t] {
			return false
		}
		seen[t] = true
		for i := 0; i < t.NumField(); i++ {
			if walk(t.Field(i).Type) {
				return true
			}
		}
		return false
	}
	return walk(t)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func quoteLabels() string {
	s := make([]string, len(Labels))
	for i, l := range Labels {
		s[i] = "`" + string(l) + "`"
	}
	return strings.Join(s, ", ")
}

// JSONFields returns the sorted wire names of every field reachable from t
// through structs, pointers and slices. Tests use it to compare the Go types
// with schema.json.
func JSONFields(t reflect.Type) []string {
	set := map[string]bool{}
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type)
	walk = func(t reflect.Type) {
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct || seen[t] {
			return
		}
		seen[t] = true
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if n, _, ok := jsonName(f); ok {
				set[n] = true
				walk(f.Type)
			}
		}
	}
	walk(t)
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
