package main

import (
	"fmt"
	"io"

	"github.com/dshills/kern/compiler/analysis"
	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/lower"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// explainOwnership implements the explain-ownership machine command (spec
// §16): for every owned local, parameter or access binding whose declaration
// intersects the request span, its owner, state, the accesses the IR opens on
// it, its static drop point, and the contract of the function (when any).
func explainOwnership(req Request, wire string, srcs []*source.File, stdout, stderr io.Writer) int {
	if req.Span == nil {
		return failAt(stdout, wire, "API_MISSING_SPAN", "explain-ownership requires a span")
	}
	var target *source.File
	for _, f := range srcs {
		if f.Name == req.Span.File {
			target = f
		}
	}
	if target == nil || req.Span.Start < 0 || req.Span.Start > req.Span.End || req.Span.End > len(target.Bytes) || !target.OnBoundary(req.Span.Start) || !target.OnBoundary(req.Span.End) {
		return failAt(stdout, wire, "API_INVALID_SPAN", "span does not name a request file or violates the span rules")
	}
	prog := resolve.Resolve(srcs)
	ds := prog.Diags
	if len(ds) == 0 {
		ds = check.Check(prog)
	}
	if len(ds) > 0 {
		_ = diag.WriteAt(stdout, wire, diag.Response{OK: false, Result: nil, Diagnostics: ds})
		return 2
	}
	mod, lds := lower.Lower(prog)
	if len(lds) > 0 {
		return internal(req, wire, &InternalError{Err: fmt.Errorf("%s", lds[0].Message)}, stdout, stderr)
	}
	// ownership is explained on the lowered IR as written: inlining would
	// rename a helper's locals and drop its body, and the symbols below
	// are matched to IR locals by name
	if errs := ir.Validate(mod); len(errs) > 0 {
		return internal(req, wire, &InternalError{Err: fmt.Errorf("IR validation: %v", errs[0])}, stdout, stderr)
	}
	res := analysis.ExplainOwnership(prog, mod, req.Span.File, diag.Span{Start: req.Span.Start, End: req.Span.End})
	resp := diag.Response{OK: true, Result: res, Diagnostics: []diag.Diagnostic{}}
	if err := diag.WriteAt(stdout, wire, resp); err != nil {
		return 3
	}
	return 0
}
