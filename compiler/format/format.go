// Package format prints kern source in the canonical form defined by
// spec/format.md. Format parses the file, then prints the AST with comments
// re-attached by position; the result is idempotent and preserves the
// diagnostic codes of the input.
package format

import (
	"bytes"
	"sort"
	"strconv"
	"strings"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/lexer"
	"github.com/dshills/kern/compiler/parser"
	"github.com/dshills/kern/compiler/source"
)

const indentUnit = "    "

// Format returns the canonical form of f. When f does not parse, it returns
// nil and the parse diagnostics unchanged.
func Format(f *source.File) ([]byte, []diag.Diagnostic) {
	af, ds := parser.Parse(f)
	if af == nil {
		return nil, ds
	}
	_, comments, _ := lexer.LexWithComments(f.Bytes)
	p := &printer{src: f.Bytes, comments: comments}
	p.file(af)
	return p.buf.Bytes(), nil
}

// printer holds the output buffer and the comment cursor. Comments are
// consumed in source order: an own-line comment is printed on its own line
// before the next item at the current indentation, and a comment that
// shares a line with code is appended to the line of the item it followed.
type printer struct {
	src      []byte
	buf      bytes.Buffer
	indent   int
	comments []lexer.Comment
	ci       int      // next unconsumed comment
	pending  []string // same-line comments to append at the next line end
	lastEnd  int      // source offset after the last printed item or comment
	// allowBlank permits one blank line before the next item; it is false at
	// the start of the file and directly after an opening brace.
	allowBlank bool
}

// ---- low-level output ----

func (p *printer) w(s string) { p.buf.WriteString(s) }

func (p *printer) ind() {
	for i := 0; i < p.indent; i++ {
		p.w(indentUnit)
	}
}

// nl ends the current line, appending any pending same-line comments.
func (p *printer) nl() {
	for _, c := range p.pending {
		p.w("  ")
		p.w(c)
	}
	p.pending = nil
	p.w("\n")
}

// appendPrev appends a comment to the most recently completed line.
func (p *printer) appendPrev(c string) {
	b := p.buf.Bytes()
	if n := len(b); n > 0 && b[n-1] == '\n' {
		p.buf.Truncate(n - 1)
		p.w("  ")
		p.w(c)
		p.w("\n")
		return
	}
	p.pending = append(p.pending, c)
}

func (p *printer) noteEnd(end int) {
	if end > p.lastEnd {
		p.lastEnd = end
	}
}

// blankBefore emits one blank line when the source had at least one blank
// line between the last printed element and offset start.
func (p *printer) blankBefore(start int) {
	if !p.allowBlank || start <= p.lastEnd {
		return
	}
	if bytes.Count(p.src[p.lastEnd:start], []byte("\n")) >= 2 {
		p.w("\n")
	}
}

func commentText(c lexer.Comment) string { return strings.TrimRight(c.Text, " \t\r") }

// beforeItem consumes the comments that precede an item starting at start
// and those inside its header (up to headerEnd), then starts the item's
// line. Own-line comments go on their own lines; comments on the previous
// line are appended to it; same-line comments inside the header are held
// for the end of the item's first line.
func (p *printer) beforeItem(start, headerEnd int) {
	for p.ci < len(p.comments) && p.comments[p.ci].Start < headerEnd {
		c := p.comments[p.ci]
		p.ci++
		switch {
		case c.OwnLine:
			for _, s := range p.pending {
				p.appendPrev(s)
			}
			p.pending = nil
			p.blankBefore(c.Start)
			p.ind()
			p.w(commentText(c))
			p.nl()
			p.allowBlank = true
		case c.Start < start:
			p.appendPrev(commentText(c))
		default:
			p.pending = append(p.pending, commentText(c))
		}
		p.noteEnd(c.End)
	}
	p.blankBefore(start)
	p.ind()
}

// afterItem ends an item's line, attaching any comment left inside the item
// and the comment that follows it on the same line.
func (p *printer) afterItem(end int) {
	for p.ci < len(p.comments) {
		c := p.comments[p.ci]
		if c.Start >= end && (c.OwnLine || bytes.IndexByte(p.src[end:c.Start], '\n') >= 0) {
			break
		}
		p.ci++
		p.pending = append(p.pending, commentText(c))
		p.noteEnd(c.End)
	}
	p.noteEnd(end)
	p.nl()
	p.allowBlank = true
}

