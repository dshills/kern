package lower

import (
	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/resolve"
)

func (fl *fnLower) block(b *ast.Block) {
	fl.pushFrame()
	for _, s := range b.Stmts {
		if fl.cur == nil {
			break
		}
		fl.stmt(s)
	}
	if fl.cur != nil {
		fl.exit(fl.l.info.Drops[b], len(fl.frames)-1)
	}
	fl.popFrame()
}

func (fl *fnLower) stmt(s ast.Stmt) {
	switch s := s.(type) {
	case *ast.VarDecl:
		sym := fl.l.info.Decls[s]
		if sym == nil {
			return
		}
		loc := fl.local(sym)
		fl.bindSym(sym)
		if s.Init != nil {
			v := fl.value(s.Init, loc.Type)
			fl.emit(&ir.Store{Place: ir.Place{Local: loc}, Src: v})
			fl.emit(&ir.Init{Place: ir.Place{Local: loc}})
		}
	case *ast.ShortDecl:
		sym := fl.l.info.Decls[s]
		if sym == nil {
			return
		}
		loc := fl.local(sym)
		fl.bindSym(sym)
		v := fl.value(s.Value, loc.Type)
		fl.emit(&ir.Store{Place: ir.Place{Local: loc}, Src: v})
		fl.emit(&ir.Init{Place: ir.Place{Local: loc}})
	case *ast.Assign:
		fl.assign(s)
	case *ast.With:
		fl.with(s)
	case *ast.Match:
		fl.match(s)
	case *ast.For:
		fl.forStmt(s)
	case *ast.While:
		fl.whileStmt(s)
	case *ast.If:
		fl.ifStmt(s)
	case *ast.Return:
		fl.ret(s)
	case *ast.Break:
		fl.exit(fl.l.info.Drops[s], fl.loopFrame())
		fl.terminate(&ir.Br{Target: fl.loops[len(fl.loops)-1].breakBlk})
	case *ast.Continue:
		fl.exit(fl.l.info.Drops[s], fl.loopFrame())
		fl.terminate(&ir.Br{Target: fl.loops[len(fl.loops)-1].continueBlk})
	case *ast.Unsafe:
		fl.emit(&ir.UnsafeBegin{})
		fl.block(s.Body)
		if fl.cur != nil {
			fl.emit(&ir.UnsafeEnd{})
		}
	case *ast.Block:
		fl.block(s)
	case *ast.ExprStmt:
		if _, ok := s.X.(*ast.TrapExpr); ok {
			fl.emit(&ir.Trap{Kind: "explicit", Pos: s.Pos})
			fl.terminate(&ir.Unreachable{})
			return
		}
		if fl.isTrapKindCall(s.X) {
			fl.emit(&ir.Trap{Kind: fl.trapKindArg(s.X.(*ast.CallExpr)), Pos: s.Pos})
			fl.terminate(&ir.Unreachable{})
			return
		}
		r := fl.expr(s.X, nil)
		fl.discard(r)
	}
}

// discard releases a result that is not consumed: a projection is ended, an
// owned temporary is dropped.
func (fl *fnLower) discard(r operand) {
	switch {
	case r.access != nil && r.ownsAccess:
		fl.endAccess(r.access)
	case r.val != nil && r.owned && !resolve.IsCopy(r.val.Type):
		tmp := fl.newTemp(r.val.Type)
		fl.emit(&ir.Store{Place: ir.Place{Local: tmp}, Src: r.val})
		fl.emit(&ir.Init{Place: ir.Place{Local: tmp}})
		fl.emitDrop(ir.Place{Local: tmp}, tmp.Type, ast.Pos{})
	}
}

func (fl *fnLower) assign(s *ast.Assign) {
	target := fl.place(s.Target)
	v := fl.value(s.Value, fl.typeOf(s.Target))
	for _, sym := range fl.l.info.Drops[s] {
		fl.dropSym(sym)
	}
	fl.outFieldDrops(s)
	if !target.isPlace() {
		fl.fail(s.Pos, "assignment target is not a place")
	}
	fl.emit(&ir.Store{Place: target.pl, Src: v})
	if target.pl.Local != nil {
		fl.emit(&ir.Init{Place: target.pl})
	}
	if target.access != nil && target.ownsAccess {
		fl.endAccess(target.access)
	}
}

