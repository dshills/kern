package lower

import (
	"strings"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/resolve"
)

// operand is the result of lowering an expression: a value, a place, an
// access value (a projection), or a tuple of accesses.
type operand struct {
	val        *ir.Value
	owned      bool // val is an owned temporary (move-only)
	pl         ir.Place
	perm       ast.Conv // strongest permission on pl
	access     *ir.Value
	ownsAccess bool // the access must be ended by the consumer
	tuple      []*ir.Value
	release    bool
}

func (o operand) isPlace() bool { return o.pl.Local != nil || o.pl.Access != nil || o.pl.Global != nil }

// value lowers e in value position: Copy places are loaded, move-only
// places are moved, projections are read (Copy) and ended.
func (fl *fnLower) value(e ast.Expr, want resolve.Type) *ir.Value {
	r := fl.expr(e, want)
	return fl.toValue(r, e)
}

func (fl *fnLower) toValue(r operand, e ast.Expr) *ir.Value {
	t := fl.typeOf(e)
	if a, ok := t.(*resolve.Access); ok {
		t = accessElem(a)
	}
	switch {
	case r.val != nil:
		return r.val
	case r.isPlace():
		// a place, possibly a path through a projection
		v := fl.newValue(t)
		if resolve.IsCopy(t) {
			fl.emit(&ir.Load{Dst: v, Place: r.pl, Pos: e.Span()})
		} else {
			fl.emit(&ir.Move{Dst: v, Src: r.pl})
		}
		if r.access != nil && r.ownsAccess {
			fl.endAccess(r.access)
		}
		return v
	case r.access != nil:
		v := fl.newValue(t)
		fl.emit(&ir.Load{Dst: v, Place: ir.Place{Access: r.access}, Pos: e.Span()})
		if r.ownsAccess {
			fl.endAccess(r.access)
		}
		return v
	}
	fl.fail(e.Span(), "expression has no value")
	return nil
}

func accessElem(a *resolve.Access) resolve.Type {
	if a.Seq {
		return &resolve.Slice{Elem: a.Elem}
	}
	return a.Elem
}

// place lowers a place expression.
func (fl *fnLower) place(e ast.Expr) operand {
	r := fl.expr(e, nil)
	if r.access != nil && !r.isPlace() {
		return operand{pl: ir.Place{Access: r.access}, perm: permOf(r.access), access: r.access, ownsAccess: r.ownsAccess}
	}
	return r
}

func permOf(v *ir.Value) ast.Conv {
	if a, ok := v.Type.(*resolve.Access); ok && a.Edit {
		return ast.ConvEdit
	}
	return ast.ConvView
}

func (fl *fnLower) constInt(t resolve.Type, n uint64) *ir.Value {
	v := fl.newValue(t)
	fl.emit(&ir.Const{Dst: v, Int: n})
	return v
}

