package check

import (
	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/resolve"
)

// result describes what evaluating an expression produced for the access
// checker: a place (for place expressions), a returned projection (isAccess),
// an owned temporary (owned), or a plain value.
type result struct {
	pl        place
	isAccess  bool    // a projection returned by a call (or a slice), live until consumed
	acc       *access // the live access record for a projection
	owned     bool    // an owned move-only temporary or `move x`
	ownedRoot *resolve.Symbol
	release   bool
	rawAccess bool // an access opened on raw memory (ptr_view and its kin): returnable from unsafe code
	t         resolve.Type
}

// placeOf returns the place denoted by a place expression (identifier, field,
// index, slice chains rooted in a local, parameter or binding), or nil.
// It does not record accesses; callers do. When read is true, uninitialized
// or moved roots are reported.
func (f *fn) placeOf(st *flow, e ast.Expr, read bool) *place {
	switch x := e.(type) {
	case *ast.Paren:
		return f.placeOf(st, x.X, read)
	case *ast.Ident:
		sym := f.c.info.Idents[x]
		if sym == nil {
			return nil
		}
		switch sym.Kind {
		case resolve.SymLocal, resolve.SymParam, resolve.SymAccess, resolve.SymGlobal:
		default:
			return nil
		}
		perm := ast.ConvEdit
		if sym.Kind == resolve.SymAccess && sym.Conv == ast.ConvView || sym.Kind == resolve.SymParam && sym.Conv == ast.ConvView {
			perm = ast.ConvView
		}
		if read {
			f.checkReadable(st, sym, x.Pos, nil)
		}
		return &place{root: sym, perm: perm}
	case *ast.FieldExpr:
		p := f.placeOf(st, x.X, false)
		if p == nil {
			return nil
		}
		np := *p
		np.path = append(append([]pathElem{}, p.path...), pathElem{kind: peField, name: x.Name})
		if read {
			f.checkReadable(st, np.root, x.Pos, np.path)
		}
		return &np
	case *ast.IndexExpr:
		p := f.placeOf(st, x.X, read)
		if p == nil || len(x.Args) != 1 {
			return nil
		}
		f.consumeValue(st, x.Args[0])
		el := pathElem{kind: peIndex}
		if v, ok := f.constInt(x.Args[0]); ok {
			el.isConst, el.idx = true, v
		}
		np := *p
		np.path = append(append([]pathElem{}, p.path...), el)
		return &np
	case *ast.SliceExpr:
		p := f.placeOf(st, x.X, read)
		if p == nil {
			return nil
		}
		el := pathElem{kind: peSlice, isConst: true}
		if x.Lo != nil {
			f.consumeValue(st, x.Lo)
			if v, ok := f.constInt(x.Lo); ok {
				el.lo = v
			} else {
				el.isConst = false
			}
		} else {
			el.loOpen = true
		}
		if x.Hi != nil {
			f.consumeValue(st, x.Hi)
			if v, ok := f.constInt(x.Hi); ok {
				el.hi = v
			} else {
				el.isConst = false
			}
		} else {
			el.hiOpen = true
		}
		np := *p
		np.path = append(append([]pathElem{}, p.path...), el)
		return &np
	}
	return nil
}

func (f *fn) constInt(e ast.Expr) (int64, bool) {
	if lit, ok := e.(*ast.IntLit); ok {
		v := int64(lit.Value)
		if lit.Neg {
			v = -v
		}
		return v, true
	}
	if p, ok := e.(*ast.Paren); ok {
		return f.constInt(p.X)
	}
	return 0, false
}

// checkReadable reports use of a moved or uninitialized variable.
func (f *fn) checkReadable(st *flow, sym *resolve.Symbol, pos ast.Pos, path []pathElem) {
	vs, ok := st.vars[sym]
	if !ok {
		return
	}
	switch vs.st {
	case stMoved:
		f.errorf("OWN_USE_AFTER_MOVE", pos, "%s is used after being moved", sym.Name)
	case stUninit:
		f.errorf("INIT_UNINITIALIZED_READ", pos, "%s is read before initialization", sym.Name)
	case stPartial:
		if len(path) >= 1 && path[0].kind == peField && vs.fields[path[0].name] {
			return
		}
		f.errorf("INIT_UNINITIALIZED_READ", pos, "%s is only partially initialized", sym.Name)
	}
}

