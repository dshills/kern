// Package analysis computes the §16 machine-API facts of Phase 2 from the
// resolved program and the semantic IR: layouts, copies, allocation sites and
// bounds checks. Every label is derived from IR provenance, never from
// heuristics (spec §7).
package analysis

import (
	"sort"
	"strings"

	"github.com/dshills/kern/compiler/ast"
	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/layout"
	"github.com/dshills/kern/compiler/resolve"
)

// Layout reports the §4.1 layout of every non-generic struct and union
// declared by the program's non-prelude modules, in declaration order per
// module, modules sorted by name.
func Layout(prog *resolve.Program, target string) diag.ShowLayoutResult {
	res := diag.ShowLayoutResult{Target: target, Types: []diag.TypeLayout{}}
	var names []string
	for name, m := range prog.Modules {
		if m == prog.Core || name == "core" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		m := prog.Modules[name]
		var decls []*resolve.TypeDecl
		for _, td := range m.Types {
			if len(td.TypeParams) == 0 && (td.Kind == resolve.KindStruct || td.Kind == resolve.KindUnion) {
				decls = append(decls, td)
			}
		}
		sort.Slice(decls, func(i, j int) bool { return decls[i].Pos.Start < decls[j].Pos.Start })
		for _, td := range decls {
			n := &resolve.Named{Decl: td}
			if layout.Opaque(n) {
				continue
			}
			li := layout.Of(n)
			tl := diag.TypeLayout{Name: td.Name, Size: li.Size, Align: li.Align, Fields: []diag.FieldLayout{}}
			switch td.Kind {
			case resolve.KindStruct:
				for i, f := range td.Fields {
					tl.Fields = append(tl.Fields, diag.FieldLayout{Name: f.Name, Offset: li.Fields[i], Size: layout.Of(f.Type).Size})
				}
			case resolve.KindUnion:
				tl.Fields = append(tl.Fields, diag.FieldLayout{Name: "tag", Offset: 0, Size: li.TagSize})
				for _, v := range td.Variants {
					offs := layout.VariantLayout(n, v)
					fts := resolve.VariantFieldTypes(n, v)
					for i, f := range v.Fields {
						tl.Fields = append(tl.Fields, diag.FieldLayout{Name: v.Name + "." + f.Name, Offset: offs[i], Size: layout.Of(fts[i]).Size})
					}
				}
			}
			res.Types = append(res.Types, tl)
		}
	}
	return res
}

func loc(f *ir.Func, p ast.Pos) diag.Loc {
	return diag.Loc{File: f.File, Span: diag.Span{Start: p.Start, End: p.End}}
}

// Copies reports every bitwise copy of an aggregate Copy value (a load of a
// struct, union, array or tuple into a value) and every explicit deep copy
// (`clone`). Facts come from the IR, so they are proven.
func Copies(mod *ir.Module, target string) diag.ShowCopiesResult {
	res := diag.ShowCopiesResult{Target: target, Copies: []diag.Copy{}}
	for _, f := range mod.Funcs {
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				switch i := in.(type) {
				case *ir.Load:
					if isAggregateCopy(i.Dst.Type) {
						res.Copies = append(res.Copies, diag.Copy{Loc: loc(f, i.Pos), Type: i.Dst.Type.String(), Bytes: layout.Of(i.Dst.Type).Size, Kind: diag.CopyBitwise, Heap: false, Label: diag.LabelProven})
					}
				case *ir.Call:
					if i.Intrinsic == "clone" && len(i.Dsts) == 1 {
						t := i.Dsts[0].Type
						res.Copies = append(res.Copies, diag.Copy{Loc: loc(f, i.Pos), Type: t.String(), Bytes: layout.Of(t).Size, Kind: diag.CopyExplicitDeep, Heap: !resolve.IsCopy(t), Label: diag.LabelProven})
					}
				}
			}
		}
	}
	sortByLoc(res.Copies, func(c diag.Copy) diag.Loc { return c.Loc })
	return res
}

func isAggregateCopy(t resolve.Type) bool {
	if !resolve.IsCopy(t) {
		return false
	}
	switch x := t.(type) {
	case *resolve.Array, *resolve.Tuple:
		return true
	case *resolve.Named:
		return !layout.Opaque(x)
	}
	return false
}

