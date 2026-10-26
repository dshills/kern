package interp

import (
	"fmt"
	"math"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/resolve"
)

// intrinsicFn implements a runtime-provided prelude function.
// callCtx carries the call's source position and type arguments.
type callCtx struct {
	pos   ast.Pos
	targs []resolve.Type
}

func (c callCtx) targ(i int) resolve.Type {
	if i < len(c.targs) && c.targs[i] != nil {
		return c.targs[i]
	}
	return &resolve.Unit{}
}

type intrinsicFn func(it *Interp, args []Value, c callCtx) []Value

func acc(v Value) *Access { return v.(*Access) }

func u64(v Value) uint64 { return v.(Int).Bits }

// intrinsics is the table of prelude functions (std/core.kern) provided by
// the interpreter in Phase 1.
var intrinsics = map[string]intrinsicFn{}

func init() {
	for k, v := range map[string]intrinsicFn{
		// output
		"print_u64": func(it *Interp, a []Value, _ callCtx) []Value { _, _ = fmt.Fprint(it.out, a[0].(Int).Bits); return nil },
		"print_i64": func(it *Interp, a []Value, _ callCtx) []Value {
			_, _ = fmt.Fprint(it.out, signedOf(a[0].(Int)))
			return nil
		},
		"print_bool": func(it *Interp, a []Value, _ callCtx) []Value { _, _ = fmt.Fprint(it.out, fmtValue(a[0])); return nil },
		"print_str": func(it *Interp, a []Value, _ callCtx) []Value {
			s := acc(a[0]).Seq
			b := make([]byte, s.length())
			for i := range b {
				b[i] = byte(s.at(i).get().(Int).Bits)
			}
			_, _ = fmt.Fprint(it.out, string(b))
			return nil
		},
		"println": func(it *Interp, a []Value, _ callCtx) []Value { _, _ = fmt.Fprintln(it.out); return nil },
		"zero":    func(it *Interp, a []Value, _ callCtx) []Value { return []Value{Unit{}} },
		"clone": func(it *Interp, a []Value, c callCtx) []Value {
			return []Value{deepClone(it, acc(a[0]).Ref.get(), c.pos)}
		},
		// Box
		"Box.new": func(it *Interp, a []Value, c callCtx) []Value {
			pos := c.pos
			it.allocOrTrap(pos)
			return []Value{&Box{Val: a[0]}}
		},
		"Box.get": func(it *Interp, a []Value, _ callCtx) []Value {
			b := acc(a[0]).Ref.get().(*Box)
			return []Value{&Access{Ref: slotRef{&b.Val}}}
		},
		"Box.at": func(it *Interp, a []Value, _ callCtx) []Value {
			b := acc(a[0]).Ref.get().(*Box)
			return []Value{&Access{Ref: slotRef{&b.Val}, Edit: true}}
		},
		"Box.into": func(it *Interp, a []Value, _ callCtx) []Value { b := a[0].(*Box); b.Dead = true; return []Value{b.Val} },
		// Vec
		"Vec.new": func(it *Interp, a []Value, _ callCtx) []Value { return []Value{&Vec{}} },
		"Vec.with_capacity": func(it *Interp, a []Value, _ callCtx) []Value {
			return []Value{&Vec{Items: make([]Value, 0, u64(a[0]))}}
		},
		"Vec.len": func(it *Interp, a []Value, _ callCtx) []Value {
			return []Value{mkInt("usize", uint64(len(vecOf(a[0]).Items)))}
		},
		"Vec.push": func(it *Interp, a []Value, c callCtx) []Value {
			pos := c.pos
			it.allocOrTrap(pos)
			v := vecOf(a[0])
			v.Items = append(v.Items, a[1])
			return nil
		},
		"Vec.pop": func(it *Interp, a []Value, _ callCtx) []Value {
			v := vecOf(a[0])
			if len(v.Items) == 0 {
				return []Value{&Union{Tag: 1}}
			}
			last := v.Items[len(v.Items)-1]
			v.Items[len(v.Items)-1] = nil
			v.Items = v.Items[:len(v.Items)-1]
			return []Value{&Union{Tag: 0, Fields: []Value{last}}}
		},
		"Vec.get": func(it *Interp, a []Value, c callCtx) []Value {
			pos := c.pos
			v := vecOf(a[0])
			if u64(a[1]) >= uint64(len(v.Items)) {
				it.trap("bounds", pos)
			}
			i := int(u64(a[1]))
			return []Value{&Access{Ref: idxRef{elems: func() []Value { return v.Items }, i: i}}}
		},
		"Vec.at": func(it *Interp, a []Value, c callCtx) []Value {
			pos := c.pos
			v := vecOf(a[0])
			if u64(a[1]) >= uint64(len(v.Items)) {
				it.trap("bounds", pos)
			}
			i := int(u64(a[1]))
			return []Value{&Access{Ref: idxRef{elems: func() []Value { return v.Items }, i: i}, Edit: true}}
		},
		"Vec.as_view": func(it *Interp, a []Value, _ callCtx) []Value {
			v := vecOf(a[0])
			return []Value{&Access{Seq: &seqRef{elems: func() []Value { return v.Items }, lo: 0, hi: len(v.Items)}}}
		},
		"Vec.as_edit": func(it *Interp, a []Value, _ callCtx) []Value {
			v := vecOf(a[0])
			return []Value{&Access{Seq: &seqRef{elems: func() []Value { return v.Items }, lo: 0, hi: len(v.Items)}, Edit: true}}
		},
		"Vec.clear": func(it *Interp, a []Value, _ callCtx) []Value {
			v := vecOf(a[0])
			for i := len(v.Items) - 1; i >= 0; i-- {
				it.destroy(v.Items[i], v.Elem)
				v.Items[i] = nil
			}
			v.Items = v.Items[:0]
			return nil
		},
		// Pool
		"Pool.new": func(it *Interp, a []Value, c callCtx) []Value {
			p := it.newPool()
			p.Elem = c.targ(0)
			return []Value{p}
		},
		"Pool.alloc": poolAlloc,
		"Pool.get": func(it *Interp, a []Value, c callCtx) []Value {
			pos := c.pos
			return []Value{poolRef(it, a[0], a[1], false, pos)}
		},
		"Pool.at": func(it *Interp, a []Value, c callCtx) []Value {
			pos := c.pos
			return []Value{poolRef(it, a[0], a[1], true, pos)}
		},
		"Pool.get2": func(it *Interp, a []Value, c callCtx) []Value {
			pos := c.pos
			x := poolRef(it, a[0], a[1], true, pos)
			y := poolRef(it, a[0], a[2], true, pos)
			if handleIndex(a[1]) == handleIndex(a[2]) {
				it.trap("disjoint", pos)
			}
			return []Value{x, y}
		},
		"Pool.free": poolFree,
		"Pool.len": func(it *Interp, a []Value, _ callCtx) []Value {
			p := acc(a[0]).Ref.get().(*Pool)
			n := 0
			for _, s := range p.Slots {
				if s.Live {
					n++
				}
			}
			return []Value{mkInt("usize", uint64(n))}
		},
		// Arena
		"Arena.new": func(it *Interp, a []Value, _ callCtx) []Value {
			ar := &Arena{Identity: it.nextIdent}
			it.nextIdent++
			return []Value{ar}
		},
		"Arena.alloc": func(it *Interp, a []Value, _ callCtx) []Value {
			ar := acc(a[0]).Ref.get().(*Arena)
			ar.Slots = append(ar.Slots, a[1])
			id := &Struct{Fields: []Value{mkInt("u32", uint64(len(ar.Slots)-1)), mkInt("u32", uint64(ar.Identity))}}
			return []Value{&Union{Tag: 0, Fields: []Value{id}}}
		},
		"Arena.get": func(it *Interp, a []Value, c callCtx) []Value {
			pos := c.pos
			return []Value{arenaRef(it, a[0], a[1], false, pos)}
		},
		"Arena.at": func(it *Interp, a []Value, c callCtx) []Value {
			pos := c.pos
			return []Value{arenaRef(it, a[0], a[1], true, pos)}
		},
		"Arena.len": func(it *Interp, a []Value, _ callCtx) []Value {
			return []Value{mkInt("usize", uint64(len(acc(a[0]).Ref.get().(*Arena).Slots)))}
		},
		// Shared / Mutex / Atomic
		"Shared.new": func(it *Interp, a []Value, _ callCtx) []Value { return []Value{&Shared{Val: a[0], Count: 1}} },
		"Shared.get": func(it *Interp, a []Value, _ callCtx) []Value {
			s := acc(a[0]).Ref.get().(*Shared)
			return []Value{&Access{Ref: slotRef{&s.Val}}}
		},
		"Shared.clone": func(it *Interp, a []Value, c callCtx) []Value {
			pos := c.pos
			s := acc(a[0]).Ref.get().(*Shared)
			if s.Count == math.MaxUint64 {
				it.trap("overflow", pos)
			}
			s.Count++
			return []Value{s}
		},
		"Mutex.new": func(it *Interp, a []Value, _ callCtx) []Value { return []Value{&Mutex{Val: a[0]}} },
		"Mutex.lock": func(it *Interp, a []Value, c callCtx) []Value {
			pos := c.pos
			m := acc(a[0]).Ref.get().(*Mutex)
			if m.Locked {
				it.trap("explicit", pos) // recursive acquisition (spec §13 accepted decision)
			}
			m.Locked = true
			return []Value{&Access{Ref: slotRef{&m.Val}, Edit: true}}
		},
		"Mutex.unlock": func(it *Interp, a []Value, _ callCtx) []Value {
			acc(a[0]).Ref.get().(*Mutex).Locked = false
			return nil
		},
		"Atomic.new":   func(it *Interp, a []Value, _ callCtx) []Value { return []Value{&Atomic{Val: a[0]}} },
		"Atomic.load":  func(it *Interp, a []Value, _ callCtx) []Value { return []Value{atomicOf(a[0]).Val} },
		"Atomic.store": func(it *Interp, a []Value, _ callCtx) []Value { atomicOf(a[0]).Val = a[1]; return nil },
		"Atomic.swap": func(it *Interp, a []Value, _ callCtx) []Value {
			at := atomicOf(a[0])
			old := at.Val
			at.Val = a[1]
			return []Value{old}
		},
		"Atomic.compare_exchange": func(it *Interp, a []Value, _ callCtx) []Value {
			at := atomicOf(a[0])
			if equalValues(at.Val, a[1]) {
				old := at.Val
				at.Val = a[2]
				return []Value{&Union{Tag: 0, Fields: []Value{old}}}
			}
			return []Value{&Union{Tag: 1, Fields: []Value{at.Val}}}
		},
		"Atomic.fetch_add": atomicRMW("+"),
		"Atomic.fetch_sub": atomicRMW("-"),
		"Atomic.fetch_and": atomicRMW("&"),
		"Atomic.fetch_or":  atomicRMW("|"),
		"Atomic.fetch_xor": atomicRMW("^"),
		"fence":            func(it *Interp, a []Value, _ callCtx) []Value { return nil },
		// threads (Phase 1: run eagerly on the calling thread)
		"thread.scope": func(it *Interp, a []Value, _ callCtx) []Value { return []Value{&Scope{}} },
		"thread.spawn": func(it *Interp, a []Value, _ callCtx) []Value {
			rets := it.callNamed(a[0].(FuncVal).Name, a[1:])
			t := &Thread{Done: true}
			if len(rets) > 0 {
				t.Result = rets[0]
			} else {
				t.Result = Unit{}
			}
			return []Value{&Union{Tag: 0, Fields: []Value{t}}}
		},
		"scope.spawn": func(it *Interp, a []Value, _ callCtx) []Value {
			sc := acc(a[1]).Ref.get().(*Scope)
			rets := it.callNamed(a[0].(FuncVal).Name, a[2:])
			j := &Join{Done: true}
			if len(rets) > 0 {
				j.Result = rets[0]
			} else {
				j.Result = Unit{}
			}
			sc.Joins = append(sc.Joins, j)
			return []Value{&Union{Tag: 0, Fields: []Value{j}}}
		},
		"Thread.join": func(it *Interp, a []Value, _ callCtx) []Value { return []Value{a[0].(*Thread).Result} },
		"Join.get": func(it *Interp, a []Value, _ callCtx) []Value {
			j := a[0].(*Join)
			j.Taken = true
			return []Value{j.Result}
		},
		// trap handler
		"trap.set_handler": func(it *Interp, a []Value, _ callCtx) []Value {
			prev := FuncVal{}
			if it.handler != nil {
				prev = *it.handler
			}
			f := a[0].(FuncVal)
			it.handler = &f
			return []Value{prev}
		},
		// allocator stand-in
		"SystemAllocator.new": func(it *Interp, a []Value, _ callCtx) []Value { return []Value{&SysAlloc{}} },
		"SystemAllocator.alloc": func(it *Interp, a []Value, c callCtx) []Value {
			pos := c.pos
			it.trap("explicit", pos)
			return nil
		},
		"SystemAllocator.free": func(it *Interp, a []Value, c callCtx) []Value {
			pos := c.pos
			it.trap("explicit", pos)
			return nil
		},
		// test hooks
		"testing.seed_generation": func(it *Interp, a []Value, c callCtx) []Value {
			pos := c.pos
			if !it.TestHooks {
				it.trap("explicit", pos)
			}
			p := acc(a[0]).Ref.get().(*Pool)
			slot := int(u64(a[1]))
			for len(p.Slots) <= slot {
				p.Slots = append(p.Slots, &PoolSlot{})
				p.Free = append(p.Free, len(p.Slots)-1)
			}
			p.Slots[slot].Gen = uint32(u64(a[2]))
			return nil
		},
		"testing.fail_next_alloc": func(it *Interp, a []Value, c callCtx) []Value {
			pos := c.pos
			if !it.TestHooks {
				it.trap("explicit", pos)
			}
			it.failNextAlloc = true
			return nil
		},
		"testing.seed_shared_count": func(it *Interp, a []Value, c callCtx) []Value {
			pos := c.pos
			if !it.TestHooks {
				it.trap("explicit", pos)
			}
			acc(a[0]).Ref.get().(*Shared).Count = u64(a[1])
			return nil
		},
	} {
		intrinsics[k] = v
	}
}