func (f *fn) markMoved(st *flow, sym *resolve.Symbol, pos ast.Pos) {
	vs := st.get(sym)
	if vs.st == stMoved {
		// already reported by checkReadable at the read of the place
		return
	}
	if vs.st == stUninit {
		f.errorf("INIT_UNINITIALIZED_READ", pos, "%s is moved before initialization", sym.Name)
		return
	}
	vs.st = stMoved
	vs.movedAt = pos
}

// consumeValue evaluates e in value position: a move-only place is moved
// (consume), a Copy place is read (instantaneous view), a projection is read
// and closed.
func (f *fn) consumeValue(st *flow, e ast.Expr) *result {
	r := f.expr(st, e)
	if r == nil {
		return nil
	}
	if r.isAccess {
		// reading through a projection consumes it: its interval ends here
		f.closeAccess(r)
		return r
	}
	if r.pl.root != nil && !r.owned {
		if resolve.IsCopy(r.t) {
			t := f.tick()
			f.accs = append(f.accs, &access{pl: r.pl, kind: accView, start: t, end: t, pos: e.Span()})
			return r
		}
		f.consumePlace(st, r, e.Span())
	}
	return r
}

// consumePlace moves out of a place: only whole variables may be moved.
func (f *fn) consumePlace(st *flow, r *result, pos ast.Pos) {
	root := r.pl.root
	if len(r.pl.path) > 0 || root.Kind == resolve.SymAccess || (root.Kind == resolve.SymParam && (root.Conv == ast.ConvView || root.Conv == ast.ConvEdit)) {
		f.errorf("OWN_PARTIAL_MOVE", pos, "cannot move out of %s; use take or replace", describePlace(r.pl))
		return
	}
	t := f.tick()
	f.accs = append(f.accs, &access{pl: r.pl, kind: accConsume, start: t, end: t, pos: pos})
	f.markMoved(st, root, pos)
}

// closeAccess ends a projection's live interval at the current time.
func (f *fn) closeAccess(r *result) {
	if r.acc != nil && r.acc.end == forever {
		r.acc.end = f.tick()
	}
}

