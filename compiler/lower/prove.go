package lower

import (
	"fmt"

	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/resolve"
)

// prove marks the bounds checks a function's own control flow proves
// redundant (spec §7 invariant 3, ADR-008 invariant 4). The backend leaves a
// proven check out, the interpreter keeps executing it (so the differential
// harness would expose a wrong proof), and show-bounds-checks reports it
// with its proof fact.
//
//   - dominated_by_check: the index is covered by an earlier check, or by a
//     branch condition, on the same length that every path to the check
//     passes through. Facts flow only along single-predecessor edges (a
//     block with several predecessors, a loop head among them, starts with
//     none); values are related through the locals they were loaded from
//     and the places their lengths came from; and an index that is a
//     checked addition of a constant to a value is read as that value plus
//     the constant, so `i + 1 < n` covers `i` and `i + 1`.
//   - constant: a literal index into an array whose length its type gives.
//
// Two equivalences hold everywhere: a local stored exactly once (and never
// moved from or opened for edit) holds that value wherever it is read, the
// checker having rejected reads before the store; and the length of an
// access value never changes. Nothing else is assumed: a wrapping
// addition, a value loaded through an access, and whatever a call or a
// store through an edit access may have changed are opaque.
func prove(f *ir.Func) {
	if f.Entry == nil {
		return
	}
	p := &prover{f: f, def: map[*ir.Value]ir.Instr{}, equiv: map[*ir.Value]*ir.Value{}, edits: map[*ir.Local]bool{}, clen: map[*ir.Value]uint64{}, out: map[*ir.Block]*facts{}, single: map[*ir.Local]*ir.Value{}, alens: map[string]*ir.Value{}}
	stores := map[*ir.Local]int{}
	unstable := map[*ir.Local]bool{}
	for _, b := range f.Blocks {
		for _, in := range b.Instrs {
			for _, d := range defs(in) {
				p.def[d] = in
			}
			switch i := in.(type) {
			case *ir.AccessBegin:
				if i.Edit && i.Place.Local != nil {
					p.edits[i.Place.Local] = true
					unstable[i.Place.Local] = true
				}
			case *ir.Store:
				if i.Place.Local != nil {
					stores[i.Place.Local]++
					if len(i.Place.Path) == 0 && stores[i.Place.Local] == 1 {
						p.single[i.Place.Local] = i.Src
					} else {
						unstable[i.Place.Local] = true
					}
				}
			case *ir.Move:
				if i.Src.Local != nil {
					unstable[i.Src.Local] = true
				}
			}
		}
	}
	for l := range unstable {
		delete(p.single, l)
	}
	for _, l := range f.Params {
		delete(p.single, l) // a parameter arrives initialized: its store, if any, is a reassignment
	}
	preds := map[*ir.Block][]*ir.Block{}
	for _, b := range f.Blocks {
		for _, s := range successors(b.Term) {
			preds[s] = append(preds[s], b)
		}
	}
	for _, b := range postorder(f.Entry) {
		st := newFacts()
		if b != f.Entry && len(preds[b]) == 1 {
			if prev := p.out[preds[b][0]]; prev != nil {
				st = prev.clone()
				p.edgeFacts(st, preds[b][0], b)
			}
		}
		for _, in := range b.Instrs {
			p.instr(st, in)
		}
		p.out[b] = st
	}
}

type prover struct {
	f      *ir.Func
	def    map[*ir.Value]ir.Instr  // the defining instruction of every value
	equiv  map[*ir.Value]*ir.Value // a loaded or length value known equal to an earlier one
	edits  map[*ir.Local]bool      // locals an edit access is opened on somewhere in the function
	clen   map[*ir.Value]uint64    // length values of arrays, from their types
	out    map[*ir.Block]*facts    // the state at the end of each processed block
	single map[*ir.Local]*ir.Value // locals stored exactly once: the value they hold everywhere
	alens  map[string]*ir.Value    // the first Len of each place rooted at an access value
}

