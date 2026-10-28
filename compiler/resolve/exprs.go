package resolve

import (
	"github.com/dshills/kern/compiler/diag"
	"math"
	"strings"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/parser"
)

// exprExpect types e and requires it to be assignable to want.
func (c *fnCtx) exprExpect(sc *scope, e ast.Expr, want Type) Type {
	got := c.expr(sc, e, want)
	if !c.assignable(got, want, e) {
		c.errorf("TYPE_MISMATCH", e.Span(), "expected %s, found %s", typeString(want), typeString(got))
		c.literalFix(e, want)
	}
	return got
}

// literalFix offers the literal written with the expected type's suffix when
// the mismatch is a suffixed numeric literal against a numeric type.
func (c *fnCtx) literalFix(e ast.Expr, want Type) {
	p, ok := want.(*Prim)
	if !ok {
		return
	}
	// a negative float is a unary minus over the literal (the parser folds
	// the sign into integer literals only): the fix rewrites the literal
	// and leaves the sign where it is
	if u, ok := e.(*ast.Unary); ok && u.Op == "-" {
		if fl, ok := u.X.(*ast.FloatLit); ok {
			e = fl
		}
	}
	switch x := e.(type) {
	case *ast.IntLit:
		if x.Suffix == "" || x.Text == "" || !IsInteger(want) || !fits(x, p.Name) {
			return
		}
		text := strings.TrimSuffix(x.Text, x.Suffix) + p.Name
		if x.Neg {
			text = "-" + text
		}
		c.fix(diag.Replace(c.file, span(x.Pos), text))
	case *ast.FloatLit:
		if x.Suffix == "" || x.Text == "" || !IsFloat(want) || (p.Name == "f32" && math.Abs(x.Value) > math.MaxFloat32) {
			return
		}
		c.fix(diag.Replace(c.file, span(x.Pos), strings.TrimSuffix(x.Text, x.Suffix)+p.Name))
	}
}

// assignable reports whether a value of type got can be used where want is
// required: identical types, or an access whose element type matches a value
// requirement (a projection consumed as a value is a read of its place).
func (c *fnCtx) assignable(got, want Type, e ast.Expr) bool {
	if got == nil || want == nil {
		return true
	}
	if Identical(got, want) {
		return true
	}
	if a, ok := got.(*Access); ok {
		return Identical(accessElem(a), want) && IsCopy(want)
	}
	return false
}

func (c *fnCtx) record(e ast.Expr, t Type) Type {
	c.r.prog.Info.Types[e] = t
	return t
}

