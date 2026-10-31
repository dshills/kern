package lsp

// Rename (specs/editor-names R2-R4): every name of the document's program
// whose target is the target at the position is replaced, after checking
// in memory that the renamed program has no more errors and that every
// name still means what it meant.

import (
	"fmt"
	"sort"

	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/lexer"
	"github.com/dshills/kern/compiler/source"
)

// codeRequestFailed is LSP's RequestFailed: a valid request the server
// could not carry out.
const codeRequestFailed = -32803

type renameParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	NewName      string                 `json:"newName"`
}

// renameTarget is the program of a document and the renameable name at a
// position: one whose target the program declares.
func (s *Server) renameTarget(uri string, p Position) (program, map[string][]byte, *navIndex, nameRef, nameRef, bool) {
	d := s.kernDoc(uri)
	if d == nil {
		return program{}, nil, nil, nameRef{}, nameRef{}, false
	}
	pg := s.load(d)
	text := map[string][]byte{}
	for _, f := range pg.files {
		text[f.Name] = f.Bytes
	}
	src := text[d.path]
	if src == nil {
		return program{}, nil, nil, nameRef{}, nameRef{}, false
	}
	idx := buildNav(s.opts.Resolve(pg.files), text)
	r, ok := idx.at(d.path, ToOffset(src, p, s.enc))
	if !ok {
		return program{}, nil, nil, nameRef{}, nameRef{}, false
	}
	decl, ok := idx.decls[r.key]
	return pg, text, idx, r, decl, ok
}

// prepareRename answers textDocument/prepareRename: the range of the name
// and its text, or null where nothing can be renamed (R2).
func (s *Server) prepareRename(p textDocumentPositionParams) (any, *rpcError) {
	_, text, _, r, decl, ok := s.renameTarget(p.TextDocument.URI, p.Position)
	if !ok {
		return nil, nil
	}
	name := string(text[decl.file][decl.span.Start:decl.span.End])
	return map[string]any{"range": s.rangeOf(text[r.file], r.span.Start, r.span.End), "placeholder": name}, nil
}

// rename answers textDocument/rename (R3, R4).
func (s *Server) rename(p renameParams) (any, *rpcError) {
	fail := func(format string, args ...any) (any, *rpcError) {
		return nil, &rpcError{Code: codeRequestFailed, Message: fmt.Sprintf(format, args...)}
	}
	pg, text, idx, _, decl, ok := s.renameTarget(p.TextDocument.URI, p.Position)
	if !ok {
		return fail("there is no name declared in this program at the position")
	}
	if !validIdent(p.NewName) {
		return fail("%q is not an identifier, or is a keyword", p.NewName)
	}
	old := string(text[decl.file][decl.span.Start:decl.span.End])
	if p.NewName == old {
		return WorkspaceEdit{Changes: map[string][]TextEdit{}}, nil
	}
	// every name of the target, each spelling the old name
	type site struct {
		file  string
		start int
	}
	seen := map[site]bool{}
	var sites []nameRef
	for _, x := range idx.refs {
		if x.key != decl.key || seen[site{x.file, x.span.Start}] {
			continue
		}
		seen[site{x.file, x.span.Start}] = true
		if string(text[x.file][x.span.Start:x.span.End]) != old {
			return fail("cannot locate every occurrence of %s", old)
		}
		sites = append(sites, x)
	}
	// the renamed program, in memory
	starts := map[string][]int{}
	for _, x := range sites {
		starts[x.file] = append(starts[x.file], x.span.Start)
	}
	for f := range starts {
		sort.Ints(starts[f])
	}
	delta := len(p.NewName) - len(old)
	shift := func(file string, off int) int {
		return off + delta*sort.SearchInts(starts[file], off) // edits strictly before off
	}
	var files []*source.File
	newText := map[string][]byte{}
	for _, f := range pg.files {
		b := f.Bytes
		if ss := starts[f.Name]; len(ss) > 0 {
			var out []byte
			prev := 0
			for _, at := range ss {
				out = append(append(out, b[prev:at]...), p.NewName...)
				prev = at + len(old)
			}
			b = append(out, b[prev:]...)
		}
		newText[f.Name] = b
		files = append(files, &source.File{Name: f.Name, Bytes: b})
	}
	if n, o := s.errorCount(files), s.errorCount(pg.files); n > o {
		return fail("renaming %s to %s introduces errors", old, p.NewName)
	}
	// every name keeps its meaning: the name at the shifted position of
	// each old name has the shifted declaration (or the same library key)
	nidx := buildNav(s.opts.Resolve(files), newText)
	byPos := map[site]nameRef{}
	for _, x := range nidx.refs {
		byPos[site{x.file, x.span.Start}] = x
	}
	count := map[site]bool{}
	for _, x := range idx.refs {
		k := site{x.file, shift(x.file, x.span.Start)}
		if count[k] {
			continue
		}
		count[k] = true
		y, found := byPos[k]
		od, oldDeclared := idx.decls[x.key]
		nd, newDeclared := nidx.decls[y.key]
		switch {
		case !found, oldDeclared != newDeclared:
			return fail("renaming %s to %s changes what a name refers to", old, p.NewName)
		case oldDeclared && (nd.file != od.file || nd.span.Start != shift(od.file, od.span.Start)):
			return fail("renaming %s to %s changes what a name refers to", old, p.NewName)
		case !oldDeclared && x.key != y.key:
			return fail("renaming %s to %s changes what a name refers to", old, p.NewName)
		}
	}
	if distinct(nidx) != len(count) {
		return fail("renaming %s to %s changes what a name refers to", old, p.NewName)
	}
	edit := WorkspaceEdit{Changes: map[string][]TextEdit{}}
	sort.Slice(sites, func(i, j int) bool {
		if sites[i].file != sites[j].file {
			return sites[i].file < sites[j].file
		}
		return sites[i].span.Start < sites[j].span.Start
	})
	for _, x := range sites {
		u := s.uriOf(x.file)
		edit.Changes[u] = append(edit.Changes[u], TextEdit{Range: s.rangeOf(text[x.file], x.span.Start, x.span.End), NewText: p.NewName})
	}
	return edit, nil
}

// distinct is the number of distinct name positions of an index.
func distinct(idx *navIndex) int {
	type site struct {
		file  string
		start int
	}
	seen := map[site]bool{}
	for _, x := range idx.refs {
		seen[site{x.file, x.span.Start}] = true
	}
	return len(seen)
}

// errorCount is the number of error diagnostics resolution and checking
// report for files.
func (s *Server) errorCount(files []*source.File) int {
	prog := s.opts.Resolve(files)
	ds := prog.Diags
	if len(ds) == 0 {
		ds = check.Check(prog)
	}
	n := 0
	for _, d := range ds {
		if d.Severity == "error" {
			n++
		}
	}
	return n
}

// validIdent reports whether name can name a declaration: an identifier
// that is not a keyword.
func validIdent(name string) bool {
	if name == "" || name == "_" || lexer.IsKeyword(name) {
		return false
	}
	for i, c := range name {
		letter := c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		if !letter && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return true
}
