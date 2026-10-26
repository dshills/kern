# Workload: hash_table

## Summary
An open-addressing hash map with linear probing, tombstones, and rehash on a
load factor of 3/4: `insert`, `get`, `remove`, iteration, and growth. Keys are
`u64`; values are a move-only `V` to exercise moves into and out of slots. The
map is written in user code over `Vec`, not taken from the standard library, so
the measurement is of core-language code.

## Classification
expressible in the core

## Data structures and ownership graph
- `Map[V] { states: Vec[u8]; keys: Vec[u64]; vals: Vec[Option[V]]; count: usize; tomb: usize }`
  (structure-of-arrays; the array-of-structs form `Vec[Slot[V]]` with
  `union Slot[V] { Empty; Tomb; Full(key: u64, val: V) }` works identically).
- The map owns its three vectors; each vector owns its slots; a `Full` slot owns
  one `V`. Nothing points into a slot: lookups return a projection consumed in
  the calling statement, or a `Copy` index.
- Rehash builds a fresh `Map[V]`, moves every live value into it with
  `replace(v, Option.None)`, then `replace(self, new)`; no element is copied.

## Access patterns
| Operation | Convention | Notes |
|---|---|---|
| `probe(view self, key) -> usize` | `view` | returns a `Copy` index; no access escapes |
| `get(view self, key) -> Option[view[V]]`: not expressible (an access inside a union is `ACC_ESCAPE_STORE`); instead `find(view self, key) -> Option[usize]` then `val_at(view self, i) -> view[V]` | elision | two statements, one source |
| `insert(edit self, key, own V) -> Result[(), AllocErr]` | `edit`, `own` | writes through `with v := self.vals.at(i)` |
| `remove(edit self, key) -> Option[V]` | `edit` | `replace(v, Option.None)` moves the value out |
| iteration: `first(view self) -> Option[usize]`, `next(view self, i) -> Option[usize]` | cursor index | the cursor-handle rewrite |
No `get2` is needed; no release accessor. Growth happens only in `insert`,
which holds the only `edit` and therefore cannot conflict with any live
projection (§5.4).

## Why this classification
Every write goes through an `edit self` and a single `with` binding, and every
read is a projection consumed in its own statement. The one pattern the core
refuses is an entry handle that holds a view (`Entry { m: view[Map]; i: usize }`),
which is `ACC_ESCAPE_STORE` (§5.2); the rewrite is the index cursor above, and
it costs one extra bounds check per step that `show-bounds-checks` reports.
No rule forces `unsafe`: the probe loop is integer arithmetic on `Copy` values.

## Sketch
```text
module hash_bench

impl[V] Map[V] {
    func probe(view self, key: u64) -> usize {
        mask := self.keys.len() - 1
        i := hash(key) & mask
        while true {
            s := self.states.get(i)                    // u8 is Copy: read ends immediately
            if s == 0u8 || (s == 2u8 && self.keys.get(i) == key) { return i }
            i = (i + 1) & mask
        }
    }

    func insert(edit self, key: u64, own val: V) -> Result[(), AllocErr] {
        if (self.count + self.tomb) * 4 >= self.keys.len() * 3 { self.grow()? }
        i := self.probe(key)
        with s := self.states.at(i) { s = 2u8 }
        with k := self.keys.at(i) { k = key }
        with v := self.vals.at(i) {                      // edit rooted at self.vals
            replace(v, Option.Some(val))                 // val moved in; old value destroyed
        }
        self.count = self.count + 1                      // self.count is disjoint from self.vals
        return Result.Ok(())
    }

    func remove(edit self, key: u64) -> Option[V] {
        i := self.probe(key)
        if self.states.get(i) != 2u8 { return Option.None }
        with s := self.states.at(i) { s = 1u8 }          // tombstone
        self.count = self.count - 1
        self.tomb = self.tomb + 1
        with v := self.vals.at(i) { return replace(v, Option.None) }  // moved out whole
    }
}
```
The three `with` blocks are sequential, so their accesses never overlap; a
single-statement form `self.vals.at(i) = ...` is not a place (grammar `place`
is an identifier chain), which is why `with` is used.

## Benchmark notes
- C: identical open-addressing table with `uint64_t` keys, the same hash
  (a fixed multiplicative hash, not SipHash), the same load factor and
  tombstone policy.
- Rust: a hand-written table with the same layout (not `HashMap`, whose
  SwissTable layout and SipHash default would confound the comparison); a
  secondary row against `hashbrown` with the same hasher is reported as context.
- Measure: insert throughput to 1e7 keys, hit and miss lookup p50/p99, remove
  throughput, rehash cost (p99 captures it), peak RSS, allocation count (three
  per rehash), copies (zero), unsafe lines (zero), text bytes, compile time.
- Failure paths: `AllocErr` on `grow` must leave the map intact (the fresh map
  is built before `replace(self, new)`).

## Open risks
- The SoA form costs three bounds checks per probe step where C has none; the
  10% target depends on `dominated_by_check` proofs across the three vectors of
  equal length, which the Phase 1 provenance rules do not yet promise.
- Iteration via `first`/`next` returns `Option[usize]`, so a range `for` cannot
  be used directly; if the agent experiment shows cursor loops are error-prone
  this is a data point for the §5.6 extension, not a blocker.