func vecOf(v Value) *Vec       { return acc(v).Ref.get().(*Vec) }
func atomicOf(v Value) *Atomic { return acc(v).Ref.get().(*Atomic) }

func atomicRMW(op string) intrinsicFn {
	return func(it *Interp, a []Value, _ callCtx) []Value {
		at := atomicOf(a[0])
		old := at.Val
		at.Val = it.binary(op, old, a[1], "wrapping")
		return []Value{old}
	}
}

// deepClone duplicates a value including library objects (the `clone`
// intrinsic behind copy(x) for move-only types).
func deepClone(it *Interp, v Value, pos ast.Pos) Value {
	switch x := v.(type) {
	case *Box:
		return &Box{Val: deepClone(it, x.Val, pos)}
	case *Vec:
		n := &Vec{Items: make([]Value, len(x.Items)), Elem: x.Elem}
		for i, e := range x.Items {
			n.Items[i] = deepClone(it, e, pos)
		}
		return n
	case *Shared:
		// a clone of a Shared is another owner of the same value
		if x.Count == math.MaxUint64 {
			it.trap("overflow", pos)
		}
		x.Count++
		return x
	case *Struct, *Union, *Array:
		// aggregates: clone fields recursively (library objects inside them too)
		switch y := x.(type) {
		case *Struct:
			n := &Struct{Fields: make([]Value, len(y.Fields))}
			for i, f := range y.Fields {
				n.Fields[i] = deepClone(it, f, pos)
			}
			return n
		case *Union:
			n := &Union{Tag: y.Tag, Fields: make([]Value, len(y.Fields))}
			for i, f := range y.Fields {
				n.Fields[i] = deepClone(it, f, pos)
			}
			return n
		case *Array:
			n := &Array{Elems: make([]Value, len(y.Elems))}
			for i, e := range y.Elems {
				n.Elems[i] = deepClone(it, e, pos)
			}
			return n
		}
	}
	return v
}

