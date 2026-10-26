// Package edit applies §16 structured edits: rename expansion, conflict and
// coordinate rules, and all-or-nothing application (PLAN.md 4.1).
package edit

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// expanded is one replace/insert/delete attributed to a request op index.
type expanded struct {
	op    diag.StructuredEditOp
	index int
	seq   int // request order among inserts at the same offset
}

// Failure names the offending op by index with an EDIT_* code.
type Failure struct {
	Code    diag.Code
	Index   int
	Message string
}

// Apply returns the new text of every changed file, or the single failure
// of lowest op index (EDIT_INVALID_SPAN takes precedence over EDIT_OVERLAP
// on the same op). srcs are the unedited request files.
func Apply(srcs []*source.File, ops []diag.StructuredEditOp) (map[string][]byte, *Failure) {
	files := map[string][]byte{}
	for _, f := range srcs {
		files[f.Name] = f.Bytes
	}
	// (1) rename resolution and expansion against the unedited files
	var exp []expanded
	var prog *resolve.Program
	var failure *Failure
	report := func(code diag.Code, index int, msg string) {
		if failure == nil || index < failure.Index || (index == failure.Index && code == "EDIT_INVALID_SPAN" && failure.Code != "EDIT_INVALID_SPAN") {
			failure = &Failure{Code: code, Index: index, Message: msg}
		}
	}
	for idx, op := range ops {
		if op.Op != diag.OpRenameSymbol {
			exp = append(exp, expanded{op: op, index: idx, seq: idx})
			continue
		}
		if prog == nil {
			prog = resolve.Resolve(srcs)
		}
		if _, ok := files[op.File]; !ok || op.Span == nil {
			report("EDIT_UNRESOLVED_SYMBOL", idx, "rename_symbol span does not name a request file")
			continue
		}
		spans, ok := renameSpans(prog, srcs, op.File, *op.Span)
		if !ok {
			report("EDIT_UNRESOLVED_SYMBOL", idx, "rename_symbol span is not a resolvable declaration")
			continue
		}
		for _, s := range spans {
			sp := s.span
			exp = append(exp, expanded{op: diag.StructuredEditOp{Op: diag.OpReplaceSpan, File: s.file, Span: &sp, Text: op.NewName}, index: idx, seq: idx})
		}
	}
	// (5) coordinate validation and (3) conflicts: the failing op of lowest
	// index is reported; when one op is both invalid and conflicting,
	// EDIT_INVALID_SPAN takes precedence (spec §13 accepted gap 6)
	sort.SliceStable(exp, func(i, j int) bool {
		if exp[i].index != exp[j].index {
			return exp[i].index < exp[j].index
		}
		return exp[i].seq < exp[j].seq
	})
	bad := map[int]bool{}
	for i, x := range exp {
		if msg := invalid(files, x.op); msg != "" {
			bad[i] = true
			report("EDIT_INVALID_SPAN", x.index, msg)
		}
	}
	for i := range exp {
		for j := i + 1; j < len(exp); j++ {
			a, b := exp[i], exp[j]
			if bad[i] || bad[j] || a.op.File != b.op.File || !conflicts(a.op, b.op) {
				continue
			}
			lo := a.index
			if b.index < lo {
				lo = b.index
			}
			report("EDIT_OVERLAP", lo, "structured-edit ops overlap")
		}
	}
	if failure != nil {
		return nil, failure
	}
	// (4) application per file
	out := map[string][]byte{}
	perFile := map[string][]expanded{}
	for _, x := range exp {
		perFile[x.op.File] = append(perFile[x.op.File], x)
	}
	for name, xs := range perFile {
		out[name] = applyFile(files[name], xs)
	}
	return out, nil
}

