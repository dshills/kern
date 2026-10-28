package check

import (
	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/resolve"
	"reflect"
)

// block checks a block, returning the state at its end. Owned locals
// declared in the block are destroyed at its end (statically).
func (f *fn) block(st *flow, b *ast.Block) *flow {
	f.pushScope(b)
	cur := st
	for _, s := range b.Stmts {
		if cur.dead {
			break
		}
		cur = f.stmt(cur, s)
	}
	if !cur.dead {
		f.c.info.Drops[b] = f.exitSyms(cur, len(f.scopes)-1)
	}
	f.popScope()
	return cur
}

// stmt checks one statement and returns the resulting state.
func (f *fn) stmt(st *flow, s ast.Stmt) *flow {
	f.accs = f.accs[:0]
	switch s := s.(type) {
	case *ast.VarDecl:
		sym := f.c.info.Decls[s]
		if s.Init != nil {
			f.checkOutCallUse(s.Init)
			f.consumeValue(st, s.Init)
			f.finish(st, s)
			if sym != nil {
				st.vars[sym] = &varState{st: stInit}
				f.curScope().owned = append(f.curScope().owned, sym)
			}
			return st
		}
		if sym != nil {
			st.vars[sym] = &varState{st: stUninit}
			f.curScope().owned = append(f.curScope().owned, sym)
		}
		return st
	case *ast.ShortDecl:
		f.checkOutCallUse(s.Value)
		f.consumeValue(st, s.Value)
		f.finish(st, s)
		if sym := f.c.info.Decls[s]; sym != nil {
			st.vars[sym] = &varState{st: stInit}
			f.curScope().owned = append(f.curScope().owned, sym)
		}
		return st
	case *ast.Assign:
		return f.assign(st, s)
	case *ast.With:
		return f.with(st, s)
	case *ast.Match:
		return f.match(st, s)
	case *ast.For:
		return f.forStmt(st, s)
	case *ast.While:
		return f.loop(st, s.Cond, s.Body, s.Pos)
	case *ast.If:
		return f.ifStmt(st, s)
	case *ast.Return:
		return f.ret(st, s)
	case *ast.Break, *ast.Continue:
		out := st.clone()
		out.dead = true
		f.c.info.Drops[s] = f.exitSyms(st, f.loopScopeIndex())
		f.recordLoopExit(st, s)
		return out
	case *ast.Unsafe:
		f.unsafe++
		f.hasUnsafe = true
		out := f.block(st, s.Body)
		f.unsafe--
		return out
	case *ast.Block:
		return f.block(st, s)
	case *ast.ExprStmt:
		f.checkOutCallUse(s.X)
		r := f.expr(st, s.X)
		if r != nil && r.isAccess {
			f.closeAccess(r)
		}
		f.finish(st, s)
		if _, ok := s.X.(*ast.TrapExpr); ok || f.isTrapKindCall(s.X) {
			out := st.clone()
			out.dead = true
			return out
		}
		return st
	}
	return st
}

// checkOutCallUse reports INIT_OUT_RESULT_UNHANDLED when a fallible out call
// is used other than through ? or as a match subject.
func (f *fn) checkOutCallUse(e ast.Expr) {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return
	}
	if !f.hasOutArg(call) {
		return
	}
	if n, ok := f.c.info.Types[call].(*resolve.Named); ok && n.Decl.Name == "Result" && n.Decl.Module == "core" {
		f.errorf("INIT_OUT_RESULT_UNHANDLED", call.Pos, "the result of a fallible out call must be consumed by ? or match")
		// ? is the fix when the enclosing function returns a Result with the
		// same error type, so the propagated error type-checks
		if len(f.fd.Ret) == 1 {
			if r, ok := f.fd.Ret[0].(*resolve.Named); ok && r.Decl == n.Decl && len(r.Args) == 2 && len(n.Args) == 2 && reflect.DeepEqual(r.Args[1], n.Args[1]) {
				f.fix(diag.InsertAt(f.file, call.End, "?"))
			}
		}
	}
}

