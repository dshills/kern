# Allocation sites through library calls

**Date:** 2026-10-30. **Spec:** `specs/alloc-sites-std/SPEC.md`.
**Plan:** `specs/alloc-sites-std/PLAN.md`. **Found by:** the design of
the fourth agent suite (`docs/reports/agent-tasks-v4.md`, "Defects found
on the way").

## Before

`show-allocations` decided what an allocation site is from a fixed table
of core operations, and the table had drifted from the library in both
directions:

- **Missed allocations.** A call into library code that allocates was
  reported only when its name was in the table. `String.push_u64`,
  `Map.new`, `Map.insert`, `Vec.extend` and `Vec.resize` were not, so a
  program could allocate through them with no site reported, while
  `show-effects` correctly said the calling function allocates.
- **Phantom allocations.** Since the containers became kern code,
  `Vec.new`, `Pool.new` and `Arena.new` build an empty container and
  allocate nothing; their own effect summaries say so. The table still
  reported every call to them as an unconditional, `proven` allocation
  with a size. `Vec.with_capacity(n)` allocates only when `n` is above
  0, yet it was reported as unconditional.

## What changed

- **Library sites.** A call from user code to a library function whose
  effect summary allocates is now a site, labelled `unknown` with a
  `null` size: the callee may or may not allocate on a given call. The
  summary is the one `show-effects` computes, now factored out as
  `effectSummaries` and shared by both analyses, so the two can no longer
  disagree on a library call.
- **The table corrected.** `Vec.new`, `Pool.new` and `Arena.new` are no
  longer entries, and `Vec.with_capacity` is conditional (`unknown`,
  no size). `Box.new` stays `proven` with its size. The runtime
  intrinsics (`Shared.new`, `Mutex.new`, spawns, the `SystemAllocator`
  operations) and `clone` are unchanged. A library entry now yields a
  site only when its callee's summary allocates.
- **Drift is caught.** `TestAllocationTableEntriesAllocate` calls every
  library entry of the table and fails when one does not allocate. With
  `Vec.new` put back it fails naming `Vec.new[T=u64]`.
- **The §16 note** for `show-allocations` states the two kinds of site
  and the constructors that are not sites. `tools/agent-api/schema.md`
  is regenerated.

## Verification

- **§1's program.** `push_u64`, `Map.new`, `insert` and `extend` are
  `unknown` sites with no size. `String.new()` and `Vec.new()` are not
  sites. `Box.new` keeps its `proven` site of 8 bytes. A pure summing
  function has none. This holds in both opt levels.
- **A sweep over every run fixture** that checks, in debug and release
  (`TestAllocationSitesAgreeWithEffects`), confirms three things:
  - every site of the old table rule survives with equal fields, except
    those R1 removes or relabels;
  - every call from a user function to an allocating library function is
    a site;
  - every allocating user function has a site of its own, or an
    allocating user callee, a function value, an extern callee or an
    allocating destructor.

  Disabling the new rule makes the sweep report 125 missing sites.
- **Gates.** Lint reports 0 issues and `go test ./...` is green.
  Regeneration leaves no diff.

## Counts, before and after

Debug, `show-allocations <file>` run from the file's own directory, for
every `.kern` file under `bench/kern/` and `examples/`. The benchmark
report's recorded numbers change only when its harness next runs; no
timed run was made. Every relabel is a `Vec.with_capacity` call going
from `proven` with a size to `unknown` with none: 22 sites in all.

| program | before | after | no longer sites (allocate nothing) | new sites (library calls) |
|---|---|---|---|---|
| `bench/kern/arena_ast/arena_ast.kern` | 4 | 3 | `Arena[Expr].new()` | — |
| `bench/kern/btree/btree.kern` | 6 | 5 | `Pool[Node].new()` | — |
| `bench/kern/buffer_pool/buffer_pool.kern` | 6 | 6 | — | — |
| `bench/kern/buffer_pool/buffer_pool_fail.kern` | 6 | 6 | — | — |
| `bench/kern/hash_table/hash_table.kern` | 5 | 5 | three `Vec.new()` | three `resize(cap, 0)` |
| `bench/kern/intrusive_run_queue/intrusive_run_queue.kern` | 3 | 3 | — | — |
| `bench/kern/lock_free_queue/lock_free_queue.kern` | 4 | 4 | — | — |
| `bench/kern/lock_free_queue/lock_free_queue_fail.kern` | 4 | 4 | — | — |
| `bench/kern/lock_protected_map/lock_protected_map.kern` | 11 | 11 | three `Vec.new()` | three `resize(cap, 0)` |
| `bench/kern/packet_parser/packet_parser.kern` | 5 | 4 | `Vec[u8].new()` | — |
| `bench/kern/packet_parser/packet_parser_simd.kern` | 5 | 4 | `Vec[u8].new()` | — |
| `bench/kern/page_frame_allocator/page_frame_allocator.kern` | 2 | 2 | — | — |
| `bench/kern/page_frame_allocator/page_frame_allocator_fail.kern` | 2 | 2 | — | — |
| `bench/kern/ring_buffer/ring_buffer.kern` | 2 | 2 | — | — |
| `bench/kern/thread_pool/thread_pool.kern` | 12 | 12 | — | — |
| `bench/kern/vector/vector.kern` | 4 | 3 | `Vec[u64].new()` | — |
| `bench/kern/wal/wal.kern` | 3 | 4 | `Vec[u8].new()` | two `w.log.extend(..)` |
| `examples/arena_ast/ast.kern` | 4 | 4 | — | — |
| `examples/btree/btree.kern` | 0 | 0 | — | — |
| `examples/packet_parser/parser.kern` | 0 | 0 | — | — |
| `examples/selfhost/lexer.kern` | 2 | 1 | `Vec[Token].new()` | — |
| `examples/testing/app.kern` | 0 | 0 | — | — |
| `examples/testing/app_test.kern` | 0 | 0 | — | — |
| `examples/testing/stack/stack.kern` | 0 | 0 | — | — |
| `examples/thread_handoff/handoff.kern` | 6 | 6 | — | — |
| `examples/vector/vector.kern` | 5 | 4 | `Vec[u64].new()` | — |

One file is not in the table:
`examples/testing/stack/stack_test.kern` does not resolve on its own
(`NAME_UNRESOLVED`), being a test file of a module. Its program is
covered through `kern test`.

The hash table, the lock-protected map and the write-ahead log keep or
grow their counts. In each, an empty `Vec.new()` was reported where the
real allocation happens at a later `resize` or `extend`, which is now
reported instead.

## Agent suites

Spec R6 required:
- the dry run of every suite with allocation budgets to pass;
- a budget to be lowered where a start now meets it.

Only one budget changed:

| suite | task | budget before | budget after | why |
|---|---|---|---|---|
| first (`tasks-v1`) | t05_allocations | 2 | 0 | The start's count fell from 3 to 2 (its `Vec.new()` no longer counts), so it met the old budget. The reference measures 0; the prompt now asks for no allocation sites. |

In the second suite (`tasks-v2`) every start still fails and every
reference passes. The references of t05, t08 and t09 now measure 5, 2
and 6 against budgets of 7, 4 and 8, because each one's `Vec.new()` or
`Pool.new()` no longer counts. The budgets are left as they are: a
start still fails, and the recorded matrix of that suite was scored by
the old analysis and is not re-scored. The third and fourth suites set
no allocation budget, and their dry runs pass unchanged.

## Reviews

- **speccritic:** VALID_WITH_GAPS, no critical finding, gated twice
  (before and after the table correction).
- **plancritic:** EXECUTABLE_WITH_CLARIFICATIONS, score 86. Folded in.
- **prism:** no finding on the staged change.
- **realitycheck:** PARTIALLY_ALIGNED, score 100, no violations.
- **verifier:** name-matching recommendations only, such as "Add test for
  Allocations" and "Copies". `Allocations` is exercised by the three new
  tests, and the others predate this change.
