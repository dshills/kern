package opt

// pmap is the proof pass's persistent map (specs/prove-state R1): an
// immutable hash-array-mapped trie. Every operation returns a new map
// that shares what it did not change with the old one, so taking a
// block's state from its predecessor's costs nothing, and the meet of
// two states that diverged from one returns every subtree they still
// share as it is (R2).
//
// A key is hashed by the type parameter H, a zero-size type with one
// method, so that a map is a comparable value (a root and nothing else)
// and can itself be a map's value. Successive 5-bit chunks of the 64-bit
// hash index the levels; below the last chunk the keys whose hashes are
// all equal share one node, searched in order.

import (
	"math/bits"

	"github.com/dshills/kern/compiler/ir"
)

// hasher hashes keys of type K. Equal keys must have equal hashes.
type hasher[K comparable] interface{ hash(K) uint64 }

type pmap[K comparable, V comparable, H hasher[K]] struct {
	root *pnode[K, V]
}

// pnode is a trie node: the slots of the set bits of bitmap, in bit
// order, each an entry or a subtree; at the bottom (shift >= 64) a list
// of entries with one hash and no bitmap. size counts the entries below.
// A slot points at its entry, so that copying a node on a write moves
// two words a slot and the entries it keeps stay shared.
// owner is the transient that made the node (see token), or nil.
type pnode[K comparable, V comparable] struct {
	bitmap uint32
	size   int
	slots  []pslot[K, V]
	owner  *token
}

// token identifies a transient: a state that may change in place the
// nodes it made itself, because nothing else can reach them yet. A write
// under a token copies a node made by anything else (and owns the copy)
// and changes a node it owns in place, so a run of writes to one state
// copies each node at most once. A token is retired, and its nodes
// become immutable, once the state is shared (facts.child, snapshot);
// the nil token owns nothing, so a write under it always copies.
type token struct{ _ byte }

func newToken() *token { return &token{} }

func (n *pnode[K, V]) ownedBy(tok *token) bool { return tok != nil && n.owner == tok }

type pslot[K comparable, V comparable] struct {
	sub *pnode[K, V]  // a subtree, or nil for an entry
	e   *pentry[K, V] // the entry, when sub is nil
}

type pentry[K comparable, V comparable] struct {
	h uint64
	k K
	v V
}

func entrySlot[K comparable, V comparable](h uint64, k K, v V) pslot[K, V] {
	return pslot[K, V]{e: &pentry[K, V]{h: h, k: k, v: v}}
}

// keepEntry is a slot for e's key bound to v: e itself when v is e's.
func keepEntry[K comparable, V comparable](e *pentry[K, V], v V) pslot[K, V] {
	if e.v == v {
		return pslot[K, V]{e: e}
	}
	return entrySlot(e.h, e.k, v)
}

const pmapBits = 4

func (m pmap[K, V, H]) hashOf(k K) uint64 {
	var h H
	return h.hash(k)
}

// size is the number of entries.
func (m pmap[K, V, H]) size() int {
	if m.root == nil {
		return 0
	}
	return m.root.size
}

func chunk(h uint64, shift uint) uint32 { return uint32(1) << ((h >> shift) & (1<<pmapBits - 1)) }

func slotIndex(bitmap, bit uint32) int { return bits.OnesCount32(bitmap & (bit - 1)) }

// get looks k up.
func (m pmap[K, V, H]) get(k K) (V, bool) {
	return lookup(m.root, 0, m.hashOf(k), k)
}

func lookup[K comparable, V comparable](n *pnode[K, V], shift uint, h uint64, k K) (V, bool) {
	var zero V
	for n != nil {
		if shift >= 64 {
			for _, s := range n.slots {
				if s.e.k == k {
					return s.e.v, true
				}
			}
			return zero, false
		}
		bit := chunk(h, shift)
		if n.bitmap&bit == 0 {
			return zero, false
		}
		s := &n.slots[slotIndex(n.bitmap, bit)]
		if s.sub == nil {
			if s.e.k == k {
				return s.e.v, true
			}
			return zero, false
		}
		n, shift = s.sub, shift+pmapBits
	}
	return zero, false
}

