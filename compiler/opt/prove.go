package opt

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
// moved from or accessed, by view or edit) holds that value wherever it is
// read, the checker having rejected reads before the store; and the length
// of an access value never changes. An access opened on another access denotes
// the same place (a reborrow), so places are resolved through those before
// they are named. Nothing else is assumed: a wrapping addition proves
// nothing, a store through an access or a call forgets every local it may
// reach (a call may write through any access it receives, an edit or a
// view of an Interior value), and a global is opaque.
// readOnlyIntrinsics read through their access argument and write nothing,
// so they forget no fact.
var readOnlyIntrinsics = map[string]bool{"load_be": true, "load_le": true}

func Prove(mod *ir.Module) {
	for _, f := range mod.Funcs {
		if !f.Inlined {
			prove(f)
		}
	}
}

func prove(f *ir.Func) {
	if f.Entry == nil {
		return
	}
	p := &prover{f: f, def: map[*ir.Value]ir.Instr{}, equiv: map[*ir.Value]*ir.Value{}, edits: map[*ir.Local]bool{}, accessed: map[*ir.Local]bool{}, alias: map[*ir.Value][]*ir.Value{}, clen: map[*ir.Value]uint64{}, out: map[*ir.Block]*facts{}, single: map[*ir.Local]*ir.Value{}, alens: map[string]*ir.Value{}, reborrow: map[*ir.Value]ir.Place{}}
	stores := map[*ir.Local]int{}
	unstable := map[*ir.Local]bool{}
	for _, b := range f.Blocks {
		for _, in := range b.Instrs {
			for _, d := range defs(in) {
				p.def[d] = in
			}
			switch i := in.(type) {
			case *ir.AccessBegin:
				p.reborrow[i.Dst] = i.Place
				if base := p.resolve(i.Place); base.Local != nil {
					// any access to the local makes it unstable: a callee may
					// write through an edit, or through a view of an Interior
					// value, so its stored value is not the value it holds
					unstable[base.Local] = true
					p.accessed[base.Local] = true
					if i.Edit {
						p.edits[base.Local] = true
					}
				}
			case *ir.Call:
				// a returned projection may alias the access argument it was
				// derived from (the checker requires exactly one access source
				// for a returned access, and the validator roots it there; every
				// access argument is kept as a candidate regardless): it is
				// never the same place, only something a write through it may
				// reach
				for _, d := range i.Dsts {
					if _, isAcc := d.Type.(*resolve.Access); !isAcc {
						continue
					}
					for _, a := range i.Args {
						if _, ok := a.Type.(*resolve.Access); ok {
							p.alias[d] = append(p.alias[d], a)
						}
					}
				}
			case *ir.Store:
				// Init is not a write: it marks the place initialized by the
				// store before it (or by a call through an access, which
				// excludes the local above) and has no runtime effect
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
	f        *ir.Func
	def      map[*ir.Value]ir.Instr    // the defining instruction of every value
	equiv    map[*ir.Value]*ir.Value   // a loaded or length value known equal to an earlier one
	edits    map[*ir.Local]bool        // locals an edit access is opened on somewhere in the function
	accessed map[*ir.Local]bool        // locals any access is opened on somewhere in the function
	alias    map[*ir.Value][]*ir.Value // a returned projection: the access arguments it may alias (invalidation only, never identity)
	clen     map[*ir.Value]uint64      // length values of arrays, from their types
	out      map[*ir.Block]*facts      // the state at the end of each processed block
	single   map[*ir.Local]*ir.Value   // locals stored exactly once: the value they hold everywhere
	alens    map[string]*ir.Value      // the first Len of each place rooted at an access value
	reborrow map[*ir.Value]ir.Place    // the place each access value denotes: what it was opened on
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

// invalidateAccessed forgets every local any access was opened on: an
// access of unknown origin (a parameter, a projection a call returned
// without an access argument) may reach any of them, through an edit or a
// view of an Interior value.
func (p *prover) invalidateAccessed(s *facts) {
	for l := range p.accessed {
		s.invalidate(l)
	}
}

// forgetReached forgets what a write through the access may reach: the
// local it resolves to; for a returned projection, every local any of the
// access arguments it may alias resolves to, transitively; and every
// accessed local as soon as one origin is unknown.
func (p *prover) forgetReached(s *facts, a *ir.Value) {
	seen := map[*ir.Value]bool{}
	var walk func(a *ir.Value) bool
	walk = func(a *ir.Value) bool {
		if seen[a] {
			return true
		}
		seen[a] = true
		if _, root := p.placeKey(ir.Place{Access: a}); root != nil {
			s.invalidate(root)
			return true
		}
		cands, ok := p.alias[a]
		if !ok {
			return false // a parameter, or a projection with no access argument: unknown
		}
		known := true
		for _, c := range cands {
			if !walk(c) {
				known = false
			}
		}
		return known
	}
	if !walk(a) {
		p.invalidateAccessed(s)
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
		if key, root := p.placeKey(i.Place); root != nil {
			if v, known := s.locals[key]; known {
				p.equiv[i.Dst] = v
			} else {
				s.locals[key] = i.Dst
			}
		}
	case *ir.Store:
		key, root := p.placeKey(i.Place)
		if root == nil {
			p.forgetReached(s, i.Place.Access)
			return
		}
		s.invalidate(root)
		s.locals[key] = p.canon(i.Src)
	case *ir.Move:
		if _, root := p.placeKey(i.Src); root != nil {
			s.invalidate(root)
			return
		}
		p.forgetReached(s, i.Src.Access)
	case *ir.Drop:
		if _, root := p.placeKey(i.Place); root != nil {
			s.invalidate(root)
			return
		}
		p.forgetReached(s, i.Place.Access)
	case *ir.Init:
		if _, root := p.placeKey(i.Place); root != nil {
			s.invalidate(root)
			return
		}
		p.forgetReached(s, i.Place.Access)
	case *ir.AccessBegin:
		p.reborrow[i.Dst] = i.Place // the access denotes the place it was opened on
		if _, root := p.placeKey(i.Place); root != nil && i.Edit {
			s.invalidate(root)
		}
	case *ir.Call:
		if readOnlyIntrinsics[i.Intrinsic] {
			return
		}
		// the callee may write through any access it receives: an edit, or a
		// view of an Interior value (unsafe code in its declaring module);
		// an argument whose origin is unknown may reach any accessed local
		for _, a := range i.Args {
			if _, isAcc := a.Type.(*resolve.Access); isAcc {
				p.forgetReached(s, a)
			}
		}
		p.invalidateEdits(s)
	case *ir.Len:
		place := p.resolve(i.Place)
		if n, ok := arrayLen(place); ok {
			p.clen[i.Dst] = n
		}
		key, root := p.placeKey(i.Place)
		if key == "" {
			return
		}
		if root == nil {
			// rooted at an access value (a parameter or a returned
			// projection): immutable, so the length holds everywhere
			if v, known := p.alens[key]; known {
				p.equiv[i.Dst] = v
			} else {
				p.alens[key] = i.Dst
			}
			return
		}
		if v, known := s.lens[key]; known {
			p.equiv[i.Dst] = v
		} else {
			s.lens[key] = i.Dst
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
// of `a < b` or `a > b`, the Else edge of `a >= b` or `a <= b`, and the
// strict bound a literal gives on the other edges, on unsigned operands.
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
	case b == cb.Else && cmp.Op == "<", b == cb.Then && cmp.Op == ">=":
		// `len >= K` (the Else edge of `len < K`, the Then edge of `len >= K`)
		// with a literal K >= 1 establishes K - 1 < len
		if k, ok := p.constant(p.canon(cmp.R)); ok && k >= 1 {
			s.addLt(nil, k-1, p.canon(cmp.L))
		}
		return
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

// resolve follows access values to what they were opened on (a local, or
// another access: a reborrow), so a place rooted at one is the opened
// place extended by the path; a parameter access, or a projection a call
// returned (which may be a sub-slice or a field of its argument), has no
// exact origin and stays as it is.
func (p *prover) resolve(pl ir.Place) ir.Place {
	for pl.Access != nil {
		base, ok := p.reborrow[pl.Access]
		if !ok {
			return pl
		}
		path := append(append([]ir.PathElem(nil), base.Path...), pl.Path...)
		pl = ir.Place{Local: base.Local, Access: base.Access, Global: base.Global, Path: path}
	}
	return pl
}

// placeKey names a place after resolving reborrows: "l<id>|path" with the
// root local when it reaches one, "a<id>|path" with no root when it stays
// rooted at an access value (a parameter or a returned projection), and
// "" for a global.
func (p *prover) placeKey(pl ir.Place) (string, *ir.Local) {
	pl = p.resolve(pl)
	switch {
	case pl.Local != nil:
		return fmt.Sprintf("l%d|", pl.Local.ID) + p.pathKey(pl.Path), pl.Local
	case pl.Access != nil:
		return fmt.Sprintf("a%d|", pl.Access.ID) + p.pathKey(pl.Path), nil
	}
	return "", nil
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