// headerPending consumes the comments inside [start, end) as same-line
// comments, for headers that cannot be preceded by a line (else-if).
func (p *printer) headerPending(end int) {
	for p.ci < len(p.comments) && p.comments[p.ci].Start < end {
		c := p.comments[p.ci]
		p.ci++
		p.pending = append(p.pending, commentText(c))
		p.noteEnd(c.End)
	}
}

// flushBefore prints every unconsumed comment before offset end, at the end
// of a brace body or of the file.
func (p *printer) flushBefore(end int) {
	for p.ci < len(p.comments) && p.comments[p.ci].Start < end {
		c := p.comments[p.ci]
		p.ci++
		if c.OwnLine {
			p.blankBefore(c.Start)
			p.ind()
			p.w(commentText(c))
			p.nl()
			p.allowBlank = true
		} else {
			p.appendPrev(commentText(c))
		}
		p.noteEnd(c.End)
	}
}

// hasCommentIn reports whether a comment starts inside [start, end).
func (p *printer) hasCommentIn(start, end int) bool {
	i := sort.Search(len(p.comments), func(i int) bool { return p.comments[i].Start >= start })
	return i < len(p.comments) && p.comments[i].Start < end
}

// oneLine reports whether the source of a brace body [start, end) was
// written on one line and may stay so: it holds no comment and nothing
// that must occupy its own line.
func (p *printer) oneLine(start, end int, needsLines bool) bool {
	if end > len(p.src) || start < 0 || start > end {
		return false
	}
	return !needsLines && bytes.IndexByte(p.src[start:end], '\n') < 0 && !p.hasCommentIn(start, end)
}

// ---- file and declarations ----

func (p *printer) file(f *ast.File) {
	modEnd := f.Start + len("module")
	if i := bytes.Index(p.src[modEnd:], []byte(f.Module)); i >= 0 {
		modEnd += i + len(f.Module)
	}
	p.beforeItem(f.Start, f.Start)
	p.w("module ")
	p.w(f.Module)
	p.afterItem(modEnd)
	for _, im := range f.Imports {
		p.beforeItem(im.Start, im.Start)
		p.w("import ")
		p.w(quote(im.Path))
		p.afterItem(im.End)
	}
	for _, d := range f.Decls {
		p.decl(d)
	}
	p.flushBefore(len(p.src) + 1)
}

func (p *printer) decl(d ast.Decl) {
	switch d := d.(type) {
	case *ast.StructDecl:
		p.beforeItem(d.Start, d.Start)
		if d.View {
			p.w("view ")
		}
		p.w("struct ")
		p.w(d.Name)
		p.typeParams(d.TypeParams)
		if d.NoCopy {
			p.w(" nocopy")
		}
		if d.Packed {
			p.w(" packed")
		}
		p.w(" ")
		items := make([]bodyItem, len(d.Fields))
		for i, f := range d.Fields {
			f := f
			items[i] = bodyItem{pos: f.Pos, print: func() { p.field(f) }}
		}
		p.body(d.Pos, items, false)
		p.afterItem(d.End)
	case *ast.UnionDecl:
		p.beforeItem(d.Start, d.Start)
		p.w("union ")
		p.w(d.Name)
		p.typeParams(d.TypeParams)
		p.w(" ")
		items := make([]bodyItem, len(d.Variants))
		for i, v := range d.Variants {
			v := v
			items[i] = bodyItem{pos: v.Pos, print: func() { p.variant(v) }}
		}
		p.body(d.Pos, items, false)
		p.afterItem(d.End)
	case *ast.InterfaceDecl:
		p.beforeItem(d.Start, d.Start)
		p.w("interface ")
		p.w(d.Name)
		p.typeParams(d.TypeParams)
		p.w(" ")
		items := make([]bodyItem, len(d.Methods))
		for i, m := range d.Methods {
			m := m
			items[i] = bodyItem{pos: m.Pos, print: func() { p.funcHeader(m) }}
		}
		p.body(d.Pos, items, false)
		p.afterItem(d.End)
	case *ast.ImplDecl:
		p.beforeItem(d.Start, d.Start)
		p.w("impl ")
		if len(d.TypeParams) > 0 {
			p.typeParams(d.TypeParams)
			p.w(" ")
		}
		if d.Iface != nil {
			p.typeName(d.Iface)
			p.w(" for ")
		}
		p.typ(d.Target)
		p.w(" ")
		var items []bodyItem
		needsLines := false
		for _, fn := range d.Funcs {
			fn := fn
			items = append(items, bodyItem{pos: fn.Pos, print: func() { p.funcDecl(fn, true) }, multi: func() { p.funcDeclLines(fn) }})
			needsLines = needsLines || fn.Body == nil || stmtsNeedLines(fn.Body.Stmts)
		}
		if dt := d.Destructor; dt != nil {
			items = append(items, bodyItem{pos: dt.Pos, print: func() {
				p.w("destructor(edit self) ")
				p.block(dt.Body)
			}})
			needsLines = needsLines || stmtsNeedLines(dt.Body.Stmts)
		}
		sort.SliceStable(items, func(i, j int) bool { return items[i].pos.Start < items[j].pos.Start })
		p.body(d.Pos, items, needsLines)
		p.afterItem(d.End)
	case *ast.FuncDecl:
		if d.Extern != "" {
			p.beforeItem(d.Start, d.End)
			p.w("extern ")
			p.w(quote(d.Extern))
			p.w(" ")
			p.funcHeader(d)
			p.afterItem(d.End)
			return
		}
		p.funcDeclLines(d)
	case *ast.ConstDecl:
		p.beforeItem(d.Start, d.End)
		p.w("const ")
		p.w(d.Name)
		p.w(": ")
		p.typ(d.Type)
		p.w(" = ")
		p.expr(d.Value)
		p.afterItem(d.End)
	case *ast.GlobalDecl:
		p.beforeItem(d.Start, d.End)
		p.w("thread_local ")
		p.w(d.Name)
		p.w(": ")
		p.typ(d.Type)
		p.w(" = ")
		p.expr(d.Value)
		p.afterItem(d.End)
	case *ast.AssertDecl:
		p.beforeItem(d.Start, d.End)
		p.w("unsafe assert ")
		p.w(d.Marker)
		p.w(" for ")
		p.typeName(d.Type)
		p.afterItem(d.End)
	}
}