// expr lowers an expression.
func (fl *fnLower) expr(e ast.Expr, want resolve.Type) operand {
	t := fl.typeOf(e)
	switch x := e.(type) {
	case *ast.Paren:
		return fl.expr(x.X, want)
	case *ast.IntLit:
		v := fl.newValue(t)
		fl.emit(&ir.Const{Dst: v, Int: x.Value, Neg: x.Neg})
		return operand{val: v}
	case *ast.FloatLit:
		v := fl.newValue(t)
		fl.emit(&ir.Const{Dst: v, Float: x.Value})
		return operand{val: v}
	case *ast.BoolLit:
		v := fl.newValue(t)
		fl.emit(&ir.Const{Dst: v, Bool: x.Value})
		return operand{val: v}
	case *ast.UnitLit:
		v := fl.newValue(t)
		fl.emit(&ir.Const{Dst: v, Unit: true})
		return operand{val: v}
	case *ast.StringLit:
		v := fl.newValue(t)
		fl.emit(&ir.Const{Dst: v, Bytes: []byte(x.Value)})
		return operand{access: v, ownsAccess: false}
	case *ast.Ident:
		return fl.ident(x)
	case *ast.FieldExpr:
		if k := fl.l.info.ImportedConsts[x]; k != nil {
			return fl.expr(k.Value, t) // an imported constant
		}
		return fl.field(x)
	case *ast.IndexExpr:
		return fl.index(x)
	case *ast.SliceExpr:
		return fl.slice(x)
	case *ast.CallExpr:
		return fl.call(x, want)
	case *ast.Binary:
		return fl.binary(x)
	case *ast.Unary:
		xv := fl.value(x.X, t)
		v := fl.newValue(t)
		if x.Op == "-" {
			zero := fl.constInt(t, 0)
			fl.emit(&ir.CheckOverflow{Op: "-", L: zero, R: xv, Type: t.String(), Prov: ir.RuntimeChecked, Pos: x.Pos})
			fl.emit(&ir.Unary{Dst: v, Op: "neg", X: xv})
		} else {
			fl.emit(&ir.Unary{Dst: v, Op: "!", X: xv})
		}
		return operand{val: v}
	case *ast.Try:
		return fl.try(x)
	case *ast.Move:
		sym := fl.symAt(x.Pos, x.Name)
		if sym == nil {
			fl.fail(x.Pos, "move of unknown %s", x.Name)
		}
		v := fl.newValue(t)
		fl.emit(&ir.Move{Dst: v, Src: ir.Place{Local: fl.local(sym)}})
		return operand{val: v, owned: true}
	case *ast.Copy:
		r := fl.expr(x.X, nil)
		v := fl.newValue(t)
		src := r.pl
		if r.access != nil && !r.isPlace() {
			src = ir.Place{Access: r.access}
		}
		if resolve.IsCopy(t) {
			fl.emit(&ir.Load{Dst: v, Place: src, Pos: x.Pos})
		} else {
			acc := fl.newValue(&resolve.Access{Elem: t})
			fl.emit(&ir.AccessBegin{Dst: acc, Place: src, Pos: x.Pos})
			fl.emit(&ir.Call{Dsts: []*ir.Value{v}, Intrinsic: "clone", Args: []*ir.Value{acc}, Pos: x.Pos})
			fl.emit(&ir.AccessEnd{Access: acc})
		}
		if r.access != nil && r.ownsAccess {
			fl.endAccess(r.access)
		}
		return operand{val: v, owned: !resolve.IsCopy(t)}
	case *ast.Take:
		r := fl.place(x.Place)
		v := fl.newValue(t)
		fl.emit(&ir.Move{Dst: v, Src: r.pl})
		fl.emit(&ir.Store{Place: r.pl, Src: fl.defaultValue(t, x.Pos)})
		if r.access != nil && r.ownsAccess {
			fl.endAccess(r.access)
		}
		return operand{val: v, owned: true}
	case *ast.Replace:
		nv := fl.value(x.Value, t)
		r := fl.place(x.Place)
		v := fl.newValue(t)
		fl.emit(&ir.Move{Dst: v, Src: r.pl})
		fl.emit(&ir.Store{Place: r.pl, Src: nv})
		if r.access != nil && r.ownsAccess {
			fl.endAccess(r.access)
		}
		return operand{val: v, owned: true}
	case *ast.Drop:
		sym := fl.symAt(x.Pos, x.Name)
		if sym != nil {
			loc := fl.local(sym)
			fl.emitDrop(ir.Place{Local: loc}, loc.Type, x.Pos)
		}
		v := fl.newValue(&resolve.Unit{})
		fl.emit(&ir.Const{Dst: v, Unit: true})
		return operand{val: v}
	case *ast.TrapExpr:
		fl.emit(&ir.Trap{Kind: "explicit", Pos: x.Pos})
		v := fl.newValue(&resolve.Unit{})
		fl.emit(&ir.Const{Dst: v, Unit: true})
		return operand{val: v}
	case *ast.ConvExpr:
		xv := fl.value(x.X, nil)
		v := fl.newValue(t)
		if !x.Truncate {
			fl.emit(&ir.CheckConv{X: xv, To: x.To, Prov: ir.RuntimeChecked, Pos: x.Pos})
		}
		fl.emit(&ir.Conv{Dst: v, To: x.To, X: xv, Truncate: x.Truncate})
		return operand{val: v}
	case *ast.ArrayLit:
		arr := t.(*resolve.Array)
		var elems []*ir.Value
		for _, el := range x.Elems {
			elems = append(elems, fl.value(el, arr.Elem))
		}
		v := fl.newValue(t)
		fl.emit(&ir.MakeArray{Dst: v, Elems: elems})
		return operand{val: v, owned: true}
	case *ast.StructLit:
		n, ok := t.(*resolve.Named)
		if !ok {
			fl.fail(x.Pos, "struct literal of non-struct")
		}
		byName := map[string]*ir.Value{}
		for _, fi := range x.Fields {
			_, ft := resolve.FieldType(n, fi.Name)
			byName[fi.Name] = fl.value(fi.Value, fl.ty(ft))
		}
		var fields []*ir.Value
		for _, fd := range n.Decl.Fields {
			fields = append(fields, byName[fd.Name])
		}
		v := fl.newValue(t)
		fl.emit(&ir.MakeStruct{Dst: v, Fields: fields})
		return operand{val: v, owned: !resolve.IsCopy(t)}
	case *ast.TypeArgExpr:
		fl.fail(x.Pos, "type in expression position")
	case *ast.RangeExpr:
		fl.fail(x.Pos, "range outside for")
	}
	fl.fail(e.Span(), "unsupported expression %T", e)
	return operand{}
}

