package lsp

// Go-to-definition, find-references and rename (specs/editor-nav R1-R4,
// specs/editor-names): the names of a resolved program, each with a key
// that identifies its target, and the declaration of every target the
// program declares.

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/resolve"
)

// nameRef is a name: a span of a file and the key of its target.
type nameRef struct {
	file string
	span diag.Span
	key  string
}

// navIndex is a program's names and the declaration of each target the
// program declares; a target whose key has no declaration is a library
// declaration (or one outside the program).
type navIndex struct {
	refs  []nameRef
	decls map[string]nameRef
}

func tyKey(td *resolve.TypeDecl) string { return "type|" + td.Module + "|" + td.Name }

// ownerOf is the declared type a field access's subject has, through any
// access.
func ownerOf(t resolve.Type) *resolve.TypeDecl {
	for {
		switch x := t.(type) {
		case *resolve.Access:
			t = x.Elem
		case *resolve.Named:
			return x.Decl
		default:
			return nil
		}
	}
}

// symKey is the key of what an identifier's symbol names; "" for a module
// or type parameter, which have no declaration to go to.
func symKey(sym *resolve.Symbol) string {
	switch sym.Kind {
	case resolve.SymLocal, resolve.SymParam, resolve.SymAccess:
		return fmt.Sprintf("local|%s|%d|%d", sym.File, sym.Pos.Start, sym.Pos.End)
	case resolve.SymFunc:
		if sym.Func != nil {
			return fnKey(sym.Func)
		}
	case resolve.SymType:
		if sym.TDecl != nil {
			return tyKey(sym.TDecl)
		}
		if sym.Iface != nil {
			return "iface|" + sym.Iface.Module + "|" + sym.Iface.Name
		}
	case resolve.SymConst:
		if sym.Const != nil {
			return "const|" + sym.Const.Module + "|" + sym.Const.Name
		}
	case resolve.SymGlobal:
		if sym.Global != nil {
			return "global|" + sym.Global.Module + "|" + sym.Global.Name
		}
	}
	return ""
}

func fnKey(f *resolve.FuncDecl) string {
	return fmt.Sprintf("func|%s|%d|%d", f.File, f.Pos.Start, f.Pos.End)
}

// nameSpan is the span of the first whole-word occurrence of name in
// src[start:end] after keyword (when keyword is not ""), outside a `//`
// comment (kern's only comments); the start of the range, with name's
// length, when there is none.
func nameSpan(src []byte, start, end int, keyword, name string) diag.Span {
	if start < 0 {
		start = 0
	}
	if end > len(src) || end < start {
		end = len(src)
	}
	from := start
	if keyword != "" {
		if i := bytes.Index(src[start:end], []byte(keyword)); i >= 0 {
			from = start + i + len(keyword)
		}
	}
	for i := from; i+len(name) <= end; {
		j := bytes.Index(src[i:end], []byte(name))
		if j < 0 {
			break
		}
		at := i + j
		if !wordByte(src, at-1) && !wordByte(src, at+len(name)) && !inComment(src, at) {
			return diag.Span{Start: at, End: at + len(name)}
		}
		i = at + 1
	}
	return diag.Span{Start: start, End: start + len(name)}
}

// inComment reports whether offset at follows a `//` on its line that is
// not inside a string literal.
func inComment(src []byte, at int) bool {
	inString := false
	for i := bytes.LastIndexByte(src[:at], '\n') + 1; i < at; i++ {
		switch {
		case inString && src[i] == '\\':
			i++ // an escaped byte
		case src[i] == '"':
			inString = !inString
		case !inString && src[i] == '/' && i+1 < at && src[i+1] == '/':
			return true
		}
	}
	return false
}

