package lsp

import (
	"fmt"
	"strings"

	"github.com/dshills/kern/compiler/analysis"
	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/edit"
	"github.com/dshills/kern/compiler/format"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/lower"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// formatting answers textDocument/formatting (spec §3.4).
func (s *Server) formatting(p documentFormattingParams) (any, *rpcError) {
	d := s.docs[p.TextDocument.URI]
	if d == nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "not an open document: " + p.TextDocument.URI}
	}
	name := d.path
	if name == "" {
		name = d.uri
	}
	out, ds := format.Format(&source.File{Name: name, Bytes: d.text})
	if len(ds) > 0 {
		return nil, nil
	}
	e, ok := MinimalEdit(d.text, out, s.enc)
	if !ok {
		return []TextEdit{}, nil
	}
	return []TextEdit{e}, nil
}

// kernDoc is the kern document of a URI, or nil.
func (s *Server) kernDoc(uri string) *document {
	if d := s.docs[uri]; d != nil && d.kern() {
		return d
	}
	return nil
}

// name is a hover candidate: a span of the document and its symbol.
type name struct {
	pos      ast.Pos
	sym      *resolve.Symbol
	resolved bool // an identifier use, which wins over a declaration of the same span
}

// hover answers textDocument/hover (spec R6).
func (s *Server) hover(p textDocumentPositionParams) (any, *rpcError) {
	d := s.kernDoc(p.TextDocument.URI)
	if d == nil {
		return nil, nil
	}
	pg := s.load(d)
	var text []byte
	for _, f := range pg.files {
		if f.Name == d.path {
			text = f.Bytes
		}
	}
	if text == nil {
		return nil, nil // the loader could not read the document's program
	}
	off := ToOffset(text, p.Position, s.enc)
	prog := s.opts.Resolve(pg.files)
	best, found := s.nameAt(prog, d.path, off)
	if !found {
		return nil, nil
	}
	var b strings.Builder
	b.WriteString("```kern\n" + signature(best.sym) + "\n```\n")
	if local(best.sym) && len(pg.diags) == 0 && len(prog.Diags) == 0 {
		if pl, ok := s.place(prog, best.sym); ok {
			b.WriteString(s.renderPlace(pl))
		}
	}
	return Hover{Contents: MarkupContent{Kind: "markdown", Value: b.String()}, Range: s.rangeOf(text, best.pos.Start, best.pos.End)}, nil
}

// nameAt is the innermost name of file whose span contains off, the end
// included.
func (s *Server) nameAt(prog *resolve.Program, file string, off int) (name, bool) {
	var best name
	found := false
	consider := func(n name) {
		if n.pos.Start > off || off > n.pos.End {
			return
		}
		w, bw := n.pos.End-n.pos.Start, best.pos.End-best.pos.Start
		if !found || w < bw || w == bw && n.resolved && !best.resolved {
			best, found = n, true
		}
	}
	for _, m := range prog.Modules {
		for _, f := range m.Files {
			if f.Name != file {
				continue
			}
			ast.Walk(f, func(n any) bool {
				if id, ok := n.(*ast.Ident); ok {
					if sym := prog.Info.Idents[id]; sym != nil {
						consider(name{pos: id.Pos, sym: sym, resolved: true})
					}
				}
				return false
			})
		}
	}
	for _, sym := range prog.Info.Decls {
		if sym != nil && sym.File == file && local(sym) {
			consider(name{pos: sym.Pos, sym: sym})
		}
	}
	return best, found
}

// local reports whether a symbol is a local, parameter or access binding.
func local(sym *resolve.Symbol) bool {
	return sym.Kind == resolve.SymLocal || sym.Kind == resolve.SymParam || sym.Kind == resolve.SymAccess
}

var convNames = map[ast.Conv]string{ast.ConvView: "view", ast.ConvEdit: "edit", ast.ConvOwn: "own", ast.ConvOut: "out"}

// signature is the hover's code line (spec R6).
func signature(sym *resolve.Symbol) string {
	switch sym.Kind {
	case resolve.SymLocal, resolve.SymParam, resolve.SymAccess:
		kind := map[resolve.SymbolKind]string{resolve.SymLocal: "local", resolve.SymParam: "param", resolve.SymAccess: "binding"}[sym.Kind]
		parts := []string{kind}
		if c, ok := convNames[sym.Conv]; ok {
			parts = append(parts, c)
		}
		line := strings.Join(parts, " ") + " " + sym.Name
		if sym.Type != nil {
			line += ": " + sym.Type.String()
		}
		return line
	case resolve.SymFunc:
		if f := sym.Func; f != nil {
			line := "func " + f.Module + "." + f.Name
			if f.Type != nil {
				line += ": " + f.Type.String()
			}
			return line
		}
		return "func " + sym.Name
	}
	kind := map[resolve.SymbolKind]string{resolve.SymType: "type", resolve.SymConst: "const", resolve.SymGlobal: "global", resolve.SymModule: "module", resolve.SymTypeParam: "type parameter"}[sym.Kind]
	return kind + " " + sym.Name
}