// defaultValue yields the declared default of a type for take (spec §5.1):
// the zero value for zeroable types, None for Option, and the clone-free
// empty value for library containers.
func (fl *fnLower) defaultValue(t resolve.Type, pos ast.Pos) *ir.Value {
	v := fl.newValue(t)
	if resolve.IsZeroable(t) {
		fl.emit(&ir.Zero{Dst: v})
		return v
	}
	if n, ok := t.(*resolve.Named); ok {
		switch n.Decl.Module + "." + n.Decl.Name {
		case "core.Option":
			fl.emit(&ir.MakeUnion{Dst: v, Tag: 1})
			return v
		case "core.Vec":
			fl.emit(&ir.Call{Dsts: []*ir.Value{v}, Intrinsic: "Vec.new", Pos: pos})
			return v
		}
	}
	fl.fail(pos, "type %s declares no default for take", t)
	return nil
}

func (fl *fnLower) symAt(pos ast.Pos, name string) *resolve.Symbol {
	for id, sym := range fl.l.info.Idents {
		if id.Pos == pos && id.Name == name {
			return sym
		}
	}
	return nil
}

func (fl *fnLower) ident(x *ast.Ident) operand {
	sym := fl.l.info.Idents[x]
	t := fl.typeOf(x)
	if sym == nil {
		fl.fail(x.Pos, "unresolved identifier %s", x.Name)
	}
	switch sym.Kind {
	case resolve.SymLocal, resolve.SymParam:
		if acc, ok := fl.accs[sym]; ok {
			return operand{pl: ir.Place{Access: acc}, perm: permOf(acc)}
		}
		perm := ast.ConvEdit
		return operand{pl: ir.Place{Local: fl.local(sym)}, perm: perm}
	case resolve.SymAccess:
		acc := fl.accs[sym]
		if acc == nil {
			fl.fail(x.Pos, "binding %s has no access value", x.Name)
		}
		return operand{pl: ir.Place{Access: acc}, perm: permOf(acc)}
	case resolve.SymConst:
		return fl.expr(sym.Const.Value, t)
	case resolve.SymGlobal:
		return operand{pl: ir.Place{Global: fl.l.global(sym.Global)}, perm: ast.ConvEdit}
	case resolve.SymFunc:
		v := fl.newValue(t)
		fl.emit(&ir.FuncRef{Dst: v, Name: fl.l.request(sym.Func, nil, fl.file)})
		return operand{val: v}
	case resolve.SymType:
		// a bare variant without payload (e.g. None resolved through bareVariant)
		if n, ok := t.(*resolve.Named); ok && n.Decl.Kind == resolve.KindUnion {
			for _, vd := range n.Decl.Variants {
				if vd.Name == x.Name {
					v := fl.newValue(t)
					fl.emit(&ir.MakeUnion{Dst: v, Tag: vd.Index})
					return operand{val: v, owned: !resolve.IsCopy(t)}
				}
			}
		}
	}
	fl.fail(x.Pos, "identifier %s is not a value", x.Name)
	return operand{}
}

