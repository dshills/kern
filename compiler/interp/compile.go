package interp

// Compiled instructions (specs/interp-compile): at a function's first call
// each block becomes a list of ops, closures that do what exec does for
// their instruction with the decisions exec makes at every execution
// (which kind of instruction, which operands, which call target, which
// arithmetic) taken once. A specialised op handles one case of exec and
// hands every other case to it, so the two agree by construction.

import (
	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/resolve"
)

// op is one compiled instruction.
type op func(it *Interp, fr *frame)

// cblock is a compiled block: its ops and its terminator with the
// successors compiled.
type cblock struct {
	b   *ir.Block
	ops []op
	// the terminator
	kind       termKind
	next       *cblock         // br
	cond       int             // condbr: the condition's value ID
	then, els  *cblock         // condbr
	tag        int             // switch: the tag's value ID
	cases      map[int]*cblock // switch
	def        *cblock         // switch default
	rets       []int           // ret: the returned values' IDs
	unreachPos ast.Pos         // unreachable: the function's position
}

type termKind uint8

const (
	termNone termKind = iota
	termBr
	termCondBr
	termSwitch
	termRet
	termUnreachable
)

// compile is f's blocks compiled, by block ID; fi holds the function's
// constants and call targets.
func compile(f *ir.Func, fi *funcInfo) []*cblock {
	n := 0
	for _, b := range f.Blocks {
		n = max(n, b.ID+1)
	}
	code := make([]*cblock, n)
	for _, b := range f.Blocks {
		code[b.ID] = &cblock{b: b}
	}
	at := func(b *ir.Block) *cblock {
		if b == nil || b.ID >= len(code) {
			return nil
		}
		return code[b.ID]
	}
	for _, b := range f.Blocks {
		cb := code[b.ID]
		for _, in := range b.Instrs {
			if o := compileInstr(fi, in); o != nil {
				cb.ops = append(cb.ops, o)
			}
		}
		switch t := b.Term.(type) {
		case *ir.Br:
			cb.kind, cb.next = termBr, at(t.Target)
		case *ir.CondBr:
			cb.kind, cb.cond, cb.then, cb.els = termCondBr, t.Cond.ID, at(t.Then), at(t.Else)
		case *ir.Switch:
			cb.kind, cb.tag, cb.def = termSwitch, t.Tag.ID, at(t.Default)
			cb.cases = make(map[int]*cblock, len(t.Cases))
			for k, s := range t.Cases {
				cb.cases[k] = at(s)
			}
		case *ir.Ret:
			cb.kind = termRet
			for _, v := range t.Vals {
				cb.rets = append(cb.rets, v.ID)
			}
		case *ir.Unreachable:
			cb.kind, cb.unreachPos = termUnreachable, f.Pos
		}
	}
	return code
}

// generic is the op that runs exec on in.
func generic(in ir.Instr) op { return func(it *Interp, fr *frame) { it.exec(fr, in) } }

// plainLocal reports a place that is a local's own slot: no path, and no
// access convention (whose slot holds the caller's access, which resolve
// dereferences).
func plainLocal(p ir.Place) (int, bool) {
	if p.Local == nil || p.Global != nil || p.Access != nil || len(p.Path) > 0 {
		return 0, false
	}
	switch p.Local.Conv {
	case ast.ConvView, ast.ConvEdit, ast.ConvOut:
		return 0, false
	}
	return p.Local.ID, true
}

// placeFn resolves a compiled place: resolve's result for it.
type placeFn func(it *Interp, fr *frame) (ref, *seqRef)

