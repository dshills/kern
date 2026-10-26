# Workload: intrusive_run_queue

## Summary
A scheduler run queue whose link fields live inside the task object: O(1)
enqueue, dequeue, and unlink of an arbitrary task by identity, with no per-link
allocation and no lookup. Tasks are owned by a task table; the queue borrows
their addresses. This is the §5.8 case "intrusive linked lists".

## Classification
requires audited unsafe internals behind a safe API

## Data structures and ownership graph
```text
struct Task  { id: TaskId; prio: u8; state: u8; next: RawPtr[Task]; prev: RawPtr[Task]; ctx: Context }
struct Table { tasks: Pool[Task] }                       // owns every Task; slots never move
struct RunQueue { head: RawPtr[Task]; tail: RawPtr[Task]; len: usize }
unsafe assert Send for RunQueue                          // obligation: linked tasks are in one Table
```
- `Table.tasks` owns the tasks. `RunQueue` owns nothing: `head`, `tail`,
  `next`, `prev` are raw addresses of pool slots. `RawPtr` is `Copy`, neither
  `Send` nor `Share` (§5.5), so `Task` and `RunQueue` need `unsafe assert` to
  cross threads.
- Safe callers only ever see `TaskId` (`PoolId[Task]`); the raw pointers are
  module-private and read or written only inside `unsafe` in `runq.kern`.
- Invariant (contract): every task reachable from `head` is a live slot of the
  one `Table` passed to every operation, is linked at most once, `prev`/`next`
  are mutually consistent, and `Table.free` is never called on a linked task
  (the safe `retire` unlinks first).

## Access patterns
| Operation | Convention | Notes |
|---|---|---|
| `enqueue(q: edit[RunQueue], t: edit[Table], id: TaskId)` | `edit`, `edit` | `t.tasks.raw_slot(id)` validates the handle (bounds, identity, vacant, generation) then yields the address |
| `dequeue(q: edit[RunQueue], t: edit[Table]) -> Option[TaskId]` | `edit`, `edit` | reads `head`, relinks, returns the `Copy` id |
| `unlink(q: edit[RunQueue], t: edit[Table], id: TaskId)` | `edit`, `edit` | O(1): no traversal |
| `RawPtr[Task].deref() -> edit[Task]` | unsafe projection, fresh root | obligation: no other access to that slot is live |
Both `edit`s are required: holding `edit[Table]` for the call guarantees (§5.4)
that no safe access into the pool is live while raw writes happen, which is
what makes the contract dischargeable. No `get2`; no release accessor.

## Why this classification
A link field must name another task that the queue does not own. The core
offers two ways to name a value: a second-class access, which cannot be
stored in a field (`ACC_ESCAPE_STORE`, §5.2), or a container handle (§5.6).
Handles work and are safe: `next: TaskId; prev: TaskId` in a `Pool[Task]` is
the core version, and it is O(1) too. But it is not intrusive in the sense the
benchmark measures: every hop performs a bounds check plus a generation
compare (and an identity check in debug), which is exactly the P11
handle-indirection cost this workload exists to quantify against C. The
intrusive form therefore stores `RawPtr[Task]`, whose dereference and
arithmetic are enumerated unsafe operations (§6), and §5.8 classifies it as
"unsafe internals, handle API". Both forms are built; the handle form is the
row (a) user-level comparison and the raw form is row (b).

## Sketch
```text
module runq_bench

contract {
    invariant "every Task reachable from q.head is a live slot of t.tasks, linked exactly once, with prev/next consistent";
    requires  "id is live in t.tasks and not currently linked";
    ensures   "id is the tail of q and q.len grew by one";
}
func enqueue(q: edit[RunQueue], t: edit[Table], id: TaskId) {
    p := t.tasks.raw_slot(id)                       // safe: validates id (traps vacant/generation), returns RawPtr[Task]
    unsafe {
        with n := p.deref() {                       // unsafe projection with a fresh root
            n.prev = q.tail
            n.next = RawPtr.null()
        }
        if q.tail.is_null() {
            q.head = p
        } else {
            with old := q.tail.deref() { old.next = p }
        }
        q.tail = p
    }
    q.len = q.len + 1
}

contract { invariant "as enqueue"; requires "id is linked in q"; ensures "id is unlinked and q.len shrank by one"; }
func unlink(q: edit[RunQueue], t: edit[Table], id: TaskId) {
    p := t.tasks.raw_slot(id)
    unsafe {
        var prev: RawPtr[Task]; var next: RawPtr[Task]
        with n := p.deref() { prev = n.prev; next = n.next; n.prev = RawPtr.null(); n.next = RawPtr.null() }
        if prev.is_null() { q.head = next } else { with pn := prev.deref() { pn.next = next } }
        if next.is_null() { q.tail = prev } else { with nx := next.deref() { nx.prev = prev } }
    }
    q.len = q.len - 1
}
```
The safe surface is `enqueue`, `dequeue`, `unlink`, `peek_head`; the unsafe
lines are confined to those four functions and counted in the report.

## Benchmark notes
- C: the Linux-style `struct list_head` embedded in `struct task`, with
  `container_of`; this is the lower bound.
- Rust: `intrusive_collections::LinkedList` with `UnsafeRef` (the standard
  intrusive crate), and a `slab` + index variant for the handle row.
- Measure: enqueue/dequeue pairs per second over 1e6 tasks, `unlink` of a
  random linked task (the operation where handles pay a lookup and intrusive
  links do not), p50/p99, peak RSS (task size per `show-layout` must equal the
  C struct: two pointers of link overhead), allocation count (pool chunks),
  copies (zero), unsafe lines (expected about 30 in `runq.kern`), the handle
  row's generation checks per hop, text bytes, compile time.
- Failure paths: `enqueue` of a freed id traps `generation` in `raw_slot`
  before any unsafe code runs; a deliberate contract violation fixture
  (`Table.free` on a linked task) is run under ASan in Phase 3 to show the
  audit catches it.

## Open risks
- `RawPtr[T].deref() -> edit[T]` has no source parameter for the elision rule;
  the checker must give it a fresh root inside `unsafe` and PLAN 2.3 must say
  so. Without it, raw field writes need `load`/`store` of whole values, which
  a move-only `Task` cannot do. A Phase 2 design item, not a Phase 0 blocker.
- `Pool` must guarantee stable slot addresses (chunked storage, never a
  reallocating vector) or the invariant is false; the Phase 1 library
  reference must state it. The `unsafe assert Send for RunQueue` obligation is
  that queue and table move together (`Mutex[(RunQueue, Table)]` enforces it).
