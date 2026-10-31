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

## Second step: transients and tables by ID

Same host and method. Each step is measured against the one before.

- **Transient writes** (`pmap.go`, `token`):
  - A node records the state that made it, and that state's writes
    change it in place. A block's run of inserts therefore copies each
    node once, not once per insert.
  - A state retires its token whenever its maps become shared: on
    `child()` (both sides take fresh tokens) and on `snapshot()`.
  - A meet's new nodes belong to no token. Nothing shared is ever
    changed.
- **Per-function tables by ID:** each value's defining instruction, and
  the block order, predecessors, loop headers and per-round states, are
  slices indexed by value and block IDs instead of Go maps keyed by
  pointer. `postorder`'s visited set is one too.

Results:

| program | a52c790 | after |
|---|---|---|
| lexer, per run | 8.20M instructions, 1.83M cycles | 6.69M, 1.43M |
| guards, per run | 15.96M instructions, 3.79M cycles | 12.11M, 2.64M |
| `BenchmarkProveLexer` | 0.51 ms, 0.63 MB | 0.39 ms, 0.50 MB |
| `BenchmarkProveSequentialGuards` | 1.30 ms, 1.66 MB | 0.86 ms, 1.04 MB |

**Both programs now meet their gates** (0.5 ms and 1.0 ms), measured on
a loaded host. The labels are byte-identical to 703fb12's again, over
the same 312 files.

## Phase review

- **realitycheck** (gpt-6.1-sol, the spec as its own plan, against
  `compiler/opt`): PARTIALLY_ALIGNED, score 100, no drift, no
  violations. Several items read UNCLEAR or PARTIAL because it sees
  symbols, not bodies; R1–R4 are verified by the tests and the label
  comparison above.
- **verifier analyze** (offline): five low-priority unit-test
  recommendations, none on the fact state. The one on `ExecOptionsFor`
  is answered by `TestExecOptions`.