func (fl *fnLower) field(x *ast.FieldExpr) operand {
	t := fl.typeOf(x)
	// payload-free variant through a type: Option[u64].None
	if n, ok := t.(*resolve.Named); ok && n.Decl.Kind == resolve.KindUnion && fl.l.info.Fields[x] == nil {
		if _, isType := fl.l.info.Types[x.X]; !isType || fl.isTypeExpr(x.X) {
			for _, vd := range n.Decl.Variants {
				if vd.Name == x.Name {
					v := fl.newValue(t)
					fl.emit(&ir.MakeUnion{Dst: v, Tag: vd.Index})
					return operand{val: v, owned: !resolve.IsCopy(t)}
				}
			}
		}
	}
	if fl.l.info.Fields[x] == nil {
		// a function value through a static reference (Type.func) is not
		// supported in Phase 1; module.func is handled by call()
		fl.fail(x.Pos, "unsupported field expression %s", x.Name)
	}
	base := fl.expr(x.X, nil)
	bt := fl.typeOf(x.X)
	if a, ok := bt.(*resolve.Access); ok {
		bt = a.Elem
	}
	n, ok := bt.(*resolve.Named)
	if !ok {
		fl.fail(x.Pos, "field of non-struct")
	}
	idx := fieldIndex(n, x.Name)
	el := ir.PathElem{Kind: ir.PathField, Field: idx, Name: x.Name}
	return fl.extend(base, el)
}

// isTypeExpr reports whether e denotes a type (resolved as SymType).
func (fl *fnLower) isTypeExpr(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		s := fl.l.info.Idents[x]
		return s != nil && s.Kind == resolve.SymType
	case *ast.IndexExpr:
		return fl.isTypeExpr(x.X)
	}
	return false
}

// extend appends a path element to a place or projection operand.
func (fl *fnLower) extend(base operand, el ir.PathElem) operand {
	switch {
	case base.isPlace():
		np := base.pl
		np.Path = append(append([]ir.PathElem{}, base.pl.Path...), el)
		return operand{pl: np, perm: base.perm, access: base.access, ownsAccess: base.ownsAccess, release: base.release}
	case base.access != nil:
		return operand{pl: ir.Place{Access: base.access, Path: []ir.PathElem{el}}, perm: permOf(base.access), access: base.access, ownsAccess: base.ownsAccess, release: base.release}
	}
	if base.val != nil {
		// a temporary value: materialize it so a path can be taken
		tmp := fl.newTemp(base.val.Type)
		fl.emit(&ir.Store{Place: ir.Place{Local: tmp}, Src: base.val})
		fl.emit(&ir.Init{Place: ir.Place{Local: tmp}})
		if !resolve.IsCopy(base.val.Type) {
			fl.top().temps = append(fl.top().temps, tmp)
		}
		return operand{pl: ir.Place{Local: tmp, Path: []ir.PathElem{el}}, perm: ast.ConvEdit}
	}
	fl.fail(ast.Pos{}, "field of a non-place")
	return operand{}
}

func (fl *fnLower) index(x *ast.IndexExpr) operand {
	base := fl.expr(x.X, nil)
	usize := &resolve.Prim{Name: "usize"}
	idx := fl.value(x.Args[0], usize)
	var seq ir.Place
	switch {
	case base.isPlace():
		seq = base.pl
	case base.access != nil:
		seq = ir.Place{Access: base.access}
	default:
		fl.fail(x.Pos, "index of a non-place")
	}
	ln := fl.newValue(usize)
	fl.emit(&ir.Len{Dst: ln, Place: seq})
	fl.emit(&ir.CheckBounds{Index: idx, Len: ln, Prov: ir.RuntimeChecked, Pos: x.Pos})
	return fl.extend(base, ir.PathElem{Kind: ir.PathIndex, Index: idx})
}

func (fl *fnLower) slice(x *ast.SliceExpr) operand {
	base := fl.expr(x.X, nil)
	usize := &resolve.Prim{Name: "usize"}
	var seq ir.Place
	switch {
	case base.isPlace():
		seq = base.pl
	case base.access != nil:
		seq = ir.Place{Access: base.access}
	default:
		fl.fail(x.Pos, "slice of a non-place")
	}
	ln := fl.newValue(usize)
	fl.emit(&ir.Len{Dst: ln, Place: seq})
	var lo, hi *ir.Value
	if x.Lo != nil {
		lo = fl.value(x.Lo, usize)
	} else {
		lo = fl.constInt(usize, 0)
	}
	if x.Hi != nil {
		hi = fl.value(x.Hi, usize)
	} else {
		hi = ln
	}
	fl.emit(&ir.CheckBounds{Lo: lo, Hi: hi, Len: ln, Prov: ir.RuntimeChecked, Pos: x.Pos})
	t := fl.typeOf(x)
	v := fl.newValue(t)
	np := seq
	np.Path = append(append([]ir.PathElem{}, seq.Path...), ir.PathElem{Kind: ir.PathSlice, Lo: lo, Hi: hi})
	fl.emit(&ir.AccessBegin{Dst: v, Edit: permOf2(base) == ast.ConvEdit, Place: np, Pos: x.Pos})
	if base.access != nil && base.ownsAccess {
		// the sub-slice derives from the projection; end the parent when the
		// child ends is handled by the consumer (one access value)
		fl.endAccess(base.access)
	}
	return operand{access: v, ownsAccess: true}
}