func (f *fn) hasOutArg(call *ast.CallExpr) bool {
	for _, a := range call.Args {
		if a.Out {
			return true
		}
	}
	return false
}

// finish runs the conflict check for the accesses of the statement.
func (f *fn) finish(st *flow, s ast.Node) {
	all := append([]*access{}, f.encl...)
	all = append(all, f.accs...)
	reported := map[[2]*access]bool{}
	reportedRoot := map[*resolve.Symbol]bool{}
	for i := 0; i < len(all); i++ {
		for j := i + 1; j < len(all); j++ {
			a, b := all[i], all[j]
			if a.kind == accFreeze && b.kind == accFreeze {
				continue
			}
			if !conflicts(a, b) {
				continue
			}
			// report on the statement's own access
			own := b
			other := a
			if j < len(f.encl) {
				continue
			}
			if i >= len(f.encl) {
				// both in this statement: report on the later one
				if a.start > b.start {
					own, other = a, b
				}
			}
			key := [2]*access{own, other}
			if reported[key] || reportedRoot[own.pl.root] {
				continue
			}
			reported[key] = true
			reportedRoot[own.pl.root] = true
			if own.kind == accConsume && len(own.pl.path) == 0 {
				f.errorf("OWN_USE_AFTER_MOVE", own.pos, "%s is moved while another access to it is live", own.pl.root.Name)
				continue
			}
			if other.kind == accConsume && len(other.pl.path) == 0 {
				f.errorf("OWN_USE_AFTER_MOVE", own.pos, "%s is used after being moved in this statement", own.pl.root.Name)
				continue
			}
			f.errorf("ACC_CONFLICT", own.pos, "%s access to %s conflicts with a live %s access", own.kind, describePlace(own.pl), describeOther(other))
		}
	}
	f.accs = f.accs[:0]
}

func describeOther(a *access) string {
	if a.kind == accFreeze {
		return "binding of " + describePlace(a.pl)
	}
	return a.kind.String() + " access to " + describePlace(a.pl)
}

func describePlace(p place) string {
	s := p.root.Name
	for _, e := range p.path {
		switch e.kind {
		case peField:
			s += "." + e.name
		case peIndex:
			s += "[..]"
		case peSlice:
			s += "[..:..]"
		}
	}
	return s
}

func (f *fn) assign(st *flow, s *ast.Assign) *flow {
	// RHS first (spec §4), then the target's edit access at store time.
	f.checkOutCallUse(s.Value)
	f.consumeValue(st, s.Value)
	target := f.placeOf(st, s.Target, false)
	if target == nil {
		r := f.expr(st, s.Target)
		if r != nil && r.isAccess {
			// assignment through a returned projection, e.g. v.at(i) = x
			t := f.tick()
			f.accs = append(f.accs, &access{pl: r.pl, kind: accEdit, start: t, end: t, pos: s.Target.Span()})
			f.closeAccess(r)
			if r.pl.perm != ast.ConvEdit {
				f.errorf("ACC_PERMISSION_ESCALATION", s.Target.Span(), "assignment through a view")
			}
		}
		f.finish(st, s)
		return st
	}
	root := target.root
	vs := st.vars[root]
	if len(target.path) == 0 {
		// whole-variable assignment: initializes or replaces
		if vs != nil && (vs.st == stUninit || vs.st == stMoved || vs.st == stPartial) {
			if vs.st == stPartial {
				all := f.outFieldDrops(st)
				f.c.info.OutFieldDrops[s] = map[*resolve.Symbol][]string{root: all[root]}
			}
			f.finish(st, s)
			st.vars[root] = &varState{st: stInit}
			return st
		}
		if (vs == nil || vs.st == stInit) && !resolve.IsCopy(root.Type) && (root.Kind == resolve.SymLocal || root.Kind == resolve.SymParam && root.Conv != ast.ConvView && root.Conv != ast.ConvEdit) {
			f.c.info.Drops[s] = []*resolve.Symbol{root}
		}
	} else if vs != nil && vs.st != stInit {
		// field assignment on uninitialized or moved storage
		if vs.st == stMoved {
			f.errorf("OWN_USE_AFTER_MOVE", s.Target.Span(), "%s was moved", root.Name)
			f.finish(st, s)
			return st
		}
		if root.Conv == ast.ConvOut && target.path[0].kind == peField && len(target.path) == 1 {
			if vs.fields == nil {
				vs.fields = map[string]bool{}
			}
			vs.fields[target.path[0].name] = true
			vs.st = stPartial
			if f.allFieldsInit(root, vs) {
				vs.st = stInit
				vs.fields = nil
			}
			f.finish(st, s)
			return st
		}
		f.errorf("INIT_FIELD_OF_UNINITIALIZED", s.Target.Span(), "field of %s assigned before %s is initialized", root.Name, root.Name)
		f.finish(st, s)
		st.vars[root] = &varState{st: stInit} // recover: suppress cascading reads
		return st
	}
	if target.perm != ast.ConvEdit && !f.interiorEscalation(target) {
		f.errorf("ACC_PERMISSION_ESCALATION", s.Target.Span(), "assignment through a view binding")
	}
	t := f.tick()
	f.accs = append(f.accs, &access{pl: *target, kind: accEdit, start: t, end: t, pos: s.Target.Span()})
	f.finish(st, s)
	return st
}

