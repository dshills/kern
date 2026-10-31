// Package parser builds the canonical AST from tokens (spec/grammar.ebnf).
package parser

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/lexer"
	"github.com/dshills/kern/compiler/source"
)

// Parse parses one file. Syntax errors are reported as SYNTAX diagnostics;
// the returned file is nil when any error occurred.
func Parse(f *source.File) (*ast.File, []diag.Diagnostic) { return ParseWith(f, Options{}) }

// ParseWith parses with options.
func ParseWith(f *source.File, opts Options) (file *ast.File, ds []diag.Diagnostic) {
	toks, lerrs := lexer.Lex(f.Bytes)
	for _, e := range lerrs {
		ds = append(ds, diag.New("SYNTAX", f.Name, diag.Span{Start: e.Start, End: e.End}, "%s", e.Msg))
	}
	if len(ds) > 0 {
		return nil, ds
	}
	p := &parser{file: f, toks: toks, opts: opts}
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(bailout); !ok {
				panic(r)
			}
			file, ds = nil, p.diags
		}
	}()
	out := p.parseFile()
	if len(p.diags) > 0 {
		return nil, p.diags
	}
	return out, nil
}

type bailout struct{}

// Options control parsing.
type Options struct {
	// AllowBodyless permits `func` declarations without a body (intrinsic
	// declarations in the standard-library prelude).
	AllowBodyless bool
}

type parser struct {
	opts    Options
	file    *source.File
	toks    []lexer.Token
	pos     int
	nesting int  // >0 inside ( ) or [ ]: newlines are insignificant
	noLit   bool // struct literals not allowed (if/while/for/match headers)
	diags   []diag.Diagnostic
}

func (p *parser) cur() lexer.Token {
	for p.nesting > 0 && p.toks[p.pos].Kind == lexer.Newline {
		p.pos++
	}
	return p.toks[p.pos]
}

func (p *parser) peekKind(n int) lexer.Kind {
	i := p.pos
	seen := 0
	for ; i < len(p.toks); i++ {
		if p.nesting > 0 && p.toks[i].Kind == lexer.Newline {
			continue
		}
		if seen == n {
			return p.toks[i].Kind
		}
		seen++
	}
	return lexer.EOF
}

func (p *parser) next() lexer.Token {
	t := p.cur()
	if t.Kind != lexer.EOF {
		p.pos++
	}
	return t
}

func (p *parser) at(k lexer.Kind) bool { return p.cur().Kind == k }

func (p *parser) accept(k lexer.Kind) bool {
	if p.at(k) {
		p.next()
		return true
	}
	return false
}

func (p *parser) errorf(t lexer.Token, format string, args ...any) {
	p.diags = append(p.diags, diag.New("SYNTAX", p.file.Name, diag.Span{Start: t.Start, End: t.End}, format, args...))
	panic(bailout{})
}

func (p *parser) expect(k lexer.Kind) lexer.Token {
	t := p.cur()
	if t.Kind != k {
		p.errorf(t, "expected %s, found %s", k, describe(t))
	}
	return p.next()
}

func describe(t lexer.Token) string {
	switch t.Kind {
	case lexer.Ident, lexer.Int, lexer.Float:
		return fmt.Sprintf("%s %q", t.Kind, t.Text)
	case lexer.String:
		return "string literal"
	}
	return t.Kind.String()
}

func (p *parser) skipNewlines() {
	for p.toks[p.pos].Kind == lexer.Newline || p.toks[p.pos].Kind == lexer.Semi {
		p.pos++
	}
}

func (p *parser) stmtEnd() {
	t := p.cur()
	switch t.Kind {
	case lexer.Newline, lexer.Semi:
		p.next()
	case lexer.RBrace, lexer.EOF:
	default:
		p.errorf(t, "expected end of statement, found %s", describe(t))
	}
}

func span(a, b ast.Pos) ast.Pos    { return ast.Pos{Start: a.Start, End: b.End} }
func tokPos(t lexer.Token) ast.Pos { return ast.Pos{Start: t.Start, End: t.End} }

// ---- file and declarations ----

func (p *parser) parseFile() *ast.File {
	p.skipNewlines()
	f := &ast.File{Name: p.file.Name}
	start := p.cur()
	p.expect(lexer.KwModule)
	f.Module = p.expect(lexer.Ident).Text
	p.stmtEnd()
	p.skipNewlines()
	for p.at(lexer.KwImport) {
		t := p.next()
		s := p.expect(lexer.String)
		f.Imports = append(f.Imports, &ast.Import{Pos: span(tokPos(t), tokPos(s)), Path: s.Text})
		p.stmtEnd()
		p.skipNewlines()
	}
	for !p.at(lexer.EOF) {
		f.Decls = append(f.Decls, p.parseDecl())
		p.skipNewlines()
	}
	f.Pos = ast.Pos{Start: start.Start, End: p.cur().End}
	return f
}

func (p *parser) parseDecl() ast.Decl {
	t := p.cur()
	switch t.Kind {
	case lexer.KwView:
		if p.peekKind(1) == lexer.KwStruct {
			p.next()
			d := p.parseStruct()
			d.View = true
			d.Start = t.Start
			return d
		}
	case lexer.KwStruct:
		return p.parseStruct()
	case lexer.KwUnion:
		return p.parseUnion()
	case lexer.KwInterface:
		return p.parseInterface()
	case lexer.KwImpl:
		return p.parseImpl()
	case lexer.KwFunc, lexer.KwContract:
		fn := p.parseFunc(true)
		if fn.Body == nil {
			p.stmtEnd()
		}
		return fn
	case lexer.KwExtern:
		return p.parseExtern()
	case lexer.KwConst:
		return p.parseConst()
	case lexer.KwThreadLocal:
		return p.parseGlobal()
	case lexer.KwUnsafe:
		if p.peekKind(1) == lexer.KwAssert {
			return p.parseAssert()
		}
	}
	p.errorf(t, "expected declaration, found %s", describe(t))
	return nil
}