// expr types an expression. want is the expected type when known (used for
// untyped literals, variant constructors and generic inference), else nil.
func (c *fnCtx) expr(sc *scope, e ast.Expr, want Type) Type {
	switch x := e.(type) {
	case *ast.IntLit:
		c.r.prog.Info.Consts[e] = true
		return c.record(e, c.intLit(x, want))
	case *ast.FloatLit:
		c.r.prog.Info.Consts[e] = true
		if x.Suffix != "" {
			return c.record(e, &Prim{Name: x.Suffix})
		}
		if want != nil && IsFloat(want) {
			return c.record(e, want)
		}
		return c.record(e, &Prim{Name: "f64"})
	case *ast.StringLit:
		c.r.prog.Info.Consts[e] = true
		return c.record(e, &Access{Seq: true, Elem: &Prim{Name: "u8"}})
	case *ast.BoolLit:
		c.r.prog.Info.Consts[e] = true
		return c.record(e, &Prim{Name: "bool"})
	case *ast.UnitLit:
		return c.record(e, &Unit{})
	case *ast.Paren:
		return c.record(e, c.expr(sc, x.X, want))
	case *ast.Ident:
		return c.record(e, c.ident(sc, x, want))
	case *ast.FieldExpr:
		return c.record(e, c.fieldWant(sc, x, want))
	case *ast.IndexExpr:
		return c.record(e, c.index(sc, x, want))
	case *ast.SliceExpr:
		return c.record(e, c.slice(sc, x))
	case *ast.CallExpr:
		return c.record(e, c.call(sc, x, want))
	case *ast.ClosureExpr:
		return c.record(e, c.closure(sc, x, want))
	case *ast.Binary:
		return c.record(e, c.binary(sc, x, want))
	case *ast.Unary:
		t := c.expr(sc, x.X, want)
		if x.Op == "!" && !IsBool(t) {
			c.errorf("TYPE_MISMATCH", x.Pos, "! requires bool, found %s", typeString(t))
		}
		if x.Op == "-" && !IsInteger(t) && !IsFloat(t) {
			c.errorf("TYPE_MISMATCH", x.Pos, "unary - requires a number, found %s", typeString(t))
		}
		return c.record(e, valueOf(t))
	case *ast.Try:
		t := c.expr(sc, x.X, nil)
		n, ok := valueOf(t).(*Named)
		if !ok || n.Decl.Name != "Result" || n.Decl.Module != "core" {
			c.errorf("TYPE_QUESTION_MISMATCH", x.Pos, "? requires a Result, found %s", typeString(t))
			return c.record(e, &Bad{})
		}
		if len(c.fd.Ret) != 1 {
			c.errorf("TYPE_QUESTION_MISMATCH", x.Pos, "? used in a function that does not return Result")
			return c.record(e, n.Args[0])
		}
		rn, ok := c.fd.Ret[0].(*Named)
		if !ok || rn.Decl != n.Decl || !Identical(rn.Args[1], n.Args[1]) {
			c.errorf("TYPE_QUESTION_MISMATCH", x.Pos, "? error type %s does not match the function's %s", n.Args[1], typeString(c.fd.Ret[0]))
		}
		return c.record(e, n.Args[0])
	case *ast.Move:
		sym := sc.lookup(x.Name)
		if sym == nil || (sym.Kind != SymLocal && sym.Kind != SymParam) {
			c.errorf("NAME_UNRESOLVED", x.Pos, "move requires an owned local, found %s", x.Name)
			return c.record(e, &Bad{})
		}
		id := &ast.Ident{Pos: x.Pos, Name: x.Name}
		c.r.prog.Info.Idents[id] = sym
		c.r.prog.Info.Consumed[x] = id
		return c.record(e, sym.Type)
	case *ast.Copy:
		t := c.expr(sc, x.X, nil)
		return c.record(e, valueOf(t))
	case *ast.Take:
		t := c.expr(sc, x.Place, nil)
		if !c.isPlace(sc, x.Place) {
			c.errorf("PLACE_REQUIRED", x.Place.Span(), "take requires a place")
		}
		return c.record(e, valueOf(t))
	case *ast.Replace:
		t := c.expr(sc, x.Place, nil)
		if !c.isPlace(sc, x.Place) {
			c.errorf("PLACE_REQUIRED", x.Place.Span(), "replace requires a place")
		}
		c.exprExpect(sc, x.Value, valueOf(t))
		return c.record(e, valueOf(t))
	case *ast.Drop:
		sym := sc.lookup(x.Name)
		if sym == nil || (sym.Kind != SymLocal && (sym.Kind != SymParam || (sym.Conv != ast.ConvOwn && sym.Conv != ast.ConvValue))) {
			c.errorf("NAME_UNRESOLVED", x.Pos, "drop requires an owned local, found %s", x.Name)
			return c.record(e, &Bad{})
		}
		id := &ast.Ident{Pos: x.Pos, Name: x.Name}
		c.r.prog.Info.Idents[id] = sym
		c.r.prog.Info.Consumed[x] = id
		return c.record(e, &Unit{})
	case *ast.TrapExpr:
		return c.record(e, &Unit{})
	case *ast.ConvExpr:
		t := c.expr(sc, x.X, nil)
		to := &Prim{Name: x.To}
		if x.Truncate {
			if !IsInteger(t) || !IsInteger(to) {
				c.errorf("TYPE_MISMATCH", x.Pos, "truncate requires integer types")
			}
		} else if (!IsInteger(t) && !IsFloat(t)) || (!IsInteger(to) && !IsFloat(to)) {
			c.errorf("TYPE_MISMATCH", x.Pos, "cannot convert %s to %s", typeString(t), x.To)
		}
		return c.record(e, to)
	case *ast.StructLit:
		return c.record(e, c.structLit(sc, x))
	case *ast.ArrayLit:
		return c.record(e, c.arrayLit(sc, x, want))
	case *ast.RangeExpr:
		c.errorf("TYPE_MISMATCH", x.Pos, "range is only valid in for")
		return c.record(e, &Bad{})
	case *ast.Qualified:
		c.errorf("NAME_UNRESOLVED", x.Pos, "qualified name not supported here")
		return c.record(e, &Bad{})
	}
	c.errorf("SYNTAX", e.Span(), "unsupported expression")
	return c.record(e, &Bad{})
}

// valueOf strips an access type to the value type it denotes.
func valueOf(t Type) Type {
	if a, ok := t.(*Access); ok {
		return accessElem(a)
	}
	return t
}

// intLimits holds, per integer type, the largest positive magnitude and the
// largest negative magnitude a literal may have.
var intLimits = map[string][2]uint64{
	"i8": {127, 128}, "i16": {32767, 32768}, "i32": {2147483647, 2147483648},
	"i64": {math.MaxInt64, 1 << 63}, "isize": {math.MaxInt64, 1 << 63},
	"u8": {255, 0}, "u16": {65535, 0}, "u32": {4294967295, 0}, "u64": {math.MaxUint64, 0}, "usize": {math.MaxUint64, 0},
}

func (c *fnCtx) intLit(x *ast.IntLit, want Type) Type {
	name := x.Suffix
	if name == "" {
		if want != nil && IsInteger(want) {
			name = want.(*Prim).Name
		} else if want != nil && IsFloat(want) {
			return want
		} else {
			name = "i64"
		}
	}
	if !fits(x, name) {
		c.errorf("TYPE_MISMATCH", x.Pos, "literal does not fit in %s", name)
	}
	return &Prim{Name: name}
}