func (f *fn) allFieldsInit(root *resolve.Symbol, vs *varState) bool {
	n, ok := root.Type.(*resolve.Named)
	if !ok {
		return true
	}
	for _, fl := range n.Decl.Fields {
		if !vs.fields[fl.Name] {
			return false
		}
	}
	return true
}

func (f *fn) with(st *flow, s *ast.With) *flow {
	r := f.expr(st, s.Subject)
	if r != nil && r.owned && r.ownedRoot != nil {
		// block owns the value: consume it now
		t := f.tick()
		f.accs = append(f.accs, &access{pl: place{root: r.ownedRoot}, kind: accConsume, start: t, end: t, pos: s.Subject.Span()})
		f.markMoved(st, r.ownedRoot, s.Subject.Span())
	}
	if r != nil && r.isAccess {
		f.closeAccess(r)
	}
	f.finish(st, s)
	sc := f.pushScope(s)
	// Bindings
	single := f.c.info.Decls[s]
	if r != nil && (r.isAccess || r.owned) {
		if r.isAccess && r.pl.root != nil {
			// freeze the underlying place for the block
			f.encl = append(f.encl, &access{pl: r.pl, kind: accFreeze, start: 0, end: forever, pos: s.Subject.Span(), release: r.release})
		}
		if single != nil {
			f.bindAccess(single, r)
			sc.bindings = append(sc.bindings, single)
			if n, ok := single.Type.(*resolve.Named); ok && n.Decl.Name == "Scope" && n.Decl.Module == "core" {
				sc.scopeSym = single
			}
			if call, ok := s.Subject.(*ast.CallExpr); ok && isCoreMethod(f.c.info.Calls[call], "Mutex", "lock") {
				if f.guards == nil {
					f.guards = map[*resolve.Symbol]bool{}
				}
				f.guards[single] = true
			}
		}
		for i := range s.NamePos {
			if sym := f.c.info.Decls[&s.NamePos[i]]; sym != nil {
				f.bindAccess(sym, r)
				sc.bindings = append(sc.bindings, sym)
			}
		}
	}
	if r != nil && r.owned {
		f.c.info.OwnedSubject[s] = true
	}
	out := f.block(st, s.Body)
	if !out.dead {
		f.c.info.Drops[s] = f.exitSyms(out, len(f.scopes)-1)
	}
	f.popScope()
	return out
}

