// Package opt holds the IR-to-IR passes that run after lowering (PLAN.md
// Phase 6): inlining of small leaf functions, so that the lowering's proof
// facts can reach across calls. The passes keep every §7 invariant;
// ir.Validate must pass after each of them.
package opt

import (
	"fmt"
	"sort"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/resolve"
)

// MaxCalleeSize is the largest callee the inliner copies: its size, the
// number of instructions over its blocks plus one per block for the
// terminator, as the function was lowered, before the pass inlined the
// callee's own callees into it. Judging the callee as written keeps the
// callees-first order from defeating the limit: a small function that
// absorbs two small helpers is still the small function the limit was
// set for, and what its copies cost the caller is bounded by MaxGrowth.
// Counting blocks keeps a chain of empty functions that call their
// predecessor twice from doubling the caller's control flow at every
// step.
var MaxCalleeSize = 48

// MaxGrowth bounds the net instruction growth of one function through
// inlining, measured on the callee as it is when copied; a site whose
// rewrite would exceed it stays a call, as do every later site in that
// function.
var MaxGrowth = 512

// InlineSingleSite admits a callee of any size at its only call site
// when nothing else references it. The copy replaces the body, so the
// module does not grow by it, and for the same reason the copy is not
// charged against the caller's MaxGrowth: charging it would starve the
// small helpers at the sites after it (docs/reports/inliner-sizing.md
// has the measurement). The callee is otherwise judged like any other
// (no loops, no out parameters, no returned accesses, not recursive,
// same file, not library).
var InlineSingleSite = true

// MaxCallerSize bounds a function's size through single-site copies: a
// single-site callee is outside MaxGrowth only while the caller's size
// after the copy stays within it (past that, the site is judged by the
// budget like any other). Without the bound a chain of functions each
// calling its predecessor once would be copied level by level into its
// last member, quadratic work in the chain's length; with it the work
// is linear in the module's size.
var MaxCallerSize = 2048

// Inline rewrites every eligible direct call site of the module (PLAN.md
// 6.2, 6.3), callees before callers, and marks the functions nothing
// references afterwards as Inlined. The result is a function of the module
// alone: functions are visited in a deterministic order and a function's
// call sites in block then instruction order.
func Inline(mod *ir.Module) {
	funcs := append([]*ir.Func(nil), mod.Funcs...)
	sort.Slice(funcs, func(i, j int) bool { return funcs[i].Name < funcs[j].Name })
	in := &inliner{
		mod:       mod,
		recursive: recursiveFuncs(mod, funcs),
		sizes:     map[*ir.Func]int{},
		sites:     callSites(mod, funcs),
	}
	for _, f := range funcs {
		in.sizes[f] = size(f)
	}
	for _, f := range callOrder(mod, funcs) {
		if f.Entry == nil {
			continue
		}
		in.inlineInto(f)
	}
	markInlined(mod, funcs)
}

// inliner holds what the pass decides eligibility from, recorded before
// any function is rewritten: the functions in recursive components, every
// function's size as lowered, and its direct call sites over the module.
type inliner struct {
	mod       *ir.Module
	recursive map[*ir.Func]bool
	sizes     map[*ir.Func]int // size as lowered, before the pass
	sites     map[*ir.Func]int // direct call sites as lowered; -1 when otherwise referenced
}

// callSites counts each function's direct call sites over the module as
// lowered. A function the module also references another way, by
// func_ref, as a release function, as the entry, a destructor or an
// exported symbol, keeps its body whatever the pass does at its calls, so
// it counts as referenced (-1) rather than called.
func callSites(mod *ir.Module, funcs []*ir.Func) map[*ir.Func]int {
	sites := map[*ir.Func]int{}
	referenced := map[string]bool{mod.Entry: true}
	for _, f := range funcs {
		if f.Release != "" {
			referenced[f.Release] = true
		}
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				switch i := in.(type) {
				case *ir.Call:
					if i.Fn == nil && i.Intrinsic == "" {
						if g := mod.Func(i.Callee); g != nil {
							sites[g]++
						}
					}
					if i.Release != "" {
						referenced[i.Release] = true
					}
				case *ir.AccessEnd:
					if i.Release != "" {
						referenced[i.Release] = true
					}
				case *ir.FuncRef:
					referenced[i.Name] = true
				}
			}
		}
	}
	for _, f := range funcs {
		if referenced[f.Name] || f.Extern != "" || isDestructor(f.Name) {
			sites[f] = -1
		}
	}
	return sites
}

