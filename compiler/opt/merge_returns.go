package opt

// MergeReturns (specs/merge-returns): blocks that end in ret and differ only
// in a union tag or a constant become one block, reached from each with the
// differing value in a temporary. Written as `if c { return f(x, A(..)) }
// return f(x, B(..))`, the two returns would otherwise each receive a copy
// of f from the inliner, and the branch between them, on a value the
// program cannot predict, stays in the machine code (arena_ast's Add or
// Mul node: docs/reports/merge-returns.md). Merged, f is called once with
// the selected value and LLVM folds the temporary to a select.

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/resolve"
)

// MergeReturns merges the ret blocks of every function with a body in the
// module, other than library instantiations, wherever the result validates.
func MergeReturns(mod *ir.Module) {
	for _, f := range mod.Funcs {
		if f.Entry == nil || f.Intrinsic || f.Extern != "" || f.Library {
			continue
		}
		mergeReturnsIn(mod, f)
	}
}

func mergeReturnsIn(mod *ir.Module, f *ir.Func) {
	var rets []*ir.Block
	for _, b := range f.Blocks {
		if _, ok := b.Term.(*ir.Ret); ok && b.File == "" {
			rets = append(rets, b)
		}
	}
	// only blocks of the same shape (instruction kinds and callees) can
	// match, so the pairs compared are within a bucket
	buckets := map[string][]*ir.Block{}
	var keys []string
	for _, b := range rets {
		k := shape(b)
		if buckets[k] == nil {
			keys = append(keys, k)
		}
		buckets[k] = append(buckets[k], b)
	}
	for _, k := range keys {
		mergeBucket(mod, f, buckets[k])
	}
}

// shape is a ret block's instruction kinds and direct callees in order.
func shape(b *ir.Block) string {
	var sb strings.Builder
	for _, in := range b.Instrs {
		fmt.Fprintf(&sb, "%T", in)
		if c, ok := in.(*ir.Call); ok {
			sb.WriteString(c.Callee)
		}
		sb.WriteByte(';')
	}
	return sb.String()
}

// mergeBucket merges, within ret blocks of one shape, each block with the
// later ones that match it.
func mergeBucket(mod *ir.Module, f *ir.Func, rets []*ir.Block) {
	done := map[*ir.Block]bool{}
	for i, a := range rets {
		if done[a] {
			continue
		}
		group := []*ir.Block{a}
		diff := map[int]bool{}
		for _, b := range rets[i+1:] {
			if done[b] {
				continue
			}
			if ks, ok := sameReturn(mod, f, a, b); ok && len(ks) > 0 {
				group = append(group, b)
				for _, k := range ks {
					diff[k] = true
				}
			}
		}
		if len(group) < 2 {
			continue
		}
		if mergeGroup(f, group, diff) {
			for _, b := range group {
				done[b] = true
			}
		}
	}
}

// sameReturn reports whether ret block b is a's instructions and
// terminator under a mapping of b's values onto a's, and the indices at
// which the two differ in a selectable way: a union of the same type with
// another tag, or a constant of the same type with another value.
// Positions must agree wherever a trap reports one (a check, a trap, a
// drop, a call into the library or the runtime); elsewhere they only place
// a debug line, and the merged block keeps a's.
func sameReturn(mod *ir.Module, f *ir.Func, a, b *ir.Block) ([]int, bool) {
	if len(a.Instrs) != len(b.Instrs) || len(a.Instrs) == 0 {
		return nil, false
	}
	m := &copier{f: &ir.Func{}, vals: map[*ir.Value]*ir.Value{}, locs: map[*ir.Local]*ir.Local{}}
	for _, l := range f.Locals {
		m.locs[l] = l
	}
	// a value b reads but does not define is defined before both blocks
	// and stands for itself
	local := map[*ir.Value]bool{}
	for _, in := range b.Instrs {
		for _, u := range uses(in) {
			if !local[u] {
				m.vals[u] = u
			}
		}
		for _, d := range defs(in) {
			local[d] = true
		}
	}
	for _, u := range b.Term.(*ir.Ret).Vals {
		if !local[u] {
			m.vals[u] = u
		}
	}
	var diff []int
	for k := range a.Instrs {
		da, db := defs(a.Instrs[k]), defs(b.Instrs[k])
		if len(da) != len(db) {
			return nil, false
		}
		for j := range da {
			if !resolve.Identical(da[j].Type, db[j].Type) {
				return nil, false
			}
			m.vals[db[j]] = da[j]
		}
		x, y := a.Instrs[k], m.instr(b.Instrs[k])
		clearDebugPos(mod, x, y)
		if reflect.DeepEqual(x, y) {
			continue
		}
		if !selectable(x, y) {
			return nil, false
		}
		diff = append(diff, k)
	}
	ra, rb := a.Term.(*ir.Ret), b.Term.(*ir.Ret)
	if !reflect.DeepEqual(ra.Vals, m.vs(rb.Vals)) {
		return nil, false
	}
	return diff, true
}

