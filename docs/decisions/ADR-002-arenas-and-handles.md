# ADR-002: Arenas, pools and handles as library types; no regions in the type system

**Status:** Decided
**Date:** 2026-10-26
**Spec:** §3 (principle 9), §5.6, §5.8; evaluation §2 (P2, P3), §3 rows C and F, §4.6, §5.
**Depends on:** ADR-001. **Interacts with:** ADR-004 (`get2`), ADR-008 (check order).

## Problem

Trees, graphs, parent pointers, cycles, intrusive queues and buffer pools all need an object
to refer to another object that it does not own. Under ADR-001 an access cannot be stored,
so these references must be *data*, not accesses. Spec v0.1 offered two mechanisms for this,
regions in the type system and stable handles; evaluation P3 showed they are the same feature
twice, and P2 showed that regions as described ("cross-region references", "nested regions")
are exactly the cases where Cyclone needed region type parameters and where Tofte–Talpin
inference becomes unpredictable. Principle 9 (spec §3) requires one ownership mechanism with
arenas, pools, handles and synchronization as library types built on it.

## Alternatives

- **Regions as types with annotations (Cyclone)** [cyclone-regions-2002] [cyclone-2002].
  Nodes in one region, pointers within a region are safe, cycles are fine, split is trivial.
  Rejected: region variables on functions and structs are lifetime annotations, which
  contradicts the goal; and a single node can never be freed, since a region frees as a whole.
- **Region inference (Tofte–Talpin)** [tofte-talpin-1997]. Rejected: a region's extent is
  computed, not written, so allocation cost is not inspectable (spec §3 principle 2), and
  region leaks are a known pathology that an agent cannot be told how to fix.
- **Named borrow regions (Austral)** [austral-spec]. Rejected for the same reason as
  Cyclone: the name is an annotation. The owned-container-plus-index pattern Austral programs
  use for arena ASTs is the pattern adopted here.
- **Generational references on every object (Vale)** [vale-genrefs]. Rejected as a global
  mechanism: a generation word per object and a check per dereference. Adopted *locally*: a
  `Pool[T]` slot carries a generation and a `PoolId[T]` carries the expected one.
- **Rust: lifetimes on nodes, or `Rc`/`RefCell`/`Weak`** [rust-reference]. Rejected: the
  first needs lifetimes, the second is runtime borrow tracking with a reference count, and
  Rust's own arena and graph crates settle on indices anyway.
- **Chosen: owned containers with `Copy` handles.** `Arena[T]` (bump, freed as a whole),
  `Pool[T]` (free list with generation counters; a slot whose generation would wrap is
  retired) are standard-library owned
  values with audited unsafe internals; nothing in the checker knows about them.

## Invariants

1. There are no regions in the type system. An arena is an owned value; its contents are
   destroyed when it is destroyed; `Pool.free(id)` runs the element destructor and
   increments the slot's generation (spec §5.6).
2. A handle (`ArenaId[T]`, `PoolId[T]`) is a `Copy` integer-like value and is permitted to be
   stored anywhere, including inside objects in the same container.
3. Dereference is a projection: `get(id) -> view[T]`, `at(id) -> edit[T]`,
   `get2(a, b) -> (edit[T], edit[T])`, subject to the elision and multi-access rules of
   ADR-004. A successful dereference requires, in order: slot index in bounds (else trap
   `bounds`); in debug builds, equal nonzero identity tags (else `identity`); slot occupied
   by a live, fully initialized element (else `vacant`); equal generation (else `generation`).
   `Pool.free` performs the same checks in the same order.
4. A handle is not bound to a container. Using it with another container of the same element
   type is a logic error that is memory-safe: every slot is always valid memory of the right
   type. Logical validity (runtime) and memory safety (static) are distinct guarantees.
5. Identity tags are 32-bit, drawn from a process-global atomic counter starting at 1, never
   reused; tag 0 disables the identity check for that container. Generations are 32-bit; a
   slot whose generation would wrap is retired and never reused.