// expr evaluates an expression for access checking, recording argument
// accesses in evaluation order (spec §4).
func (f *fn) expr(st *flow, e ast.Expr) *result {
	t := f.c.info.Types[e]
	switch x := e.(type) {
	case *ast.Paren:
		return f.expr(st, x.X)
	case *ast.Ident, *ast.FieldExpr, *ast.IndexExpr, *ast.SliceExpr:
		if p := f.placeOf(st, e, true); p != nil {
			r := &result{pl: *p, t: t}
			if _, isSlice := e.(*ast.SliceExpr); isSlice {
				r.isAccess = true
				r.acc = &access{pl: *p, kind: accView, start: f.tick(), end: forever, pos: e.Span()}
				if p.perm == ast.ConvEdit {
					r.acc.kind = accEdit
				}
				f.accs = append(f.accs, r.acc)
			}
			return r
		}
		if fx, ok := e.(*ast.FieldExpr); ok {
			base := f.expr(st, fx.X)
			if base != nil && base.isAccess {
				np := base.pl
				np.path = append(append([]pathElem{}, base.pl.path...), pathElem{kind: peField, name: fx.Name})
				return &result{pl: np, isAccess: true, acc: base.acc, release: base.release, t: t}
			}
		}
		if sx, ok := e.(*ast.SliceExpr); ok {
			base := f.expr(st, sx.X)
			if sx.Lo != nil {
				f.consumeValue(st, sx.Lo)
			}
			if sx.Hi != nil {
				f.consumeValue(st, sx.Hi)
			}
			if base != nil && base.isAccess {
				// slicing a projection yields a projection over the same place
				return &result{pl: base.pl, isAccess: true, acc: base.acc, release: base.release, t: t}
			}
			return &result{t: t}
		}
		if ix, ok := e.(*ast.IndexExpr); ok {
			base := f.expr(st, ix.X)
			for _, a := range ix.Args {
				f.consumeValue(st, a)
			}
			if base != nil && base.isAccess && len(ix.Args) == 1 {
				el := pathElem{kind: peIndex}
				if v, ok := f.constInt(ix.Args[0]); ok {
					el.isConst, el.idx = true, v
				}
				np := base.pl
				np.path = append(append([]pathElem{}, base.pl.path...), el)
				return &result{pl: np, isAccess: true, acc: base.acc, release: base.release, t: t}
			}
		}
		return &result{t: t}
	case *ast.CallExpr:
		return f.call(st, x)
	case *ast.Binary:
		f.consumeValue(st, x.L)
		if x.Op == "&&" || x.Op == "||" {
			// the right operand is a conditional path (spec/types.md §5)
			branch := st.clone()
			f.consumeValue(branch, x.R)
			merged := f.join([]*flow{st, branch}, x.Pos)
			st.vars = merged.vars
			return &result{t: t}
		}
		f.consumeValue(st, x.R)
		return &result{t: t}
	case *ast.Unary:
		f.consumeValue(st, x.X)
		return &result{t: t}
	case *ast.Try:
		f.consumeValue(st, x.X)
		// the error path exits every scope (spec §4.1 error propagation),
		// after the operand's own moves have taken effect
		f.c.info.Drops[x] = f.exitSyms(st, 0)
		f.c.info.OutFieldDrops[x] = f.outFieldDrops(st)
		if call, ok := x.X.(*ast.CallExpr); ok {
			if sym := f.outArgOf(call); sym != nil {
				st.vars[sym] = &varState{st: stInit}
			}
		}
		return &result{t: t, owned: true}
	case *ast.Move:
		id := &ast.Ident{Pos: x.Pos, Name: x.Name}
		var sym *resolve.Symbol
		for k, v := range f.c.info.Idents {
			if k.Pos == x.Pos && k.Name == x.Name {
				sym = v
			}
		}
		_ = id
		if sym == nil {
			return &result{t: t}
		}
		r := &result{pl: place{root: sym, perm: ast.ConvEdit}, t: t, owned: true, ownedRoot: sym}
		f.checkReadable(st, sym, x.Pos, nil)
		f.consumePlace(st, r, x.Pos)
		return &result{t: t, owned: true}
	case *ast.Copy:
		r := f.expr(st, x.X)
		if r != nil {
			if r.isAccess {
				f.closeAccess(r)
			} else if r.pl.root != nil {
				tk := f.tick()
				f.accs = append(f.accs, &access{pl: r.pl, kind: accView, start: tk, end: tk, pos: x.Pos})
			}
		}
		return &result{t: t, owned: true}
	case *ast.Take:
		if p := f.placeOf(st, x.Place, true); p != nil {
			if p.perm != ast.ConvEdit {
				f.errorf("ACC_PERMISSION_ESCALATION", x.Pos, "take through a view")
			}
			tk := f.tick()
			f.accs = append(f.accs, &access{pl: *p, kind: accEdit, start: tk, end: tk, pos: x.Pos})
		}
		return &result{t: t, owned: true}
	case *ast.Replace:
		f.consumeValue(st, x.Value)
		if p := f.placeOf(st, x.Place, true); p != nil {
			if p.perm != ast.ConvEdit {
				f.errorf("ACC_PERMISSION_ESCALATION", x.Pos, "replace through a view")
			}
			tk := f.tick()
			f.accs = append(f.accs, &access{pl: *p, kind: accEdit, start: tk, end: tk, pos: x.Pos})
		}
		return &result{t: t, owned: true}
	case *ast.Drop:
		var sym *resolve.Symbol
		for k, v := range f.c.info.Idents {
			if k.Pos == x.Pos && k.Name == x.Name {
				sym = v
			}
		}
		if sym != nil {
			r := &result{pl: place{root: sym, perm: ast.ConvEdit}, t: sym.Type}
			f.checkReadable(st, sym, x.Pos, nil)
			f.consumePlace(st, r, x.Pos)
		}
		return &result{t: t}
	case *ast.ConvExpr:
		f.consumeValue(st, x.X)
		return &result{t: t}
	case *ast.ArrayLit:
		for _, el := range x.Elems {
			f.consumeValue(st, el)
		}
		return &result{t: t, owned: true}
	case *ast.StructLit:
		for _, fi := range x.Fields {
			if ft := f.c.info.Types[fi.Value]; ft != nil {
				if _, isAcc := ft.(*resolve.Access); isAcc && !resolve.IsCopy(valueType(ft)) {
					f.errorf("ACC_ESCAPE_STORE", fi.Pos, "field %s stores a second-class access", fi.Name)
				}
			}
			f.consumeValue(st, fi.Value)
		}
		return &result{t: t, owned: true}
	case *ast.IntLit, *ast.FloatLit, *ast.StringLit, *ast.BoolLit, *ast.UnitLit, *ast.TrapExpr:
		return &result{t: t}
	case *ast.RangeExpr:
		f.consumeValue(st, x.Lo)
		f.consumeValue(st, x.Hi)
		return &result{t: t}
	}
	return &result{t: t}
}