// clearDebugPos gives y x's position where the position places only a
// debug line: a load, an access, a call to a user function with a body.
func clearDebugPos(mod *ir.Module, x, y ir.Instr) {
	switch i := y.(type) {
	case *ir.Load:
		if j, ok := x.(*ir.Load); ok {
			i.Pos = j.Pos
		}
	case *ir.AccessBegin:
		if j, ok := x.(*ir.AccessBegin); ok {
			i.Pos = j.Pos
		}
	case *ir.Call:
		if j, ok := x.(*ir.Call); ok && i.Fn == nil && i.Intrinsic == "" {
			if g := mod.Func(i.Callee); g != nil && g.Entry != nil && !g.Library && !g.Intrinsic && g.Extern == "" {
				i.Pos = j.Pos
			}
		}
	}
}

// selectable reports whether x and y (y already mapped onto x's values)
// differ only in what a temporary can carry: a union's tag or a constant.
func selectable(x, y ir.Instr) bool {
	switch i := x.(type) {
	case *ir.MakeUnion:
		j, ok := y.(*ir.MakeUnion)
		return ok && j.Dst == i.Dst && reflect.DeepEqual(i.Fields, j.Fields)
	case *ir.Const:
		j, ok := y.(*ir.Const)
		return ok && j.Dst == i.Dst
	}
	return false
}

// mergeGroup rewrites the ret blocks of group, which agree but at the
// indices diff, into one block that each of them branches to. The
// instructions at diff, with the pure instructions they read, stay in
// every member, and their values reach the merged block through a Copy
// temporary each; the rest of the members' instructions are the merged
// block's. It reports false, with f unchanged, where the values cannot be
// carried that way or the merged function does not validate.
func mergeGroup(f *ir.Func, group []*ir.Block, diff map[int]bool) bool {
	a := group[0]
	// the instructions that stay in the members: diff and the pure
	// instructions defining what they read
	keep := map[int]bool{}
	defAt := map[*ir.Value]int{}
	for k, in := range a.Instrs {
		for _, d := range defs(in) {
			defAt[d] = k
		}
	}
	var visit func(k int) bool
	visit = func(k int) bool {
		if keep[k] {
			return true
		}
		if !pureMovable(a.Instrs[k]) {
			return false
		}
		keep[k] = true
		for _, u := range uses(a.Instrs[k]) {
			if j, ok := defAt[u]; ok && !visit(j) {
				return false
			}
		}
		return true
	}
	for k := range diff {
		if !visit(k) {
			return false
		}
	}
	// a kept instruction that is not at diff is read only by kept ones; a
	// kept load reads a local nothing it moves ahead of writes or opens;
	// the merged block still calls a function with a body
	readers := map[*ir.Value][]int{}
	for k, in := range a.Instrs {
		for _, u := range uses(in) {
			readers[u] = append(readers[u], k)
		}
	}
	for _, u := range a.Term.(*ir.Ret).Vals {
		readers[u] = append(readers[u], len(a.Instrs))
	}
	calls := false
	for k, in := range a.Instrs {
		if keep[k] {
			if !diff[k] {
				for _, d := range defs(in) {
					for _, r := range readers[d] {
						if !keep[r] {
							return false
						}
					}
				}
			}
			if l, ok := in.(*ir.Load); ok {
				for j := 0; j < k; j++ {
					if !keep[j] && touches(a.Instrs[j], l.Place.Local) {
						return false
					}
				}
			}
			continue
		}
		if c, ok := in.(*ir.Call); ok && c.Fn == nil && c.Intrinsic == "" && c.Callee != "" {
			calls = true
		}
	}
	if !calls {
		return false
	}
	for k := range diff {
		if t := defs(a.Instrs[k])[0].Type; !resolve.IsCopy(t) {
			return false
		}
	}

	// snapshot for the revert
	type saved struct {
		instrs []ir.Instr
		term   ir.Term
	}
	old := map[*ir.Block]saved{}
	for _, b := range group {
		old[b] = saved{b.Instrs, b.Term}
	}
	nLocals, nBlocks, nValues := len(f.Locals), len(f.Blocks), f.NumValues

	nextBlock := 0
	for _, x := range f.Blocks {
		if x.ID >= nextBlock {
			nextBlock = x.ID + 1
		}
	}
	merged := &ir.Block{ID: nextBlock, Name: "ret.merge", Term: a.Term}
	temps := map[int]*ir.Local{}
	for k := range a.Instrs {
		if !diff[k] {
			continue
		}
		t := defs(a.Instrs[k])[0].Type
		temps[k] = &ir.Local{ID: len(f.Locals), Type: t, Temp: true}
		f.Locals = append(f.Locals, temps[k])
	}
	for k, in := range a.Instrs {
		switch {
		case diff[k]:
			merged.Instrs = append(merged.Instrs, &ir.Load{Dst: defs(in)[0], Place: ir.Place{Local: temps[k]}})
		case !keep[k]:
			merged.Instrs = append(merged.Instrs, in)
		}
	}
	for gi, b := range group {
		var instrs, tail []ir.Instr
		for k, in := range b.Instrs {
			if !keep[k] {
				continue
			}
			if diff[k] {
				src := defs(in)[0]
				if gi == 0 {
					// a's value is the merged block's load now
					fresh := &ir.Value{ID: f.NumValues, Type: src.Type}
					f.NumValues++
					in = withDst(in, fresh)
					src = fresh
				}
				tail = append(tail, &ir.Store{Place: ir.Place{Local: temps[k]}, Src: src}, &ir.Init{Place: ir.Place{Local: temps[k]}})
			}
			instrs = append(instrs, in)
		}
		b.Instrs = append(instrs, tail...)
		b.Term = &ir.Br{Target: merged}
	}
	f.Blocks = append(f.Blocks, merged)
	if errs := ir.Validate(&ir.Module{Funcs: []*ir.Func{f}}); len(errs) > 0 {
		for b, s := range old {
			b.Instrs, b.Term = s.instrs, s.term
		}
		f.Locals, f.Blocks, f.NumValues = f.Locals[:nLocals], f.Blocks[:nBlocks], nValues
		return false
	}
	return true
}