func permOf2(o operand) ast.Conv {
	if o.isPlace() {
		return o.perm
	}
	if o.access != nil {
		return permOf(o.access)
	}
	return ast.ConvView
}

func (fl *fnLower) binary(x *ast.Binary) operand {
	t := fl.typeOf(x)
	if x.Op == "&&" || x.Op == "||" {
		boolT := &resolve.Prim{Name: "bool"}
		res := fl.newTemp(boolT)
		l := fl.value(x.L, boolT)
		fl.emit(&ir.Store{Place: ir.Place{Local: res}, Src: l})
		fl.emit(&ir.Init{Place: ir.Place{Local: res}})
		rhs := fl.newBlock("sc.rhs")
		join := fl.newBlock("sc.join")
		if x.Op == "&&" {
			fl.terminate(&ir.CondBr{Cond: l, Then: rhs, Else: join})
		} else {
			fl.terminate(&ir.CondBr{Cond: l, Then: join, Else: rhs})
		}
		fl.cur = rhs
		r := fl.value(x.R, boolT)
		fl.emit(&ir.Store{Place: ir.Place{Local: res}, Src: r})
		fl.terminate(&ir.Br{Target: join})
		fl.cur = join
		v := fl.newValue(boolT)
		fl.emit(&ir.Load{Dst: v, Place: ir.Place{Local: res}})
		return operand{val: v}
	}
	lt := fl.typeOf(x.L)
	if a, ok := lt.(*resolve.Access); ok {
		lt = accessElem(a)
	}
	l := fl.value(x.L, lt)
	r := fl.value(x.R, lt)
	v := fl.newValue(t)
	switch x.Op {
	case "+", "-", "*", "/", "%", "<<":
		if resolve.IsInteger(lt) {
			fl.emit(&ir.CheckOverflow{Op: x.Op, L: l, R: r, Type: lt.String(), Prov: ir.RuntimeChecked, Pos: x.Pos})
		}
	case ">>":
		if resolve.IsInteger(lt) {
			fl.emit(&ir.CheckOverflow{Op: x.Op, L: l, R: r, Type: lt.String(), Prov: ir.RuntimeChecked, Pos: x.Pos})
		}
	}
	fl.emit(&ir.Binary{Dst: v, Op: x.Op, L: l, R: r})
	return operand{val: v}
}

// try lowers `x?`: on Err the function returns Err(e) after running the
// error-path drops; on Ok the payload is the value.
func (fl *fnLower) try(x *ast.Try) operand {
	rt := fl.typeOf(x.X)
	if a, ok := rt.(*resolve.Access); ok {
		rt = accessElem(a)
	}
	n, ok := rt.(*resolve.Named)
	if !ok {
		fl.fail(x.Pos, "? on non-Result")
	}
	rv := fl.value(x.X, rt)
	tmp := fl.newTemp(rt)
	fl.emit(&ir.Store{Place: ir.Place{Local: tmp}, Src: rv})
	fl.emit(&ir.Init{Place: ir.Place{Local: tmp}})
	tag := fl.newValue(&resolve.Prim{Name: "u32"})
	fl.emit(&ir.Tag{Dst: tag, Place: ir.Place{Local: tmp}})
	okBlk := fl.newBlock("try.ok")
	errBlk := fl.newBlock("try.err")
	fl.terminate(&ir.Switch{Tag: tag, Cases: map[int]*ir.Block{0: okBlk, 1: errBlk}, Default: errBlk})
	// error path
	fl.cur = errBlk
	errT := fl.ty(n.Args[1])
	ev := fl.newValue(errT)
	fl.emit(&ir.Move{Dst: ev, Src: ir.Place{Local: tmp, Path: []ir.PathElem{{Kind: ir.PathVariant, Tag: 1, Field: 0, Name: "Err"}}}})
	fl.outFieldDrops(x)
	fl.exit(fl.l.info.Drops[x], 0)
	retT := fl.f.Rets[0]
	rv2 := fl.newValue(retT)
	fl.emit(&ir.MakeUnion{Dst: rv2, Tag: 1, Fields: []*ir.Value{ev}})
	fl.terminate(&ir.Ret{Vals: []*ir.Value{rv2}})
	// ok path
	fl.cur = okBlk
	okT := fl.ty(n.Args[0])
	v := fl.newValue(okT)
	fl.emit(&ir.Move{Dst: v, Src: ir.Place{Local: tmp, Path: []ir.PathElem{{Kind: ir.PathVariant, Tag: 0, Field: 0, Name: "Ok"}}}})
	if sym := fl.outArgOf(x.X); sym != nil {
		fl.emit(&ir.Init{Place: ir.Place{Local: fl.local(sym)}})
	}
	return operand{val: v, owned: !resolve.IsCopy(okT)}
}