// eligible reports whether a call from f to g may be inlined (PLAN.md 6.2).
func (in *inliner) eligible(f, g *ir.Func) bool {
	recursive := in.recursive
	switch {
	case g == nil || g == f || g.Entry == nil:
		return false
	case g.Library || g.Extern != "" || g.Intrinsic:
		return false // library bodies keep their call-site trap spans and reporting
	case g.File != f.File:
		return false // positions are offsets within the function's file
	case g.Release != "":
		return false
	case recursive[g]:
		return false // a recursive function, or one in a recursive component, is never copied
	}
	for _, p := range g.Params {
		if p.Conv == ast.ConvOut {
			return false // the callee writes through the parameter slot
		}
	}
	for _, r := range g.Rets {
		if _, ok := r.(*resolve.Access); ok {
			return false // a returned projection is tied to the caller's AccessEnd
		}
	}
	if hasLoop(g) {
		return false
	}
	if in.sizes[g] > MaxCalleeSize {
		return InlineSingleSite && in.sites[g] == 1 // the copy replaces the body
	}
	return true
}

// size counts a function's instructions plus one per block (its terminator).
func size(f *ir.Func) int {
	n := 0
	for _, b := range f.Blocks {
		n += len(b.Instrs) + 1
	}
	return n
}

// hasLoop reports a back edge in the function's control-flow graph.
func hasLoop(f *ir.Func) bool {
	const (
		unseen = iota
		active
		done
	)
	state := map[*ir.Block]int{}
	var visit func(b *ir.Block) bool
	visit = func(b *ir.Block) bool {
		state[b] = active
		for _, s := range successors(b.Term) {
			switch state[s] {
			case active:
				return true
			case unseen:
				if visit(s) {
					return true
				}
			}
		}
		state[b] = done
		return false
	}
	return visit(f.Entry)
}

func successors(t ir.Term) []*ir.Block {
	switch x := t.(type) {
	case *ir.Br:
		return []*ir.Block{x.Target}
	case *ir.CondBr:
		return []*ir.Block{x.Then, x.Else}
	case *ir.Switch:
		keys := make([]int, 0, len(x.Cases))
		for k := range x.Cases {
			keys = append(keys, k)
		}
		sort.Ints(keys)
		var out []*ir.Block
		for _, k := range keys {
			out = append(out, x.Cases[k])
		}
		if x.Default != nil {
			out = append(out, x.Default)
		}
		return out
	}
	return nil
}

// directCallees lists the functions f calls directly, in call order.
func directCallees(mod *ir.Module, f *ir.Func) []*ir.Func {
	var out []*ir.Func
	for _, b := range f.Blocks {
		for _, in := range b.Instrs {
			if c, ok := in.(*ir.Call); ok && c.Fn == nil && c.Intrinsic == "" {
				if g := mod.Func(c.Callee); g != nil {
					out = append(out, g)
				}
			}
		}
	}
	return out
}

