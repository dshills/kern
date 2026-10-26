package resolve

import (
	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/diag"
)

// fnCtx is the per-function typing context.
type fnCtx struct {
	r            *resolver
	m            *Module
	file         string
	fd           *FuncDecl
	nextID       int
	unsafe       int
	inDestructor bool
	dtor         *ast.Destructor
}

func (r *resolver) checkBodies(m *Module) {
	for _, f := range m.Files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.ConstDecl:
				// a constant's initializer is typed against its declared type
				// once, so every use (and the lowering) sees a typed literal
				if k := m.Consts[d.Name]; k != nil && k.Value == d.Value {
					c := &fnCtx{r: r, m: m, file: f.Name}
					c.exprExpect(r.typeScope(m, nil), d.Value, k.Type)
				}
			case *ast.FuncDecl:
				if fd := r.prog.Info.Funcs[d]; fd != nil && d.Body != nil {
					r.checkFunc(m, f.Name, fd, nil)
				}
			case *ast.ImplDecl:
				for _, fn := range d.Funcs {
					if fd := r.prog.Info.Funcs[fn]; fd != nil && fn.Body != nil {
						r.checkFunc(m, f.Name, fd, nil)
					}
				}
				if d.Destructor != nil {
					r.checkDestructor(m, f.Name, d)
				}
			}
		}
	}
}

func (r *resolver) checkDestructor(m *Module, file string, d *ast.ImplDecl) {
	sc := r.typeScope(m, nil)
	target := r.resolveType(m, file, sc, d.Target)
	fd := &FuncDecl{Name: "destructor", Module: m.Name, Pos: d.Destructor.Pos, Params: []*ParamDecl{{Name: "self", Conv: ast.ConvEdit, Type: target, Self: true, Pos: d.Destructor.Pos}}, Type: &Func{}}
	if n, ok := target.(*Named); ok {
		fd.Receiver = n.Decl
		for _, tp := range n.Decl.TypeParams {
			sc.vars[tp.Name] = &Symbol{Kind: SymTypeParam, Name: tp.Name, TParam: tp}
		}
	}
	c := &fnCtx{r: r, m: m, file: file, fd: fd, inDestructor: true, dtor: d.Destructor}
	bodyScope := sc.child()
	c.declareParams(bodyScope)
	c.block(bodyScope, d.Destructor.Body)
}

func (r *resolver) checkFunc(m *Module, file string, fd *FuncDecl, _ *scope) {
	sc := r.typeScope(m, fd.OwnerParams)
	for _, tp := range fd.TypeParams {
		sc.vars[tp.Name] = &Symbol{Kind: SymTypeParam, Name: tp.Name, TParam: tp}
	}
	c := &fnCtx{r: r, m: m, file: file, fd: fd}
	bodyScope := sc.child()
	c.declareParams(bodyScope)
	c.block(bodyScope, fd.AST.Body)
}

func (c *fnCtx) declareParams(sc *scope) {
	for _, p := range c.fd.Params {
		sym := &Symbol{Kind: SymParam, Name: p.Name, Type: p.Type, Conv: p.Conv, Pos: p.Pos, ID: c.newID(), File: c.file}
		if a, ok := p.Type.(*Access); ok {
			sym.Type = a.Elem
			if a.Seq {
				sym.Type = &Slice{Elem: a.Elem}
			}
		}
		if p.Conv == ast.ConvValue && !IsCopy(p.Type) {
			if _, isTP := p.Type.(*TypeParamType); !isTP || !hasMarker(p.Type.(*TypeParamType), "Copy") {
				c.r.errorf("PARAM_OWN_REQUIRED", c.file, p.Pos, "parameter %s has move-only type %s and must be declared own", p.Name, p.Type)
			}
		}
		sc.vars[p.Name] = sym
		c.r.prog.Info.Decls[p] = sym
		if p.Self && c.inDestructor {
			c.r.prog.Info.Decls[c.dtor] = sym
		}
	}
}

func (c *fnCtx) newID() int { c.nextID++; return c.nextID }

func (c *fnCtx) errorf(code diag.Code, pos ast.Pos, format string, args ...any) {
	c.r.errorf(code, c.file, pos, format, args...)
}

func (c *fnCtx) declare(sc *scope, node any, name string, kind SymbolKind, t Type, conv ast.Conv, pos ast.Pos) *Symbol {
	if name == "_" {
		return &Symbol{Kind: kind, Name: name, Type: t, Conv: conv, Pos: pos, ID: c.newID()}
	}
	if prev := sc.lookup(name); prev != nil && prev.Kind == SymParam {
		c.errorf("NAME_SHADOWS_PARAM", pos, "%s shadows a parameter", name)
	}
	if _, dup := sc.vars[name]; dup {
		c.errorf("NAME_DUPLICATE", pos, "%s is already declared in this scope", name)
	}
	sym := &Symbol{Kind: kind, Name: name, Type: t, Conv: conv, Pos: pos, ID: c.newID(), File: c.file}
	sc.vars[name] = sym
	if node != nil {
		c.r.prog.Info.Decls[node] = sym
	}
	return sym
}

