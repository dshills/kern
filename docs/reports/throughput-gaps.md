# Closing the throughput gaps

**Date:** 2026-10-27. **Builds on:** the containers rewrite
(`docs/reports/containers-in-kern.md`) and the Pool growth fix
(`docs/reports/btree-rss.md`). **Starting point:** `bench/REPORT.md` at
`a524d9a`: three of thirteen workloads within 10% of the best of C and Rust,
four between 0.72× and 0.87× (vector, hash_table, btree,
page_frame_allocator).

## Method

Each of the four was built twice with `kern build --opt=release`, once as
is and once with `--unsafe-elide-checks`, and the two were timed on the
same host (best of three). The difference bounds what the runtime checks
cost; the rest of the gap is elsewhere. The LLVM text of each hot loop was
then read after `clang -O2`.

| workload | checks on (Mops/s) | checks elided | best of C/Rust | where the gap was |
|---|---|---|---|---|
| vector | 916 | 1086 | 1122 (C) | the `sum` pass: a checked `+` per element blocks vectorisation; the push loop's checks were already folded by LLVM |
| hash_table | 16.2 | 17.1 | 20.1 (C) | `new_map` pushing three zero vectors slot by slot on every doubling (about ten pushes per insert over the run) where C uses `memset` and Rust `vec![0; cap]` |
| btree | 4.5 | 4.5 | 5.8 (Rust) | not the checks: handle size (`PoolId` is 12 bytes against a `u32` index, so a node is 440 bytes against 304) and the separate slot-metadata array |
| page_frame_allocator | 149.7 | 152.0 | 162.9 (Rust) | `ctz64` written as six compare-and-shift steps where C and Rust use the processor's count-trailing-zeros |

## Changes

- **Bit-count intrinsics.** `count_trailing_zeros`, `count_leading_zeros`
  and `count_ones` join the prelude (`std/core.kern`, spec §4.1) as unary
  operations on any integer type returning `u32`: `ctz`/`clz`/`popcount` in
  the IR, `llvm.cttz`/`llvm.ctlz`/`llvm.ctpop` natively (zero is not poison,
  so the count of a zero operand is the width), `math/bits` in the
  interpreter. `tests/run/bitcount.kern` runs both engines over several
  widths and the zero operand. The page-frame allocator benchmark uses
  `count_trailing_zeros` in place of its hand-written `ctz64`, as its C and
  Rust versions already used the builtin.
- **`Vec.resize(n, x)`.** Sets the length to `n`: elements past `n` are
  destroyed last to first, new elements are `copy(x)` written over a local
  index so LLVM turns the fill into a block store, and `x` is destroyed on
  return (two contracted unsafe blocks; `docs/unsafe-audit.md`,
  `tests/run/vec_resize.kern`). The hash table benchmark builds each table
  with `resize` instead of a push loop, matching `vec![0; cap]`.
- **Wrapping sum in the vector benchmark.** `sum` now accumulates with
  `wrapping_add`, as the C (`total += items[i]`) and Rust
  (`fold(0, wrapping_add)`) versions do and as the wrapping checksum it feeds
  implies; the kern version alone had been paying for an overflow check per
  element that also prevented vectorisation.
- **btree: no change.** Rebinding the node once per level the way the C
  version does (one `with` over `t.nodes.get(x)` instead of five to seven
  projections) measured the same 3.9 Mops/s under the same load: LLVM had
  already merged the repeated handle checks. What remains is the design's:
  generational handles are three words, and every access touches the slot
  metadata as well as the node.

## Result

Single-binary timings on the development host (best of three, load 5–7):

| workload | before (Mops/s) | after | change |
|---|---|---|---|
| vector | 896 | 1053 | +17% |
| hash_table | 15.9 | 17.7 | +11% |
| page_frame_allocator | 147.4 | 157.4 | +7% |

Full harness run (`go run ./bench/harness`, five runs per row, 95%
intervals): see the table below and `bench/REPORT.md`.

| workload | before (Mops/s) | after | best (lang) | ratio before → after | throughput target |
|---|---|---|---|---|---|
| vector | 806.4 | 955.8 | 1138.1 (c) | 0.72 → 0.84 | no |
| hash_table | 15.3 | 17.2 | 20.4 (c) | 0.76 → 0.85 | no |
| btree | 4.4 | 4.1 | 5.8 (c) | 0.75 → 0.69 | no (unchanged code; within the row's interval) |
| page_frame_allocator | 141.3 | 152.4 | 167.4 (rust) | 0.87 → 0.91 | **now meets** |

Load average at start 2.95 (the previous report's 4.92). The other nine rows
are unchanged code; buffer_pool read 0.87 against 0.92 (it has sat at the
edge of the target, 0.84–0.93, across every run), and the multi-threaded
lock_protected_map and lock_free_queue rows fell for C and kern alike, so
the count of workloads meeting the throughput target stays at three
(ring_buffer, intrusive_run_queue, page_frame_allocator) and RSS at eleven.

Checksums are unchanged on every workload. `go test ./...` and
`golangci-lint run ./...` pass. The Prism review (OpenAI gpt-6.1-sol, staged
diff) returned one medium finding, addressed: the bit-count intrinsics
accepted any `Copy` type, so the resolver now applies the wrapping forms'
integer-only check to them (`tests/negative/bitcount_type.kern` and its
twin).

## Follow-ups

- The B-tree gap is the handle representation, not the compiler: a
  `PoolId` of two words (index and generation, with the identity tag
  recovered from the pool) would shrink a node by 64 bytes and is a spec
  §5.6 change, not a library one.
- `Vec.push` keeps `size` and `cap` in memory across the loop because the
  whole `Vec` is passed to the out-of-line `grow`; passing the three fields
  separately, or letting the backend mark `grow` as not reading the caller's
  stack, would remove two stores per push.