## Hard counterexample

The case a region type system cannot express is freeing one node of a structure while the
rest lives on, with the freed node's old handle detected afterwards:

```text
struct Node { keys: [MAX]u64; count: u32; children: [MAX1]PoolId[Node]; leaf: bool }
struct BTree { nodes: Pool[Node]; root: PoolId[Node] }    // handles stored inside the pool's own elements

func remove_child(t: edit[BTree], parent: PoolId[Node], i: u32) {
    child := t.nodes.get(parent).children[i]       // Copy handle: the projection ends in this statement
    t.nodes.free(child)                            // frees exactly one node; a region frees all or nothing
    with p := t.nodes.at(parent) {
        p.count = p.count - 1
    }
    with c := t.nodes.at(child) {                  // traps `vacant` (or `generation` once the slot is reused)
        c.leaf = true
    }
}
```

Under Cyclone the same `Node` would carry a region parameter and `free(child)` would not
exist. Under Rust, `children: [NodeId; MAX1]` is the same index pattern, so B loses nothing
relative to A here (evaluation §3, B-tree row).

## Expressiveness cost

Handle validity is a runtime property, not a compile-time one: a stale or foreign handle is
caught by a trap, where Rust's borrow checker would reject the program and a region system
would make the stale case unwritable. Safe code cannot hold a pointer into a container
across a mutation of that container (rejected by §5.4). Intrusive structures with raw
interior pointers still need audited unsafe internals behind a handle API, as in Rust and in
the Linux kernel's Rust layer; no model without a garbage collector does better.

## Performance cost

The one intrinsic cost ADR-001 isolates (evaluation P11): user code that would chase a
pointer in C computes `base + index` and compares a generation (plus an identity tag in
`--opt debug`). It is measurable on B-tree descent, intrusive run queues and graph traversal.
Library containers may use audited unsafe raw pointers internally, as Rust's `BTreeMap`
does, so the 10% target (spec §2.3) is reported separately for user-level handle code and
for library internals. Memory cost: one generation word per pool slot, one identity tag per
container, and retired slots after generation wrap.

## Complexity

Zero in the checker: arenas are ordinary owned values and handles are ordinary `Copy` data.
The complexity moves into the standard library's unsafe internals, each wrapped by a
contract block (spec §6, `UNSAFE_MISSING_CONTRACT`) and listed in the unsafe audit
(PLAN.md 3.4). The runtime check order and the identity/generation exhaustion rules are the
only semantics the spec fixes, and they are the same for every container.

## Experiments

- PLAN.md 0.4: adversarial fixtures and `tests/adversarial/NOTES.md` for intrusive linked
  lists, self-reference and parent pointers and cycles, interior pointers after container
  growth, and two simultaneous edits into one pool.
- PLAN.md 0.5: `examples/btree/btree.kern` (split and merge via `Pool.get2`) and
  `examples/arena_ast/ast.kern` (`Arena` handles).
- PLAN.md 1.2 and 1.4: `Arena`/`Pool` intrinsics with the check order, tag counter and
  tag-0 rule; trap fixtures `bounds`, `generation`, `vacant`, `identity` (restricted to
  `opt=debug`, with an `opt=release` accept twin showing the foreign-handle access completing
  memory-safely) and `disjoint`, each with a completing twin; `alloc_with` failure fixtures.
- PLAN.md 2.3: generation-wrap retirement through `kern_test_pool_seed_generation`.
- PLAN.md 2.6 and 3.4: B-tree, arena AST, buffer pool and intrusive run queue benchmark rows
  with user-level and library rows separated.

## Conclusion

No regions in the type system. `Arena[T]` and `Pool[T]` are owned library
containers; `Copy` handles are the only stored reference-like data in safe code; dereference
is a checked projection. The `view struct` extension of §5.6 is the only reserved addition
and is unrelated to regions; a Phase 0 workload count above two reopens ADR-001, not this
record.
