package analysis

import (
	"sort"
	"strings"

	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/resolve"
)

// trapping lists the intrinsics that may trap at runtime (checks the
// lowering does not emit as instructions: allocator obligations, recursive
// locking, reference-count overflow, raw memory in the interpreter). The
// containers' checks are Trap instructions in their kern bodies and reach
// callers through propagation.
var trapping = map[string]bool{
	"Mutex.lock": true, "Condvar.wait": true, "Shared.clone": true,
	"SystemAllocator.alloc": true, "SystemAllocator.realloc": true, "SystemAllocator.alloc_zeroed": true, "SystemAllocator.free": true, "simd_shuffle": true, "asm": true,
	"ptr_load": true, "ptr_store": true, "ptr_take": true, "ptr_load_volatile": true, "ptr_store_volatile": true, "ptr_copy": true, "ptr_store_seq": true,
	"ptr_view": true, "ptr_edit": true, "ptr_view_seq": true, "ptr_edit_seq": true, "ptr_edit2": true,
	"testing.require_hooks": true, "testing.seed_shared_count": true,
}

// spawning lists the thread-creating intrinsics.
var spawning = map[string]bool{"thread.spawn": true, "scope.spawn": true}

// unsafeIntrinsics are the enumerated unsafe operations among intrinsics.
func unsafeIntrinsic(name string) bool {
	return strings.HasPrefix(name, "ptr_") || name == "asm" || name == "SystemAllocator.alloc" || name == "SystemAllocator.realloc" || name == "SystemAllocator.alloc_zeroed" || name == "SystemAllocator.free"
}

type effects struct {
	allocates, traps, spawns, unsafe bool
	label                            diag.Label
}

// Effects computes the §16 effect summary of every function in the module:
// transitive over statically resolved callees; a call through a function
// value makes every effect possible with label unknown, a call into FFI with
// label assumed-unsafe; otherwise the summary is proven.
func Effects(mod *ir.Module, target string) diag.ShowEffectsResult {
	res := diag.ShowEffectsResult{Target: target, Functions: []diag.FunctionEffects{}}
	summaries, defined := effectSummaries(mod)
	for _, f := range defined {
		e := summaries[f.Name]
		res.Functions = append(res.Functions, diag.FunctionEffects{Name: f.Name, Allocates: e.allocates, Traps: e.traps, Spawns: e.spawns, Unsafe: e.unsafe, Label: e.label})
	}
	sort.Slice(res.Functions, func(i, j int) bool { return res.Functions[i].Name < res.Functions[j].Name })
	return res
}

// effectSummaries computes the transitive effect summary of every function
// of the module that has one (all but intrinsics and inlined functions),
// by name, and returns the functions with a body in module order. An
// extern declaration without a body has every effect, assumed-unsafe.
func effectSummaries(mod *ir.Module) (map[string]*effects, []*ir.Func) {
	summaries := map[string]*effects{}
	var defined []*ir.Func
	for _, f := range mod.Funcs {
		if f.Intrinsic || f.Inlined {
			continue
		}
		if f.Extern != "" && len(f.Blocks) == 0 {
			summaries[f.Name] = &effects{allocates: true, traps: true, spawns: true, unsafe: true, label: diag.LabelAssumedUnsafe}
			continue
		}
		summaries[f.Name] = &effects{label: diag.LabelProven}
		defined = append(defined, f)
	}
	// direct effects and callees once, then propagation to a fixed point so
	// that recursion in any order converges
	type direct struct {
		e       effects
		callees []string
		dtors   []string
	}
	directs := map[string]*direct{}
	for _, f := range defined {
		d := &direct{e: effects{label: diag.LabelProven}}
		for _, b := range f.Blocks {
			for _, in := range b.Instrs {
				switch i := in.(type) {
				case *ir.CheckBounds, *ir.CheckOverflow, *ir.CheckConv, *ir.Trap:
					d.e.traps = true
				case *ir.UnsafeBegin:
					d.e.unsafe = true
				case *ir.Call:
					switch {
					case i.Fn != nil || i.Intrinsic == "ptr_init" || i.Intrinsic == "ptr_init_ctx":
						// a call through a function value (ptr_init and
						// ptr_init_ctx call their initializer through one):
						// any effect, unknown
						d.e.allocates, d.e.traps, d.e.spawns, d.e.unsafe = true, true, true, true
						d.e.label = diag.LabelUnknown
					case i.Intrinsic != "":
						if always, known := allocating[i.Intrinsic]; known && (always || i.Intrinsic != "clone" || len(i.Dsts) == 0 || !resolve.IsCopy(i.Dsts[0].Type)) {
							d.e.allocates = true
						}
						if trapping[i.Intrinsic] || (i.Release != "" && trapping[i.Release]) {
							d.e.traps = true
						}
						if spawning[i.Intrinsic] {
							d.e.spawns = true
						}
						if unsafeIntrinsic(i.Intrinsic) {
							d.e.unsafe = true
						}
					default:
						if mod.Func(i.Callee) != nil {
							d.callees = append(d.callees, i.Callee)
						}
					}
				case *ir.AccessEnd:
					if i.Release != "" && trapping[i.Release] {
						d.e.traps = true
					}
				case *ir.Drop:
					if g := destructorOf(mod, i.Type); g != nil {
						d.dtors = append(d.dtors, g.Name)
					}
				}
			}
			if _, ok := b.Term.(*ir.Unreachable); ok {
				d.e.traps = true
			}
		}
		directs[f.Name] = d
	}
	join := func(e *effects, o *effects) bool {
		changed := false
		set := func(dst *bool, v bool) {
			if v && !*dst {
				*dst = true
				changed = true
			}
		}
		set(&e.allocates, o.allocates)
		set(&e.traps, o.traps)
		set(&e.spawns, o.spawns)
		set(&e.unsafe, o.unsafe)
		if o.label == diag.LabelUnknown && e.label != diag.LabelUnknown {
			e.label = diag.LabelUnknown
			changed = true
		} else if o.label == diag.LabelAssumedUnsafe && e.label == diag.LabelProven {
			e.label = diag.LabelAssumedUnsafe
			changed = true
		}
		return changed
	}
	for changed := true; changed; {
		changed = false
		for _, f := range defined {
			e := summaries[f.Name]
			d := directs[f.Name]
			if join(e, &d.e) {
				changed = true
			}
			for _, c := range append(append([]string{}, d.callees...), d.dtors...) {
				if o := summaries[c]; o != nil && join(e, o) {
					changed = true
				}
			}
		}
	}
	return summaries, defined
}

// destructorOf finds the lowered destructor of a named type, if any.
func destructorOf(mod *ir.Module, t resolve.Type) *ir.Func {
	n, ok := t.(*resolve.Named)
	if !ok || n.Decl.Destructor == nil {
		return nil
	}
	return mod.Func(n.String() + ".destructor")
}