// fits reports whether the literal's value is representable in the integer
// type name: within its positive limit, or within its negative limit when
// negated (a negative literal of an unsigned type fits only as zero).
func fits(x *ast.IntLit, name string) bool {
	lim := intLimits[name]
	if !x.Neg {
		return x.Value <= lim[0]
	}
	return x.Value <= lim[1] && (x.Value == 0 || lim[1] != 0)
}

// globalSym is the one symbol of a thread_local in this resolver (places are
// keyed by symbol identity).
func (c *fnCtx) globalSym(g *GlobalDecl) *Symbol {
	if c.r.globalSyms == nil {
		c.r.globalSyms = map[*GlobalDecl]*Symbol{}
	}
	if s, ok := c.r.globalSyms[g]; ok {
		return s
	}
	s := &Symbol{Kind: SymGlobal, Name: g.Name, Type: g.Type, Conv: ast.ConvEdit, Pos: g.Pos, Global: g, ID: c.newID()}
	c.r.globalSyms[g] = s
	return s
}

func (c *fnCtx) ident(sc *scope, x *ast.Ident, want Type) Type {
	sym := sc.lookup(x.Name)
	if sym == nil {
		if f, ok := c.m.Funcs[x.Name]; ok {
			sym = &Symbol{Kind: SymFunc, Name: x.Name, Func: f}
		} else if k, ok := c.m.Consts[x.Name]; ok {
			sym = &Symbol{Kind: SymConst, Name: x.Name, Const: k, Type: k.Type}
			c.r.prog.Info.Consts[x] = true
		} else if g, ok := c.m.Globals[x.Name]; ok {
			sym = c.globalSym(g)
		} else if td, ok := c.m.Types[x.Name]; ok {
			sym = &Symbol{Kind: SymType, Name: x.Name, TDecl: td}
		} else if mod, ok := c.m.Imports[x.Name]; ok {
			sym = &Symbol{Kind: SymModule, Name: x.Name, Module: mod}
		} else if vt := c.bareVariant(sc, x, want, nil); vt != nil {
			return vt
		} else {
			c.errorf("NAME_UNRESOLVED", x.Pos, "unknown name %s", x.Name)
			if s := nearest(x.Name, c.visibleNames(sc)); s != "" {
				c.fix(diag.Replace(c.file, span(x.Pos), s))
			}
			return &Bad{}
		}
	}
	c.r.prog.Info.Idents[x] = sym
	switch sym.Kind {
	case SymLocal, SymParam, SymAccess:
		return sym.Type
	case SymConst, SymGlobal:
		return sym.Type
	case SymFunc:
		if len(sym.Func.TypeParams) > 0 {
			c.errorf("TYPE_INFER_FAILED", x.Pos, "generic function %s used as a value needs explicit type arguments", x.Name)
		}
		if isUnsafeOp(sym.Func) {
			c.errorf("UNSAFE_OP_OUTSIDE_UNSAFE", x.Pos, "%s is an unsafe operation and cannot be used as a function value", x.Name)
		}
		return sym.Func.Type
	case SymType, SymModule, SymTypeParam:
		return &Unit{}
	}
	return &Unit{}
}

// bareVariant resolves `Variant(args)` or `Variant` without a union qualifier
// when exactly one union in scope declares the variant.
func (c *fnCtx) bareVariant(sc *scope, x *ast.Ident, want Type, args []*ast.Arg) Type {
	var found *TypeDecl
	var vd *VariantDecl
	count := 0
	for _, td := range c.m.Types {
		if td.Kind != KindUnion {
			continue
		}
		for _, v := range td.Variants {
			if v.Name == x.Name {
				found, vd = td, v
				count++
			}
		}
	}
	if count != 1 {
		return nil
	}
	c.r.prog.Info.Idents[x] = &Symbol{Kind: SymType, Name: found.Name, TDecl: found}
	return c.variantCtor(sc, x.Pos, found, vd, nil, want, args)
}

