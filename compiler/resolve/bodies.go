package resolve

import (
	"fmt"
	"strings"

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
	// cursorCopies holds, for the statement a cursor loop's desugaring
	// generates to copy a Copy element out of the getter's access, what the
	// variable it declares is to be marked with (specs/for-bindings).
	cursorCopies map[ast.Stmt]cursorCopy
}

// cursorCopy says of a cursor loop's Copy variable whether the loop is
// editable for it, and which Copy binding the loop iterates a path of, if
// any.
type cursorCopy struct {
	edit bool
	root *Symbol
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
	// `impl Box[T]` introduces T implicitly: bind the target's arguments to
	// the type's own parameters before resolving it (a generic type's
	// destructor). A repeated or non-parameter argument (impl Pair[T, T])
	// was already rejected when the impl was collected (TYPE_MISMATCH), so
	// a name is bound at most once here.
	if tn, ok := d.Target.(*ast.TypeName); ok && tn.Pkg == "" {
		if td, ok := m.Types[tn.Name]; ok {
			for i, a := range tn.Args {
				if an, ok := a.(*ast.TypeName); ok && len(an.Args) == 0 && an.Pkg == "" && i < len(td.TypeParams) {
					if _, isType := m.Types[an.Name]; !isType && sc.lookup(an.Name) == nil {
						sc.vars[an.Name] = &Symbol{Kind: SymTypeParam, Name: an.Name, TParam: td.TypeParams[i]}
					}
				}
			}
		}
	}
	target := r.resolveType(m, file, sc, d.Target)
	fd := &FuncDecl{Name: "destructor", Module: m.Name, Pos: d.Destructor.Pos, Params: []*ParamDecl{{Name: "self", Conv: ast.ConvEdit, Type: target, Self: true, Pos: d.Destructor.Pos}}, Type: &Func{}}
	if n, ok := target.(*Named); ok {
		fd.Receiver = n.Decl
		for _, tp := range n.Decl.TypeParams {
			if sc.lookup(tp.Name) == nil { // the target's own names (impl Pair[B, A]) take precedence
				sc.vars[tp.Name] = &Symbol{Kind: SymTypeParam, Name: tp.Name, TParam: tp}
			}
		}
	}
	c := &fnCtx{r: r, m: m, file: file, fd: fd, inDestructor: true, dtor: d.Destructor}
	bodyScope := sc.child()
	c.declareParams(bodyScope)
	c.block(bodyScope, d.Destructor.Body)
}

func (r *resolver) checkFunc(m *Module, file string, fd *FuncDecl, _ *scope) {
	sc := r.typeScope(m, fd.OwnerParams)
	for _, tp := range fd.bodyParams {
		sc.vars[tp.Name] = &Symbol{Kind: SymTypeParam, Name: tp.Name, TParam: tp} // the where-clause bounds
	}
	for _, tp := range fd.TypeParams {
		sc.vars[tp.Name] = &Symbol{Kind: SymTypeParam, Name: tp.Name, TParam: tp}
	}
	c := &fnCtx{r: r, m: m, file: file, fd: fd}
	bodyScope := sc.child()
	c.declareParams(bodyScope)
	c.block(bodyScope, fd.AST.Body)
}

