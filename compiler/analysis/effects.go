package analysis

import (
	"sort"
	"strings"

	"github.com/dshills/kern/compiler/diag"
	"github.com/dshills/kern/compiler/ir"
	"github.com/dshills/kern/compiler/resolve"
)

// trapKinds are the kinds show-effects lists: spec §6's, in its order,
// without stack, which every function's prologue can raise
// (specs/effect-trap-kinds §2).
var trapKinds = []string{"bounds", "overflow", "div_zero", "shift", "conversion", "generation", "vacant", "identity", "disjoint", "oom", "explicit", "unreachable"}

// kindSet is a set of listed trap kinds, bit i for trapKinds[i].
type kindSet uint16

// allKinds is every listed kind: a call the analysis cannot see into. A
// kindSet has room for 16 kinds; TestAllKindsCoversTrapKinds checks.
var allKinds = kindSet(1)<<len(trapKinds) - 1

func kindOf(k string) kindSet {
	for i, n := range trapKinds {
		if n == k {
			return 1 << i
		}
	}
	return 0
}

func kindsOf(ks ...string) kindSet {
	var s kindSet
	for _, k := range ks {
		s |= kindOf(k)
	}
	return s
}

// list returns the kinds of s in trapKinds order, never nil.
func (s kindSet) list() []string {
	out := []string{}
	for i, n := range trapKinds {
		if s&(1<<i) != 0 {
			out = append(out, n)
		}
	}
	return out
}

// trapping lists the intrinsics that may trap at runtime, with their kinds
// (checks the lowering does not emit as instructions: allocator
// obligations, recursive locking, reference-count overflow, raw memory in
// the interpreter, and the runtime objects' allocation, which traps oom).
// The containers' checks are Trap instructions in their kern bodies and
// reach callers through propagation. TestTrappingMatchesRuntime pins this
// table against the runtimes' trap sites.
var trapping = map[string]kindSet{
	"Mutex.lock": kindsOf("explicit"), "Condvar.wait": kindsOf("explicit"), "Condvar.wait_ns": kindsOf("explicit"), "Shared.clone": kindsOf("overflow"),
	"SystemAllocator.alloc": kindsOf("explicit"), "SystemAllocator.realloc": kindsOf("explicit"), "SystemAllocator.alloc_zeroed": kindsOf("explicit"), "SystemAllocator.free": kindsOf("explicit"),
	"simd_shuffle": kindsOf("bounds"), "asm": allKinds,
	"ptr_load": kindsOf("explicit"), "ptr_store": kindsOf("explicit"), "ptr_take": kindsOf("explicit"), "ptr_load_volatile": kindsOf("explicit"), "ptr_store_volatile": kindsOf("explicit"), "ptr_copy": kindsOf("explicit"), "ptr_store_seq": kindsOf("explicit"),
	"ptr_view": kindsOf("explicit"), "ptr_edit": kindsOf("explicit"), "ptr_view_seq": kindsOf("explicit"), "ptr_edit_seq": kindsOf("explicit"), "ptr_edit2": kindsOf("explicit"), "ptr_view2": kindsOf("explicit"), "ptr_view_edit": kindsOf("explicit"),
	"testing.require_hooks": kindsOf("explicit"), "testing.seed_shared_count": kindsOf("explicit", "overflow"),
	"testing.fail_next_alloc": kindsOf("explicit"), "testing.fail_next_spawn": kindsOf("explicit"),
	// the runtime objects: kern_alloc (natively) and runtimeAlloc (in the
	// interpreter) trap oom when their storage cannot be allocated
	"Shared.new": kindsOf("oom"), "Mutex.new": kindsOf("oom"), "Condvar.new": kindsOf("oom"),
	// a spawn that cannot allocate what the thread needs is an ordinary
	// spawn failure, Err(SpawnErr), and does not trap (specs/runtime RC3)
	"thread.scope": kindsOf("oom"),
	// std/time: a failure of the clock or of sleep, impossible for the
	// arguments the runtime passes, traps explicit (specs/std-time)
	"time_sleep_ns": kindsOf("explicit"), "time_wall_ns": kindsOf("explicit"),
	// std/str: parsing text over 1,023 bytes copies it to the heap for the C
	// call, which traps oom when that fails (specs/float-text)
	"str_parse_f64": kindsOf("oom"),
}