// variantCtor types a variant constructor, inferring type arguments from
// want or from the constructor arguments.
func (c *fnCtx) variantCtor(sc *scope, pos ast.Pos, td *TypeDecl, vd *VariantDecl, explicit []Type, want Type, args []*ast.Arg) Type {
	m := map[string]Type{}
	params := map[string]bool{}
	for _, tp := range td.TypeParams {
		params[tp.Name] = true
	}
	if explicit != nil {
		for i, tp := range td.TypeParams {
			if i < len(explicit) {
				m[tp.Name] = explicit[i]
			}
		}
	} else if wn, ok := valueOf(want).(*Named); ok && wn.Decl == td {
		for i, tp := range td.TypeParams {
			m[tp.Name] = wn.Args[i]
		}
	}
	if len(args) != len(vd.Fields) {
		c.errorf("CALL_ARITY", pos, "variant %s takes %d arguments, got %d", vd.Name, len(vd.Fields), len(args))
	}
	for i, a := range args {
		if i >= len(vd.Fields) {
			break
		}
		if a.Out {
			c.errorf("TYPE_MISMATCH", a.Pos, "out is not valid in a variant constructor")
		}
		ft := vd.Fields[i].Type
		var wantArg Type
		if sub := Subst(ft, m); !mentionsUnbound(sub, params, m) {
			wantArg = sub
		}
		at := c.expr(sc, a.X, wantArg)
		at = valueOf(at)
		if !Unify(ft, at, params, m) {
			c.errorf("TYPE_MISMATCH", a.X.Span(), "expected %s, found %s", typeString(Subst(ft, m)), typeString(at))
		}
	}
	n := &Named{Decl: td}
	for _, tp := range td.TypeParams {
		t, ok := m[tp.Name]
		if !ok {
			c.errorf("TYPE_INFER_FAILED", pos, "cannot infer type parameter %s of %s; write %s[...]", tp.Name, td.Name, td.Name)
			t = &Bad{}
		} else if explicit == nil {
			c.r.checkConstraints(c.file, pos, t, tp)
		}
		n.Args = append(n.Args, t)
	}
	return n
}

func mentionsUnbound(t Type, params map[string]bool, m map[string]Type) bool {
	switch x := t.(type) {
	case *TypeParamType:
		_, bound := m[x.Name]
		return params[x.Name] && !bound
	case *Array:
		return mentionsUnbound(x.Elem, params, m)
	case *Slice:
		return mentionsUnbound(x.Elem, params, m)
	case *Access:
		return mentionsUnbound(x.Elem, params, m)
	case *RawPtr:
		return mentionsUnbound(x.Elem, params, m)
	case *Named:
		for _, a := range x.Args {
			if mentionsUnbound(a, params, m) {
				return true
			}
		}
	case *Func:
		for _, p := range x.Params {
			if mentionsUnbound(p.Type, params, m) {
				return true
			}
		}
		for _, r := range x.Ret {
			if mentionsUnbound(r, params, m) {
				return true
			}
		}
	}
	return false
}

func (c *fnCtx) field(sc *scope, x *ast.FieldExpr) Type { return c.fieldWant(sc, x, nil) }

// fieldWant types a field selection; a field of a packed struct is an
// unaligned access and requires unsafe (rule 6.7), on every path.
func (c *fnCtx) fieldWant(sc *scope, x *ast.FieldExpr, want Type) Type {
	t := c.fieldWant1(sc, x, want)
	if n, ok := valueOf(c.r.prog.Info.Types[x.X]).(*Named); ok && n.Decl.Packed && c.unsafe == 0 {
		c.errorf("UNSAFE_OP_OUTSIDE_UNSAFE", x.Pos, "field access on packed struct %s requires unsafe", n.Decl.Name)
	}
	return t
}

func (c *fnCtx) fieldWant1(sc *scope, x *ast.FieldExpr, want Type) Type {
	// Static member of a type or module: handled by call(); as a bare
	// expression a type's variant without arguments, e.g. Option[u64].None.
	if td, explicit, ok := c.typeExprOf(sc, x.X); ok {
		for _, v := range td.Variants {
			if v.Name == x.Name {
				return c.variantCtor(sc, x.Pos, td, v, explicit, want, nil)
			}
		}
		if m := td.Methods[x.Name]; m != nil && len(m.Params) > 0 && m.Params[0].Self {
			c.errorf("TYPE_MISMATCH", x.Pos, "%s.%s is a method; call it on a value", td.Name, x.Name)
			return &Bad{}
		}
		if m := td.Methods[x.Name]; m != nil {
			return m.Type
		}
		c.errorf("NAME_UNRESOLVED", x.Pos, "%s has no member %s", td.Name, x.Name)
		return &Bad{}
	}
	if id, ok := x.X.(*ast.Ident); ok {
		if mod, ok := c.m.Imports[id.Name]; ok && sc.lookup(id.Name) == nil {
			if f, ok := mod.Funcs[x.Name]; ok {
				c.r.prog.Info.Idents[id] = &Symbol{Kind: SymModule, Name: id.Name, Module: mod}
				if !isExported(x.Name) && mod.Name != "core" {
					c.errorf("NAME_PRIVATE", x.Pos, "%s.%s is private to module %s", id.Name, x.Name, mod.Name)
				}
				return f.Type
			}
			if k, ok := mod.Consts[x.Name]; ok {
				// an imported constant: a value of its declared type
				c.r.prog.Info.Idents[id] = &Symbol{Kind: SymModule, Name: id.Name, Module: mod}
				c.r.prog.Info.Consts[x] = true
				c.r.prog.Info.ImportedConsts[x] = k
				if !isExported(x.Name) && mod.Name != "core" {
					c.errorf("NAME_PRIVATE", x.Pos, "%s.%s is private to module %s", id.Name, x.Name, mod.Name)
				}
				return k.Type
			}
			c.errorf("NAME_UNRESOLVED", x.Pos, "module %s has no %s", id.Name, x.Name)
			return &Bad{}
		}
	}
	t := valueOf(c.expr(sc, x.X, nil))
	n, ok := t.(*Named)
	if !ok || n.Decl.Kind != KindStruct {
		c.errorf("NAME_UNRESOLVED", x.Pos, "%s has no field %s", typeString(t), x.Name)
		return &Bad{}
	}
	if n.Decl.Module == "core" && x.Name == tagField && c.m.Name != "core" {
		// reported the same in a release build, whose handles lack the field
		c.errorf("NAME_PRIVATE", x.Pos, "field %s is private to module %s", x.Name, n.Decl.Module)
		return &Prim{Name: "u32"}
	}
	fd, ft := FieldType(n, x.Name)
	if fd == nil {
		if m := n.Decl.Methods[x.Name]; m != nil {
			c.errorf("TYPE_MISMATCH", x.Pos, "%s.%s is a method; call it", n.Decl.Name, x.Name)
		} else {
			c.errorf("NAME_UNRESOLVED", x.Pos, "%s has no field %s", n.Decl.Name, x.Name)
		}
		return &Unit{}
	}
	if fieldPrivate(n, x.Name, c.m.Name) {
		c.errorf("NAME_PRIVATE", x.Pos, "field %s is private to module %s", x.Name, n.Decl.Module)
	}
	c.r.prog.Info.Fields[x] = fd
	return ft
}

