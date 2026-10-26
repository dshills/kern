# Workload: buffer_pool

## Summary
A database buffer pool: a fixed set of 4 KiB page frames, a page table from
`PageId` to frame, pin/unpin with a per-frame pin count, clock eviction of
unpinned frames, and dirty write-back through a `Storage`. Many threads pin
different pages concurrently through a `Shared[BufferPool]`. The pin accessor
is the release accessor named in §5.3.

## Classification
requires audited unsafe internals behind a safe API

## Data structures and ownership graph
```text
struct Frame { page: Page; pins: Atomic[u32]; dirty: Atomic[bool]; id: Atomic[u64] }
struct Page { data: [4096]u8 }
struct BufferPool { frames: Vec[Frame]; table: Mutex[PageTable]; clock: Atomic[usize]; store: Mutex[File] }
unsafe assert Interior for Frame        // page bytes change through a view, guarded by pins
```
- `BufferPool` owns the frames, the page table, and the storage; each `Frame`
  owns its `Page` inline (no per-page heap allocation).
- Threads hold `Shared[BufferPool]` (`Share` because every field is `Interior`
  with its own synchronization or a `Mutex`); nobody holds an `edit` of the
  pool. A pinned page is reached through `with p := pool.frame_of(id)?.pin() { ... }`,
  which is a `view`-rooted access escalated to `edit[Page]` at runtime.
- Eviction writes a new page into a frame whose pin count is zero, under the
  page-table lock; that write goes through a `view` and is the interior
  write that `unsafe assert Interior` authorizes.

## Access patterns
| Operation | Convention | Notes |
|---|---|---|
| `frame_of(view self, id: PageId) -> Result[view[Frame], IoErr]`: not expressible (access inside `Result` is `ACC_ESCAPE_STORE`); instead `fetch(view self, id) -> Result[usize, IoErr]` then `frame(view self, f) -> view[Frame]` | elision | two statements |
| `Frame.pin(view self) -> edit[Page] release unpin` | runtime-exclusive escalation, §5.3 | `unsafe`, contract required |
| `Frame.unpin(view self)` | release function | receives the same receiver, runs at access end |
| `flush(view self, f: usize) -> Result[(), IoErr]` | `view` + `Mutex` | reads the page under a pin, writes through `store.lock()` |
| `evict(view self) -> Option[usize]` | `view` under `table.lock()` | interior write of `Frame.page`, `unsafe` |
The release accessor is required here: the pin must be dropped on every exit
of the block, including `?` and `return`, and §5.3 guarantees exactly that.

## Why this classification
`pin` returns `edit[Page]` from a `view self` receiver. In safe code that is
`ACC_PERMISSION_ESCALATION` (§5.3 permission rule); §5.3 "runtime-exclusive
escalation" permits it only inside `unsafe`, with a documented runtime
mechanism (here the pin count plus the rule that an exclusive pin is one whose
count went 0 to 1 in `pin_exclusive`, or a shared pin that yields `view[Page]`).
In addition, replacing a frame's page during eviction is a write through a
`view` of an `Interior` type, an enumerated unsafe operation (§6). Both live in
`bench/kern/buffer_pool/pool.kern` behind `pin`/`unpin`/`fetch`, each carrying a
`contract` block (`UNSAFE_MISSING_CONTRACT` otherwise). Every caller is safe
core code. The §5.6 extension is not required: the pinned page is a `with`
binding, never a stored value.

## Sketch
```text
module buffer_pool_bench

impl Frame {
    contract {
        invariant "pins > 0 implies the frame is not an eviction candidate";
        requires  "the caller holds no other pin on this frame (recursive pin traps explicit)";
        ensures   "the returned edit is the only live access to page until unpin runs";
    }
    func pin(view self) -> edit[Page] release unpin {
        unsafe {
            if self.pins.fetch_add(1u32, Ordering.acq_rel) != 0u32 { trap() }   // exclusive pin only
            return self.page_edit()            // enumerated: runtime-exclusive escalation (§5.3)
        }
    }
    func unpin(view self) {
        self.pins.fetch_sub(1u32, Ordering.release)   // runs once, at the end of the pinned access
    }
}

func update_row(pool: view[BufferPool], id: PageId, off: usize, v: u64) -> Result[(), IoErr] {
    f := pool.fetch(id)?                        // frame index; loads from store if absent
    with p := pool.frame(f).pin() {             // edit[Page] rooted at pool, release at '}'
        put_u64(p.data[off : off + 8], v)       // bounds-checked sub-slice
        if v == 0u64 { return Result.Ok(()) }   // early return: unpin still runs
        pool.frame(f).dirty.store(true, Ordering.release)   // ERROR: ACC_CONFLICT, pool frozen
    }
    pool.frame(f).dirty.store(true, Ordering.release)       // correct placement: after unpin
    return Result.Ok(())
}
```
The marked line is the deliberate negative: while `p` is live, `pool` is
frozen (§5.3 escalation roots the `edit` at the receiver's root), so even the
atomic store through a `view` conflicts. The fix moves it after the block.

## Benchmark notes
- C: the same frame array with `atomic_uint` pins and a mutex-protected hash
  page table; eviction by clock hand; `pread`/`pwrite` for I/O.
- Rust: a hand-written pool with `AtomicU32` pins, `parking_lot::Mutex` page
  table, and an `UnsafeCell<Page>` behind a `PinGuard` whose `Drop` unpins (the
  direct analogue of `release unpin`).
- Measure: pin/unpin throughput with a 100% hit rate (measures the accessor
  path alone), hit rate 90% with 8 threads (measures table lock contention),
  p50/p99 pin latency, eviction throughput, peak RSS (frames inline, no page
  heap objects), allocation count (frames vector once), copies (zero; pages
  are never moved after construction via `out`), unsafe lines in the pool,
  text bytes, compile time.
- Failure paths: `fetch` on an `IoErr` must leave the victim frame unpinned
  and absent from the table; a recursive pin traps `explicit`; both fixtures.

## Open risks
- §5.3 says the release function "receives the same receiver". Making the
  receiver the `Frame` (not the pool) is what lets `unpin` know which pin count
  to decrement; a pool-level `pin(view self, id)` as the spec's wording suggests
  would need the release function to receive the released access as well.
  The Phase 1 library reference must fix the release signature one way or the
  other; this note assumes the per-frame form.
- A shared (read-only) pin that returns `view[Page]` from `view self` is not an
  escalation and needs no `unsafe`, but it still needs `release`; the sketch
  shows only the exclusive form.
- The eviction write races with a reader that has not yet incremented its pin
  unless `fetch` re-checks the frame's `id` after pinning; the contract block
  must state this (the C and Rust versions have the same obligation).