func (c *fnCtx) declareParams(sc *scope) {
	for i, p := range c.fd.Params {
		sym := &Symbol{Kind: SymParam, Name: p.Name, Type: p.Type, Conv: p.Conv, Pos: p.Pos, ID: c.newID(), File: c.file}
		if a, ok := p.Type.(*Access); ok {
			sym.Type = a.Elem
			if a.Seq {
				sym.Type = &Slice{Elem: a.Elem}
			}
		}
		if ft, isClosure := p.Type.(*Func); p.Conv == ast.ConvValue && !IsCopy(p.Type) && (!isClosure || !ft.Closure) {
			if _, isTP := p.Type.(*TypeParamType); !isTP || !hasMarker(p.Type.(*TypeParamType), "Copy") {
				c.r.errorf("PARAM_OWN_REQUIRED", c.file, p.Pos, "parameter %s has move-only type %s and must be declared own", p.Name, p.Type)
				if a := c.fd.AST; a != nil && i < len(a.Params) && a.Params[i].Type != nil && a.Params[i].Name == p.Name {
					c.fix(diag.InsertAt(c.file, a.Params[i].Type.Span().Start, "own "))
				}
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

// fix attaches a machine-applicable edit to the diagnostic just reported.
func (c *fnCtx) fix(ops ...diag.StructuredEditOp) { c.r.fix(ops...) }

func span(p ast.Pos) diag.Span { return diag.Span{Start: p.Start, End: p.End} }

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
		if ContainsClosure(t) {
			c.errorf("ACC_UNBOUND_PROJECTION", s.Pos, "a local cannot have a closure type; a closure is passed as an argument")
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
		case *Func:
			if tt.Closure {
				c.errorf("ACC_UNBOUND_PROJECTION", s.Pos, "closure bound with :=; a closure is passed as an argument")
			}
		}
		sym := c.declare(sc, s, s.Name, SymLocal, t, ast.ConvValue, s.Pos)
		if cc, ok := c.cursorCopies[s]; ok {
			// a cursor loop's Copy variable: a copy unless the body uses it
			// as an edit (specs/for-bindings F1, F2, F6)
			sym.CopyBound, sym.CopyEdit, sym.Derived = true, cc.edit, cc.root
		}
	case *ast.Assign:
		c.demandEdit(sc, s.Target)
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
		if a, ok := ret[0].(*Access); ok {
			// returning a place as an access (Rule 5.3.4 elision): the
			// place's type must be the access's element type
			t := c.expr(sc, s.Value, nil)
			if _, isAcc := t.(*Access); !isAcc && c.isPlace(sc, s.Value) && Identical(valueOf(t), accessElem(a)) {
				if a.Edit {
					c.demandEdit(sc, s.Value)
				}
				if a.Edit && !c.placeIsMutable(sc, s.Value) {
					c.errorf("ACC_PERMISSION_ESCALATION", s.Value.Span(), "returning an edit of a read-only place")
				}
				return
			}
			if !c.assignable(t, ret[0], s.Value) {
				c.errorf("TYPE_MISMATCH", s.Value.Span(), "expected %s, found %s", typeString(ret[0]), typeString(t))
			}
			return
		}
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
		sym := c.declare(inner, s, s.Names[0], SymAccess, accessElem(st), conv, s.NamePos[0])
		// a binding of a path of a match arm's Copy binding (a slice of it)
		// is derived from it: an edit use of the one is an edit use of the
		// other (specs/match-bindings §1, item 6)
		if root := c.placeRoot(sc, s.Subject); root != nil && root.copyBound() {
			sym.Derived = root
		}
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

// isTemporary reports an expression whose value nothing else names: a
// call's result, a literal, a moved variable, what take and replace move
// out of a place, and the Ok value that `?` unwraps from one of these.
func isTemporary(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.CallExpr, *ast.StructLit, *ast.Move, *ast.Take, *ast.Replace:
		return true
	case *ast.Try:
		return isTemporary(x.X)
	}
	return false
}

func (c *fnCtx) match(sc *scope, s *ast.Match) {
	var subjT Type
	owned := false
	edit := false
	// root is the binding the subject is a path of, when that is a match
	// arm's Copy binding or derives from one: this match's own Copy
	// bindings are then derived from it (specs/match-bindings §1, item 6)
	var root *Symbol
	if s.Move {
		subjT = c.expr(sc, s.Subject, nil)
		owned = true
	} else {
		subjT = c.expr(sc, s.Subject, nil)
		if sym := c.placeRoot(sc, s.Subject); sym != nil && sym.copyBound() {
			root = sym
		}
		if a, ok := subjT.(*Access); ok {
			subjT = accessElem(a)
			edit = a.Edit
		} else if isTemporary(s.Subject) {
			owned = true
		} else if sym := c.placeRoot(sc, s.Subject); sym != nil && sym.CopyBound {
			// a Copy binding not yet used as an edit: a write through this
			// match would make it one, which the outer subject must allow
			edit = sym.CopyEdit
		} else if sym != nil && sym.Kind == SymAccess {
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
	if owned && n.Decl.Destructor != nil {
		// rule 5.3.1: a type that declares a destructor is destroyed whole,
		// so its payload must not be moved out from under it. A payload's
		// own destructor is no obstacle: a bound payload moves into the
		// arm's local, which destroys it, and an unbound one is destroyed
		// on entering the arm. (Until 2026-11-09 any destructor anywhere in
		// the subject was refused, which ruled out `match ch.recv()` and
		// every Option or Result of a container.)
		c.errorf("OWN_DESTRUCTURE_DESTRUCTOR", s.Subject.Span(), "owned match subject of type %s declares a destructor", subjT)
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
				if !owned {
					// a Copy binding of a place match: a copy unless the arm
					// uses it as an edit (specs/match-bindings M1, M2)
					sym.CopyBound, sym.CopyEdit, sym.Derived = true, edit, root
				}
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
		if s.Var2 != "" {
			c.errorf("TYPE_MISMATCH", s.Var2Pos, "a for with two names iterates an array, a sequence or a type with the cursor protocol, and a range is none (its values are the indexes)")
			c.declare(inner, &s.Var2Pos, s.Var2, SymLocal, &Bad{}, ast.ConvValue, s.Var2Pos)
		}
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
	if c.cursorFor(sc, s, t) {
		return
	}
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
		if s.Var2 != "" {
			c.errorf("TYPE_MISMATCH", s.Var2Pos, "a for with two names iterates an array, a sequence (index and element) or a type with the cursor protocol (cursor_first, cursor_next, cursor_end, cursor_entry), and %s is none", typeString(t))
			c.declare(inner, &s.Var2Pos, s.Var2, SymLocal, &Bad{}, ast.ConvValue, s.Var2Pos)
		} else {
			c.errorf("TYPE_MISMATCH", s.Iter.Span(), "cannot iterate over %s", typeString(t))
		}
		// the body still sees its names, so it reports nothing more about them
		c.declare(inner, s, s.Var, SymLocal, &Bad{}, ast.ConvValue, s.VarPos)
		c.block(inner, s.Body)
		return
	}
	// `for i, x in xs`: the first name is the index, a usize, and the
	// second the element (spec/types.md §7)
	name, pos := s.Var, s.VarPos
	if s.Var2 != "" {
		c.declare(inner, s.IndexKey(), s.Var, SymLocal, &Prim{Name: "usize"}, ast.ConvValue, s.VarPos)
		name, pos = s.Var2, s.Var2Pos
	}
	if IsCopy(elem) {
		// a copy of the element unless the body uses it as an edit, as a
		// match arm's Copy binding is (specs/for-bindings F1, F2); over a
		// path of such a binding it is derived from it (F6)
		sym := c.declare(inner, s, name, SymLocal, elem, ast.ConvValue, pos)
		sym.CopyBound, sym.CopyEdit = true, edit
		if root := c.placeRoot(sc, s.Iter); root != nil && root.copyBound() {
			sym.Derived = root
		}
	} else {
		conv := ast.ConvView
		if edit {
			conv = ast.ConvEdit
		}
		c.declare(inner, s, name, SymAccess, elem, conv, pos)
	}
	c.block(inner, s.Body)
}

// cursorFor desugars a for over a type with the cursor protocol
// (spec/types.md §7) into a while loop over positions, and resolves that
// loop, which the checker and the lowering then run in the for's place:
//
//	var #at: usize = it.cursor_first()
//	while #at < it.cursor_end() {
//	    #cur := #at
//	    #at = it.cursor_next(#cur)
//	    with x := it.cursor_get(#cur) { body }        // one name
//	    with k, v := it.cursor_entry(#cur) { body }   // two names
//	}
//
// The position advances before the body, so continue needs no step block.
// The getter is the _mut one (cursor_get_mut, cursor_entry_mut) when the
// iterated place is mutable and the element (the value, for two names) is
// not Copy. A Copy element is bound as a copy, as a for over a sequence
// binds it. Two names come from one call, a tuple of accesses, because
// while a binding is live its root may be accessed only through it
// (spec §5.4). An iterated expression that is not a place is bound once
// with `with #it := ...`: anything but a name or a field path, which the
// loop may evaluate again (a call is evaluated once, and so is an index,
// which the body may change). It reports false, doing nothing, for a type
// without the protocol.
func (c *fnCtx) cursorFor(sc *scope, s *ast.For, t Type) bool {
	nt, ok := valueOf(t).(*Named)
	if !ok {
		return false
	}
	method := func(name string) *FuncDecl { return nt.Decl.Methods[name] }
	if method("cursor_first") == nil || method("cursor_next") == nil || method("cursor_end") == nil {
		return false
	}
	getter, want := "cursor_get", 1
	if s.Var2 != "" {
		getter, want = "cursor_entry", 2
	}
	fail := func(format string, args ...any) bool {
		c.errorf("TYPE_MISMATCH", s.Iter.Span(), format, args...)
		inner := sc.child()
		c.declare(inner, s, s.Var, SymLocal, &Bad{}, ast.ConvValue, s.VarPos)
		if s.Var2 != "" {
			c.declare(inner, &s.Var2Pos, s.Var2, SymLocal, &Bad{}, ast.ConvValue, s.Var2Pos)
		}
		c.block(inner, s.Body)
		return true
	}
	if method(getter) == nil {
		return fail("%s has the cursor protocol but no %s, which a for with %d name(s) needs", typeString(t), getter, want)
	}
	subst := map[string]Type{}
	for i, tp := range nt.Decl.TypeParams {
		if i < len(nt.Args) {
			subst[tp.Name] = nt.Args[i]
		}
	}
	var elems []Type
	for _, r := range method(getter).Ret {
		a, isAcc := Subst(r, subst).(*Access)
		if !isAcc {
			elems = nil
			break
		}
		elems = append(elems, accessElem(a))
	}
	if len(elems) != want {
		return fail("%s.%s must return %d access(es)", typeString(t), getter, want)
	}
	mutable := c.placeIsMutable(sc, s.Iter)
	if a, isAcc := t.(*Access); isAcc {
		mutable = a.Edit
	}
	// editable[i]: the loop gives the i-th name an edit where its element is
	// not Copy, which is what a Copy variable the body writes then gets
	// (specs/for-bindings §1): the place is mutable, the type has the _mut
	// getter, and that getter returns an edit of the same element type for
	// the name
	editable := make([]bool, want)
	if mm := method(getter + "_mut"); mutable && mm != nil && len(mm.Ret) == want {
		for i, r := range mm.Ret {
			if a, isAcc := Subst(r, subst).(*Access); isAcc && a.Edit && Identical(accessElem(a), elems[i]) {
				editable[i] = true
			}
		}
	}
	if mutable && !IsCopy(elems[want-1]) && method(getter+"_mut") != nil {
		getter += "_mut"
	}
	var iterRoot *Symbol
	if root := c.placeRoot(sc, s.Iter); root != nil && root.copyBound() {
		iterRoot = root
	}
	ip, sp := s.Iter.Span(), s.Pos
	it := s.Iter
	wrap := !FieldPath(s.Iter)
	if wrap {
		it = &ast.Ident{Pos: ip, Name: "#it"}
	}
	id := func(name string) *ast.Ident { return &ast.Ident{Pos: sp, Name: name} }
	call := func(name string, args ...ast.Expr) *ast.CallExpr {
		x := &ast.CallExpr{Pos: ip, Fn: &ast.FieldExpr{Pos: ip, X: it, Name: name}}
		for _, a := range args {
			x.Args = append(x.Args, &ast.Arg{Pos: ip, X: a})
		}
		c.r.prog.Info.CursorCalls[x] = true
		return x
	}
	names := []string{s.Var, s.Var2}[:want]
	poses := []ast.Pos{s.VarPos, s.Var2Pos}[:want]
	var copies []ast.Stmt
	copyOf := make([]*ast.ShortDecl, want) // the copy statement of each Copy name
	withNames := make([]string, want)
	for i := range names {
		withNames[i] = names[i]
		if IsCopy(elems[i]) {
			// a Copy element is bound as a copy of the getter's access,
			// unless the body turns out to use it as an edit (below)
			withNames[i] = "#e" + string(rune('0'+i))
			copyOf[i] = &ast.ShortDecl{Pos: poses[i], Name: names[i], Value: &ast.Ident{Pos: poses[i], Name: withNames[i]}}
			copies = append(copies, copyOf[i])
			if c.cursorCopies == nil {
				c.cursorCopies = map[ast.Stmt]cursorCopy{}
			}
			c.cursorCopies[copyOf[i]] = cursorCopy{edit: editable[i], root: iterRoot}
		}
	}
	get := &ast.With{Pos: sp, Names: withNames, NamePos: poses, Subject: call(getter, id("#cur")), Body: &ast.Block{Pos: s.Body.Pos, Stmts: append(copies, s.Body)}}
	if !strings.HasSuffix(getter, "_mut") {
		c.r.prog.Info.CursorViews[get] = true
	}
	loop := &ast.Block{Pos: sp, Stmts: []ast.Stmt{
		&ast.ShortDecl{Pos: sp, Name: "#cur", Value: id("#at")},
		&ast.Assign{Pos: sp, Target: id("#at"), Value: call("cursor_next", id("#cur"))},
		get,
	}}
	blk := &ast.Block{Pos: sp, Stmts: []ast.Stmt{
		&ast.VarDecl{Pos: sp, Name: "#at", Type: &ast.PrimType{Pos: sp, Name: "usize"}, Init: call("cursor_first")},
		&ast.While{Pos: sp, Cond: &ast.Binary{Pos: sp, Op: "<", L: id("#at"), R: call("cursor_end")}, Body: loop},
	}}
	if wrap {
		blk = &ast.Block{Pos: sp, Stmts: []ast.Stmt{&ast.With{Pos: sp, Names: []string{"#it"}, NamePos: []ast.Pos{ip}, Subject: s.Iter, Body: blk}}}
	}
	s.SetCursor(blk)
	c.block(sc, blk)
	c.rebindCursor(sc, get, copyOf, names, getter, it, wrap)
	return true
}

// rebindCursor finishes a cursor loop whose body has been resolved. A Copy
// variable that the body used as an edit, in a loop that is editable for
// it, is by now an access binding (promote), but the loop was desugared to
// copy it out of the view getter's access. The loop is rebuilt in place as
// the _mut loop it would have been for an element that is not Copy
// (specs/for-bindings F7): the copy statement goes, the with binds the
// variable's own symbol, and the getter call becomes the _mut one.
//
// What holds afterwards, which the checker and the lowering rely on:
// Info.Decls has no entry for a removed copy statement, and the with's
// entry for each name is the symbol bound to the getter's access for it,
// with that access's permission; Info.Calls and Info.Types of the getter
// call are those of the method it now names; the with is in
// Info.CursorViews only if it calls the view getter.
func (c *fnCtx) rebindCursor(sc *scope, get *ast.With, copyOf []*ast.ShortDecl, names []string, getter string, it ast.Expr, wrap bool) {
	info := c.r.prog.Info
	withSym := func(i int) *Symbol {
		if len(copyOf) == 1 {
			return info.Decls[get]
		}
		return info.Decls[&get.NamePos[i]]
	}
	rebuilt := false
	for i, cp := range copyOf {
		if cp == nil {
			continue
		}
		sym := info.Decls[cp]
		delete(c.cursorCopies, cp)
		if sym == nil || sym.EditUse == nil || !sym.CopyEdit {
			continue
		}
		for j, st := range get.Body.Stmts {
			if st == ast.Stmt(cp) {
				get.Body.Stmts = append(get.Body.Stmts[:j:j], get.Body.Stmts[j+1:]...)
				break
			}
		}
		delete(info.Decls, cp)
		get.Names[i] = names[i]
		if len(copyOf) == 1 {
			info.Decls[get] = sym
		} else {
			info.Decls[&get.NamePos[i]] = sym
		}
		rebuilt = true
	}
	if !rebuilt || strings.HasSuffix(getter, "_mut") {
		return
	}
	call := get.Subject.(*ast.CallExpr)
	call.Fn.(*ast.FieldExpr).Name = getter + "_mut"
	delete(info.CursorViews, get)
	// the call is resolved again where its generated names mean what they
	// meant the first time
	tmp := sc.child()
	if cur, ok := call.Args[0].X.(*ast.Ident); ok && info.Idents[cur] != nil {
		tmp.vars[cur.Name] = info.Idents[cur]
	}
	if id, ok := it.(*ast.Ident); wrap && ok && info.Idents[id] != nil {
		tmp.vars[id.Name] = info.Idents[id]
	}
	var accs []Type
	switch t := c.expr(tmp, call, nil).(type) {
	case *Access:
		accs = []Type{t}
	case *Tuple:
		accs = t.Elems
	}
	if len(accs) != len(copyOf) {
		return // the call did not resolve; that has been reported
	}
	// a name that was not rebound is bound to the _mut getter's access for
	// it now, and has that access's permission
	for i := range copyOf {
		ws := withSym(i)
		acc, isAcc := accs[i].(*Access)
		if !isAcc || ws == nil || ws.EditUse != nil {
			continue
		}
		ws.Conv = ast.ConvView
		if acc.Edit {
			ws.Conv = ast.ConvEdit
		}
	}
}

// FieldPath reports a name or a path of field accesses from a name.
func FieldPath(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return true
	case *ast.FieldExpr:
		return FieldPath(x.X)
	case *ast.Paren:
		return FieldPath(x.X)
	}
	return false
}

// placeIsMutable reports whether a place is reachable for writing: rooted in
// an owned local, an own/out parameter, or an edit binding.
// projectionBase returns the access type of the call projection a place
// path is rooted at (e.g. `p.get().words`), or nil.
func (c *fnCtx) projectionBase(e ast.Expr) *Access {
	switch x := e.(type) {
	case *ast.FieldExpr:
		return c.projectionBase(x.X)
	case *ast.IndexExpr:
		return c.projectionBase(x.X)
	case *ast.SliceExpr:
		return c.projectionBase(x.X)
	case *ast.Paren:
		return c.projectionBase(x.X)
	case *ast.CallExpr:
		acc, _ := c.r.prog.Info.Types[e].(*Access)
		return acc
	}
	return nil
}

func (c *fnCtx) placeIsMutable(sc *scope, e ast.Expr) bool {
	sym := c.placeRoot(sc, e)
	if sym == nil {
		// a path through a returned projection: its permission is the access's
		if acc := c.projectionBase(e); acc != nil {
			return acc.Edit
		}
		return false
	}
	switch sym.Kind {
	case SymLocal:
		// a match arm's Copy binding is written through to the subject, so
		// it is as writable as the subject is (specs/match-bindings M3). A
		// caller that requires the place to be writable calls demandEdit
		// first, which makes the binding the access it then is; a caller
		// that only asks (a slice's or a loop's permission) does not
		return !sym.CopyBound || sym.CopyEdit
	case SymGlobal:
		return true
	case SymParam:
		if sym.Conv == ast.ConvEdit || sym.Conv == ast.ConvOwn || sym.Conv == ast.ConvOut {
			return true
		}
		return sym.Conv == ast.ConvView && c.interiorWrite(sym.Type)
	case SymAccess:
		return sym.Conv == ast.ConvEdit || (sym.Conv == ast.ConvView && c.interiorWrite(sym.Type))
	}
	return false
}

// interiorWrite reports whether a write through a view of t is permitted
// here: t is an Interior type declared in this module and the write is
// inside unsafe (spec §6, the declaring module's internal synchronization).
func (c *fnCtx) interiorWrite(t Type) bool {
	n, ok := valueOf(t).(*Named)
	if !ok || n.Decl.Module != c.m.Name || !IsInterior(n) {
		return false
	}
	return c.unsafe > 0
}

// copyBound reports whether the symbol is, or was until its arm or loop
// body wrote it, a Copy binding (a match arm's, a for loop's), or is
// derived from one.
func (s *Symbol) copyBound() bool { return s.CopyBound || s.EditUse != nil || s.Derived != nil }

// demandEdit records that the place e is used as an edit: an assignment's
// target, an edit argument or receiver, the place of a take or a replace,
// a place returned as an edit. A match arm's Copy binding at the root of
// e, and each binding it derives from, is from then on an access binding
// for its whole arm (specs/match-bindings M2); a root of any other kind
// is untouched. Callers make their permission check after this call, so
// the check sees the binding as the view or the edit it now is.
func (c *fnCtx) demandEdit(sc *scope, e ast.Expr) {
	at := e.Span()
	for sym := c.placeRoot(sc, e); sym != nil; sym = sym.Derived {
		c.promote(sym, at)
		// the edit use that starts first in the source, in whatever order
		// the uses are met (M7's related span)
		if sym.EditUse != nil && at.Start < sym.EditUse.Start {
			use := at
			sym.EditUse = &use
		}
	}
}

// promote makes a Copy arm binding the access binding it is once the arm
// uses it as an edit: an edit of the payload field when the subject is
// editable, a view when it is not, in which case the caller's permission
// check refuses the use (M3). The uses of the name as an owned local met
// so far get the diagnostic an access binding gets (M4).
func (c *fnCtx) promote(sym *Symbol, at ast.Pos) {
	if !sym.CopyBound {
		return
	}
	sym.CopyBound = false
	sym.Kind = SymAccess
	sym.Conv = ast.ConvView
	if sym.CopyEdit {
		sym.Conv = ast.ConvEdit
	}
	sym.EditUse = &at
	for _, u := range sym.ownedUses {
		c.errorf(u.code, u.pos, "%s", u.msg)
	}
	sym.ownedUses = nil
}

// ownedUse notes a use of sym as an owned local (move, drop, an out
// argument) that is an error if sym, a Copy arm binding so far only read,
// is later used as an edit; promote reports it then.
func (c *fnCtx) ownedUse(sym *Symbol, code diag.Code, pos ast.Pos, format string, args ...any) {
	if sym.CopyBound {
		sym.ownedUses = append(sym.ownedUses, ownedUse{code: code, pos: pos, msg: fmt.Sprintf(format, args...)})
	}
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
	case *ast.Ident:
		return c.placeRoot(sc, e) != nil
	case *ast.FieldExpr:
		return c.placeRoot(sc, e) != nil || c.isPlace(sc, x.X)
	case *ast.IndexExpr:
		return c.placeRoot(sc, e) != nil || c.isPlace(sc, x.X)
	case *ast.SliceExpr:
		return c.placeRoot(sc, e) != nil || c.isPlace(sc, x.X)
	case *ast.Paren:
		return c.isPlace(sc, x.X)
	case *ast.CallExpr:
		if _, ok := c.r.prog.Info.Types[e].(*Access); ok {
			return true
		}
	}
	return false
}