func (p *parser) parseTypeParams() []*ast.TypeParam {
	if !p.at(lexer.LBrack) {
		return nil
	}
	p.next()
	p.nesting++
	var out []*ast.TypeParam
	for {
		n := p.expect(lexer.Ident)
		tp := &ast.TypeParam{Pos: tokPos(n), Name: n.Text}
		if p.accept(lexer.Colon) {
			for {
				c := p.parseTypeName()
				tp.Constraints = append(tp.Constraints, c)
				tp.End = c.End
				if !p.accept(lexer.Plus) {
					break
				}
			}
		}
		out = append(out, tp)
		if !p.accept(lexer.Comma) {
			break
		}
	}
	p.nesting--
	p.expect(lexer.RBrack)
	return out
}

func (p *parser) parseStruct() *ast.StructDecl {
	t := p.expect(lexer.KwStruct)
	d := &ast.StructDecl{Pos: tokPos(t)}
	d.Name = p.expect(lexer.Ident).Text
	d.TypeParams = p.parseTypeParams()
	if p.accept(lexer.KwNocopy) {
		d.NoCopy = true
	}
	if p.at(lexer.Ident) && p.toks[p.pos].Text == "packed" {
		p.next()
		d.Packed = true
	}
	p.expect(lexer.LBrace)
	p.skipNewlines()
	for !p.at(lexer.RBrace) {
		n := p.expect(lexer.Ident)
		p.expect(lexer.Colon)
		ty := p.parseType()
		d.Fields = append(d.Fields, &ast.Field{Pos: span(tokPos(n), ty.Span()), Name: n.Text, Type: ty})
		p.stmtEnd()
		p.skipNewlines()
	}
	d.End = p.expect(lexer.RBrace).End
	return d
}

func (p *parser) parseUnion() *ast.UnionDecl {
	t := p.expect(lexer.KwUnion)
	d := &ast.UnionDecl{Pos: tokPos(t)}
	d.Name = p.expect(lexer.Ident).Text
	d.TypeParams = p.parseTypeParams()
	p.expect(lexer.LBrace)
	p.skipNewlines()
	for !p.at(lexer.RBrace) {
		n := p.expect(lexer.Ident)
		v := &ast.Variant{Pos: tokPos(n), Name: n.Text}
		if p.at(lexer.LParen) {
			p.next()
			p.nesting++
			for !p.at(lexer.RParen) {
				var fld *ast.Field
				if p.at(lexer.Ident) && p.peekKind(1) == lexer.Colon {
					fn := p.next()
					p.next()
					ty := p.parseType()
					fld = &ast.Field{Pos: span(tokPos(fn), ty.Span()), Name: fn.Text, Type: ty}
				} else {
					ty := p.parseType()
					fld = &ast.Field{Pos: ty.Span(), Type: ty}
				}
				v.Fields = append(v.Fields, fld)
				if !p.accept(lexer.Comma) {
					break
				}
			}
			p.nesting--
			v.End = p.expect(lexer.RParen).End
		}
		d.Variants = append(d.Variants, v)
		p.stmtEnd()
		p.skipNewlines()
	}
	d.End = p.expect(lexer.RBrace).End
	return d
}

func (p *parser) parseInterface() *ast.InterfaceDecl {
	t := p.expect(lexer.KwInterface)
	d := &ast.InterfaceDecl{Pos: tokPos(t)}
	d.Name = p.expect(lexer.Ident).Text
	d.TypeParams = p.parseTypeParams()
	p.expect(lexer.LBrace)
	p.skipNewlines()
	for !p.at(lexer.RBrace) {
		d.Methods = append(d.Methods, p.parseFunc(false))
		p.stmtEnd()
		p.skipNewlines()
	}
	d.End = p.expect(lexer.RBrace).End
	return d
}

func (p *parser) parseImpl() *ast.ImplDecl {
	t := p.expect(lexer.KwImpl)
	d := &ast.ImplDecl{Pos: tokPos(t)}
	d.TypeParams = p.parseTypeParams()
	first := p.parseType()
	if p.accept(lexer.KwFor) {
		tn, ok := first.(*ast.TypeName)
		if !ok {
			p.errorf(p.cur(), "interface name expected before 'for'")
		}
		d.Iface = tn
		d.Target = p.parseType()
	} else {
		d.Target = first
	}
	p.expect(lexer.LBrace)
	p.skipNewlines()
	for !p.at(lexer.RBrace) {
		if p.at(lexer.KwDestructor) {
			dt := p.next()
			p.expect(lexer.LParen)
			p.expect(lexer.KwEdit)
			p.expect(lexer.KwSelf)
			p.expect(lexer.RParen)
			body := p.parseBlock()
			d.Destructor = &ast.Destructor{Pos: span(tokPos(dt), body.Pos), Body: body}
		} else {
			fn := p.parseFunc(true)
			d.Funcs = append(d.Funcs, fn)
			if fn.Body == nil {
				p.stmtEnd()
			}
		}
		p.skipNewlines()
	}
	d.End = p.expect(lexer.RBrace).End
	return d
}

func (p *parser) parseContract() *ast.Contract {
	t := p.expect(lexer.KwContract)
	c := &ast.Contract{Pos: tokPos(t)}
	p.expect(lexer.LBrace)
	p.skipNewlines()
	for !p.at(lexer.RBrace) {
		k := p.next()
		s := p.expect(lexer.String)
		switch k.Kind {
		case lexer.KwInvariant:
			c.Invariant = append(c.Invariant, s.Text)
		case lexer.KwRequires:
			c.Requires = append(c.Requires, s.Text)
		case lexer.KwEnsures:
			c.Ensures = append(c.Ensures, s.Text)
		default:
			p.errorf(k, "expected invariant, requires or ensures")
		}
		p.stmtEnd()
		p.skipNewlines()
	}
	c.End = p.expect(lexer.RBrace).End
	p.skipNewlines()
	return c
}