// bindAccess records that binding sym derives from r's place (for the
// spawn root-scope rule and for permission checks).
func (f *fn) bindAccess(sym *resolve.Symbol, r *result) {
	if f.bindRoots == nil {
		f.bindRoots = map[*resolve.Symbol]*result{}
	}
	f.bindRoots[sym] = r
}

func (f *fn) match(st *flow, s *ast.Match) *flow {
	r := f.expr(st, s.Subject)
	if r != nil && r.owned && r.ownedRoot != nil {
		t := f.tick()
		f.accs = append(f.accs, &access{pl: place{root: r.ownedRoot}, kind: accConsume, start: t, end: t, pos: s.Subject.Span()})
		f.markMoved(st, r.ownedRoot, s.Subject.Span())
	} else if s.Move && r != nil && r.pl.root != nil && len(r.pl.path) == 0 && !r.isAccess {
		// `match move x`: the match consumes the whole variable (Rule 5.3.1),
		// so nothing of it is dropped afterwards (a generic body is checked
		// once; an instantiation whose payload has a destructor relies on this)
		t := f.tick()
		f.accs = append(f.accs, &access{pl: place{root: r.pl.root}, kind: accConsume, start: t, end: t, pos: s.Subject.Span()})
		f.markMoved(st, r.pl.root, s.Subject.Span())
	}
	if r != nil && r.isAccess {
		f.closeAccess(r)
	}
	f.finish(st, s)
	sc := f.pushScope(s)
	// The subject stays frozen only while an arm holds an access binding;
	// arms that bind only Copy values copy them when entered.
	needsFreeze := false
	for _, arm := range s.Arms {
		for i := range arm.BindPos {
			if sym := f.c.info.Decls[&arm.BindPos[i]]; sym != nil && sym.Kind == resolve.SymAccess {
				needsFreeze = true
			}
		}
	}
	if r != nil && r.pl.root != nil && !s.Move && needsFreeze {
		f.encl = append(f.encl, &access{pl: r.pl, kind: accFreeze, start: 0, end: forever, pos: s.Subject.Span()})
	}
	if r != nil && r.owned {
		f.c.info.OwnedSubject[s] = true
	}
	outVar := f.outArgOf(s.Subject)
	var results []*flow
	for _, arm := range s.Arms {
		armSt := st.clone()
		if outVar != nil {
			if arm.Variant == "Ok" {
				armSt.vars[outVar] = &varState{st: stInit}
			} else {
				armSt.vars[outVar] = &varState{st: stUninit}
			}
		}
		armScope := f.pushScope(arm)
		for i := range arm.BindPos {
			if sym := f.c.info.Decls[&arm.BindPos[i]]; sym != nil {
				if sym.Kind == resolve.SymLocal {
					armSt.vars[sym] = &varState{st: stInit}
					armScope.owned = append(armScope.owned, sym)
				} else {
					armScope.bindings = append(armScope.bindings, sym)
					if r != nil {
						f.bindAccess(sym, r)
					}
				}
			}
		}
		end := f.block(armSt, arm.Body)
		if !end.dead {
			f.c.info.Drops[arm] = f.exitSyms(end, len(f.scopes)-1)
		}
		f.popScope()
		results = append(results, end)
	}
	_ = sc
	f.popScope()
	return f.join(results, s.Pos)
}

// outArgOf returns the out-argument symbol of a fallible out call used as a
// match subject, or nil.
func (f *fn) outArgOf(e ast.Expr) *resolve.Symbol {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return nil
	}
	for _, a := range call.Args {
		if a.Out {
			if id, ok := a.X.(*ast.Ident); ok {
				return f.c.info.Idents[id]
			}
		}
	}
	return nil
}