func valueType(t resolve.Type) resolve.Type {
	if a, ok := t.(*resolve.Access); ok {
		if a.Seq {
			return &resolve.Slice{Elem: a.Elem}
		}
		return a.Elem
	}
	return t
}

// call records argument accesses in evaluation order, closes them when the
// call returns, and produces the call's result (a projection when the callee
// returns accesses, derived from its single access argument).
func (f *fn) call(st *flow, x *ast.CallExpr) *result {
	info := f.c.info.Calls[x]
	t := f.c.info.Types[x]
	if info == nil {
		for _, a := range x.Args {
			f.consumeValue(st, a.X)
		}
		return &result{t: t}
	}
	if info.Intrinsic == "thread.spawn" || info.Intrinsic == "scope.spawn" {
		return f.spawnCall(st, x, info)
	}
	if info.Fn == nil && !info.Method && info.Intrinsic == "" {
		// call through a function value: evaluate the callee expression first (spec §4)
		f.consumeValue(st, x.Fn)
	}
	var argAccs []*access
	var sourceAcc *access
	var sourcePlace *place
	var srcResult *result
	args := x.Args
	params := info.Params
	if info.Method {
		recv := x.Fn.(*ast.FieldExpr).X
		args = append([]*ast.Arg{{Pos: recv.Span(), X: recv}}, args...)
	}
	for i, a := range args {
		if i >= len(params) {
			f.consumeValue(st, a.X)
			continue
		}
		p := params[i]
		switch p.Conv {
		case ast.ConvView, ast.ConvEdit:
			r := f.expr(st, a.X)
			if r == nil {
				continue
			}
			kind := accView
			if p.Conv == ast.ConvEdit {
				kind = accEdit
			}
			if r.isAccess {
				// a projection passed down is reborrowed at the parameter's kind
				// for the duration of the call
				f.closeAccess(r)
				acc := &access{pl: r.pl, kind: kind, start: f.tick(), end: forever, pos: a.Pos, release: r.release}
				f.accs = append(f.accs, acc)
				argAccs = append(argAccs, acc)
				sourceAcc, sourcePlace, srcResult = acc, &r.pl, r
				continue
			}
			if r.pl.root == nil {
				continue
			}
			acc := &access{pl: r.pl, kind: kind, start: f.tick(), end: forever, pos: a.Pos}
			f.accs = append(f.accs, acc)
			argAccs = append(argAccs, acc)
			sourceAcc, sourcePlace, srcResult = acc, &r.pl, r
		case ast.ConvOut:
			id, ok := a.X.(*ast.Ident)
			if !ok {
				continue
			}
			sym := f.c.info.Idents[id]
			if sym == nil {
				continue
			}
			vs := st.get(sym)
			if vs.st == stInit {
				f.errorf("ACC_CONFLICT", a.Pos, "%s is already initialized; out requires uninitialized storage", sym.Name)
			}
			acc := &access{pl: place{root: sym, perm: ast.ConvEdit}, kind: accInit, start: f.tick(), end: forever, pos: a.Pos}
			f.accs = append(f.accs, acc)
			argAccs = append(argAccs, acc)
			// infallible out call initializes unconditionally; fallible ones are
			// resolved by ? or match (stmts.go)
			if n, ok := t.(*resolve.Named); !ok || n.Decl.Name != "Result" || n.Decl.Module != "core" {
				vs.st = stInit
				vs.fields = nil
			}
		default:
			f.consumeValue(st, a.X)
		}
	}
	end := f.tick()
	for _, a := range argAccs {
		if a.end == forever {
			a.end = end
		}
	}
	f.checkAtomicOrdering(x, info)
	// the result's interval starts strictly after the arguments' intervals end
	start := f.tick()
	// Result
	retIsAccess := false
	anyEdit := false
	for _, r := range info.Ret {
		if a, ok := r.(*resolve.Access); ok {
			retIsAccess = true
			if a.Edit {
				anyEdit = true
			}
		}
	}
	if !retIsAccess {
		return &result{t: t, owned: !resolve.IsCopy(t)}
	}
	if sourcePlace == nil {
		// intrinsic without a place source (e.g. String literal slices): fresh root
		return &result{t: t, isAccess: true, acc: &access{kind: accView, start: start, end: forever}, rawAccess: isRawAccessOp(info.Fn)}
	}
	kind := accView
	if anyEdit {
		kind = accEdit
	}
	acc := &access{pl: *sourcePlace, kind: kind, start: start, end: forever, pos: x.Pos, release: info.Release}
	_ = sourceAcc
	f.accs = append(f.accs, acc)
	return &result{pl: *sourcePlace, isAccess: true, acc: acc, release: info.Release || (srcResult != nil && srcResult.release), t: t}
}