// parseFunc parses a function declaration; withBody=false parses an interface
// method signature.
func (p *parser) parseFunc(withBody bool) *ast.FuncDecl {
	d := &ast.FuncDecl{}
	if p.at(lexer.KwContract) {
		d.Contract = p.parseContract()
		d.Pos = d.Contract.Pos
	}
	t := p.expect(lexer.KwFunc)
	if d.Contract == nil {
		d.Pos = tokPos(t)
	}
	d.Name = p.expect(lexer.Ident).Text
	d.TypeParams = p.parseTypeParams()
	d.Params = p.parseParams()
	d.Ret = p.parseRet()
	if p.at(lexer.Ident) && p.cur().Text == "where" { // contextual keyword
		p.next()
		for {
			n := p.expect(lexer.Ident)
			tp := &ast.TypeParam{Pos: tokPos(n), Name: n.Text}
			p.expect(lexer.Colon)
			for {
				c := p.parseTypeName()
				tp.Constraints = append(tp.Constraints, c)
				tp.End = c.End
				if !p.accept(lexer.Plus) {
					break
				}
			}
			d.Where = append(d.Where, tp)
			if !p.accept(lexer.Comma) {
				break
			}
		}
	}
	if p.at(lexer.Ident) && p.cur().Text == "release" { // contextual keyword
		p.next()
		d.Release = p.expect(lexer.Ident).Text
	}
	if withBody && (!p.opts.AllowBodyless || p.at(lexer.LBrace)) {
		d.Body = p.parseBlock()
		d.End = d.Body.End
	} else {
		d.End = p.toks[p.pos-1].End
	}
	return d
}

func (p *parser) parseParams() []*ast.Param {
	p.expect(lexer.LParen)
	p.nesting++
	var out []*ast.Param
	for !p.at(lexer.RParen) {
		out = append(out, p.parseParam())
		if !p.accept(lexer.Comma) {
			break
		}
	}
	p.nesting--
	p.expect(lexer.RParen)
	return out
}

func (p *parser) parseParam() *ast.Param {
	t := p.cur()
	if (t.Kind == lexer.KwView || t.Kind == lexer.KwEdit || t.Kind == lexer.KwOwn) && p.peekKind(1) == lexer.KwSelf {
		p.next()
		s := p.next()
		conv := map[lexer.Kind]ast.Conv{lexer.KwView: ast.ConvView, lexer.KwEdit: ast.ConvEdit, lexer.KwOwn: ast.ConvOwn}[t.Kind]
		return &ast.Param{Pos: span(tokPos(t), tokPos(s)), Name: "self", Conv: conv, Self: true}
	}
	n := p.expect(lexer.Ident)
	p.expect(lexer.Colon)
	conv, ty := p.parseParamType()
	return &ast.Param{Pos: span(tokPos(n), ty.Span()), Name: n.Text, Conv: conv, Type: ty}
}

func (p *parser) parseParamType() (ast.Conv, ast.TypeExpr) {
	switch p.cur().Kind {
	case lexer.KwOwn:
		p.next()
		return ast.ConvOwn, p.parseType()
	case lexer.KwOut:
		p.next()
		return ast.ConvOut, p.parseType()
	}
	ty := p.parseType()
	if a, ok := ty.(*ast.AccessType); ok {
		if a.Edit {
			return ast.ConvEdit, ty
		}
		return ast.ConvView, ty
	}
	return ast.ConvValue, ty
}

func (p *parser) parseRet() []ast.TypeExpr {
	if !p.accept(lexer.Arrow) {
		return nil
	}
	if p.at(lexer.LParen) && p.peekKind(1) != lexer.RParen {
		p.next()
		p.nesting++
		var out []ast.TypeExpr
		for {
			out = append(out, p.parseType())
			if !p.accept(lexer.Comma) {
				break
			}
		}
		p.nesting--
		p.expect(lexer.RParen)
		return out
	}
	return []ast.TypeExpr{p.parseType()}
}

func (p *parser) parseExtern() ast.Decl {
	t := p.expect(lexer.KwExtern)
	abi := p.expect(lexer.String)
	p.expect(lexer.KwFunc)
	d := &ast.FuncDecl{Pos: tokPos(t), Extern: abi.Text}
	d.Name = p.expect(lexer.Ident).Text
	d.Params = p.parseParams()
	d.Ret = p.parseRet()
	d.End = p.toks[p.pos-1].End
	if p.at(lexer.LBrace) {
		// an exported function: defined here under its C symbol
		d.Body = p.parseBlock()
		d.End = p.toks[p.pos-1].End
		return d
	}
	p.stmtEnd()
	return d
}

func (p *parser) parseConst() ast.Decl {
	t := p.expect(lexer.KwConst)
	d := &ast.ConstDecl{Pos: tokPos(t)}
	d.Name = p.expect(lexer.Ident).Text
	p.expect(lexer.Colon)
	d.Type = p.parseType()
	p.expect(lexer.Assign)
	d.Value = p.parseExpr()
	d.End = d.Value.Span().End
	p.stmtEnd()
	return d
}

func (p *parser) parseGlobal() ast.Decl {
	t := p.expect(lexer.KwThreadLocal)
	d := &ast.GlobalDecl{Pos: tokPos(t)}
	d.Name = p.expect(lexer.Ident).Text
	p.expect(lexer.Colon)
	d.Type = p.parseType()
	p.expect(lexer.Assign)
	d.Value = p.parseExpr()
	d.End = d.Value.Span().End
	p.stmtEnd()
	return d
}