// facts is the state at a program point.
type facts struct {
	lt     map[[2]*ir.Value]uint64 // (base, len): the largest k with base + k < len known; base nil for a constant
	locals map[string]*ir.Value    // the value a place rooted at a local holds
	lens   map[string]*ir.Value    // the value Len yielded for a place
}

func newFacts() *facts {
	return &facts{lt: map[[2]*ir.Value]uint64{}, locals: map[string]*ir.Value{}, lens: map[string]*ir.Value{}}
}

func (s *facts) clone() *facts {
	c := newFacts()
	for k, v := range s.lt {
		c.lt[k] = v
	}
	for k, v := range s.locals {
		c.locals[k] = v
	}
	for k, v := range s.lens {
		c.lens[k] = v
	}
	return c
}

func (s *facts) addLt(base *ir.Value, off uint64, ln *ir.Value) {
	key := [2]*ir.Value{base, ln}
	if cur, ok := s.lt[key]; !ok || off > cur {
		s.lt[key] = off
	}
}

// invalidate forgets everything known about places rooted at a local.
func (s *facts) invalidate(l *ir.Local) {
	prefix := fmt.Sprintf("l%d|", l.ID)
	for k := range s.locals {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(s.locals, k)
		}
	}
	for k := range s.lens {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(s.lens, k)
		}
	}
}

// invalidateEdits forgets every local an edit access may reach: a store
// through an access, or a call, may have written it.
func (p *prover) invalidateEdits(s *facts) {
	for l := range p.edits {
		s.invalidate(l)
	}
}

func (p *prover) instr(s *facts, in ir.Instr) {
	switch i := in.(type) {
	case *ir.Load:
		if i.Place.Local != nil && len(i.Place.Path) == 0 {
			if v, ok := p.single[i.Place.Local]; ok {
				p.equiv[i.Dst] = p.canon(v)
				return
			}
		}
		if key, ok := p.localKey(i.Place); ok {
			if v, known := s.locals[key]; known {
				p.equiv[i.Dst] = v
			} else {
				s.locals[key] = i.Dst
			}
		}
	case *ir.Store:
		if i.Place.Local != nil {
			s.invalidate(i.Place.Local)
			if key, ok := p.localKey(i.Place); ok {
				s.locals[key] = p.canon(i.Src)
			}
			return
		}
		p.invalidateEdits(s)
	case *ir.Move:
		if i.Src.Local != nil {
			s.invalidate(i.Src.Local)
			return
		}
		p.invalidateEdits(s)
	case *ir.Drop:
		if i.Place.Local != nil {
			s.invalidate(i.Place.Local)
			return
		}
		p.invalidateEdits(s)
	case *ir.Init:
		if i.Place.Local != nil {
			s.invalidate(i.Place.Local)
		}
	case *ir.AccessBegin:
		if i.Edit && i.Place.Local != nil {
			s.invalidate(i.Place.Local)
		}
	case *ir.Call:
		p.invalidateEdits(s)
	case *ir.Len:
		if n, ok := arrayLen(i.Place); ok {
			p.clen[i.Dst] = n
		}
		if i.Place.Access != nil {
			key, _ := p.lenKey(i.Place)
			if v, known := p.alens[key]; known {
				p.equiv[i.Dst] = v
			} else {
				p.alens[key] = i.Dst
			}
			return
		}
		if key, ok := p.lenKey(i.Place); ok {
			if v, known := s.lens[key]; known {
				p.equiv[i.Dst] = v
			} else {
				s.lens[key] = i.Dst
			}
		}
	case *ir.CheckBounds:
		if i.Index == nil {
			return
		}
		base, off := p.index(i.Index)
		ln := p.canon(i.Len)
		if i.Prov.Label != "proven" {
			if base == nil {
				if n, ok := p.clen[ln]; ok && off < n {
					i.Prov = ir.Provenance{Label: "proven", ProvenBy: "constant"}
				}
			}
			if i.Prov.Label != "proven" {
				if k, ok := s.lt[[2]*ir.Value{base, ln}]; ok && k >= off {
					i.Prov = ir.Provenance{Label: "proven", ProvenBy: "dominated_by_check"}
				}
			}
		}
		s.addLt(base, off, ln) // past the check (proven or passed), the index is in range
	}
}

