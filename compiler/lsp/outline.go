package lsp

// Document symbols and signature help (specs/editor-outline): the outline
// of a document's own declarations, and the signature of the call whose
// argument list holds the cursor.

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/parser"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// SymbolKind values of LSP 3.17.
const (
	skMethod      = 6
	skField       = 8
	skEnum        = 10
	skInterface   = 11
	skFunction    = 12
	skVariable    = 13
	skConstant    = 14
	skObject      = 19
	skEnumMember  = 22
	skStructKind  = 23
	destructorSym = "destructor"
)

// DocumentSymbol is one outline item.
type DocumentSymbol struct {
	Name           string           `json:"name"`
	Detail         string           `json:"detail,omitempty"`
	Kind           int              `json:"kind"`
	Range          Range            `json:"range"`
	SelectionRange Range            `json:"selectionRange"`
	Children       []DocumentSymbol `json:"children,omitempty"`
}

type documentSymbolParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
}

// documentSymbol answers textDocument/documentSymbol (R1).
func (s *Server) documentSymbol(p documentSymbolParams) (any, *rpcError) {
	out := []DocumentSymbol{}
	d := s.kernDoc(p.TextDocument.URI)
	if d == nil {
		return out, nil
	}
	text := s.textOf(d.path)
	file, pds := parser.Parse(&source.File{Name: d.path, Bytes: text})
	if file == nil || len(pds) > 0 {
		return out, nil
	}
	// the resolved program gives the types; the document's own AST from
	// it when it has one, so that the two agree
	var mod *resolve.Module
	var info *resolve.Info
	if prog := s.opts.Resolve(s.load(d).files); prog != nil {
		info = prog.Info
		for _, m := range prog.Modules {
			for _, f := range m.Files {
				if f.Name == d.path {
					file, mod = f, m
				}
			}
		}
	}
	o := outliner{s: s, text: text, mod: mod, info: info}
	for _, decl := range file.Decls {
		if sym, ok := o.decl(decl); ok {
			out = append(out, sym)
		}
	}
	return out, nil
}

// outliner builds a document's symbols.
type outliner struct {
	s    *Server
	text []byte
	mod  *resolve.Module
	info *resolve.Info
}

func (o *outliner) rng(p ast.Pos) Range {
	return Range{Start: ToPosition(o.text, p.Start, o.s.enc), End: ToPosition(o.text, p.End, o.s.enc)}
}

// symbol is an item over span p named name, its selection the name's
// first whole occurrence inside p.
func (o *outliner) symbol(name string, kind int, p ast.Pos, detail string) DocumentSymbol {
	return DocumentSymbol{Name: name, Detail: detail, Kind: kind, Range: o.rng(p), SelectionRange: o.rng(nameIn(o.text, p, name))}
}

// nameIn is the span of name's first occurrence as a whole identifier
// inside p, outside comments and string and character literals (a
// contract's clauses and a comment may mention the name first), or p
// itself.
func nameIn(text []byte, p ast.Pos, name string) ast.Pos {
	if name == "" || p.Start < 0 || p.End > len(text) || p.Start > p.End {
		return p
	}
	for i := p.Start; i < p.End; i++ {
		switch c := text[i]; {
		case c == '/' && i+1 < p.End && text[i+1] == '/':
			for i < p.End && text[i] != '\n' {
				i++
			}
		case c == '"' || c == '\'':
			for i++; i < p.End && text[i] != c; i++ {
				if text[i] == '\\' {
					i++
				}
			}
		case wordByte(text, i):
			j := i
			for j < p.End && wordByte(text, j) {
				j++
			}
			if string(text[i:j]) == name {
				return ast.Pos{Start: i, End: j}
			}
			i = j - 1
		}
	}
	return p
}

func typeOrBlank(t resolve.Type) string {
	if t == nil {
		return ""
	}
	return t.String()
}

// funcType is a function's type as hover prints it, or "".
func (o *outliner) funcType(fd *ast.FuncDecl) string {
	if o.info == nil {
		return ""
	}
	if f := o.info.Funcs[fd]; f != nil && f.Type != nil {
		return f.Type.String()
	}
	return ""
}