func isExported(name string) bool { return len(name) > 0 && name[0] >= 'A' && name[0] <= 'Z' }

// IsExported reports whether a declaration name is exported (capitalized).
func IsExported(name string) bool { return isExported(name) }

// typeExprOf recognises an expression that denotes a type: `Name` or
// `Name[Args]` where Name is a struct or union, or pkg.Name. It returns the
// declaration and the explicit type arguments (nil when none).
func (c *fnCtx) typeExprOf(sc *scope, e ast.Expr) (*TypeDecl, []Type, bool) {
	switch x := e.(type) {
	case *ast.Ident:
		if sc.lookup(x.Name) != nil {
			return nil, nil, false
		}
		if td, ok := c.m.Types[x.Name]; ok {
			c.r.prog.Info.Idents[x] = &Symbol{Kind: SymType, Name: x.Name, TDecl: td}
			return td, nil, true
		}
	case *ast.IndexExpr:
		id, ok := x.X.(*ast.Ident)
		if !ok || sc.lookup(id.Name) != nil {
			return nil, nil, false
		}
		td, ok := c.m.Types[id.Name]
		if !ok {
			return nil, nil, false
		}
		c.r.prog.Info.Idents[id] = &Symbol{Kind: SymType, Name: id.Name, TDecl: td}
		var args []Type
		p := &parser.Converter{}
		for _, a := range x.Args {
			te := p.ExprToType(a)
			if te == nil {
				c.errorf("TYPE_MISMATCH", a.Span(), "expected a type argument")
				return td, nil, true
			}
			args = append(args, c.r.resolveType(c.m, c.file, sc, te))
		}
		if len(args) != len(td.TypeParams) {
			c.errorf("TYPE_ARITY", x.Pos, "%s expects %d type arguments, got %d", td.Name, len(td.TypeParams), len(args))
			return td, args, true
		}
		for i, at := range args {
			c.r.checkConstraints(c.file, x.Args[i].Span(), at, td.TypeParams[i])
		}
		return td, args, true
	}
	return nil, nil, false
}

func (c *fnCtx) index(sc *scope, x *ast.IndexExpr, want Type) Type {
	// Generic instantiation of a function: f[T]
	if id, ok := x.X.(*ast.Ident); ok && sc.lookup(id.Name) == nil {
		if f, ok := c.m.Funcs[id.Name]; ok {
			c.r.prog.Info.Idents[id] = &Symbol{Kind: SymFunc, Name: id.Name, Func: f}
			args := c.typeArgsOf(sc, x.Args)
			if len(args) != len(f.TypeParams) {
				c.errorf("TYPE_ARITY", x.Pos, "%s expects %d type arguments, got %d", f.Name, len(f.TypeParams), len(args))
				return &Bad{}
			}
			m := map[string]Type{}
			for i, tp := range f.TypeParams {
				m[tp.Name] = args[i]
				c.r.checkConstraints(c.file, x.Args[i].Span(), args[i], tp)
			}
			return Subst(f.Type, m)
		}
	}
	if td, args, ok := c.typeExprOf(sc, x); ok {
		_ = td
		_ = args
		return &Unit{} // a type used as an expression: only valid as a call receiver
	}
	t := c.expr(sc, x.X, nil)
	if len(x.Args) != 1 {
		c.errorf("TYPE_MISMATCH", x.Pos, "indexing takes one index")
		return &Bad{}
	}
	c.exprExpect(sc, x.Args[0], &Prim{Name: "usize"})
	switch it := t.(type) {
	case *Array:
		return it.Elem
	case *Slice:
		return it.Elem
	case *Access:
		if it.Seq {
			return it.Elem
		}
		if arr, ok := it.Elem.(*Array); ok {
			return arr.Elem
		}
		if sl, ok := it.Elem.(*Slice); ok {
			return sl.Elem
		}
	}
	c.errorf("TYPE_MISMATCH", x.Pos, "cannot index %s", typeString(t))
	return &Bad{}
}