// components computes the strongly connected components of the direct
// call graph (Tarjan), numbering each function with its component.
func components(mod *ir.Module, funcs []*ir.Func) map[*ir.Func]int {
	index := map[*ir.Func]int{}
	low := map[*ir.Func]int{}
	onStack := map[*ir.Func]bool{}
	comp := map[*ir.Func]int{}
	var stack []*ir.Func
	next, ncomp := 0, 0
	var strong func(f *ir.Func)
	strong = func(f *ir.Func) {
		index[f] = next
		low[f] = next
		next++
		stack = append(stack, f)
		onStack[f] = true
		for _, g := range directCallees(mod, f) {
			if _, seen := index[g]; !seen {
				strong(g)
				if low[g] < low[f] {
					low[f] = low[g]
				}
			} else if onStack[g] && index[g] < low[f] {
				low[f] = index[g]
			}
		}
		if low[f] == index[f] {
			for {
				g := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[g] = false
				comp[g] = ncomp
				if g == f {
					break
				}
			}
			ncomp++
		}
	}
	for _, f := range funcs {
		if _, seen := index[f]; !seen {
			strong(f)
		}
	}
	return comp
}

// recursiveFuncs reports the functions in a component of more than one
// member, or that call themselves: never inlined anywhere (which also
// keeps a component's members from inlining one another).
func recursiveFuncs(mod *ir.Module, funcs []*ir.Func) map[*ir.Func]bool {
	componentOf := components(mod, funcs)
	members := map[int]int{}
	for _, c := range componentOf {
		members[c]++
	}
	rec := map[*ir.Func]bool{}
	for _, f := range funcs {
		if members[componentOf[f]] > 1 {
			rec[f] = true
			continue
		}
		for _, g := range directCallees(mod, f) {
			if g == f {
				rec[f] = true
			}
		}
	}
	return rec
}

// callOrder lists the functions callees before callers (a topological
// order of the component graph; ties by name, which the input order
// already is).
func callOrder(mod *ir.Module, funcs []*ir.Func) []*ir.Func {
	var order []*ir.Func
	seen := map[*ir.Func]bool{}
	var visit func(f *ir.Func)
	visit = func(f *ir.Func) {
		if seen[f] {
			return
		}
		seen[f] = true
		for _, g := range directCallees(mod, f) {
			visit(g)
		}
		order = append(order, f)
	}
	for _, f := range funcs {
		visit(f)
	}
	return order
}

// inlineInto rewrites the eligible call sites of f, in block then
// instruction order, within the growth budget; a single-site callee's
// copy is outside the budget while f stays within MaxCallerSize (see
// InlineSingleSite).
func (in *inliner) inlineInto(f *ir.Func) {
	growth := 0
	for bi := 0; bi < len(f.Blocks); bi++ {
		b := f.Blocks[bi]
		for k := 0; k < len(b.Instrs); k++ {
			c, ok := b.Instrs[k].(*ir.Call)
			if !ok || c.Fn != nil || c.Intrinsic != "" {
				continue
			}
			g := in.mod.Func(c.Callee)
			if !in.eligible(f, g) {
				continue
			}
			delta := growthOf(c, g)
			single := InlineSingleSite && in.sites[g] == 1 && size(f)+delta <= MaxCallerSize
			if growth+delta > MaxGrowth && !single {
				return // this site and the ones after it stay calls
			}
			before := size(f)
			rewrite(f, b, k, c, g)
			if size(f)-before != delta {
				panic(fmt.Sprintf("opt: inline: growth of %s at %d is %d, estimated %d", c.Callee, c.Pos.Start, size(f)-before, delta))
			}
			if !single {
				growth += delta
			}
			break // the rest of b moved to the continuation block, visited later
		}
	}
}

// retPlan decides how the callee's returns reach the call's destinations:
// directly, when there is one ret whose values are distinct, defined by the
// callee's own instructions and not an aliased parameter access (the
// copied instruction then defines the destination), or through result
// locals otherwise. It also lists the callee's returning blocks.
func retPlan(c *ir.Call, g *ir.Func, aliased map[*ir.Value]bool) (bool, []*ir.Block) {
	var rets []*ir.Block
	for _, blk := range g.Blocks {
		if _, ok := blk.Term.(*ir.Ret); ok {
			rets = append(rets, blk)
		}
	}
	if len(rets) != 1 || len(c.Dsts) == 0 {
		return false, rets
	}
	seen := map[*ir.Value]bool{}
	for _, v := range rets[0].Term.(*ir.Ret).Vals {
		if seen[v] || aliased[v] || !definedIn(g, v) {
			return false, rets
		}
		seen[v] = true
	}
	return true, rets
}