func (f *fn) forStmt(st *flow, s *ast.For) *flow {
	var r *result
	if _, isRange := s.Iter.(*ast.RangeExpr); isRange {
		rg := s.Iter.(*ast.RangeExpr)
		f.consumeValue(st, rg.Lo)
		f.consumeValue(st, rg.Hi)
	} else {
		r = f.expr(st, s.Iter)
	}
	if r != nil && r.isAccess {
		f.closeAccess(r)
	}
	f.finish(st, s)
	sc := f.pushScope(s)
	sc.isLoop = true
	if r != nil && r.pl.root != nil {
		f.encl = append(f.encl, &access{pl: r.pl, kind: accFreeze, start: 0, end: forever, pos: s.Iter.Span()})
	}
	sym := f.c.info.Decls[s]
	out := f.loopBody(st, func(in *flow) *flow {
		b := in.clone()
		if sym != nil {
			if sym.Kind == resolve.SymLocal {
				b.vars[sym] = &varState{st: stInit}
				sc.owned = append(sc.owned, sym)
			} else {
				sc.bindings = append(sc.bindings, sym)
				if r != nil {
					f.bindAccess(sym, r)
				}
			}
		}
		end := f.block(b, s.Body)
		if !end.dead {
			f.c.info.Drops[s] = f.exitSyms(end, len(f.scopes)-1)
		}
		return end
	}, s.Pos)
	f.popScope()
	return out
}

func (f *fn) loop(st *flow, cond ast.Expr, body *ast.Block, pos ast.Pos) *flow {
	f.consumeValue(st, cond)
	f.finish(st, body)
	reeval := func(back *flow) {
		// the condition is evaluated again on every back edge
		f.accs = f.accs[:0]
		f.consumeValue(back, cond)
		f.finish(back, body)
	}
	sc := f.pushScope(body)
	sc.isLoop = true
	out := f.loopBodyWith(st, func(in *flow) *flow { return f.block(in, body) }, reeval, pos)
	f.popScope()
	return out
}

// loopBody checks a loop body once from the entry state and requires the
// state at the back edge to agree with the entry state for variables declared
// outside the loop (spec §5.1: static moved-ness).
func (f *fn) loopBody(entry *flow, body func(*flow) *flow, pos ast.Pos) *flow {
	return f.loopBodyWith(entry, body, nil, pos)
}

// loopBodyWith is loopBody with a transfer function applied to every back
// edge (the while condition's re-evaluation) before the comparison.
func (f *fn) loopBodyWith(entry *flow, body func(*flow) *flow, backEdge func(*flow), pos ast.Pos) *flow {
	f.loopDepth++
	saved := f.loopExits
	f.loopExits = nil
	end := body(entry.clone())
	f.loopDepth--
	backEdges := []*flow{end}
	var breaks []*flow
	for _, e := range f.loopExits {
		if e.isBreak {
			breaks = append(breaks, e.st)
		} else {
			backEdges = append(backEdges, e.st)
		}
	}
	f.loopExits = saved
	if backEdge != nil {
		for _, be := range backEdges {
			if !be.dead {
				backEdge(be)
			}
		}
	}
	for _, be := range backEdges {
		if be.dead {
			continue
		}
		for sym, v := range entry.vars {
			if bv, ok := be.vars[sym]; ok && bv.st != v.st {
				f.errorf("OWN_CONDITIONAL_MOVE", pos, "%s is %s at the end of the loop body but %s at loop entry", sym.Name, stateName(bv.st), stateName(v.st))
			}
		}
	}
	out := entry.clone()
	all := append([]*flow{out}, breaks...)
	return f.join(all, pos)
}

type loopExit struct {
	st      *flow
	isBreak bool
}

func (f *fn) recordLoopExit(st *flow, s ast.Stmt) {
	_, isBreak := s.(*ast.Break)
	f.loopExits = append(f.loopExits, loopExit{st: st.clone(), isBreak: isBreak})
}

func stateName(s initState) string {
	switch s {
	case stUninit:
		return "uninitialized"
	case stInit:
		return "initialized"
	case stMoved:
		return "moved"
	}
	return "partially initialized"
}

