package parser

import (
	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/lexer"
	"github.com/dshills/kern/compiler/source"
)

// Prologue is a file's module line and import declarations.
type Prologue struct {
	Module    string
	ModulePos ast.Pos // the span of the module name
	Imports   []*ast.Import
}

// ParsePrologue parses a file's prologue, the `module` line and the
// `import` declarations, with the productions Parse uses, and stops at the
// first declaration without parsing it. The whole file is lexed, as Parse
// lexes it, so a lexical error anywhere is reported as Parse reports it;
// a prologue that does not parse yields the SYNTAX diagnostic Parse would
// yield for it and no imports. Module discovery uses it to find a
// program's files without parsing their bodies.
func ParsePrologue(f *source.File) (pro Prologue, ds []diag.Diagnostic) {
	toks, lerrs := lexer.Lex(f.Bytes)
	for _, e := range lerrs {
		ds = append(ds, diag.New("SYNTAX", f.Name, diag.Span{Start: e.Start, End: e.End}, "%s", e.Msg))
	}
	if len(ds) > 0 {
		return Prologue{}, ds
	}
	p := &parser{file: f, toks: toks}
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(bailout); !ok {
				panic(r)
			}
			pro, ds = Prologue{}, p.diags
		}
	}()
	p.skipNewlines()
	p.expect(lexer.KwModule)
	name := p.expect(lexer.Ident)
	pro.Module = name.Text
	pro.ModulePos = tokPos(name)
	p.stmtEnd()
	p.skipNewlines()
	for p.at(lexer.KwImport) {
		t := p.next()
		s := p.expect(lexer.String)
		pro.Imports = append(pro.Imports, &ast.Import{Pos: span(tokPos(t), tokPos(s)), Path: s.Text})
		p.stmtEnd()
		p.skipNewlines()
	}
	if len(p.diags) > 0 {
		return Prologue{}, p.diags
	}
	return pro, nil
}
