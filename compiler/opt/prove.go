package opt

import (
	"strconv"
	"strings"

	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/resolve"
)

// prove marks the bounds checks a function's own control flow proves
// redundant (spec §7 invariant 3, ADR-008 invariant 4;
// specs/prove-guards/SPEC.md). The backend leaves a proven check out, the
// interpreter keeps executing it (so the differential harness would expose
// a wrong proof), and show-bounds-checks reports it with its proof fact.
//
//   - dominated_by_check: the index, or the slice's bounds, are covered by
//     an earlier check or a branch condition on the same length that every
//     path to the check passes through.
//   - constant: a literal index, or literal slice bounds, into an array
//     whose length its type gives; or an index bounded by a constant below
//     that length (a mask, a remainder by a literal, a local every store
//     to which was so bounded: specs/prove-masks, specs/prove-bounds).
//
// The pass is a forward must-analysis: the facts at a block are those that
// hold on every path to it, the intersection of its predecessors' facts
// with what each incoming edge establishes, computed to a fixed point
// through loops (rounds that only remove facts, bounded by maxRounds with
// a pessimistic final round past the bound). A proof is assigned in a
// marking pass over the stable facts only.
//
// Facts relate values: an index is read as a base plus a constant offset
// (a checked addition or subtraction of a literal, so `i + 1 < n` covers
// `i` and `i + 1`, and `i < n` covers `i - 1`); a load of a place that
// holds a known value is that value; two values computed by the same pure
// operation on the same operands are the same value; the length of an
// access referent never changes; an array's length is its type's, so a
// literal equal to it names the same length; and an equality guard on two
// lengths makes them one. A comparison establishes its strict bound on
// the strict edge, and `x <= y` the bound `x - 1 < y` when `x` is an
// offset of at least one. Nothing else is assumed: a wrapping operation is
// not read as an offset, a signed comparison proves nothing, a store
// through an access or a call forgets every value a local it may reach
// held (but not the lengths of arrays and sequences, which are fixed), and
// a thread_local is opaque.
//
// readOnlyIntrinsics read through their access argument and write nothing,
// so they forget no fact.
var readOnlyIntrinsics = map[string]bool{"load_be": true, "load_le": true,
	"saturating_add": true, "saturating_sub": true, "saturating_mul": true,
	"checked_add": true, "checked_sub": true, "checked_mul": true, "checked_div": true, "checked_rem": true, "checked_shl": true, "checked_shr": true}

// maxRounds bounds the fixed-point rounds of a function; a variable so
// that a test can force the pessimistic fallback.
var maxRounds = 8

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
	p := &prover{f: f, def: map[*ir.Value]ir.Instr{}, gequiv: map[*ir.Value]*ir.Value{}, edits: map[*ir.Local]bool{}, accessed: map[*ir.Local]bool{}, alias: map[*ir.Value][]*ir.Value{}, clen: map[*ir.Value]uint64{}, single: map[*ir.Local]*ir.Value{}, alens: map[string]*ir.Value{}, reborrow: map[*ir.Value]ir.Place{}, closureCaps: map[*ir.Value][]*ir.Value{}, constLens: map[uint64]*ir.Value{}, ubVals: map[uint64]*ir.Value{}, ubOf: map[*ir.Value]uint64{}, loadUb: map[*ir.Value]uint64{}}
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
			case *ir.MakeClosure:
				p.closureCaps[i.Dst] = i.Captures
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
			case *ir.Len:
				if n, ok := arrayLen(p.resolve(i.Place)); ok {
					p.clen[i.Dst] = n
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
	rpo := postorder(f.Entry)
	order := map[*ir.Block]int{}
	for i, b := range rpo {
		order[b] = i
	}
	preds := map[*ir.Block][]*ir.Block{}
	for _, b := range rpo {
		for _, s := range successors(b.Term) {
			preds[s] = append(preds[s], b)
		}
	}
	headers := map[*ir.Block]bool{}
	for _, b := range rpo {
		for _, pr := range preds[b] {
			if order[pr] >= order[b] {
				headers[b] = true
			}
		}
	}
	// entry computes a block's state from its predecessors: those already
	// visited in the round through out, a back edge through prev (nil to
	// leave it out), or no facts for a header when pessimistic
	entry := func(b *ir.Block, out, prev map[*ir.Block]*facts, pessimistic bool) *facts {
		var st *facts
		if !pessimistic || !headers[b] {
			for _, pr := range preds[b] {
				var po *facts
				if order[pr] < order[b] {
					po = out[pr]
				} else if prev != nil {
					po = prev[pr]
				} else {
					continue
				}
				e := po.child()
				p.edgeFacts(e, pr, b)
				if st == nil {
					st = e
				} else {
					st = meet(st, e, p)
				}
			}
		}
		if st == nil {
			st = newFacts()
		}
		return st
	}
	// the rounds: in round one a back edge (a predecessor later in reverse
	// postorder, a loop header's) is left out, the optimistic start; from
	// round two every predecessor counts, a back edge with the previous
	// round's state. Facts only shrink, so a state changed when it lost an
	// entry, and the rounds end when none did, at the sound fixed point.
	// A function without a loop settles in round one.
	// Only a header's entry state can differ between rounds (every other
	// block is computed from the headers and from earlier blocks of the
	// same round), so the headers' entry states, compared whole, decide
	// convergence.
	var prev map[*ir.Block]*facts
	headerState := map[*ir.Block]snapshot{}
	converged := len(headers) == 0
	pessimistic := false
	if converged {
		// no loop: one pass is the fixed point, and it can mark as it goes
		out := map[*ir.Block]*facts{}
		for _, b := range rpo {
			st := entry(b, out, nil, false)
			p.block(st, b, true)
			out[b] = st
		}
		return
	}
	for round := 1; round <= maxRounds; round++ {
		out := map[*ir.Block]*facts{}
		changed := round == 1
		for _, b := range rpo {
			st := entry(b, out, prev, false)
			if headers[b] {
				snap := st.snapshot()
				if round > 1 && !snap.same(headerState[b]) {
					changed = true
				}
				headerState[b] = snap
			}
			p.block(st, b, false)
			out[b] = st
		}
		prev = out
		if !changed || converged {
			converged = true
			break
		}
	}
	if !converged {
		pessimistic = true // past the bound: headers start from no facts
	}
	// the marking pass: each block's state from the converged (or
	// pessimistic) predecessors, with proofs assigned
	out := map[*ir.Block]*facts{}
	for _, b := range rpo {
		st := entry(b, out, prev, pessimistic)
		p.block(st, b, true)
		out[b] = st
	}
}