func (f *fn) ifStmt(st *flow, s *ast.If) *flow {
	f.consumeValue(st, s.Cond)
	f.finish(st, s)
	thenSt := f.block(st.clone(), s.Then)
	var elseSt *flow
	switch e := s.Else.(type) {
	case *ast.If:
		elseSt = f.ifStmt(st.clone(), e)
	case *ast.Block:
		elseSt = f.block(st.clone(), e)
	default:
		elseSt = st.clone()
	}
	return f.join([]*flow{thenSt, elseSt}, s.Pos)
}

// join merges states at a control-flow join, reporting OWN_CONDITIONAL_MOVE
// and INIT_CONDITIONAL on disagreement for variables still in scope.
func (f *fn) join(states []*flow, pos ast.Pos) *flow {
	var live []*flow
	for _, s := range states {
		if !s.dead {
			live = append(live, s)
		}
	}
	if len(live) == 0 {
		out := newFlow()
		out.dead = true
		if len(states) > 0 {
			out = states[0].clone()
			out.dead = true
		}
		return out
	}
	out := live[0].clone()
	for _, s := range live[1:] {
		for sym, v := range out.vars {
			w, ok := s.vars[sym]
			if !ok {
				continue
			}
			if sym.Conv == ast.ConvOut {
				out.vars[sym] = f.mergeOut(sym, v, w)
				continue
			}
			if v.st == w.st {
				continue
			}
			if !f.inScope(sym) {
				continue
			}
			switch {
			case v.st == stMoved && w.st == stInit || v.st == stInit && w.st == stMoved:
				f.errorf("OWN_CONDITIONAL_MOVE", pos, "%s is moved on one path but not on another; add drop(%s) on the non-moving path", sym.Name, sym.Name)
				out.vars[sym] = &varState{st: stMoved}
			case (v.st == stUninit || v.st == stPartial) && w.st == stInit || v.st == stInit && (w.st == stUninit || w.st == stPartial):
				f.errorf("INIT_CONDITIONAL", pos, "%s is initialized on one path but not on another", sym.Name)
				out.vars[sym] = &varState{st: stUninit}
			default:
				out.vars[sym] = &varState{st: stUninit}
			}
		}
	}
	return out
}

// inScope reports whether a symbol is still visible at the current point:
// its declaration order precedes the end of the current scope's last binding.
func (f *fn) inScope(sym *resolve.Symbol) bool {
	if _, isParam := f.params[sym.Name]; isParam && f.params[sym.Name] == sym {
		return true
	}
	return f.declaredAtOrAbove(sym)
}

func (f *fn) declaredAtOrAbove(sym *resolve.Symbol) bool {
	for _, s := range f.scopes {
		for _, o := range s.owned {
			if o == sym {
				return true
			}
		}
	}
	return false
}

func (f *fn) ret(st *flow, s *ast.Return) *flow {
	out := st.clone()
	out.dead = true
	defer func() {
		f.c.info.Drops[s] = f.exitSyms(st, 0)
	}()
	if s.Value == nil {
		f.normalReturn(st, s.Pos)
		return out
	}
	isErr := f.isErrReturn(s.Value)
	if isErr {
		f.c.info.OutFieldDrops[s] = f.outFieldDrops(st)
	}
	if len(f.fd.Ret) >= 1 {
		if _, isAcc := f.fd.Ret[0].(*resolve.Access); isAcc || len(f.fd.Ret) > 1 {
			f.returnAccess(st, s)
			f.finish(st, s)
			f.normalReturn(st, s.Pos)
			return out
		}
	}
	f.consumeValue(st, s.Value)
	f.finish(st, s)
	if !isErr {
		f.normalReturn(st, s.Pos)
	}
	return out
}