// compilePlace is resolve for a place whose path has only field and
// variant steps, with the root and the steps decided once; nil for any
// other place (an index or slice step), which resolve itself handles.
func compilePlace(p ir.Place) placeFn {
	for _, e := range p.Path {
		if e.Kind != ir.PathField && e.Kind != ir.PathVariant {
			return nil
		}
	}
	type step struct {
		variant    bool
		tag, field int
	}
	steps := make([]step, len(p.Path))
	for k, e := range p.Path {
		steps[k] = step{variant: e.Kind == ir.PathVariant, tag: e.Tag, field: e.Field}
	}
	walk := func(cur ref, seq *seqRef) (ref, *seqRef) {
		for _, e := range steps {
			if e.variant {
				if rr, ok := cur.(rawRef); ok {
					cur = rr.variantField(e.tag, e.field)
				} else {
					// as a struct's: another variant is another *Union
					cur = slotRef{&cur.get().(*Union).Fields[e.field]}
				}
			} else if rr, ok := cur.(rawRef); ok {
				cur = rr.field(e.field)
			} else {
				// a struct's fields are fixed once it is built
				cur = slotRef{&cur.get().(*Struct).Fields[e.field]}
			}
			seq = nil
		}
		return cur, seq
	}
	switch {
	case p.Global != nil:
		id := p.Global.ID
		return func(it *Interp, _ *frame) (ref, *seqRef) { return walk(slotRef{&it.globals[id]}, nil) }
	case p.Local != nil:
		id := p.Local.ID
		viaAccess := p.Local.Conv == ast.ConvView || p.Local.Conv == ast.ConvEdit || p.Local.Conv == ast.ConvOut
		return func(_ *Interp, fr *frame) (ref, *seqRef) {
			// an access parameter's slot holds the caller's access
			if viaAccess {
				if a, ok := fr.locals[id].(*Access); ok {
					return walk(a.Ref, a.Seq)
				}
			}
			return walk(slotRef{&fr.locals[id]}, nil)
		}
	case p.Access != nil:
		id := p.Access.ID
		return func(_ *Interp, fr *frame) (ref, *seqRef) {
			a := fr.vals[id].(*Access)
			return walk(a.Ref, a.Seq)
		}
	}
	return nil
}

// staticInt is the integer type of a value's static type, if it is one.
func staticInt(v *ir.Value) (IntTy, bool) {
	p, ok := v.Type.(*resolve.Prim)
	if !ok || !resolve.IsInteger(p) {
		return 0, false
	}
	return tyOf(p.Name), true
}

// compileInstr is in's op, or nil for an instruction without effect.
func compileInstr(fi *funcInfo, in ir.Instr) op {
	switch i := in.(type) {
	case *ir.UnsafeBegin, *ir.UnsafeEnd, *ir.Init:
		return nil
	case *ir.Const:
		d := i.Dst.ID
		if d < len(fi.consts) {
			if v := fi.consts[d]; v != nil {
				return func(_ *Interp, fr *frame) { fr.vals[d] = v }
			}
		}
	case *ir.Load:
		d := i.Dst.ID
		if id, ok := plainLocal(i.Place); ok {
			return func(_ *Interp, fr *frame) { fr.vals[d] = copyValue(fr.locals[id]) }
		}
		if pl := compilePlace(i.Place); pl != nil {
			return func(it *Interp, fr *frame) {
				r, seq := pl(it, fr)
				if seq != nil {
					fr.vals[d] = &Access{Seq: seq}
					return
				}
				fr.vals[d] = copyValue(r.get())
			}
		}
	case *ir.Store:
		src := i.Src.ID
		if id, ok := plainLocal(i.Place); ok {
			return func(_ *Interp, fr *frame) { fr.locals[id] = fr.vals[src] }
		}
		if pl := compilePlace(i.Place); pl != nil {
			return func(it *Interp, fr *frame) {
				r, _ := pl(it, fr)
				r.set(fr.vals[src])
			}
		}
	case *ir.AccessBegin:
		if pl := compilePlace(i.Place); pl != nil {
			d, edit := i.Dst.ID, i.Edit
			return func(it *Interp, fr *frame) {
				r, seq := pl(it, fr)
				fr.vals[d] = &Access{Ref: r, Seq: seq, Edit: edit}
			}
		}
	case *ir.AccessEnd:
		if i.Release == "" {
			return nil // ending an access without a release does nothing
		}
	case *ir.Drop:
		if pl := compilePlace(i.Place); pl != nil {
			t := i.Type
			return func(it *Interp, fr *frame) {
				r, _ := pl(it, fr)
				it.destroy(r.get(), t)
				r.set(nil)
			}
		}
	case *ir.Move:
		if pl := compilePlace(i.Src); pl != nil {
			d := i.Dst.ID
			return func(it *Interp, fr *frame) {
				r, _ := pl(it, fr)
				fr.vals[d] = r.get()
				r.set(nil)
			}
		}
	case *ir.Conv:
		d, x, to, trunc, sat := i.Dst.ID, i.X.ID, i.To, i.Truncate, i.Saturate
		return func(it *Interp, fr *frame) { fr.vals[d] = it.conv(fr.vals[x], to, trunc, sat) }
	case *ir.Copy:
		d, src := i.Dst.ID, i.Src.ID
		return func(_ *Interp, fr *frame) { fr.vals[d] = copyValue(fr.vals[src]) }
	case *ir.Binary:
		if o := compileBinary(i); o != nil {
			return o
		}
	case *ir.CheckOverflow:
		if _, ok := staticInt(i.L); ok {
			l, r, opr, pos := i.L.ID, i.R.ID, i.Op, i.Pos
			return func(it *Interp, fr *frame) {
				a, ok := fr.vals[l].(Int)
				if !ok {
					return // floats never trap, as checkOverflow
				}
				if kind := arithTrap(opr, a, fr.vals[r].(Int)); kind != "" {
					it.trap(kind, fr.at(pos))
				}
			}
		}
	case *ir.CheckBounds:
		if i.Index != nil {
			idx, ln, pos := i.Index.ID, i.Len.ID, i.Pos
			return func(it *Interp, fr *frame) {
				n := fr.vals[ln].(Int).Bits
				if fr.vals[idx].(Int).Bits >= n {
					it.trap("bounds", fr.at(pos))
				}
			}
		}
	case *ir.Call:
		if o := compileCall(fi, i); o != nil {
			return o
		}
	}
	return generic(in)
}

