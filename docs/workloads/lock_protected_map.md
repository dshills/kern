# Workload: lock_protected_map

## Summary
The hash table workload wrapped in `Mutex[Map[V]]` and shared between threads
two ways: moved into `Shared[Mutex[Map]]` for detached threads, and borrowed as
`view` by scoped threads for a bounded parallel phase. Measures lock
acquisition cost, the release accessor on every exit path, and the
`Send`/`Share` derivation for a non-trivial container.

## Classification
expressible in the core

## Data structures and ownership graph
```text
struct Map[V] { ... }                                   // from hash_table.md: Send iff V: Send
struct Counter { m: Mutex[Map[u64]] }                   // Send and Share iff Map[u64]: Send
```
- Scoped form: `m` is owned by the frame that opens `with s := thread.scope()`;
  every spawned thread receives `view[Mutex[Map]]`, which requires `Share`
  (§5.5), and the scope joins before `m` can be touched or destroyed again.
- Detached form: `Shared[Mutex[Map]]` with one count per thread; the last
  owner destroys the map.
- Inside a thread, `with g := m.lock()` binds `g: edit[Map]` rooted at the
  mutex; the mutex itself is frozen for the block, so a second `lock()` in the
  same block is `ACC_CONFLICT` statically (and a recursive lock from another
  call path traps `explicit`, spec §13 gap 4).

## Access patterns
| Operation | Convention | Notes |
|---|---|---|
| `Mutex[T].lock(view self) -> edit[T] release unlock` | std escalation | one per critical section |
| `bump(m: view[Mutex[Map[u64]]], key: u64)` | `view` | the spawned function; `view` arg requires `Share` |
| `s.spawn(bump_range, view m, lo, hi)` | scoped `view` | live until the `with s` block ends |
| `thread.spawn(worker, shared.clone())` | `own Shared` | detached form |
| read path `get_copy(m: view[Mutex[Map[u64]]], key) -> Option[u64]` | `view` | copies the `u64` out under the lock |
No `get2`. Returning a `view[V]` obtained under the lock is `ACC_RELEASE_ESCAPE`
(§5.3): a value must be copied or moved out before the block ends, which is
the correct rule for a lock-protected map.

## Why this classification
`Mutex[T]` is `Send` and `Share` iff `T: Send` (§5.5); `Map[u64]` is `Send`
by structural derivation (three `Vec`s of `Send` elements). A scoped spawn may
pass `view` arguments if the type is `Share`, and the access is extended to the
end of the `with s` block, the one cross-thread borrow in the language
(§5.5). Mutation happens only through the `edit` that `lock` returns, which the
checker roots at the mutex and treats as exclusive; data-race freedom then
follows from the §5.5 argument with no annotation in the workload. The
workload contains no `unsafe`; `std/mutex.kern` contains the escalation.

## Sketch
```text
module lockmap_bench

func bump_range(m: view[Mutex[Map[u64]]], lo: u64, hi: u64) {
    for k in lo..hi {
        with g := m.lock() {                             // edit[Map[u64]]; unlock at '}'
            match g.find(k) {
                Some(i) => with v := g.val_at_mut(i) { v = v + 1u64 }
                None    => { match g.insert(k, 1u64) { Ok(u) => {}  Err(e) => trap() } }
            }
        }
    }
}

func parallel_count(data: view[]u64, threads: u64) -> Result[u64, SpawnErr] {
    m := Mutex.new(Map[u64].new(SystemAllocator{}))
    n := u64(len(data)) / threads
    with s := thread.scope() {                            // Scope joins every thread at '}'
        for t in 0..threads {
            s.spawn(bump_range, view m, t * n, (t + 1u64) * n)?   // m: Share, rooted outside the block
        }
    }                                                     // all views released here
    with g := m.lock() { return Result.Ok(u64(g.count())) }   // m is edit-able again
}

func reader(own shared: Shared[Mutex[Map[u64]]], key: u64) -> Option[u64] {
    with g := shared.get().lock() {
        match g.find(key) {
            Some(i) => return Option.Some(g.val_at(i))   // u64 copied out before unlock
            None    => return Option.None
        }
    }
}
```
`s.spawn(..., view m, ...)?`: on `Err` the `view` is released and no thread
exists (§5.5); the `?` leaves the block, which joins the threads already
started before `m` is destroyed.

## Benchmark notes
- C: pthreads with `pthread_mutex_t` around the C hash table from
  hash_table.md; the same thread counts and key ranges.
- Rust: `std::thread::scope` with `&Mutex<Map>` (exact analogue of the scoped
  form) and `Arc<Mutex<Map>>` for the detached form; `parking_lot` as context.
- Measure: operations per second with 1, 4, 8 threads on uniform and skewed
  keys (contention), p50/p99 lock hold and acquire time, peak RSS, allocation
  count (the map's rehashes only), copies (zero), unsafe lines (zero outside
  `std`), text bytes, compile time; the Phase 3 TSan run with a deliberate
  race twin (an `unsafe assert Share` on a non-synchronized counter) that TSan
  must catch.
- Failure paths: `SpawnErr` mid-loop; a trap inside a critical section
  terminates with the lock held (no release runs, §6) and the fixture's
  sentinel lines prove it.

## Open risks
- Lock granularity: one mutex around the whole map is the workload as
  specified; a striped map is `[16]Mutex[Map]` indexed by hash, still core.
- `find` then `val_at_mut` is two probes under the lock where C does one;
  an `entry`-style API returning `edit[Option[V]]` from `edit self` by
  elision collapses them and is core too.
