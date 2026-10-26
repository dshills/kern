package llvm

import (
	"fmt"
	"strings"

	"github.com/dshills/kern/compiler/layout"
	"github.com/dshills/kern/compiler/resolve"
)

// eqFor returns the symbol of `i1 (ptr, ptr)` structural equality for an
// aggregate type, emitting it once: structs and tuples compare every field,
// arrays every element, unions the tag and then the active payload; floats
// compare with ordered equality (so -0.0 == 0.0 and NaN != NaN) and padding
// is never read.
func (e *emitter) eqFor(t resolve.Type) string {
	name := "eq." + t.String()
	sym := fnSym(name)
	if e.eqs == nil {
		e.eqs = map[string]bool{}
	}
	if e.eqs[name] {
		return sym
	}
	e.eqs[name] = true
	g := &eqGen{e: e}
	fmt.Fprintf(&g.b, "define internal i1 %s(ptr %%a, ptr %%b) {\nentry:\n", sym)
	switch x := t.(type) {
	case *resolve.Array:
		g.array(x)
	case *resolve.Tuple:
		g.fields(x.Elems, func(i int) string { return fmt.Sprintf("getelementptr %s, ptr %%%%s, i32 0, i32 %d", e.lty(x), i) })
	case *resolve.Named:
		switch x.Decl.Kind {
		case resolve.KindStruct:
			args := typeArgs(x)
			var fts []resolve.Type
			for _, f := range x.Decl.Fields {
				fts = append(fts, resolve.Subst(f.Type, args))
			}
			g.fields(fts, func(i int) string {
				return fmt.Sprintf("getelementptr %s, ptr %%%%s, i32 0, i32 %d", e.namedType(x), i)
			})
		case resolve.KindUnion:
			g.union(x)
		}
	}
	g.b.WriteString("  ret i1 true\nfail:\n  ret i1 false\n}\n")
	e.glueBody.WriteString(g.b.String())
	return sym
}

type eqGen struct {
	e *emitter
	b strings.Builder
	n int
}

func (g *eqGen) next() string {
	g.n++
	return fmt.Sprintf("%%t%d", g.n)
}

func (g *eqGen) label() string {
	g.n++
	return fmt.Sprintf("l%d", g.n)
}

// scalar compares the values at pa and pb of type ty, returning an i1 register.
func (g *eqGen) scalar(pa, pb string, ty resolve.Type) string {
	e := g.e
	switch x := ty.(type) {
	case *resolve.Unit:
		return "true"
	case *resolve.Prim, *resolve.RawPtr, *resolve.Func:
		mt := e.mty(ty)
		a, b, r := g.next(), g.next(), g.next()
		fmt.Fprintf(&g.b, "  %s = load %s, ptr %s\n  %s = load %s, ptr %s\n", a, mt, pa, b, mt, pb)
		if resolve.IsFloat(ty) {
			fmt.Fprintf(&g.b, "  %s = fcmp oeq %s %s, %s\n", r, mt, a, b)
		} else {
			fmt.Fprintf(&g.b, "  %s = icmp eq %s %s, %s\n", r, mt, a, b)
		}
		return r
	case *resolve.Named:
		if layout.Opaque(x) {
			a, b, r := g.next(), g.next(), g.next()
			fmt.Fprintf(&g.b, "  %s = load ptr, ptr %s\n  %s = load ptr, ptr %s\n  %s = icmp eq ptr %s, %s\n", a, pa, b, pb, r, a, b)
			return r
		}
	case *resolve.Access, *resolve.Slice:
		// second-class values never reach aggregate equality
		return "true"
	}
	r := g.next()
	fmt.Fprintf(&g.b, "  %s = call i1 %s(ptr %s, ptr %s)\n", r, g.e.eqFor(ty), pa, pb)
	return r
}

// require branches to fail unless cond holds.
func (g *eqGen) require(cond string) {
	l := g.label()
	fmt.Fprintf(&g.b, "  br i1 %s, label %%%s, label %%fail\n%s:\n", cond, l, l)
}

// fields compares fields addressed by gep(i) with "%s" standing for the base.
func (g *eqGen) fields(fts []resolve.Type, gep func(i int) string) {
	for i, ft := range fts {
		pa, pb := g.next(), g.next()
		fmt.Fprintf(&g.b, "  %s = %s\n", pa, fmt.Sprintf(gep(i), "a"))
		fmt.Fprintf(&g.b, "  %s = %s\n", pb, fmt.Sprintf(gep(i), "b"))
		g.require(g.scalar(pa, pb, ft))
	}
}

func (g *eqGen) array(x *resolve.Array) {
	if x.Len == 0 {
		return
	}
	et := g.e.mty(x.Elem)
	fmt.Fprintf(&g.b, "  br label %%loop\nloop:\n  %%i = phi i64 [ 0, %%entry ], [ %%next, %%cont ]\n  %%done = icmp eq i64 %%i, %d\n  br i1 %%done, label %%exit, label %%body\nbody:\n  %%ea = getelementptr %s, ptr %%a, i64 %%i\n  %%eb = getelementptr %s, ptr %%b, i64 %%i\n", x.Len, et, et)
	r := g.scalar("%ea", "%eb", x.Elem)
	fmt.Fprintf(&g.b, "  br i1 %s, label %%cont, label %%fail\ncont:\n  %%next = add i64 %%i, 1\n  br label %%loop\nexit:\n", r)
}

func (g *eqGen) union(n *resolve.Named) {
	li := layout.Of(n)
	tagTy := "i8"
	if li.TagSize == 2 {
		tagTy = "i16"
	}
	ta, tb, same := g.next(), g.next(), g.next()
	fmt.Fprintf(&g.b, "  %s = load %s, ptr %%a\n  %s = load %s, ptr %%b\n  %s = icmp eq %s %s, %s\n", ta, tagTy, tb, tagTy, same, tagTy, ta, tb)
	g.require(same)
	var cases []string
	var bodies []string
	for k, v := range n.Decl.Variants {
		if len(v.Fields) == 0 {
			continue
		}
		l := g.label()
		cases = append(cases, fmt.Sprintf("%s %d, label %%%s", tagTy, k, l))
		var vb strings.Builder
		fmt.Fprintf(&vb, "%s:\n", l)
		offs := layout.VariantLayout(n, v)
		fts := resolve.VariantFieldTypes(n, v)
		saved := g.b
		g.b = vb
		for i, ft := range fts {
			pa, pb := g.next(), g.next()
			fmt.Fprintf(&g.b, "  %s = getelementptr i8, ptr %%a, i64 %d\n  %s = getelementptr i8, ptr %%b, i64 %d\n", pa, offs[i], pb, offs[i])
			g.require(g.scalar(pa, pb, ft))
		}
		g.b.WriteString("  ret i1 true\n")
		bodies = append(bodies, g.b.String())
		g.b = saved
	}
	fmt.Fprintf(&g.b, "  switch %s %s, label %%same [ %s ]\nsame:\n  ret i1 true\n", tagTy, ta, strings.Join(cases, " "))
	for _, body := range bodies {
		g.b.WriteString(body)
	}
}