func (fl *fnLower) with(s *ast.With) {
	r := fl.expr(s.Subject, nil)
	fr := fl.pushFrame()
	var bindVals []*ir.Value
	switch {
	case r.tuple != nil:
		bindVals = r.tuple
	case r.access != nil:
		bindVals = []*ir.Value{r.access}
	case r.isPlace():
		// a place used as a with subject: open an access on it
		v := fl.newValue(&resolve.Access{Edit: r.perm == ast.ConvEdit, Elem: fl.typeOf(s.Subject)})
		fl.emit(&ir.AccessBegin{Dst: v, Edit: r.perm == ast.ConvEdit, Place: r.pl, Pos: s.Subject.Span()})
		bindVals = []*ir.Value{v}
	default:
		// owned value: the block owns a temporary
		tmp := fl.newTemp(r.val.Type)
		fl.emit(&ir.Store{Place: ir.Place{Local: tmp}, Src: r.val})
		fl.emit(&ir.Init{Place: ir.Place{Local: tmp}})
		v := fl.newValue(&resolve.Access{Edit: true, Elem: r.val.Type})
		fl.emit(&ir.AccessBegin{Dst: v, Edit: true, Place: ir.Place{Local: tmp}, Pos: s.Subject.Span()})
		bindVals = []*ir.Value{v}
		fr.temps = append(fr.temps, tmp)
	}
	// the bindings' access values are ended by their symbols' exits; the
	// values the lowering created without a symbol end with the frame
	bound := map[*ir.Value]bool{}
	if single := fl.l.info.Decls[s]; single != nil && len(bindVals) > 0 {
		fl.accs[single] = bindVals[0]
		fl.bindSym(single)
		bound[bindVals[0]] = true
	}
	for i := range s.NamePos {
		if sym := fl.l.info.Decls[&s.NamePos[i]]; sym != nil && i < len(bindVals) {
			fl.accs[sym] = bindVals[i]
			fl.bindSym(sym)
			bound[bindVals[i]] = true
		}
	}
	for _, v := range bindVals {
		if !bound[v] {
			fr.accs = append(fr.accs, v)
		}
	}
	fl.block(s.Body)
	if fl.cur != nil {
		fl.exit(fl.l.info.Drops[s], len(fl.frames)-1)
	}
	fl.popFrame()
}

