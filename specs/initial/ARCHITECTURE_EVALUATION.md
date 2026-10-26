# Architecture Evaluation — AI-Native Systems Language SPEC v0.1

**Status:** Pre-implementation review. **Date:** 2026-10-26.
**Input:** `AI_Native_Systems_Language_SPEC.md` v0.1.
**Scope:** Fundamental problems, ownership-model alternatives, and the simplest architecture that meets the safety contract (§5) and the performance target (§2). Everything marked *[unproven]* needs a Phase 0 experiment before it is relied on.

---

## 1. Verdict

**Feasible, conditionally.** The safety contract in §5 (no use-after-free, double-free, dangling, uninitialized reads, unsynchronized mutable aliasing, data races in safe code) can be met without lifetime annotations, but only by giving up *first-class references entirely*, not merely lifetime syntax. The spec already leans this way (`view`/`edit` as non-escaping parameters) but then undercuts it by adding two more ownership mechanisms (regions and stable handles) and by leaving open whether any safe reference may escape.

The recommended core is one mechanism, not three:

> **Unique ownership + second-class access (parameter conventions and lexically scoped projections) + arenas-with-handles as an ordinary library pattern.**

This is the model Hylo (formerly Val) calls *mutable value semantics*, and it is the model Mojo and Swift started from before each added a restricted form of lifetime dependence. That history is the strongest evidence available: the annotation-free model works for most systems code, and it has one known expressiveness wall (aggregates that hold references, chiefly iterators and parser/cursor objects). The recommendation below keeps the simple core and reserves exactly one escape valve for that wall, to be opened only if Phase 0 shows the workloads need it.

Phase 0 should be a go/no-go on this core, exercised against the thirteen workloads in §8. Do not build regions, closures, or dynamic dispatch into the checker until the core survives those workloads.

---

## 2. Fundamental problems in the spec as written

Ordered by risk. Each is a hypothesis the spec treats as settled or leaves ambiguous.

**P1. The spec misdiagnoses where Rust's complexity comes from.**
Lifetime *annotations* are a symptom. The cause is first-class references: values of reference type that can be returned, stored in structs, captured, and held across other operations. Any design that keeps first-class references needs lifetimes (named or inferred) to say what they depend on. Removing annotations while keeping escaping references yields either unsoundness or an inference engine whose failures are unexplainable to an agent. The real design choice is *second-class references* (stack-bounded, non-escaping), and the spec should say so explicitly. Reference: Osvald et al., "Gentrification Gone Too Far?" (OOPSLA 2016), which formalizes second-class values with stack-bounded lifetimes and proves soundness; its 2017 follow-up applies them to Rust-like borrowing.

**P2. Regions, as described, reintroduce lifetime variables.**
"Cross-region references" and "nested regions" are exactly the cases where Cyclone needed region type parameters on functions and structs. Region *inference* (Tofte–Talpin) avoids annotations but is unpredictable in exactly the way principle 2 forbids: a region's extent is computed, not written, and leaks are a known pathology. Either regions get annotations (contradicting the goal) or inference (contradicting predictability). **Recommendation: no regions in the type system.** An arena is an owned allocator object; objects inside it are addressed by handles. See §4.6.

**P3. Regions and stable handles are the same feature twice.**
The arena-AST, B-tree, buffer-pool and registry workloads are all served by "owner-managed index/generation handles". Once handles exist, a region is just an arena that owns its objects. Two mechanisms double the checker, the IR invariants, and the agent-facing explanations for no gain in expressiveness. Collapse them.

**P4. "Whether any escaping safe references are allowed" cannot stay open.**
It is the load-bearing decision; every other ownership question depends on it. Answer it now: **no** in the core, with a single, bounded relaxation (§4.4, the elision rule for returned projections) and one reserved valve (§6).

**P5. Lexical vs. non-lexical access windows is only a question if references can be bound to locals.**
If temporary access exists only as (a) arguments at a call site, (b) expression-scoped projection results, and (c) names bound by explicit lexical blocks (`with`, `match` arms, `for` variables), then every access window is written in the source and there is nothing to infer. Non-lexical lifetimes were Rust's remedy for the ergonomics of reference-typed locals under conditional control flow. Remove the locals and the problem does not arise. **Recommendation: explicit lexical scopes only.**

