package lsp

// Rename (specs/editor-names R2-R4): every name of the document's program
// whose target is the target at the position is replaced, after checking
// in memory that the renamed program has no more errors and that every
// name still means what it meant.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/lexer"
	"github.com/dshills/kern/compiler/parser"
	"github.com/dshills/kern/compiler/project"
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

// rename answers textDocument/rename (R3, R4): the document's program and,
// with a workspace root, every other program of the workspace that loads
// the file declaring the target (specs/editor-workspace R1-R4). A refusal
// in any program refuses the rename.
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
	type site struct {
		file  string
		start int
	}
	edits := map[site]nameRef{}
	texts := map[string][]byte{}
	run := func(pg program, text map[string][]byte, idx *navIndex) string {
		sites, why := s.renameIn(pg, text, idx, decl.key, old, p.NewName)
		for _, x := range sites {
			edits[site{x.file, x.span.Start}] = x
			texts[x.file] = text[x.file]
		}
		return why
	}
	if why := run(pg, text, idx); why != "" {
		return fail("%s", why)
	}
	// programs are compared by their files: an entry whose program has
	// exactly the files of one already renamed is that program
	done := map[string]bool{filesKey(pg): true}
	for _, entry := range s.workspaceEntries() {
		other := s.loadPath(entry)
		if len(other.diags) > 0 {
			continue // a program that does not load is not checked (spec §4)
		}
		if k := filesKey(other); done[k] {
			continue
		} else {
			done[k] = true
		}
		otext := map[string][]byte{}
		holds := false
		for _, f := range other.files {
			otext[f.Name] = f.Bytes
			holds = holds || f.Name == decl.file
		}
		if !holds {
			continue // unaffected: it does not load the declaring file
		}
		if why := run(other, otext, buildNav(s.opts.Resolve(other.files), otext)); why != "" {
			return fail("%s (in the program of %s)", why, entry)
		}
	}
	all := make([]nameRef, 0, len(edits))
	for _, x := range edits {
		all = append(all, x)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].file != all[j].file {
			return all[i].file < all[j].file
		}
		return all[i].span.Start < all[j].span.Start
	})
	edit := WorkspaceEdit{Changes: map[string][]TextEdit{}}
	for _, x := range all {
		u := s.uriOf(x.file)
		edit.Changes[u] = append(edit.Changes[u], TextEdit{Range: s.rangeOf(texts[x.file], x.span.Start, x.span.End), NewText: p.NewName})
	}
	return edit, nil
}

// filesKey identifies a program by the sorted names of its files.
func filesKey(pg program) string {
	names := make([]string, 0, len(pg.files))
	for _, f := range pg.files {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	return strings.Join(names, "\x00")
}

// renameIn renames the target key from old to name in one program: the
// names to edit, or why the rename is refused there (the program would
// gain errors, or a name would change what it refers to).
func (s *Server) renameIn(pg program, text map[string][]byte, idx *navIndex, key, old, name string) ([]nameRef, string) {
	type site struct {
		file  string
		start int
	}
	seen := map[site]bool{}
	var sites []nameRef
	for _, x := range idx.refs {
		if x.key != key || seen[site{x.file, x.span.Start}] {
			continue
		}
		seen[site{x.file, x.span.Start}] = true
		if string(text[x.file][x.span.Start:x.span.End]) != old {
			return nil, fmt.Sprintf("cannot locate every occurrence of %s", old)
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
	delta := len(name) - len(old)
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
				out = append(append(out, b[prev:at]...), name...)
				prev = at + len(old)
			}
			b = append(out, b[prev:]...)
		}
		newText[f.Name] = b
		files = append(files, &source.File{Name: f.Name, Bytes: b})
	}
	if n, o := s.errorCount(files), s.errorCount(pg.files); n > o {
		return nil, fmt.Sprintf("renaming %s to %s introduces errors", old, name)
	}
	// every name keeps its meaning: the name at the shifted position of
	// each old name has the shifted declaration (or the same library key)
	nidx := buildNav(s.opts.Resolve(files), newText)
	byPos := map[site]nameRef{}
	for _, x := range nidx.refs {
		byPos[site{x.file, x.span.Start}] = x
	}
	changes := fmt.Sprintf("renaming %s to %s changes what a name refers to", old, name)
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
			return nil, changes
		case oldDeclared && (nd.file != od.file || nd.span.Start != shift(od.file, od.span.Start)):
			return nil, changes
		case !oldDeclared && x.key != y.key:
			return nil, changes
		}
	}
	if distinct(nidx) != len(count) {
		return nil, changes
	}
	return sites, ""
}

// maxEntries caps the workspace programs a rename loads (spec §4).
const maxEntries = 200

// workspaceEntries are the workspace's candidate entries, sorted: every
// manifest's entries, and every file that is not a test file and declares
// a module-level func main, outside std/, the prelude, .git, testdata and
// .claude (specs/editor-workspace glossary). Without a root there are none.
func (s *Server) workspaceEntries() []string {
	if s.root == "" {
		return nil
	}
	set := map[string]bool{}
	werr := filepath.WalkDir(s.root, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			s.logMessage(2, "rename: cannot read %s: %v", path, err)
			if e != nil && e.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if e.IsDir() {
			switch name := e.Name(); {
			case path != s.root && (name == ".git" || name == "testdata" || name == ".claude"):
				return fs.SkipDir
			case name == "std" && filepath.Dir(path) == s.root:
				return fs.SkipDir
			case strings.HasSuffix(path, filepath.Join("compiler", "resolve", "prelude")):
				return fs.SkipDir
			}
			return nil
		}
		switch {
		case e.Name() == project.ManifestName:
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				s.logMessage(2, "rename: cannot read %s: %v", path, rerr)
				return nil
			}
			if m, ds := project.ParseManifest(path, b); len(ds) == 0 && m != nil {
				for _, entry := range m.Entry {
					set[filepath.Join(filepath.Dir(path), entry)] = true
				}
			}
		case strings.HasSuffix(path, ".kern") && !project.TestFile(path):
			if declaresMain(path, s.textOf(path)) {
				set[path] = true
			}
		}
		return nil
	})
	if werr != nil {
		s.logMessage(2, "rename: walking %s: %v", s.root, werr)
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	if len(out) > maxEntries {
		s.logMessage(2, "rename: the workspace has %d programs; only the first %d are renamed", len(out), maxEntries)
		out = out[:maxEntries]
	}
	return out
}

// declaresMain reports whether a kern file parses and declares a
// module-level func main.
func declaresMain(path string, text []byte) bool {
	f, ds := parser.Parse(&source.File{Name: path, Bytes: text})
	if len(ds) > 0 || f == nil {
		return false
	}
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name == "main" {
			return true
		}
	}
	return false
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