// funcDeclLines prints a function declaration as one or more lines: the
// contract block, if any, then the signature and body.
func (p *printer) funcDeclLines(d *ast.FuncDecl) {
	headerEnd := d.End
	if d.Body != nil {
		headerEnd = d.Body.Start
	}
	if c := d.Contract; c != nil {
		p.beforeItem(d.Start, d.Start)
		p.contract(c)
		p.afterItem(c.End)
		p.allowBlank = false
		p.beforeItem(c.End, headerEnd)
	} else {
		p.beforeItem(d.Start, headerEnd)
	}
	p.funcDecl(d, false)
	p.afterItem(d.End)
}

// funcDecl prints a signature and body on the current line. withContract
// prints the contract block inline before the signature (one-line impl
// bodies); funcDeclLines prints it on lines of its own instead.
func (p *printer) funcDecl(d *ast.FuncDecl, withContract bool) {
	if withContract && d.Contract != nil {
		p.contract(d.Contract)
		p.w(" ")
	}
	p.funcHeader(d)
	if d.Body != nil {
		p.w(" ")
		p.block(d.Body)
	}
}

// contract prints a contract block. The AST groups clauses by kind, so the
// block's source is re-lexed to recover the clauses in source order; the
// AST lists are the fallback when the two disagree.
func (p *printer) contract(c *ast.Contract) {
	type clause struct {
		kw, text string
		pos      ast.Pos
	}
	var cls []clause
	if c.End <= len(p.src) {
		toks, _ := lexer.Lex(p.src[c.Start:c.End])
		for i := 0; i+1 < len(toks); i++ {
			switch toks[i].Kind {
			case lexer.KwInvariant, lexer.KwRequires, lexer.KwEnsures:
				if toks[i+1].Kind == lexer.String {
					cls = append(cls, clause{toks[i].Text, toks[i+1].Text, ast.Pos{Start: c.Start + toks[i].Start, End: c.Start + toks[i+1].End}})
				}
			}
		}
	}
	if len(cls) != len(c.Invariant)+len(c.Requires)+len(c.Ensures) {
		cls = cls[:0]
		for _, s := range c.Invariant {
			cls = append(cls, clause{"invariant", s, c.Pos})
		}
		for _, s := range c.Requires {
			cls = append(cls, clause{"requires", s, c.Pos})
		}
		for _, s := range c.Ensures {
			cls = append(cls, clause{"ensures", s, c.Pos})
		}
	}
	p.w("contract ")
	items := make([]bodyItem, len(cls))
	for i, cl := range cls {
		cl := cl
		items[i] = bodyItem{pos: cl.pos, print: func() {
			p.w(cl.kw)
			p.w(" ")
			p.w(quote(cl.text))
		}}
	}
	p.body(c.Pos, items, false)
}

