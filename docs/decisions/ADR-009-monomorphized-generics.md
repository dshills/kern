# ADR-009: Generics monomorphized with nominal interface constraints; no dynamic dispatch in the core

**Status:** Decided (Phase 0, per PLAN.md 0.1 and 0.2)
**Date:** 2026-10-26
**Spec:** §4, §10 (ADR-9), §7 (`show-effects`: statically resolved callees); `spec/types.md`
§2 (function types), §4 (generics); `spec/grammar.ebnf` (`type_params`, `constraint`,
`interface_decl`, `impl_decl`); evaluation §4.9; PLAN.md 0.3 (constraints checked,
bodies not instantiated), 1.1 (`compiler/lower/mono.go`). Spec §10 lists this record as
open; PLAN.md closes it in Phase 0 because the resolver needs the constraint rules.
**Depends on:** ADR-001 (receivers are conventions), ADR-003 (`Copy` as a constraint).

## Problem

Containers (`Vec[T]`, `Pool[T]`, `Result[T, E]`) and algorithms over them need type
parameters. Spec §3 principle 2 requires dynamic dispatch to be explicit or
compiler-inspectable, and `show-effects` must resolve callees statically to label effects
`proven`. The record must fix how type parameters are constrained, how calls on them are
resolved, how instantiation happens, and whether any dynamic dispatch exists in the core.

## Alternatives

- **Rust traits, monomorphization, `dyn Trait` objects** [rust-reference]. Adopted in
  substance minus the trait machinery: nominal interfaces with explicit `impl`,
  monomorphized instantiation, no associated types, no specialization, no `dyn`.
- **C++ templates and concepts** [iso-cpp20]. Rejected: unconstrained templates report
  errors at instantiation inside the callee, which is the unexplainable failure spec §3
  forbids; concepts are structural.
- **Zig `comptime` duck typing** [zig-ref]. Rejected for the same reason: a generic body is
  checked only per instantiation, so a diagnostic names the caller's types, not a rule.
- **Austral typeclasses** [austral-spec] and **Ada generics with explicit instantiation**
  [ada-rm]. Both nominal and static; consistent with the choice. Ada's requirement to name
  every instantiation is relaxed to first-order inference from argument types.
- **Hare: no generics** [hare-spec]. Rejected: the §8 containers would be written per element
  type or through `void*`-style unsafe.
- **Dictionary passing or a GC-backed interface value (Go)**. Rejected in the core: hidden
  indirect calls and, in Go's case, a garbage collector. `dyn I` as an explicit owned box or
  explicit view is the form spec §10 names for a later addition; the interface name is not a
  type until then.

## Invariants

1. Type parameters are declared on structs, unions, interfaces, impls and functions:
   `[T: Copy + Ord, U]`. Constraints are the built-in markers `Copy`, `Send`, `Share` and
   nominal interfaces (`spec/types.md` §4).
2. An unconstrained parameter admits every type and permits only moves, `drop`, and passing
   by convention; calling a method requires the interface constraint
   (`CONSTRAINT_REQUIRED`).
3. Instantiation is explicit (`f[u64](x)`) or inferred by first-order unification over the
   parameter types (`TYPE_INFER_FAILED`); an unsatisfied constraint is
   `CONSTRAINT_UNSATISFIED`. A generic function must be instantiated before use as a value.
4. `impl Name for T` provides every method with an identical signature (`IMPL_INCOMPLETE`,
   `IMPL_SIGNATURE_MISMATCH`). Types are nominal; two declarations are never the same type.
5. There is no dynamic dispatch; an interface name is not a type. Phase 0 checks generic
   bodies once against their constraints; from Phase 1 every instantiation is specialized to
   monomorphic IR before validation, so layout, destructors and marker derivation are
   computed on concrete types only.
6. `show-effects` is transitive over statically resolved callees; only a call through a
   function pointer or into FFI makes effects `unknown` (§16).

## Hard counterexample

The heterogeneous collection that needs `dyn`, and the duck-typed body that needs a
constraint:

```text
interface Shape { func area(view self) -> f64 }
struct Circle { r: f64 }
struct Rect { w: f64; h: f64 }
impl Shape for Circle { func area(view self) -> f64 { return 3.14159 * self.r * self.r } }
impl Shape for Rect   { func area(view self) -> f64 { return self.w * self.h } }

struct Scene { items: Vec[Shape] }                      // rejected: an interface name is not a type

func largest[T](a: view[]T) -> usize {
    best := 0usize
    for i in 1..len(a) {
        if a[best].less(a[i]) { best = i }              // CONSTRAINT_REQUIRED: T has no Ord constraint
    }
    return best
}
```

The accepted forms are a closed tagged union with exhaustive `match`, or a function pointer
with an owned context (ADR-010), and a constrained parameter:

```text
union Item { C(Circle); R(Rect) }
func area(it: view[Item]) -> f64 {
    match it {
        C(c) => return c.area()
        R(r) => return r.area()
    }
}

func largest[T: Ord](a: view[]T) -> usize { ... }       // monomorphized per T; a[best].less(a[i]) resolves statically
```

## Expressiveness cost

No open-world polymorphism in the core: a collection of differently typed values is a
union, which must be edited when a variant is added, or a `(func, own ctx)` pair. No
associated types, no specialization, no default methods, no generic methods on interfaces
beyond what `func_sig` allows. Constraint inference is first-order only, so higher-order
uses need explicit instantiation. `dyn I`, when added, will be an explicit box or view and
is not part of this record.

## Performance cost

Static dispatch everywhere, so every call is inlinable and `show-effects` is exact. The
costs are code size (one copy per instantiation) and compile time, both recorded per
benchmark (`binary text-section bytes`, `compile time in milliseconds`, spec §2.3). The
incremental cache (PLAN.md 4.1) carries generic bodies in interface files because they are
specialized at the use site.

## Complexity

The resolver checks constraints once per generic body; the specialization pass is a
substitution with a per-instantiation cache; unification is first-order. There is no trait
coherence, no object safety, no vtable layout. The diagnostics are the five codes of
`spec/types.md` §4, each naming the constraint or signature at fault.

## Experiments

- PLAN.md 0.3: resolver with generic constraint checking, monomorphization deferred;
  `Copy`/`Send`/`Share` derivation as constraints; `kern check` on every generic in the
  `std/` stub declarations (`Vec`, `Pool`, `Arena`, `Result`, `Option`, `Shared`, `Mutex`).
- PLAN.md 0.5: `examples/vector/vector.kern` (a generic `Vec[T]` user) and
  `examples/btree/btree.kern` (`Pool[Node]`, `PoolId[Node]`).
- PLAN.md 1.1: `compiler/lower/mono.go` specializing every instantiation before validation;
  the validator runs on monomorphic IR; `emit-ir` round-trips the specialized form.
- PLAN.md 2.1: the specialization pass reused unchanged by the LLVM backend; `show-layout`
  verified against C `offsetof`/`sizeof` on instantiated types.
- PLAN.md 2.6 and 4.1: text-section and compile-time rows per workload; incremental-cache
  invalidation tests that change one generic body in an imported module.

## Conclusion

Monomorphized generics with nominal `interface`/`impl` constraints and the built-in markers
`Copy`, `Send`, `Share`; explicit or first-order-inferred instantiation; no dynamic dispatch
in the core and no interface-typed values. `dyn I` is deferred and, if added, is an explicit
owned box or explicit view.