func (fl *fnLower) match(s *ast.Match) {
	r := fl.expr(s.Subject, nil)
	subjT := fl.typeOf(s.Subject)
	if a, ok := subjT.(*resolve.Access); ok {
		subjT = a.Elem
	}
	n, ok := subjT.(*resolve.Named)
	if !ok {
		fl.fail(s.Pos, "match subject is not a union")
	}
	var subj ir.Place
	owned := fl.l.info.OwnedSubject[s] || s.Move
	var tmp *ir.Local
	switch {
	case r.access != nil:
		subj = ir.Place{Access: r.access}
	case r.isPlace():
		if owned && r.pl.Local != nil && len(r.pl.Path) == 0 {
			tmp = fl.newTemp(r.pl.Local.Type)
			mv := fl.newValue(r.pl.Local.Type)
			fl.emit(&ir.Move{Dst: mv, Src: r.pl})
			fl.emit(&ir.Store{Place: ir.Place{Local: tmp}, Src: mv})
			fl.emit(&ir.Init{Place: ir.Place{Local: tmp}})
			subj = ir.Place{Local: tmp}
		} else {
			subj = r.pl
		}
	default:
		tmp = fl.newTemp(r.val.Type)
		fl.emit(&ir.Store{Place: ir.Place{Local: tmp}, Src: r.val})
		fl.emit(&ir.Init{Place: ir.Place{Local: tmp}})
		subj = ir.Place{Local: tmp}
		owned = true
	}
	mf := fl.pushFrame()
	if r.access != nil && r.ownsAccess {
		mf.accs = append(mf.accs, r.access)
	}
	if tmp != nil && !owned {
		mf.temps = append(mf.temps, tmp)
	}
	tag := fl.newValue(&resolve.Prim{Name: "u32"})
	fl.emit(&ir.Tag{Dst: tag, Place: subj})
	join := fl.newBlock("match.join")
	sw := &ir.Switch{Tag: tag, Cases: map[int]*ir.Block{}}
	head := fl.cur
	fl.terminate(sw)
	outVar := fl.outArgOf(s.Subject)
	for _, arm := range s.Arms {
		blk := fl.newBlock("arm")
		fl.cur = blk
		vd := fl.l.info.Variants[arm]
		if arm.Wild || vd == nil {
			sw.Default = blk
		} else {
			sw.Cases[vd.Index] = blk
		}
		if outVar != nil {
			if arm.Variant == "Ok" {
				fl.emit(&ir.Init{Place: ir.Place{Local: fl.local(outVar)}})
			}
		}
		fl.pushFrame()
		if vd == nil && owned && tmp != nil {
			// a wildcard arm of an owned subject: nothing is bound, so the
			// whole subject is destroyed on entry
			fl.emitDrop(ir.Place{Local: tmp}, tmp.Type, arm.Pos)
		}
		if vd != nil {
			fts := resolve.VariantFieldTypes(n, vd)
			for i := range vd.Fields {
				path := append(append([]ir.PathElem{}, subj.Path...), ir.PathElem{Kind: ir.PathVariant, Tag: vd.Index, Field: i, Name: vd.Name})
				fieldPlace := ir.Place{Local: subj.Local, Access: subj.Access, Path: path}
				var sym *resolve.Symbol
				if i < len(arm.BindPos) {
					sym = fl.l.info.Decls[&arm.BindPos[i]]
				}
				ft := fl.ty(fts[i])
				switch {
				case sym == nil:
					if owned && !resolve.IsCopy(ft) {
						fl.emitDrop(fieldPlace, ft, arm.Pos)
					}
				case sym.Kind == resolve.SymLocal:
					loc := fl.local(sym)
					fl.bindSym(sym)
					v := fl.newValue(ft)
					if resolve.IsCopy(ft) {
						fl.emit(&ir.Load{Dst: v, Place: fieldPlace, Pos: arm.BindPos[i]})
					} else {
						fl.emit(&ir.Move{Dst: v, Src: fieldPlace})
					}
					fl.emit(&ir.Store{Place: ir.Place{Local: loc}, Src: v})
					fl.emit(&ir.Init{Place: ir.Place{Local: loc}})
				default:
					v := fl.newValue(&resolve.Access{Edit: sym.Conv == ast.ConvEdit, Elem: ft})
					fl.emit(&ir.AccessBegin{Dst: v, Edit: sym.Conv == ast.ConvEdit, Place: fieldPlace, Pos: arm.BindPos[i]})
					fl.accs[sym] = v
					fl.bindSym(sym)
				}
			}
		}
		fl.block(arm.Body)
		if fl.cur != nil {
			fl.exit(fl.l.info.Drops[arm], len(fl.frames)-1)
			fl.terminate(&ir.Br{Target: join})
		}
		fl.popFrame()
	}
	if sw.Default == nil {
		dflt := fl.newBlock("match.unreachable")
		dflt.Instrs = append(dflt.Instrs, &ir.Trap{Kind: "unreachable", Pos: s.Pos})
		dflt.Term = &ir.Unreachable{}
		sw.Default = dflt
	}
	_ = head
	fl.cur = join
	if noPredecessors(fl.f, join) {
		fl.popFrame()
		fl.terminate(&ir.Unreachable{})
		return
	}
	fl.exit(nil, len(fl.frames)-1)
	fl.popFrame()
}

func noPredecessors(f *ir.Func, b *ir.Block) bool {
	for _, blk := range f.Blocks {
		switch t := blk.Term.(type) {
		case *ir.Br:
			if t.Target == b {
				return false
			}
		case *ir.CondBr:
			if t.Then == b || t.Else == b {
				return false
			}
		case *ir.Switch:
			if t.Default == b {
				return false
			}
			for _, c := range t.Cases {
				if c == b {
					return false
				}
			}
		}
	}
	return true
}

func (fl *fnLower) outArgOf(e ast.Expr) *resolve.Symbol {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return nil
	}
	for _, a := range call.Args {
		if a.Out {
			if id, ok := a.X.(*ast.Ident); ok {
				return fl.l.info.Idents[id]
			}
		}
	}
	return nil
}

