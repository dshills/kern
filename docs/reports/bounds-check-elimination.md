# Bounds checks: what survives, and what the checks really cost

**Date:** 2026-10-27. **Builds on:** `docs/reports/throughput-gaps.md` and
`docs/reports/inline-atomics.md`. **Starting point:** `bench/REPORT.md` at
`b1fef21`: packet_parser at 0.20 of Rust, the worst row, with the §9
separation measuring its runtime checks at 195% of its time (32 Mops/s with
checks, 95 without); vector at 0.84 of C with 17%.

## Diagnosis

The release build of `bench/kern/packet_parser/packet_parser.kern` was read
after `clang -O2` (the `--emit-llvm` text optimised again with
`-S -emit-llvm`, and clang's `-Rpass=inline -Rpass-missed=inline` remarks).
Three things were true, none of them a bounds check that LLVM could not
fold:

1. **The checks were blocking inlining.** Every check ended in its own
   block with a `call @kern_trap`, and LLVM's inline cost model charges a
   call penalty for each of them. `rd32`, four byte reads with four checks,
   came out at exactly cost 225 against the threshold of 225 and stayed a
   call; `sum16` at 235-245 likewise; `parse` at 465. With
   `--unsafe-elide-checks` the same functions cost 40-90 and were inlined,
   which is where most of the "cost of checks" came from: not the compares,
   but the call and the lost optimisation around it.
2. **LLVM already folds the dominated checks once it can see them.** After
   inlining `rd16` into `sum16`, the loop guard `i + 1 < n` eliminates both
   byte reads' checks and the `i + 2` overflow check (ConstraintElimination
   and GVN, since the index arithmetic carries `nuw` from the overflow
   checks). In the optimised text of `sum16`, one check survives per 16-bit
   word: the overflow check on the 32-bit accumulator `s + word`.
3. **That one check is a semantic mismatch, not a provable fact.** No
   analysis can bound the sum of an unknown-length input. The C version
   accumulates into a `uint32_t`, which wraps, and the Rust version's
   `s += ... as u32` wraps in release builds; the kern version alone trapped
   on a sum that cannot occur (a packet holds at most 88 words). It is the
   same situation the vector benchmark's `sum` was in
   (`docs/reports/throughput-gaps.md`).

## Changes

- **One trap block per function** (`compiler/backend/llvm/emit.go`). A check
  is now `icmp` + `br` to the function's single landing block, which
  receives the trap kind and span through `phi` nodes and makes the one
  `call @kern_trap`. The trap records are byte-for-byte what they were
  (every `tests/trap/` fixture is unchanged), and LLVM's inliner sees one
  call per function instead of one per check: `rd32` costs 90, `sum16`
  145, both inlined; `rd16` 40-45.
- **Proven checks are left out natively** (`compiler/backend/llvm/emit.go`).
  The backend had emitted every `check_bounds`, including the `for` loop's
  `loop_bound` ones the lowering has marked proven since Phase 1. A check
  whose provenance is `proven` is now skipped in native code; the
  interpreter still executes it, so a wrong proof shows up as a
  disagreement in the differential harness rather than as silent memory
  unsafety.
- **A proof pass in the lowering** (`compiler/lower/prove.go`, run on every
  lowered function). It records the two remaining proof facts of spec §7
  invariant 3 and ADR-008 invariant 4:
  `dominated_by_check` when an index is covered by an earlier check, or by
  a branch condition, on the same length along a single-predecessor chain
  (an index that is a checked addition of a constant is read as base plus
  constant, so `i + 1 < n` covers `i` and `i + 1`; a local stored once is
  the same value wherever it is read; an access value's length never
  changes), and `constant` for a literal index into an array of known
  length. Everything else stays `runtime-checked`: a block with several
  predecessors starts with no facts, a wrapping addition proves nothing, a
  value loaded through an access is opaque, and a call or a store through
  an edit access forgets every local an edit access was opened on.
  `compiler/lower/prove_test.go` pins what is and is not proven on seven
  shapes, including ADR-008's `data[i + stride]`; `tests/trap/bounds_guard.kern`
  and its twin run a guarded loop in both engines and trap on the one
  access the guard does not cover. `show-bounds-checks` now reports all
  three `proven_by` facts; on the packet parser: 2 `constant`,
  1 `dominated_by_check`, 1 `loop_bound`, 15 runtime-checked.
- **The parser's accumulator wraps** (`bench/kern/packet_parser/*.kern`):
  `sum16` adds with `wrapping_add`, as C and Rust do, with the bound stated
  in its comment. Checksums are unchanged.

What this did not do: emit a single hoisted check for a group of indexes
`at .. at + 3`. With inlining restored, LLVM folds those groups wherever a
guard dominates them, and where none does (the header reads in `parse`)
the four compares are a few cycles against a call that is now gone.

## Results

Stand-alone, best of three on the development host:

| build | before (Mops/s) | after |
|---|---|---|
| packet_parser, checks on | 32.1 | 93.7 |
| packet_parser, `--unsafe-elide-checks` | 98.6 | 98.6 |

Full harness run (`go run ./bench/harness -experiment`, five runs per row,
load average at start 3.04; `bench/REPORT.md`). Checksums agree on all
thirteen workloads. The before column is the report committed with
`b1fef21`; the cost-of-checks row for that baseline is the §9 separation in
`docs/reports/throughput-gaps.md`, the last report that carried it.

| workload | before (Mops/s) | after | best (lang) | ratio before → after | cost of checks before → after |
|---|---|---|---|---|---|
| packet_parser | 31.9 | 89.6 | 152.2 (rust) | 0.20 → 0.59 | 195% → 8% |
| vector | 942.3 | 918.2 | 1082.3 (c) | 0.81 → 0.85 | 17% → 1% |
| hash_table | 17.0 | 17.9 | 20.4 (c) | 0.86 → 0.87 | 2% → 3% |

The other rows are unchanged code and read within their usual ranges;
lock_free_queue stands at 0.82 and page_frame_allocator at 0.88 (its 0.84 to
0.92 band), so the count of workloads meeting the throughput target stays at
three (ring_buffer, buffer_pool, intrusive_run_queue) and RSS at eleven.
The "cost of checks" of vector and packet_parser is now what the compares
cost: the rest of their old cost was the inlining the trap calls blocked.

`go test ./...` and `golangci-lint run ./...` pass. Prism (OpenAI
gpt-6.1-sol, staged diff) returned no findings.

## Follow-ups

- `parse` (cost 785) is still a call per packet and returns a
  `Result[Header, ParseErr]` by value; the rest of the gap to Rust (which
  inlines everything) is there and in the sampler, not in the checks.
- The proof pass stops at a call: the checks inside a helper such as `rd16`
  are proven only by LLVM after inlining. A kern-level inliner for small
  leaf functions would let the lowering prove them and `show-bounds-checks`
  report them.