func (p *parser) parseAssert() ast.Decl {
	t := p.expect(lexer.KwUnsafe)
	p.expect(lexer.KwAssert)
	m := p.expect(lexer.Ident)
	p.expect(lexer.KwFor)
	tn := p.parseTypeName()
	p.stmtEnd()
	return &ast.AssertDecl{Pos: span(tokPos(t), tn.Pos), Marker: m.Text, Type: tn}
}

// ---- types ----

var primNames = map[string]bool{
	"i8": true, "i16": true, "i32": true, "i64": true, "isize": true,
	"u8": true, "u16": true, "u32": true, "u64": true, "usize": true,
	"f32": true, "f64": true, "bool": true,
}

// IsPrim reports whether name is a primitive type name.
func IsPrim(name string) bool { return primNames[name] }

func (p *parser) parseType() ast.TypeExpr {
	t := p.cur()
	if t.Kind == lexer.Ident && t.Text == "closure" && p.peekKind(1) == lexer.LParen {
		// closure(P...) -> R, a second-class callable (contextual keyword)
		p.next()
		ft := &ast.FuncType{Pos: tokPos(t), Closure: true}
		p.expect(lexer.LParen)
		p.nesting++
		for !p.at(lexer.RParen) {
			conv, ty := p.parseParamType()
			ft.Params = append(ft.Params, &ast.Param{Pos: ty.Span(), Conv: conv, Type: ty})
			if !p.accept(lexer.Comma) {
				break
			}
		}
		p.nesting--
		p.expect(lexer.RParen)
		ft.Ret = p.parseRet()
		ft.End = p.toks[p.pos-1].End
		return ft
	}
	switch t.Kind {
	case lexer.LBrack:
		p.next()
		p.nesting++
		if p.at(lexer.RBrack) {
			p.nesting--
			p.next()
			elem := p.parseType()
			return &ast.SliceType{Pos: span(tokPos(t), elem.Span()), Elem: elem}
		}
		n := p.expect(lexer.Int)
		p.nesting--
		p.expect(lexer.RBrack)
		elem := p.parseType()
		ln, err := strconv.Atoi(strings.ReplaceAll(n.Text, "_", ""))
		if err != nil || ln < 0 {
			p.errorf(n, "array length must be a non-negative integer literal")
		}
		return &ast.ArrayType{Pos: span(tokPos(t), elem.Span()), Len: ln, Elem: elem}
	case lexer.KwView, lexer.KwEdit:
		p.next()
		a := &ast.AccessType{Pos: tokPos(t), Edit: t.Kind == lexer.KwEdit}
		p.expect(lexer.LBrack)
		p.nesting++
		if p.at(lexer.RBrack) {
			p.nesting--
			p.next()
			a.Seq = true
			a.Elem = p.parseType()
		} else {
			a.Elem = p.parseType()
			p.nesting--
			p.expect(lexer.RBrack)
		}
		a.End = a.Elem.Span().End
		return a
	case lexer.KwFunc:
		p.next()
		ft := &ast.FuncType{Pos: tokPos(t)}
		p.expect(lexer.LParen)
		p.nesting++
		for !p.at(lexer.RParen) {
			conv, ty := p.parseParamType()
			ft.Params = append(ft.Params, &ast.Param{Pos: ty.Span(), Conv: conv, Type: ty})
			if !p.accept(lexer.Comma) {
				break
			}
		}
		p.nesting--
		p.expect(lexer.RParen)
		ft.Ret = p.parseRet()
		ft.End = p.toks[p.pos-1].End
		return ft
	case lexer.LParen:
		p.next()
		e := p.expect(lexer.RParen)
		return &ast.UnitType{Pos: span(tokPos(t), tokPos(e))}
	case lexer.Ident:
		if primNames[t.Text] {
			p.next()
			return &ast.PrimType{Pos: tokPos(t), Name: t.Text}
		}
		if t.Text == "RawPtr" {
			p.next()
			p.expect(lexer.LBrack)
			p.nesting++
			elem := p.parseType()
			p.nesting--
			e := p.expect(lexer.RBrack)
			return &ast.RawPtrType{Pos: span(tokPos(t), tokPos(e)), Elem: elem}
		}
		return p.parseTypeName()
	}
	p.errorf(t, "expected type, found %s", describe(t))
	return nil
}

func (p *parser) parseTypeName() *ast.TypeName {
	n := p.expect(lexer.Ident)
	tn := &ast.TypeName{Pos: tokPos(n), Name: n.Text}
	if p.at(lexer.Dot) && p.peekKind(1) == lexer.Ident {
		p.next()
		m := p.next()
		tn.Pkg, tn.Name = n.Text, m.Text
		tn.End = m.End
	}
	if p.at(lexer.LBrack) {
		p.next()
		p.nesting++
		for {
			tn.Args = append(tn.Args, p.parseType())
			if !p.accept(lexer.Comma) {
				break
			}
		}
		p.nesting--
		tn.End = p.expect(lexer.RBrack).End
	}
	return tn
}

// ---- statements ----

func (p *parser) parseBlock() *ast.Block {
	t := p.expect(lexer.LBrace)
	b := &ast.Block{Pos: tokPos(t)}
	saved := p.nesting
	p.nesting = 0
	p.skipNewlines()
	for !p.at(lexer.RBrace) {
		if p.at(lexer.EOF) {
			p.errorf(p.cur(), "unexpected end of file in block")
		}
		b.Stmts = append(b.Stmts, p.parseStmt())
		p.skipNewlines()
	}
	b.End = p.expect(lexer.RBrace).End
	p.nesting = saved
	return b
}