// spawnCall applies the §5.5 rules for thread.spawn and Scope.spawn.
func (f *fn) spawnCall(st *flow, x *ast.CallExpr, info *resolve.CallInfo) *result {
	t := f.c.info.Types[x]
	if len(x.Args) == 0 {
		return &result{t: t}
	}
	f.consumeValue(st, x.Args[0].X)
	var scopeSym *resolve.Symbol
	if info.Intrinsic == "scope.spawn" {
		if fe, ok := x.Fn.(*ast.FieldExpr); ok {
			if id, ok := fe.X.(*ast.Ident); ok {
				scopeSym = f.c.info.Idents[id]
			}
		}
	}
	for i, a := range x.Args[1:] {
		if i+1 >= len(info.Params) {
			f.consumeValue(st, a.X)
			continue
		}
		p := info.Params[i+1]
		switch p.Conv {
		case ast.ConvView, ast.ConvEdit:
			r := f.expr(st, a.X)
			if r == nil || (r.pl.root == nil) {
				continue
			}
			kind := accView
			if p.Conv == ast.ConvEdit {
				kind = accEdit
				if r.pl.perm != ast.ConvEdit {
					f.errorf("ACC_PERMISSION_ESCALATION", a.Pos, "edit argument given a view place")
				}
			}
			if info.Intrinsic == "thread.spawn" {
				f.errorf("ACC_LIVE_ACROSS_SPAWN", a.Pos, "access passed to thread.spawn")
				continue
			}
			// scoped: the access lives until the end of the enclosing with-block
			root := f.underlyingRoot(r.pl.root)
			if scopeSym != nil && root != nil && root.ID > scopeSym.ID {
				f.errorf("ACC_LIVE_ACROSS_SPAWN", a.Pos, "%s is declared inside the scope block and would be released before the join", r.pl.root.Name)
			}
			if r.isAccess {
				f.closeAccess(r)
			}
			acc := &access{pl: r.pl, kind: kind, start: f.tick(), end: forever, pos: a.Pos, label: "spawn"}
			f.accs = append(f.accs, acc)
			if sc := f.scopeFor(scopeSym); sc != nil {
				f.encl = append(f.encl, &access{pl: r.pl, kind: kind, start: 0, end: forever, pos: a.Pos, label: "spawn"})
			}
		default:
			f.consumeValue(st, a.X)
		}
	}
	return &result{t: t, owned: true}
}

