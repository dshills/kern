# Adversarial cases (spec §5.8) — classification notes

One section per §5.8 case. Each names its fixture under `tests/adversarial/`,
its classification (safe / unsafe internals behind a safe API / rejected by
design), and, where a program is rejected, the spec rule and the diagnostic
code from `compiler/diag/codes.go` with its rule number. Fixtures are Phase 0
static fixtures (`check` only); accepting fixtures carry `//! phase: 1` (or
`2`/`3` for FFI and concurrency), reject fixtures carry no phase line, and
every reject fixture has an accepting twin. Sketches omit boilerplate.

## 1. Intrusive linked lists — `intrusive_list.kern`, twin `intrusive_list_reject.kern`
**Scenario.** A doubly linked list whose link fields live inside the node, nodes
owned elsewhere, O(1) unlink by identity.
**Classification.** Unsafe internals behind a safe API (§5.8: "unsafe
internals, handle API"). Link fields are `RawPtr[Node]`; dereference is an
enumerated unsafe operation (§6); the safe API takes `PoolId[Node]`.
**Rejected twin.** Dereferencing the raw link outside `unsafe` is
`UNSAFE_OP_OUTSIDE_UNSAFE` (rule 6.4); the same function with `unsafe` but no
`contract` block is `UNSAFE_MISSING_CONTRACT` (rule 6.4). The fixture uses the
first.
```text
//! phase: 2
//! expect: accept
//! twin: intrusive_list_reject
struct Node { next: RawPtr[Node]; prev: RawPtr[Node]; v: u64 }
struct List { head: RawPtr[Node]; tail: RawPtr[Node]; nodes: Pool[Node] }
contract { invariant "every node reachable from head is a live slot of nodes, linked once"; }
func push_back(l: edit[List], id: PoolId[Node]) {
    p := l.nodes.raw_slot(id)                 // validates id, yields RawPtr[Node]
    unsafe {
        with n := p.deref() { n.prev = l.tail; n.next = RawPtr.null() }
        if !l.tail.is_null() { with t := l.tail.deref() { t.next = p } } else { l.head = p }
        l.tail = p
    }
}
```
Reject twin: the `with n := p.deref()` line outside any `unsafe` block.

## 2. Self-reference, parent pointers, cycles — `handles_cycles.kern`
**Scenario.** A tree whose nodes carry a parent link, and a graph with a cycle.
**Classification.** Safe. Handles are `Copy` integer-like values storable
anywhere, including inside objects of the same container (§5.6). A cycle of
handles is memory-safe; a stale handle traps `generation`.
```text
//! phase: 1
//! expect: accept
struct N { parent: PoolId[N]; first: PoolId[N]; next: PoolId[N]; v: u64 }
func link_child(p: edit[Pool[N]], parent: PoolId[N], child: PoolId[N]) {
    old_first := p.get(parent).first                       // Copy read, access ends
    with c := p.at(child) { c.parent = parent; c.next = old_first }
    with q := p.at(parent) { q.first = child }             // sequential blocks, no conflict
}
func self_loop(p: edit[Pool[N]], id: PoolId[N]) { with n := p.at(id) { n.next = id } }
```

## 3. Interior pointers after container growth — `interior_pointer_growth.kern` (reject), twin `interior_pointer_growth_ok.kern`
**Scenario.** Hold a reference to `v[0]` and push onto `v`.
**Classification.** Rejected by design. The `with` binding `e` is a live
`edit` (or `view`) rooted at `v`; `v.push(..)` is an `edit` of the same root
whose path (`v`) is a prefix of `v[0]` (§5.4 conflict rule). Diagnostic:
`ACC_CONFLICT`, rule 5.4.4. The one-statement form `v.push(v.get(0))` is the
same conflict: the argument `view` is live when the `edit self` begins.
```text
//! expect: reject ACC_CONFLICT rule=5.4.4
//! twin: interior_pointer_growth_ok
func grow_while_held(v: edit[Vec[u64]]) {
    with e := v.at(0) {
        v.push(e)                 // ACC_CONFLICT: edit of v while e (rooted at v) is live
    }
}
```
Twin: read `x := v.get(0)` (a `Copy` read whose access ends immediately), close
nothing, then `v.push(x)`.

## 4. Two simultaneous edits into one pool — `pool_get2.kern`, twin `pool_get2_reject.kern`
**Scenario.** B-tree split: edit two sibling nodes at once.
**Classification.** Safe at the call site; `get2` is implemented in `std`
under the multi-access rule (§5.3: unsafe-implemented, documented pairwise
disjointness, runtime trap `disjoint` when `a == b`). The two bound names are
distinct roots and never conflict with each other (§5.4 multi-access
bindings); the pool is frozen for the block.
**Rejected twin.** Opening `at(a)` and `at(b)` in nested `with` blocks is
`ACC_CONFLICT` (rule 5.4.4): both are `edit` accesses rooted at the pool with
non-constant indices, so their paths overlap.
```text
//! phase: 1
//! expect: accept
//! twin: pool_get2_reject
func move_half(p: edit[Pool[Node]], a: PoolId[Node], b: PoolId[Node]) {
    with l, r := p.get2(a, b) {                       // traps disjoint if a == b
        for k in 0..7 { r.keys[k] = l.keys[8 + k] }
        l.count = 7u32
        r.count = 7u32
    }
}
```
Reject twin: `with l := p.at(a) { with r := p.at(b) { ... } }`.

## 5. FFI lifetime mismatch — `ffi_registration.kern`, twin `ffi_registration_reject.kern`
**Scenario.** C keeps a `void*` to our state after `register(cb, ctx)`; we must
not free the state before `unregister`.
**Classification.** Unsafe internals behind a safe API (§5.8: "owned
registration object whose destructor unregisters"). The imported call is
unsafe (§7); the wrapper owns the context `Box` and its destructor calls
`unregister` first, so the C pointer is never dangling while registered.
Ownership, not lifetimes, carries the guarantee.
**Rejected twin.** Declaring the import with an `own` parameter is
`FFI_UNSUPPORTED_TYPE` (rule 7.2); calling the import outside `unsafe` is
`UNSAFE_OP_OUTSIDE_UNSAFE` (rule 6.4). The twin uses the first.
```text
//! phase: 2
//! expect: accept
//! twin: ffi_registration_reject
extern "C" func c_register(cb: func(RawPtr[u8]), ctx: RawPtr[u8]) -> i32
extern "C" func c_unregister(ctx: RawPtr[u8])
struct Registration { ctx: Box[State] }
impl Registration {
    contract { invariant "ctx stays registered exactly while this value lives"; }
    func new(own s: Box[State]) -> Result[Registration, FfiErr] {
        unsafe { if c_register(on_event, s.raw()) != 0i32 { return Result.Err(FfiErr.Busy) } }
        return Result.Ok(Registration{ ctx: s })
    }
    destructor(edit self) { unsafe { c_unregister(self.ctx.raw()) } }   // runs once, before ctx is freed
}
```
Reject twin: `extern "C" func c_register(cb: func(RawPtr[u8]), ctx: own Box[State]) -> i32`.

## 6. Partially initialized objects — `partial_init.kern`, twin `partial_init_reject.kern`
**Scenario.** Build a struct field by field, possibly failing halfway.
**Classification.** Safe, by the `out` convention plus definite initialization
(§5.1, §5.2). Per-field initialization exists only inside a callee for its
`out` storage; on an error path the compiler destroys the initialized fields
in reverse order and the caller sees the storage as uninitialized.
**Rejected twin.** A normal-return path that leaves a field unset is
`INIT_OUT_NOT_SET` (rule 5.2.1). Assigning a field of an ordinary
uninitialized local is `INIT_FIELD_OF_UNINITIALIZED` (rule 5.1.8). The twin
uses the first.
```text
//! phase: 1
//! expect: accept
//! twin: partial_init_reject
struct Conn { sock: Socket; buf: Vec[u8]; id: u64 }
func open(c: out Conn, addr: u32) -> Result[(), NetErr] {
    c.sock = Socket.connect(addr)?            // nothing initialized yet on Err
    c.buf = Vec[u8].new(SystemAllocator{})
    c.id = next_id()?                          // on Err: buf then sock destroyed, c uninitialized
    return Result.Ok(())
}
func use() -> Result[(), NetErr] {
    var c: Conn
    open(out c, 1u32)?                         // c initialized after ?
    return send(c)
}
```
Reject twin: `open` returns `Ok(())` on a path where `c.id` was never assigned.

## 7. Destructor reentrancy — `destructor_reentrancy.kern`, twin `destructor_reentrancy_reject.kern`
**Scenario.** A destructor that tries to reach its own value again (re-enter,
move `self` out, or destroy it twice).
**Classification.** Rejected by design / impossible (§5.7 non-reentrancy): a
value has one owner, destruction consumes that owner, and inside the
destructor `self` is reachable only through the `edit self` binding. Nothing
can observe the value while its destructor runs, and a trap inside a
destructor terminates (§6).
**Rejected twin.** Moving `self` or a field out inside the destructor is
`OWN_PARTIAL_MOVE` (rule 5.1.6); calling `drop(self)` is the same code
(`consume` through a binding).
```text
//! phase: 1
//! expect: accept
//! twin: destructor_reentrancy_reject
struct Guard { log: Vec[u8]; depth: u32 }
impl Guard {
    destructor(edit self) {
        self.depth = self.depth + 1u32              // edit through the binding: fine
        flush(self.log)                             // view of a field through self: fine
        replace(self.log, Vec[u8].new(SystemAllocator{}))   // edit access, not a move
    }
}
```
Reject twin: `destructor(edit self) { stash(self.log) }` where `stash` takes `own Vec[u8]`.

## 8. Concurrent container access — `concurrent_container.kern`, twin `concurrent_container_reject.kern`
**Scenario.** Several threads read one arena; one thread writes it.
**Classification.** Safe, by derived `Send`/`Share` and `Mutex` (§5.5). An
`Arena[T]` is `Share` iff `T: Share`, so scoped threads may `view` it; mutation
is only through `Mutex[Arena[T]].lock()` or an exclusive scoped `edit`.
**Rejected twin.** Passing the arena as `view` to a scoped spawn when `T`
holds a `RawPtr` is `SHARE_NOT_SHARE` (rule 5.5.3); passing it as `edit` to two
spawns in one scope is `ACC_CONFLICT` (rule 5.4.4); passing a view rooted
inside the block is `ACC_LIVE_ACROSS_SPAWN` (rule 5.5.3). The twin uses
`SHARE_NOT_SHARE`.
```text
//! phase: 3
//! expect: accept
//! twin: concurrent_container_reject
func sum_part(a: view[Arena[u64]], lo: usize, hi: usize) -> u64 { ... }
func par_sum(a: view[Arena[u64]]) -> Result[u64, SpawnErr] {
    with s := thread.scope() {
        j0 := s.spawn(sum_part, view a, 0, 500)?        // a: Share, rooted outside the block
        j1 := s.spawn(sum_part, view a, 500, 1000)?
        return Result.Ok(j0.get() + j1.get())
    }
}
```
Reject twin: the same with `Arena[Cell]` where `struct Cell { p: RawPtr[u8] }`.

## 9. Large-object construction without copies — `large_object_out.kern`, twin `large_object_out_reject.kern`
**Scenario.** Construct a 4 KiB `Page` directly in a heap slot or pool slot.
**Classification.** Safe, by `out` (§5.2): `Box.new_with(init: func(out Page) -> Result[(), E])`
and `Pool.alloc_with` construct in the destination with no intermediate value;
`show-copies` must report zero copies of `Page`.
**Rejected twin.** Storing the `Result` of a fallible `out` call instead of
consuming it with `?` or `match` in the same statement is
`INIT_OUT_RESULT_UNHANDLED` (rule 5.2.3).
```text
//! phase: 1
//! expect: accept
//! twin: large_object_out_reject
struct Page { id: u64; data: [4096]u8 }
func init_page(p: out Page, id: u64) -> Result[(), IoErr] { p.id = id; read_disk(id, out p.data)?; return Result.Ok(()) }
func load(pool: edit[Pool[Page]], id: u64) -> Result[PoolId[Page], LoadErr] {
    return pool.alloc_with(init_page, id)          // slot initialized in place; no Page value exists
}
```
Reject twin: `r := init_page(out p, 7u64)` followed by a use of `r`.

## Closing note: callbacks and async
**Callbacks retaining references** is not an adversarial case because it is
not expressible: function values are plain pointers that cannot capture (§4),
and `view`/`edit` cannot be stored in any aggregate (§5.2). Trying to store a
callback context that holds a view is `ACC_ESCAPE_STORE` (rule 5.2.2); fixture
`callback_retains_view.kern` (reject, twin `callback_owned_ctx.kern`) shows
`struct Cb { f: func(view[]u8); data: view[]u8 }` rejected, and the twin shows
the sanctioned `(function pointer, own Box[Ctx])` pair (thread_pool.md).

**Async suspension and cancellation** are out of scope (§2.2). The recorded
constraint (§5.8, evaluation P10): no `view`/`edit` may be live across a
suspension point in any future async design, which must be built from owned
state machines. The scoped-spawn rule (`ACC_LIVE_ACROSS_SPAWN`) is the
template: an access crossing a scheduling boundary is allowed only when the
boundary is a lexical block that joins before the access's root can change.