type prover struct {
	f        *ir.Func
	def      map[*ir.Value]ir.Instr    // the defining instruction of every value
	gequiv   map[*ir.Value]*ir.Value   // path-independent equalities: the first Len of an access referent
	edits    map[*ir.Local]bool        // locals an edit access is opened on somewhere in the function
	accessed map[*ir.Local]bool        // locals any access is opened on somewhere in the function
	alias    map[*ir.Value][]*ir.Value // a returned projection: the access arguments it may alias (invalidation only, never identity)
	clen     map[*ir.Value]uint64      // length values of arrays, from their types
	single   map[*ir.Local]*ir.Value   // locals stored exactly once: the value they hold everywhere
	alens    map[string]*ir.Value      // the first Len of each place rooted at an access value or a read-only global
	reborrow map[*ir.Value]ir.Place    // the place each access value denotes: what it was opened on
	// closureCaps are the capture accesses of each closure value: a call
	// that receives the closure may write through them
	closureCaps map[*ir.Value][]*ir.Value
	constLens   map[uint64]*ir.Value // one synthetic length value per array length (never in the IR)
	// upper bounds kept on places (specs/prove-bounds): a place's bound is
	// stored in the locals map under its key prefixed by ubPrefix, as the
	// synthetic value of that constant, so that it is forgotten, met
	// and compared with what the place holds; the meet keeps the larger
	ubVals map[uint64]*ir.Value
	ubOf   map[*ir.Value]uint64
	// loadUb is the bound of a load's value: the bound its place held at
	// the load in the latest visit of the load (a value is defined once, so
	// its uses, later in the same pass, see the bound of that visit)
	loadUb map[*ir.Value]uint64
}

// ubPrefix marks a locals key that holds a place's upper bound rather
// than its value.
const ubPrefix = "^"

// ubVal is the synthetic value naming the bound c.
func (p *prover) ubVal(c uint64) *ir.Value {
	if v, ok := p.ubVals[c]; ok {
		return v
	}
	v := &ir.Value{ID: -1_000_000 - len(p.ubVals), Type: &resolve.Prim{Name: "u64"}}
	p.ubVals[c], p.ubOf[v] = v, c
	return v
}

// ltKey is a bound fact's key: the index base (nil for a literal) and the
// length key it is below.
type ltKey struct{ base, ln *ir.Value }

// facts is the state at a program point: what holds on every path to it
// (specs/prove-state). Each kind of fact is a persistent map, so taking a
// block's state as a child of its predecessor's copies six roots, a write
// replaces a root, and the meet of two paths that diverged from one state
// returns every subtree they still share as it is (pmap.go).
type facts struct {
	lt     pmap[ltKey, int64, ltHash]            // the largest k with base + k < ln known (k < 0 when only a subtraction is known); never removed, only raised
	locals pmap[*ir.Local, pathMap, localHash]   // the value a place rooted at a local holds, by path
	lens   pmap[*ir.Local, pathMap, localHash]   // the value Len yielded for a place rooted at a local, by path
	equiv  pmap[*ir.Value, *ir.Value, valueHash] // a value known equal to an earlier one (loads, lengths, recomputation); never removed
	vn     pmap[string, *ir.Value, strHash]      // value numbering: a pure operation on canonical operands, to its first value; never removed
	rep    pmap[*ir.Value, *ir.Value, valueHash] // same-length classes: a length key to its representative
}

// pathMap is what one local's places hold, by path.
type pathMap = pmap[string, *ir.Value, strHash]

// newFacts is the empty state.
func newFacts() *facts { return &facts{} }