// allocating maps allocating intrinsics to whether the allocation is
// unconditional (a constructor) or conditional (growth, pooled slots).
var allocating = map[string]bool{
	"Box.new": true, "Vec.new": true, "Vec.with_capacity": true, "Pool.new": true, "Arena.new": true,
	"Shared.new": true, "Mutex.new": true, "Atomic.new": true, "thread.scope": true,
	"Vec.push": false, "Pool.alloc": false, "Arena.alloc": false, "thread.spawn": false, "scope.spawn": false,
	"SystemAllocator.alloc": false, "clone": false,
}

// Allocations reports every allocation site: library constructors and the
// growth or slot operations that may allocate. The size is the inline size
// of the stored element when it is static (a constructor), else null; an
// unconditional allocation is proven, a conditional one is unknown.
func Allocations(mod *ir.Module) diag.ShowAllocationsResult {
	res := diag.ShowAllocationsResult{Sites: []diag.AllocationSite{}}
	for _, f := range mod.Funcs {
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				c, ok := in.(*ir.Call)
				if !ok {
					continue
				}
				always, known := allocating[c.Intrinsic]
				if !known {
					continue
				}
				if c.Intrinsic == "clone" && (len(c.Dsts) != 1 || resolve.IsCopy(c.Dsts[0].Type)) {
					continue
				}
				site := diag.AllocationSite{Loc: loc(f, c.Pos), Allocator: "system", Label: diag.LabelUnknown}
				if always {
					site.Label = diag.LabelProven
					if len(c.TypeArgs) > 0 && c.TypeArgs[0] != nil {
						n := layout.Of(c.TypeArgs[0]).Size
						site.Size = &n
					} else if len(c.Args) == 1 {
						n := layout.Of(c.Args[0].Type).Size
						site.Size = &n
					}
				}
				res.Sites = append(res.Sites, site)
			}
		}
	}
	sortByLoc(res.Sites, func(s diag.AllocationSite) diag.Loc { return s.Loc })
	return res
}

// checking maps intrinsics that perform a runtime generation or disjointness
// check to the check kind.
var checking = map[string]string{
	"Pool.get": diag.CheckGeneration, "Pool.at": diag.CheckGeneration, "Pool.free": diag.CheckGeneration,
	"Pool.get2": diag.CheckDisjoint, "Pool.at2": diag.CheckDisjoint,
}

// BoundsChecks reports every bounds, overflow, generation and disjointness
// check with its provenance label.
func BoundsChecks(mod *ir.Module) diag.ShowBoundsChecksResult {
	res := diag.ShowBoundsChecksResult{Checks: []diag.BoundsCheck{}}
	add := func(f *ir.Func, p ast.Pos, kind string, prov ir.Provenance) {
		c := diag.BoundsCheck{Loc: loc(f, p), Kind: kind, Label: diag.Label(prov.Label)}
		if prov.Label == "proven" && prov.ProvenBy != "" {
			by := prov.ProvenBy
			c.ProvenBy = &by
		}
		res.Checks = append(res.Checks, c)
	}
	for _, f := range mod.Funcs {
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				switch i := in.(type) {
				case *ir.CheckBounds:
					add(f, i.Pos, diag.CheckBounds, i.Prov)
				case *ir.CheckOverflow:
					add(f, i.Pos, diag.CheckOverflow, i.Prov)
				case *ir.Call:
					if kind, ok := checking[i.Intrinsic]; ok {
						add(f, i.Pos, kind, ir.RuntimeChecked)
					}
				}
			}
		}
	}
	sortByLoc(res.Checks, func(c diag.BoundsCheck) diag.Loc { return c.Loc })
	return res
}

func sortByLoc[T any](xs []T, key func(T) diag.Loc) {
	sort.SliceStable(xs, func(i, j int) bool {
		a, b := key(xs[i]), key(xs[j])
		if a.File != b.File {
			return strings.Compare(a.File, b.File) < 0
		}
		if a.Span.Start != b.Span.Start {
			return a.Span.Start < b.Span.Start
		}
		return a.Span.End < b.Span.End
	})
}
