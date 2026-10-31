package lsp

// Completion (specs/editor-complete): after `x.` the fields and methods of
// x's type, the static functions and variants of a type, or the exported
// declarations of an imported module; elsewhere the locals in scope, the
// module's and the prelude's declarations, the imported modules and the
// keywords. Every item is filtered by the identifier prefix before the
// cursor.

import (
	"sort"
	"strings"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/lexer"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// CompletionItemKind values of LSP 3.17.
const (
	ciMethod    = 2
	ciFunction  = 3
	ciField     = 5
	ciVariable  = 6
	ciInterface = 8
	ciModule    = 9
	ciEnum      = 13
	ciKeyword   = 14
	ciConstant  = 21
	ciStruct    = 22
	ciEnumMem   = 20
)

// CompletionItem is one completion.
type CompletionItem struct {
	Label  string `json:"label"`
	Kind   int    `json:"kind"`
	Detail string `json:"detail,omitempty"`
}

// CompletionList is textDocument/completion's result.
type CompletionList struct {
	IsIncomplete bool             `json:"isIncomplete"`
	Items        []CompletionItem `json:"items"`
}

// placeholder stands for the member being typed after a `.` with nothing
// after it yet, so that the text parses and the receiver resolves.
const placeholder = "kern_complete_"

// completion answers textDocument/completion (R1-R4).
func (s *Server) completion(p textDocumentPositionParams) (any, *rpcError) {
	out := CompletionList{Items: []CompletionItem{}}
	d := s.kernDoc(p.TextDocument.URI)
	if d == nil {
		return out, nil
	}
	pg := s.load(d)
	var src []byte
	for _, f := range pg.files {
		if f.Name == d.path {
			src = f.Bytes
		}
	}
	if src == nil {
		return out, nil
	}
	off := ToOffset(src, p.Position, s.enc)
	start := off
	for start > 0 && wordByte(src, start-1) {
		start--
	}
	prefix := string(src[start:off])
	member := start > 0 && src[start-1] == '.'
	// the text as resolved: with a placeholder member when nothing follows
	// the dot yet
	text := src
	if member && prefix == "" {
		text = append(append(append([]byte(nil), src[:off]...), placeholder...), src[off:]...)
	}
	var files []*source.File
	for _, f := range pg.files {
		if f.Name == d.path {
			files = append(files, &source.File{Name: f.Name, Bytes: text})
			continue
		}
		files = append(files, f)
	}
	prog := s.opts.Resolve(files)
	var file *ast.File
	var mod *resolve.Module
	for _, m := range prog.Modules {
		for _, f := range m.Files {
			if f.Name == d.path {
				file, mod = f, m
			}
		}
	}
	add := func(label string, kind int, detail string) {
		if strings.HasPrefix(label, prefix) && !strings.HasPrefix(label, "#") && label != placeholder {
			out.Items = append(out.Items, CompletionItem{Label: label, Kind: kind, Detail: detail})
		}
	}
	if member {
		if file != nil {
			s.memberItems(prog, mod, file, start, add)
		}
	} else {
		if file != nil {
			localItems(prog, file, start, add)
		}
		if mod != nil {
			moduleItems(mod, true, add)
			for alias := range mod.Imports {
				add(alias, ciModule, "module")
			}
		}
		if prog.Core != nil {
			moduleItems(prog.Core, false, add)
		}
		for _, k := range lexer.Keywords() {
			add(k, ciKeyword, "")
		}
	}
	sort.SliceStable(out.Items, func(i, j int) bool {
		if out.Items[i].Label != out.Items[j].Label {
			return out.Items[i].Label < out.Items[j].Label
		}
		return out.Items[i].Kind < out.Items[j].Kind
	})
	// one item per label and kind (a name declared twice, a local shadowing)
	dedup := out.Items[:0]
	for i, it := range out.Items {
		if i > 0 && it.Label == out.Items[i-1].Label && it.Kind == out.Items[i-1].Kind {
			continue
		}
		dedup = append(dedup, it)
	}
	out.Items = dedup
	return out, nil
}

// memberItems completes the member of the field expression whose name
// starts at offset at.
func (s *Server) memberItems(prog *resolve.Program, mod *resolve.Module, file *ast.File, at int, add func(string, int, string)) {
	var fe *ast.FieldExpr
	ast.Walk(file, func(n any) bool {
		if x, ok := n.(*ast.FieldExpr); ok && x.End-len(x.Name) == at {
			fe = x
		}
		return false
	})
	if fe == nil {
		return
	}
	info := prog.Info
	// an imported module: its exported declarations
	if id, ok := fe.X.(*ast.Ident); ok && mod != nil {
		if sym := info.Idents[id]; sym == nil || sym.Kind == resolve.SymModule {
			if m := mod.Imports[id.Name]; m != nil {
				moduleItems(m, m.Std, add)
				return
			}
		}
		// a type: its static functions and its variants
		if sym := info.Idents[id]; sym != nil && sym.Kind == resolve.SymType && sym.TDecl != nil {
			typeStatics(sym.TDecl, add)
			return
		}
	}
	// a qualified type (lib.Point.): its static functions and variants
	if q, ok := fe.X.(*ast.FieldExpr); ok {
		if id, ok := q.X.(*ast.Ident); ok && mod != nil {
			if m := mod.Imports[id.Name]; m != nil {
				if td := m.Types[q.Name]; td != nil {
					typeStatics(td, add)
					return
				}
			}
		}
	}
	// a value: the fields and methods of its type, through any access
	td := ownerOf(info.Types[fe.X])
	if td == nil {
		return
	}
	visible := func(name string) bool {
		return td.Std || mod == nil || td.Module == mod.Name || isExported(name)
	}
	for _, f := range td.Fields {
		if visible(f.Name) {
			add(f.Name, ciField, typeLabel(f.Type))
		}
	}
	for name, fd := range td.Methods {
		if hasSelf(fd) {
			add(name, ciMethod, fd.Type.String())
		}
	}
}

// hasSelf reports a method with a self parameter (called on a value).
func hasSelf(fd *resolve.FuncDecl) bool { return len(fd.Params) > 0 && fd.Params[0].Self }

// typeStatics adds a type's functions without self and its variants.
func typeStatics(td *resolve.TypeDecl, add func(string, int, string)) {
	for name, fd := range td.Methods {
		if !hasSelf(fd) {
			add(name, ciFunction, fd.Type.String())
		}
	}
	for _, v := range td.Variants {
		add(v.Name, ciEnumMem, td.Name)
	}
}

// moduleItems adds a module's declarations; all of them inside the module
// or for a standard-library one, the exported ones otherwise.
func moduleItems(m *resolve.Module, all bool, add func(string, int, string)) {
	ok := func(name string) bool { return all || m.Std || isExported(name) }
	for name, fd := range m.Funcs {
		if ok(name) && fd.Receiver == nil {
			add(name, ciFunction, fd.Type.String())
		}
	}
	for name, td := range m.Types {
		if ok(name) {
			kind := ciStruct
			if td.Kind == resolve.KindUnion {
				kind = ciEnum
			}
			add(name, kind, "type")
		}
	}
	for name := range m.Ifaces {
		if ok(name) {
			add(name, ciInterface, "interface")
		}
	}
	for name, cd := range m.Consts {
		if ok(name) {
			add(name, ciConstant, typeLabel(cd.Type))
		}
	}
	for name, gd := range m.Globals {
		if ok(name) {
			add(name, ciVariable, typeLabel(gd.Type))
		}
	}
}

// localItems adds the locals, parameters and bindings in scope at offset
// at: declared before it in the function containing it, in a block that
// encloses it (the innermost block holding the declaration is one of the
// cursor's enclosing blocks).
func localItems(prog *resolve.Program, file *ast.File, at int, add func(string, int, string)) {
	var fn *ast.FuncDecl
	ast.Walk(file, func(n any) bool {
		if f, ok := n.(*ast.FuncDecl); ok && f.Body != nil && f.Start <= at && at <= f.End {
			fn = f
		}
		return false
	})
	if fn == nil {
		return
	}
	var blocks []*ast.Block
	ast.Walk(fn.Body, func(n any) bool {
		if b, ok := n.(*ast.Block); ok {
			blocks = append(blocks, b)
		}
		return false
	})
	innermost := func(pos int) *ast.Block {
		var best *ast.Block
		for _, b := range blocks {
			if b.Start <= pos && pos <= b.End && (best == nil || b.End-b.Start < best.End-best.Start) {
				best = b
			}
		}
		return best
	}
	encloses := map[*ast.Block]bool{}
	for _, b := range blocks {
		if b.Start <= at && at <= b.End {
			encloses[b] = true
		}
	}
	for _, sym := range prog.Info.Decls {
		if sym == nil || sym.File != file.Name || sym.Pos.Start < fn.Start || sym.Pos.Start >= at {
			continue
		}
		switch sym.Kind {
		case resolve.SymParam:
			if sym.Pos.Start < fn.Body.Start {
				add(sym.Name, ciVariable, typeLabel(sym.Type))
			}
		case resolve.SymLocal, resolve.SymAccess:
			if b := innermost(sym.Pos.Start); b == nil || encloses[b] {
				add(sym.Name, ciVariable, typeLabel(sym.Type))
			}
		}
	}
}

func typeLabel(t resolve.Type) string {
	if t == nil {
		return ""
	}
	return t.String()
}

// isExported is the resolver's rule: a name starting with a capital.
func isExported(name string) bool { return name != "" && name[0] >= 'A' && name[0] <= 'Z' }