func (p *printer) funcHeader(d *ast.FuncDecl) {
	p.w("func ")
	p.w(d.Name)
	p.typeParams(d.TypeParams)
	p.w("(")
	for i, prm := range d.Params {
		if i > 0 {
			p.w(", ")
		}
		p.param(prm)
	}
	p.w(")")
	p.ret(d.Ret)
	for i, tp := range d.Where {
		if i == 0 {
			p.w(" where ")
		} else {
			p.w(", ")
		}
		p.w(tp.Name)
		for j, c := range tp.Constraints {
			if j == 0 {
				p.w(": ")
			} else {
				p.w(" + ")
			}
			p.typeName(c)
		}
	}
	if d.Release != "" {
		p.w(" release ")
		p.w(d.Release)
	}
}

func (p *printer) param(prm *ast.Param) {
	if prm.Self {
		switch prm.Conv {
		case ast.ConvView:
			p.w("view self")
		case ast.ConvEdit:
			p.w("edit self")
		default:
			p.w("own self")
		}
		return
	}
	if prm.Name != "" {
		p.w(prm.Name)
		p.w(": ")
	}
	p.paramType(prm.Conv, prm.Type)
}

func (p *printer) paramType(conv ast.Conv, t ast.TypeExpr) {
	switch conv {
	case ast.ConvOwn:
		p.w("own ")
	case ast.ConvOut:
		p.w("out ")
	}
	p.typ(t)
}

func (p *printer) ret(ret []ast.TypeExpr) {
	switch len(ret) {
	case 0:
	case 1:
		p.w(" -> ")
		p.typ(ret[0])
	default:
		p.w(" -> (")
		for i, t := range ret {
			if i > 0 {
				p.w(", ")
			}
			p.typ(t)
		}
		p.w(")")
	}
}

func (p *printer) typeParams(tps []*ast.TypeParam) {
	if len(tps) == 0 {
		return
	}
	p.w("[")
	for i, tp := range tps {
		if i > 0 {
			p.w(", ")
		}
		p.w(tp.Name)
		for j, c := range tp.Constraints {
			if j == 0 {
				p.w(": ")
			} else {
				p.w(" + ")
			}
			p.typeName(c)
		}
	}
	p.w("]")
}

func (p *printer) field(f *ast.Field) {
	if f.Name != "" {
		p.w(f.Name)
		p.w(": ")
	}
	p.typ(f.Type)
}

func (p *printer) variant(v *ast.Variant) {
	p.w(v.Name)
	if len(v.Fields) == 0 {
		return
	}
	p.w("(")
	for i, f := range v.Fields {
		if i > 0 {
			p.w(", ")
		}
		p.field(f)
	}
	p.w(")")
}

// ---- brace bodies ----

// bodyItem is one member of a brace body. print writes it on the current
// line; multi, when set, prints it as its own line(s) and is used instead of
// print when the body is laid out one item per line.
type bodyItem struct {
	pos   ast.Pos
	print func()
	multi func()
}

// body prints a brace-delimited member list. The body stays on one line when
// its source was on one line and it holds no comment and nothing that needs
// its own line; otherwise each item is printed on its own line.
func (p *printer) body(pos ast.Pos, items []bodyItem, needsLines bool) {
	if len(items) == 0 && !p.hasCommentIn(pos.Start, pos.End) {
		p.w("{ }")
		return
	}
	if p.oneLine(pos.Start, pos.End, needsLines) {
		p.w("{ ")
		for i, it := range items {
			if i > 0 {
				p.w("; ")
			}
			it.print()
		}
		p.w(" }")
		return
	}
	p.w("{")
	p.nl()
	p.indent++
	p.allowBlank = false
	for _, it := range items {
		if it.multi != nil {
			it.multi()
			continue
		}
		p.beforeItem(it.pos.Start, it.pos.Start)
		it.print()
		p.afterItem(it.pos.End)
	}
	p.flushBefore(pos.End - 1)
	p.indent--
	p.ind()
	p.w("}")
}

