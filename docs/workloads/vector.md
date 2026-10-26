# Workload: vector

## Summary
A growable, contiguous, owned sequence `Vec[T]`: push with amortized doubling,
pop, indexed read and write, in-place iteration, and `clear`. The §8 suite
measures it as user-level code over the standard-library container; the §5.8
case "interior pointers after container growth" is its adversarial path and is
rejected statically (see `tests/adversarial/NOTES.md`).

## Classification
expressible in the core

## Data structures and ownership graph
- `Vec[T]` (std, `std/vec.kern`): owns a raw buffer obtained from `own A: Allocator`
  (§6) and every element in `[0, len)`. The buffer, `len`, `cap` and the
  allocator are owning fields; `Vec[T]` is move-only, `Send` iff `T: Send`,
  `Share` iff `T: Share`.
- User code owns the `Vec[T]` in a local or a field. Elements are owned by their
  slot; nothing else points at a slot. There are no handles into a `Vec`: an
  element is reached only by index through a projection.
- Growth moves the buffer; no safe value can observe the old address, because no
  access into the buffer can be live at the moment `push` runs (§5.4).

## Access patterns
| Operation | Convention | Notes |
|---|---|---|
| `push(edit self, own T) -> Result[(), AllocErr]` | `edit`, `own` | element moved in; may reallocate |
| `pop(edit self) -> Option[T]` | `edit` | element moved out whole, never partially |
| `get(view self, i: usize) -> view[T]` | elision, single `view` source | bounds trap `bounds` |
| `at(edit self, i: usize) -> edit[T]` | elision, single `edit` source | bounds trap `bounds` |
| `items(view self) -> view[]T`, `items_mut(edit self) -> edit[]T` | elision | `for` subject |
| `len(view self) -> usize` | `view` | `Copy` read |
No `get2` is needed: the workload never edits two elements at once. A swap of
two elements is `swap(edit self, i, j)` inside the library, which is where the
one runtime disjointness check lives. No release accessor is needed.

## Why this classification
The workload's own source contains no `unsafe` block. Every operation is a
convention or a projection consumed in one statement or bound by `with`/`for`
(§5.3). Iteration is built in: `for x in v.items()` yields copies for `Copy`
elements and per-iteration `view`s otherwise (§4), so no borrowing-iterator
aggregate is required. The reference-holding forms that the core cannot
express (a user-defined `Iter { v: view[]T; i: usize }`) are `ACC_ESCAPE_STORE`
(§5.2) and are not needed here: a cursor is just the index `i`.
The container itself uses audited unsafe internals (`RawPtr`, `Allocator`) in
`std/vec.kern`; that is row (b) of §2.3 and is not counted against this workload.

## Sketch
```text
module vector_bench

func fill(v: edit[Vec[u64]], n: usize) -> Result[(), AllocErr] {
    for i in 0..n {
        v.push(u64(i) * 3u64)?                 // own argument; may reallocate
    }
    return Result.Ok(())
}

func sum(v: view[Vec[u64]]) -> u64 {
    total := 0u64
    for x in v.items() {                      // x is a copy: u64 is Copy
        total = total + x
    }
    return total
}

func scale(v: edit[Vec[u64]], k: u64) {
    for x in v.items_mut() {                  // x is a per-iteration edit[u64]
        x = x * k
    }
}

func bump_first(v: edit[Vec[u64]]) {
    with e := v.at(0) {                       // edit rooted at v, live for the block
        e = e + 1u64
    }                                          // access ends; v may now grow again
    v.push(0u64)                               // accepted
}
```
The rejected twin is `with e := v.at(0) { v.push(e) }`: `push` edits root `v`
while `e` holds a live derived access rooted at `v` (`ACC_CONFLICT`, §5.4).

## Benchmark notes
- C: `realloc`-based vector with the same doubling factor and the same bounds
  checks compiled in (checks are part of the semantics being compared, so the C
  version keeps an explicit `if (i >= len) abort()`).
- Rust: `std::vec::Vec<u64>` with indexing (checked) and `iter()/iter_mut()`.
- Measure per §8: push throughput at 1e6 and 1e8 elements, p50/p99 per push
  (reallocation spikes appear at p99), indexed read and write throughput,
  `for` iteration throughput, peak RSS (should equal the C/Rust curve: no
  hidden copies), heap allocation count (`show-allocations` must report
  `log2(n)` growth allocations and nothing per push), copies (`show-copies`
  must show zero deep copies), unsafe lines (zero in `bench/kern/vector/`),
  text bytes, compile time.
- The `[unproven]` codegen check of §5.4: the `scale` loop with `edit[]u64`
  lowered to `noalias` must vectorize like Rust's `&mut [u64]`.

## Open risks
- Bounds checks in `items()` loops must be eliminated by `proven_by: loop_bound`
  (§7) or the 10% target fails on the read loop; `show-bounds-checks` is the
  evidence.
- `Vec[T].new(own A)` and `push` returning `Result[(), AllocErr]` force a `?` on
  every push; if the benchmark harness compares against unchecked C `realloc`
  the error path must be counted as semantics, not overhead.