func (p *parser) parseStmt() ast.Stmt {
	t := p.cur()
	switch t.Kind {
	case lexer.KwVar:
		p.next()
		n := p.expect(lexer.Ident)
		p.expect(lexer.Colon)
		ty := p.parseType()
		d := &ast.VarDecl{Pos: span(tokPos(t), ty.Span()), Name: n.Text, Type: ty}
		if p.accept(lexer.Assign) {
			d.Init = p.parseExpr()
			d.End = d.Init.Span().End
		}
		p.stmtEnd()
		return d
	case lexer.KwWith:
		return p.parseWith()
	case lexer.KwMatch:
		return p.parseMatch()
	case lexer.KwFor:
		p.next()
		v := p.expect(lexer.Ident)
		var v2 lexer.Token
		if p.accept(lexer.Comma) {
			v2 = p.expect(lexer.Ident)
		}
		p.expect(lexer.KwIn)
		iter := p.parseExprNoLit()
		body := p.parseBlock()
		f := &ast.For{Pos: span(tokPos(t), body.Pos), Var: v.Text, VarPos: tokPos(v), Iter: iter, Body: body}
		if v2.Text != "" {
			f.Var2, f.Var2Pos = v2.Text, tokPos(v2)
		}
		return f
	case lexer.KwWhile:
		p.next()
		c := p.parseExprNoLit()
		body := p.parseBlock()
		return &ast.While{Pos: span(tokPos(t), body.Pos), Cond: c, Body: body}
	case lexer.KwIf:
		return p.parseIf()
	case lexer.KwReturn:
		p.next()
		r := &ast.Return{Pos: tokPos(t)}
		if !p.at(lexer.Newline) && !p.at(lexer.Semi) && !p.at(lexer.RBrace) {
			r.Value = p.parseExpr()
			r.End = r.Value.Span().End
		}
		p.stmtEnd()
		return r
	case lexer.KwBreak:
		p.next()
		p.stmtEnd()
		return &ast.Break{Pos: tokPos(t)}
	case lexer.KwContinue:
		p.next()
		p.stmtEnd()
		return &ast.Continue{Pos: tokPos(t)}
	case lexer.KwUnsafe:
		p.next()
		body := p.parseBlock()
		return &ast.Unsafe{Pos: span(tokPos(t), body.Pos), Body: body}
	case lexer.LBrace:
		return p.parseBlock()
	case lexer.Ident:
		if p.peekKind(1) == lexer.ColonEq {
			p.next()
			p.next()
			v := p.parseExpr()
			p.stmtEnd()
			return &ast.ShortDecl{Pos: span(tokPos(t), v.Span()), Name: t.Text, Value: v}
		}
	}
	e := p.parseExpr()
	if p.accept(lexer.Assign) {
		v := p.parseExpr()
		p.stmtEnd()
		return &ast.Assign{Pos: span(e.Span(), v.Span()), Target: e, Value: v}
	}
	p.stmtEnd()
	return &ast.ExprStmt{Pos: e.Span(), X: e}
}

func (p *parser) parseWith() ast.Stmt {
	t := p.expect(lexer.KwWith)
	w := &ast.With{Pos: tokPos(t)}
	for {
		n := p.expect(lexer.Ident)
		w.Names = append(w.Names, n.Text)
		w.NamePos = append(w.NamePos, tokPos(n))
		if !p.accept(lexer.Comma) {
			break
		}
	}
	p.expect(lexer.ColonEq)
	w.Subject = p.parseExprNoLit()
	w.Body = p.parseBlock()
	w.End = w.Body.End
	return w
}

func (p *parser) parseMatch() ast.Stmt {
	t := p.expect(lexer.KwMatch)
	m := &ast.Match{Pos: tokPos(t)}
	if p.at(lexer.KwMove) {
		mt := p.next()
		n := p.expect(lexer.Ident)
		m.Move = true
		m.Subject = &ast.Ident{Pos: span(tokPos(mt), tokPos(n)), Name: n.Text}
	} else {
		m.Subject = p.parseExprNoLit()
	}
	p.expect(lexer.LBrace)
	p.skipNewlines()
	for !p.at(lexer.RBrace) {
		m.Arms = append(m.Arms, p.parseArm())
		p.skipNewlines()
	}
	m.End = p.expect(lexer.RBrace).End
	return m
}

func (p *parser) parseArm() *ast.Arm {
	t := p.cur()
	a := &ast.Arm{Pos: tokPos(t)}
	if p.accept(lexer.Underscore) {
		a.Wild = true
	} else {
		n := p.expect(lexer.Ident)
		a.Variant = n.Text
		for p.at(lexer.Dot) {
			p.next()
			m := p.expect(lexer.Ident)
			if a.Qual == nil {
				a.Qual = &ast.TypeName{Pos: tokPos(n), Name: n.Text}
			} else {
				a.Qual.Pkg, a.Qual.Name = a.Qual.Name, a.Variant
			}
			a.Variant = m.Text
		}
		if p.at(lexer.LParen) {
			p.next()
			p.nesting++
			for !p.at(lexer.RParen) {
				b := p.expect(lexer.Ident)
				a.Binds = append(a.Binds, b.Text)
				a.BindPos = append(a.BindPos, tokPos(b))
				if !p.accept(lexer.Comma) {
					break
				}
			}
			p.nesting--
			p.expect(lexer.RParen)
		}
	}
	p.expect(lexer.FatArrow)
	if p.at(lexer.LBrace) {
		a.Body = p.parseBlock()
	} else {
		s := p.parseStmt()
		a.Body = &ast.Block{Pos: s.Span(), Stmts: []ast.Stmt{s}}
		a.End = s.Span().End
		return a
	}
	a.End = a.Body.End
	p.stmtEnd()
	return a
}

