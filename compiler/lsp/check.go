package lsp

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dshills/kern/compiler/check"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/parser"
	"github.com/dshills/kern/compiler/project"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// defaultCheck is `kern check`'s work on a file set: resolution, then
// checking when resolution reports nothing.
func defaultCheck(srcs []*source.File) ([]diag.Diagnostic, error) {
	prog := resolve.Resolve(srcs)
	if len(prog.Diags) > 0 {
		return prog.Diags, nil
	}
	return check.Check(prog), nil
}

// holder is the document that supplies a path's text: of the documents
// with that path, the one opened or changed last (spec §2).
func (s *Server) holder(path string) *document {
	var best *document
	for _, d := range s.docs {
		if d.path == path && (best == nil || d.seq > best.seq) {
			best = d
		}
	}
	return best
}

// overlay is the loader's overlay: a document's text for its path.
func (s *Server) overlay(path string) ([]byte, bool) {
	if d := s.holder(path); d != nil {
		return append([]byte(nil), d.text...), true
	}
	return nil, false
}

// textOf is a file's text through the overlay; nil when it cannot be read.
func (s *Server) textOf(path string) []byte {
	if b, ok := s.overlay(path); ok {
		return b
	}
	b, _ := os.ReadFile(path)
	return b
}

// uriOf is the URI a path is published on: the URI of the document that
// holds it, else its file URI.
func (s *Server) uriOf(path string) string {
	if d := s.holder(path); d != nil {
		return d.uri
	}
	return URIOf(path)
}

// program is a loaded program: its files with absolute names, the
// loader's diagnostics, and the manifest's absolute path ("" for none).
type program struct {
	dir      string
	files    []*source.File
	diags    []diag.Diagnostic
	manifest string
}

// absName makes a loader name absolute against the document's directory.
func absName(dir, name string) string {
	if name == "" || filepath.IsAbs(name) {
		return name
	}
	return filepath.Join(dir, name)
}

// load loads the program of a kern document (spec §2).
func (s *Server) load(d *document) program {
	dir := filepath.Dir(d.path)
	entries := []string{d.path}
	tests := false
	pro, pds := parser.ParsePrologue(&source.File{Name: d.path, Bytes: s.textOf(d.path)})
	if len(pds) == 0 {
		tests = project.TestFile(d.path)
		if listing, err := os.ReadDir(dir); err == nil {
			for _, e := range listing {
				p := filepath.Join(dir, e.Name())
				if p == d.path || !strings.HasSuffix(e.Name(), ".kern") || project.TestFile(p) {
					continue
				}
				if st, err := os.Stat(p); err != nil || !st.Mode().IsRegular() {
					continue
				}
				text, ok := s.overlay(p)
				if !ok {
					b, err := os.ReadFile(p)
					if err != nil {
						continue
					}
					text = b
				}
				if sp, ds := parser.ParsePrologue(&source.File{Name: p, Bytes: text}); len(ds) == 0 && sp.Module == pro.Module {
					entries = append(entries, p)
				}
			}
		}
	}
	r := project.LoadWith(dir, entries, project.Options{Discover: true, Tests: tests, Overlay: s.overlay})
	pg := program{dir: dir, files: r.Files, diags: r.Diags}
	for _, f := range pg.files {
		f.Name = absName(dir, f.Name)
	}
	for i := range pg.diags {
		pg.diags[i].File = absName(dir, pg.diags[i].File)
	}
	if r.Manifest != nil {
		pg.manifest = absName(dir, filepath.Join(r.Root, project.ManifestName))
	}
	return pg
}

// checkDoc checks a kern document's program and publishes the result
// (spec R1, R2, R5).
func (s *Server) checkDoc(d *document) {
	var pg program
	var ds []diag.Diagnostic
	var failure string
	func() {
		defer func() {
			if r := recover(); r != nil {
				failure = fmt.Sprint(r)
			}
		}()
		pg = s.load(d)
		if len(pg.diags) > 0 {
			ds = pg.diags
			return
		}
		var err error
		if ds, err = s.opts.Check(pg.files); err != nil {
			failure = err.Error()
		}
	}()
	if failure != "" {
		ds = []diag.Diagnostic{diag.New("INTERNAL", d.path, diag.Span{}, "%s", failure)}
		s.logMessage(1, "check of %s: %s", d.uri, failure)
	}
	s.publish(d, pg, ds)
	if s.opts.OnCheck != nil {
		s.opts.OnCheck(d.uri)
	}
}