// ---- statements ----

func (c *fnCtx) block(sc *scope, b *ast.Block) {
	inner := sc.child()
	for _, s := range b.Stmts {
		c.stmt(inner, s)
	}
}

func (c *fnCtx) stmt(sc *scope, s ast.Stmt) {
	switch s := s.(type) {
	case *ast.VarDecl:
		t := c.r.resolveType(c.m, c.file, sc, s.Type)
		if _, isAcc := t.(*Access); isAcc {
			c.errorf("ACC_UNBOUND_PROJECTION", s.Pos, "a local cannot have an access type; use with/match/for")
		}
		if s.Init != nil {
			c.exprExpect(sc, s.Init, t)
		}
		c.declare(sc, s, s.Name, SymLocal, t, ast.ConvValue, s.Pos)
	case *ast.ShortDecl:
		t := c.expr(sc, s.Value, nil)
		switch tt := t.(type) {
		case *Access:
			if IsCopy(accessElem(tt)) && !tt.Seq {
				t = accessElem(tt) // a read of a Copy value through a projection
			} else {
				c.errorf("ACC_UNBOUND_PROJECTION", s.Pos, "projection bound with :=; use with/match/for")
			}
		case *Tuple:
			c.errorf("ACC_UNBOUND_PROJECTION", s.Pos, "projection bound with :=; use with/match/for")
		}
		c.declare(sc, s, s.Name, SymLocal, t, ast.ConvValue, s.Pos)
	case *ast.Assign:
		tt := c.expr(sc, s.Target, nil)
		if acc, isAcc := tt.(*Access); isAcc {
			if !acc.Edit {
				c.errorf("ASSIGN_IMMUTABLE", s.Target.Span(), "assignment through a view projection")
			}
			tt = accessElem(acc)
		} else if !c.isPlace(sc, s.Target) {
			c.errorf("ASSIGN_IMMUTABLE", s.Target.Span(), "assignment target is not a place")
		} else if !c.placeIsMutable(sc, s.Target) {
			c.errorf("ASSIGN_IMMUTABLE", s.Target.Span(), "assignment through a read-only place")
		}
		c.exprExpect(sc, s.Value, tt)
	case *ast.With:
		c.with(sc, s)
	case *ast.Match:
		c.match(sc, s)
	case *ast.For:
		c.forStmt(sc, s)
	case *ast.While:
		c.exprExpect(sc, s.Cond, &Prim{Name: "bool"})
		c.block(sc, s.Body)
	case *ast.If:
		c.exprExpect(sc, s.Cond, &Prim{Name: "bool"})
		c.block(sc, s.Then)
		switch e := s.Else.(type) {
		case *ast.If:
			c.stmt(sc, e)
		case *ast.Block:
			c.block(sc, e)
		}
	case *ast.Return:
		c.ret(sc, s)
	case *ast.Break, *ast.Continue:
	case *ast.Unsafe:
		c.unsafe++
		c.block(sc, s.Body)
		c.unsafe--
	case *ast.Block:
		c.block(sc, s)
	case *ast.ExprStmt:
		c.expr(sc, s.X, nil)
	default:
		c.errorf("SYNTAX", s.Span(), "unsupported statement")
	}
}

func (c *fnCtx) ret(sc *scope, s *ast.Return) {
	ret := c.fd.Ret
	if c.inDestructor && s.Value != nil {
		c.errorf("TYPE_MISMATCH", s.Pos, "destructor must not return a value")
		return
	}
	if len(ret) == 0 {
		if s.Value != nil {
			if _, ok := s.Value.(*ast.UnitLit); !ok {
				c.errorf("TYPE_MISMATCH", s.Value.Span(), "function returns nothing")
			}
		}
		return
	}
	if s.Value == nil {
		c.errorf("TYPE_MISMATCH", s.Pos, "function must return %s", c.fd.Type)
		return
	}
	if len(ret) == 1 {
		c.exprExpect(sc, s.Value, ret[0])
		return
	}
	t := c.expr(sc, s.Value, &Tuple{Elems: ret})
	if !Identical(t, &Tuple{Elems: ret}) {
		c.errorf("TYPE_MISMATCH", s.Value.Span(), "expected %s, found %s", (&Tuple{ret}).String(), typeString(t))
	}
}