func (p *parser) parseIf() ast.Stmt {
	t := p.expect(lexer.KwIf)
	s := &ast.If{Pos: tokPos(t)}
	s.Cond = p.parseExprNoLit()
	s.Then = p.parseBlock()
	s.End = s.Then.End
	if p.accept(lexer.KwElse) {
		if p.at(lexer.KwIf) {
			s.Else = p.parseIf()
		} else {
			s.Else = p.parseBlock()
		}
		s.End = s.Else.Span().End
	}
	return s
}

// ---- expressions ----

func (p *parser) parseExprNoLit() ast.Expr {
	saved := p.noLit
	p.noLit = true
	e := p.parseExpr()
	p.noLit = saved
	return e
}

func (p *parser) parseExpr() ast.Expr { return p.parseOr() }

func (p *parser) parseOr() ast.Expr {
	l := p.parseAnd()
	for p.at(lexer.OrOr) {
		p.next()
		r := p.parseAnd()
		l = &ast.Binary{Pos: span(l.Span(), r.Span()), Op: "||", L: l, R: r}
	}
	return l
}

func (p *parser) parseAnd() ast.Expr {
	l := p.parseCmp()
	for p.at(lexer.AndAnd) {
		p.next()
		r := p.parseCmp()
		l = &ast.Binary{Pos: span(l.Span(), r.Span()), Op: "&&", L: l, R: r}
	}
	return l
}

var cmpOps = map[lexer.Kind]string{lexer.Eq: "==", lexer.Ne: "!=", lexer.Lt: "<", lexer.Le: "<=", lexer.Gt: ">", lexer.Ge: ">="}

func (p *parser) parseCmp() ast.Expr {
	l := p.parseRange()
	if op, ok := cmpOps[p.cur().Kind]; ok {
		p.next()
		r := p.parseRange()
		return &ast.Binary{Pos: span(l.Span(), r.Span()), Op: op, L: l, R: r}
	}
	return l
}

func (p *parser) parseRange() ast.Expr {
	l := p.parseAdd()
	if p.at(lexer.DotDot) {
		p.next()
		r := p.parseAdd()
		return &ast.RangeExpr{Pos: span(l.Span(), r.Span()), Lo: l, Hi: r}
	}
	return l
}

var addOps = map[lexer.Kind]string{lexer.Plus: "+", lexer.Minus: "-", lexer.Pipe: "|", lexer.Caret: "^"}
var mulOps = map[lexer.Kind]string{lexer.Star: "*", lexer.Slash: "/", lexer.Percent: "%", lexer.Amp: "&", lexer.Shl: "<<", lexer.Shr: ">>"}

func (p *parser) parseAdd() ast.Expr {
	l := p.parseMul()
	for {
		op, ok := addOps[p.cur().Kind]
		if !ok {
			return l
		}
		p.next()
		r := p.parseMul()
		l = &ast.Binary{Pos: span(l.Span(), r.Span()), Op: op, L: l, R: r}
	}
}

func (p *parser) parseMul() ast.Expr {
	l := p.parseUnary()
	for {
		op, ok := mulOps[p.cur().Kind]
		if !ok {
			return l
		}
		p.next()
		r := p.parseUnary()
		l = &ast.Binary{Pos: span(l.Span(), r.Span()), Op: op, L: l, R: r}
	}
}

func (p *parser) parseUnary() ast.Expr {
	t := p.cur()
	if t.Kind == lexer.Pipe || t.Kind == lexer.OrOr {
		return p.parseClosure()
	}
	if t.Kind == lexer.Minus || t.Kind == lexer.Bang {
		p.next()
		x := p.parseUnary()
		if lit, ok := x.(*ast.IntLit); ok && t.Kind == lexer.Minus && !lit.Neg {
			lit.Neg = true
			lit.Start = t.Start
			return lit
		}
		return &ast.Unary{Pos: span(tokPos(t), x.Span()), Op: t.Text, X: x}
	}
	return p.parsePostfix()
}

// parseClosure parses `|a, b: T| expr` or `|a| { ... }`; `||` (one token)
// opens a closure without parameters.
func (p *parser) parseClosure() ast.Expr {
	t := p.next()
	c := &ast.ClosureExpr{Pos: tokPos(t)}
	if t.Kind == lexer.Pipe {
		p.nesting++
		for !p.at(lexer.Pipe) {
			n := p.expect(lexer.Ident)
			prm := &ast.Param{Pos: tokPos(n), Name: n.Text}
			if p.accept(lexer.Colon) {
				prm.Conv, prm.Type = p.parseParamType()
				prm.End = prm.Type.Span().End
			}
			c.Params = append(c.Params, prm)
			if !p.accept(lexer.Comma) {
				break
			}
		}
		p.nesting--
		p.expect(lexer.Pipe)
	}
	if p.at(lexer.LBrace) {
		c.Body = p.parseBlock()
		c.End = c.Body.End
		return c
	}
	c.Expr = p.parseExpr()
	c.End = c.Expr.Span().End
	return c
}

