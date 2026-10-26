# ADR-001: Second-class access instead of first-class references

**Status:** Decided
**Date:** 2026-10-26
**Spec:** §1, §5.2, §5.3, §5.4, §7 (IR invariant 5); evaluation §2 (P1, P4, P5), §3, §4.2–4.5.
**Decides:** everything else. ADR-002 through ADR-007 assume this record.

## Problem

The safety contract (spec §5) requires safe code to be free of use-after-free, double-free,
dangling references, uninitialized reads, unsynchronized mutable aliasing, and data races,
with no mandatory garbage collector and no lifetime annotations. Evaluation P1 states the
diagnosis: lifetime annotations are a symptom; the cause is *first-class references*, values
of reference type that can be returned, stored in aggregates, captured, and held across
other operations. Any design that keeps first-class references needs lifetimes, written or
inferred, to say what each reference depends on. Removing the syntax while keeping escaping
references yields either unsoundness or an inference engine whose failures cannot be
explained to an agent in one sentence (spec §3, principles 2 and 3).

The decision is therefore not "which lifetime syntax" but "whether references are values".

## Alternatives

Evaluated in the §3 matrix of the evaluation against the five structure classes (B-tree
split, intrusive structures, arena AST, FFI callbacks, concurrency).

- **A. Rust: ownership + first-class borrows + lifetimes** [rust-reference] [rustonomicon].
  Expressiveness baseline; `&mut` gives the same `noalias` codegen. Rejected: lifetimes,
  variance, non-lexical lifetimes, reborrows and drop check are exactly the semantic
  complexity spec §3 forbids, and every one of them exists to track escaping references.
- **C. Regions as types (Cyclone, Tofte–Talpin)** [cyclone-regions-2002] [tofte-talpin-1997].
  Rejected as the access mechanism: region variables are lifetime variables under another
  name, and inference makes extents computed rather than written (P2). Treated in ADR-002.
- **D. Linear types with borrow regions (Austral)** [austral-spec]. Informative: the chosen
  model is Austral without region names and with affine (droppable) values. Rejected as is:
  named borrow regions reintroduce the annotation, and strict linearity is verbose.
- **E. Reference capabilities (Pony)** [pony-caps-2015]. Rejected: the six-capability
  lattice is high semantic complexity and the model depends on a garbage collector. Its
  `iso`/`val` ideas survive as `Send`/`Share` (ADR-007).
- **F. Generational references everywhere (Vale)** [vale-genrefs]. Rejected as core: every
  dereference pays a generation check and can fault. Adopted only as the `Pool` handle
  design (ADR-002).
- **B. Unique ownership + second-class access (chosen).** Hylo's mutable value semantics
  [hylo-mvs] [hylo-tour] with the second-class discipline formalized and proved sound by
  Osvald et al. [osvald-2016] [osvald-2017]. Swift [swift-se-0446] [swift-lifetime-pitch]
  and Mojo [mojo-origins] [mojo-1.0] started here and each later added a restricted
  lifetime dependence; that history is the evidence both for the model and for the one
  known wall (reference-holding aggregates), which spec §5.6 reserves one valve for.

## Invariants

1. Safe code has no reference-typed values. `view[T]` and `edit[T]` (and the sequence forms
   `view[]T`, `edit[]T`) appear only as parameter types, as return types under the elision
   rule, and in `with`/`match`/`for` bindings (spec §5.2, `spec/types.md` §2).
2. An access is never stored in a struct, union, array, or container (`ACC_ESCAPE_STORE`);
   never returned except under §5.3 (`ACC_ESCAPE_RETURN`); never captured; never live
   across a thread spawn other than a scoped spawn (`ACC_LIVE_ACROSS_SPAWN`); never live
   across any future suspension point.
3. There are no reference-typed `:=` locals: binding a projection with `:=` is
   `ACC_UNBOUND_PROJECTION`. Every access window is written in the source as a call
   argument, an expression-scoped projection, or a lexical block; nothing is inferred.
4. The aliasing check is syntactic path disjointness plus lexical scope (§5.4): two
   simultaneously live accesses with the same root conflict iff their paths overlap and at
   least one is `edit`, `consume`, or `init`.
5. IR invariant 5 (§7): no second-class value flows into a store, a non-elision return, or
   an escaping call argument, asserted by the validator after every pass.

## Hard counterexample

