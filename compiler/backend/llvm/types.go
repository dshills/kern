// Package llvm emits textual LLVM IR for the semantic IR (PLAN.md 2.1). The
// output is compiled and linked with the hosted C runtime by clang.
package llvm

import (
	"fmt"
	"strings"

	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/layout"
	"github.com/dshills/kern/compiler/resolve"
)

// emitter holds module-level state.
type emitter struct {
	mod      *ir.Module
	opts     Options
	sb       strings.Builder // function bodies
	decls    strings.Builder // type and global declarations
	types    map[string]bool // emitted named types
	strs     []string        // string literal globals
	glue     map[string]bool // emitted drop glue
	eqs      map[string]bool // emitted equality glue
	glueBody strings.Builder
	runtime  map[string]bool // declared runtime functions
	errs     []error
	thunks   int
	dbg      *debugInfo // DWARF metadata, nil without debug info
}

// lty maps a type to its LLVM type in registers (bool is i1).
func (e *emitter) lty(t resolve.Type) string {
	switch x := t.(type) {
	case *resolve.Prim:
		switch x.Name {
		case "bool":
			return "i1"
		case "f32":
			return "float"
		case "f64":
			return "double"
		case "i8", "u8":
			return "i8"
		case "i16", "u16":
			return "i16"
		case "i32", "u32":
			return "i32"
		}
		return "i64"
	case *resolve.Unit:
		return "{}"
	case *resolve.RawPtr, *resolve.Func:
		return "ptr"
	case *resolve.Access:
		if seq, _ := accessInfo(x); seq {
			return "{ ptr, i64 }"
		}
		return "ptr"
	case *resolve.Slice:
		return "{ ptr, i64 }"
	case *resolve.Array:
		e.sizeCheck(t)
		return fmt.Sprintf("[%d x %s]", x.Len, e.mty(x.Elem))
	case *resolve.Named:
		e.sizeCheck(t)
		return e.namedType(x)
	case *resolve.Tuple:
		parts := make([]string, len(x.Elems))
		for i, el := range x.Elems {
			parts[i] = e.lty(el)
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	case *resolve.Bad:
		return "{}"
	}
	return "{}"
}

// sizeCheck reports a type whose layout saturated at layout.MaxSize.
func (e *emitter) sizeCheck(t resolve.Type) {
	if layout.Of(t).Size >= layout.MaxSize {
		e.errs = append(e.errs, fmt.Errorf("type %s is too large for the target", t))
	}
}

// mty maps a type to its LLVM type in memory (bool is i8).
func (e *emitter) mty(t resolve.Type) string {
	if resolve.IsBool(t) {
		return "i8"
	}
	return e.lty(t)
}

// namedType returns the LLVM identified struct for a named type, emitting
// its definition once. The runtime-provided library objects are opaque pointers.
func (e *emitter) namedType(n *resolve.Named) string {
	if layout.Opaque(n) {
		return "ptr"
	}
	name := "%" + quote("T."+n.String())
	if e.types[name] {
		return name
	}
	e.types[name] = true
	args := typeArgs(n)
	switch n.Decl.Kind {
	case resolve.KindStruct:
		var fields []string
		for _, f := range n.Decl.Fields {
			fields = append(fields, e.mty(resolve.Subst(f.Type, args)))
		}
		if len(fields) == 0 {
			fields = []string{"i8"}
		}
		if n.Decl.Packed {
			fmt.Fprintf(&e.decls, "%s = type <{ %s }>\n", name, strings.Join(fields, ", "))
		} else {
			fmt.Fprintf(&e.decls, "%s = type { %s }\n", name, strings.Join(fields, ", "))
		}
	case resolve.KindUnion:
		li := layout.Of(n)
		tag := "i8"
		if li.TagSize == 2 {
			tag = "i16"
		}
		// tag, padding to the payload, then the payload as bytes grouped by alignment
		pad := li.Payload - li.TagSize
		pay := li.Size - li.Payload
		parts := []string{tag}
		if pad > 0 {
			parts = append(parts, fmt.Sprintf("[%d x i8]", pad))
		}
		if pay > 0 {
			unit := li.Align * 8
			if li.Align > 8 {
				unit = 64
			}
			n := pay / (unit / 8)
			parts = append(parts, fmt.Sprintf("[%d x i%d]", n, unit))
		}
		fmt.Fprintf(&e.decls, "%s = type { %s }\n", name, strings.Join(parts, ", "))
	}
	return name
}

func typeArgs(n *resolve.Named) map[string]resolve.Type {
	m := map[string]resolve.Type{}
	for i, tp := range n.Decl.TypeParams {
		if i < len(n.Args) {
			m[tp.Name] = n.Args[i]
		}
	}
	return m
}

// quote renders an identifier usable after @ or % in textual LLVM IR.
func quote(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `\22`) + `"`
}

func fnSym(name string) string { return "@" + quote(name) }

// needsDrop reports whether destroying a value of type t does anything.
func needsDrop(t resolve.Type) bool {
	return !resolve.IsCopy(t) && !isUnit(t)
}

func isUnit(t resolve.Type) bool {
	_, ok := t.(*resolve.Unit)
	return ok
}