// place is the explain-ownership place of a local's declaration, on a
// program that checks cleanly and lowers.
func (s *Server) place(prog *resolve.Program, sym *resolve.Symbol) (diag.Place, bool) {
	if len(check.Check(prog)) > 0 {
		return diag.Place{}, false
	}
	mod, lds := lower.Lower(prog)
	if len(lds) > 0 || len(ir.Validate(mod)) > 0 {
		return diag.Place{}, false
	}
	span := diag.Span{Start: sym.Pos.Start, End: sym.Pos.End}
	for _, pl := range analysis.ExplainOwnership(prog, mod, sym.File, span).Places {
		if pl.Loc.Span == span {
			return pl, true
		}
	}
	return diag.Place{}, false
}

// renderPlace is the ownership part of a hover (spec R6).
func (s *Server) renderPlace(pl diag.Place) string {
	text := s.textOf(pl.Loc.File)
	line := func(off int) int { return ToPosition(text, off, s.enc).Line + 1 }
	var b strings.Builder
	fmt.Fprintf(&b, "\nstate: %s\n", pl.State)
	if len(pl.Accesses) > 0 {
		b.WriteString("\n")
		for _, a := range pl.Accesses {
			fmt.Fprintf(&b, "- %s at line %d\n", a.Kind, line(a.Span.Start))
		}
	}
	if pl.DropAt != nil {
		fmt.Fprintf(&b, "\ndropped at line %d\n", ToPosition(s.textOf(pl.DropAt.File), pl.DropAt.Span.Start, s.enc).Line+1)
	}
	if c := pl.Contract; c != nil {
		for _, kc := range []struct {
			kind    string
			clauses []string
		}{{"requires", c.Requires}, {"ensures", c.Ensures}, {"invariant", c.Invariant}} {
			for _, cl := range kc.clauses {
				fmt.Fprintf(&b, "\n%s %s\n", kc.kind, cl)
			}
		}
	}
	return b.String()
}

// intersects reports whether two ranges intersect, touching ends
// included (spec R7).
func intersects(a, b Range) bool { return !before(a.End, b.Start) && !before(b.End, a.Start) }

// wantsQuickFix reports whether a code-action request's only filter
// admits quickfix: no filter, or a kind that is quickfix or a prefix of
// it in the dotted hierarchy (spec R7).
func wantsQuickFix(only []string) bool {
	if only == nil {
		return true
	}
	for _, k := range only {
		if k == "" || k == "quickfix" || strings.HasPrefix("quickfix", k+".") {
			return true
		}
	}
	return false
}

// codeAction answers textDocument/codeAction (spec R7).
func (s *Server) codeAction(p codeActionParams) (any, *rpcError) {
	d := s.kernDoc(p.TextDocument.URI)
	if d == nil || !s.literal || !wantsQuickFix(p.Context.Only) {
		return []CodeAction{}, nil
	}
	if s.stale[d.uri] {
		delete(s.stale, d.uri)
		s.checkDoc(d)
	}
	actions := []CodeAction{}
	pb := s.pubs[d.uri]
	if pb == nil {
		return actions, nil
	}
	var pg *program
	for i, k := range pb.kern {
		if k.Fix == nil || !intersects(pb.lsp[i].Range, p.Range) {
			continue
		}
		if pg == nil {
			loaded := s.load(d)
			pg = &loaded
		}
		changed, failure := edit.Apply(pg.files, k.Fix.Ops)
		if failure != nil {
			continue
		}
		changes := map[string][]TextEdit{}
		for _, f := range pg.files {
			nb, ok := changed[f.Name]
			if !ok {
				continue
			}
			if e, ok := MinimalEdit(f.Bytes, nb, s.enc); ok {
				changes[s.uriOf(f.Name)] = []TextEdit{e}
			}
		}
		if len(changes) == 0 {
			continue
		}
		actions = append(actions, CodeAction{
			Title:       "Apply the compiler's fix for " + string(k.Code),
			Kind:        "quickfix",
			Diagnostics: []Diagnostic{pb.lsp[i]},
			Edit:        WorkspaceEdit{Changes: changes},
		})
	}
	return actions, nil
}