func (fl *fnLower) forStmt(s *ast.For) {
	if b := s.Cursor(); b != nil {
		fl.block(b) // a cursor-protocol for, desugared by the resolver
		return
	}
	sym := fl.l.info.Decls[s]
	if rg, ok := s.Iter.(*ast.RangeExpr); ok {
		t := fl.typeOf(rg)
		lo := fl.value(rg.Lo, t)
		hi := fl.value(rg.Hi, t)
		counter := fl.newTemp(t)
		fl.emit(&ir.Store{Place: ir.Place{Local: counter}, Src: lo})
		fl.emit(&ir.Init{Place: ir.Place{Local: counter}})
		head := fl.newBlock("for.head")
		body := fl.newBlock("for.body")
		step := fl.newBlock("for.step")
		exit := fl.newBlock("for.exit")
		fl.terminate(&ir.Br{Target: head})
		fl.cur = head
		cv := fl.newValue(t)
		fl.emit(&ir.Load{Dst: cv, Place: ir.Place{Local: counter}})
		cond := fl.newValue(&resolve.Prim{Name: "bool"})
		fl.emit(&ir.Binary{Dst: cond, Op: "<", L: cv, R: hi})
		fl.terminate(&ir.CondBr{Cond: cond, Then: body, Else: exit})
		fl.cur = body
		lf := fl.pushFrame()
		lf.isLoop = true
		if sym != nil {
			loc := fl.local(sym)
			fl.bindSym(sym)
			v := fl.newValue(t)
			fl.emit(&ir.Load{Dst: v, Place: ir.Place{Local: counter}})
			fl.emit(&ir.Store{Place: ir.Place{Local: loc}, Src: v})
			fl.emit(&ir.Init{Place: ir.Place{Local: loc}})
		}
		fl.loops = append(fl.loops, &loopCtx{breakBlk: exit, continueBlk: step})
		fl.block(s.Body)
		fl.loops = fl.loops[:len(fl.loops)-1]
		if fl.cur != nil {
			fl.exit(fl.l.info.Drops[s], len(fl.frames)-1)
			fl.terminate(&ir.Br{Target: step})
		}
		fl.popFrame()
		fl.cur = step
		one := fl.newValue(t)
		fl.emit(&ir.Const{Dst: one, Int: 1})
		cur := fl.newValue(t)
		fl.emit(&ir.Load{Dst: cur, Place: ir.Place{Local: counter}})
		next := fl.newValue(t)
		fl.emit(&ir.Binary{Dst: next, Op: "+", L: cur, R: one, Mode: "wrapping"})
		fl.emit(&ir.Store{Place: ir.Place{Local: counter}, Src: next})
		fl.terminate(&ir.Br{Target: head})
		fl.cur = exit
		return
	}
	// iteration over an array place or a sequence access; an array value
	// (a const, or a literal) is given a place first
	r := fl.materialize(fl.expr(s.Iter, nil))
	var seq ir.Place
	var seqAccess *ir.Value
	edit := false
	switch {
	case r.access != nil:
		seqAccess = r.access
		seq = ir.Place{Access: r.access}
		if a, ok := r.access.Type.(*resolve.Access); ok {
			edit = a.Edit
		}
	case r.isPlace():
		v := fl.newValue(&resolve.Access{Edit: r.perm == ast.ConvEdit, Seq: true, Elem: elemTypeOf(fl.typeOf(s.Iter))})
		fl.emit(&ir.AccessBegin{Dst: v, Edit: r.perm == ast.ConvEdit, Place: r.pl, Pos: s.Iter.Span()})
		seqAccess = v
		seq = ir.Place{Access: v}
		edit = r.perm == ast.ConvEdit
		r.ownsAccess = true
	default:
		fl.fail(s.Iter.Span(), "cannot iterate over a value")
	}
	of := fl.pushFrame() // holds the sequence access for the loop's duration
	if r.ownsAccess && seqAccess != nil {
		of.accs = append(of.accs, seqAccess)
	}
	usize := &resolve.Prim{Name: "usize"}
	ln := fl.newValue(usize)
	fl.emit(&ir.Len{Dst: ln, Place: seq})
	counter := fl.newTemp(usize)
	zero := fl.newValue(usize)
	fl.emit(&ir.Const{Dst: zero})
	fl.emit(&ir.Store{Place: ir.Place{Local: counter}, Src: zero})
	fl.emit(&ir.Init{Place: ir.Place{Local: counter}})
	head := fl.newBlock("for.head")
	body := fl.newBlock("for.body")
	step := fl.newBlock("for.step")
	exit := fl.newBlock("for.exit")
	fl.terminate(&ir.Br{Target: head})
	fl.cur = head
	cv := fl.newValue(usize)
	fl.emit(&ir.Load{Dst: cv, Place: ir.Place{Local: counter}})
	cond := fl.newValue(&resolve.Prim{Name: "bool"})
	fl.emit(&ir.Binary{Dst: cond, Op: "<", L: cv, R: ln})
	fl.terminate(&ir.CondBr{Cond: cond, Then: body, Else: exit})
	fl.cur = body
	idx := fl.newValue(usize)
	fl.emit(&ir.Load{Dst: idx, Place: ir.Place{Local: counter}})
	// the loop bound proves the index in range
	fl.emit(&ir.CheckBounds{Index: idx, Len: ln, Prov: ir.Provenance{Label: "proven", ProvenBy: "loop_bound"}, Pos: s.Pos})
	elemPlace := ir.Place{Access: seqAccess, Path: []ir.PathElem{{Kind: ir.PathIndex, Index: idx}}}
	var elemAcc *ir.Value
	lf := fl.pushFrame()
	lf.isLoop = true
	if sym != nil {
		fl.bindSym(sym)
		et := fl.ty(sym.Type)
		if sym.Kind == resolve.SymLocal {
			loc := fl.local(sym)
			v := fl.newValue(et)
			fl.emit(&ir.Load{Dst: v, Place: elemPlace, Pos: s.VarPos})
			fl.emit(&ir.Store{Place: ir.Place{Local: loc}, Src: v})
			fl.emit(&ir.Init{Place: ir.Place{Local: loc}})
		} else {
			elemAcc = fl.newValue(&resolve.Access{Edit: edit && sym.Conv == ast.ConvEdit, Elem: et})
			fl.emit(&ir.AccessBegin{Dst: elemAcc, Edit: edit && sym.Conv == ast.ConvEdit, Place: elemPlace, Pos: s.VarPos})
			fl.accs[sym] = elemAcc
		}
	}
	fl.loops = append(fl.loops, &loopCtx{breakBlk: exit, continueBlk: step})
	fl.block(s.Body)
	fl.loops = fl.loops[:len(fl.loops)-1]
	if fl.cur != nil {
		fl.exit(fl.l.info.Drops[s], len(fl.frames)-1)
		fl.terminate(&ir.Br{Target: step})
	}
	fl.popFrame()
	fl.cur = step
	one := fl.newValue(usize)
	fl.emit(&ir.Const{Dst: one, Int: 1})
	cur := fl.newValue(usize)
	fl.emit(&ir.Load{Dst: cur, Place: ir.Place{Local: counter}})
	next := fl.newValue(usize)
	fl.emit(&ir.Binary{Dst: next, Op: "+", L: cur, R: one, Mode: "wrapping"})
	fl.emit(&ir.Store{Place: ir.Place{Local: counter}, Src: next})
	fl.terminate(&ir.Br{Target: head})
	fl.cur = exit
	fl.exit(nil, len(fl.frames)-1)
	fl.popFrame()
	_ = elemAcc
}

