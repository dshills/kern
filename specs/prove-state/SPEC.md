# Proof pass: persistent fact state

**Version:** 0.1 (2026-10-31). **Scope:** the fact state of the proof
pass in `compiler/opt/prove.go` (the `facts` type, its accessors and
`meet`), a new file `compiler/opt/pmap.go` with its tests, the benchmark
and test files of `compiler/opt`, and the report
`docs/reports/prove-state.md`. **Language impact:** none. No proof
changes, so no label `show-bounds-checks` reports changes.

## 0. Terms, goals and non-goals

**Terms.**
- **Rn:** requirement number n of §2.
- **ID:** the integer `ID` field of an `ir.Value` or `ir.Local`.
- **`GOGC=off`:** the Go runtime setting that disables the garbage
  collector for a run.
- **FNV-1a:** the 64-bit Fowler–Noll–Vo string hash (variant 1a), from
  Go's `hash/fnv`.
- **Hash-array-mapped trie:** a tree indexed by successive 5-bit chunks
  of a key's hash, each node holding a bitmap of its occupied slots.
- **Instructions retired, cycles:** the counters `/usr/bin/time -l`
  reports for a process.

**Goals.**
- Make the proof pass cheaper, toward the compile-time gate below, by
  replacing the representation of its state.
- Keep every proof exactly as it is.

The representation is this spec's subject. Unlike a behavioral spec it
prescribes the data structure (R1, R3), because the change is that
structure, and nothing observable must change (R4).

**Non-goals.**
- New proofs, or fewer.
- Changes to the lowering, the backends or the interpreter.
- Changes to how a check is labelled or reported.
- Meeting the gate is the aim, not a condition of the change: a measured
  miss is reported (R5).

## 1. Background

The pass is a forward must-analysis. Each block's entry state is the
meet (intersection) of its incoming states.

Today a state is a chain of mutable map layers: a block's state is a
child layer over its predecessor's. A lookup walks the chain, one map
lookup per layer, up to 32 layers. `meet` collects every key either side
added since the chains' common ancestor, then looks each key up through
both chains and the ancestor.

At 703fb12, one run of the pass costs 23.5M instructions on
`examples/selfhost/lexer.kern` and 40M on the 200-guard program of
`BenchmarkProveSequentialGuards`, measured with `GOGC=off` (§5).
`BenchmarkProveLexer` measures 1.72 ms per run. The compile-time gate of
`docs/reports/prove-guards.md` is 0.5 ms for the lexer and 1.0 ms for
the guards program.

## 2. Requirements

**R1 — Persistent maps.** `compiler/opt/pmap.go` provides an immutable
hash-array-mapped trie, `pmap[K, V]`. Its operations:
- lookup;
- insert and delete, each returning a new map that shares every
  untouched subtree with the old one;
- iteration over all entries;
- size;
- intersection with a caller's combine function (R2);
- equality with a caller's value-equality function.

An operation never changes an existing map. Keys are hashed by a
function the map is given:
- an `*ir.Value` or `*ir.Local` by its ID;
- a string by FNV-1a;
- a pair of values by a mix of both IDs.

Keys whose 64-bit hashes are equal are kept in a collision list at the
bottom of the trie and compared with `==`.

The hash function must be deterministic, and keys equal under `==` must
have equal hashes. A map keeps the hash function it was made with for its
whole life and passes it to every map derived from it. Intersection and
equality take two maps of the same key type, which use the same hash
function by construction: one function per key type, fixed in
`prove.go`.

**R2 — Intersection.** `intersect(a, b, combine)` returns the map of
keys present in both. Each value is what `combine(key, va, vb)` returns,
and a key is dropped when it reports false. Where the two maps share a
subtree (the same node pointer), the result is that subtree, untouched.
That requires `combine(k, v, v)` to return `(v, true)`, which every
combine function in R3 does.

**R3 — The fact state.** `facts` holds one `pmap` per kind of fact:
- `lt`: bound keys to the largest offset;
- `equiv`: values to values;
- `vn`: value-numbering keys to values;
- `rep`: length keys to their class representative;
- `locals` and `lens`: a local to a `pmap` of path to value.

Taking a block's state as a child of its predecessor's copies these
roots, constant work. A write replaces a root. Forgetting a local
(`invalidate`) deletes its entry from `locals`, and from `lens` unless
lengths are kept. The meet is the per-kind intersection, with the
present rules unchanged:

| kind | the meet keeps a key with |
|---|---|
| `lt` | the smaller offset |
| `equiv`, `vn`, `lens` | the value, when both sides hold the same one |
| `locals` | the value, when both sides hold the same one; for a bound path (`ubPrefix`), the larger bound |
| `rep` | members in one class on both sides, in one class, its member of smallest ID as representative |

`locals` and `lens` meet at two levels. The outer meet keeps a local
present on both sides. For each such local, its two path maps meet by
the table's rule for that kind. A local whose path meet is empty is
dropped, which holds the same facts as keeping it with no paths.

The depth limit, flattening and layer chains are removed.

**R4 — Identical results.** For every function of every program, the
pass assigns exactly the proofs it assigns at 703fb12: the same checks
proven, with the same `ProvenBy`. Convergence is decided as before, by
comparing each loop header's entry state with the previous round's;
states are compared with R1's equality.

**R5 — Cost.** The pass's instructions per run are reported before and
after the change. The figure is (I_with − I_without) / 3000, where both
runs use the §4 harness with `GOGC=off`: I_with counts the 3,000 runs of
the pass, and I_without runs the same harness without the pass, so the
lowering and the harness are subtracted. Before and after are measured
on the same host, back to back. The report records the host, the Go
version and both commits. It is reported:
- on `examples/selfhost/lexer.kern`;
- on the 200-guard program.

`BenchmarkProveLexer` and `BenchmarkProveSequentialGuards` are also
reported, wall time with `-benchmem`. Against the gate, the report states
whether it is met. A miss is reported as a miss, not a failure of R1–R4.

## 3. Verification

- **`pmap` unit tests:**
  - insert, lookup, delete and size against a Go map, over random
    operation sequences, colliding hashes included;
  - persistence: an old map is unchanged after the operations that
    derive a new one;
  - intersection against a reference intersection of Go maps, with the
    shared-subtree case returning the same root;
  - equality.
- **The existing tests:** `compiler/opt` (including its proof tests) and
  `tests/prove_diff_test.go`, the interpreter-versus-native differential
  over generated programs.
- **The labels:** the `show-bounds-checks` output (both opt levels) of
  every program under `bench/kern`, `examples` and `tests` (`run`,
  `trap`, `positive`), compared byte for byte between 703fb12 and the
  change.
- **The full suite:** `go test ./...`.

## 4. Acceptance criteria

The change is accepted when all of the following hold:
- every test of §3 passes;
- the `show-bounds-checks` comparison of §3 shows no difference;
- `golangci-lint run ./...` reports no issue;
- the report gives R5's figures.

## 5. Measurement method

Instructions retired and cycles come from `/usr/bin/time -l` over a
scratch test that lowers one module and runs `Prove` on it 3,000 times,
under `GOGC=off`, so that collector work is excluded. The same test with
the pass skipped gives the baseline subtracted in R5. Re-running the
pass on an already-proven module repeats the same analysis; only the
labels of checks already proven are not re-assigned. Go's CPU profiler
cannot attribute the pass on macOS, so attribution, where needed, comes
from `sample <pid>`.