func (c *fnCtx) typeArgsOf(sc *scope, es []ast.Expr) []Type {
	var out []Type
	p := &parser.Converter{}
	for _, a := range es {
		te := p.ExprToType(a)
		if te == nil {
			c.errorf("TYPE_MISMATCH", a.Span(), "expected a type argument")
			out = append(out, &Unit{})
			continue
		}
		out = append(out, c.r.resolveType(c.m, c.file, sc, te))
	}
	return out
}

func (c *fnCtx) slice(sc *scope, x *ast.SliceExpr) Type {
	t := c.expr(sc, x.X, nil)
	if x.Lo != nil {
		c.exprExpect(sc, x.Lo, &Prim{Name: "usize"})
	}
	if x.Hi != nil {
		c.exprExpect(sc, x.Hi, &Prim{Name: "usize"})
	}
	edit := c.placeIsMutable(sc, x.X)
	var elem Type
	switch it := t.(type) {
	case *Array:
		elem = it.Elem
	case *Slice:
		elem = it.Elem
	case *Access:
		edit = it.Edit
		if it.Seq {
			elem = it.Elem
		} else if arr, ok := it.Elem.(*Array); ok {
			elem = arr.Elem
		} else if sl, ok := it.Elem.(*Slice); ok {
			elem = sl.Elem
		}
	}
	if elem == nil {
		c.errorf("TYPE_MISMATCH", x.Pos, "cannot slice %s", typeString(t))
		return &Bad{}
	}
	return &Access{Edit: edit, Seq: true, Elem: elem}
}

func (c *fnCtx) binary(sc *scope, x *ast.Binary, want Type) Type {
	switch x.Op {
	case "&&", "||":
		c.exprExpect(sc, x.L, &Prim{Name: "bool"})
		c.exprExpect(sc, x.R, &Prim{Name: "bool"})
		return &Prim{Name: "bool"}
	case "==", "!=", "<", "<=", ">", ">=":
		lt := valueOf(c.expr(sc, x.L, nil))
		rt := valueOf(c.expr(sc, x.R, lt))
		if !Identical(lt, rt) {
			// retry with the right operand's type guiding the left literal
			if _, isLit := x.L.(*ast.IntLit); isLit {
				lt = valueOf(c.expr(sc, x.L, rt))
			}
		}
		if !Identical(lt, rt) {
			c.errorf("TYPE_MISMATCH", x.Pos, "cannot compare %s with %s", typeString(lt), typeString(rt))
		}
		if _, isNamed := lt.(*Named); isNamed && x.Op != "==" && x.Op != "!=" {
			c.errorf("TYPE_MISMATCH", x.Pos, "ordering comparison on %s", typeString(lt))
		}
		if c.r.prog.Info.Consts[x.L] && c.r.prog.Info.Consts[x.R] {
			c.r.prog.Info.Consts[x] = true
		}
		return &Prim{Name: "bool"}
	case "<<", ">>":
		lt := valueOf(c.expr(sc, x.L, want))
		c.exprExpect(sc, x.R, &Prim{Name: "u32"})
		if !IsInteger(lt) {
			c.errorf("TYPE_MISMATCH", x.Pos, "shift requires an integer, found %s", typeString(lt))
		}
		return lt
	}
	lt := valueOf(c.expr(sc, x.L, want))
	if _, isLit := x.L.(*ast.IntLit); isLit && want == nil {
		rt := valueOf(c.expr(sc, x.R, nil))
		lt = valueOf(c.expr(sc, x.L, rt))
		if !Identical(lt, rt) {
			c.errorf("TYPE_MISMATCH", x.Pos, "operands of %s differ: %s and %s", x.Op, typeString(lt), typeString(rt))
		}
	} else {
		c.exprExpect(sc, x.R, lt)
	}
	if !IsInteger(lt) && !IsFloat(lt) {
		c.errorf("TYPE_MISMATCH", x.Pos, "%s requires numbers, found %s", x.Op, typeString(lt))
	}
	if strings.ContainsAny(x.Op, "|&^") && IsFloat(lt) {
		c.errorf("TYPE_MISMATCH", x.Pos, "%s requires integers", x.Op)
	}
	if c.r.prog.Info.Consts[x.L] && c.r.prog.Info.Consts[x.R] {
		c.r.prog.Info.Consts[x] = true
	}
	return lt
}

