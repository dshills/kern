# Proof pass: persistent fact state

**Date:** 2026-10-31. **Spec:** `specs/prove-state/SPEC.md`.
**Host:** Apple M4 Pro (arm64, macOS 27.2), Go 1.27.3, busy throughout
(one-minute load 8–28). **Commits:** 703fb12 before, this change after.

## The change

The proof pass's state used to be a chain of mutable map layers, one per
block, walked per lookup. The meet looked up every key either side had
touched through both chains and their ancestor.

It is now a struct of persistent maps (`compiler/opt/pmap.go`), one per
kind of fact:
- **Branching:** a 16-way hash-array-mapped trie, with a collision list
  below the last hash bits.
- **Slots** point at shared entries, so a write copies two words a slot
  along its path.
- **Taking a block's state** copies six roots.
- **The meet** intersects map by map. A subtree the two states share is
  returned untouched, and a node one side keeps unchanged is reused, so
  a meet after a branch costs only what the branch changed.

Value-numbering and place keys, the same strings as before, are built
with `strconv` instead of `fmt.Sprintf`. The depth limit, flattening and
the meet's key sets are gone.

Alternatives measured on the way:
- **Branching factor.** 32-way slots of 40 bytes made the guards program
  allocate 4.0 MB per run. 16-way with slots pointing at their entries
  brought it to 1.7 MB. 8-way was no better than 16-way on the guards
  program and cost the lexer more allocations.
- **Building meet results on the stack**, allocating only a node that is
  neither input, removed a large share of the guards program's garbage.

## Proofs unchanged

The `show-bounds-checks` output at both opt levels was compared byte for
byte between 703fb12 and the change, for every program under
`bench/kern`, `examples`, `tests/run`, `tests/trap` and
`tests/positive`: 312 files, 689 proven checks. There is no difference.
The `compiler/opt` tests (with the new `pmap` tests), the
interpreter-versus-native proof differential and the full suite pass.

## Cost (R5)

The figure is instructions retired per run of the pass: 3,000 runs on
one lowered module with `GOGC=off`, minus the same harness without the
pass, back to back:

| program | instructions | cycles |
|---|---|---|
| `examples/selfhost/lexer.kern` | 22.48M → 8.20M (2.7x fewer) | 5.93M → 1.88M (3.2x fewer) |
| 200 sequential guards | 39.94M → 15.97M (2.5x fewer) | 8.43M → 3.91M (2.2x fewer) |

The committed benchmarks, wall time with the collector on (`-benchmem`,
busy host):

| benchmark | 703fb12 | after | gate |
|---|---|---|---|
| `BenchmarkProveLexer` | 1.84 ms, 1.06 MB, 12.6K allocs | 0.51 ms, 0.63 MB, 15.4K allocs | 0.5 ms |
| `BenchmarkProveSequentialGuards` | 2.44 ms, 1.53 MB, 15.5K allocs | 1.30 ms, 1.66 MB, 27.3K allocs | 1.0 ms |

**Against the gate:**
- **The lexer is at it:** 0.51 ms of wall time on a loaded host. Its
  1.88M cycles come to about 0.42 ms at the M4's clock without the
  collector.
- **The guards program misses it by 30%.** Its states grow by a few
  facts per guard, each a persistent insert that copies its path through
  the trie, and its meets keep finding the facts the taken branch added
  and dropping them.

Allocation counts rose on both: path copying makes more, smaller
objects. Bytes fell on the lexer and are level on the guards program.

**What remains:**
- path copying on insert (`withSlot`, the largest allocation left);
- the pass's per-function setup maps;
- `defs`, which returns a fresh slice per instruction.

A transient (mutable while one block is processed) variant of the map
would remove most of the copying, at the cost of tracking ownership.
