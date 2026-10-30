package analysis

import (
	"sort"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/resolve"
)

// ExplainOwnership is the result of the explain-ownership machine command
// (spec §16) for a checked program and its lowered, validated IR: for
// every owned local, parameter or access binding of file whose
// declaration intersects span, its owner, state, the accesses the IR
// opens on it, its static drop point, and the contract of the function
// (when any), in declaration order. The IR is the lowering as written:
// inlining would rename a helper's locals and drop its body, and the
// symbols are matched to IR locals by name, in the declaring function
// only (every instantiation of it): the innermost function of the file,
// library code excluded, whose span holds the declaration
// (specs/language-server/SPEC.md R13).
func ExplainOwnership(prog *resolve.Program, mod *ir.Module, file string, span diag.Span) diag.ExplainOwnershipResult {
	res := diag.ExplainOwnershipResult{Places: []diag.Place{}}
	intersects := func(p ast.Pos) bool { return p.Start < span.End && span.Start < p.End }
	var syms []*resolve.Symbol
	for _, sym := range prog.Info.Decls {
		if sym == nil || sym.File != file || !intersects(sym.Pos) {
			continue
		}
		switch sym.Kind {
		case resolve.SymLocal, resolve.SymParam, resolve.SymAccess:
		default:
			continue
		}
		syms = append(syms, sym)
	}
	sort.Slice(syms, func(i, j int) bool {
		if syms[i].Pos.Start != syms[j].Pos.Start {
			return syms[i].Pos.Start < syms[j].Pos.Start
		}
		return syms[i].ID < syms[j].ID
	})
	for _, sym := range syms {
		res.Places = append(res.Places, place(mod, file, sym))
	}
	return res
}

// place is the explain-ownership place of one symbol.
func place(mod *ir.Module, file string, sym *resolve.Symbol) diag.Place {
	loc := diag.Loc{File: file, Span: diag.Span{Start: sym.Pos.Start, End: sym.Pos.End}}
	p := diag.Place{Loc: loc, Owner: loc, State: "owned", Accesses: []diag.AccessEntry{}, Label: diag.LabelProven}
	if sym.Kind == resolve.SymAccess || (sym.Kind == resolve.SymParam && (sym.Conv == ast.ConvView || sym.Conv == ast.ConvEdit)) {
		p.State = "access"
	}
	for _, f := range declaring(mod, file, sym.Pos) {
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
						p.DropAt = &diag.Loc{File: file, Span: diag.Span{Start: i.Pos.Start, End: i.Pos.End}}
					}
				}
			}
		}
		if f.Contract != nil {
			p.Contract = &diag.Contract{Invariant: f.Contract.Invariant, Requires: f.Contract.Requires, Ensures: f.Contract.Ensures}
		}
	}
	return p
}

// declaring returns the IR functions a declaration at pos of file belongs
// to: of the non-library functions of file whose span holds pos, those
// with the narrowest span, which are the instantiations of one function
// (a closure, lowered to a function of its own, is narrower than the
// function it is written in).
func declaring(mod *ir.Module, file string, pos ast.Pos) []*ir.Func {
	var out []*ir.Func
	width := -1
	for _, f := range mod.Funcs {
		if f.Library || f.File != file || f.Pos.Start > pos.Start || pos.End > f.Pos.End {
			continue
		}
		w := f.Pos.End - f.Pos.Start
		switch {
		case width < 0 || w < width:
			out, width = []*ir.Func{f}, w
		case w == width && f.Pos == out[0].Pos:
			out = append(out, f)
		}
	}
	return out
}