// child is a state to extend from s, which stays as it is.
func (s *facts) child() *facts {
	c := *s
	return &c
}

// ---- lookups and updates ----

func (s *facts) getLt(k ltKey) (int64, bool) { return s.lt.get(k) }

func (s *facts) putLt(k ltKey, v int64) { s.lt = s.lt.set(k, v) }

func (s *facts) addLt(base *ir.Value, off int64, ln *ir.Value) {
	key := ltKey{base, ln}
	if cur, ok := s.getLt(key); !ok || off > cur {
		s.putLt(key, off)
	}
}

func (s *facts) getLocal(root *ir.Local, path string) (*ir.Value, bool) {
	m, ok := s.locals.get(root)
	if !ok {
		return nil, false
	}
	return m.get(path)
}

func (s *facts) setLocal(root *ir.Local, path string, v *ir.Value) {
	m, _ := s.locals.get(root)
	s.locals = s.locals.set(root, m.set(path, v))
}

func (s *facts) getLen(root *ir.Local, path string) (*ir.Value, bool) {
	m, ok := s.lens.get(root)
	if !ok {
		return nil, false
	}
	return m.get(path)
}

func (s *facts) setLen(root *ir.Local, path string, v *ir.Value) {
	m, _ := s.lens.get(root)
	s.lens = s.lens.set(root, m.set(path, v))
}

func (s *facts) getEquiv(v *ir.Value) (*ir.Value, bool) { return s.equiv.get(v) }

func (s *facts) putEquiv(k, v *ir.Value) { s.equiv = s.equiv.set(k, v) }

func (s *facts) getVn(k string) (*ir.Value, bool) { return s.vn.get(k) }

func (s *facts) putVn(k string, v *ir.Value) { s.vn = s.vn.set(k, v) }

// invalidate forgets what places rooted at a local hold and, unless
// keepLens, the lengths they yielded. A write through an access or a call
// cannot change the length of an array (its type's) or of a sequence (its
// storage's), only a store, move or drop of the root can.
func (s *facts) invalidate(l *ir.Local, keepLens bool) {
	s.locals = s.locals.del(l)
	if !keepLens {
		s.lens = s.lens.del(l)
	}
}

// ---- comparing states across rounds ----

// snapshot is a state as it stands, for comparing a header's entry state
// between rounds: the maps are immutable, so a copy of the roots is the
// whole state.
type snapshot = facts

func (s *facts) snapshot() snapshot { return *s }

// same reports whether two states hold exactly the same facts: the same
// keys with the same bounds, values and class representatives (the
// representatives are canonical, the member with the smallest ID).
func (s facts) same(o facts) bool {
	eqVal := func(x, y *ir.Value) bool { return x == y }
	eqPaths := func(x, y pathMap) bool { return equal(x, y, eqVal) }
	return equal(s.lt, o.lt, func(x, y int64) bool { return x == y }) &&
		equal(s.equiv, o.equiv, eqVal) && equal(s.vn, o.vn, eqVal) && equal(s.rep, o.rep, eqVal) &&
		equal(s.locals, o.locals, eqPaths) && equal(s.lens, o.lens, eqPaths)
}

// ---- same-length classes ----

func (s *facts) find(v *ir.Value) *ir.Value {
	if r, ok := s.rep.get(v); ok {
		return r
	}
	return v
}

// members lists ln's same-length class, ln itself included.
func (s *facts) members(ln *ir.Value) []*ir.Value {
	if s.rep.size() == 0 {
		return []*ir.Value{ln}
	}
	r := s.find(ln)
	if r == ln {
		if _, ok := s.rep.get(ln); !ok {
			return []*ir.Value{ln}
		}
	}
	out := []*ir.Value{}
	s.rep.each(func(v, rv *ir.Value) {
		if rv == r {
			out = append(out, v)
		}
	})
	if len(out) == 0 {
		return []*ir.Value{ln}
	}
	return out
}

// union puts two length keys in one class.
func (s *facts) union(a, b *ir.Value) {
	if a == b {
		return
	}
	ra, rb := s.find(a), s.find(b)
	if ra == rb {
		return
	}
	r := ra
	if rb.ID < ra.ID {
		r = rb
	}
	var moved []*ir.Value
	s.rep.each(func(v, rv *ir.Value) {
		if rv == ra || rv == rb {
			moved = append(moved, v)
		}
	})
	for _, v := range moved {
		s.rep = s.rep.set(v, r)
	}
	s.rep = s.rep.set(a, r).set(b, r).set(r, r)
}

// bound is the largest known k with base + k < ln, through ln's class.
func (s *facts) bound(base *ir.Value, ln *ir.Value) (int64, bool) {
	best, found := int64(0), false
	for _, m := range s.members(ln) {
		if k, ok := s.getLt(ltKey{base, m}); ok && (!found || k > best) {
			best, found = k, true
		}
	}
	return best, found
}

// ---- the meet ----