// publish sends the diagnostics of one check of d's program (spec R2,
// R3) and records them for R4 and R7.
func (s *Server) publish(d *document, pg program, ds []diag.Diagnostic) {
	inProgram := map[string]bool{}
	targets := map[string]bool{}
	for _, f := range pg.files {
		inProgram[f.Name] = true
		targets[f.Name] = true
	}
	byFile := map[string][]diag.Diagnostic{}
	for _, k := range ds {
		k.File = absName(pg.dir, k.File)
		if k.File == "" || !inProgram[k.File] && k.File != pg.manifest {
			if k.File != "" {
				k.Message = k.File + ": " + k.Message
			}
			k.File, k.Span, k.Related = d.path, diag.Span{}, nil
		}
		targets[k.File] = true
		byFile[k.File] = append(byFile[k.File], k)
	}
	paths := make([]string, 0, len(targets))
	for p := range targets {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	now := map[string]bool{}
	for _, p := range paths {
		uri := s.uriOf(p)
		now[uri] = true
		s.publishURI(uri, p, byFile[p], d.uri, true)
	}
	// a URI this check no longer publishes is cleared unless another
	// document's last check published it (spec R2)
	var gone []string
	for uri := range s.docPubs[d.uri] {
		if !now[uri] {
			gone = append(gone, uri)
		}
	}
	sort.Strings(gone)
	s.docPubs[d.uri] = now
	s.release(d.uri, gone)
}

// publishURI publishes diagnostics of one file on uri and records owner
// as one of its owners (or removes it, when own is false).
func (s *Server) publishURI(uri, path string, ds []diag.Diagnostic, owner string, own bool) {
	pb := s.pubs[uri]
	if pb == nil {
		pb = &published{owners: map[string]bool{}}
		s.pubs[uri] = pb
	}
	if own {
		pb.owners[owner] = true
	} else {
		delete(pb.owners, owner)
	}
	pb.kern = ds
	pb.lsp = []Diagnostic{}
	if len(ds) > 0 {
		text := s.textOf(path)
		for _, k := range ds {
			pb.lsp = append(pb.lsp, s.convert(uri, text, k))
		}
	}
	params := PublishDiagnosticsParams{URI: uri, Diagnostics: pb.lsp}
	if doc := s.docs[uri]; doc != nil {
		v := doc.version
		params.Version = &v
	}
	s.notify("textDocument/publishDiagnostics", params)
}

// convert is one kern diagnostic as an LSP diagnostic (spec R3).
func (s *Server) convert(uri string, text []byte, k diag.Diagnostic) Diagnostic {
	sev := 1
	if k.Severity == "warning" {
		sev = 2
	}
	msg := k.Message
	if msg == "" {
		msg = string(k.Code)
	}
	if k.Rule != "" {
		msg += " (rule " + k.Rule + ")"
	}
	out := Diagnostic{Range: s.rangeOf(text, k.Span.Start, k.Span.End), Severity: sev, Code: string(k.Code), Source: "kern", Message: msg}
	for _, r := range k.Related {
		out.RelatedInformation = append(out.RelatedInformation, DiagnosticRelatedInformation{
			Location: Location{URI: uri, Range: s.rangeOf(text, r.Start, r.End)},
			Message:  r.Label,
		})
	}
	return out
}

func (s *Server) rangeOf(text []byte, start, end int) Range {
	return Range{Start: ToPosition(text, start, s.enc), End: ToPosition(text, end, s.enc)}
}

// close drops a document and clears the URIs only it had published
// (spec R4).
func (s *Server) close(uri string) {
	delete(s.docs, uri)
	delete(s.stale, uri)
	var pubs []string
	for u := range s.docPubs[uri] {
		pubs = append(pubs, u)
	}
	sort.Strings(pubs)
	delete(s.docPubs, uri)
	s.release(uri, pubs)
}

// release removes owner from the owners of each URI, in order, and
// publishes an empty array on each that no document owns any more.
func (s *Server) release(owner string, uris []string) {
	for _, u := range uris {
		pb := s.pubs[u]
		if pb == nil {
			continue
		}
		delete(pb.owners, owner)
		if len(pb.owners) == 0 {
			s.publishURI(u, "", nil, owner, false)
			delete(s.pubs, u) // nothing is published there any more
		}
	}
}
