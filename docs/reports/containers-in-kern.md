# Library containers in kern

Date: 2026-10-27. Branch `kern-containers`. Closes the deviation "Library
containers are still C" carried from Phase 2 through Phase 3
(docs/reports/phase-3.md) and PLAN.md 2.3's kern rewrite of the containers.

## What changed

`Box`, `Vec`, `Pool` and `Arena` (and the handles `PoolId`, `ArenaId`) are kern
code in `std/core.kern` over the raw-pointer core: structs of `RawPtr` fields
whose raw operations sit in 33 small contracted `unsafe` blocks
(docs/unsafe-audit.md). The corresponding C (`kern_box_*`, `kern_vec_*`,
`kern_pool_*`, `kern_arena_*`, about 190 lines) is deleted from the hosted
runtime, which keeps traps, output, threads, `Shared`/`Mutex`/`Atomic` and the
system allocator. Every container operation is an ordinary kern function that
the backend compiles with the program and LLVM inlines; growth and allocation
paths (`grow`, `reserve`, the `heap_*` helpers) are emitted `noinline cold` so
the hot operations stay small enough to inline into their callers.

Supporting changes:

- Prelude intrinsics: `size_of`/`align_of`; `ptr_view`, `ptr_edit`,
  `ptr_view_seq`, `ptr_edit_seq`, `ptr_edit2` (an access on raw memory,
  returnable only from `unsafe` under the function's contract, Rule 6.6);
  `trap_kind(kind)`; `container_identity()`; `SystemAllocator.realloc`
  (vectors and arenas grow in place as the C `realloc` did, which keeps peak
  RSS where it was); `testing.require_hooks()`.
- Prelude bodies are resolved, checked and lowered per instantiation like
  user code (`ir.Func.Library`). A trap inside the library reports the span of
  the user-level call that entered it: library functions take two hidden span
  arguments natively and the interpreter's frame carries the span, so every
  trap fixture under `tests/trap/` reports the same kind and span as before.
- The interpreter runs the same kern code over its byte-addressed heap
  (`rawRef`/`rawSeq` places), so interpreter and native executions compare the
  containers' behaviour directly; `tests/run/containers.kern` covers nested
  vectors, pool and arena elements with destructors, `Box.into`, `copy` of a
  vector, `pop` and `clear`.
- Fields of a nocopy core struct are private outside the prelude; the
  containers' `Send`/`Share`/`Interior` markers derive from the element type.
  Generic types' destructors resolve their implicit type parameters
  (`impl Box[T]`), and destructor instantiation stops after eight growing
  steps of an expanding recursive instantiation (the bound marker derivation
  uses), trapping `unreachable` beyond it instead of expanding forever.
- `copy(v)` of a `Vec` or `Box` calls their kern `clone`, which deep-copies
  the elements (the C `kern_vec_clone` copied element bytes).
- `kern_sysalloc_alloc` consumes the `fail_next_alloc` hook (so `oom` traps
  come from the allocator) and rounds alignments below `sizeof(void *)` up,
  which macOS's `aligned_alloc` requires. `Vec.push` with spare capacity no
  longer traps when the hook is armed; only a real allocation does.
- Machine API: `show-allocations`, `show-copies` and `show-bounds-checks`
  report the user-level call sites of the library operations as before and
  leave the library's own bodies out.

## Benchmarks

`go run ./bench/harness -experiment`, five runs per row, release rows against
the better of C and Rust (bench/REPORT.md; the previous report was generated
2026-10-26 on the same host). Checksums are unchanged on every workload.

| workload | before (Mops/s) | after | change | best (lang) | ratio before → after | throughput target | RSS ratio before → after |
|---|---|---|---|---|---|---|---|
| vector | 293.2 | 839.8 | +186% | 1178.9 (c) | 0.26 → 0.71 | no | 1.00 → 1.00 |
| hash_table | 8.3 | 15.9 | +92% | 20.5 (c) | 0.46 → 0.78 | no | 1.06 → 1.06 |
| btree | 2.2 | 4.1 | +86% | 5.9 (c) | 0.56 → 0.71 | no | 3.21 → 3.21 |
| arena_ast | 64.8 | 76.6 | +18% | 138.5 (rust) | 0.59 → 0.55 | no | 1.37 → 1.37 |
| ring_buffer | 934.0 | 970.3 | +4% | 954.3 (rust) | 1.01 → 1.02 | meets | 1.03 → 1.04 |
| wal | 2.8 | 4.9 | +75% | 7.9 (c) | 0.39 → 0.61 | no | 1.00 → 1.00 |
| packet_parser | 31.3 | 32.5 | +4% | 160.2 (rust) | 0.21 → 0.20 | no | 1.05 → 1.05 |
| buffer_pool | 15.5 | 30.1 | +94% | 32.3 (rust) | 0.50 → 0.93 | **now meets** | 1.01 → 1.02 |
| page_frame_allocator | 143.3 | 144.0 | +0% | 176.0 (rust) | 0.88 → 0.82 | no | 1.06 → 1.07 |
| intrusive_run_queue | 66.2 | 71.2 | +8% | 70.8 (rust) | 1.01 → 1.01 | meets | 1.05 → 1.05 |
| thread_pool | 17.9 | 19.4 | +8% | 27.9 (c) | 0.62 → 0.69 | no | 1.03 → 1.02 |
| lock_protected_map | 8.3 | 15.9 | +92% | 28.8 (c) | 0.52 → 0.55 | no | 1.06 → 1.06 |
| lock_free_queue | 272.2 | 266.8 | -2% | 780.8 (c) | 0.44 → 0.34 | no | 1.02 → 1.04 |

Throughput targets met: 2 workloads before, 3 after; RSS targets met: 11 before and after; checksums identical on all 13.

*Update 2026-10-27, after merging the freestanding allocator and the Pool
realloc growth fix (`docs/reports/btree-rss.md`):* a full `go run ./bench/harness`
on `main` (load average 4.9 at start) confirms the three throughput targets
(buffer_pool 0.92, ring_buffer 1.00, intrusive_run_queue 1.03) and eleven RSS
targets; btree peak RSS is now 1.44× C instead of 3.21×, and lock_protected_map
reads 0.61 against 0.55 here. Checksums agree on all thirteen.

The container workloads moved as the Phase 3 report predicted: vector runs
2.9× faster, hash table, B-tree, WAL, buffer pool and lock-protected map
1.8–1.9× faster; buffer_pool joins ring_buffer and intrusive_run_queue in
meeting the 10% throughput target.
page_frame_allocator sits at the edge of the target (0.84–0.88 across runs) as
before. The remaining gap on vector, hash_table and btree is no longer the
container calls: with `Vec.push` inlined, the vector loop is the benchmark's
sampler and the checked arithmetic (§9 separations: checks elided), and the
hash table and B-tree spend their time in hashing and the split logic. Peak RSS
is unchanged on every workload (realloc growth).

## Deviations and follow-ups

- Freestanding programs have the containers natively since 2026-10-27: the
  freestanding runtime serves `SystemAllocator` from a static 1 MiB arena
  (Rule 6.8, ADR-011 amendment), and the interpreter's freestanding emulation
  uses its raw heap as hosted mode does (`tests/freestanding/containers.kern`).
- `release`, `grow`, `reserve`, `check` and `seed_generation` are visible like
  every core declaration; they keep the container valid (`release` leaves an
  empty container with a fresh identity; `seed_generation` is gated by
  `testing.require_hooks`).
- realitycheck marks PLAN-054 (this rewrite) UNCLEAR from the inventory alone;
  this note and docs/unsafe-audit.md are its record.