// ---- Pool (spec §5.6) ----

func (it *Interp) newPool() *Pool {
	p := &Pool{Identity: it.nextIdent}
	if it.nextIdent != 0 {
		it.nextIdent++
	}
	return p
}

func handleIndex(h Value) int    { return int(h.(*Struct).Fields[0].(Int).Bits) }
func handleGen(h Value) uint32   { return uint32(h.(*Struct).Fields[1].(Int).Bits) }
func handleIdent(h Value) uint32 { return uint32(h.(*Struct).Fields[2].(Int).Bits) }

func poolAlloc(it *Interp, a []Value, _ callCtx) []Value {
	p := acc(a[0]).Ref.get().(*Pool)
	var idx int
	if len(p.Free) > 0 {
		idx = p.Free[len(p.Free)-1]
		p.Free = p.Free[:len(p.Free)-1]
	} else {
		p.Slots = append(p.Slots, &PoolSlot{})
		idx = len(p.Slots) - 1
	}
	s := p.Slots[idx]
	s.Val = a[1]
	s.Live = true
	id := &Struct{Fields: []Value{mkInt("u32", uint64(idx)), mkInt("u32", uint64(s.Gen)), mkInt("u32", uint64(p.Identity))}}
	return []Value{&Union{Tag: 0, Fields: []Value{id}}}
}

