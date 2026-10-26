// Package iface serializes a module's interface file: everything checking
// or analysis consumes from an import (PLAN.md 4.1).
package iface

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/layout"
	"github.com/dshills/kern/compiler/resolve"
)

// IfaceVersion is the interface-file format version; it is part of every
// incremental cache key (PLAN.md 4.1).
const Version = "1"

// Interface is the canonical serialization of everything checking or
// analysis consumes from an imported module: exported type definitions with
// layout and derived markers, constants, function signatures with
// conventions and contract blocks, generic bodies in full (they are
// specialized at the use site), thread-locals, interfaces, and each
// exported function's effect summary. src maps file names to their bytes
// (generic bodies and constant values are carried as source text).
func Interface(m *resolve.Module, src map[string][]byte) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "iface %s module %s\n", Version, m.Name)
	text := func(file string, p ast.Pos) string {
		s := src[file]
		if p.Start < 0 || p.End > len(s) || p.Start > p.End {
			return ""
		}
		return string(s[p.Start:p.End])
	}
	// the defining file of every declaration node, by identity (methods of
	// different impls share names)
	fileOfNode := map[any]string{}
	for _, f := range m.Files {
		for _, d := range f.Decls {
			fileOfNode[d] = f.Name
			if impl, ok := d.(*ast.ImplDecl); ok {
				for _, fn := range impl.Funcs {
					fileOfNode[fn] = f.Name
				}
			}
		}
	}
	fileOf := func(node any) string { return fileOfNode[node] }
	var names []string
	for n := range m.Types {
		if resolve.IsExported(n) || m.Name == "core" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		td := m.Types[n]
		fmt.Fprintf(&b, "type %s kind=%d nocopy=%v packed=%v view=%v dtor=%v asserts=%v/%v/%v", n, td.Kind, td.NoCopy, td.Packed, td.View, td.Destructor != nil, td.AssertSend, td.AssertShare, td.AssertInterior)
		for _, tp := range td.TypeParams {
			fmt.Fprintf(&b, " [%s]", tp.Name)
		}
		b.WriteString("\n")
		for _, f := range td.Fields {
			fmt.Fprintf(&b, "  field %s: %s\n", f.Name, f.Type)
		}
		for _, v := range td.Variants {
			fmt.Fprintf(&b, "  variant %s", v.Name)
			for _, f := range v.Fields {
				fmt.Fprintf(&b, " %s", f.Type)
			}
			b.WriteString("\n")
		}
		if len(td.TypeParams) == 0 && (td.Kind == resolve.KindStruct || td.Kind == resolve.KindUnion) {
			nt := &resolve.Named{Decl: td}
			li := layout.Of(nt)
			fmt.Fprintf(&b, "  layout size=%d align=%d copy=%v send=%v share=%v interior=%v zeroable=%v\n", li.Size, li.Align, resolve.IsCopy(nt), resolve.IsSend(nt), resolve.IsShare(nt), resolve.IsInterior(nt), resolve.IsZeroable(nt))
		} else if len(td.TypeParams) > 0 {
			// a generic type is specialized at the use site: its whole text matters
			if f := fileOf(td.AST); f != "" {
				fmt.Fprintf(&b, "  body %q\n", text(f, td.Pos))
			}
		}
		var ms []string
		for mn := range td.Methods {
			ms = append(ms, mn)
		}
		sort.Strings(ms)
		for _, mn := range ms {
			writeFunc(&b, "  method", td.Methods[mn], text, fileOf)
		}
	}
	names = names[:0]
	for n := range m.Ifaces {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		id := m.Ifaces[n]
		fmt.Fprintf(&b, "interface %s\n", n)
		var ms []string
		for mn := range id.Methods {
			ms = append(ms, mn)
		}
		sort.Strings(ms)
		for _, mn := range ms {
			writeFunc(&b, "  method", id.Methods[mn], text, fileOf)
		}
	}
	names = names[:0]
	for n := range m.Consts {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		k := m.Consts[n]
		kf := ""
		for _, f := range m.Files {
			for _, d := range f.Decls {
				if cd, ok := d.(*ast.ConstDecl); ok && cd.Value == k.Value {
					kf = f.Name
				}
			}
		}
		fmt.Fprintf(&b, "const %s: %s = %q\n", n, k.Type, text(kf, k.Value.Span()))
	}
	names = names[:0]
	for n := range m.Globals {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		g := m.Globals[n]
		fmt.Fprintf(&b, "thread_local %s: %s = %v\n", n, g.Type, g.Init)
	}
	names = names[:0]
	for n := range m.Funcs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		writeFunc(&b, "func", m.Funcs[n], text, fileOf)
	}
	return []byte(b.String())
}

func writeFunc(b *strings.Builder, kind string, fd *resolve.FuncDecl, text func(string, ast.Pos) string, fileOf func(any) string) {
	fmt.Fprintf(b, "%s %s", kind, fd.Name)
	for _, tp := range fd.TypeParams {
		fmt.Fprintf(b, " [%s", tp.Name)
		for _, c := range tp.Constraints {
			fmt.Fprintf(b, ":%+v", c)
		}
		b.WriteString("]")
	}
	b.WriteString(" (")
	for i, p := range fd.Params {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "%v %s: %s", p.Conv, p.Name, p.Type)
	}
	b.WriteString(") ->")
	for _, r := range fd.Ret {
		fmt.Fprintf(b, " %s", r)
	}
	if fd.Release != "" {
		fmt.Fprintf(b, " release %s", fd.Release)
	}
	if fd.Extern != "" {
		fmt.Fprintf(b, " extern %q", fd.Extern)
	}
	if fd.Contract != nil {
		fmt.Fprintf(b, " contract %q/%q/%q", fd.Contract.Invariant, fd.Contract.Requires, fd.Contract.Ensures)
	}
	if fd.AST != nil && fd.AST.Body != nil {
		fmt.Fprintf(b, " effects=%s", effectSummary(fd.AST.Body))
		if len(fd.TypeParams) > 0 || len(fd.OwnerParams) > 0 {
			// generic bodies are specialized by importers
			if f := fileOf(fd.AST); f != "" {
				fmt.Fprintf(b, " body %q", text(f, fd.AST.Pos))
			}
		}
	}
	b.WriteString("\n")
}

// effectSummary is a syntactic over-approximation of a body's effects
// (allocates, traps, spawns, unsafe) for the cache key: any call makes
// every effect possible, so a transitive effect can never be lost; the
// exact summary is the show-effects analysis over the lowered program.
func effectSummary(body *ast.Block) string {
	alloc, trap, spawn, unsafe := false, false, false, false
	ast.Walk(body, func(n any) bool {
		switch x := n.(type) {
		case *ast.Unsafe:
			unsafe = true
		case *ast.Binary:
			trap = true
		case *ast.IndexExpr, *ast.SliceExpr, *ast.TrapExpr:
			trap = true
		case *ast.CallExpr:
			// any call may carry every effect transitively
			alloc, trap, spawn, unsafe = true, true, true, true
			_ = x
		}
		return false
	})
	return fmt.Sprintf("a=%v,t=%v,s=%v,u=%v", alloc, trap, spawn, unsafe)
}