func (c *fnCtx) with(sc *scope, s *ast.With) {
	t := c.expr(sc, s.Subject, nil)
	inner := sc.child()
	switch st := t.(type) {
	case *Access:
		if len(s.Names) != 1 {
			c.errorf("TYPE_MISMATCH", s.Pos, "with binds %d names but the subject yields one access", len(s.Names))
		}
		conv := ast.ConvView
		if st.Edit {
			conv = ast.ConvEdit
		}
		c.declare(inner, s, s.Names[0], SymAccess, accessElem(st), conv, s.NamePos[0])
	case *Tuple:
		if len(s.Names) != len(st.Elems) {
			c.errorf("TYPE_MISMATCH", s.Pos, "with binds %d names but the subject yields %d accesses", len(s.Names), len(st.Elems))
		}
		for i, n := range s.Names {
			if i >= len(st.Elems) {
				break
			}
			a, ok := st.Elems[i].(*Access)
			if !ok {
				c.errorf("TYPE_MISMATCH", s.Subject.Span(), "tuple element %d is not an access", i)
				continue
			}
			conv := ast.ConvView
			if a.Edit {
				conv = ast.ConvEdit
			}
			sym := c.declare(inner, nil, n, SymAccess, accessElem(a), conv, s.NamePos[i])
			c.r.prog.Info.Decls[&s.NamePos[i]] = sym
		}
	default:
		if IsCopy(t) || c.isPlace(sc, s.Subject) && !isTemporary(s.Subject) {
			c.errorf("TYPE_MISMATCH", s.Subject.Span(), "with subject must be a projection or an owned move-only value")
		}
		if len(s.Names) != 1 {
			c.errorf("TYPE_MISMATCH", s.Pos, "with binds %d names but the subject is one owned value", len(s.Names))
		}
		c.declare(inner, s, s.Names[0], SymAccess, t, ast.ConvEdit, s.NamePos[0])
	}
	c.block(inner, s.Body)
}

func accessElem(a *Access) Type {
	if a.Seq {
		return &Slice{Elem: a.Elem}
	}
	return a.Elem
}

func isTemporary(e ast.Expr) bool {
	switch e.(type) {
	case *ast.CallExpr, *ast.StructLit, *ast.Move:
		return true
	}
	return false
}

func (c *fnCtx) match(sc *scope, s *ast.Match) {
	var subjT Type
	owned := false
	edit := false
	if s.Move {
		subjT = c.expr(sc, s.Subject, nil)
		owned = true
	} else {
		subjT = c.expr(sc, s.Subject, nil)
		if a, ok := subjT.(*Access); ok {
			subjT = accessElem(a)
			edit = a.Edit
		} else if isTemporary(s.Subject) {
			owned = true
		} else if sym := c.placeRoot(sc, s.Subject); sym != nil && sym.Kind == SymAccess {
			edit = sym.Conv == ast.ConvEdit
		} else if sym != nil && sym.Kind == SymParam && sym.Conv == ast.ConvEdit {
			edit = true
		} else if sym != nil && (sym.Kind == SymLocal || sym.Conv == ast.ConvOwn || sym.Conv == ast.ConvOut) {
			edit = true
		}
	}
	n, ok := subjT.(*Named)
	if !ok || n.Decl.Kind != KindUnion {
		c.errorf("TYPE_MISMATCH", s.Subject.Span(), "match subject must be a union, found %s", typeString(subjT))
		for _, arm := range s.Arms {
			c.block(sc, arm.Body)
		}
		return
	}
	if owned && HasDestructor(subjT) {
		c.errorf("OWN_DESTRUCTURE_DESTRUCTOR", s.Subject.Span(), "owned match subject of type %s has a destructor", subjT)
	}
	covered := map[string]bool{}
	wild := false
	for _, arm := range s.Arms {
		inner := sc.child()
		if arm.Wild {
			wild = true
			c.block(inner, arm.Body)
			continue
		}
		if arm.Qual != nil && arm.Qual.Name != n.Decl.Name {
			c.errorf("TYPE_MISMATCH", arm.Pos, "pattern names %s but the subject is %s", arm.Qual.Name, n.Decl.Name)
		}
		var vd *VariantDecl
		for _, v := range n.Decl.Variants {
			if v.Name == arm.Variant {
				vd = v
			}
		}
		if vd == nil {
			c.errorf("NAME_UNRESOLVED", arm.Pos, "%s has no variant %s", n.Decl.Name, arm.Variant)
			c.block(inner, arm.Body)
			continue
		}
		c.r.prog.Info.Variants[arm] = vd
		covered[vd.Name] = true
		fts := VariantFieldTypes(n, vd)
		if len(arm.Binds) != len(fts) {
			c.errorf("CALL_ARITY", arm.Pos, "variant %s has %d fields, pattern binds %d", vd.Name, len(fts), len(arm.Binds))
		}
		for i, b := range arm.Binds {
			if i >= len(fts) {
				break
			}
			if owned || IsCopy(fts[i]) {
				sym := c.declare(inner, nil, b, SymLocal, fts[i], ast.ConvValue, arm.BindPos[i])
				c.r.prog.Info.Decls[&arm.BindPos[i]] = sym
				continue
			}
			conv := ast.ConvView
			if edit {
				conv = ast.ConvEdit
			}
			sym := c.declare(inner, nil, b, SymAccess, fts[i], conv, arm.BindPos[i])
			c.r.prog.Info.Decls[&arm.BindPos[i]] = sym
		}
		c.block(inner, arm.Body)
	}
	if !wild {
		for _, v := range n.Decl.Variants {
			if !covered[v.Name] {
				c.errorf("MATCH_NOT_EXHAUSTIVE", s.Pos, "variant %s is not covered", v.Name)
				break
			}
		}
	}
}

