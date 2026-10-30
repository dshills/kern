// Package testrun implements `kern test` (specs/test-command/SPEC.md):
// it finds the test functions of a program's test files, runs each in the
// interpreter and natively in both opt levels, and compares the runs.
package testrun

import (
	"bytes"
	"sort"
	"strings"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/project"
	"github.com/dshills/kern/compiler/resolve"
	"github.com/dshills/kern/compiler/source"
)

// TrapKinds are the trap kinds of spec §6, the kinds a directive may name.
var TrapKinds = []string{"bounds", "overflow", "div_zero", "shift", "conversion", "generation", "vacant", "identity", "disjoint", "oom", "stack", "explicit", "unreachable"}

// Expect is a test's expectation (§2.3): a return, or a trap of a kind.
type Expect struct {
	Outcome string `json:"outcome"`        // "return" or "trap"
	Kind    string `json:"kind,omitempty"` // the trap kind, for "trap"
}

// Test is one test function.
type Test struct {
	Name   string // qualified: <module>.<function>
	Decl   *ast.FuncDecl
	File   string    // the test file's display name
	Span   diag.Span // the declaration's span in File
	Expect Expect
}

// Find returns the test functions of the program's test files, in the
// order of §2.2 (file name, then position), and the TEST_SIGNATURE and
// TEST_DIRECTIVE diagnostics of R3 and R4. srcs supply the bytes the
// directive lines are read from.
func Find(prog *resolve.Program, srcs []*source.File) ([]Test, []diag.Diagnostic) {
	bytesOf := map[string][]byte{}
	for _, f := range srcs {
		bytesOf[f.Name] = f.Bytes
	}
	var files []*ast.File
	for _, m := range prog.Modules {
		if m.Std {
			continue
		}
		for _, f := range m.Files {
			if project.TestFile(f.Name) {
				files = append(files, f)
			}
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	var tests []Test
	var ds []diag.Diagnostic
	for _, f := range files {
		src := bytesOf[f.Name]
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			isTest := strings.HasPrefix(fd.Name, "test_")
			span := diag.Span{Start: fd.Start, End: fd.End}
			exp, dirs, dds := directive(f.Name, src, fd.Start)
			if !isTest {
				for _, at := range dirs {
					ds = append(ds, diag.New("TEST_DIRECTIVE", f.Name, at, "an expect directive is only allowed on a test function, and %s is not one (its name does not begin with test_)", fd.Name))
				}
				continue
			}
			ds = append(ds, dds...)
			if why := badSignature(fd); why != "" {
				ds = append(ds, diag.New("TEST_SIGNATURE", f.Name, span, "test function %s %s; a test function takes no parameters, has no type parameters and no results, and has a body", fd.Name, why))
				continue
			}
			tests = append(tests, Test{Name: f.Module + "." + fd.Name, Decl: fd, File: f.Name, Span: span, Expect: exp})
		}
	}
	diag.Sort(ds)
	return tests, ds
}

func badSignature(fd *ast.FuncDecl) string {
	switch {
	case fd.Extern != "":
		return "is extern"
	case fd.Body == nil:
		return "has no body"
	case len(fd.TypeParams) > 0:
		return "has type parameters"
	case len(fd.Params) > 0:
		return "has parameters"
	case len(fd.Ret) > 0:
		return "has results"
	}
	return ""
}

// directive reads the expectation of the function declared at offset at:
// among the comment lines directly above its line (contiguous, no blank
// line), the one beginning with `//! expect:` (§2.3). It returns the
// expectation (return when there is no directive), the spans of every
// directive line, and the diagnostics of a malformed or repeated one.
func directive(file string, src []byte, at int) (Expect, []diag.Span, []diag.Diagnostic) {
	exp := Expect{Outcome: "return"}
	if at > len(src) {
		return exp, nil, nil
	}
	lineStart := bytes.LastIndexByte(src[:at], '\n') + 1
	if len(bytes.TrimSpace(src[lineStart:at])) > 0 {
		return exp, nil, nil // the declaration does not begin its line
	}
	var spans []diag.Span
	var texts []string
	for end := lineStart - 1; end >= 0; {
		start := bytes.LastIndexByte(src[:end], '\n') + 1
		line := strings.TrimRight(string(src[start:end]), "\r")
		trimmed := strings.TrimLeft(line, " \t")
		if !strings.HasPrefix(trimmed, "//") {
			break
		}
		if strings.HasPrefix(trimmed, "//! expect:") {
			s := start + len(line) - len(trimmed)
			spans = append([]diag.Span{{Start: s, End: start + len(line)}}, spans...)
			texts = append([]string{strings.TrimSpace(strings.TrimPrefix(trimmed, "//! expect:"))}, texts...)
		}
		end = start - 1
	}
	var ds []diag.Diagnostic
	if len(spans) > 1 {
		for _, s := range spans[1:] {
			ds = append(ds, diag.New("TEST_DIRECTIVE", file, s, "a test function has at most one expect directive"))
		}
	}
	if len(spans) > 0 {
		e, ok := parseExpect(texts[0])
		if !ok {
			ds = append(ds, diag.New("TEST_DIRECTIVE", file, spans[0], "expect directive %q is neither `return` nor `trap <kind>` with a kind of spec §6 (%s)", texts[0], strings.Join(TrapKinds, ", ")))
		} else {
			exp = e
		}
	}
	return exp, spans, ds
}

func parseExpect(text string) (Expect, bool) {
	fields := strings.Fields(text)
	switch {
	case len(fields) == 1 && fields[0] == "return":
		return Expect{Outcome: "return"}, true
	case len(fields) == 2 && fields[0] == "trap":
		for _, k := range TrapKinds {
			if fields[1] == k {
				return Expect{Outcome: "trap", Kind: k}, true
			}
		}
	}
	return Expect{}, false
}