// paramAccesses finds the access values the callee's view and edit
// parameters open in its entry block: the values the rewrite aliases.
func paramAccesses(g *ir.Func) map[*ir.Value]bool {
	aliased := map[*ir.Value]bool{}
	for _, p := range g.Params {
		if p.Conv != ast.ConvView && p.Conv != ast.ConvEdit {
			continue
		}
		for _, in := range g.Entry.Instrs {
			if ab, ok := in.(*ir.AccessBegin); ok && ab.Place.Local == p && len(ab.Place.Path) == 0 {
				aliased[ab.Dst] = true
				break
			}
		}
	}
	return aliased
}

// growthOf is the net size growth of the caller when c is rewritten to
// g's body: the copied blocks and instructions, less the parameter access
// begins and ends the aliasing drops, plus the parameter stores and
// inits, the result stores and inits, the destination loads or moves and
// the continuation block, less the call itself.
func growthOf(c *ir.Call, g *ir.Func) int {
	aliased := paramAccesses(g)
	n := size(g) - 1 + 1 // minus the call, plus the continuation block
	for _, blk := range g.Blocks {
		for _, in := range blk.Instrs {
			switch i := in.(type) {
			case *ir.AccessBegin:
				if aliased[i.Dst] {
					n--
				}
			case *ir.AccessEnd:
				if aliased[i.Access] {
					n--
				}
			}
		}
	}
	for _, p := range g.Params {
		if p.Conv != ast.ConvView && p.Conv != ast.ConvEdit {
			n += 2
		}
	}
	if direct, rets := retPlan(c, g, aliased); !direct {
		for _, blk := range rets {
			n += 2 * len(blk.Term.(*ir.Ret).Vals)
		}
		n += len(c.Dsts)
	}
	for _, d := range retDepths(g) {
		n += d
	}
	return n
}

// retDepths is the unsafe depth at each of g's returns. A function may
// return from inside an unsafe block (the exit closes the region), but
// once inlined the return is a branch to the join, so the copy closes
// the open regions there. Validation guarantees one depth per block.
func retDepths(g *ir.Func) map[*ir.Block]int {
	entry := map[*ir.Block]int{g.Entry: 0}
	out := map[*ir.Block]int{}
	var visit func(b *ir.Block)
	visit = func(b *ir.Block) {
		d := entry[b]
		for _, in := range b.Instrs {
			switch in.(type) {
			case *ir.UnsafeBegin:
				d++
			case *ir.UnsafeEnd:
				d--
			}
		}
		if _, ok := b.Term.(*ir.Ret); ok {
			out[b] = d
		}
		for _, s := range successors(b.Term) {
			if _, seen := entry[s]; seen {
				continue
			}
			entry[s] = d
			visit(s)
		}
	}
	visit(g.Entry)
	return out
}

// copier maps the callee's values, locals and blocks to the caller's copies.
type copier struct {
	f      *ir.Func
	vals   map[*ir.Value]*ir.Value
	locs   map[*ir.Local]*ir.Local
	blocks map[*ir.Block]*ir.Block
}

func (m *copier) v(x *ir.Value) *ir.Value {
	if x == nil {
		return nil
	}
	if y, ok := m.vals[x]; ok {
		return y
	}
	y := &ir.Value{ID: m.f.NumValues, Type: x.Type}
	m.f.NumValues++
	m.vals[x] = y
	return y
}

func (m *copier) vs(xs []*ir.Value) []*ir.Value {
	if xs == nil {
		return nil
	}
	out := make([]*ir.Value, len(xs))
	for i, x := range xs {
		out[i] = m.v(x)
	}
	return out
}