// pureMovable reports an instruction that reads no state but a plain
// local's Copy fields and cannot trap, so it may run earlier in its block.
func pureMovable(in ir.Instr) bool {
	switch i := in.(type) {
	case *ir.Const, *ir.MakeUnion, *ir.MakeStruct, *ir.MakeArray, *ir.Zero, *ir.Copy:
		return true
	case *ir.Load:
		if i.Place.Local == nil || i.Place.Access != nil || i.Place.Global != nil {
			return false
		}
		for _, e := range i.Place.Path {
			if e.Kind != ir.PathField {
				return false
			}
		}
		return true
	}
	return false
}

// touches reports whether in may write l or open an access to it: any
// call (through an access it was passed earlier), an instruction whose
// place is rooted at l, any write through an access (which may alias l,
// opened before the block), and an access end that calls a release
// function.
func touches(in ir.Instr, l *ir.Local) bool {
	switch i := in.(type) {
	case *ir.Call:
		return true
	case *ir.Store:
		return i.Place.Local == l || i.Place.Access != nil
	case *ir.Move:
		return i.Src.Local == l || i.Src.Access != nil
	case *ir.Drop:
		return i.Place.Local == l || i.Place.Access != nil
	case *ir.Init:
		return i.Place.Local == l || i.Place.Access != nil
	case *ir.AccessBegin:
		return i.Place.Local == l
	case *ir.AccessEnd:
		return i.Release != ""
	}
	return false
}

// withDst is a copy of the single-result instruction in defining d.
func withDst(in ir.Instr, d *ir.Value) ir.Instr {
	switch i := in.(type) {
	case *ir.MakeUnion:
		c := *i
		c.Dst = d
		return &c
	case *ir.Const:
		c := *i
		c.Dst = d
		return &c
	}
	panic("opt: merge-returns: unexpected instruction")
}

// uses lists the values in reads, place operands included.
func uses(in ir.Instr) []*ir.Value {
	var out []*ir.Value
	add := func(xs ...*ir.Value) {
		for _, x := range xs {
			if x != nil {
				out = append(out, x)
			}
		}
	}
	place := func(p ir.Place) {
		add(p.Access)
		for _, e := range p.Path {
			add(e.Index, e.Lo, e.Hi)
		}
	}
	switch i := in.(type) {
	case *ir.Load:
		place(i.Place)
	case *ir.Store:
		place(i.Place)
		add(i.Src)
	case *ir.Move:
		place(i.Src)
	case *ir.Drop:
		place(i.Place)
	case *ir.AccessBegin:
		place(i.Place)
	case *ir.AccessEnd:
		add(i.Access, i.Recv)
	case *ir.Call:
		add(i.Fn)
		add(i.Args...)
	case *ir.Binary:
		add(i.L, i.R)
	case *ir.Unary:
		add(i.X)
	case *ir.Conv:
		add(i.X)
	case *ir.CheckOverflow:
		add(i.L, i.R)
	case *ir.CheckBounds:
		add(i.Index, i.Lo, i.Hi, i.Len)
	case *ir.CheckConv:
		add(i.X)
	case *ir.Len:
		place(i.Place)
	case *ir.MakeStruct:
		add(i.Fields...)
	case *ir.MakeUnion:
		add(i.Fields...)
	case *ir.MakeArray:
		add(i.Elems...)
	case *ir.Tag:
		place(i.Place)
	case *ir.MakeClosure:
		add(i.Captures...)
	case *ir.Copy:
		add(i.Src)
	case *ir.Init:
		place(i.Place)
	}
	return out
}