// compileCall is a direct call with its target held: what exec does for
// a call whose target funcInfo resolved (not through a function value).
func compileCall(fi *funcInfo, i *ir.Call) op {
	if i.Fn != nil {
		return nil
	}
	target := fi.calls[i]
	argIDs := make([]int, len(i.Args))
	for k, a := range i.Args {
		argIDs[k] = a.ID
	}
	dsts := make([]int, len(i.Dsts))
	for k, d := range i.Dsts {
		dsts[k] = d.ID
	}
	pos, typeArgs, callee, name := i.Pos, i.TypeArgs, target.callee, i.Callee
	store := func(fr *frame, rets []Value) {
		for k, d := range dsts {
			if k < len(rets) {
				fr.vals[d] = rets[k]
			}
		}
	}
	args := func(fr *frame) []Value {
		out := make([]Value, len(argIDs))
		for k, a := range argIDs {
			out[k] = fr.vals[a]
		}
		return out
	}
	switch {
	case i.Intrinsic != "":
		fn := target.intrinsic
		if fn == nil {
			return nil // exec panics with the intrinsic's name
		}
		return func(it *Interp, fr *frame) {
			store(fr, fn(it, args(fr), callCtx{pos: fr.at(pos), targs: typeArgs}))
		}
	case callee != nil && callee.Library:
		// entering (or staying in) the library: traps report this call
		return func(it *Interp, fr *frame) {
			a := args(fr)
			p := fr.at(pos)
			store(fr, it.callAt(callee, a, &p))
		}
	case callee != nil:
		return func(it *Interp, fr *frame) { store(fr, it.callAt(callee, args(fr), nil)) }
	}
	return func(it *Interp, fr *frame) { store(fr, it.callNamed(name, args(fr))) }
}

// compileBinary is integer arithmetic or comparison with its operation
// chosen once: binary's Int case for the operator and the operands'
// static signedness. An operand of another type at run time goes to
// exec.
func compileBinary(i *ir.Binary) op {
	ty, ok := staticInt(i.L)
	if !ok {
		return nil
	}
	f := intBinary(i.Op, signed(ty), i.Mode)
	if f == nil {
		return nil
	}
	in := ir.Instr(i)
	d, l, r := i.Dst.ID, i.L.ID, i.R.ID
	return func(it *Interp, fr *frame) {
		a, oka := fr.vals[l].(Int)
		b, okb := fr.vals[r].(Int)
		if !oka || !okb || a.Ty != ty {
			it.exec(fr, in)
			return
		}
		fr.vals[d] = f(a, b)
	}
}