func (m *copier) place(p ir.Place) ir.Place {
	q := ir.Place{Global: p.Global}
	if p.Local != nil {
		q.Local = m.locs[p.Local]
	}
	if p.Access != nil {
		q.Access = m.v(p.Access)
	}
	for _, e := range p.Path {
		q.Path = append(q.Path, ir.PathElem{Kind: e.Kind, Field: e.Field, Name: e.Name, Tag: e.Tag, Index: m.v(e.Index), Lo: m.v(e.Lo), Hi: m.v(e.Hi)})
	}
	return q
}

// instr copies one instruction with every reference remapped; positions
// are kept verbatim (the callee is in the caller's file).
func (m *copier) instr(in ir.Instr) ir.Instr {
	switch i := in.(type) {
	case *ir.Const:
		return &ir.Const{Dst: m.v(i.Dst), Int: i.Int, Neg: i.Neg, Float: i.Float, Bool: i.Bool, Bytes: i.Bytes, Unit: i.Unit}
	case *ir.Load:
		return &ir.Load{Dst: m.v(i.Dst), Place: m.place(i.Place), Pos: i.Pos}
	case *ir.Store:
		return &ir.Store{Place: m.place(i.Place), Src: m.v(i.Src)}
	case *ir.Move:
		return &ir.Move{Dst: m.v(i.Dst), Src: m.place(i.Src)}
	case *ir.Drop:
		return &ir.Drop{Place: m.place(i.Place), Type: i.Type, Pos: i.Pos}
	case *ir.AccessBegin:
		return &ir.AccessBegin{Dst: m.v(i.Dst), Edit: i.Edit, Place: m.place(i.Place), Pos: i.Pos}
	case *ir.AccessEnd:
		return &ir.AccessEnd{Access: m.v(i.Access), Release: i.Release, Recv: m.v(i.Recv)}
	case *ir.Call:
		return &ir.Call{Dsts: m.vs(i.Dsts), Callee: i.Callee, Fn: m.v(i.Fn), Args: m.vs(i.Args), Intrinsic: i.Intrinsic, Pos: i.Pos, Release: i.Release, TypeArgs: i.TypeArgs}
	case *ir.Binary:
		return &ir.Binary{Dst: m.v(i.Dst), Op: i.Op, L: m.v(i.L), R: m.v(i.R), Mode: i.Mode}
	case *ir.Unary:
		return &ir.Unary{Dst: m.v(i.Dst), Op: i.Op, X: m.v(i.X)}
	case *ir.Conv:
		return &ir.Conv{Dst: m.v(i.Dst), To: i.To, X: m.v(i.X), Truncate: i.Truncate, Saturate: i.Saturate}
	case *ir.CheckOverflow:
		return &ir.CheckOverflow{Op: i.Op, L: m.v(i.L), R: m.v(i.R), Type: i.Type, Prov: i.Prov, Pos: i.Pos}
	case *ir.CheckBounds:
		return &ir.CheckBounds{Index: m.v(i.Index), Lo: m.v(i.Lo), Hi: m.v(i.Hi), Len: m.v(i.Len), Prov: i.Prov, Pos: i.Pos}
	case *ir.CheckConv:
		return &ir.CheckConv{X: m.v(i.X), To: i.To, Prov: i.Prov, Pos: i.Pos}
	case *ir.Len:
		return &ir.Len{Dst: m.v(i.Dst), Place: m.place(i.Place)}
	case *ir.MakeStruct:
		return &ir.MakeStruct{Dst: m.v(i.Dst), Fields: m.vs(i.Fields)}
	case *ir.MakeUnion:
		return &ir.MakeUnion{Dst: m.v(i.Dst), Tag: i.Tag, Fields: m.vs(i.Fields)}
	case *ir.MakeArray:
		return &ir.MakeArray{Dst: m.v(i.Dst), Elems: m.vs(i.Elems)}
	case *ir.Zero:
		return &ir.Zero{Dst: m.v(i.Dst)}
	case *ir.Tag:
		return &ir.Tag{Dst: m.v(i.Dst), Place: m.place(i.Place)}
	case *ir.FuncRef:
		return &ir.FuncRef{Dst: m.v(i.Dst), Name: i.Name}
	case *ir.Trap:
		return &ir.Trap{Kind: i.Kind, Pos: i.Pos}
	case *ir.UnsafeBegin:
		return &ir.UnsafeBegin{}
	case *ir.UnsafeEnd:
		return &ir.UnsafeEnd{}
	case *ir.Copy:
		return &ir.Copy{Dst: m.v(i.Dst), Src: m.v(i.Src)}
	case *ir.Init:
		return &ir.Init{Place: m.place(i.Place)}
	}
	panic(fmt.Sprintf("opt: inline: unknown instruction %T", in))
}