// ---- calls ----

func (fl *fnLower) call(x *ast.CallExpr, want resolve.Type) operand {
	info := fl.l.info.Calls[x]
	t := fl.typeOf(x)
	if info == nil {
		fl.fail(x.Pos, "unresolved call")
	}
	switch info.Intrinsic {
	case "simd_reduce_add":
		vt := fl.ty(info.TypeArgs["T"])
		v := fl.newValue(t)
		fl.emit(&ir.Call{Dsts: []*ir.Value{v}, Intrinsic: "simd_reduce_add", Args: []*ir.Value{fl.value(x.Args[0].X, vt)}, Pos: x.Pos, TypeArgs: []resolve.Type{vt}})
		return operand{val: v}
	case "len":
		base := fl.expr(x.Args[0].X, nil)
		var seq ir.Place
		switch {
		case base.isPlace():
			seq = base.pl
		case base.access != nil:
			seq = ir.Place{Access: base.access}
		}
		v := fl.newValue(t)
		fl.emit(&ir.Len{Dst: v, Place: seq})
		if base.access != nil && base.ownsAccess {
			fl.endAccess(base.access)
		}
		return operand{val: v}
	case "thread.spawn", "scope.spawn":
		return fl.spawn(x, info, t)
	}
	// the §4.1 wrapping forms are binary operations in wrapping mode
	if info.Fn != nil && info.Fn.Module == "core" && info.Fn.Receiver == nil {
		if op, ok := map[string]string{"wrapping_add": "+", "wrapping_sub": "-", "wrapping_mul": "*"}[info.Fn.Name]; ok {
			l := fl.value(x.Args[0].X, t)
			r := fl.value(x.Args[1].X, t)
			v := fl.newValue(t)
			fl.emit(&ir.Binary{Dst: v, Op: op, L: l, R: r, Mode: "wrapping"})
			return operand{val: v}
		}
	}
	// zero[T]() is the Zero instruction
	if info.Fn != nil && info.Fn.Module == "core" && info.Fn.Name == "zero" && info.Fn.Receiver == nil {
		v := fl.newValue(t)
		fl.emit(&ir.Zero{Dst: v})
		return operand{val: v}
	}
	// variant constructor
	if info.Fn == nil && len(info.Params) == 0 && info.Intrinsic == "" {
		if n, ok := t.(*resolve.Named); ok && n.Decl.Kind == resolve.KindUnion {
			return fl.variantCtor(x, n)
		}
	}
	var args []*ir.Value
	var argAccs []*ir.Value
	var argExprs []*ast.Arg
	if info.Method {
		argExprs = append(argExprs, &ast.Arg{Pos: x.Fn.(*ast.FieldExpr).X.Span(), X: x.Fn.(*ast.FieldExpr).X})
	}
	argExprs = append(argExprs, x.Args...)
	var fnVal *ir.Value
	if info.Fn == nil {
		fnVal = fl.value(x.Fn, nil)
	}
	var outLocals []*ir.Local
	for i, a := range argExprs {
		if i >= len(info.Params) {
			break
		}
		p := info.Params[i]
		pt := fl.ty(p.Type)
		switch p.Conv {
		case ast.ConvView, ast.ConvEdit:
			r := fl.expr(a.X, nil)
			var acc *ir.Value
			switch {
			case r.access != nil && !r.isPlace():
				acc = r.access
				if r.ownsAccess {
					argAccs = append(argAccs, acc)
				}
			case r.isPlace():
				if r.access != nil && r.ownsAccess {
					argAccs = append(argAccs, r.access) // the parent ends after its child
				}
				acc = fl.newValue(pt)
				fl.emit(&ir.AccessBegin{Dst: acc, Edit: p.Conv == ast.ConvEdit, Place: r.pl, Pos: a.Pos})
				argAccs = append(argAccs, acc)
			default:
				// a temporary value viewed: materialize it
				tmp := fl.newTemp(r.val.Type)
				fl.emit(&ir.Store{Place: ir.Place{Local: tmp}, Src: r.val})
				fl.emit(&ir.Init{Place: ir.Place{Local: tmp}})
				acc = fl.newValue(pt)
				fl.emit(&ir.AccessBegin{Dst: acc, Edit: p.Conv == ast.ConvEdit, Place: ir.Place{Local: tmp}, Pos: a.Pos})
				argAccs = append(argAccs, acc)
			}
			args = append(args, acc)
		case ast.ConvOut:
			id := a.X.(*ast.Ident)
			sym := fl.l.info.Idents[id]
			loc := fl.local(sym)
			acc := fl.newValue(&resolve.Access{Edit: true, Elem: loc.Type})
			fl.emit(&ir.AccessBegin{Dst: acc, Edit: true, Place: ir.Place{Local: loc}, Pos: a.Pos})
			args = append(args, acc)
			argAccs = append(argAccs, acc)
			outLocals = append(outLocals, loc)
		default:
			args = append(args, fl.value(a.X, pt))
		}
	}
	var dsts []*ir.Value
	for _, rt := range info.Ret {
		dsts = append(dsts, fl.newValue(fl.ty(rt)))
	}
	callee := ""
	intrinsic := ""
	if info.Fn != nil {
		callee = fl.l.request(info.Fn, fl.callSubst(info), fl.file)
		if info.Fn.Intrinsic {
			intrinsic = intrinsicName(info.Fn)
		}
	}
	rel := ""
	if info.Fn != nil && info.Fn.Release != "" {
		rel = fl.l.request(info.Fn.Receiver.Methods[info.Fn.Release], fl.callSubst(info), fl.file)
		if info.Fn.Receiver.Methods[info.Fn.Release].Intrinsic {
			rel = intrinsicName(info.Fn.Receiver.Methods[info.Fn.Release])
		}
	}
	var targs []resolve.Type
	if info.Fn != nil {
		sub := fl.callSubst(info)
		for _, tp := range info.Fn.OwnerParams {
			targs = append(targs, sub[tp.Name])
		}
		for _, tp := range info.Fn.TypeParams {
			targs = append(targs, sub[tp.Name])
		}
	}
	fl.emit(&ir.Call{Dsts: dsts, Callee: callee, Fn: fnVal, Args: args, Intrinsic: intrinsic, Pos: x.Pos, Release: rel, TypeArgs: targs})
	// infallible out calls initialize their storage
	isResult := false
	if n, ok := t.(*resolve.Named); ok && n.Decl.Name == "Result" && n.Decl.Module == "core" {
		isResult = true
	}
	for _, loc := range outLocals {
		if !isResult {
			fl.emit(&ir.Init{Place: ir.Place{Local: loc}})
		}
	}
	// result projections stay open; argument accesses end now
	retIsAccess := false
	for _, d := range dsts {
		if _, ok := d.Type.(*resolve.Access); ok {
			retIsAccess = true
		}
	}
	for i := len(argAccs) - 1; i >= 0; i-- {
		fl.emit(&ir.AccessEnd{Access: argAccs[i]})
	}
	if retIsAccess {
		if len(dsts) > 1 {
			for _, d := range dsts {
				if rel != "" {
					fl.relOf[d] = releaseInfo{fn: rel, recv: args[0]}
				}
			}
			return operand{tuple: dsts}
		}
		if rel != "" {
			fl.relOf[dsts[0]] = releaseInfo{fn: rel, recv: args[0]}
		}
		return operand{access: dsts[0], ownsAccess: true, release: rel != ""}
	}
	if len(dsts) == 0 {
		v := fl.newValue(&resolve.Unit{})
		fl.emit(&ir.Const{Dst: v, Unit: true})
		return operand{val: v}
	}
	return operand{val: dsts[0], owned: !resolve.IsCopy(dsts[0].Type)}
}