func invalid(files map[string][]byte, op diag.StructuredEditOp) string {
	src, ok := files[op.File]
	if !ok {
		return "op names a file that is not in the request"
	}
	boundary := func(k int) bool { return k == 0 || k == len(src) || utf8.RuneStart(src[k]) }
	switch op.Op {
	case diag.OpInsert:
		if op.At == nil || *op.At < 0 || *op.At > len(src) || !boundary(*op.At) {
			return "insert offset is invalid"
		}
	case diag.OpReplaceSpan, diag.OpDeleteSpan:
		if op.Span == nil || op.Span.Start < 0 || op.Span.Start > op.Span.End || op.Span.End > len(src) || op.Span.Start == op.Span.End || !boundary(op.Span.Start) || !boundary(op.Span.End) {
			return "span is invalid"
		}
	default:
		return "unknown op"
	}
	return ""
}

func conflicts(a, b diag.StructuredEditOp) bool {
	ai, bi := a.Op == diag.OpInsert, b.Op == diag.OpInsert
	switch {
	case ai && bi:
		return false
	case ai:
		return b.Span.Start < *a.At && *a.At < b.Span.End
	case bi:
		return a.Span.Start < *b.At && *b.At < a.Span.End
	}
	return a.Span.Start < b.Span.End && b.Span.Start < a.Span.End
}

// applyFile builds the new text: replacements in span order, inserts at
// offset k before the original byte at k (so before a replacement starting
// at k and after one ending at k), several inserts at k in request order.
func applyFile(src []byte, xs []expanded) []byte {
	type piece struct {
		at, end int // original range consumed ([at, end), empty for inserts)
		text    string
		insert  bool
		seq     int
	}
	var ps []piece
	for _, x := range xs {
		if x.op.Op == diag.OpInsert {
			ps = append(ps, piece{at: *x.op.At, end: *x.op.At, text: x.op.Text, insert: true, seq: x.seq})
		} else {
			ps = append(ps, piece{at: x.op.Span.Start, end: x.op.Span.End, text: x.op.Text, seq: x.seq})
		}
	}
	sort.SliceStable(ps, func(i, j int) bool {
		a, b := ps[i], ps[j]
		if a.at != b.at {
			return a.at < b.at
		}
		if a.insert != b.insert {
			// at the same offset an insert precedes a replacement starting there;
			// a replacement ending at k precedes an insert at k by its lower start
			return a.insert
		}
		return a.seq < b.seq
	})
	var out []byte
	cur := 0
	for _, p := range ps {
		out = append(out, src[cur:p.at]...)
		out = append(out, p.text...)
		cur = p.end
	}
	return append(out, src[cur:]...)
}

type located struct {
	file string
	span diag.Span
}

// renameSpans finds the declaration whose name occupies span in file and
// every resolved use of it across the request files. Supported
// declarations: locals, parameters and bindings (uses through Idents),
// functions (uses through Idents), struct and union types (uses through
// type names and struct literals).
func renameSpans(prog *resolve.Program, srcs []*source.File, file string, span diag.Span) ([]located, bool) {
	var src *source.File
	for _, f := range srcs {
		if f.Name == file {
			src = f
		}
	}
	if src == nil || span.Start < 0 || span.End > len(src.Bytes) || span.Start >= span.End {
		return nil, false
	}
	name := string(src.Bytes[span.Start:span.End])
	info := prog.Info
	// a local, parameter or binding: a symbol declared at a position whose name text is span
	for node, sym := range info.Decls {
		if sym == nil || sym.Name != name || sym.File != file && sym.File != "" {
			continue
		}
		if !nameAt(src.Bytes, declPos(node, sym), name, span) {
			continue
		}
		spans := []located{{file, span}}
		for id, s := range info.Idents {
			if s == sym {
				spans = append(spans, located{sym.File, diag.Span{Start: id.Start, End: id.End}})
			}
		}
		return dedupe(spans), true
	}
	// a function of this file's module
	for _, m := range prog.Modules {
		for _, f := range m.Files {
			if f.Name != file {
				continue
			}
			for _, d := range f.Decls {
				switch d := d.(type) {
				case *ast.FuncDecl:
					if d.Name == name && nameAt(src.Bytes, d.Pos, name, span) {
						fd := info.Funcs[d]
						spans := []located{{file, span}}
						for id, s := range info.Idents {
							if s.Kind == resolve.SymFunc && s.Func == fd {
								spans = append(spans, located{fileOfIdent(prog, id), diag.Span{Start: id.Start, End: id.End}})
							}
						}
						return dedupe(spans), true
					}
				case *ast.StructDecl:
					if d.Name == name && nameAt(src.Bytes, d.Pos, name, span) {
						return dedupe(append([]located{{file, span}}, typeUses(prog, srcs, m, name)...)), true
					}
				case *ast.UnionDecl:
					if d.Name == name && nameAt(src.Bytes, d.Pos, name, span) {
						return dedupe(append([]located{{file, span}}, typeUses(prog, srcs, m, name)...)), true
					}
				}
			}
		}
	}
	return nil, false
}