**P6. No closures plus a thread pool, callbacks, and scoped threads is a real contradiction.**
§4 defers closures; §8 requires a thread pool and callbacks; §5 mentions "callbacks retaining references". The core must therefore provide plain function pointers (no captures) and owned context values, and `spawn`/scoped-thread primitives must take `(function, arguments)` rather than a closure. This is workable (§5, example 4) but must be a stated design, not an omission. Non-escaping closures fit the second-class model naturally and are the right *later* addition; escaping closures are the one that would need lifetimes.

**P7. "Verified lowering" is a research program, not a phase deliverable.**
Verified compilation (CompCert-class) is years of proof engineering. What is achievable and valuable: an IR with a *validator* that checks the ownership invariants after every pass (as rustc validates MIR and LLVM verifies IR), plus differential testing against the interpreter. Rename it *checked lowering* and specify the invariants (§7).

**P8. In-place construction is unaddressed and conflicts with "no silent deep copy".**
`Page { data: [4096]u8 }` built on the stack and moved into a heap slot is a 4 KiB memcpy. Rust still struggles with this. The language needs a convention for initializing uninitialized storage in place (Hylo's `set`, here `out`). Without it, every large object pays a hidden copy that `show-copies` will report but nobody can remove.

**P9. Panic/unwind semantics are undecided, and unwinding is a large hidden cost.**
Unwinding forces every destructor-bearing scope to carry landing pads and makes every unsafe block reason about exception safety. With errors as values (`Result`) there is no need for recoverable panics. **Recommendation: panic = trap (abort).** Optionally run destructors before terminating in hosted builds for resource cleanup, but never resume. This also makes destructor points static in the IR.

**P10. Async suspension and cancellation are listed as adversarial cases but are incompatible with the core.**
A suspension point is an escape: any `view`/`edit` live across it outlives its stack frame. Any future async design must be built from owned state machines with no temporary access live across `await`. State this as a constraint now so nothing in the core is designed around a future that cannot exist.

**P11. The 10% performance target has one intrinsic cost the spec does not isolate: handle indirection.**
User code that would use pointers in C uses `base + index` (plus an optional generation compare) here. For pointer-chasing workloads (B-tree descent, intrusive run queue, graph traversal) this is measurable. Standard-library containers can use audited unsafe raw pointers internally, exactly as Rust's `BTreeMap` does, so the target is achievable for *library* implementations. The benchmark methodology must report user-level handle code and library-level unsafe code separately, or the number will be meaningless.

**P12. The agent-benefit hypothesis is confounded by training data and cannot be rescued by methodology alone.**
Go and Rust have orders of magnitude more training data. Even with ablations, a new language will lose on first-attempt correctness. The defensible claim is narrower: *time-to-green under structured diagnostics* improves relative to the *same* language without them. Reframe the success metric to that within-language comparison, and treat cross-language comparisons as descriptive only.

Two smaller spec defects: the diagnostics example lacks the "violated rule" field §7 promises; and §4's pseudocode uses `view[u8]` for a loop that copies bytes, which is fine but the spec should state that `for b in data` over a view yields copies for `Copy` element types and projections otherwise.

---

## 3. Ownership models compared

Evaluated against the five structure classes §13 names. "Annotations" means user-written lifetime or region variables.

| Model | Annotations | B-tree split/merge | Intrusive list / parent ptrs | Arena AST | FFI callback | Concurrency | Runtime cost vs C | Semantic complexity | Verdict |
|---|---|---|---|---|---|---|---|---|---|
| **A. Rust: ownership + first-class borrows + lifetimes** | Yes (elided often) | Safe via `&mut` + `split_at_mut`-style APIs; std uses unsafe internally | Unsafe (or `Rc`/`RefCell`/`Weak`) | Indices or lifetimes on nodes | Unsafe + owned registration | `Send`/`Sync` traits, scoped threads | ~0 | High: lifetimes, variance, NLL, reborrows, drop check | Baseline for expressiveness; fails the "no lifetime complexity" goal |
| **B. Unique ownership + second-class access (Hylo-style conventions + projections)** | None | Safe via handles in a pool + `get2(a,b)` disjoint projection (runtime `a≠b` check) | Unsafe internals behind handle API, same as Rust | Arena + handles, natural fit | Unsafe + owned registration | Derived `Send`/`Share` markers, scoped spawn with scope-long borrows | ~0 for conventions; handle indirection in user code | Low: four conventions, one scoping rule, per-call disjointness check | **Recommended core** |
| **C. Regions as types (Cyclone / Tofte–Talpin)** | Region vars, or inference | Nodes in one region; split is easy; delete-one-node is impossible (region frees as a whole) | Pointers within a region are safe; cycles OK; no individual free | Excellent | Poor: callback state must outlive its region | Region-per-thread works; shared regions need extra typing | ~0, but memory bloat from region leaks | Medium–high; inference unpredictable | Rejected as core; arenas-as-library capture the benefit |
| **D. Linear types with explicit borrow regions (Austral)** | Region names in `borrow` blocks | Similar to B but with named regions | Unsafe | Arena + indices | Same as B | Linear = `Send` for free | ~0 | Medium: linear discipline is strict (every value consumed exactly once) and verbose | Informative; B is Austral without region names and with affine (droppable) values |
| **E. Reference capabilities (Pony)** | Capability per reference (6 kinds) | Possible but reasoning about `iso`→`trn`→`ref` transitions is hard | Natural (GC-backed) | Natural | Weak | Excellent (deny-capabilities give data-race freedom) | Needs GC | High: lattice of six capabilities | Rejected (GC); borrow its `iso`/`val` ideas for `Send`/`Share` |
| **F. Generational references everywhere (Vale)** | None | Natural | Natural | Natural | Natural | Needs separate story | Generation check on every deref; per-object generation word | Low | Rejected as core (runtime cost, every deref can fault); adopt as *library* handle type |

### Per-workload notes

- **B-tree.** The hard moment is splitting: two nodes mutated simultaneously. Under A it is `&mut` to two children obtained through an unsafe or index-based `get2`. Under B it is identical: a pool API `get2(a, b) -> (edit[Node], edit[Node])` that panics if `a == b` and is implemented with audited unsafe. Under C it is trivial but you can never free a single node. **B loses nothing relative to A here.**
- **Intrusive structures.** No safe model without GC expresses an intrusive doubly-linked list with raw interior pointers. Rust puts it in unsafe; so does Linux's Rust layer. B does the same. The safe interface is handle-based; the unsafe internals carry documented invariants. **No model wins; B ties A.**
- **Arena AST.** B and C are both natural. B's handles are plain `Copy` integers that can be stored anywhere, compared, serialized; C's region pointers cannot leave the region's scope. **B wins on simplicity.**
- **FFI callbacks.** The C side holds a `void*`. Safety comes from an owned registration object whose destructor unregisters. All models need unsafe here; only ownership of the registration matters. **Tie.**
- **Concurrency.** Unique ownership makes transfer trivial (move). Immutable sharing needs a `Share` marker (no interior mutability without synchronization). Scoped mutation needs a lexical block. B expresses all three with existing constructs (`own` move, `view` into scoped threads, `with m.lock() as e`). **B ties A** with a smaller surface.

### Expressiveness B gives up relative to A, honestly

1. Structs that hold references (e.g. `Parser<'a> { input: &'a [u8] }`). Rewrite: pass the view to each method; the struct holds offsets.
2. Borrowing iterators (`for x in &vec` over user containers). Rewrite: built-in iteration over arrays/slices/views; cursor-handle pattern for user containers (`c := t.first(); while c.valid() { use(t.at(c)); c = t.next(c) }`).
3. Reference-returning methods with multiple input borrows (`fn pick<'a>(&'a A, &'a B) -> &'a T`). Rewrite: the elision rule covers one source; multi-source returns are not expressible.
4. Holding a reference across unrelated mutation of a different object. Not lost: since root places are tracked syntactically, disjoint places are independent.

Items 1 and 2 are exactly what pushed Mojo to add *origins* and Swift to add `~Escapable` with lifetime dependence. That is the wall. §6 reserves one valve for it.

---

## 4. Recommended semantic core

Minimal, with each rule stated so a checker can implement it and an agent can be told which rule it broke.

### 4.1 Values and ownership
- Every value has exactly one owner: a variable, a field, an array element, or an arena slot.
- A type is **`Copy`** iff it has no destructor and no owning fields (derived automatically; may be opted out). `Copy` values are duplicated bitwise on use. Everything else is **move-only**.
- Deep copies are explicit (`copy(x)` or a `clone` method). There is no implicit deep copy anywhere.
- A move invalidates the source. **Moved-ness is static:** at every program point the checker knows whether each variable is live-and-owned or moved. A control-flow join where one path moved and another did not is an error (`OWN_CONDITIONAL_MOVE`) with a machine-applicable fix: insert `drop(x)` on the non-moving path or restructure. Consequence: no drop flags, every destructor call site is static in the IR. *[Decision ADR-3; relaxable later by adding drop flags if the fixtures show the rule is too annoying.]*
- **No partial moves.** Moving out of a field requires `take(s.f)` (leaves a default/none) or `replace(s.f, v)`. Move tracking is therefore per-variable, not per-path. *[ADR-3]*
- Definite initialization is flow-sensitive (as in Go/Java/Rust); reads of uninitialized variables are `INIT_UNINITIALIZED_READ`.

### 4.2 Parameter conventions (the only reference-like types)
Four conventions, written on the parameter, chosen by the callee:

| Convention | Meaning | Caller passes | Callee may |
|---|---|---|---|
| `view[T]` | read-only, non-escaping | any place of type `T`, or another view | read; pass down as view; return a projection of it (elision rule) |
| `edit[T]` | exclusive mutable, non-escaping | a mutable owned place, or another edit | read/write in place; pass down as view or edit; return a projection (elision) |
| `own T` | ownership transfer (sink) | an owned value (moved) or `copy(x)` | anything; must destroy or move it |
| `out T` | initialize uninitialized storage in place | an uninitialized place or a fresh slot | must initialize before returning on every path |

`view`/`edit` are **second-class**: they cannot be stored in a struct, array, union, or arena; cannot be returned except by the elision rule; cannot be captured; cannot be live across a thread spawn (except scoped spawn, §4.7) or any future suspension point. `out` exists to solve P8; it is what makes `Box.new(out p: Page)`-style in-place construction possible without a 4 KiB copy.

### 4.3 Where a named access can exist
Only three forms bind an access to a name, all lexically scoped:
1. `with name := <projection> { ... }` — the projection is live for the block.
2. `match <place or projection> { Variant(a, b) => ... }` — arm bindings are views/edits of the matched place for the arm.
3. `for x in <place or projection> { ... }` — `x` is a per-iteration view/edit of the element (or a copy if `Copy`).

There are **no reference-typed `:=` locals.** `p := vec.at(i)` is an error if `at` returns an access (`ACC_UNBOUND_PROJECTION`); write `with p := vec.at(i) { ... }` or use the projection directly in the expression.

### 4.4 Projections and the elision rule
- A **projection** is an expression that denotes a sub-place: field access `s.f`, indexing `a[i]`, slicing `a[i:j]`, or a call to a function whose return type is `view[U]`/`edit[U]`.
- A function may return `view[U]`/`edit[U]` only if it has **exactly one** `view`/`edit` parameter (or a `self` receiver with that convention); the result is derived from it. Multiple candidates: `ACC_AMBIGUOUS_SOURCE`. This is Rust's lifetime elision without lifetime variables and covers `get`, `at`, `slice`, `first`, `payload`.
- A returned projection is **expression-scoped**: it must be consumed as an argument, an assignment target, a `with`/`match`/`for` subject, or a field read, in the same statement.

### 4.5 The aliasing check
At each statement, collect the **root place** and **path** of every access it creates (arguments, projections, `with`/`match`/`for` subjects currently live in enclosing scopes). Two accesses conflict iff their paths overlap (one is a prefix of the other, or they index the same array with non-provably-distinct indices) and at least one is `edit`. Overlap with a live enclosing `with`/`match`/`for` access is also a conflict, as is any use of a root place that is currently moved.

That is the whole borrow checker: syntactic path disjointness plus lexical scopes. No dataflow beyond definite-init and moved-ness. Rejected programs are explainable in one sentence ("`v` is edited by argument 1 and viewed by argument 2"). Index disjointness is not proven; `get2(i, j)` style APIs with a runtime `i ≠ j` check are the sanctioned way to edit two elements at once, and `show-bounds-checks` reports the check.

**Performance consequence:** every `edit` parameter is provably unaliased for the call's duration and lowers to LLVM `noalias`; every `view` lowers to `readonly`. This is the same optimization Rust gets from `&mut`, and it is the main reason the 10% target is plausible for conventions-based code.

### 4.6 Arenas and handles (library, not type system)
- `Arena[T]`, `Pool[T]` (with free list and generation counters), and `Slab[T]` are standard-library owned containers, written with audited unsafe internals.
- A handle (`ArenaId[T]`, `PoolId[T]`) is a `Copy` integer-like value. It can be stored anywhere, including in other arena objects, which is how trees, graphs, parent pointers and cycles are expressed.
- Dereference is a projection: `arena.get(id) -> view[T]`, `arena.at(id) -> edit[T]`, `pool.get2(a, b) -> (edit[T], edit[T])`. A stale generation panics; memory safety holds regardless because the slot is always valid memory of the right type. The spec's distinction between logical validity (runtime) and memory safety (static) is preserved exactly.
- Destruction: the arena destroys its contents when it is destroyed; individual `pool.free(id)` runs the element destructor and bumps the generation.
- "Regions" in the spec become: an arena whose handle type is the only way to reach its contents. Nothing in the checker knows about arenas.

### 4.7 Concurrency
- **`Send`** (may be moved to another thread) and **`Share`** (a `view` of it may be read from multiple threads) are compiler-derived marker properties, structurally computed from fields. Raw pointers, non-atomic interior-mutable cells and thread-affine handles are neither; `unsafe assert Send for Foo` opts in with documented obligations. No user trait system is needed for this.
- **Transfer:** `thread.spawn(f, own args...)` requires each argument type to be `Send`.
- **Scoped sharing:** `with s := thread.scope() { s.spawn(f, view a, ...) }`. The compiler treats every access passed to `s.spawn` as live until the end of the `with s` block (the scope joins all threads). This is the one place a "lifetime relation" exists in the language, and it is hard-coded to the block. `view` arguments require `Share`; `edit` arguments require `Send` and are subject to the normal disjointness check against everything else live in the block.
- **Shared ownership:** `Shared[T]` (atomic refcount, immutable contents, requires `T: Share` to be `Send`). `Mutex[T]` provides `with g := m.lock() { ... g: edit[T] ... }`. Both are library types over unsafe internals.
- **Atomics:** explicit `Atomic[u32]` etc. with an ordering argument on every operation; the memory model is C11/C++20 happens-before. Volatile MMIO is a separate unsafe operation, never conflated with atomics.
- Data-race freedom then follows from: within a thread, `edit` is exclusive (§4.5); across threads, only `Share` data is viewed and only `Send` data is moved; mutation across threads only happens inside `Mutex`/atomics.

### 4.8 Unsafe
As in the spec: `unsafe` blocks authorize an enumerated set of operations (raw pointer deref and arithmetic, volatile, packed/unaligned, inline asm, allocator internals, `assert Send/Share`, transmute-style casts). Nothing else changes inside `unsafe`; conventions and moves are still checked. Each safe wrapper documents invariants, preconditions, and postconditions in a machine-readable block the `explain-ownership` command can surface.

### 4.9 Generics, interfaces, dispatch, functions
- Monomorphized generics with nominal interface constraints (`interface Ord { func less(view self, view other: Self) -> bool }`). Implementations are explicit (`impl Ord for u64`). No specialization, no associated-type machinery in Phase 1.
- `Copy`, `Send`, `Share` are built-in, derived, and usable as constraints.
- No dynamic dispatch in the core. When added, `dyn I` is an explicit owned box or an explicit view; never implicit.
- Plain function pointers (`func(view[u8]) -> u64`) are first-class `Copy` values. They cannot capture. Callback and thread-pool patterns use `(function pointer, own context)` pairs; see example 4.
- Closures: deferred. When added, non-escaping closures are second-class values under exactly the rules of §4.2 and need no new machinery; escaping closures are not planned.

### 4.10 Destruction and failure
- Destructors run at scope exit in reverse declaration order, exactly once, at statically known points (§4.1). A destructor takes `edit self` and cannot move `self` out.
- Errors are values (`Result[T, E]`, with `?`-style propagation). Panics trap. In hosted builds a trap may run pending destructors before `abort`; it never resumes. Destructor reentrancy is impossible because a panicking destructor traps.
- OOM: allocation returns `Result` from explicit allocator APIs; the convenience `Box.new` traps on failure. Freestanding mode has no default allocator.

---

## 5. Adversarial cases under the recommended core

| Case (§5) | Outcome | Mechanism |
|---|---|---|
| Intrusive linked list | Unsafe internals, safe handle API | Same as Rust; no safe model without GC does better |
| Self-reference, parent pointers, cycles | Safe via handles into an owning arena/pool | Handles are `Copy` data, not references |
| Interior pointer after vector growth | **Rejected statically** | `v.push(v.at(0))` conflicts: `edit v` vs `view v`; a `with e := v.at(0) { v.push(..) }` conflicts with the live block access |
| Callbacks retaining references | **Impossible to write** in safe code | Function pointers cannot capture; `view`/`edit` cannot be stored |
| FFI lifetime mismatch | Unsafe boundary; safe wrapper owns the registration and unregisters in its destructor | Ownership, not lifetimes |
| Partially initialized objects | `out` convention + definite-init | Each path must initialize; `INIT_*` codes |
| Destructor reentrancy | Impossible | Panics trap; destructors take `edit self` and cannot re-enter owner |
| Async suspension / cancellation | Out of scope; constraint recorded (P10) | No access may be live across a future suspension point |
| Concurrent region access | Arena is a value: `Send` if `T: Send`; shared only via `Mutex[Arena[T]]` or `view` under scoped threads if `T: Share` | Derived markers |
| B-tree split (two nodes at once) | `pool.get2(a, b)` with runtime `a ≠ b` | Disjointness by runtime check, reported by tooling |

---

## 6. The one reserved escape valve *[unproven]*

If Phase 0 shows that the cursor/handle rewrites of items 1–2 in §3 are unacceptable for the workloads (zero-copy parser, range iteration over B-tree/hash table), add **view-kinded aggregates**: a struct declared `view struct Cursor { data: view[u8]; pos: usize }` is itself second-class. It may be produced by the elision rule, bound only in `with`/`match`/`for`, passed as `view`/`edit`, and never stored or returned otherwise. Its root place is the root of the view it contains. This is the smallest generalization that gives borrowing iterators and reference-holding parsers, it needs no annotations, and it is precisely Swift's `~Escapable` with the single "downward-only" dependency the Swift steering group accepted first.

Do not add it preemptively. The exit criterion: at least two of the thirteen workloads require it for either correctness-by-construction or the 10% target. Otherwise the core stays as in §4.

Second-tier valve, only if the first proves insufficient: lifetime dependence on returns from multiple sources (Mojo origins / Swift `@lifetime`). Adopting it means conceding the thesis; record that as a possible Phase 0 outcome, not a failure.

---

## 7. Compiler and IR

- **Prototype in Go: yes.** The compiler's GC is irrelevant to generated code.
- **Phase 1 interpreter must execute the ownership-checked semantic IR, not the AST.** Otherwise the IR invariants are untested until Phase 2.
- **Backend:** emit textual LLVM IR and invoke `clang`/`llc`. This avoids cgo bindings, gets the C ABI and freestanding targets through clang's existing flags, and is trivially inspectable (`emit-ir` is the file). A C-emitting backend is a reasonable second option for odd targets; do both only when a target demands it.
- **Checked lowering (replaces "verified lowering").** The semantic IR carries, as explicit instructions or attributes: `move`, `drop` (static), `access_begin/access_end` for every convention and scoped access (Hylo's `project`/`end_project`), `check_bounds`, `check_generation`, `unsafe_begin/unsafe_end`, and a provenance tag on every check. An IR validator asserts, after every pass:
  1. Every `drop` dominates no use of the same place and is reached exactly once per owner on every path (no drop flags exist to be wrong).
  2. Every `access_begin` has a matching `access_end` in the same function, and no two overlapping `edit` accesses are simultaneously open.
  3. Every `check_*` either remains or is replaced by a recorded proof fact (`proven_by: loop_bound | dominated_by_check | constant`), which is what `show-bounds-checks` reports as `proven` vs `runtime-checked`.
  4. No instruction outside an `unsafe` region performs an unsafe operation.
  5. Second-class values never flow into a store, return (except elision), or call argument marked escaping.
- **Diagnostics:** keep the stable-code JSON design; add the `rule` field the spec promises; every `ACC_*`, `OWN_*`, `INIT_*`, `SEND_*` code maps to a rule number in §4 of the language reference.

---

## 8. Example programs in the recommended core

Surface syntax is illustrative and follows the spec's Go-flavored direction.

**1. B-tree split (handles, two simultaneous edits).**
```text
struct Node { keys: [MAX]u64; count: u32; children: [MAX+1]NodeId; leaf: bool }
struct BTree { nodes: Pool[Node]; root: NodeId }

func split_child(t: edit[BTree], parent: NodeId, i: u32) {
    right := t.nodes.alloc(Node.empty())           // NodeId, Copy
    left  := t.nodes.get(parent).children[i]       // projection consumed in-statement
    with l, r := t.nodes.get2(left, right) {       // runtime check: left != right
        r.count = MIN
        copy_range(out r.keys, l.keys[MIN+1 : MAX])
        l.count = MIN
    }                                               // accesses end here
    with p := t.nodes.at(parent) {                  // legal: previous block is closed
        insert_child(p, i + 1, right, t.nodes.get(left).keys[MIN])  // ERROR: ACC_CONFLICT
    }
}
```
The last line is a deliberate negative case: `p` is a live `edit` into `t.nodes`, and `t.nodes.get(left)` is a `view` of the same root. The fix is to read the median key before opening the block, which is also the correct code.

**2. Arena AST.**
```text
union Expr { Num(i64); Add(lhs: ExprId, rhs: ExprId) }
struct Ast { nodes: Arena[Expr] }

func eval(a: view[Ast], id: ExprId) -> i64 {
    match a.nodes.get(id) {
        Num(n)      => return n
        Add(l, r)   => return eval(a, l) + eval(a, r)   // l, r are Copy handles
    }
}
```

**3. Zero-copy packet parser (elision rule, no reference-holding struct).**
```text
struct Header { src: u16; dst: u16; len: u16 }
func parse_header(pkt: view[u8]) -> Result[Header, ParseErr] { ... }
func payload(pkt: view[u8], h: Header) -> view[u8] { return pkt[8 : 8 + usize(h.len)] }

func handle(pkt: view[u8]) -> Result[(), ParseErr] {
    h := parse_header(pkt)?
    with body := payload(pkt, h) { deliver(h.dst, body) }
    return Ok(())
}
```

**4. Thread handoff and scoped parallel sum (no closures).**
```text
struct Job { f: func(own Box[Task]) ; ctx: own Box[Task] }       // Box[Task]: Send
func worker(q: own Shared[Mutex[Queue[Job]]]) { ... with g := q.lock() { ... } }

func par_sum(data: view[u64]) -> u64 {
    results := [4]u64{}
    with s := thread.scope() {
        for i in 0..4 {
            s.spawn(sum_part, data[i*n : (i+1)*n], out results[i])   // views live until scope end
        }
    }
    return results[0] + results[1] + results[2] + results[3]
}
```
`out results[i]` with a loop index is a conflict unless the checker can see distinct constant indices; the sanctioned form is `s.spawn(...) -> Join[u64]` collected at scope end. Either way the rule is visible, not inferred.

**Negative fixtures (minimum set for Phase 0).**
`OWN_USE_AFTER_MOVE`, `OWN_CONDITIONAL_MOVE`, `OWN_PARTIAL_MOVE`, `INIT_UNINITIALIZED_READ`, `INIT_OUT_NOT_SET`, `ACC_CONFLICT` (edit/edit, edit/view, enclosing-scope), `ACC_UNBOUND_PROJECTION`, `ACC_ESCAPE_STORE`, `ACC_ESCAPE_RETURN`, `ACC_AMBIGUOUS_SOURCE`, `ACC_LIVE_ACROSS_SPAWN`, `SEND_NOT_SEND`, `SHARE_NOT_SHARE`, `UNSAFE_OP_OUTSIDE_UNSAFE`, `DROP_IN_DESTRUCTOR_MOVE_SELF`. Each has a positive twin that compiles.

---

## 9. Concrete changes to the spec

1. §1: replace "without Rust's general lifetime-annotation and borrowing complexity" with "without first-class references; temporary access is second-class (non-escaping)". Cite Osvald et al. and Hylo.
2. §5 Temporary access: resolve "initially prohibit returning" to the elision rule; delete "inferred non-lexical access windows" and state explicit lexical scoping via `with`/`match`/`for`.
3. §5 Regions: delete as a type-system feature; move to §6 as `Arena`/`Pool` library types; fold "Stable handles" into the same subsection.
4. §5: add the `out` convention and the no-partial-move / static-move rules.
5. §4 and §8: state that function pointers are first-class and capture-free; define spawn and scoped spawn signatures; remove "callbacks retaining references" from adversarial cases (it is unexpressible) and add "two simultaneous edits into one pool".
6. §6: panic = trap; no unwinding; document the hosted-cleanup option.
7. §7: "checked lowering" with the five validator invariants; add `rule` to the diagnostic schema.
8. §10: answer "escaping safe references" = no, with §6 valve; answer "regions vs owned containers" = handles; mark the remaining items as ADRs in the order below.
9. §9/§2: replace the cross-language agent metric with the within-language ablation as the primary success criterion.
10. §14: add Hylo (mutable value semantics; Racordon et al., "Implementation Strategies for Mutable Value Semantics"), Osvald et al. 2016/2017, Swift SE-0446 (`~Escapable`) and the lifetime-dependence pitches, Mojo origins and interior origins (v1.0 release notes) as the primary reference directions; Vale and Cyclone become secondary.

---

## 10. Phase 0 plan and ADR order

**Phase 0 deliverables (go/no-go):**
1. Language-reference draft for §4 of this document only (conventions, scoping, aliasing rule, moves, destruction).
2. A checker prototype in Go over a tiny AST: definite-init, static moves, path-disjointness, scope tracking, elision rule. No codegen. Target: under 3 kLOC.
3. The 15 negative fixtures and their positive twins above, plus the 20 adversarial fixtures the spec calls for.
4. Hand-written versions of vector, B-tree split/merge, arena AST, zero-copy parser, and thread handoff in the surface syntax, type-checked by the prototype.
5. For each of the thirteen workloads, a one-page note: expressible in the core as-is / needs the §6 valve / needs unsafe internals. This note *is* the go/no-go.

**Exit criteria:** every §8 workload is expressible with at most audited-unsafe internals in library code; at most two require the §6 valve; every rejected adversarial fixture produces a single-sentence explanation referencing one rule.

**ADRs, in dependency order:**
1. Second-class access vs first-class references (decides everything else).
2. Regions as library arenas + handles vs regions in the type system.
3. Static moves, no partial moves, no drop flags.
4. Elision rule for returned projections; no multi-source returns.
5. `out` convention and in-place construction.
6. Panic = trap; no unwinding.
7. Derived `Send`/`Share`; scoped spawn as the only cross-thread borrow.
8. Bounds and generation checks on by default; provenance of eliminated checks.
9. Monomorphized generics with nominal interfaces; no dyn in core.
10. Function pointers now, non-escaping closures later, no escaping closures.

---

## 11. References

- Osvald, Essertel, Wu, González-Alayón, Rompf. *Gentrification Gone too Far? Affordable 2nd-Class Values for Fun and (Co-)Effect.* OOPSLA 2016. https://www.cs.purdue.edu/homes/rompf/papers/osvald-oopsla16.pdf — second-class values with stack-bounded lifetimes; Coq soundness proof. Follow-up: *Rust-Like Borrowing with 2nd-Class Values*, Scala 2017.
- Racordon, Shirako, Abrahams, et al. *Implementation Strategies for Mutable Value Semantics* (Val/Hylo). https://arxiv.org/pdf/2106.12678 ; Hylo language tour: https://docs.hylo-lang.org/language-tour/functions-and-methods , https://docs.hylo-lang.org/language-tour/bindings — `let`/`inout`/`sink`/`set` conventions, projections, exclusive access without lifetime annotations.
- Swift Evolution SE-0446 *Nonescapable Types*: https://forums.swift.org/t/se-0446-nonescapable-types/74666 ; acceptance note: https://forums.swift.org/t/accepted-with-modifications-se-0446-nonescapable-types/75504 ; lifetime-dependence pitches: https://forums.swift.org/t/pitch-3-compile-time-lifetime-dependency-annotations/84968 , https://forums.swift.org/t/experimental-support-for-lifetime-dependencies-in-swift-6-2-and-beyond/78638 — evidence that non-escapable values need a downward-only dependency first, then more.
- Mojo origins (formerly lifetimes): https://docs.modular.com/mojo/manual/values/lifetimes ; Mojo 24.6 rename rationale: https://modular.com/blog/hands-on-with-mojo-24-6 ; Mojo 1.0 interior origins for container element references: https://mojolang.org/releases/v1.0.0/ — evidence that a conventions-only start grew lifetimes to support references into containers.
- Cyclone (regions), Tofte & Talpin (region inference), Austral (linear types with borrow regions), Pony (reference capabilities), Vale (generational references): cite primary sources in the research report; this evaluation uses them only for the comparison matrix.