func (m *copier) term(t ir.Term) ir.Term {
	switch x := t.(type) {
	case *ir.Br:
		return &ir.Br{Target: m.blocks[x.Target]}
	case *ir.CondBr:
		return &ir.CondBr{Cond: m.v(x.Cond), Then: m.blocks[x.Then], Else: m.blocks[x.Else]}
	case *ir.Switch:
		cases := map[int]*ir.Block{}
		for k, b := range x.Cases {
			cases[k] = m.blocks[b]
		}
		var def *ir.Block
		if x.Default != nil {
			def = m.blocks[x.Default]
		}
		return &ir.Switch{Tag: m.v(x.Tag), Cases: cases, Default: def}
	case *ir.Unreachable:
		return &ir.Unreachable{}
	}
	panic(fmt.Sprintf("opt: inline: unexpected terminator %T", t))
}

// rewrite inlines the call c, the k-th instruction of block b of f, to g
// (PLAN.md 6.3).
func rewrite(f *ir.Func, b *ir.Block, k int, c *ir.Call, g *ir.Func) {
	m := &copier{f: f, vals: map[*ir.Value]*ir.Value{}, locs: map[*ir.Local]*ir.Local{}, blocks: map[*ir.Block]*ir.Block{}}
	nextBlock := 0
	for _, x := range f.Blocks {
		if x.ID >= nextBlock {
			nextBlock = x.ID + 1
		}
	}
	// locals: fresh copies named after the call site
	suffix := fmt.Sprintf("@%d", c.Pos.Start)
	for _, l := range g.Locals {
		name := l.Name
		if name != "" {
			name += suffix
		}
		nl := &ir.Local{ID: len(f.Locals), Name: name, Type: l.Type, Conv: l.Conv, Temp: l.Temp}
		f.Locals = append(f.Locals, nl)
		m.locs[l] = nl
	}
	// access parameters alias the caller's argument accesses: their
	// AccessBegin in the entry block and the AccessEnds of that value go
	var prologue []ir.Instr
	drop := map[ir.Instr]bool{}
	aliased := paramAccesses(g)
	for i, p := range g.Params {
		arg := c.Args[i]
		switch p.Conv {
		case ast.ConvView, ast.ConvEdit:
			for _, in := range g.Entry.Instrs {
				if ab, ok := in.(*ir.AccessBegin); ok && aliased[ab.Dst] && ab.Place.Local == p {
					m.vals[ab.Dst] = arg
					drop[in] = true
					break
				}
			}
		default:
			nl := m.locs[p]
			prologue = append(prologue, &ir.Store{Place: ir.Place{Local: nl}, Src: arg}, &ir.Init{Place: ir.Place{Local: nl}})
		}
	}
	for _, blk := range g.Blocks {
		for _, in := range blk.Instrs {
			if ae, ok := in.(*ir.AccessEnd); ok && aliased[ae.Access] {
				drop[in] = true
			}
		}
	}
	// returns: a single ret whose values are distinct and defined in g
	// defines the call's destinations directly; otherwise result locals
	direct, rets := retPlan(c, g, aliased)
	depths := retDepths(g)
	if direct {
		for i, v := range rets[0].Term.(*ir.Ret).Vals {
			m.vals[v] = c.Dsts[i]
		}
	}
	var results []*ir.Local
	if !direct {
		for _, d := range c.Dsts {
			rl := &ir.Local{ID: len(f.Locals), Name: "ret" + suffix, Type: d.Type, Temp: true}
			f.Locals = append(f.Locals, rl)
			results = append(results, rl)
		}
	}
	// blocks
	for _, blk := range g.Blocks {
		m.blocks[blk] = &ir.Block{ID: nextBlock, Name: blk.Name}
		nextBlock++
	}
	cont := &ir.Block{ID: nextBlock, Name: "inline.join"}
	for _, blk := range g.Blocks {
		nb := m.blocks[blk]
		if blk == g.Entry {
			nb.Instrs = append(nb.Instrs, prologue...)
		}
		for _, in := range blk.Instrs {
			if drop[in] {
				continue
			}
			nb.Instrs = append(nb.Instrs, m.instr(in))
		}
		if r, ok := blk.Term.(*ir.Ret); ok {
			if !direct {
				for i, v := range r.Vals {
					nb.Instrs = append(nb.Instrs, &ir.Store{Place: ir.Place{Local: results[i]}, Src: m.v(v)}, &ir.Init{Place: ir.Place{Local: results[i]}})
				}
			}
			for d := depths[blk]; d > 0; d-- {
				nb.Instrs = append(nb.Instrs, &ir.UnsafeEnd{})
			}
			nb.Term = &ir.Br{Target: cont}
			continue
		}
		nb.Term = m.term(blk.Term)
	}
	// the continuation: the call's destinations, then the rest of b
	for i, d := range c.Dsts {
		if direct {
			break
		}
		if resolve.IsCopy(d.Type) {
			cont.Instrs = append(cont.Instrs, &ir.Load{Dst: d, Place: ir.Place{Local: results[i]}, Pos: c.Pos})
		} else {
			cont.Instrs = append(cont.Instrs, &ir.Move{Dst: d, Src: ir.Place{Local: results[i]}})
		}
	}
	cont.Instrs = append(cont.Instrs, b.Instrs[k+1:]...)
	cont.Term = b.Term
	b.Instrs = append([]ir.Instr(nil), b.Instrs[:k]...)
	b.Term = &ir.Br{Target: m.blocks[g.Entry]}
	for _, blk := range g.Blocks {
		f.Blocks = append(f.Blocks, m.blocks[blk])
	}
	f.Blocks = append(f.Blocks, cont)
}

