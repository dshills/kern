# Workload: lock_free_queue

## Summary
An audited bounded multi-producer multi-consumer queue (Vyukov's sequence-
numbered ring): producers and consumers hold only a `view` of the queue and
synchronize with `Atomic[usize]` positions and per-cell sequence numbers;
element slots are written and read through the `view` inside `unsafe`. The
safe API is `push(view self, own T) -> Result[(), T]` and
`pop(view self) -> Option[T]`. §5.5 names this case: "Lock-free structures are
permitted to use audited unsafe internals behind a safe API."

## Classification
requires audited unsafe internals behind a safe API

## Data structures and ownership graph
```text
struct Cell[T] { seq: Atomic[usize]; val: Option[T] }
struct Mpmc[T] { cells: Vec[Cell[T]]; mask: usize; enq: Atomic[usize]; deq: Atomic[usize] }
unsafe assert Interior for Mpmc        // cells change through a view; obligation: the seq protocol
unsafe assert Share for Mpmc           // obligation: T: Send; every slot has exactly one writer and one reader per turn
```
- `Mpmc` owns the cell vector; each cell owns at most one `T`. Threads hold
  `Shared[Mpmc[T]]` or a scoped `view`; nobody ever holds an `edit`.
- The sequence protocol is the ownership story at runtime: cell `i` belongs to
  the producer that won `enq` for position `pos` from `seq == pos` until it
  publishes `seq = pos + 1`, then to the consumer that wins `deq` until it
  publishes `seq = pos + mask + 1`. That transfer of ownership is invisible to
  the checker and is the content of the contract block.
- A bounded ring was chosen over Michael-Scott because `Atomic[T]` is defined
  only for integers and `bool` (§5.5); a pointer-based queue would cast
  `RawPtr` to `usize` for CAS and face ABA, which is a worse audit.

## Access patterns
| Operation | Convention | Notes |
|---|---|---|
| `push(view self, own v: T) -> Result[(), T]` | `view`, `own` | CAS loop on `enq`; slot write is an interior write |
| `pop(view self) -> Option[T]` | `view` | CAS loop on `deq`; slot read moves the value out through a `view` |
| `Cell.put(view self, own v: T)` / `Cell.take_out(view self) -> T` | `unsafe` interior write, module-private | each guarded by the seq protocol |
| `Atomic.load/store/compare_exchange_weak` | `view` | safe; orderings are compile-time constants (`ATOMIC_INVALID_ORDERING` otherwise) |
No `get2`; no release accessor. The queue never hands out an access to a
slot; values cross the API by move only.

## Why this classification
Safe mutation needs an `edit`, and an `edit` is exclusive for its duration
(§5.4). A lock-free queue is defined by several threads mutating the same
object at once through non-exclusive access, so every slot write and every
move out of a slot is a write through a `view` of an `Interior` type: an
enumerated unsafe operation permitted only in the declaring module (§6), under
`unsafe assert Interior` whose obligation is that the type's own
synchronization (here the sequence atomics with acquire/release pairs) makes
each such write data-race-free. The atomics themselves are safe. Nothing else
in the language could express this and nothing should: the §5.6 extension is
about borrowing, not about concurrent mutation, and is not involved.

## Sketch
```text
module mpmc_bench

contract {
    invariant "cell i is written only by the producer that CASed enq from pos to pos+1 with i == pos & mask, and read only by the consumer that CASed deq likewise";
    requires  "mask + 1 is a power of two and every cell starts with seq == its index";
    ensures   "on Ok, v is owned by the cell until a pop moves it out; on Err, v is returned unmoved";
}
func push(view self, own v: T) -> Result[(), T] {
    pos := self.enq.load(Ordering.relaxed)
    while true {
        i := pos & self.mask
        seq := self.cells.get(i).seq.load(Ordering.acquire)        // projection consumed in-statement
        if seq == pos {
            match self.enq.compare_exchange_weak(pos, pos + 1, Ordering.relaxed, Ordering.relaxed) {
                Ok(old) => {
                    unsafe { self.cells.get(i).put(v) }                // interior write through a view
                    self.cells.get(i).seq.store(pos + 1, Ordering.release)   // publish
                    return Result.Ok(())
                }
                Err(cur) => pos = cur
            }
        } else {
            if seq < pos { return Result.Err(v) }                    // full: hand v back
            pos = self.enq.load(Ordering.relaxed)
        }
    }
}

```
`pop(view self) -> Option[T]` is the mirror: it waits for `seq == pos + 1` on
`deq`, CASes `deq`, does `unsafe { v = self.cells.get(i).take_out() }`, and
publishes `seq.store(pos + self.mask + 1, Ordering.release)`; `seq < pos + 1`
means empty. In `push`, `v` is moved on the `Ok` path and returned on the full
path; every loop back-edge is reached with `v` still owned, so
`OWN_CONDITIONAL_MOVE` does not fire. The `unsafe` surface is `put`,
`take_out`, and the two assertions.

## Benchmark notes
- C: the same Vyukov ring with C11 atomics and the same orderings.
- Rust: `crossbeam::ArrayQueue` (which is this algorithm) and a hand-written
  port with identical orderings; `std::sync::mpsc` as context only.
- Measure: push/pop pairs per second with 1P1C, 4P4C, 8P8C, p50/p99 per
  operation, fairness (per-thread throughput spread), peak RSS (cells inline:
  `show-layout` of `Cell[u64]` must be 16 bytes plus `Option` padding),
  allocation count (one), copies (zero), unsafe lines (expected under 20),
  text bytes, compile time; the Phase 3 litmus suite under
  `tests/concurrency/litmus/` for the acquire/release pairs and a TSan run.
- Failure paths: push on full returns the value; pop on empty returns `None`;
  a deliberately weakened ordering twin (`relaxed` publish) is a TSan fixture
  that must be caught.

## Open risks
- `Option[T]` in each cell is there only so that every slot is always
  initialized for the destructor (§5.7); it costs a discriminant per cell and
  a branch in `take_out`. A `MaybeUninit`-style cell would need an unsafe
  destructor that consults `seq`, which is more audit surface. Measure first.
- Interior writes through a `view` across a `Vec` projection require the
  `Interior` property to propagate through `Vec[Cell[T]]` (types.md §3 says it
  does); the checker must not treat `self.cells.get(i)` as a non-`Interior`
  `view` and emit `noalias readonly` (§5.4), or the publish store could be
  reordered. This is the one codegen hazard specific to this workload.