func elemTypeOf(t resolve.Type) resolve.Type {
	switch x := t.(type) {
	case *resolve.Array:
		return x.Elem
	case *resolve.Slice:
		return x.Elem
	case *resolve.Access:
		if x.Seq {
			return x.Elem
		}
		return elemTypeOf(x.Elem)
	}
	return t
}

// condBr branches on a condition: `a && b` tests b only when a holds,
// `a || b` only when a fails, `!a` swaps the targets, and any other
// condition is evaluated to a value. Each comparison then terminates its
// own block, so the proof pass reads every operand's bound on the edge it
// establishes (specs/prove-guards/SPEC.md F2); the operands are evaluated
// in source order, each at most once, as the value lowering evaluates
// them.
func (fl *fnLower) condBr(e ast.Expr, then, els *ir.Block) {
	switch x := e.(type) {
	case *ast.Binary:
		if x.Op == "&&" || x.Op == "||" {
			rhs := fl.newBlock("cond.rhs")
			if x.Op == "&&" {
				fl.condBr(x.L, rhs, els)
			} else {
				fl.condBr(x.L, then, rhs)
			}
			fl.cur = rhs
			fl.condBr(x.R, then, els)
			return
		}
	case *ast.Unary:
		if x.Op == "!" {
			fl.condBr(x.X, els, then)
			return
		}
	}
	cond := fl.value(e, &resolve.Prim{Name: "bool"})
	fl.terminate(&ir.CondBr{Cond: cond, Then: then, Else: els})
}

