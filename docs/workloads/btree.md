# Workload: btree

## Summary
An in-memory B-tree over `u64` keys with fixed fanout, implementing insert with
node split and delete with merge and key rotation. Nodes live in a `Pool[Node]`
and refer to each other by `PoolId[Node]` handles. Split is the moment two
nodes are mutated at once, which the core answers with `Pool.get2`.

## Classification
expressible in the core

## Data structures and ownership graph
```text
struct Node { keys: [15]u64; vals: [15]u64; children: [16]NodeId; count: u32; leaf: bool }
struct BTree { nodes: Pool[Node]; root: NodeId; height: u32 }
```
- `BTree` owns the pool; the pool owns every node; `root` and every
  `children[i]` are `Copy` handles (`PoolId[Node]`), not references (§5.6).
- A parent does not own its children in the type system; ownership is flat
  (pool slot), structure is handles. A handle to a freed node traps
  (`vacant`/`generation`), which is memory-safe by construction.
- With move-only values, `vals` would be `[15]Option[V]` and moves use
  `take`/`replace`; the benchmark uses `u64` to measure the handle path, not
  the value path.

## Access patterns
| Operation | Convention | Notes |
|---|---|---|
| `search(t: view[BTree], key) -> Option[u64]` | `view` | descends by handle; each hop is `t.nodes.get(id)` consumed in the statement |
| `insert(t: edit[BTree], key, val)` | `edit` | split path below |
| `t.nodes.get2(left, right) -> (edit[Node], edit[Node])` | multi-access, runtime `disjoint` | the only place two nodes are edited at once |
| `t.nodes.at(parent) -> edit[Node]` | elision | opened only after the `get2` block closes |
| merge: `get2(left, right)` then `at(parent)`; rotation: `get2(sibling, child)` then `at(parent)` | same shape |
| range scan: cursor `Cursor { node: NodeId; slot: u32 }` (`Copy`), `next(t: view[BTree], c: Cursor) -> Option[Cursor]` | cursor-handle rewrite |
No release accessor is needed. The pool's `alloc(edit self, own Node) -> Result[NodeId, AllocErr]`
is the only allocation.

## Why this classification
The hard case (two simultaneous edits into one pool) is §5.8's own example and
is covered by the multi-access rule of §5.3: `get2` is implemented in `std`
inside `unsafe`, traps when `a == b`, and its two returned accesses are distinct
roots that never conflict (§5.4 multi-access bindings). The workload's source
has no `unsafe`. The parent edit is opened in a separate block because the
`get2` block freezes `t.nodes` (§5.4): reading the median key before the block
is forced by `ACC_CONFLICT`, and that is also the correct code.
A reference-holding range iterator (`Iter { t: view[BTree]; ... }`) is
`ACC_ESCAPE_STORE`; the `Cursor` of two `Copy` fields replaces it at the cost
of one `get` per step. The §5.6 extension is not required.

## Sketch
```text
module btree_bench

func split_child(t: edit[BTree], parent: NodeId, i: usize) -> Result[(), AllocErr] {
    left := t.nodes.get(parent).children[i]            // Copy handle read, access ends here
    median := t.nodes.get(left).keys[7]                // read before any edit is open
    med_val := t.nodes.get(left).vals[7]
    right := t.nodes.alloc(Node.empty())?              // may fail: nothing edited yet
    with l, r := t.nodes.get2(left, right) {           // traps disjoint if left == right
        for k in 0..7 {
            r.keys[k] = l.keys[8 + k]
            r.vals[k] = l.vals[8 + k]
        }
        if !l.leaf {
            for k in 0..8 { r.children[k] = l.children[8 + k] }
        }
        r.leaf = l.leaf
        r.count = 7u32
        l.count = 7u32
    }                                                   // both accesses end; t.nodes unfrozen
    with p := t.nodes.at(parent) {
        shift_right(p, i + 1)                           // p: edit[Node] passed down
        p.keys[i] = median
        p.vals[i] = med_val
        p.children[i + 1] = right
        p.count = p.count + 1u32
    }
    return Result.Ok(())
}
```
Merge is the mirror image: `with l, r := t.nodes.get2(left, right)` moves
`r`'s keys into `l`, the block closes, `t.nodes.free(right)` runs the slot's
destructor and bumps its generation, then `with p := t.nodes.at(parent)`
removes the separator.

## Benchmark notes
- C: array-of-nodes B-tree with `uint32_t` indices (not pointers), so the
  indirection is identical, plus a pointer-based variant as the lower bound.
- Rust: a hand-written index-based B-tree with the same fanout (the std
  `BTreeMap` uses raw pointers internally and a different fanout; it is
  reported as context only).
- Measure: random insert throughput at 1e7 keys, point lookup p50/p99, delete
  with merge, range scan of 1000 keys via the cursor, peak RSS (pool slot size
  must equal the C struct size per `show-layout`), allocation count (pool
  chunks only), copies (zero), unsafe lines (zero in the workload),
  generation-check count per lookup from `show-bounds-checks` (one per hop),
  text bytes, compile time.
- Failure paths: `get2(a, a)` traps `disjoint`; a stale handle after `free`
  traps `generation`; both are fixtures.

## Open risks
- One generation compare per hop is the P11 handle-indirection cost; the
  user-level row (a) may miss 10% on deep trees while the library row (b)
  with raw pointers meets it. The report must show both.
- `Node.empty()` constructs a 15-key node by value and moves it into the pool
  slot (a bitwise move of about 300 bytes); `alloc_with(out)` (plan 1.2)
  removes it and should be used in the benchmark build.
