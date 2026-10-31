package opt

import (
	"reflect"
	"sort"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/resolve"
)

// InlineLibrary copies small library bodies (std/core and the std
// modules) into their callers, callees before callers, after the proof
// pass (PLAN.md 6.6, first item; specs/inline-library). A copy into a
// user function takes the call site's position for every instruction and
// the call's block's file for every block, so a trap inside it reports
// the user-level call, as a trap inside the called library function does
// (spec §6). A copy into a library function keeps its positions, which a
// library function's traps do not read (they report the span it was
// entered with). The proof labels are those the proof pass gave, so what
// show-bounds-checks reports (on a module this pass never ran on) is what
// the copies check. Library functions are never marked inlined: a call
// the pass left keeps its callee.
func InlineLibrary(mod *ir.Module) {
	funcs := append([]*ir.Func(nil), mod.Funcs...)
	sort.Slice(funcs, func(i, j int) bool { return funcs[i].Name < funcs[j].Name })
	in := &inliner{mod: mod, recursive: recursiveFuncs(mod, funcs), sizes: map[*ir.Func]int{}}
	for _, f := range funcs {
		in.sizes[f] = size(f)
	}
	for _, f := range callOrder(mod, funcs) {
		if f.Entry != nil && !f.Inlined {
			in.inlineLibraryInto(f)
		}
	}
}

// libraryEligible reports whether a call from f to the library function g
// may be copied: the user inliner's exclusions (out and closure
// parameters, recursion, release functions), the containers'
// cold growth paths, functions that write through a raw pointer
// (writesRaw), and the callee size limit; a returned access only where
// accessRetOK allows it at the site.
func (in *inliner) libraryEligible(f, g *ir.Func) bool {
	switch {
	case g == nil || g == f || g.Entry == nil || !g.Library:
		return false
	case g.Extern != "" || g.Intrinsic || g.Release != "" || g.Closure:
		return false
	case in.recursive[g] || ir.ColdLibrary[baseName(g.Name)] || isDestructor(g.Name):
		return false
	case in.sizes[g] > MaxCalleeSize:
		return false
	case writesRaw(g):
		return false
	}
	for _, p := range g.Params {
		if p.Conv == ast.ConvOut {
			return false
		}
		if ft, ok := p.Type.(*resolve.Func); ok && ft.Closure {
			return false
		}
	}
	return true
}

// rawWrites are the intrinsics that write memory through a raw pointer.
var rawWrites = map[string]bool{"ptr_store": true, "ptr_take": true, "ptr_copy": true, "ptr_store_seq": true,
	"ptr_store_volatile": true, "ptr_init": true, "ptr_init_ctx": true}

// writesRaw reports a library function that writes through a raw pointer
// (Vec.push, Vec.pop): left a call, LLVM inlines it with its receiver
// still noalias, so the element store cannot clobber the container's
// header, which stays in registers; copied here, that fact is lost and
// every store reloads the header (vector's push loop: 28% more
// instructions; docs/reports/native-ab.md). A function that only reads
// through a raw pointer (Vec.get) gains from the copy.
func writesRaw(g *ir.Func) bool {
	for _, b := range g.Blocks {
		for _, x := range b.Instrs {
			if c, ok := x.(*ir.Call); ok && rawWrites[c.Intrinsic] {
				return true
			}
		}
	}
	return false
}

// inlineLibraryInto rewrites f's eligible library call sites in block
// then instruction order, within MaxGrowth.
func (in *inliner) inlineLibraryInto(f *ir.Func) {
	growth := 0
	work := append([]*ir.Block(nil), f.Blocks...)
	for bi := 0; bi < len(work); bi++ {
		b := work[bi]
		for k := 0; k < len(b.Instrs); k++ {
			c, ok := b.Instrs[k].(*ir.Call)
			if !ok || c.Fn != nil || c.Intrinsic != "" {
				continue
			}
			g := in.mod.Func(c.Callee)
			if !in.libraryEligible(f, g) || !accessRetOK(c, g) {
				continue
			}
			if hasLoop(g) && onCycle(b) {
				continue
			}
			delta := growthOf(c, g)
			if growth+delta > MaxGrowth {
				return
			}
			file := b.FileIn(f)
			spliced := rewrite(f, b, k, c, g)
			if !f.Library {
				// the copy is user code now: its traps and calls report the
				// user-level call, in the call's file
				for _, nb := range spliced[:len(spliced)-1] {
					nb.File = ""
					if file != f.File {
						nb.File = file
					}
					for _, x := range nb.Instrs {
						setPos(x, c.Pos)
					}
				}
			}
			work = append(work, spliced...)
			growth += delta
			break
		}
	}
}

// setPos gives an instruction the position p, when it has one.
func setPos(x ir.Instr, p ast.Pos) {
	v := reflect.ValueOf(x)
	if v.Kind() != reflect.Pointer || v.Elem().Kind() != reflect.Struct {
		return
	}
	if fv := v.Elem().FieldByName("Pos"); fv.IsValid() && fv.CanSet() && fv.Type() == reflect.TypeOf(p) {
		fv.Set(reflect.ValueOf(p))
	}
}

// baseName strips the specialization suffix: "Vec.push[T=u64]" -> "Vec.push".
func baseName(name string) string {
	for i := 0; i < len(name); i++ {
		if name[i] == '[' {
			return name[:i]
		}
	}
	return name
}