func (o *outliner) decl(decl ast.Decl) (DocumentSymbol, bool) {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		return o.symbol(d.Name, skFunction, d.Pos, o.funcType(d)), true
	case *ast.StructDecl:
		sym := o.symbol(d.Name, skStructKind, d.Pos, "")
		var td *resolve.TypeDecl
		if o.mod != nil {
			td = o.mod.Types[d.Name]
		}
		for i, f := range d.Fields {
			detail := ""
			if td != nil && i < len(td.Fields) {
				detail = typeOrBlank(td.Fields[i].Type)
			}
			sym.Children = append(sym.Children, o.symbol(f.Name, skField, f.Pos, detail))
		}
		return sym, true
	case *ast.UnionDecl:
		sym := o.symbol(d.Name, skEnum, d.Pos, "")
		var td *resolve.TypeDecl
		if o.mod != nil {
			td = o.mod.Types[d.Name]
		}
		for i, v := range d.Variants {
			detail := ""
			if td != nil && i < len(td.Variants) {
				var types []string
				for _, f := range td.Variants[i].Fields {
					types = append(types, typeOrBlank(f.Type))
				}
				detail = strings.Join(types, ", ")
			}
			sym.Children = append(sym.Children, o.symbol(v.Name, skEnumMember, v.Pos, detail))
		}
		return sym, true
	case *ast.InterfaceDecl:
		sym := o.symbol(d.Name, skInterface, d.Pos, "")
		var id *resolve.InterfaceDecl
		if o.mod != nil {
			id = o.mod.Ifaces[d.Name]
		}
		for _, m := range d.Methods {
			detail := ""
			if id != nil {
				if f := id.Methods[m.Name]; f != nil && f.Type != nil {
					detail = f.Type.String()
				}
			}
			sym.Children = append(sym.Children, o.symbol(m.Name, skMethod, m.Pos, detail))
		}
		return sym, true
	case *ast.ImplDecl:
		target := o.source(d.Target.Span())
		name := "impl " + target
		if d.Iface != nil {
			name = "impl " + o.source(d.Iface.Pos) + " for " + target
		}
		sym := DocumentSymbol{Name: name, Kind: skObject, Range: o.rng(d.Pos), SelectionRange: o.rng(d.Target.Span())}
		// the destructor in source order among the functions
		type member struct {
			pos ast.Pos
			sym DocumentSymbol
		}
		var members []member
		for _, f := range d.Funcs {
			members = append(members, member{f.Pos, o.symbol(f.Name, skMethod, f.Pos, o.funcType(f))})
		}
		if d.Destructor != nil {
			members = append(members, member{d.Destructor.Pos, o.symbol(destructorSym, skMethod, d.Destructor.Pos, "")})
		}
		for i := 1; i < len(members); i++ {
			for j := i; j > 0 && members[j].pos.Start < members[j-1].pos.Start; j-- {
				members[j], members[j-1] = members[j-1], members[j]
			}
		}
		for _, m := range members {
			sym.Children = append(sym.Children, m.sym)
		}
		return sym, true
	case *ast.ConstDecl:
		detail := ""
		if o.mod != nil {
			if c := o.mod.Consts[d.Name]; c != nil {
				detail = typeOrBlank(c.Type)
			}
		}
		return o.symbol(d.Name, skConstant, d.Pos, detail), true
	case *ast.GlobalDecl:
		detail := ""
		if o.mod != nil {
			if g := o.mod.Globals[d.Name]; g != nil {
				detail = typeOrBlank(g.Type)
			}
		}
		return o.symbol(d.Name, skVariable, d.Pos, detail), true
	}
	return DocumentSymbol{}, false
}

// source is the text of p with whitespace runs collapsed to one space.
func (o *outliner) source(p ast.Pos) string {
	if p.Start < 0 || p.End > len(o.text) || p.Start > p.End {
		return ""
	}
	return strings.Join(strings.Fields(string(o.text[p.Start:p.End])), " ")
}

// ---- signature help ----

// SignatureHelp is textDocument/signatureHelp's result.
type SignatureHelp struct {
	Signatures      []SignatureInformation `json:"signatures"`
	ActiveSignature int                    `json:"activeSignature"`
	ActiveParameter int                    `json:"activeParameter"`
}

// SignatureInformation is one signature.
type SignatureInformation struct {
	Label      string                 `json:"label"`
	Parameters []ParameterInformation `json:"parameters"`
}

// ParameterInformation is a parameter: its [start, end) in the label, in
// UTF-16 code units.
type ParameterInformation struct {
	Label [2]int `json:"label"`
}

// signatureHelp answers textDocument/signatureHelp (R2).
func (s *Server) signatureHelp(p textDocumentPositionParams) (any, *rpcError) {
	d := s.kernDoc(p.TextDocument.URI)
	if d == nil {
		return nil, nil
	}
	pg := s.load(d)
	var src []byte
	for _, f := range pg.files {
		if f.Name == d.path {
			src = f.Bytes
		}
	}
	if src == nil {
		return nil, nil
	}
	off := ToOffset(src, p.Position, s.enc)
	call, lparen, prog := s.callAround(pg.files, d.path, src, off)
	if call == nil {
		// unclosed calls being typed (`pair(scale(`): close every one open
		// at the cursor
		if closers := openClosers(src[:off]); closers != "" {
			closed := append(append(append([]byte(nil), src[:off]...), closers...), src[off:]...)
			call, lparen, prog = s.callAround(pg.files, d.path, closed, off)
			src = closed
		}
	}
	if call == nil {
		return nil, nil
	}
	ci := prog.Info.Calls[call]
	if ci == nil || ci.Fn == nil {
		return nil, nil
	}
	sig := signatureOf(ci)
	active := 0
	if n := len(sig.Parameters); n > 0 {
		active = min(topLevelCommas(src[lparen+1:off]), n-1)
	}
	return SignatureHelp{Signatures: []SignatureInformation{sig}, ActiveParameter: active}, nil
}