// callSubst builds the substitution for a callee instantiation: the call's
// inferred type arguments, themselves substituted by the caller's.
func (fl *fnLower) callSubst(info *resolve.CallInfo) map[string]resolve.Type {
	m := map[string]resolve.Type{}
	for k, v := range info.TypeArgs {
		if k == "Self" {
			continue
		}
		m[k] = fl.ty(v)
	}
	return m
}

// intrinsicName names a runtime-provided prelude function: "Type.method" or
// the bare function name.
func intrinsicName(fd *resolve.FuncDecl) string {
	if fd.Receiver != nil {
		return fd.Receiver.Name + "." + fd.Name
	}
	return fd.Name
}

func (fl *fnLower) variantCtor(x *ast.CallExpr, n *resolve.Named) operand {
	var name string
	switch fn := x.Fn.(type) {
	case *ast.Ident:
		name = fn.Name
	case *ast.FieldExpr:
		name = fn.Name
	}
	var vd *resolve.VariantDecl
	for _, v := range n.Decl.Variants {
		if v.Name == name {
			vd = v
		}
	}
	if vd == nil {
		fl.fail(x.Pos, "unknown variant %s", name)
	}
	fts := resolve.VariantFieldTypes(n, vd)
	var fields []*ir.Value
	for i, a := range x.Args {
		fields = append(fields, fl.value(a.X, fl.ty(fts[i])))
	}
	v := fl.newValue(n)
	fl.emit(&ir.MakeUnion{Dst: v, Tag: vd.Index, Fields: fields})
	return operand{val: v, owned: !resolve.IsCopy(n)}
}