// meet is the intersection of two states: what both know, with the
// smaller bound, and two length keys in one class only when both states
// have them in one class (specs/prove-state R3). Every map meets by
// pmap intersection, so what the two states share costs nothing.
func meet(a, b *facts, ub *prover) *facts {
	sameVal := func(_ *ir.Value, x, y *ir.Value) (*ir.Value, bool) { return x, x == y }
	samePath := func(_ string, x, y *ir.Value) (*ir.Value, bool) { return x, x == y }
	c := &facts{
		lt:    intersect(a.lt, b.lt, func(_ ltKey, x, y int64) (int64, bool) { return min(x, y), true }),
		equiv: intersect(a.equiv, b.equiv, sameVal),
		vn:    intersect(a.vn, b.vn, func(_ string, x, y *ir.Value) (*ir.Value, bool) { return x, x == y }),
	}
	// a place holds what both sides hold for it; a bound on either path:
	// the larger holds on both
	localPath := func(path string, x, y *ir.Value) (*ir.Value, bool) {
		if x == y {
			return x, true
		}
		if ub != nil && strings.HasPrefix(path, ubPrefix) {
			bx, okx := ub.ubOf[x]
			by, oky := ub.ubOf[y]
			if okx && oky {
				return ub.ubVal(max(bx, by)), true
			}
		}
		return nil, false
	}
	c.locals = intersect(a.locals, b.locals, func(_ *ir.Local, x, y pathMap) (pathMap, bool) {
		m := intersect(x, y, localPath)
		return m, m.size() > 0
	})
	c.lens = intersect(a.lens, b.lens, func(_ *ir.Local, x, y pathMap) (pathMap, bool) {
		m := intersect(x, y, samePath)
		return m, m.size() > 0
	})
	c.rep = meetClasses(a.rep, b.rep)
	return c
}

// meetClasses keeps two length keys in one class when both sides have
// them in one class, the class's member of smallest ID its representative.
func meetClasses(ra, rb pmap[*ir.Value, *ir.Value, valueHash]) pmap[*ir.Value, *ir.Value, valueHash] {
	if ra.root == rb.root {
		return ra
	}
	groups := map[[2]*ir.Value][]*ir.Value{}
	var keys [][2]*ir.Value
	ra.each(func(v, x *ir.Value) {
		if y, ok := rb.get(v); ok {
			key := [2]*ir.Value{x, y}
			if groups[key] == nil {
				keys = append(keys, key)
			}
			groups[key] = append(groups[key], v)
		}
	})
	var out pmap[*ir.Value, *ir.Value, valueHash]
	for _, key := range keys {
		members := groups[key]
		if len(members) < 2 {
			continue
		}
		r := members[0]
		for _, m := range members[1:] {
			if m.ID < r.ID {
				r = m
			}
		}
		for _, m := range members {
			out = out.set(m, r)
		}
	}
	return out
}

// invalidateEdits forgets every local an edit access may reach: a store
// through an access, or a call, may have written it.
func (p *prover) invalidateEdits(s *facts) {
	for l := range p.edits {
		s.invalidate(l, true)
	}
}