// definedIn reports whether v is defined by an instruction of f.
func definedIn(f *ir.Func, v *ir.Value) bool {
	for _, b := range f.Blocks {
		for _, in := range b.Instrs {
			for _, d := range defs(in) {
				if d == v {
					return true
				}
			}
		}
	}
	return false
}

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

// markInlined marks the functions nothing references after inlining: not
// the entry, not called directly, not taken by func_ref, not named as a
// release function (by an accessor, a call to one, or an AccessEnd), not a
// destructor, not exported under the C ABI, and not a library
// instantiation (which the backend discards on its own when unused).
func markInlined(mod *ir.Module, funcs []*ir.Func) {
	referenced := map[string]bool{mod.Entry: true}
	for _, f := range funcs {
		if f.Release != "" {
			referenced[f.Release] = true
		}
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				switch i := in.(type) {
				case *ir.Call:
					if i.Callee != "" {
						referenced[i.Callee] = true
					}
					if i.Release != "" {
						referenced[i.Release] = true
					}
				case *ir.AccessEnd:
					if i.Release != "" {
						referenced[i.Release] = true
					}
				case *ir.FuncRef:
					referenced[i.Name] = true
				}
			}
		}
	}
	for _, f := range funcs {
		if referenced[f.Name] || f.Extern != "" || f.Library || f.Intrinsic || isDestructor(f.Name) {
			continue
		}
		f.Inlined = true
	}
}

func isDestructor(name string) bool {
	const suffix = ".destructor"
	return len(name) > len(suffix) && name[len(name)-len(suffix):] == suffix
}