func (p *parser) parsePostfix() ast.Expr {
	x := p.parsePrimary()
	for {
		t := p.cur()
		switch t.Kind {
		case lexer.Dot:
			p.next()
			n := p.expect(lexer.Ident)
			x = &ast.FieldExpr{Pos: span(x.Span(), tokPos(n)), X: x, Name: n.Text}
		case lexer.LBrack:
			p.next()
			p.nesting++
			if p.at(lexer.Colon) {
				p.next()
				s := &ast.SliceExpr{Pos: x.Span(), X: x}
				if !p.at(lexer.RBrack) {
					s.Hi = p.parseExpr()
				}
				p.nesting--
				s.End = p.expect(lexer.RBrack).End
				x = s
				continue
			}
			first := p.parseIndexArg()
			if p.at(lexer.Colon) {
				p.next()
				s := &ast.SliceExpr{Pos: x.Span(), X: x, Lo: first}
				if !p.at(lexer.RBrack) {
					s.Hi = p.parseExpr()
				}
				p.nesting--
				s.End = p.expect(lexer.RBrack).End
				x = s
				continue
			}
			ix := &ast.IndexExpr{Pos: x.Span(), X: x, Args: []ast.Expr{first}}
			for p.accept(lexer.Comma) {
				ix.Args = append(ix.Args, p.parseIndexArg())
			}
			p.nesting--
			ix.End = p.expect(lexer.RBrack).End
			x = ix
		case lexer.LParen:
			p.next()
			p.nesting++
			savedLit := p.noLit
			p.noLit = false
			c := &ast.CallExpr{Pos: x.Span(), Fn: x}
			for !p.at(lexer.RParen) {
				a := &ast.Arg{Pos: tokPos(p.cur())}
				if p.accept(lexer.KwOut) {
					a.Out = true
				}
				a.X = p.parseExpr()
				a.End = a.X.Span().End
				c.Args = append(c.Args, a)
				if !p.accept(lexer.Comma) {
					break
				}
			}
			p.nesting--
			p.noLit = savedLit
			c.End = p.expect(lexer.RParen).End
			x = c
		case lexer.Question:
			p.next()
			x = &ast.Try{Pos: span(x.Span(), tokPos(t)), X: x}
		case lexer.LBrace:
			if p.noLit || !isTypeLike(x) {
				return x
			}
			x = p.parseStructLit(x)
		default:
			return x
		}
	}
}

// isTypeLike reports whether an expression can be the type of a struct literal:
// an identifier or pkg.Name starting with an upper-case letter, optionally with
// generic arguments.
func isTypeLike(x ast.Expr) bool {
	switch e := x.(type) {
	case *ast.Ident:
		return isUpper(e.Name)
	case *ast.FieldExpr:
		if id, ok := e.X.(*ast.Ident); ok {
			return !isUpper(id.Name) && isUpper(e.Name)
		}
	case *ast.IndexExpr:
		return isTypeLike(e.X)
	}
	return false
}

func isUpper(s string) bool { return len(s) > 0 && s[0] >= 'A' && s[0] <= 'Z' }

func (p *parser) parseStructLit(x ast.Expr) ast.Expr {
	tn := p.exprToTypeName(x)
	t := p.expect(lexer.LBrace)
	p.nesting++
	lit := &ast.StructLit{Pos: span(x.Span(), tokPos(t)), Type: tn}
	for !p.at(lexer.RBrace) {
		n := p.expect(lexer.Ident)
		p.expect(lexer.Colon)
		v := p.parseExpr()
		lit.Fields = append(lit.Fields, &ast.FieldInit{Pos: span(tokPos(n), v.Span()), Name: n.Text, Value: v})
		if !p.accept(lexer.Comma) {
			break
		}
	}
	p.nesting--
	lit.End = p.expect(lexer.RBrace).End
	return lit
}

// exprToTypeName converts a type-like expression (see isTypeLike) to a TypeName.
func (p *parser) exprToTypeName(x ast.Expr) *ast.TypeName {
	switch e := x.(type) {
	case *ast.Ident:
		return &ast.TypeName{Pos: e.Pos, Name: e.Name}
	case *ast.FieldExpr:
		id, ok := e.X.(*ast.Ident)
		if !ok {
			p.errorf(p.cur(), "expected a module-qualified type name before '{'")
		}
		return &ast.TypeName{Pos: e.Pos, Pkg: id.Name, Name: e.Name}
	case *ast.IndexExpr:
		tn := p.exprToTypeName(e.X)
		tn.Pos = e.Pos
		for _, a := range e.Args {
			tn.Args = append(tn.Args, p.ExprToType(a))
		}
		return tn
	}
	p.errorf(p.cur(), "expected type name before '{'")
	return nil
}

// ExprToType reinterprets an expression parsed inside `[...]` as a type.
// It is exported for the resolver, which performs the same conversion for
// generic instantiations such as f[u64](x).
func (p *parser) ExprToType(x ast.Expr) ast.TypeExpr {
	switch e := x.(type) {
	case *ast.TypeArgExpr:
		return e.Type
	case *ast.Ident:
		if primNames[e.Name] {
			return &ast.PrimType{Pos: e.Pos, Name: e.Name}
		}
		return &ast.TypeName{Pos: e.Pos, Name: e.Name}
	case *ast.FieldExpr:
		if id, ok := e.X.(*ast.Ident); ok {
			return &ast.TypeName{Pos: e.Pos, Pkg: id.Name, Name: e.Name}
		}
	case *ast.IndexExpr:
		if id, ok := e.X.(*ast.Ident); ok && id.Name == "RawPtr" && len(e.Args) == 1 {
			return &ast.RawPtrType{Pos: e.Pos, Elem: p.ExprToType(e.Args[0])}
		}
		tn := p.exprToTypeName(e)
		return tn
	case *ast.UnitLit:
		return &ast.UnitType{Pos: e.Pos}
	}
	p.errorf(p.cur(), "expected type argument")
	return nil
}