// poolCheck validates a handle in the §5.6 order and returns the slot.
func poolCheck(it *Interp, p *Pool, h Value, pos ast.Pos) *PoolSlot {
	idx := handleIndex(h)
	if idx >= len(p.Slots) {
		it.trap("bounds", pos)
	}
	if hi, pi := handleIdent(h), p.Identity; hi != 0 && pi != 0 && hi != pi {
		it.trap("identity", pos)
	}
	s := p.Slots[idx]
	if !s.Live {
		it.trap("vacant", pos)
	}
	if s.Gen != handleGen(h) {
		it.trap("generation", pos)
	}
	return s
}

func poolRef(it *Interp, pool, h Value, edit bool, pos ast.Pos) Value {
	p := acc(pool).Ref.get().(*Pool)
	s := poolCheck(it, p, h, pos)
	return &Access{Ref: slotRef{&s.Val}, Edit: edit}
}

func poolFree(it *Interp, a []Value, c callCtx) []Value {
	pos := c.pos
	p := acc(a[0]).Ref.get().(*Pool)
	s := poolCheck(it, p, a[1], pos)
	elem := Value(s.Val)
	s.Live = false
	s.Val = nil
	it.destroy(elem, p.Elem)
	if s.Gen == math.MaxUint32 {
		s.Retired = true // never reused: stale handles stay detectable
	} else {
		s.Gen++
		p.Free = append(p.Free, handleIndex(a[1]))
	}
	return nil
}

func arenaRef(it *Interp, arena, h Value, edit bool, pos ast.Pos) Value {
	ar := acc(arena).Ref.get().(*Arena)
	idx := handleIndex(h)
	if idx >= len(ar.Slots) {
		it.trap("bounds", pos)
	}
	if hi := uint32(h.(*Struct).Fields[1].(Int).Bits); hi != 0 && ar.Identity != 0 && hi != ar.Identity {
		it.trap("identity", pos)
	}
	return &Access{Ref: idxRef{elems: func() []Value { return ar.Slots }, i: idx}, Edit: edit}
}

// allocOrTrap models allocation failure for convenience constructors: with
// the fail_next_alloc test hook armed, the next allocation traps with kind
// oom (spec §6).
func (it *Interp) allocOrTrap(pos ast.Pos) {
	if it.failNextAlloc {
		it.failNextAlloc = false
		it.trap("oom", pos)
	}
}