func declPos(node any, sym *resolve.Symbol) ast.Pos {
	switch n := node.(type) {
	case *ast.Param:
		return n.Pos
	case *ast.ShortDecl:
		return n.Pos
	case *ast.VarDecl:
		return n.Pos
	case *ast.Pos:
		return *n
	}
	return sym.Pos
}

// nameAt reports whether the declaration starting at pos has its name text
// exactly at span (the first occurrence of name from pos).
func nameAt(src []byte, pos ast.Pos, name string, span diag.Span) bool {
	if span.End-span.Start != len(name) {
		return false
	}
	if pos.Start < 0 || pos.Start > len(src) {
		return false
	}
	isIdent := func(c byte) bool {
		return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
	}
	for k := pos.Start; k+len(name) <= len(src) && k <= pos.Start+256; k++ {
		if string(src[k:k+len(name)]) != name {
			continue
		}
		// the name token: not part of a longer identifier
		if (k > 0 && isIdent(src[k-1])) || (k+len(name) < len(src) && isIdent(src[k+len(name)])) {
			continue
		}
		return k == span.Start
	}
	return false
}

func fileOfIdent(prog *resolve.Program, id *ast.Ident) string {
	for _, m := range prog.Modules {
		for _, f := range m.Files {
			if containsNode(f, id) {
				return f.Name
			}
		}
	}
	return ""
}

// containsNode reports whether the identifier lies within the file's byte
// range (every file's node positions are file-relative, so the module's
// files are distinguished by scanning their declarations).
func containsNode(f *ast.File, id *ast.Ident) bool {
	for _, d := range f.Decls {
		if ast.Walk(d, func(n any) bool { return n == id }) {
			return true
		}
	}
	return false
}

// typeUses finds every type name referring to m's type: unqualified in m's
// own files, qualified (`alias.Name`) in every module importing m; only the
// identifier is replaced.
func typeUses(prog *resolve.Program, srcs []*source.File, m *resolve.Module, name string) []located {
	bytesOf := map[string][]byte{}
	for _, s := range srcs {
		bytesOf[s.Name] = s.Bytes
	}
	var out []located
	for _, mod := range prog.Modules {
		alias := ""
		if mod != m {
			for a, imp := range mod.Imports {
				if imp == m {
					alias = a
				}
			}
			if alias == "" {
				continue
			}
		}
		for _, f := range mod.Files {
			for _, d := range f.Decls {
				ast.Walk(d, func(n any) bool {
					x, ok := n.(*ast.TypeName)
					if !ok || x.Name != name {
						return false
					}
					switch {
					case mod == m && x.Pkg == "":
						out = append(out, located{f.Name, diag.Span{Start: x.Start, End: x.Start + len(name)}})
					case mod != m && x.Pkg == alias:
						// the identifier after the qualifier within the node's text
						src := bytesOf[f.Name]
						if x.Start >= 0 && x.End <= len(src) {
							rel := strings.LastIndex(string(src[x.Start:x.End]), name)
							if rel >= 0 {
								out = append(out, located{f.Name, diag.Span{Start: x.Start + rel, End: x.Start + rel + len(name)}})
							}
						}
					}
					return false
				})
			}
		}
	}
	return out
}

func dedupe(xs []located) []located {
	seen := map[located]bool{}
	var out []located
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].file != out[j].file {
			return out[i].file < out[j].file
		}
		return out[i].span.Start < out[j].span.Start
	})
	return out
}