// intBinary is binary's Int case for one operator and signedness, or nil
// for an operator binary does not apply to integers.
func intBinary(opr string, isSigned bool, mode string) func(a, b Int) Value {
	if isSigned {
		switch opr {
		case "+":
			return func(a, b Int) Value { return intVal(a.Ty, uint64(signedOf(a)+signedOf(b))) }
		case "-":
			return func(a, b Int) Value { return intVal(a.Ty, uint64(signedOf(a)-signedOf(b))) }
		case "*":
			return func(a, b Int) Value { return intVal(a.Ty, uint64(signedOf(a)*signedOf(b))) }
		case "/":
			return func(a, b Int) Value { return intVal(a.Ty, uint64(signedOf(a)/signedOf(b))) }
		case "%":
			return func(a, b Int) Value { return intVal(a.Ty, uint64(signedOf(a)%signedOf(b))) }
		case "<<":
			return func(a, b Int) Value { return intVal(a.Ty, uint64(signedOf(a)<<shiftCount(b.Bits, a.Ty, mode))) }
		case ">>":
			return func(a, b Int) Value { return intVal(a.Ty, uint64(signedOf(a)>>shiftCount(b.Bits, a.Ty, mode))) }
		case "==":
			return func(a, b Int) Value { return Bool(signedOf(a) == signedOf(b)) }
		case "!=":
			return func(a, b Int) Value { return Bool(signedOf(a) != signedOf(b)) }
		case "<":
			return func(a, b Int) Value { return Bool(signedOf(a) < signedOf(b)) }
		case "<=":
			return func(a, b Int) Value { return Bool(signedOf(a) <= signedOf(b)) }
		case ">":
			return func(a, b Int) Value { return Bool(signedOf(a) > signedOf(b)) }
		case ">=":
			return func(a, b Int) Value { return Bool(signedOf(a) >= signedOf(b)) }
		}
	}
	switch opr {
	case "+":
		return func(a, b Int) Value { return intVal(a.Ty, a.Bits+b.Bits) }
	case "-":
		return func(a, b Int) Value { return intVal(a.Ty, a.Bits-b.Bits) }
	case "*":
		return func(a, b Int) Value { return intVal(a.Ty, a.Bits*b.Bits) }
	case "/":
		return func(a, b Int) Value { return intVal(a.Ty, a.Bits/b.Bits) }
	case "%":
		return func(a, b Int) Value { return intVal(a.Ty, a.Bits%b.Bits) }
	case "<<":
		return func(a, b Int) Value { return intVal(a.Ty, a.Bits<<shiftCount(b.Bits, a.Ty, mode)) }
	case ">>":
		return func(a, b Int) Value { return intVal(a.Ty, a.Bits>>shiftCount(b.Bits, a.Ty, mode)) }
	case "&":
		return func(a, b Int) Value { return intVal(a.Ty, a.Bits&b.Bits) }
	case "|":
		return func(a, b Int) Value { return intVal(a.Ty, a.Bits|b.Bits) }
	case "^":
		return func(a, b Int) Value { return intVal(a.Ty, a.Bits^b.Bits) }
	case "==":
		return func(a, b Int) Value { return Bool(a.Bits == b.Bits) }
	case "!=":
		return func(a, b Int) Value { return Bool(a.Bits != b.Bits) }
	case "<":
		return func(a, b Int) Value { return Bool(a.Bits < b.Bits) }
	case "<=":
		return func(a, b Int) Value { return Bool(a.Bits <= b.Bits) }
	case ">":
		return func(a, b Int) Value { return Bool(a.Bits > b.Bits) }
	case ">=":
		return func(a, b Int) Value { return Bool(a.Bits >= b.Bits) }
	}
	return nil
}

// run executes a compiled function body from its entry block: callAt's
// block loop over ops (R1).
func (it *Interp) run(fr *frame, code []*cblock, entry *ir.Block) []Value {
	cb := code[entry.ID]
	for {
		if it.sh.stop.Load() {
			panic(exitCode(StoppedStatus))
		}
		fr.block = cb.b
		for _, o := range cb.ops {
			o(it, fr)
		}
		switch cb.kind {
		case termBr:
			cb = cb.next
		case termCondBr:
			if fr.vals[cb.cond].(Bool) {
				cb = cb.then
			} else {
				cb = cb.els
			}
		case termSwitch:
			tag := int(fr.vals[cb.tag].(Int).Bits)
			if nb, ok := cb.cases[tag]; ok {
				cb = nb
			} else {
				cb = cb.def
			}
		case termRet:
			out := make([]Value, len(cb.rets))
			for i, v := range cb.rets {
				out[i] = fr.vals[v]
			}
			return out
		case termUnreachable:
			it.trap("unreachable", fr.at(cb.unreachPos))
		default:
			panic("interp: block without terminator")
		}
	}
}
