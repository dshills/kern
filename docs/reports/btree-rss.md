# btree peak RSS: why kern used 3.2x the memory of C

Date: 2026-10-27. Follows docs/reports/containers-in-kern.md, after which the
btree row of bench/REPORT.md still read 195.8 MiB for kern against 60.9 MiB
for C (ratio 3.21). Measurements are single runs under `/usr/bin/time -l` on
the report's host (Apple M4 Pro, macOS 25.6); the harness figures are the
same quantity (`ru_maxrss`) averaged over five runs.

## What the two programs allocate

Both implementations build the same tree: 195,695 nodes in a pool whose
capacity doubles to 262,144 (C from 16, kern from 8; the last doubling is the
same in both). Per node:

| | C (`bench/c/btree/main.c`) | kern (`bench/kern/btree/btree.kern`) |
|---|---|---|
| keys + vals (15 u64 each) | 240 B | 240 B |
| children (16 handles) | 64 B (`uint32_t` index) | 192 B (`PoolId[Node]`: index, generation, identity tag, 12 B) |
| count, leaf, padding | 8 B | 8 B |
| node (`kern show-layout`) | 312 B | 440 B |
| `Pool` slot metadata (`PoolSlotMeta`) | — | 8 B |
| `Pool` free list | — | 4 B |
| bytes per node in use | 312 B | 452 B |
| nodes × bytes in use | 58.2 MiB | 84.4 MiB |

So the data each program keeps live explains a ratio of 1.45, not 3.2. The
rest came from how `Pool.grow` obtained its storage.

## Growth policy against the allocator

C grows with `realloc`, which macOS libmalloc (and glibc, through `mremap`)
performs on a large block without a second resident copy. `Pool.grow`
allocated three fresh buffers, copied, then freed the old ones, for the sake
of a transactional grow (nothing changes unless every new buffer was
obtained). On macOS that pattern costs twice:

- at the moment of the last doubling the old element buffer (131,072 × 440 B
  = 55 MiB, fully touched) and the copied half of the new one are both
  resident;
- `free` of a large block parks it in libmalloc's large-block cache instead
  of returning it to the kernel, so every outgrown element buffer (8 … 131,072
  slots, 110 MiB in total) stayed resident until exit.

A C simulation of the two policies with the real sizes reproduces the report
(`ru_maxrss`, elements touched once, 195,695 of a 262,144 capacity):

| growth | 312 B elements, from 16 | 440 B elements, from 8 |
|---|---|---|
| `realloc` | 59.2 MiB | 82.9 MiB |
| alloc, copy, free | 136.1 MiB | 191.3 MiB |

Running the unmodified kern binary with `MallocLargeCache=0` gave 116.6 MiB:
the transient double copy alone, without the cache, is still 1.9x.

## Change

`Pool.grow` (std/core.kern, mirrored in compiler/resolve/prelude/core.kern)
now allocates the two small buffers (slot metadata, free list) first and
then resizes the element buffer with `heap_try_realloc`, as `Arena.grow` and
`Vec.reserve` already did. The grow is still transactional: a failed realloc
leaves the old element buffer valid and the two fresh buffers are released;
a successful one never holds two copies of the elements. `heap_try_realloc`
goes through `realloc` for alignments up to 16 and falls back to allocate,
copy and free above that, so the saving applies to every element type a
B-tree node would plausibly have.

Measured on the btree workload (release build, same host):

| | before | after |
|---|---|---|
| kern peak RSS | 195.9 MiB | 87.8 MiB |
| C peak RSS | 60.9 MiB | 60.9 MiB |
| ratio | 3.21 | 1.44 |
| kern wall time, 2M inserts + traversal | 651 ms | 503 ms |

The checksum (13433169380067381159) is unchanged; the time improvement is the
page faults and the copies that the doubling no longer performs.

## What remains, and why it stays

The remaining 1.44 is the per-node table above: 128 of the extra 140 bytes
per node are the 16 `PoolId` handles in the workload's own `Node`, 12 bytes
each against C's 4-byte index because a kern handle carries the generation
and the pool identity that `Pool.check` verifies on every access (spec §5.6:
bounds, identity, vacant, generation). The other 12 bytes are the pool's
per-slot generation and free list. Neither is a property of the benchmark
program or of `Pool`'s growth policy; shrinking them would mean a narrower
handle in the language (for instance a 32-bit generation shared with the
index, or an identity tag kept out of the handle in release builds), which
is a specification change, not a library fix. The btree row therefore still
misses the 10% RSS target, by the width of a checked handle rather than by
the allocator.