func (fl *fnLower) spawn(x *ast.CallExpr, info *resolve.CallInfo, t resolve.Type) operand {
	fnVal := fl.value(x.Args[0].X, nil)
	args := []*ir.Value{fnVal}
	var recvAcc *ir.Value
	scopeFrame := -1
	if info.Intrinsic == "scope.spawn" {
		recvExpr := x.Fn.(*ast.FieldExpr).X
		if id, ok := recvExpr.(*ast.Ident); ok {
			if sym := fl.l.info.Idents[id]; sym != nil {
				scopeFrame = fl.frameOf(sym)
			}
		}
		recv := fl.expr(recvExpr, nil)
		if recv.isPlace() {
			recvAcc = fl.newValue(&resolve.Access{Edit: true, Elem: fl.typeOf(recvExpr)})
			fl.emit(&ir.AccessBegin{Dst: recvAcc, Edit: true, Place: recv.pl, Pos: x.Pos})
		} else {
			recvAcc = recv.access
		}
		args = append(args, recvAcc)
	}
	var spawned []*ir.Value
	for i, a := range x.Args[1:] {
		if i+1 >= len(info.Params) {
			break
		}
		p := info.Params[i+1]
		switch p.Conv {
		case ast.ConvView, ast.ConvEdit:
			r := fl.expr(a.X, nil)
			var acc *ir.Value
			if r.access != nil && !r.isPlace() {
				acc = r.access
			} else {
				acc = fl.newValue(fl.ty(p.Type))
				fl.emit(&ir.AccessBegin{Dst: acc, Edit: p.Conv == ast.ConvEdit, Place: r.pl, Pos: a.Pos})
			}
			args = append(args, acc)
			spawned = append(spawned, acc)
		default:
			args = append(args, fl.value(a.X, fl.ty(p.Type)))
		}
	}
	v := fl.newValue(t)
	fl.emit(&ir.Call{Dsts: []*ir.Value{v}, Intrinsic: info.Intrinsic, Args: args, Pos: x.Pos})
	if recvAcc != nil && info.Intrinsic == "scope.spawn" {
		fl.emit(&ir.AccessEnd{Access: recvAcc})
	}
	// scoped accesses stay open until the scope block ends (after the join)
	if scopeFrame >= 0 {
		fl.frames[scopeFrame].late = append(fl.frames[scopeFrame].late, spawned...)
	} else {
		for _, acc := range spawned {
			fl.emit(&ir.AccessEnd{Access: acc})
		}
	}
	return operand{val: v, owned: true}
}

var _ = strings.HasPrefix