// isErrReturn reports whether e constructs the Err variant of core.Result,
// using the resolver's symbol for the constructor rather than its spelling.
func (f *fn) isErrReturn(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	isResult := func(sym *resolve.Symbol) bool {
		return sym != nil && sym.Kind == resolve.SymType && sym.TDecl != nil && sym.TDecl.Name == "Result" && sym.TDecl.Module == "core"
	}
	switch fn := call.Fn.(type) {
	case *ast.Ident:
		return fn.Name == "Err" && isResult(f.c.info.Idents[fn])
	case *ast.FieldExpr:
		if fn.Name != "Err" {
			return false
		}
		switch base := fn.X.(type) {
		case *ast.Ident:
			return isResult(f.c.info.Idents[base])
		case *ast.IndexExpr:
			if id, ok := base.X.(*ast.Ident); ok {
				return isResult(f.c.info.Idents[id])
			}
		}
	}
	return false
}

// returnAccess checks the elision, permission, release-escape and
// multi-access rules for a function returning accesses (spec §5.3).
func (f *fn) returnAccess(st *flow, s *ast.Return) {
	r := f.expr(st, s.Value)
	if r == nil {
		return
	}
	if !r.isAccess {
		// a place expression: an access to a local or parameter place
		if r.pl.root == nil {
			f.errorf("ACC_ESCAPE_RETURN", s.Value.Span(), "returned expression is not an access")
			return
		}
	}
	if r.isAccess && r.rawAccess && f.unsafe > 0 {
		// an access opened on raw memory (spec §6): its validity is the
		// contract's obligation, not derived from a parameter
		f.closeAccess(r)
		return
	}
	if len(f.accessParams) == 0 {
		f.errorf("ACC_ESCAPE_RETURN", s.Value.Span(), "function has no view/edit parameter to derive the returned access from")
		return
	}
	if len(f.accessParams) > 1 {
		f.errorf("ACC_AMBIGUOUS_SOURCE", s.Value.Span(), "function has %d view/edit parameters; a returned access needs exactly one source", len(f.accessParams))
		return
	}
	root := r.pl.root
	src := f.accessParams[0]
	if root != src {
		if root != nil && root.Kind == resolve.SymAccess {
			if br := f.bindRoots[root]; br != nil && br.pl.root == src {
				root = src
			}
		}
	}
	if root != src {
		f.errorf("ACC_ESCAPE_RETURN", s.Value.Span(), "returned access derives from %s, not from the access parameter %s", rootName(root), src.Name)
		return
	}
	if r.release {
		f.errorf("ACC_RELEASE_ESCAPE", s.Value.Span(), "returned access carries a pending release function")
	}
	wantEdit := false
	for _, rt := range f.fd.Ret {
		if a, ok := rt.(*resolve.Access); ok && a.Edit {
			wantEdit = true
		}
	}
	if wantEdit && !r.isAccess && r.pl.perm != ast.ConvEdit {
		f.errorf("ACC_PERMISSION_ESCALATION", s.Value.Span(), "returning an edit of a read-only place")
	}
	if r.isAccess {
		f.closeAccess(r)
	}
}

func rootName(s *resolve.Symbol) string {
	if s == nil {
		return "a temporary"
	}
	return s.Name
}

// mergeOut merges two states of out storage: the intersection of initialized
// fields, where stInit means every field and stUninit means none.
// INIT_OUT_NOT_SET is reported at the return (spec §5.2), not at the join.
func (f *fn) mergeOut(sym *resolve.Symbol, v, w *varState) *varState {
	if v.st == stInit && w.st == stInit {
		return &varState{st: stInit}
	}
	fieldsOf := func(x *varState) map[string]bool {
		switch x.st {
		case stInit:
			all := map[string]bool{}
			if n, ok := sym.Type.(*resolve.Named); ok {
				for _, fl := range n.Decl.Fields {
					all[fl.Name] = true
				}
			}
			return all
		case stPartial:
			return x.fields
		}
		return map[string]bool{}
	}
	a, b := fieldsOf(v), fieldsOf(w)
	merged := &varState{st: stPartial, fields: map[string]bool{}}
	for k := range a {
		if b[k] {
			merged.fields[k] = true
		}
	}
	if len(merged.fields) == 0 {
		merged.st = stUninit
	}
	return merged
}