// stmtsNeedLines reports whether a statement list contains a statement that
// must be laid out on multiple lines (a match).
func stmtsNeedLines(stmts []ast.Stmt) bool {
	for _, s := range stmts {
		if stmtNeedsLines(s) {
			return true
		}
	}
	return false
}

func stmtNeedsLines(s ast.Stmt) bool {
	switch s := s.(type) {
	case *ast.Match:
		return true
	case *ast.Block:
		return stmtsNeedLines(s.Stmts)
	case *ast.If:
		return stmtsNeedLines(s.Then.Stmts) || (s.Else != nil && stmtNeedsLines(s.Else))
	case *ast.While:
		return stmtsNeedLines(s.Body.Stmts)
	case *ast.For:
		return stmtsNeedLines(s.Body.Stmts)
	case *ast.With:
		return stmtsNeedLines(s.Body.Stmts)
	case *ast.Unsafe:
		return stmtsNeedLines(s.Body.Stmts)
	}
	return false
}

func (p *printer) block(b *ast.Block) {
	items := make([]bodyItem, len(b.Stmts))
	for i, s := range b.Stmts {
		s := s
		items[i] = bodyItem{pos: s.Span(), print: func() { p.stmt(s) }, multi: func() { p.stmtLines(s) }}
	}
	p.body(b.Pos, items, stmtsNeedLines(b.Stmts))
}

// ---- statements ----

// headerEnd returns the end of a statement's header: the part before its
// first nested block, or the whole statement when it has none.
func headerEnd(s ast.Stmt) int {
	switch s := s.(type) {
	case *ast.If:
		return s.Then.Start
	case *ast.While:
		return s.Body.Start
	case *ast.For:
		return s.Body.Start
	case *ast.With:
		return s.Body.Start
	case *ast.Match:
		return s.Subject.Span().End
	case *ast.Unsafe:
		return s.Body.Start
	case *ast.Block:
		return s.Start
	}
	return s.Span().End
}

// stmtLines prints a statement as its own line(s).
func (p *printer) stmtLines(s ast.Stmt) {
	p.beforeItem(s.Span().Start, headerEnd(s))
	p.stmt(s)
	p.afterItem(s.Span().End)
}

func (p *printer) stmt(s ast.Stmt) {
	switch s := s.(type) {
	case *ast.VarDecl:
		p.w("var ")
		p.w(s.Name)
		p.w(": ")
		p.typ(s.Type)
		if s.Init != nil {
			p.w(" = ")
			p.expr(s.Init)
		}
	case *ast.ShortDecl:
		p.w(s.Name)
		p.w(" := ")
		p.expr(s.Value)
	case *ast.Assign:
		p.expr(s.Target)
		p.w(" = ")
		p.expr(s.Value)
	case *ast.With:
		p.w("with ")
		p.w(strings.Join(s.Names, ", "))
		p.w(" := ")
		p.expr(s.Subject)
		p.w(" ")
		p.block(s.Body)
	case *ast.Match:
		p.match(s)
	case *ast.For:
		p.w("for ")
		p.w(s.Var)
		if s.Var2 != "" {
			p.w(", ")
			p.w(s.Var2)
		}
		p.w(" in ")
		p.expr(s.Iter)
		p.w(" ")
		p.block(s.Body)
	case *ast.While:
		p.w("while ")
		p.expr(s.Cond)
		p.w(" ")
		p.block(s.Body)
	case *ast.If:
		p.ifStmt(s)
	case *ast.Return:
		p.w("return")
		if s.Value != nil {
			p.w(" ")
			p.expr(s.Value)
		}
	case *ast.Break:
		p.w("break")
	case *ast.Continue:
		p.w("continue")
	case *ast.Unsafe:
		p.w("unsafe ")
		p.block(s.Body)
	case *ast.Block:
		p.block(s)
	case *ast.ExprStmt:
		p.expr(s.X)
	}
}

func (p *printer) ifStmt(s *ast.If) {
	p.w("if ")
	p.expr(s.Cond)
	p.w(" ")
	p.block(s.Then)
	switch e := s.Else.(type) {
	case *ast.If:
		p.w(" else ")
		p.headerPending(e.Then.Start)
		p.ifStmt(e)
	case *ast.Block:
		p.w(" else ")
		p.block(e)
	}
}

