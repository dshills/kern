package diag

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// SchemaVersion is the machine-API schema version this compiler speaks (spec §16).
const SchemaVersion = "0.3"

// Accepted lists the request schema versions the compiler answers: the
// newest (SchemaVersion), whose diagnostics carry a message, and 0.2,
// answered at 0.2 with the diagnostics' messages left out
// (specs/diag-message/SPEC.md F4).
var Accepted = []string{"0.2", SchemaVersion}

// Supported reports whether a request's schema_version is answered.
func Supported(v string) bool {
	for _, a := range Accepted {
		if a == v {
			return true
		}
	}
	return false
}

// Span is a half-open byte range [Start, End) within a file.
type Span struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Related is a labelled secondary location in the same file as the diagnostic.
type Related struct {
	Label string `json:"label"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

// Diagnostic is one §16 diagnostic. Message is the compiler's sentence
// for it, the text the human rendering prints; it is on the wire from
// schema_version 0.3 and left out of a 0.2 response (WriteAt).
type Diagnostic struct {
	Code     Code            `json:"code"`
	Severity string          `json:"severity"`
	Rule     string          `json:"rule"`
	File     string          `json:"file"`
	Span     Span            `json:"span"`
	Related  []Related       `json:"related"`
	Fix      *StructuredEdit `json:"fix"`
	OpIndex  *int            `json:"op_index"`
	Message  string          `json:"message,omitempty"`
}

// Replace is a replace_span fix operation on file.
func Replace(file string, span Span, text string) StructuredEditOp {
	return StructuredEditOp{Op: OpReplaceSpan, File: file, Span: &span, Text: text}
}

// InsertAt is an insert fix operation at byte offset at of file.
func InsertAt(file string, at int, text string) StructuredEditOp {
	return StructuredEditOp{Op: OpInsert, File: file, At: &at, Text: text}
}

// Response is the §7 machine-API response envelope.
type Response struct {
	SchemaVersion string       `json:"schema_version"`
	OK            bool         `json:"ok"`
	Result        any          `json:"result"`
	Diagnostics   []Diagnostic `json:"diagnostics"`
}

// New builds a diagnostic for a registered code. It panics on an unregistered
// code, which is a programming error caught by tests.
func New(code Code, file string, span Span, format string, args ...any) Diagnostic {
	info, ok := Registry[code]
	if !ok {
		panic(fmt.Sprintf("diag: unregistered code %q", code))
	}
	return Diagnostic{
		Code:     code,
		Severity: "error",
		Rule:     info.Rule,
		File:     file,
		Span:     span,
		Related:  []Related{},
		Message:  fmt.Sprintf(format, args...),
	}
}

// Sort orders diagnostics by file, then start offset, then code, so output is
// deterministic.
func Sort(ds []Diagnostic) {
	sort.SliceStable(ds, func(i, j int) bool {
		a, b := ds[i], ds[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Span.Start != b.Span.Start {
			return a.Span.Start < b.Span.Start
		}
		return a.Code < b.Code
	})
}

// Write encodes a response as one JSON document followed by a newline, at
// the newest schema version.
func Write(w io.Writer, r Response) error { return WriteAt(w, SchemaVersion, r) }

// WriteAt encodes a response at a wire version: the response says that
// version, and at 0.2 its diagnostics carry no message, so that a 0.2
// consumer sees exactly the 0.2 shape. The caller's response and its
// diagnostics are left as they are: the diagnostics are copied.
func WriteAt(w io.Writer, version string, r Response) error {
	r.SchemaVersion = version
	ds := make([]Diagnostic, len(r.Diagnostics))
	copy(ds, r.Diagnostics)
	for i := range ds {
		if ds[i].Related == nil {
			ds[i].Related = []Related{}
		}
		if version == "0.2" {
			ds[i].Message = ""
		}
	}
	r.Diagnostics = ds
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(r)
}

// Render returns a human-readable one-line form used by `kern check --human`.
func Render(d Diagnostic) string {
	return fmt.Sprintf("%s:%d-%d: %s [%s rule %s]: %s", d.File, d.Span.Start, d.Span.End, d.Severity, d.Code, d.Rule, d.Message)
}