func (c *fnCtx) forStmt(sc *scope, s *ast.For) {
	inner := sc.child()
	if rg, ok := s.Iter.(*ast.RangeExpr); ok {
		lt := c.expr(sc, rg.Lo, nil)
		if !IsInteger(lt) {
			lt = &Prim{Name: "usize"}
			c.exprExpect(sc, rg.Lo, lt)
		}
		c.exprExpect(sc, rg.Hi, lt)
		c.r.prog.Info.Types[rg] = lt
		c.declare(inner, s, s.Var, SymLocal, lt, ast.ConvValue, s.VarPos)
		c.block(inner, s.Body)
		return
	}
	t := c.expr(sc, s.Iter, nil)
	var elem Type
	edit := false
	switch it := t.(type) {
	case *Access:
		if sl, ok := it.Elem.(*Slice); ok && !it.Seq {
			elem = sl.Elem
		} else if arr, ok := it.Elem.(*Array); ok && !it.Seq {
			elem = arr.Elem
		} else if it.Seq {
			elem = it.Elem
		}
		edit = it.Edit
	case *Array:
		elem = it.Elem
		edit = c.placeIsMutable(sc, s.Iter)
	case *Slice:
		elem = it.Elem
		edit = c.placeIsMutable(sc, s.Iter)
	}
	if elem == nil {
		c.errorf("TYPE_MISMATCH", s.Iter.Span(), "cannot iterate over %s", typeString(t))
		c.block(inner, s.Body)
		return
	}
	if IsCopy(elem) {
		c.declare(inner, s, s.Var, SymLocal, elem, ast.ConvValue, s.VarPos)
	} else {
		conv := ast.ConvView
		if edit {
			conv = ast.ConvEdit
		}
		c.declare(inner, s, s.Var, SymAccess, elem, conv, s.VarPos)
	}
	c.block(inner, s.Body)
}

// placeIsMutable reports whether a place is reachable for writing: rooted in
// an owned local, an own/out parameter, or an edit binding.
func (c *fnCtx) placeIsMutable(sc *scope, e ast.Expr) bool {
	sym := c.placeRoot(sc, e)
	if sym == nil {
		return false
	}
	switch sym.Kind {
	case SymLocal, SymGlobal:
		return true
	case SymParam:
		return sym.Conv == ast.ConvEdit || sym.Conv == ast.ConvOwn || sym.Conv == ast.ConvOut
	case SymAccess:
		return sym.Conv == ast.ConvEdit
	}
	return false
}

// placeRoot returns the root symbol of a place expression, or nil.
func (c *fnCtx) placeRoot(sc *scope, e ast.Expr) *Symbol {
	switch x := e.(type) {
	case *ast.Ident:
		sym := sc.lookup(x.Name)
		if sym != nil && (sym.Kind == SymLocal || sym.Kind == SymParam || sym.Kind == SymAccess) {
			return sym
		}
		if sym == nil {
			if g, ok := c.m.Globals[x.Name]; ok {
				return c.globalSym(g)
			}
		}
	case *ast.FieldExpr:
		return c.placeRoot(sc, x.X)
	case *ast.IndexExpr:
		return c.placeRoot(sc, x.X)
	case *ast.SliceExpr:
		return c.placeRoot(sc, x.X)
	case *ast.Paren:
		return c.placeRoot(sc, x.X)
	}
	return nil
}

func (c *fnCtx) isPlace(sc *scope, e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident, *ast.FieldExpr, *ast.IndexExpr, *ast.SliceExpr:
		return c.placeRoot(sc, e) != nil
	case *ast.Paren:
		return c.isPlace(sc, x.X)
	case *ast.CallExpr:
		if _, ok := c.r.prog.Info.Types[e].(*Access); ok {
			return true
		}
	}
	return false
}