// edgeFacts adds what the branch from pred to b establishes: the Then edge
// of `a < b` or `a > b`, the Else edge of `a >= b` or `a <= b`, on
// unsigned operands.
func (p *prover) edgeFacts(s *facts, pred, b *ir.Block) {
	cb, ok := pred.Term.(*ir.CondBr)
	if !ok || cb.Then == cb.Else {
		return
	}
	cmp, ok := p.def[p.canon(cb.Cond)].(*ir.Binary)
	if !ok || !unsigned(cmp.L.Type) {
		return
	}
	var lo, hi *ir.Value
	switch {
	case b == cb.Then && cmp.Op == "<", b == cb.Else && cmp.Op == ">=":
		lo, hi = cmp.L, cmp.R
	case b == cb.Then && cmp.Op == ">", b == cb.Else && cmp.Op == "<=":
		lo, hi = cmp.R, cmp.L
	default:
		return
	}
	base, off := p.index(lo)
	s.addLt(base, off, p.canon(hi))
}

// canon follows the equivalences recorded for loaded and length values.
func (p *prover) canon(v *ir.Value) *ir.Value {
	for {
		w, ok := p.equiv[v]
		if !ok {
			return v
		}
		v = w
	}
}

// index reads an index value as base + off: a constant (base nil), a checked
// addition of a constant to a value (which did not wrap, or it would have
// trapped before the value existed), or the value itself.
func (p *prover) index(v *ir.Value) (*ir.Value, uint64) {
	v = p.canon(v)
	if c, ok := p.constant(v); ok {
		return nil, c
	}
	if add, ok := p.def[v].(*ir.Binary); ok && add.Op == "+" && add.Mode == "" && unsigned(add.L.Type) {
		var other *ir.Value
		var k uint64
		if c, ok := p.constant(p.canon(add.R)); ok {
			other, k = add.L, c
		} else if c, ok := p.constant(p.canon(add.L)); ok {
			other, k = add.R, c
		}
		if other != nil {
			base, off := p.index(other)
			if off+k >= off {
				return base, off + k
			}
		}
	}
	return v, 0
}

// constant reports an unsigned integer literal.
func (p *prover) constant(v *ir.Value) (uint64, bool) {
	c, ok := p.def[v].(*ir.Const)
	if !ok || c.Neg || c.Bool || c.Unit || c.Bytes != nil || !unsigned(v.Type) {
		return 0, false
	}
	return c.Int, true
}

// localKey names a place rooted at a local, with its path.
func (p *prover) localKey(pl ir.Place) (string, bool) {
	if pl.Local == nil {
		return "", false
	}
	return fmt.Sprintf("l%d|", pl.Local.ID) + p.pathKey(pl.Path), true
}

// lenKey names a place whose length is stable until its root changes: one
// rooted at a local, or at an access value (an immutable pointer and
// length pair).
func (p *prover) lenKey(pl ir.Place) (string, bool) {
	switch {
	case pl.Local != nil:
		return p.localKey(pl)
	case pl.Access != nil:
		return fmt.Sprintf("a%d|", pl.Access.ID) + p.pathKey(pl.Path), true
	}
	return "", false
}

func (p *prover) pathKey(path []ir.PathElem) string {
	s := ""
	for _, e := range path {
		switch e.Kind {
		case ir.PathField:
			s += fmt.Sprintf(".%d", e.Field)
		case ir.PathVariant:
			s += fmt.Sprintf(".v%d.%d", e.Tag, e.Field)
		case ir.PathIndex:
			s += fmt.Sprintf("[%d]", p.canon(e.Index).ID)
		case ir.PathSlice:
			lo, hi := -1, -1
			if e.Lo != nil {
				lo = p.canon(e.Lo).ID
			}
			if e.Hi != nil {
				hi = p.canon(e.Hi).ID
			}
			s += fmt.Sprintf("[%d:%d]", lo, hi)
		}
	}
	return s
}

