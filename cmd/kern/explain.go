package main

import (
	"fmt"
	"io"
	"sort"

	"github.com/dshills/kern/compiler/ast"
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
func explainOwnership(req Request, srcs []*source.File, stdout, stderr io.Writer) int {
	if req.Span == nil {
		return fail(stdout, "API_MISSING_SPAN", "explain-ownership requires a span")
	}
	var target *source.File
	for _, f := range srcs {
		if f.Name == req.Span.File {
			target = f
		}
	}
	if target == nil || req.Span.Start < 0 || req.Span.Start > req.Span.End || req.Span.End > len(target.Bytes) || !target.OnBoundary(req.Span.Start) || !target.OnBoundary(req.Span.End) {
		return fail(stdout, "API_INVALID_SPAN", "span does not name a request file or violates the span rules")
	}
	prog := resolve.Resolve(srcs)
	ds := prog.Diags
	if len(ds) == 0 {
		ds = check.Check(prog)
	}
	if len(ds) > 0 {
		_ = diag.Write(stdout, diag.Response{SchemaVersion: diag.SchemaVersion, OK: false, Result: nil, Diagnostics: ds})
		return 2
	}
	mod, lds := lower.Lower(prog)
	if len(lds) > 0 {
		return internal(req, &InternalError{Err: fmt.Errorf("%s", lds[0].Message)}, stdout, stderr)
	}
	res := diag.ExplainOwnershipResult{Places: []diag.Place{}}
	intersects := func(p ast.Pos) bool { return p.Start < req.Span.End && req.Span.Start < p.End }
	// symbols by position, in order
	type entry struct {
		sym *resolve.Symbol
	}
	var entries []entry
	for key, sym := range prog.Info.Decls {
		if sym == nil || sym.File != req.Span.File || !intersects(sym.Pos) {
			continue
		}
		switch sym.Kind {
		case resolve.SymLocal, resolve.SymParam, resolve.SymAccess:
		default:
			continue
		}
		_ = key
		entries = append(entries, entry{sym: sym})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].sym.Pos.Start != entries[j].sym.Pos.Start {
			return entries[i].sym.Pos.Start < entries[j].sym.Pos.Start
		}
		return entries[i].sym.ID < entries[j].sym.ID
	})
	for _, e := range entries {
		sym := e.sym
		loc := diag.Loc{File: req.Span.File, Span: diag.Span{Start: sym.Pos.Start, End: sym.Pos.End}}
		p := diag.Place{Loc: loc, Owner: loc, State: "owned", Accesses: []diag.AccessEntry{}, Label: diag.LabelProven}
		if sym.Kind == resolve.SymAccess || (sym.Kind == resolve.SymParam && (sym.Conv == ast.ConvView || sym.Conv == ast.ConvEdit)) {
			p.State = "access"
		}
		for _, f := range mod.Funcs {
			var local *ir.Local
			for _, l := range f.Locals {
				if l.Name == sym.Name && !l.Temp {
					local = l
				}
			}
			if local == nil {
				continue
			}
			for _, b := range f.Blocks {
				for _, in := range b.Instrs {
					switch i := in.(type) {
					case *ir.AccessBegin:
						if i.Place.Local == local {
							kind := "view"
							if i.Edit {
								kind = "edit"
							}
							p.Accesses = append(p.Accesses, diag.AccessEntry{Kind: kind, Span: diag.Span{Start: i.Pos.Start, End: i.Pos.End}, Root: loc})
						}
					case *ir.Move:
						if i.Src.Local == local && len(i.Src.Path) == 0 {
							p.Accesses = append(p.Accesses, diag.AccessEntry{Kind: "consume", Span: diag.Span{Start: sym.Pos.Start, End: sym.Pos.End}, Root: loc})
						}
					case *ir.Drop:
						if i.Place.Local == local && len(i.Place.Path) == 0 && p.DropAt == nil {
							p.DropAt = &diag.Loc{File: req.Span.File, Span: diag.Span{Start: i.Pos.Start, End: i.Pos.End}}
						}
					}
				}
			}
			if f.Contract != nil {
				p.Contract = &diag.Contract{Invariant: f.Contract.Invariant, Requires: f.Contract.Requires, Ensures: f.Contract.Ensures}
			}
		}
		res.Places = append(res.Places, p)
	}
	resp := diag.Response{SchemaVersion: diag.SchemaVersion, OK: true, Result: res, Diagnostics: []diag.Diagnostic{}}
	if err := diag.Write(stdout, resp); err != nil {
		return 3
	}
	return 0
}
