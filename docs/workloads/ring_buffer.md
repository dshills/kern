# Workload: ring_buffer

## Summary
A bounded single-owner FIFO over a fixed array: `push` moves an element in,
`pop` moves one out, `peek` views the head, with explicit full/empty results
and no allocation after construction. This is the receive ring for the packet
parser and the per-worker local queue of the thread pool. The concurrent
single-producer single-consumer variant belongs to the lock-free queue
workload, not this one.

## Classification
expressible in the core

## Data structures and ownership graph
```text
struct Ring[T] { slots: [1024]Option[T]; head: usize; tail: usize; len: usize }
```
- The ring owns its array inline; each slot owns at most one `T`
  (`Option.Some`). Empty slots hold `Option.None`, so every slot is always a
  fully initialized value and a `Ring[T]` is destroyed by destroying 1024
  options (§5.7), which is a no-op for `None`.
- For `Copy` elements (bytes, packet descriptors) `[1024]T` without `Option`
  is used and `pop` returns a copy; the sketch shows the move-only form, which
  is the harder one.
- No handles, no pointers: positions are `usize` indices masked or reduced
  modulo the capacity.

## Access patterns
| Operation | Convention | Notes |
|---|---|---|
| `push(edit self, own T) -> Result[(), T]` | `edit`, `own` | returns the value back when full (no destroy, no copy) |
| `pop(edit self) -> Option[T]` | `edit` | `replace(self.slots[self.head], Option.None)` moves out whole |
| `peek(view self) -> Option[view[T]]`: not expressible (`ACC_ESCAPE_STORE`); instead `peek(view self) -> view[Option[T]]` matched by the caller | elision | one `view` source |
| `len(view self) -> usize`, `is_full(view self) -> bool` | `view` | `Copy` reads |
No `get2`; no release accessor. Indexing `self.slots[self.tail]` is a `place`
(identifier chain with an index), so `replace` applies directly.

## Why this classification
`replace(place, v)` and `take(place)` are the sanctioned ways to move a value
out of an element without a partial move (§5.1, §5.4: both are `edit`
accesses), and `Option[T]` gives every slot a legal resting state so no slot is
ever uninitialized. Index arithmetic is on `Copy` integers; the bounds check
on `self.slots[i]` traps rather than overruns. A `push` while a `peek` binding
is live is `ACC_CONFLICT` (`with h := r.peek() { r.push(x) }`), which is the
same interior-pointer rule as the vector. Nothing requires `unsafe` or the
§5.6 extension.

## Sketch
```text
module ring_bench

impl[T] Ring[T] {
    func push(edit self, own v: T) -> Result[(), T] {
        if self.len == 1024 { return Result.Err(v) }            // hand it back, no destroy
        replace(self.slots[self.tail], Option.Some(v))          // bounds-checked place; old None destroyed
        self.tail = (self.tail + 1) % 1024
        self.len = self.len + 1
        return Result.Ok(())
    }

    func pop(edit self) -> Option[T] {
        if self.len == 0 { return Option.None }
        v := replace(self.slots[self.head], Option.None)        // moves the element out whole
        self.head = (self.head + 1) % 1024
        self.len = self.len - 1
        return v
    }

    func peek(view self) -> view[Option[T]] {
        return self.slots[self.head]                            // elision: projection of self
    }
}

func drain(r: edit[Ring[Packet]], out_count: edit[u64]) {
    while r.len() > 0 {
        match r.pop() {                                         // owned temporary subject
            Some(p) => { handle(p); out_count = out_count + 1u64 }
            None    => trap()
        }
    }
}
```
`match r.pop()` consumes the temporary; `p` is moved into the arm and
destroyed at the arm's end unless `handle` takes it by `own` (it does).

## Benchmark notes
- C: `struct { T slots[1024]; size_t head, tail, len; }` with `memcpy`-free
  moves (assignment of a struct) and explicit full/empty checks.
- Rust: an array of `Option<T>` with `mem::replace`/`take`, which is the exact
  analogue, plus `VecDeque` with fixed capacity as context.
- Measure: push/pop pairs per second for `u64` and for a 64-byte move-only
  `Packet`, p50/p99 per operation, peak RSS (inline array; no heap), allocation
  count (zero after construction), copies (`show-copies`: one bitwise move of
  `size_of(T)` per push and per pop, matching C), bounds checks per operation
  (two; whether `self.tail < 1024` is proven from the modulo is the
  `proven_by` question), unsafe lines (zero), text bytes, compile time.
- Failure paths: push on full returns `Err(v)` and the caller must consume it
  (`OWN_*` rules make dropping it explicit); pop on empty returns `None`.

## Open risks
- `Option[T]` adds a discriminant byte plus padding to every slot
  (`show-layout` reports it); for `T = u64` that doubles the slot size. The
  `Copy`-element form without `Option` avoids it, and the report should show
  both rows.
- The modulo on every push/pop is what C would write as a mask; `& 1023` is
  equally expressible and the benchmark build should use it.