// set is the map with k bound to v; the map itself when it already is.
func (m pmap[K, V, H]) set(k K, v V) pmap[K, V, H] { return m.setT(k, v, nil) }

// setT is set under a transient's token: nodes tok owns change in place.
func (m pmap[K, V, H]) setT(k K, v V, tok *token) pmap[K, V, H] {
	return pmap[K, V, H]{root: insert(m.root, 0, m.hashOf(k), k, v, tok)}
}

func leafNode[K comparable, V comparable](shift uint, h uint64, k K, v V, tok *token) *pnode[K, V] {
	n := &pnode[K, V]{size: 1, slots: []pslot[K, V]{entrySlot(h, k, v)}, owner: tok}
	if shift < 64 {
		n.bitmap = chunk(h, shift)
	}
	return n
}

// withSlot is n with slot i replaced by s and its size set: n itself
// when tok owns it, else a copy tok owns.
func withSlot[K comparable, V comparable](n *pnode[K, V], i int, s pslot[K, V], size int, tok *token) *pnode[K, V] {
	if n.ownedBy(tok) {
		n.slots[i], n.size = s, size
		return n
	}
	slots := make([]pslot[K, V], len(n.slots))
	copy(slots, n.slots)
	slots[i] = s
	return &pnode[K, V]{bitmap: n.bitmap, size: size, slots: slots, owner: tok}
}

// withInserted is n with s inserted at slot i (of bit, 0 at the bottom).
func withInserted[K comparable, V comparable](n *pnode[K, V], i int, bit uint32, s pslot[K, V], tok *token) *pnode[K, V] {
	if n.ownedBy(tok) {
		n.slots = append(n.slots, pslot[K, V]{})
		copy(n.slots[i+1:], n.slots[i:])
		n.slots[i] = s
		n.bitmap |= bit
		n.size++
		return n
	}
	slots := make([]pslot[K, V], len(n.slots)+1)
	copy(slots, n.slots[:i])
	slots[i] = s
	copy(slots[i+1:], n.slots[i:])
	return &pnode[K, V]{bitmap: n.bitmap | bit, size: n.size + 1, slots: slots, owner: tok}
}

func insert[K comparable, V comparable](n *pnode[K, V], shift uint, h uint64, k K, v V, tok *token) *pnode[K, V] {
	if n == nil {
		return leafNode(shift, h, k, v, tok)
	}
	if shift >= 64 {
		for i, s := range n.slots {
			if s.e.k == k {
				if s.e.v == v {
					return n
				}
				return withSlot(n, i, entrySlot(h, k, v), n.size, tok)
			}
		}
		return withInserted(n, len(n.slots), 0, entrySlot(h, k, v), tok)
	}
	bit := chunk(h, shift)
	i := slotIndex(n.bitmap, bit)
	if n.bitmap&bit == 0 {
		return withInserted(n, i, bit, entrySlot(h, k, v), tok)
	}
	s := n.slots[i]
	switch {
	case s.sub != nil:
		// a subtree tok owns may grow in place: its pointer stays, its size
		// does not
		old := s.sub.size
		sub := insert(s.sub, shift+pmapBits, h, k, v, tok)
		if sub == s.sub && sub.size == old {
			return n
		}
		return withSlot(n, i, pslot[K, V]{sub: sub}, n.size-old+sub.size, tok)
	case s.e.k == k:
		if s.e.v == v {
			return n
		}
		return withSlot(n, i, entrySlot(h, k, v), n.size, tok)
	}
	// two keys in one slot: a subtree holds both
	leaf := &pnode[K, V]{size: 1, slots: []pslot[K, V]{s}, owner: tok}
	if shift+pmapBits < 64 {
		leaf.bitmap = chunk(s.e.h, shift+pmapBits)
	}
	sub := insert(leaf, shift+pmapBits, h, k, v, tok)
	return withSlot(n, i, pslot[K, V]{sub: sub}, n.size+1, tok)
}