func (p *parser) parsePrimary() ast.Expr {
	t := p.cur()
	switch t.Kind {
	case lexer.Int:
		p.next()
		return parseIntLit(t, p)
	case lexer.Float:
		p.next()
		text, suffix := splitSuffix(t.Text)
		v, err := strconv.ParseFloat(strings.ReplaceAll(text, "_", ""), 64)
		if err != nil {
			p.errorf(t, "invalid float literal")
		}
		return &ast.FloatLit{Pos: tokPos(t), Value: v, Suffix: suffix, Text: t.Text}
	case lexer.String:
		p.next()
		return &ast.StringLit{Pos: tokPos(t), Value: t.Text}
	case lexer.LBrack:
		// array literal [e1, e2, ...]
		p.next()
		p.nesting++
		lit := &ast.ArrayLit{Pos: tokPos(t)}
		savedLit := p.noLit
		p.noLit = false // brackets remove the ambiguity with a block
		defer func() { p.noLit = savedLit }()
		p.skipNewlines()
		for !p.at(lexer.RBrack) {
			lit.Elems = append(lit.Elems, p.parseExpr())
			p.skipNewlines()
			if !p.accept(lexer.Comma) {
				break
			}
			p.skipNewlines()
		}
		p.nesting--
		end := p.expect(lexer.RBrack)
		lit.End = end.End
		return lit
	case lexer.KwTrue, lexer.KwFalse:
		p.next()
		return &ast.BoolLit{Pos: tokPos(t), Value: t.Kind == lexer.KwTrue}
	case lexer.KwSelf:
		p.next()
		return &ast.Ident{Pos: tokPos(t), Name: "self"}
	case lexer.Ident:
		p.next()
		if t.Text == "trap" && p.at(lexer.LParen) && p.peekKind(1) == lexer.RParen {
			p.next()
			e := p.next()
			return &ast.TrapExpr{Pos: span(tokPos(t), tokPos(e))}
		}
		if primNames[t.Text] && p.at(lexer.LParen) {
			p.next()
			p.nesting++
			savedLit := p.noLit
			p.noLit = false
			x := p.parseExpr()
			p.noLit = savedLit
			p.nesting--
			e := p.expect(lexer.RParen)
			return &ast.ConvExpr{Pos: span(tokPos(t), tokPos(e)), To: t.Text, X: x}
		}
		return &ast.Ident{Pos: tokPos(t), Name: t.Text}
	case lexer.KwTruncate:
		p.next()
		p.expect(lexer.LBrack)
		to := p.expect(lexer.Ident)
		if !primNames[to.Text] {
			p.errorf(to, "truncate requires a primitive integer type")
		}
		p.expect(lexer.RBrack)
		p.expect(lexer.LParen)
		p.nesting++
		x := p.parseExpr()
		p.nesting--
		e := p.expect(lexer.RParen)
		return &ast.ConvExpr{Pos: span(tokPos(t), tokPos(e)), To: to.Text, X: x, Truncate: true}
	case lexer.KwMove:
		p.next()
		n := p.expect(lexer.Ident)
		return &ast.Move{Pos: span(tokPos(t), tokPos(n)), Name: n.Text}
	case lexer.KwCopy:
		p.next()
		x, e := p.parenExpr()
		return &ast.Copy{Pos: span(tokPos(t), e), X: x}
	case lexer.KwTake:
		p.next()
		x, e := p.parenExpr()
		return &ast.Take{Pos: span(tokPos(t), e), Place: x}
	case lexer.KwReplace:
		p.next()
		p.expect(lexer.LParen)
		p.nesting++
		savedLit := p.noLit
		p.noLit = false
		pl := p.parseExpr()
		p.expect(lexer.Comma)
		v := p.parseExpr()
		p.noLit = savedLit
		p.nesting--
		e := p.expect(lexer.RParen)
		return &ast.Replace{Pos: span(tokPos(t), tokPos(e)), Place: pl, Value: v}
	case lexer.KwDrop:
		p.next()
		p.expect(lexer.LParen)
		n := p.expect(lexer.Ident)
		e := p.expect(lexer.RParen)
		return &ast.Drop{Pos: span(tokPos(t), tokPos(e)), Name: n.Text}
	case lexer.LParen:
		p.next()
		if p.at(lexer.RParen) {
			e := p.next()
			return &ast.UnitLit{Pos: span(tokPos(t), tokPos(e))}
		}
		p.nesting++
		saved := p.noLit
		p.noLit = false
		x := p.parseExpr()
		p.noLit = saved
		p.nesting--
		e := p.expect(lexer.RParen)
		return &ast.Paren{Pos: span(tokPos(t), tokPos(e)), X: x}
	}
	p.errorf(t, "expected expression, found %s", describe(t))
	return nil
}

func (p *parser) parenExpr() (ast.Expr, ast.Pos) {
	p.expect(lexer.LParen)
	p.nesting++
	savedLit := p.noLit
	p.noLit = false
	x := p.parseExpr()
	p.noLit = savedLit
	p.nesting--
	e := p.expect(lexer.RParen)
	return x, tokPos(e)
}

func splitSuffix(text string) (string, string) {
	for _, s := range []string{"isize", "usize", "i8", "i16", "i32", "i64", "u8", "u16", "u32", "u64", "f32", "f64"} {
		if strings.HasSuffix(text, s) {
			return strings.TrimSuffix(text, s), s
		}
	}
	return text, ""
}

func parseIntLit(t lexer.Token, p *parser) ast.Expr {
	text, suffix := splitSuffix(t.Text)
	v, err := strconv.ParseUint(strings.ReplaceAll(text, "_", ""), 10, 64)
	if err != nil {
		p.errorf(t, "integer literal out of range")
	}
	return &ast.IntLit{Pos: tokPos(t), Value: v, Suffix: suffix, Text: t.Text}
}

// parseIndexArg parses one element of an `x[...]` list: an expression, or a
// type form that cannot begin an expression (slice, array, access, function
// or unit types), which is wrapped in a TypeArgExpr for the resolver.
func (p *parser) parseIndexArg() ast.Expr {
	switch p.cur().Kind {
	case lexer.LBrack, lexer.KwView, lexer.KwEdit, lexer.KwFunc:
		ty := p.parseType()
		return &ast.TypeArgExpr{Pos: ty.Span(), Type: ty}
	case lexer.LParen:
		if p.peekKind(1) == lexer.RParen {
			ty := p.parseType()
			return &ast.TypeArgExpr{Pos: ty.Span(), Type: ty}
		}
	}
	return p.parseExpr()
}