func wordByte(src []byte, i int) bool {
	if i < 0 || i >= len(src) {
		return false
	}
	c := src[i]
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// buildNav indexes a resolved program's names (spec §2) over the files of
// its non-library modules, whose bytes text gives.
func buildNav(prog *resolve.Program, text map[string][]byte) *navIndex {
	idx := &navIndex{decls: map[string]nameRef{}}
	add := func(file string, span diag.Span, key string, decl bool) {
		if key == "" {
			return
		}
		r := nameRef{file: file, span: span, key: key}
		idx.refs = append(idx.refs, r)
		if decl {
			if _, ok := idx.decls[key]; !ok {
				idx.decls[key] = r
			}
		}
	}
	info := prog.Info
	// declarations of locals, parameters and bindings
	for _, sym := range info.Decls {
		if sym == nil || sym.File == "" || text[sym.File] == nil {
			continue
		}
		switch sym.Kind {
		case resolve.SymLocal, resolve.SymParam, resolve.SymAccess:
			add(sym.File, nameSpan(text[sym.File], sym.Pos.Start, sym.Pos.End, "", sym.Name), symKey(sym), true)
		}
	}
	for _, m := range prog.Modules {
		if m.Std {
			continue
		}
		for _, f := range m.Files {
			src := text[f.Name]
			if src == nil {
				continue
			}
			// declarations
			declFunc := func(fd *ast.FuncDecl) {
				if rf := info.Funcs[fd]; rf != nil {
					add(f.Name, nameSpan(src, fd.Start, fd.End, "func", fd.Name), fnKey(rf), true)
				}
			}
			for _, d := range f.Decls {
				switch x := d.(type) {
				case *ast.FuncDecl:
					declFunc(x)
				case *ast.ImplDecl:
					for _, fd := range x.Funcs {
						declFunc(fd)
					}
				case *ast.StructDecl:
					if td := m.Types[x.Name]; td != nil {
						add(f.Name, nameSpan(src, x.Start, x.End, "struct", x.Name), tyKey(td), true)
						for _, fl := range x.Fields {
							add(f.Name, nameSpan(src, fl.Start, fl.End, "", fl.Name), "field|"+tyKey(td)+"|"+fl.Name, true)
						}
					}
				case *ast.UnionDecl:
					if td := m.Types[x.Name]; td != nil {
						add(f.Name, nameSpan(src, x.Start, x.End, "union", x.Name), tyKey(td), true)
					}
				case *ast.InterfaceDecl:
					if id := m.Ifaces[x.Name]; id != nil {
						add(f.Name, nameSpan(src, x.Start, x.End, "interface", x.Name), "iface|"+id.Module+"|"+id.Name, true)
						// its methods: what a call through a constraint names
						for _, fn := range x.Methods {
							if fd := id.Methods[fn.Name]; fd != nil {
								add(f.Name, nameSpan(src, fn.Start, fn.End, "func", fn.Name), fnKey(fd), true)
							}
						}
					}
				case *ast.ConstDecl:
					if cd := m.Consts[x.Name]; cd != nil {
						add(f.Name, nameSpan(src, x.Start, x.End, "const", x.Name), "const|"+cd.Module+"|"+cd.Name, true)
					}
				case *ast.GlobalDecl:
					if gd := m.Globals[x.Name]; gd != nil {
						add(f.Name, nameSpan(src, x.Start, x.End, "thread_local", x.Name), "global|"+gd.Module+"|"+gd.Name, true)
					}
				}
			}
			// uses
			ast.Walk(f, func(n any) bool {
				switch x := n.(type) {
				case *ast.Ident:
					if sym := info.Idents[x]; sym != nil {
						add(f.Name, diag.Span{Start: x.Start, End: x.End}, symKey(sym), false)
					}
				case *ast.CallExpr:
					if fe, ok := x.Fn.(*ast.FieldExpr); ok {
						if ci := info.Calls[x]; ci != nil && ci.Fn != nil {
							add(f.Name, diag.Span{Start: fe.End - len(fe.Name), End: fe.End}, fnKey(ci.Fn), false)
						}
					}
				case *ast.FieldExpr:
					span := diag.Span{Start: x.End - len(x.Name), End: x.End}
					if info.Fields[x] != nil {
						if td := ownerOf(info.Types[x.X]); td != nil {
							add(f.Name, span, "field|"+tyKey(td)+"|"+x.Name, false)
						}
					}
					if cd := info.ImportedConsts[x]; cd != nil {
						add(f.Name, span, "const|"+cd.Module+"|"+cd.Name, false)
					}
				case *ast.TypeName:
					// a type or interface named in an annotation, a literal,
					// an impl or a constraint (specs/editor-names R1)
					from := x.Start
					if x.Pkg != "" {
						if i := bytes.IndexByte(src[min(x.Start, len(src)):min(x.End, len(src))], '.'); i >= 0 {
							from = x.Start + i + 1
						}
					}
					span := nameSpan(src, from, x.End, "", x.Name)
					if td := info.TypeNames[x]; td != nil {
						add(f.Name, span, tyKey(td), false)
					} else if id := info.IfaceNames[x]; id != nil {
						add(f.Name, span, "iface|"+id.Module+"|"+id.Name, false)
					}
				case *ast.StructLit:
					// the field names of a struct literal (R1)
					if td := ownerOf(info.Types[x]); td != nil {
						for _, fi := range x.Fields {
							add(f.Name, nameSpan(src, fi.Start, fi.End, "", fi.Name), "field|"+tyKey(td)+"|"+fi.Name, false)
						}
					}
				}
				return false
			})
		}
	}
	return idx
}

// at is the innermost name of file whose span holds off, the end included.
func (idx *navIndex) at(file string, off int) (nameRef, bool) {
	var best nameRef
	found := false
	for _, r := range idx.refs {
		if r.file != file || r.span.Start > off || off > r.span.End {
			continue
		}
		if !found || r.span.End-r.span.Start < best.span.End-best.span.Start {
			best, found = r, true
		}
	}
	return best, found
}

// navTarget resolves a kern document's program and finds the name at a
// position; ok is false when there is no document, program or name.
func (s *Server) navTarget(uri string, p Position) (*navIndex, nameRef, bool) {
	d := s.kernDoc(uri)
	if d == nil {
		return nil, nameRef{}, false
	}
	pg := s.load(d)
	text := map[string][]byte{}
	for _, f := range pg.files {
		text[f.Name] = f.Bytes
	}
	src := text[d.path]
	if src == nil {
		return nil, nameRef{}, false
	}
	idx := buildNav(s.opts.Resolve(pg.files), text)
	r, ok := idx.at(d.path, ToOffset(src, p, s.enc))
	return idx, r, ok
}

func (s *Server) location(r nameRef) Location {
	return Location{URI: s.uriOf(r.file), Range: s.rangeOf(s.textOf(r.file), r.span.Start, r.span.End)}
}

// definition answers textDocument/definition (spec R1, R2a).
func (s *Server) definition(p textDocumentPositionParams) (any, *rpcError) {
	idx, r, ok := s.navTarget(p.TextDocument.URI, p.Position)
	if !ok {
		return nil, nil
	}
	decl, ok := idx.decls[r.key]
	if !ok {
		return nil, nil // a library declaration
	}
	return s.location(decl), nil
}

// references answers textDocument/references (spec R2, R2a).
func (s *Server) references(p referenceParams) (any, *rpcError) {
	out := []Location{}
	idx, r, ok := s.navTarget(p.TextDocument.URI, p.Position)
	if !ok {
		return out, nil
	}
	decl, ok := idx.decls[r.key]
	if !ok {
		return out, nil // a library declaration
	}
	type at struct {
		file  string
		start int
	}
	seen := map[at]bool{}
	var refs []nameRef
	for _, x := range idx.refs {
		if x.key != r.key {
			continue
		}
		if !p.Context.IncludeDeclaration && x.file == decl.file && x.span == decl.span {
			continue
		}
		k := at{x.file, x.span.Start}
		if seen[k] {
			continue
		}
		seen[k] = true
		refs = append(refs, x)
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].file != refs[j].file {
			return refs[i].file < refs[j].file
		}
		return refs[i].span.Start < refs[j].span.Start
	})
	for _, x := range refs {
		out = append(out, s.location(x))
	}
	return out, nil
}
