# Inliner: callees with loops, and a limit of 64

**Date:** 2026-10-28. **Follows:** `docs/reports/inliner-sizing.md` (callees
judged as lowered, the single-site rule) and `docs/reports/hash-table-gap.md`,
which left hash_table at the line of the throughput target (0.90 of C in the
2026-10-28 report) with `Map.probe` out of line in `insert`: LLVM's cost for
it is 250 against a threshold of 225, and the kern inliner skipped it twice
over, for its loop and for its size (61 against `MaxCalleeSize = 48`, two
call sites). C writes `static inline` and Rust `#[inline]`; kern has no
hint, and the report asked for a PLAN.md 6.2 rule instead.

## Change (`compiler/opt/inline.go`)

- A callee with a loop is judged like any other callee. The loop exclusion
  was a simplification of the first inliner, not a correctness condition:
  the rewrite splices the callee's blocks, back edges included, and the
  interpreter-against-native harness runs every fixture through the
  release build.
- `MaxCalleeSize` is 64, from 48: `probe` is 61. `sum16` (77) and `parse`
  (101) are where they were (a call, or a single-site copy).
- A callee with a loop is not copied into a call site that lies inside a
  loop of the caller (`onCycle`): the copy would nest its loop in the
  caller's, and measured on page_frame_allocator's `alloc_frame` that ran
  slower than clang's own inlining of the same function. A site outside
  any loop is where a copied loop pays, because the proof pass and clang
  then see the loop's bounds beside the code that feeds them.
- Tests: `TestInlineLoopCallee` (a looping callee at two straight-line
  sites is copied, and main has the loop), and a "loop into loop" case in
  `TestInlineStaysCall`. The size-limit tests are relative to the constant.
  PLAN.md 6.2 carries the amendment.

## Measured

Release builds of every workload with the inliner before and after,
interleaved, five runs each (seven for hash_table and
page_frame_allocator), one-minute load 4.3 to 6.8 (a busy host; the full
harness was not rerun). Checksums agree with `bench/CHECKSUMS.md` on all
thirteen.

| workload | before (Mops/s, median) | after | change | newly copied by the kern inliner |
|---|---|---|---|---|
| hash_table | 16.4 (C 18.8) | 17.4 | +6%, 0.87 → 0.93 of C | `Map.probe`, `shell_sort` |
| page_frame_allocator | 166.0 | 157.7 | −5% | `new_alloc`, `shell_sort` |
| vector | 1287 | 1296 | +1% | `sum`, `shell_sort` |
| btree | 4.1 | 4.2 | +2% | `insert`, `run`, `slot`, `shell_sort` |
| wal | 7.1 | 7.2 | +1% | `crc32`, `crc_init`, `get_*`, `put_*`, `replay`, `shell_sort` |
| packet_parser | 142.6 | 143.6 | +1% | `gen`, `shell_sort` |
| arena_ast, ring_buffer, buffer_pool, intrusive_run_queue, lock_free_queue, thread_pool | | | within ±1% | `shell_sort` and setup functions |
| lock_protected_map | 21.9 | 20.9 | −5%, inside the run-to-run range (18 to 31) | `Map.probe`, `shell_sort` |

`emit-llvm` of hash_table has no call to `Map.probe` after the change;
`insert` and `find` carry the copies and clang inlines them into `main`.

**page_frame_allocator.** The loss is not the loop rule: with `alloc_frame`
kept out of the loop by the nested-loop rule it remains (157 against 166),
and it is the same with `MaxCalleeSize` back at 48. What changed is
`new_alloc`, the setup function that builds the 8 KiB bitmap, now a copy in
`main`. After `clang -O2` the kern `main` has the same instruction count
(576 lines of assembly against 572), the same stack frame and the same
calls; the difference is register assignment in the hot loop (which value
clang keeps in a register and which it spills), a layout effect of the
larger function. The row stays above the lower end of its interval in the
2026-10-28 report (149.0 Mops/s), so the PLAN.md 6 gate holds; the next
quiet-host harness run records the row as it is.