func (p *printer) match(m *ast.Match) {
	p.w("match ")
	if m.Move {
		p.w("move ")
	}
	p.expr(m.Subject)
	p.w(" ")
	if len(m.Arms) == 0 && !p.hasCommentIn(m.Start, m.End) {
		p.w("{ }")
		return
	}
	p.w("{")
	p.nl()
	p.indent++
	p.allowBlank = false
	for _, a := range m.Arms {
		p.beforeItem(a.Start, a.Body.Start)
		p.arm(a)
		p.afterItem(a.End)
	}
	p.flushBefore(m.End - 1)
	p.indent--
	p.ind()
	p.w("}")
}

func (p *printer) arm(a *ast.Arm) {
	if a.Wild {
		p.w("_")
	} else {
		if q := a.Qual; q != nil {
			if q.Pkg != "" {
				p.w(q.Pkg)
				p.w(".")
			}
			p.w(q.Name)
			p.w(".")
		}
		p.w(a.Variant)
		if len(a.Binds) > 0 {
			p.w("(")
			p.w(strings.Join(a.Binds, ", "))
			p.w(")")
		}
	}
	p.w(" => ")
	// A braced body starts with '{'; otherwise the parser wrapped a single
	// statement in a synthetic block.
	if a.Body.Start < len(p.src) && p.src[a.Body.Start] == '{' {
		p.block(a.Body)
		return
	}
	if len(a.Body.Stmts) == 1 {
		p.stmt(a.Body.Stmts[0])
		return
	}
	p.block(a.Body)
}

// ---- types ----

func (p *printer) typ(t ast.TypeExpr) {
	switch t := t.(type) {
	case *ast.PrimType:
		p.w(t.Name)
	case *ast.TypeName:
		p.typeName(t)
	case *ast.ArrayType:
		p.w("[")
		p.w(strconv.Itoa(t.Len))
		p.w("]")
		p.typ(t.Elem)
	case *ast.SliceType:
		p.w("[]")
		p.typ(t.Elem)
	case *ast.AccessType:
		if t.Edit {
			p.w("edit")
		} else {
			p.w("view")
		}
		if t.Seq {
			p.w("[]")
			p.typ(t.Elem)
		} else {
			p.w("[")
			p.typ(t.Elem)
			p.w("]")
		}
	case *ast.RawPtrType:
		p.w("RawPtr[")
		p.typ(t.Elem)
		p.w("]")
	case *ast.FuncType:
		if t.Closure {
			p.w("closure(")
		} else {
			p.w("func(")
		}
		for i, prm := range t.Params {
			if i > 0 {
				p.w(", ")
			}
			p.paramType(prm.Conv, prm.Type)
		}
		p.w(")")
		p.ret(t.Ret)
	case *ast.UnitType:
		p.w("()")
	}
}

func (p *printer) typeName(t *ast.TypeName) {
	if t.Pkg != "" {
		p.w(t.Pkg)
		p.w(".")
	}
	p.w(t.Name)
	if len(t.Args) > 0 {
		p.w("[")
		for i, a := range t.Args {
			if i > 0 {
				p.w(", ")
			}
			p.typ(a)
		}
		p.w("]")
	}
}

// ---- expressions ----