// arrayLit types [e1, e2, ...]: every element has the type of the first (or
// the wanted element type); an empty literal needs a wanted array type.
func (c *fnCtx) arrayLit(sc *scope, x *ast.ArrayLit, want Type) Type {
	var elem Type
	if wa, ok := valueOf(want).(*Array); ok && want != nil {
		elem = wa.Elem
		if wa.Len != len(x.Elems) {
			c.errorf("TYPE_MISMATCH", x.Pos, "array literal has %d elements, expected %d", len(x.Elems), wa.Len)
		}
	}
	if len(x.Elems) == 0 {
		if elem == nil {
			c.errorf("TYPE_INFER_FAILED", x.Pos, "empty array literal needs an expected type")
			return &Bad{}
		}
		return &Array{Elem: elem, Len: 0}
	}
	for i, e := range x.Elems {
		t := valueOf(c.exprExpect(sc, e, elem))
		if elem == nil {
			elem = t
		} else if i > 0 && !Identical(t, elem) {
			c.errorf("TYPE_MISMATCH", e.Span(), "array element %d has type %s, expected %s", i, typeString(t), typeString(elem))
		}
	}
	return &Array{Elem: elem, Len: len(x.Elems)}
}

func (c *fnCtx) structLit(sc *scope, x *ast.StructLit) Type {
	t := c.r.resolveTypeName(c.m, c.file, sc, x.Type)
	n, ok := t.(*Named)
	if !ok || n.Decl.Kind != KindStruct {
		c.errorf("TYPE_MISMATCH", x.Pos, "%s is not a struct", typeString(t))
		return &Bad{}
	}
	if n.Decl.View {
		c.errorf("ACC_ESCAPE_STORE", x.Pos, "view struct %s cannot be constructed as a value", n.Decl.Name)
	}
	if isHandle(n) && c.m.Name != "core" {
		// a handle's fields differ between debug and release builds
		// (resolve.Prelude), so only its container issues one; the one
		// diagnostic is the same in both
		c.errorf("NAME_PRIVATE", x.Pos, "%s is issued by its container and cannot be constructed outside module core", n.Decl.Name)
		for _, fi := range x.Fields {
			c.expr(sc, fi.Value, nil)
		}
		return n
	}
	seen := map[string]bool{}
	for _, fi := range x.Fields {
		fd, ft := FieldType(n, fi.Name)
		if fd == nil {
			c.errorf("NAME_UNRESOLVED", fi.Pos, "%s has no field %s", n.Decl.Name, fi.Name)
			continue
		}
		if seen[fi.Name] {
			c.errorf("NAME_DUPLICATE", fi.Pos, "field %s initialized twice", fi.Name)
		}
		seen[fi.Name] = true
		if fieldPrivate(n, fi.Name, c.m.Name) {
			c.errorf("NAME_PRIVATE", fi.Pos, "field %s is private to module %s", fi.Name, n.Decl.Module)
		}
		c.exprExpect(sc, fi.Value, ft)
	}
	for _, f := range n.Decl.Fields {
		if !seen[f.Name] {
			c.errorf("INIT_UNINITIALIZED_READ", x.Pos, "field %s of %s is not initialized", f.Name, n.Decl.Name)
		}
	}
	return n
}

// fieldPrivate reports whether field name of n is private to a module other
// than from (spec/types.md §1): a lower-case field of a user type, or any
// field of a nocopy core struct (the containers' unsafe invariants depend on
// their fields, so only std/core touches them).
func fieldPrivate(n *Named, name, from string) bool {
	if n.Decl.Module == from {
		return false
	}
	if n.Decl.Module == "core" {
		return n.Decl.NoCopy || name == tagField
	}
	return !isExported(name)
}

// tagField is a handle's identity tag, present in debug builds only.
const tagField = "phantom_tag"

// isHandle reports the container handle types of the prelude.
func isHandle(n *Named) bool {
	return n.Decl.Module == "core" && (n.Decl.Name == "PoolId" || n.Decl.Name == "ArenaId")
}

