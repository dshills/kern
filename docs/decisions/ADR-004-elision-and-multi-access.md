# ADR-004: Single-source elision, permission rule, release-escape rule, and unsafe multi-access returns

**Status:** Decided
**Date:** 2026-10-26
**Spec:** §5.3, §5.4 (multi-access bindings, derived accesses), §5.6 (`get2`), §6 (enumerated
unsafe operations), §13 accepted gap 3; evaluation §3 (B-tree row, item 3), §4.4, §4.5.
**Depends on:** ADR-001, ADR-002.

## Problem

Container accessors must return access to an element (`get`, `at`, `slice`, `first`,
`payload`), and a B-tree split must edit two nodes of one pool at once. Under ADR-001 a
returned access has no lifetime annotation to say which parameter it depends on, and the
checker proves index disjointness only for compile-time constants. The record must say how a
returned access gets its root, how strong it may be, what happens to a `release` obligation,
and how two simultaneous edits into one container are obtained without lifetimes and without
unsoundness.

## Alternatives

- **Rust lifetime elision plus explicit lifetimes for the rest** [rust-reference]. Rust's
  elision rules cover the single-input case and fall back to `'a` annotations for
  `fn pick<'a>(&'a A, &'a B) -> &'a T`. Rejected: the fallback is the annotation ADR-001
  removes. kern keeps the elision case and makes the fallback inexpressible.
- **Mojo origins and Swift lifetime dependence** [mojo-origins] [mojo-1.0]
  [swift-lifetime-pitch]. Both grew a dependency annotation on returns exactly for
  multi-source and container-element references. Recorded in evaluation §6 as the second-tier
  valve that concedes the thesis; not adopted.
- **Hylo projections (subscripts that `yield`)** [hylo-tour] [hylo-mvs]. Adopted in
  substance: a returned access is a projection of one parameter, expression-scoped or bound
  by `with`/`match`/`for`. kern writes it as an ordinary function returning `view`/`edit`.
- **Static disjointness proofs (SPARK-style)** [spark-rm]. Rejected for the core: proving
  `a != b` for arbitrary indices needs a prover; spec §5.4 uses only the constant-index and
  constant-slice cases.
- **Rust `split_at_mut` / `get_many_mut`** [rustonomicon]. Adopted in form: a disjoint
  multi-access accessor implemented with audited unsafe behind a safe signature, with the
  disjointness check at runtime.

## Invariants

1. **Elision.** A function returns `view[U]`, `edit[U]`, or a tuple of those only if it has
   exactly one `view`/`edit` parameter (a receiver counts). The returned access carries that
   parameter's root place. Two or more candidates: `ACC_AMBIGUOUS_SOURCE`. Derived from a
   local: `ACC_ESCAPE_RETURN`. Multiple sources: not expressible.
2. **Permission.** A returned access is never stronger than its source: a `view` source
   cannot yield `edit` (`ACC_PERMISSION_ESCALATION`). The same rule governs derived accesses
   through a binding (§5.4).
3. **Release escape.** An access carrying a pending release function is consumed only within
   the function that obtained it; returning it is `ACC_RELEASE_ESCAPE`. A release function
   runs exactly once when the access ends, including on `return`, `?`, `break`, `continue`;
   a trap runs none; releases run in reverse order of access creation.
4. **Multi-access.** A function returning two or more accesses is implemented inside `unsafe`
   (or composes only such functions); its contract states pairwise disjointness; a runtime
   check traps (`get2(a, b)` traps with kind `disjoint` when `a == b`, after validating `a`,
   then `b`, per spec §13 gap 3). Names bound by one `with` from such a projection are
   distinct roots and never conflict with each other; the projection's root is frozen.
5. **Runtime-exclusive escalation** (`Mutex[T].lock(view self) -> edit[T] release unlock`,
   buffer-pool `pin`) is an enumerated unsafe operation whose contract names the runtime
   mechanism that guarantees exclusivity; the returned `edit` is rooted at the receiver.
6. A returned projection is expression-scoped: consumed in the same statement as an
   argument, assignment target, `with`/`match`/`for` subject, or `Copy` field read.