// del is the map without k; the map itself when k is absent.
func (m pmap[K, V, H]) del(k K) pmap[K, V, H] { return m.delT(k, nil) }

// delT is del under a transient's token.
func (m pmap[K, V, H]) delT(k K, tok *token) pmap[K, V, H] {
	return pmap[K, V, H]{root: remove(m.root, 0, m.hashOf(k), k, tok)}
}

func remove[K comparable, V comparable](n *pnode[K, V], shift uint, h uint64, k K, tok *token) *pnode[K, V] {
	if n == nil {
		return nil
	}
	if shift >= 64 {
		for i, s := range n.slots {
			if s.e.k == k {
				return without(n, i, 0, n.size-1, tok)
			}
		}
		return n
	}
	bit := chunk(h, shift)
	if n.bitmap&bit == 0 {
		return n
	}
	i := slotIndex(n.bitmap, bit)
	s := n.slots[i]
	if s.sub == nil {
		if s.e.k != k {
			return n
		}
		return without(n, i, bit, n.size-1, tok)
	}
	old := s.sub.size
	sub := remove(s.sub, shift+pmapBits, h, k, tok)
	switch {
	case sub == nil:
		return without(n, i, bit, n.size-old, tok)
	case sub == s.sub && sub.size == old:
		return n
	}
	return withSlot(n, i, pslot[K, V]{sub: sub}, n.size-old+sub.size, tok)
}

// without is n with slot i (of bit, 0 at the bottom) removed, or nil when
// it was the last.
func without[K comparable, V comparable](n *pnode[K, V], i int, bit uint32, size int, tok *token) *pnode[K, V] {
	if len(n.slots) == 1 {
		return nil
	}
	if n.ownedBy(tok) {
		copy(n.slots[i:], n.slots[i+1:])
		n.slots[len(n.slots)-1] = pslot[K, V]{} // the removed entry can be collected
		n.slots = n.slots[:len(n.slots)-1]
		n.bitmap &^= bit
		n.size = size
		return n
	}
	slots := make([]pslot[K, V], 0, len(n.slots)-1)
	slots = append(slots, n.slots[:i]...)
	slots = append(slots, n.slots[i+1:]...)
	return &pnode[K, V]{bitmap: n.bitmap &^ bit, size: size, slots: slots, owner: tok}
}

// each calls f on every entry, in hash order.
func (m pmap[K, V, H]) each(f func(K, V)) { walk(m.root, f) }

func walk[K comparable, V comparable](n *pnode[K, V], f func(K, V)) {
	if n == nil {
		return
	}
	for _, s := range n.slots {
		if s.sub != nil {
			walk(s.sub, f)
		} else {
			f(s.e.k, s.e.v)
		}
	}
}

// intersect is the map of the keys both maps hold, each bound to what
// combine returns for the two values, a key dropped when it reports
// false. combine(k, v, v) must be (v, true): a subtree the two maps share
// is the result's as it is, and a node of a whose entries all survive
// unchanged is reused.
func intersect[K comparable, V comparable, H hasher[K]](a, b pmap[K, V, H], combine func(K, V, V) (V, bool)) pmap[K, V, H] {
	return pmap[K, V, H]{root: meetNodes(a.root, b.root, 0, combine)}
}