// closure types a closure expression against the closure parameter type it
// is passed to (spec §4): the parameters take their types and conventions
// from it (declared types must agree), the body is resolved in a scope
// nested in the enclosing function's, with returns typed against the
// closure's result, and the enclosing symbols the body uses are recorded as
// captures, by edit where the body assigns, edits, moves or takes through
// them and by view otherwise.
func (c *fnCtx) closure(sc *scope, x *ast.ClosureExpr, want Type) Type {
	ft, ok := want.(*Func)
	if !ok || !ft.Closure {
		c.errorf("TYPE_MISMATCH", x.Pos, "closure expression where no closure parameter is expected")
		return &Bad{}
	}
	if len(x.Params) != len(ft.Params) {
		c.errorf("CALL_ARITY", x.Pos, "closure takes %d parameters, the closure type has %d", len(x.Params), len(ft.Params))
		return &Bad{}
	}
	for _, rt := range ft.Ret {
		if _, isAcc := rt.(*Access); isAcc {
			c.errorf("TYPE_MISMATCH", x.Pos, "a closure cannot return an access")
			return &Bad{}
		}
	}
	outer := map[*Symbol]bool{}
	for s := sc; s != nil; s = s.parent {
		for _, sym := range s.vars {
			if sym.Kind == SymLocal || sym.Kind == SymParam || sym.Kind == SymAccess {
				outer[sym] = true
			}
		}
	}
	info := &ClosureInfo{Type: ft}
	csc := sc.child()
	for i, p := range x.Params {
		fp := ft.Params[i]
		if p.Type != nil {
			declared := c.r.resolveType(c.m, c.file, sc, p.Type)
			if !Identical(declared, fp.Type) || p.Conv != fp.Conv {
				c.errorf("TYPE_MISMATCH", p.Pos, "closure parameter %s is declared %s, the closure type has %s", p.Name, typeString(declared), typeString(fp.Type))
			}
		}
		if fp.Conv == ast.ConvValue && !IsCopy(fp.Type) {
			c.errorf("PARAM_OWN_REQUIRED", p.Pos, "closure parameter %s has move-only type %s and the closure type must declare it own", p.Name, typeString(fp.Type))
		}
		sym := c.declare(csc, p, p.Name, SymParam, fp.Type, fp.Conv, p.Pos)
		if a, ok := fp.Type.(*Access); ok {
			sym.Type = a.Elem
			if a.Seq {
				sym.Type = &Slice{Elem: a.Elem}
			}
		}
		info.Params = append(info.Params, sym)
	}
	if x.Expr != nil && x.Body == nil {
		pos := x.Expr.Span()
		if len(ft.Ret) > 0 && !isUnitType(ft.Ret[0]) {
			x.Body = &ast.Block{Pos: pos, Stmts: []ast.Stmt{&ast.Return{Pos: pos, Value: x.Expr}}}
		} else {
			x.Body = &ast.Block{Pos: pos, Stmts: []ast.Stmt{&ast.ExprStmt{Pos: pos, X: x.Expr}}}
		}
	}
	saved := c.fd
	c.fd = &FuncDecl{Name: "closure", Module: c.m.Name, Pos: x.Pos, Ret: ft.Ret, Type: ft}
	c.block(csc, x.Body)
	c.fd = saved
	info.Captures = c.captures(x.Body, outer)
	c.r.prog.Info.Closures[x] = info
	return ft
}

func isUnitType(t Type) bool {
	_, ok := t.(*Unit)
	return ok
}

// captures lists the outer symbols a closure body uses, in order of first
// use, each marked edit when the body assigns to, takes from, replaces,
// moves, or passes as an edit or out argument a place rooted at it.
func (c *fnCtx) captures(body *ast.Block, outer map[*Symbol]bool) []*Capture {
	var out []*Capture
	index := map[*Symbol]int{}
	note := func(e ast.Expr, edit bool) {
		id := rootIdent(e)
		if id == nil {
			return
		}
		sym := c.r.prog.Info.Idents[id]
		if sym == nil || !outer[sym] {
			return
		}
		i, ok := index[sym]
		if !ok {
			i = len(out)
			index[sym] = i
			out = append(out, &Capture{Sym: sym, Ident: id})
		}
		if edit {
			out[i].Edit = true
		}
	}
	ast.Walk(body, func(n any) bool {
		switch x := n.(type) {
		case *ast.Ident:
			note(x, false)
		case *ast.Assign:
			note(x.Target, true)
		case *ast.Take:
			note(x.Place, true)
		case *ast.Replace:
			note(x.Place, true)
		case *ast.Move, *ast.Drop:
			// a move or drop of a captured variable: the checker rejects it
			// as a move out of the closure; captured by edit so that it
			// conflicts like a write would
			if id := c.r.prog.Info.Consumed[x.(ast.Expr)]; id != nil {
				note(id, true)
			}
		case *ast.CallExpr:
			info := c.r.prog.Info.Calls[x]
			if info == nil {
				return false
			}
			params := info.Params
			args := x.Args
			if info.Method {
				if fe, ok := x.Fn.(*ast.FieldExpr); ok {
					args = append([]*ast.Arg{{Pos: fe.X.Span(), X: fe.X}}, args...)
				}
			}
			for i, a := range args {
				if i < len(params) && (params[i].Conv == ast.ConvEdit || params[i].Conv == ast.ConvOut) {
					note(a.X, true)
				}
			}
		}
		return false
	})
	return out
}

// rootIdent is the identifier a place expression is rooted at, through
// parentheses, fields, indexes and slices; nil for anything else.
func rootIdent(e ast.Expr) *ast.Ident {
	for {
		switch x := e.(type) {
		case *ast.Ident:
			return x
		case *ast.Paren:
			e = x.X
		case *ast.FieldExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.SliceExpr:
			e = x.X
		default:
			return nil
		}
	}
}