// underlyingRoot follows access bindings to the variable they derive from.
func (f *fn) underlyingRoot(sym *resolve.Symbol) *resolve.Symbol {
	for i := 0; i < 16 && sym != nil && sym.Kind == resolve.SymAccess; i++ {
		br := f.bindRoots[sym]
		if br == nil || br.pl.root == nil || br.pl.root == sym {
			break
		}
		sym = br.pl.root
	}
	return sym
}

func (f *fn) scopeFor(sym *resolve.Symbol) *bindScope {
	for i := len(f.scopes) - 1; i >= 0; i-- {
		if f.scopes[i].scopeSym == sym && sym != nil {
			return f.scopes[i]
		}
	}
	return nil
}

var loadOrders = map[string]bool{"relaxed": true, "acquire": true, "seq_cst": true}
var storeOrders = map[string]bool{"relaxed": true, "release": true, "seq_cst": true}
var fenceOrders = map[string]bool{"acquire": true, "release": true, "acq_rel": true, "seq_cst": true}
var cmpxchgFailure = map[string]map[string]bool{
	"relaxed": {"relaxed": true},
	"acquire": {"relaxed": true, "acquire": true},
	"release": {"relaxed": true},
	"acq_rel": {"relaxed": true, "acquire": true},
	"seq_cst": {"relaxed": true, "acquire": true, "seq_cst": true},
}

// checkAtomicOrdering enforces the permitted orderings of §5.5.
func (f *fn) checkAtomicOrdering(x *ast.CallExpr, info *resolve.CallInfo) {
	if info.Fn == nil {
		return
	}
	isAtomic := info.Fn.Receiver != nil && info.Fn.Receiver.Name == "Atomic" && info.Fn.Receiver.Module == "core"
	isFence := info.Fn.Name == "fence" && info.Fn.Module == "core"
	if !isAtomic && !isFence {
		return
	}
	name := func(e ast.Expr) string {
		switch v := e.(type) {
		case *ast.Ident:
			return v.Name
		case *ast.FieldExpr:
			return v.Name
		}
		return ""
	}
	args := x.Args
	switch info.Fn.Name {
	case "load":
		if len(args) == 1 && !loadOrders[name(args[0].X)] {
			f.errorf("ATOMIC_INVALID_ORDERING", args[0].Pos, "load permits relaxed, acquire or seq_cst")
		}
	case "store":
		if len(args) == 2 && !storeOrders[name(args[1].X)] {
			f.errorf("ATOMIC_INVALID_ORDERING", args[1].Pos, "store permits relaxed, release or seq_cst")
		}
	case "fence":
		if len(args) == 1 && !fenceOrders[name(args[0].X)] {
			f.errorf("ATOMIC_INVALID_ORDERING", args[0].Pos, "fence permits acquire, release, acq_rel or seq_cst")
		}
	case "compare_exchange", "compare_exchange_weak":
		if len(args) == 4 {
			s, fl := name(args[2].X), name(args[3].X)
			if allowed, ok := cmpxchgFailure[s]; !ok || !allowed[fl] {
				f.errorf("ATOMIC_INVALID_ORDERING", args[3].Pos, "failure ordering %s is not permitted with success ordering %s", fl, s)
			}
		}
	}
}

// isRawAccessOp reports the prelude intrinsics that open an access on raw
// memory (ptr_view, ptr_edit, ptr_view_seq, ptr_edit_seq, ptr_edit2).
func isRawAccessOp(fd *resolve.FuncDecl) bool {
	if fd == nil || fd.Module != "core" || fd.Receiver != nil {
		return false
	}
	switch fd.Name {
	case "ptr_view", "ptr_edit", "ptr_view_seq", "ptr_edit_seq", "ptr_edit2":
		return true
	}
	return false
}

// isTrapKindCall reports a call to the prelude's trap_kind, which never returns.
func (f *fn) isTrapKindCall(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	info := f.c.info.Calls[call]
	return info != nil && info.Fn != nil && info.Fn.Module == "core" && info.Fn.Receiver == nil && info.Fn.Name == "trap_kind"
}