func meetNodes[K comparable, V comparable](a, b *pnode[K, V], shift uint, combine func(K, V, V) (V, bool)) *pnode[K, V] {
	if a == b {
		return a
	}
	if a == nil || b == nil {
		return nil
	}
	if shift >= 64 {
		var slots []pslot[K, V]
		for _, s := range a.slots {
			for _, t := range b.slots {
				if s.e.k == t.e.k {
					if v, ok := combine(s.e.k, s.e.v, t.e.v); ok {
						slots = append(slots, keepEntry(s.e, v))
					}
					break
				}
			}
		}
		return listNode(a, slots)
	}
	common := a.bitmap & b.bitmap
	if common == 0 {
		return nil
	}
	// the result is built on the stack: most meets return a or b as it
	// is, and only a node that is neither is allocated
	var buf [1 << pmapBits]pslot[K, V]
	slots := buf[:0]
	var bitmap uint32
	size := 0
	for rest := common; rest != 0; rest &= rest - 1 {
		bit := rest & -rest
		sa := a.slots[slotIndex(a.bitmap, bit)]
		sb := b.slots[slotIndex(b.bitmap, bit)]
		var out pslot[K, V]
		keep := false
		switch {
		case sa.sub != nil && sb.sub != nil:
			if sub := meetNodes(sa.sub, sb.sub, shift+pmapBits, combine); sub != nil {
				out, keep = pslot[K, V]{sub: sub}, true
			}
		case sa.sub == nil && sb.sub == nil:
			if sa.e.k == sb.e.k {
				if v, ok := combine(sa.e.k, sa.e.v, sb.e.v); ok {
					out, keep = keepEntry(sa.e, v), true
					if v != sa.e.v && v == sb.e.v {
						out = pslot[K, V]{e: sb.e}
					}
				}
			}
		case sa.sub == nil:
			if vb, ok := lookup(sb.sub, shift+pmapBits, sa.e.h, sa.e.k); ok {
				if v, ok := combine(sa.e.k, sa.e.v, vb); ok {
					out, keep = keepEntry(sa.e, v), true
				}
			}
		default:
			if va, ok := lookup(sa.sub, shift+pmapBits, sb.e.h, sb.e.k); ok {
				if v, ok := combine(sb.e.k, va, sb.e.v); ok {
					out, keep = keepEntry(sb.e, v), true
				}
			}
		}
		if !keep {
			continue
		}
		slots = append(slots, out)
		bitmap |= bit
		if out.sub != nil {
			size += out.sub.size
		} else {
			size++
		}
	}
	if len(slots) == 0 {
		return nil
	}
	if bitmap == a.bitmap && sameSlots(a.slots, slots) {
		return a
	}
	if bitmap == b.bitmap && sameSlots(b.slots, slots) {
		return b
	}
	return &pnode[K, V]{bitmap: bitmap, size: size, slots: append([]pslot[K, V](nil), slots...)}
}

// listNode is a bottom node of slots, a itself when they are a's.
func listNode[K comparable, V comparable](a *pnode[K, V], slots []pslot[K, V]) *pnode[K, V] {
	if len(slots) == 0 {
		return nil
	}
	if sameSlots(a.slots, slots) {
		return a
	}
	return &pnode[K, V]{size: len(slots), slots: slots}
}

func sameSlots[K comparable, V comparable](x, y []pslot[K, V]) bool {
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i].sub != y[i].sub || (x[i].sub == nil && (x[i].e.k != y[i].e.k || x[i].e.v != y[i].e.v)) {
			return false
		}
	}
	return true
}

// equal reports whether two maps hold the same keys with values eq
// reports equal.
func equal[K comparable, V comparable, H hasher[K]](a, b pmap[K, V, H], eq func(V, V) bool) bool {
	if a.root == b.root {
		return true
	}
	if a.size() != b.size() {
		return false
	}
	same := true
	a.each(func(k K, v V) {
		if !same {
			return
		}
		w, ok := b.get(k)
		same = ok && eq(v, w)
	})
	return same
}

// ---- the proof pass's hashes ----

// mix is splitmix64's finalizer: it spreads an ID over all 64 bits.
func mix(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	return x ^ x>>31
}

type valueHash struct{}

func (valueHash) hash(v *ir.Value) uint64 {
	if v == nil {
		return mix(1 << 62)
	}
	return mix(uint64(int64(v.ID))) //nolint:gosec // an ID, reinterpreted
}

type localHash struct{}

func (localHash) hash(l *ir.Local) uint64 { return mix(uint64(int64(l.ID))) } //nolint:gosec // as above

type ltHash struct{}

func (ltHash) hash(k ltKey) uint64 {
	return valueHash{}.hash(k.base)*31 ^ valueHash{}.hash(k.ln)
}

// strHash is 64-bit FNV-1a.
type strHash struct{}

func (strHash) hash(s string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}