// callAround resolves the program with text as the document's and returns
// the innermost call whose argument list holds off, with the offset of
// its `(`.
func (s *Server) callAround(files []*source.File, path string, text []byte, off int) (*ast.CallExpr, int, *resolve.Program) {
	var with []*source.File
	for _, f := range files {
		if f.Name == path {
			with = append(with, &source.File{Name: f.Name, Bytes: text})
			continue
		}
		with = append(with, f)
	}
	prog := s.opts.Resolve(with)
	if prog == nil {
		return nil, 0, nil
	}
	var best *ast.CallExpr
	bestParen := 0
	for _, m := range prog.Modules {
		for _, f := range m.Files {
			if f.Name != path {
				continue
			}
			ast.Walk(f, func(n any) bool {
				c, ok := n.(*ast.CallExpr)
				if !ok || c.Fn == nil {
					return false
				}
				lp := openParen(text, c.Fn.Span().End)
				rp := c.End - 1
				if lp < 0 || rp >= len(text) || text[rp] != ')' || off <= lp || off > rp {
					return false
				}
				if best == nil || c.End-c.Start < best.End-best.Start {
					best, bestParen = c, lp
				}
				return false
			})
		}
	}
	return best, bestParen, prog
}

// openParen is the offset of the `(` that opens a call's arguments: the
// first character after the callee that is not whitespace or a comment,
// or -1.
func openParen(text []byte, from int) int {
	for i := from; i >= 0 && i < len(text); i++ {
		switch {
		case text[i] == ' ' || text[i] == '\t' || text[i] == '\n' || text[i] == '\r':
		case text[i] == '/' && i+1 < len(text) && text[i+1] == '/':
			for i < len(text) && text[i] != '\n' {
				i++
			}
		case text[i] == '(':
			return i
		default:
			return -1
		}
	}
	return -1
}

// openClosers is what closes the parentheses and brackets open at the end
// of text since its last open brace, innermost first; strings, character
// literals and line comments are skipped.
func openClosers(text []byte) string {
	var stack []byte
	for i := 0; i < len(text); i++ {
		switch c := text[i]; c {
		case '(', '[', '{':
			stack = append(stack, c)
		case ')', ']', '}':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case '"', '\'':
			for i++; i < len(text) && text[i] != c; i++ {
				if text[i] == '\\' {
					i++
				}
			}
		case '/':
			if i+1 < len(text) && text[i+1] == '/' {
				for i < len(text) && text[i] != '\n' {
					i++
				}
			}
		}
	}
	var out []byte
	for i := len(stack) - 1; i >= 0 && stack[i] != '{'; i-- {
		if stack[i] == '(' {
			out = append(out, ')')
		} else {
			out = append(out, ']')
		}
	}
	return string(out)
}

// signatureOf is the label and parameters of a resolved call's function:
// the declaration's names with the call's instantiated types, a method's
// self left out.
func signatureOf(ci *resolve.CallInfo) SignatureInformation {
	var b strings.Builder
	b.WriteString(ci.Fn.Name)
	b.WriteByte('(')
	var params []ParameterInformation
	first := true
	for i, pd := range ci.Fn.Params {
		if ci.Method && pd.Self {
			continue
		}
		if !first {
			b.WriteString(", ")
		}
		first = false
		start := utf16Len(b.String())
		var t resolve.Type
		conv := pd.Conv
		if i < len(ci.Params) {
			t, conv = ci.Params[i].Type, ci.Params[i].Conv
		}
		b.WriteString(pd.Name + ": ")
		switch conv {
		case ast.ConvOwn:
			b.WriteString("own ")
		case ast.ConvOut:
			b.WriteString("out ")
		}
		if t == nil {
			b.WriteString("?")
		} else {
			b.WriteString(t.String())
		}
		params = append(params, ParameterInformation{Label: [2]int{start, utf16Len(b.String())}})
	}
	b.WriteByte(')')
	switch len(ci.Ret) {
	case 0:
	case 1:
		b.WriteString(" -> " + typeOrQuestion(ci.Ret[0]))
	default:
		b.WriteString(" -> " + (&resolve.Tuple{Elems: ci.Ret}).String())
	}
	if params == nil {
		params = []ParameterInformation{}
	}
	return SignatureInformation{Label: b.String(), Parameters: params}
}

func typeOrQuestion(t resolve.Type) string {
	if t == nil {
		return "?"
	}
	return t.String()
}

// utf16Len is s's length in UTF-16 code units.
func utf16Len(s string) int {
	n := 0
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		n += utf16.RuneLen(r)
		s = s[size:]
	}
	return n
}

// topLevelCommas counts the commas of text outside every nested bracket,
// brace and parenthesis and every string and character literal and line
// comment.
func topLevelCommas(text []byte) int {
	n, depth := 0, 0
	for i := 0; i < len(text); i++ {
		switch c := text[i]; c {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				n++
			}
		case '"', '\'':
			for i++; i < len(text) && text[i] != c; i++ {
				if text[i] == '\\' {
					i++
				}
			}
		case '/':
			if i+1 < len(text) && text[i+1] == '/' {
				for i < len(text) && text[i] != '\n' {
					i++
				}
			}
		}
	}
	return n
}