// glueFor returns the symbol of the drop glue function `void(ptr)` for t,
// emitting it once, or "null" when t needs no destruction.
func (e *emitter) glueFor(t resolve.Type) string {
	if !needsDrop(t) {
		return "null"
	}
	name := "drop." + t.String()
	sym := fnSym(name)
	if e.glue[name] {
		return sym
	}
	e.glue[name] = true
	var b strings.Builder
	fmt.Fprintf(&b, "define internal void %s(ptr %%p) {\nentry:\n", sym)
	switch x := t.(type) {
	case *resolve.Array:
		eg := e.glueFor(x.Elem)
		if eg != "null" {
			// elements in reverse order
			fmt.Fprintf(&b, "  br label %%loop\nloop:\n  %%i = phi i64 [ %d, %%entry ], [ %%next, %%body ]\n  %%done = icmp eq i64 %%i, 0\n  br i1 %%done, label %%exit, label %%body\nbody:\n  %%next = sub i64 %%i, 1\n  %%ep = getelementptr %s, ptr %%p, i64 %%next\n  call void %s(ptr %%ep)\n  br label %%loop\nexit:\n", x.Len, e.mty(x.Elem), eg)
		}
	case *resolve.Named:
		e.namedGlue(&b, x)
	case *resolve.Tuple:
		// elements in reverse order
		for i := len(x.Elems) - 1; i >= 0; i-- {
			if eg := e.glueFor(x.Elems[i]); eg != "null" {
				fmt.Fprintf(&b, "  %%e%d = getelementptr %s, ptr %%p, i32 0, i32 %d\n  call void %s(ptr %%e%d)\n", i, e.lty(x), i, eg, i)
			}
		}
	}
	b.WriteString("  ret void\n}\n")
	e.glueBody.WriteString(b.String())
	return sym
}

// namedGlue emits the body of a named type's glue: the kern destructor
// (the type's own or a prelude container's), then fields in reverse
// declaration order; the opaque library objects call the runtime with their
// element glue.
func (e *emitter) namedGlue(b *strings.Builder, n *resolve.Named) {
	args := typeArgs(n)
	elem := func(i int) resolve.Type {
		if i < len(n.Args) {
			return n.Args[i]
		}
		return &resolve.Unit{}
	}
	if layout.Opaque(n) {
		li := layout.Of(elem(0))
		eg := e.glueFor(elem(0))
		switch n.Decl.Name {
		case "Shared":
			e.declare("kern_shared_drop", "void (ptr, ptr)")
			fmt.Fprintf(b, "  %%h = load ptr, ptr %%p\n  call void @kern_shared_drop(ptr %%h, ptr %s)\n", eg)
		case "Mutex":
			e.declare("kern_mutex_drop", "void (ptr, ptr)")
			fmt.Fprintf(b, "  %%h = load ptr, ptr %%p\n  call void @kern_mutex_drop(ptr %%h, ptr %s)\n", eg)
		case "Scope":
			e.declare("kern_scope_drop", "void (ptr)")
			fmt.Fprintf(b, "  %%h = load ptr, ptr %%p\n  call void @kern_scope_drop(ptr %%h)\n")
		case "Thread":
			e.declare("kern_thread_drop", "void (ptr)")
			fmt.Fprintf(b, "  %%h = load ptr, ptr %%p\n  call void @kern_thread_drop(ptr %%h)\n")
		case "Join":
			e.declare("kern_join_drop", "void (ptr, ptr)")
			fmt.Fprintf(b, "  %%h = load ptr, ptr %%p\n  call void @kern_join_drop(ptr %%h, ptr %s)\n", eg)
		}
		_ = li
		return
	}
	ty := e.namedType(n)
	if df := e.mod.Func(n.String() + ".destructor"); df != nil && !df.Intrinsic {
		// a kern destructor: the type's own, or a prelude container's (a
		// library function, with no call site to report: an empty span)
		span := ""
		if df.Library {
			span = ", i64 0, i64 0"
		}
		fmt.Fprintf(b, "  call void %s(ptr %%p%s)\n", fnSym(n.String()+".destructor"), span)
	}
	switch n.Decl.Kind {
	case resolve.KindStruct:
		for i := len(n.Decl.Fields) - 1; i >= 0; i-- {
			ft := resolve.Subst(n.Decl.Fields[i].Type, args)
			if g := e.glueFor(ft); g != "null" {
				fmt.Fprintf(b, "  %%f%d = getelementptr %s, ptr %%p, i32 0, i32 %d\n  call void %s(ptr %%f%d)\n", i, ty, i, g, i)
			}
		}
	case resolve.KindUnion:
		li := layout.Of(n)
		tagTy := "i8"
		if li.TagSize == 2 {
			tagTy = "i16"
		}
		fmt.Fprintf(b, "  %%tag = load %s, ptr %%p\n  %%t32 = zext %s %%tag to i32\n  switch i32 %%t32, label %%done [", tagTy, tagTy)
		for _, v := range n.Decl.Variants {
			fmt.Fprintf(b, " i32 %d, label %%v%d", v.Index, v.Index)
		}
		b.WriteString(" ]\n")
		for _, v := range n.Decl.Variants {
			fmt.Fprintf(b, "v%d:\n", v.Index)
			offs := layout.VariantLayout(n, v)
			for i := len(v.Fields) - 1; i >= 0; i-- {
				ft := resolve.Subst(v.Fields[i].Type, args)
				if g := e.glueFor(ft); g != "null" {
					fmt.Fprintf(b, "  %%v%d.f%d = getelementptr i8, ptr %%p, i64 %d\n  call void %s(ptr %%v%d.f%d)\n", v.Index, i, offs[i], g, v.Index, i)
				}
			}
			b.WriteString("  br label %done\n")
		}
		b.WriteString("done:\n")
	}
}

// declare records an external runtime function declaration.
func (e *emitter) declare(name, sig string) {
	key := name
	if e.runtime[key] {
		return
	}
	e.runtime[key] = true
	// sig is "ret (params)"
	i := strings.Index(sig, " (")
	ret, params := sig[:i], sig[i+2:len(sig)-1]
	fmt.Fprintf(&e.decls, "declare %s @%s(%s)\n", ret, name, params)
}