func (fl *fnLower) whileStmt(s *ast.While) {
	head := fl.newBlock("while.head")
	body := fl.newBlock("while.body")
	exit := fl.newBlock("while.exit")
	fl.terminate(&ir.Br{Target: head})
	fl.cur = head
	fl.condBr(s.Cond, body, exit)
	fl.cur = body
	lf := fl.pushFrame()
	lf.isLoop = true
	fl.loops = append(fl.loops, &loopCtx{breakBlk: exit, continueBlk: head})
	fl.block(s.Body)
	fl.loops = fl.loops[:len(fl.loops)-1]
	if fl.cur != nil {
		fl.terminate(&ir.Br{Target: head})
	}
	fl.popFrame()
	fl.cur = exit
}

func (fl *fnLower) ifStmt(s *ast.If) {
	then := fl.newBlock("if.then")
	join := fl.newBlock("if.join")
	els := join
	if s.Else != nil {
		els = fl.newBlock("if.else")
	}
	fl.condBr(s.Cond, then, els)
	fl.cur = then
	fl.block(s.Then)
	if fl.cur != nil {
		fl.terminate(&ir.Br{Target: join})
	}
	if s.Else != nil {
		fl.cur = els
		switch e := s.Else.(type) {
		case *ast.If:
			fl.ifStmt(e)
		case *ast.Block:
			fl.block(e)
		}
		if fl.cur != nil {
			fl.terminate(&ir.Br{Target: join})
		}
	}
	fl.cur = join
	if noPredecessors(fl.f, join) {
		fl.terminate(&ir.Unreachable{})
	}
}

func (fl *fnLower) ret(s *ast.Return) {
	if s.Value == nil {
		fl.exit(fl.l.info.Drops[s], 0)
		fl.terminate(&ir.Ret{})
		return
	}
	// access-returning functions: the returned projection stays open across
	// the return (its AccessEnd belongs to the caller)
	if len(fl.f.Rets) > 0 {
		if _, isAcc := fl.f.Rets[0].(*resolve.Access); isAcc || len(fl.f.Rets) > 1 {
			r := fl.expr(s.Value, nil)
			var vals []*ir.Value
			switch {
			case r.tuple != nil:
				vals = r.tuple
			case r.access != nil:
				vals = []*ir.Value{r.access}
			default:
				v := fl.newValue(fl.f.Rets[0])
				fl.emit(&ir.AccessBegin{Dst: v, Edit: fl.f.Rets[0].(*resolve.Access).Edit, Place: r.pl, Pos: s.Pos})
				vals = []*ir.Value{v}
			}
			fl.exitExcept(fl.l.info.Drops[s], vals)
			fl.terminate(&ir.Ret{Vals: vals})
			return
		}
	}
	v := fl.value(s.Value, fl.f.Rets[0])
	fl.outFieldDrops(s)
	fl.exit(fl.l.info.Drops[s], 0)
	fl.terminate(&ir.Ret{Vals: []*ir.Value{v}})
}

// exitExcept is exit(list, 0) that keeps the returned access values open
// (their AccessEnd belongs to the caller).
func (fl *fnLower) exitExcept(list []*resolve.Symbol, keep []*ir.Value) {
	keepSet := map[*ir.Value]bool{}
	for _, k := range keep {
		keepSet[k] = true
	}
	saved := make([][]*ir.Value, len(fl.frames))
	for i, fr := range fl.frames {
		saved[i] = fr.accs
		var accs []*ir.Value
		for _, a := range fr.accs {
			if !keepSet[a] {
				accs = append(accs, a)
			}
		}
		fr.accs = accs
	}
	fl.exit(list, 0)
	for i, fr := range fl.frames {
		fr.accs = saved[i] // other exit paths keep their obligations
	}
}