// arrayLen is the length of a place whose type is a fixed-size array: a
// local or scalar access of array type, or a field path through structs
// to one.
func arrayLen(pl ir.Place) (uint64, bool) {
	var t resolve.Type
	switch {
	case pl.Local != nil:
		t = pl.Local.Type
	case pl.Access != nil:
		a, ok := pl.Access.Type.(*resolve.Access)
		if !ok || a.Seq {
			return 0, false
		}
		t = a.Elem
	default:
		return 0, false
	}
	for _, e := range pl.Path {
		if e.Kind != ir.PathField {
			return 0, false
		}
		n, ok := t.(*resolve.Named)
		if !ok || n.Decl.Kind != resolve.KindStruct || e.Field >= len(n.Decl.Fields) {
			return 0, false
		}
		args := map[string]resolve.Type{}
		for i, tp := range n.Decl.TypeParams {
			if i < len(n.Args) {
				args[tp.Name] = n.Args[i]
			}
		}
		t = resolve.Subst(n.Decl.Fields[e.Field].Type, args)
	}
	if arr, ok := t.(*resolve.Array); ok {
		return uint64(arr.Len), true
	}
	return 0, false
}

func unsigned(t resolve.Type) bool {
	p, ok := t.(*resolve.Prim)
	if !ok {
		return false
	}
	switch p.Name {
	case "u8", "u16", "u32", "u64", "usize":
		return true
	}
	return false
}

// defs lists the values an instruction defines.
func defs(in ir.Instr) []*ir.Value {
	switch i := in.(type) {
	case *ir.Const:
		return []*ir.Value{i.Dst}
	case *ir.Load:
		return []*ir.Value{i.Dst}
	case *ir.Move:
		return []*ir.Value{i.Dst}
	case *ir.AccessBegin:
		return []*ir.Value{i.Dst}
	case *ir.Call:
		return i.Dsts
	case *ir.Binary:
		return []*ir.Value{i.Dst}
	case *ir.Unary:
		return []*ir.Value{i.Dst}
	case *ir.Conv:
		return []*ir.Value{i.Dst}
	case *ir.Len:
		return []*ir.Value{i.Dst}
	case *ir.MakeStruct:
		return []*ir.Value{i.Dst}
	case *ir.MakeUnion:
		return []*ir.Value{i.Dst}
	case *ir.MakeArray:
		return []*ir.Value{i.Dst}
	case *ir.Zero:
		return []*ir.Value{i.Dst}
	case *ir.Tag:
		return []*ir.Value{i.Dst}
	case *ir.FuncRef:
		return []*ir.Value{i.Dst}
	case *ir.Copy:
		return []*ir.Value{i.Dst}
	}
	return nil
}

func successors(t ir.Term) []*ir.Block {
	switch x := t.(type) {
	case *ir.Br:
		return []*ir.Block{x.Target}
	case *ir.CondBr:
		return []*ir.Block{x.Then, x.Else}
	case *ir.Switch:
		var out []*ir.Block
		for _, b := range x.Cases {
			out = append(out, b)
		}
		if x.Default != nil {
			out = append(out, x.Default)
		}
		return out
	}
	return nil
}

// postorder lists the blocks reachable from entry in reverse postorder:
// every block after its non-back-edge predecessors.
func postorder(entry *ir.Block) []*ir.Block {
	var order []*ir.Block
	seen := map[*ir.Block]bool{}
	var visit func(b *ir.Block)
	visit = func(b *ir.Block) {
		if seen[b] {
			return
		}
		seen[b] = true
		for _, s := range successors(b.Term) {
			visit(s)
		}
		order = append(order, b)
	}
	visit(entry)
	for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
		order[i], order[j] = order[j], order[i]
	}
	return order
}
