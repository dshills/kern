package lsp

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/source"
)

// The benchmarks drive one request at a time on a server that is not
// running its loop: the document is open, nothing is written anywhere.
// Bytes and allocations per request are the numbers to read; they do not
// depend on the host's load.

// benchDoc is a document for the benchmarks: its text, the position of a
// local variable's declaration to hover, and where the file is.
type benchDoc struct {
	name  string
	path  string
	text  []byte
	hover Position
}

// benchPosition is the position of an offset that only ASCII precedes on
// its line, in a text whose lines end in \n.
func benchPosition(text []byte, off int) Position {
	return Position{Line: bytes.Count(text[:off], []byte{'\n'}), Character: off - 1 - bytes.LastIndexByte(text[:off], '\n')}
}

// kvmDoc is examples/kvm/kvm.kern, hovered at main's `args`.
func kvmDoc(b *testing.B) benchDoc {
	b.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "examples", "kvm", "kvm.kern"))
	if err != nil {
		b.Fatal(err)
	}
	text, err := os.ReadFile(path)
	if err != nil {
		b.Fatal(err)
	}
	off := strings.Index(string(text), "    args := io.args()")
	if off < 0 {
		b.Fatal("kvm.kern has no `args := io.args()`")
	}
	return benchDoc{name: "kvm", path: path, text: text, hover: benchPosition(text, off+4)}
}

// bigSource is a 16,001-line program: 4,000 four-line functions (one in
// eight under a comment with characters of two, three and four bytes) and
// a main whose local is hovered.
func bigSource() string {
	var sb strings.Builder
	sb.WriteString("module big\n")
	lines := 1
	for i := 0; lines < 15990; i++ {
		if i%8 == 0 {
			fmt.Fprintf(&sb, "// f%d: é, € and 😀 before the code\n", i)
			lines++
		}
		fmt.Fprintf(&sb, "func f%d(a: u64, b: u64) -> u64 {\n    c := a + b\n    return c\n}\n", i)
		lines += 4
	}
	for ; lines < 15996; lines++ {
		sb.WriteString("\n")
	}
	sb.WriteString("func main() -> i32 {\n    total := f0(1u64, 2u64)\n    if total == 0u64 { return 1i32 }\n    return 0i32\n}\n")
	return sb.String()
}

func bigDoc(b *testing.B) benchDoc {
	b.Helper()
	dir, err := filepath.EvalSymlinks(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	src := bigSource()
	if n := strings.Count(src, "\n"); n != 16001 {
		b.Fatalf("the synthetic file has %d lines", n)
	}
	path := filepath.Join(dir, "big.kern")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		b.Fatal(err)
	}
	off := strings.Index(src, "    total := f0(")
	return benchDoc{name: "16000-lines", path: path, text: []byte(src), hover: benchPosition([]byte(src), off+4)}
}

// benchServer is a running server holding doc, writing nowhere.
func benchServer(doc benchDoc, opts Options) (*Server, *document) {
	opts.Log = io.Discard
	s := New(strings.NewReader(""), io.Discard, opts)
	s.state, s.enc = stateRunning, UTF16
	uri := URIOf(doc.path)
	d := &document{uri: uri, path: doc.path, text: doc.text, version: 1, seq: 1}
	s.docs[uri] = d
	return s, d
}

func benchDocs(b *testing.B) []benchDoc {
	return []benchDoc{kvmDoc(b), bigDoc(b)}
}

// BenchmarkDocumentSymbol is one textDocument/documentSymbol.
func BenchmarkDocumentSymbol(b *testing.B) {
	for _, doc := range benchDocs(b) {
		b.Run(doc.name, func(b *testing.B) {
			s, d := benchServer(doc, Options{})
			p := documentSymbolParams{TextDocument: textDocumentIdentifier{URI: d.uri}}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				res, rerr := s.documentSymbol(p)
				if rerr != nil || len(res.([]DocumentSymbol)) == 0 {
					b.Fatalf("documentSymbol: %v, %d symbols", rerr, len(res.([]DocumentSymbol)))
				}
			}
		})
	}
}

// BenchmarkHover is one textDocument/hover on a local variable, whose
// answer carries the ownership lines.
func BenchmarkHover(b *testing.B) {
	for _, doc := range benchDocs(b) {
		b.Run(doc.name, func(b *testing.B) {
			s, d := benchServer(doc, Options{})
			p := textDocumentPositionParams{TextDocument: textDocumentIdentifier{URI: d.uri}, Position: doc.hover}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				res, rerr := s.hover(p)
				if rerr != nil || res == nil {
					b.Fatalf("hover: %v %v", res, rerr)
				}
			}
		})
	}
}

// BenchmarkReferences is one textDocument/references on kvm.kern's
// `faulty`, a function with some twenty callers.
func BenchmarkReferences(b *testing.B) {
	doc := kvmDoc(b)
	off := strings.Index(string(doc.text), "func faulty(")
	if off < 0 {
		b.Fatal("kvm.kern has no `func faulty(`")
	}
	s, d := benchServer(doc, Options{})
	p := referenceParams{TextDocument: textDocumentIdentifier{URI: d.uri}, Position: benchPosition(doc.text, off+len("func "))}
	p.Context.IncludeDeclaration = true
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, rerr := s.references(p)
		if rerr != nil || len(res.([]Location)) < 20 {
			b.Fatalf("references: %v, %d locations", rerr, len(res.([]Location)))
		}
	}
}

// BenchmarkCheckDoc is one diagnostics pass over the document's program,
// uncached: with what the program has (nothing, for these two), and with
// a hundred diagnostics spread over the file, each with one related span,
// in place of the check.
func BenchmarkCheckDoc(b *testing.B) {
	for _, doc := range benchDocs(b) {
		b.Run(doc.name+"/clean", func(b *testing.B) {
			s, d := benchServer(doc, Options{})
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s.checkDoc(d)
			}
			b.StopTimer()
			if pb := s.pubs[d.uri]; pb == nil || len(pb.lsp) != 0 {
				b.Fatalf("the check published %+v", pb)
			}
		})
		b.Run(doc.name+"/100-diagnostics", func(b *testing.B) {
			var ds []diag.Diagnostic
			for i := 0; i < 100; i++ {
				at := (i*len(doc.text) + len(doc.text)/2) / 100
				k := diag.New("INTERNAL", doc.path, diag.Span{Start: at, End: at + 3}, "diagnostic %d", i)
				k.Related = []diag.Related{{Label: "related", Start: at / 2, End: at/2 + 3}}
				ds = append(ds, k)
			}
			s, d := benchServer(doc, Options{Check: func([]*source.File) ([]diag.Diagnostic, error) { return ds, nil }})
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s.checkDoc(d)
			}
			b.StopTimer()
			if pb := s.pubs[d.uri]; pb == nil || len(pb.lsp) != 100 {
				b.Fatalf("the check published %+v", pb)
			}
		})
	}
}