The program the rule rejects is the reference-holding aggregate, Rust's
`Parser<'a> { input: &'a [u8] }`, and the reference-typed local that outlives a mutation:

```text
struct Cursor { data: view[]u8; pos: usize }        // ACC_ESCAPE_STORE: an access in a field

func first_then_grow(v: edit[Vec[u8]]) -> u8 {
    p := v.at(0)                                   // ACC_UNBOUND_PROJECTION: no reference-typed local
    v.push(1u8)                                    // would reallocate under p
    return p
}
```

Both are expressible in Rust (the second only until the borrow checker rejects `push`).
The accepted kern form passes the sequence to each operation and keeps offsets in the struct:

```text
struct Cursor { pos: usize }

func next(c: edit[Cursor], data: view[]u8) -> u8 {
    b := data[c.pos]                               // bounds-checked projection, consumed in-statement
    c.pos = c.pos + 1
    return b
}

func first_then_grow(v: edit[Vec[u8]]) -> u8 {
    b := v.get(0)                                  // u8 is Copy: the read ends before push begins
    v.push(1u8)
    return b
}
```

## Expressiveness cost

Stated honestly in evaluation §3: (1) structs that hold references, rewritten as offsets plus
an access parameter per method; (2) borrowing iterators over user containers, rewritten as
built-in iteration over arrays, slices and views plus the cursor-handle pattern
(`c := t.first(); while c.valid() { ... t.at(c) ... c = t.next(c) }`); (3) reference-returning
functions with several input accesses, not expressible (ADR-004); (4) holding an access across
unrelated mutation of a different object is *not* lost, because roots are tracked
syntactically. Items 1 and 2 are the wall that moved Swift and Mojo; spec §5.6 reserves
`view struct` for it, added iff Phase 0 shows at least one §8 workload needs it, and Phase 0
fails if more than two do. Callbacks retaining references are not expressible at all (§5.8).

## Performance cost

Approximately zero for conventions: every `edit` parameter is provably unaliased for the
call and lowers to LLVM `noalias`; every non-`Interior` `view` lowers to `noalias readonly`
(§5.4 implementation guidance, marked unproven in the spec). The intrinsic cost is in user code that
would use pointers in C and uses `base + index` handles here (P11); that cost belongs to
ADR-002 and is reported separately for user-level handle code and library unsafe internals
(spec §2.3). The checker has no dataflow beyond definite initialization and moved-ness.

## Complexity

Four conventions (`view`, `edit`, `own`, `out`), three binding forms (`with`, `match`,
`for`), one elision rule, one syntactic overlap rule. Evaluation §10 targets the Phase 0
checker prototype at under 3 kLOC of Go. Rejections are explainable in one sentence naming
one rule ("`v` is edited by argument 1 and viewed by argument 2"), which is what the
primary agent metric (spec §2.3) depends on. The cost is concentrated in the in-statement
access-liveness rule (§5.4, PLAN.md Phase 0 risk), not in the model.

## Experiments

- PLAN.md 0.4: reject fixtures `ACC_ESCAPE_STORE`, `ACC_ESCAPE_RETURN`,
  `ACC_UNBOUND_PROJECTION`, `ACC_LIVE_ACROSS_SPAWN`, and the four `ACC_CONFLICT` variants
  (edit/edit, edit/view, consume/view, enclosing scope), each with an accepting twin.
- PLAN.md 0.4: the §5.8 adversarial fixtures, in particular "interior pointers after
  container growth" (rejected by §5.4) and "callbacks retaining references" (not expressible).
- PLAN.md 0.5: `examples/vector`, `examples/btree`, `examples/arena_ast`,
  `examples/packet_parser`, `examples/thread_handoff` accepted by `kern check`.
- PLAN.md 0.6: thirteen workload notes under `docs/workloads/`; the reserved-extension count
  is the go/no-go number (at most two).
- PLAN.md 2.6: the codegen experiment the spec marks unproven, the same loop bodies with and without
  the aliasing attributes and against the Rust `&mut`/`&` version.

## Conclusion

Adopt unique ownership plus second-class access as the sole ownership mechanism. No safe
reference escapes except by the §5.3 elision rule. Failure of any Phase 0 exit criterion
(spec §11) reopens this record; the second-tier fallback, lifetime dependence on returns from
multiple sources (Mojo origins, Swift `@lifetime`), is recorded in evaluation §6 as a possible
Phase 0 outcome that concedes the thesis, not as a failure of the plan.