## Hard counterexample

The programs the rules reject, and the one form that is accepted:

```text
func pick(a: view[Node], b: view[Node], left: bool) -> view[Node] {   // ACC_AMBIGUOUS_SOURCE
    if left { return a }
    return b
}
// Rust writes fn pick<'a>(a: &'a Node, b: &'a Node, left: bool) -> &'a Node. Not expressible here;
// return a Copy handle or index and dereference it at the caller.

func first_mut(v: view[Vec[u8]]) -> edit[u8] {
    return v.at(0)                       // ACC_PERMISSION_ESCALATION: edit derived from a view
}

func lock_and_leak(m: view[Mutex[u64]]) -> edit[u64] {
    return m.lock()                      // ACC_RELEASE_ESCAPE: unlock may not cross a return
}

func split_child(t: edit[BTree], a: PoolId[Node], b: PoolId[Node]) {
    with l, r := t.nodes.get2(a, b) {    // unsafe-implemented; traps `disjoint` when a == b
        r.count = l.count - MIN
        l.count = MIN
    }                                    // both accesses end; t.nodes is unfrozen
}
```

`get2` itself must be written as `unsafe { ... }` under a `contract { ensures "..." }` block,
because the checker treats every pair of non-constant indices into one container as
overlapping (§5.4) and only the runtime comparison establishes disjointness.

## Expressiveness cost

Evaluation §3 item 3: reference-returning functions with several input accesses are not
expressible; "longest of two slices" or "pick one of two nodes" returns a `Copy` index or
handle instead. Static disjointness is limited to constant indices and constant slice
bounds, so every "edit two elements" API is an unsafe-implemented accessor with a runtime
check rather than a safe user function. A lock guard cannot be handed up the call stack.

## Performance cost

One equality (or range-overlap) comparison per multi-access call, reported by
`show-bounds-checks` as kind `disjoint`, label `runtime-checked`. One call per `release` at
each access end, at a statically known point (no landing pads, ADR-006). The elision rule
itself costs nothing: the returned access is the same pointer, or (pointer, length) pair,
that a Rust elided borrow would be.

## Complexity

The elision rule is a parameter count; the permission rule is a comparison of two kinds; the
release-escape rule is a flag on the access. The multi-access rule moves the hard case into
the library, where it is audited once per container rather than proven per call site. The
checker's only new state is "frozen root for the block" (§5.4), which the single-access
`with` already needs.

## Experiments

- PLAN.md 0.4: reject fixtures `ACC_AMBIGUOUS_SOURCE`, `ACC_ESCAPE_RETURN`,
  `ACC_PERMISSION_ESCALATION`, `ACC_RELEASE_ESCAPE`, `UNSAFE_OP_OUTSIDE_UNSAFE`,
  `UNSAFE_MISSING_CONTRACT`, each with an accepting twin; adversarial fixture "two
  simultaneous edits into one pool" (`get2`, runtime disjointness).
- PLAN.md 0.5: `examples/btree/btree.kern` (split and merge through `Pool.get2`) and
  `examples/packet_parser/parser.kern` (elision on `view[]u8`).
- PLAN.md 1.2 and 1.4: `get2` validation order (`a`, then `b`, then compare); the `disjoint`
  trap fixture and its completing twin; release-at-access-end on `return`, `?`, `break`,
  `continue` (named as a Phase 1 risk requiring explicit fixtures).
- PLAN.md 3.3 and 3.4: `Mutex[T].lock` as runtime-exclusive escalation with `release
  unlock`, recursive acquisition trapping `explicit`; the buffer pool's `pin`/`unpin`.
- PLAN.md 2.5: `show-bounds-checks` reporting every `disjoint` check with its label.

## Conclusion

Returned accesses come from exactly one access parameter, are never stronger than it, and
never carry a release obligation across a return. Two or more simultaneous accesses into one
place are obtained only from unsafe-implemented accessors with a documented, runtime-checked
disjointness contract; runtime-exclusive escalation is the same pattern for locks and pins.