func (p *printer) expr(e ast.Expr) {
	switch e := e.(type) {
	case *ast.IntLit:
		if e.Neg {
			p.w("-")
		}
		if e.Text != "" {
			p.w(e.Text)
		} else {
			p.w(strconv.FormatUint(e.Value, 10))
			p.w(e.Suffix)
		}
	case *ast.FloatLit:
		if e.Text != "" {
			p.w(e.Text)
		} else {
			s := strconv.FormatFloat(e.Value, 'f', -1, 64)
			if !strings.Contains(s, ".") {
				s += ".0"
			}
			p.w(s)
			p.w(e.Suffix)
		}
	case *ast.StringLit:
		p.w(quote(e.Value))
	case *ast.BoolLit:
		if e.Value {
			p.w("true")
		} else {
			p.w("false")
		}
	case *ast.Ident:
		p.w(e.Name)
	case *ast.Qualified:
		p.w(e.Pkg)
		p.w(".")
		p.w(e.Name)
	case *ast.FieldExpr:
		p.expr(e.X)
		p.w(".")
		p.w(e.Name)
	case *ast.IndexExpr:
		p.expr(e.X)
		p.w("[")
		for i, a := range e.Args {
			if i > 0 {
				p.w(", ")
			}
			p.expr(a)
		}
		p.w("]")
	case *ast.TypeArgExpr:
		p.typ(e.Type)
	case *ast.SliceExpr:
		p.expr(e.X)
		p.w("[")
		if e.Lo != nil {
			p.expr(e.Lo)
		}
		p.w(":")
		if e.Hi != nil {
			p.expr(e.Hi)
		}
		p.w("]")
	case *ast.ClosureExpr:
		if len(e.Params) == 0 {
			p.w("||")
		} else {
			p.w("|")
			for i, prm := range e.Params {
				if i > 0 {
					p.w(", ")
				}
				p.w(prm.Name)
				if prm.Type != nil {
					p.w(": ")
					p.paramType(prm.Conv, prm.Type)
				}
			}
			p.w("|")
		}
		p.w(" ")
		if e.Expr != nil {
			p.expr(e.Expr)
		} else {
			p.block(e.Body)
		}
	case *ast.CallExpr:
		p.expr(e.Fn)
		p.w("(")
		for i, a := range e.Args {
			if i > 0 {
				p.w(", ")
			}
			if a.Out {
				p.w("out ")
			}
			p.expr(a.X)
		}
		p.w(")")
	case *ast.Binary:
		p.expr(e.L)
		p.w(" ")
		p.w(e.Op)
		p.w(" ")
		p.expr(e.R)
	case *ast.Unary:
		p.w(e.Op)
		if startsWithOp(e.X, e.Op) {
			p.w(" ")
		}
		p.expr(e.X)
	case *ast.Try:
		p.expr(e.X)
		p.w("?")
	case *ast.Move:
		p.w("move ")
		p.w(e.Name)
	case *ast.Copy:
		p.w("copy(")
		p.expr(e.X)
		p.w(")")
	case *ast.Take:
		p.w("take(")
		p.expr(e.Place)
		p.w(")")
	case *ast.Replace:
		p.w("replace(")
		p.expr(e.Place)
		p.w(", ")
		p.expr(e.Value)
		p.w(")")
	case *ast.Drop:
		p.w("drop(")
		p.w(e.Name)
		p.w(")")
	case *ast.TrapExpr:
		p.w("trap()")
	case *ast.ConvExpr:
		if e.Truncate {
			p.w("truncate[")
			p.w(e.To)
			p.w("](")
		} else {
			p.w(e.To)
			p.w("(")
		}
		p.expr(e.X)
		p.w(")")
	case *ast.StructLit:
		p.structLit(e)
	case *ast.ArrayLit:
		p.w("[")
		for i, el := range e.Elems {
			if i > 0 {
				p.w(", ")
			}
			p.expr(el)
		}
		p.w("]")
	case *ast.RangeExpr:
		p.expr(e.Lo)
		p.w("..")
		p.expr(e.Hi)
	case *ast.UnitLit:
		p.w("()")
	case *ast.Paren:
		p.w("(")
		p.expr(e.X)
		p.w(")")
	}
}

// startsWithOp reports whether the printed form of x begins with op, so that
// a unary operator applied to it needs a separating space (`- -x`).
func startsWithOp(x ast.Expr, op string) bool {
	switch x := x.(type) {
	case *ast.Unary:
		return x.Op == op
	case *ast.IntLit:
		return op == "-" && x.Neg
	}
	return false
}

// structLit prints `T { f: v }`; a literal written across lines keeps one
// field per line with trailing commas.
func (p *printer) structLit(e *ast.StructLit) {
	p.typeName(e.Type)
	if len(e.Fields) == 0 {
		p.w(" { }")
		return
	}
	if e.End <= len(p.src) && bytes.IndexByte(p.src[e.Start:e.End], '\n') >= 0 {
		p.w(" {")
		p.nl()
		p.indent++
		for _, f := range e.Fields {
			p.ind()
			p.w(f.Name)
			p.w(": ")
			p.expr(f.Value)
			p.w(",")
			p.nl()
		}
		p.indent--
		p.ind()
		p.w("}")
		return
	}
	p.w(" { ")
	for i, f := range e.Fields {
		if i > 0 {
			p.w(", ")
		}
		p.w(f.Name)
		p.w(": ")
		p.expr(f.Value)
	}
	p.w(" }")
}

// quote renders a string literal with the escapes the lexer understands.
func quote(s string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\n':
			sb.WriteString(`\n`)
		case '\t':
			sb.WriteString(`\t`)
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case 0:
			sb.WriteString(`\0`)
		default:
			sb.WriteByte(c)
		}
	}
	sb.WriteByte('"')
	return sb.String()
}