// invalidateAccessed forgets every local any access was opened on: an
// access of unknown origin (a parameter, a projection a call returned
// without an access argument) may reach any of them, through an edit or a
// view of an Interior value.
func (p *prover) invalidateAccessed(s *facts) {
	for l := range p.accessed {
		s.invalidate(l, true)
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
			s.invalidate(root, true)
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

// block applies a block's instructions to its state.
func (p *prover) block(s *facts, b *ir.Block, mark bool) {
	var prev ir.Instr
	for _, i := range b.Instrs {
		p.instr(s, i, prev, mark)
		prev = i
	}
}

// instr applies one instruction; prev is the one before it in the block,
// or nil.
func (p *prover) instr(s *facts, in, prev ir.Instr, mark bool) {
	switch i := in.(type) {
	case *ir.Load:
		if i.Place.Local != nil && len(i.Place.Path) == 0 {
			if v, ok := p.single[i.Place.Local]; ok {
				s.putEquiv(i.Dst, p.canon(s, v))
				return
			}
		}
		if key, root := p.placeKey(i.Place); root != nil {
			if b, ok := s.getLocal(root, ubPrefix+key); ok {
				p.loadUb[i.Dst] = p.ubOf[b]
			} else {
				delete(p.loadUb, i.Dst)
			}
			if v, known := s.getLocal(root, key); known {
				s.putEquiv(i.Dst, v)
			} else {
				s.setLocal(root, key, i.Dst)
			}
		}
	case *ir.Store:
		key, root := p.placeKey(i.Place)
		if root == nil {
			p.forgetReached(s, i.Place.Access)
			return
		}
		// a store to an element of the local changes nothing's length; a
		// store to the local itself (or a field) replaces what it holds
		s.invalidate(root, len(i.Place.Path) > 0 && i.Place.Path[len(i.Place.Path)-1].Kind == ir.PathIndex)
		s.setLocal(root, key, p.canon(s, i.Src))
		if c, ok := p.upper(s, i.Src); ok {
			s.setLocal(root, ubPrefix+key, p.ubVal(c))
		}
	case *ir.Move:
		if _, root := p.placeKey(i.Src); root != nil {
			s.invalidate(root, false)
			return
		}
		p.forgetReached(s, i.Src.Access)
	case *ir.Drop:
		if _, root := p.placeKey(i.Place); root != nil {
			s.invalidate(root, false)
			return
		}
		p.forgetReached(s, i.Place.Access)
	case *ir.Init:
		if key, root := p.placeKey(i.Place); root != nil {
			// the bound of the store just made survives its init marker: a
			// bound names a constant, never a value, so it does not
			// unsettle the solver as a remembered value would
			b, keep := s.getLocal(root, ubPrefix+key)
			st, ok := prev.(*ir.Store)
			keep = keep && ok && st.Place.Local == root && p.pathKey(st.Place.Path) == p.pathKey(i.Place.Path)
			s.invalidate(root, false)
			if keep {
				s.setLocal(root, ubPrefix+key, b)
			}
			return
		}
		p.forgetReached(s, i.Place.Access)
	case *ir.AccessBegin:
		if _, root := p.placeKey(i.Place); root != nil && i.Edit {
			s.invalidate(root, true)
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
			for _, c := range p.closureCaps[a] {
				p.forgetReached(s, c) // the callee may call the closure, which writes through its captures
			}
		}
		p.invalidateEdits(s)
	case *ir.MakeClosure:
		// the closure may write through its edit captures at any call
		// through it; nothing is assumed about them from here on. Forgetting
		// at creation covers the call: while a capture is open the checker
		// admits no write to its place from this function (a view capture
		// conflicts with an edit, an edit capture with everything), so no
		// fact about the place can be learned between creation and the
		// call that the closure's writes would stale
		for _, a := range i.Captures {
			p.forgetReached(s, a)
		}
		p.invalidateEdits(s)
	case *ir.Len:
		key, root := p.placeKey(i.Place)
		if key == "" {
			return
		}
		if root == nil {
			// rooted at an access value (a parameter or a returned
			// projection) or a read-only global: immutable, so the length
			// holds everywhere
			if v, known := p.alens[key]; known {
				p.gequiv[i.Dst] = v
			} else {
				p.alens[key] = i.Dst
			}
			return
		}
		if v, known := s.getLen(root, key); known {
			s.putEquiv(i.Dst, v)
		} else {
			s.setLen(root, key, i.Dst)
		}
	case *ir.Binary:
		if unsigned(i.Dst.Type) {
			p.number(s, i.Dst, "b|"+i.Op+"|"+i.Mode+"|"+strconv.Itoa(p.canon(s, i.L).ID)+"|"+strconv.Itoa(p.canon(s, i.R).ID))
		}
	case *ir.Conv:
		if unsigned(i.Dst.Type) {
			p.number(s, i.Dst, "cv|"+i.To+"|"+strconv.FormatBool(i.Truncate)+"|"+strconv.Itoa(p.canon(s, i.X).ID))
		}
	case *ir.Const:
		if c, ok := p.constant(i.Dst); ok {
			p.number(s, i.Dst, "c|"+i.Dst.Type.String()+"|"+strconv.FormatUint(c, 10))
		}
	case *ir.CheckBounds:
		if i.Index != nil {
			p.indexCheck(s, i, mark)
		} else {
			p.sliceCheck(s, i, mark)
		}
	}
}

// number records a pure computation's value: the first value computed by
// the operation on the same canonical operands names every later one.
func (p *prover) number(s *facts, dst *ir.Value, key string) {
	if first, ok := s.getVn(key); ok && first != dst {
		s.putEquiv(dst, first)
		return
	}
	s.putVn(key, dst)
}

// indexCheck proves an index check from the facts, when mark is set, and
// records that the index is in range past it.
func (p *prover) indexCheck(s *facts, i *ir.CheckBounds, mark bool) {
	base, off := p.index(s, i.Index)
	ln := p.lenKey(s, i.Len)
	if mark && i.Prov.Label != "proven" {
		if base == nil && off >= 0 {
			if n, ok := p.classLen(s, ln); ok && uint64(off) < n {
				i.Prov = ir.Provenance{Label: "proven", ProvenBy: "constant"}
			}
		}
		// a bounded index (specs/prove-masks, specs/prove-bounds): at most
		// a constant plus a positive offset, below a known length
		// (constant) or a literal a guard put below the length
		// (dominated_by_check); tried before the general guard rule so
		// that a bounded index under a guard is labelled constant
		if c, bounded := p.upper(s, base); i.Prov.Label != "proven" && bounded {
			m := c
			if off > 0 {
				m += uint64(off) // c <= 2^62 and off <= 2^62: no overflow
			}
			if n, ok := p.classLen(s, ln); ok && m < n {
				i.Prov = ir.Provenance{Label: "proven", ProvenBy: "constant"}
			} else if k, ok := s.bound(nil, ln); ok && k >= 0 && uint64(k) >= m {
				i.Prov = ir.Provenance{Label: "proven", ProvenBy: "dominated_by_check"}
			}
		}
		if i.Prov.Label != "proven" {
			if k, ok := s.bound(base, ln); ok && k >= off {
				i.Prov = ir.Provenance{Label: "proven", ProvenBy: "dominated_by_check"}
			}
		}
		if i.Prov.Label != "proven" && p.maskedBelow(s, base, off, ln) {
			i.Prov = ir.Provenance{Label: "proven", ProvenBy: "dominated_by_check"}
		}
	}
	s.addLt(base, off, ln) // past the check (proven or passed), the index is in range
}

// upper is a constant v is known not to exceed (specs/prove-masks,
// specs/prove-bounds), on unsigned values:
//   - a literal is its own bound;
//   - a bitwise and is at most either operand's bound;
//   - a remainder by a literal c >= 1 is at most c - 1 (by zero it traps);
//   - a conversion to an unsigned type keeps its operand's bound (one that
//     passes its range check keeps the value, a truncation only lowers it);
//   - a load is at most the bound its place held: every value stored to
//     the place on every path to the load had a bound, the larger kept.
//
// A value's range from its type (a u8's 255) is not a bound: the fourth
// agent suite counts on byte-indexed tables staying checked.
func (p *prover) upper(s *facts, v *ir.Value) (uint64, bool) {
	return p.upperMemo(s, v, map[*ir.Value]ubResult{}, 0)
}

// ubResult is a memoized bound: an operand shared by several operations
// (x & x) is searched once, so the search is linear in the values it
// reaches.
type ubResult struct {
	c  uint64
	ok bool
}

func (p *prover) upperMemo(s *facts, v *ir.Value, memo map[*ir.Value]ubResult, depth int) (uint64, bool) {
	if v == nil || depth > 32 {
		return 0, false
	}
	v = p.canon(s, v)
	if r, ok := memo[v]; ok {
		return r.c, r.ok
	}
	c, ok := p.upperOf(s, v, memo, depth)
	memo[v] = ubResult{c, ok}
	return c, ok
}

func (p *prover) upperOf(s *facts, v *ir.Value, memo map[*ir.Value]ubResult, depth int) (uint64, bool) {
	if c, ok := p.constant(v); ok {
		return c, true
	}
	if c, ok := p.loadUb[v]; ok {
		return c, true
	}
	switch d := p.def[v].(type) {
	case *ir.Binary:
		if !unsigned(d.Dst.Type) {
			return 0, false
		}
		switch d.Op {
		case "&":
			a, oka := p.upperMemo(s, d.L, memo, depth+1)
			b, okb := p.upperMemo(s, d.R, memo, depth+1)
			switch {
			case oka && okb:
				return min(a, b), true
			case oka:
				return a, true
			case okb:
				return b, true
			}
		case "%":
			if c, ok := p.constant(p.canon(s, d.R)); ok && c >= 1 {
				if a, ok := p.upperMemo(s, d.L, memo, depth+1); ok && a < c-1 {
					return a, true
				}
				return c - 1, true
			}
		}
	case *ir.Conv:
		if unsigned(d.Dst.Type) && unsigned(d.X.Type) {
			return p.upperMemo(s, d.X, memo, depth+1)
		}
	}
	return 0, false
}

// maskedBelow reports that base + off is below ln because base is a
// bitwise and with an operand m = mb + mo below ln by more than off
// (specs/prove-bounds): a fact mb + k < ln with k >= mo + off, or mb a
// member of ln's class with mo + off < 0 (m = len - 1 was computed with a
// checked subtraction, so the length is at least one and m below it).
func (p *prover) maskedBelow(s *facts, base *ir.Value, off int64, ln *ir.Value) bool {
	if base == nil {
		return false
	}
	d, ok := p.def[p.canon(s, base)].(*ir.Binary)
	if !ok || d.Op != "&" || !unsigned(d.Dst.Type) {
		return false
	}
	for _, m := range []*ir.Value{d.L, d.R} {
		mb, mo := p.index(s, m)
		if mb == nil {
			continue // a literal: upper's rule
		}
		need := mo + off
		if (off >= 0 && need < mo) || (off < 0 && need > mo) {
			continue // overflow: no proof
		}
		if k, ok := s.bound(mb, ln); ok && k >= need {
			return true
		}
		if need < 0 && s.find(p.lenKey(s, mb)) == s.find(ln) {
			return true
		}
	}
	return false
}

// sliceCheck proves a slice check (spec F9): the high bound in range (the
// length itself, a literal under an array length, or an offset whose
// predecessor is a known index) and the low bound not above the high one.
func (p *prover) sliceCheck(s *facts, i *ir.CheckBounds, mark bool) {
	if i.Lo == nil || i.Hi == nil {
		return
	}
	ln := p.lenKey(s, i.Len)
	hb, ho := p.index(s, i.Hi)
	lb, lo := p.index(s, i.Lo)
	hiIsLen := p.lenKey(s, i.Hi) == ln && hb != nil && hb == p.canon(s, i.Hi)
	n, hasLen := p.classLen(s, ln)
	hiOK, hiConst := false, false
	switch {
	case hiIsLen:
		hiOK = true
	case hb == nil && ho >= 0 && hasLen && uint64(ho) <= n:
		hiOK, hiConst = true, true
	case ho >= 1:
		if k, ok := s.bound(hb, ln); ok && k >= ho-1 {
			hiOK = true
		}
	}
	loOK, loConst := false, false
	switch {
	case lb == nil && lo == 0:
		loOK, loConst = true, true
	case lb != nil && lb == hb && lo <= ho:
		loOK = true
	case lb == nil && hb == nil && lo <= ho:
		loOK, loConst = true, true
	case hiIsLen:
		if k, ok := s.bound(lb, ln); ok && k >= lo {
			loOK = true
		}
	}
	if mark && i.Prov.Label != "proven" && hiOK && loOK {
		by := "dominated_by_check"
		if hiConst && loConst && hasLen {
			by = "constant"
		}
		i.Prov = ir.Provenance{Label: "proven", ProvenBy: by}
	}
	// past the check, hi - 1 is a valid index when hi is an offset of at
	// least one (hi <= len)
	if ho >= 1 && (hb != nil || hasLen) {
		s.addLt(hb, ho-1, ln)
	}
}

// classLen is the array length known for any member of ln's class.
func (p *prover) classLen(s *facts, ln *ir.Value) (uint64, bool) {
	best, found := uint64(0), false
	for _, m := range s.members(ln) {
		if n, ok := p.clen[m]; ok && (!found || n < best) {
			best, found = n, true
		}
		if m.ID < 0 {
			// a synthetic constant-length value
			for n, cv := range p.constLens {
				if cv == m && (!found || n < best) {
					best, found = n, true
				}
			}
		}
	}
	return best, found
}

// lenKey is the key facts about a length are recorded under: the
// canonical value, or for an array length or an unsigned literal the
// synthetic value of that constant length, so that `len(buf)` of a
// `[8]u32` and the literal 8 are one length. Lookups go through the
// same-length class of the key.
func (p *prover) lenKey(s *facts, v *ir.Value) *ir.Value {
	v = p.canon(s, v)
	if n, ok := p.clen[v]; ok {
		return p.constLen(n)
	}
	if n, ok := p.constant(v); ok {
		return p.constLen(n)
	}
	return v
}

func (p *prover) constLen(n uint64) *ir.Value {
	if v, ok := p.constLens[n]; ok {
		return v
	}
	v := &ir.Value{ID: -1 - len(p.constLens), Type: &resolve.Prim{Name: "usize"}}
	p.constLens[n] = v
	return v
}

// edgeFacts adds what the branch from pred to b establishes, on unsigned
// operands (spec F3, F4): the strict bound on the strict edge of `<`, `>`,
// `>=` and `<=`; on the non-strict edge, `x <= y` with x an offset of at
// least one establishes x - 1 < y; `len >= K` and `len == K` with a
// literal K establish K - 1 < len; and `a == b` makes two lengths one.
func (p *prover) edgeFacts(s *facts, pred, b *ir.Block) {
	cb, ok := pred.Term.(*ir.CondBr)
	if !ok || cb.Then == cb.Else {
		return
	}
	cmp, ok := p.def[p.canon(s, cb.Cond)].(*ir.Binary)
	if !ok || !unsigned(cmp.L.Type) {
		return
	}
	then := b == cb.Then
	// normalise to a relation that holds on this edge: strict `lo < hi`,
	// non-strict `lo <= hi`, or `l == r`
	var lo, hi *ir.Value
	strict := false
	switch cmp.Op {
	case "<":
		lo, hi, strict = cmp.L, cmp.R, then
		if !then {
			lo, hi = cmp.R, cmp.L // R <= L
		}
	case ">":
		lo, hi, strict = cmp.R, cmp.L, then
		if !then {
			lo, hi = cmp.L, cmp.R // L <= R
		}
	case "<=":
		lo, hi, strict = cmp.L, cmp.R, !then
		if !then {
			lo, hi = cmp.R, cmp.L // R < L
		}
	case ">=":
		lo, hi, strict = cmp.R, cmp.L, !then
		if !then {
			lo, hi = cmp.L, cmp.R // L < R
		}
	case "==", "!=":
		if then == (cmp.Op == "!=") {
			return
		}
		l, r := p.canon(s, cmp.L), p.canon(s, cmp.R)
		if k, ok := p.constant(r); ok && k >= 1 {
			s.addLt(nil, int64(k-1), p.lenKey(s, l)) //nolint:gosec // k <= 2^63 is enforced by constant
		}
		if k, ok := p.constant(l); ok && k >= 1 {
			s.addLt(nil, int64(k-1), p.lenKey(s, r)) //nolint:gosec // as above
		}
		if _, lc := p.constant(l); !lc {
			if _, rc := p.constant(r); !rc {
				s.union(p.lenKey(s, l), p.lenKey(s, r))
			}
		}
		return
	default:
		return
	}
	base, off := p.index(s, lo)
	if strict {
		s.addLt(base, off, p.lenKey(s, hi))
		return
	}
	// lo <= hi: the bound lo - 1 < hi when lo is an offset of at least one
	if off >= 1 {
		s.addLt(base, off-1, p.lenKey(s, hi))
	}
}

// canon follows the equivalences recorded for the state and the
// path-independent ones (an access referent's length).
func (p *prover) canon(s *facts, v *ir.Value) *ir.Value {
	for n := 0; n < 64; n++ {
		w, ok := s.getEquiv(v)
		if !ok {
			w, ok = p.gequiv[v]
		}
		if !ok {
			return v
		}
		v = w
	}
	return v
}

// index reads an index value as base + off: a literal (base nil), a
// checked addition or subtraction of a literal on an unsigned value (which
// did not wrap, or it would have trapped before the value existed), or
// the value itself. The offset arithmetic refuses to overflow.
func (p *prover) index(s *facts, v *ir.Value) (*ir.Value, int64) {
	v = p.canon(s, v)
	if c, ok := p.constant(v); ok {
		return nil, int64(c) //nolint:gosec // constant refuses values past MaxInt64
	}
	if bin, ok := p.def[v].(*ir.Binary); ok && (bin.Op == "+" || bin.Op == "-") && bin.Mode == "" && unsigned(bin.L.Type) {
		var other *ir.Value
		var k uint64
		if c, ok := p.constant(p.canon(s, bin.R)); ok {
			other, k = bin.L, c
		} else if bin.Op == "+" {
			if c, ok := p.constant(p.canon(s, bin.L)); ok {
				other, k = bin.R, c
			}
		}
		if other != nil && k <= 1<<62 {
			base, off := p.index(s, other)
			d := int64(k) //nolint:gosec // bounded above
			if bin.Op == "-" {
				d = -d
			}
			if sum := off + d; (d >= 0 && sum >= off) || (d < 0 && sum < off) {
				return base, sum
			}
		}
	}
	return v, 0
}

// constant reports an unsigned integer literal that fits a non-negative
// int64 offset.
func (p *prover) constant(v *ir.Value) (uint64, bool) {
	c, ok := p.def[v].(*ir.Const)
	if !ok || c.Neg || c.Bool || c.Unit || c.Bytes != nil || !unsigned(v.Type) || c.Int > 1<<62 {
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
// rooted at an access value (a parameter or a returned projection),
// "g<id>|path" with no root for a read-only global (an aggregate constant,
// never written, so immutable like an access's referent), and "" for a
// thread_local.
func (p *prover) placeKey(pl ir.Place) (string, *ir.Local) {
	pl = p.resolve(pl)
	switch {
	case pl.Local != nil:
		return "l" + strconv.Itoa(pl.Local.ID) + "|" + p.pathKey(pl.Path), pl.Local
	case pl.Access != nil:
		return "a" + strconv.Itoa(pl.Access.ID) + "|" + p.pathKey(pl.Path), nil
	case pl.Global != nil && pl.Global.ReadOnly:
		return "g" + strconv.Itoa(pl.Global.ID) + "|" + p.pathKey(pl.Path), nil
	}
	return "", nil
}

// pathKey renders a path; an index or slice bound is named by its value's
// ID (the IR's, not a canonical one: the key must be path-independent).
func (p *prover) pathKey(path []ir.PathElem) string {
	if len(path) == 0 {
		return ""
	}
	var b []byte
	for _, e := range path {
		switch e.Kind {
		case ir.PathField:
			b = strconv.AppendInt(append(b, '.'), int64(e.Field), 10)
		case ir.PathVariant:
			b = strconv.AppendInt(append(b, ".v"...), int64(e.Tag), 10)
			b = strconv.AppendInt(append(b, '.'), int64(e.Field), 10)
		case ir.PathIndex:
			b = append(strconv.AppendInt(append(b, '['), int64(e.Index.ID), 10), ']')
		case ir.PathSlice:
			lo, hi := -1, -1
			if e.Lo != nil {
				lo = e.Lo.ID
			}
			if e.Hi != nil {
				hi = e.Hi.ID
			}
			b = strconv.AppendInt(append(b, '['), int64(lo), 10)
			b = append(strconv.AppendInt(append(b, ':'), int64(hi), 10), ']')
		}
	}
	return string(b)
}

// arrayLen is the length of a place whose type is a fixed-size array: a
// local or scalar access of array type, a read-only global, or a field or
// element path through structs and arrays to one.
func arrayLen(pl ir.Place) (uint64, bool) {
	var t resolve.Type
	switch {
	case pl.Local != nil:
		t = pl.Local.Type
		if a, ok := t.(*resolve.Access); ok {
			// a parameter local holds its access: the place is the referent
			if a.Seq {
				return 0, false
			}
			t = a.Elem
		}
	case pl.Access != nil:
		a, ok := pl.Access.Type.(*resolve.Access)
		if !ok || a.Seq {
			return 0, false
		}
		t = a.Elem
	case pl.Global != nil:
		t = pl.Global.Type
	default:
		return 0, false
	}
	for _, e := range pl.Path {
		if e.Kind == ir.PathIndex {
			// an element of an array: its type is the array's element type
			// whatever the index
			a, ok := t.(*resolve.Array)
			if !ok {
				return 0, false
			}
			t = a.Elem
			continue
		}
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