// checkKinds are the kinds a CheckOverflow can raise, by operator and
// operand type (the backend's checks, compiler/backend/llvm emitCheck).
func checkKinds(op, typ string) kindSet {
	switch op {
	case "/", "%":
		if strings.HasPrefix(typ, "i") {
			return kindsOf("div_zero", "overflow")
		}
		return kindsOf("div_zero")
	case "<<":
		return kindsOf("shift", "overflow")
	case ">>":
		return kindsOf("shift")
	}
	return kindsOf("overflow")
}

// spawning lists the thread-creating intrinsics.
var spawning = map[string]bool{"thread.spawn": true, "scope.spawn": true}

// unsafeIntrinsics are the enumerated unsafe operations among intrinsics.
func unsafeIntrinsic(name string) bool {
	return strings.HasPrefix(name, "ptr_") || name == "asm" || name == "mem_map" || name == "mem_unmap" || name == "Mutex.try_acquire" || name == "Mutex.held" || name == "SystemAllocator.alloc" || name == "SystemAllocator.realloc" || name == "SystemAllocator.alloc_zeroed" || name == "SystemAllocator.free"
}

type effects struct {
	allocates, traps, spawns, unsafe bool
	kinds                            kindSet // the trap kinds; traps iff non-empty
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
		res.Functions = append(res.Functions, diag.FunctionEffects{Name: f.Name, Allocates: e.allocates, Traps: e.traps, TrapKinds: e.kinds.list(), Spawns: e.spawns, Unsafe: e.unsafe, Label: e.label})
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
			summaries[f.Name] = &effects{allocates: true, traps: true, spawns: true, unsafe: true, kinds: allKinds, label: diag.LabelAssumedUnsafe}
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
				case *ir.CheckBounds:
					d.e.traps, d.e.kinds = true, d.e.kinds|kindsOf("bounds")
				case *ir.CheckOverflow:
					d.e.traps, d.e.kinds = true, d.e.kinds|checkKinds(i.Op, i.Type)
				case *ir.CheckConv:
					d.e.traps, d.e.kinds = true, d.e.kinds|kindsOf("conversion")
				case *ir.Trap:
					d.e.traps, d.e.kinds = true, d.e.kinds|kindOf(i.Kind)
				case *ir.UnsafeBegin:
					d.e.unsafe = true
				case *ir.Call:
					switch {
					case i.Fn != nil || i.Intrinsic == "ptr_init" || i.Intrinsic == "ptr_init_ctx":
						// a call through a function value (ptr_init and
						// ptr_init_ctx call their initializer through one):
						// any effect, unknown
						d.e.allocates, d.e.traps, d.e.spawns, d.e.unsafe = true, true, true, true
						d.e.kinds = allKinds
						d.e.label = diag.LabelUnknown
					case i.Intrinsic != "":
						if always, known := allocating[i.Intrinsic]; known && (always || i.Intrinsic != "clone" || len(i.Dsts) == 0 || !resolve.IsCopy(i.Dsts[0].Type)) {
							d.e.allocates = true
						}
						if k, ok := trapping[i.Intrinsic]; ok {
							d.e.traps, d.e.kinds = true, d.e.kinds|k
						}
						if i.Release != "" {
							if k, ok := trapping[i.Release]; ok {
								d.e.traps, d.e.kinds = true, d.e.kinds|k
							}
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
					if i.Release != "" {
						if k, ok := trapping[i.Release]; ok {
							d.e.traps, d.e.kinds = true, d.e.kinds|k
						}
					}
				case *ir.Drop:
					if g := destructorOf(mod, i.Type); g != nil {
						d.dtors = append(d.dtors, g.Name)
					}
				}
			}
			if _, ok := b.Term.(*ir.Unreachable); ok {
				// the terminator after a Trap is never reached: the trap
				// already terminated (and set traps)
				d.e.traps = true
				if !endsInTrap(b) {
					d.e.kinds |= kindsOf("unreachable")
				}
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
		if o.kinds&^e.kinds != 0 {
			e.kinds |= o.kinds
			changed = true
		}
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

// endsInTrap reports whether a block holds a Trap instruction, after which
// its terminator is dead.
func endsInTrap(b *ir.Block) bool {
	for _, in := range b.Instrs {
		if _, ok := in.(*ir.Trap); ok {
			return true
		}
	}
	return false
}

// destructorOf finds the lowered destructor of a named type, if any.
func destructorOf(mod *ir.Module, t resolve.Type) *ir.Func {
	n, ok := t.(*resolve.Named)
	if !ok || n.Decl.Destructor == nil {
		return nil
	}
	return mod.Func(n.String() + ".destructor")
}
