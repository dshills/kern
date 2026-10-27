# Where the three largest throughput gaps are

**Date:** 2026-10-27. **Starting point:** `bench/REPORT.md` at `2fbc88a`
(the inliner wired): arena_ast at 0.57 of the best of C and Rust,
packet_parser at 0.59, wal at 0.63. The btree gap (0.69) is the handle
representation (`docs/reports/throughput-gaps.md`) and is not re-read here.
No compiler or workload code was changed for this report.

## Method

Each workload was built with `kern build --opt=release`, once as is and
once with `--unsafe-elide-checks`, and timed against the C (`clang -O2`)
and Rust (`cargo build --release`) versions, best of three with the four
binaries interleaved, one-minute load 3.3 to 3.9 throughout. The LLVM
text `--emit-llvm` wrote was read after `clang -O2 -S -emit-llvm`, beside
the same output for the C source and `--emit=llvm-ir` for Rust. The
remaining runtime checks come from `show-bounds-checks --opt=release`,
copies and allocations from `show-copies` and `show-allocations`.

Experiments were run on the emitted LLVM text, not the compiler: a
function marked `alwaysinline`, or every non-entry function given
`internal` linkage, then linked with `runtime/hosted/src/kern_rt.c` exactly
as the harness does. For wal, the C version was also rewritten to push
bytes one at a time the way the kern version does.

| workload | kern (Mops/s) | checks elided | C | Rust | kern / best |
|---|---|---|---|---|---|
| arena_ast | 79.0 | 78.8 | 118.4 | 139.7 | 0.57 |
| packet_parser | 95.7 | 95.8 | 136.4 | 163.5 | 0.59 |
| wal | 4.71 | 4.74 | 7.45 | 7.56 | 0.62 |

**The runtime checks cost nothing measurable in any of the three.** Every
elided build runs at the speed of the checked one. The gaps are code
shape, and each workload has a different one.

## arena_ast: `alloc` stays out of line

The sampled operation is `alloc` (sampler start, `Arena.alloc`, sampler
stop, `Result` return). After `clang -O2` it is a separate 124-instruction
function called from the three sites in `build`; `build` and `eval` are
recursive. C's clang also keeps `alloc`, `build` and `eval` out of line,
but C's `alloc` is 68 instructions.

Why neither inliner takes it:

- The kern inliner works callees first. It inlined `Sampler.start` and
  `Sampler.stop` into `alloc`, which took `alloc` from about 40 to 95
  (80 instructions plus 15 blocks) in the IR, past `MaxCalleeSize = 48`,
  before `build` was considered. The order defeats the limit: a small
  function grows past it by absorbing its own callees.
- LLVM's inliner does not take a 124-instruction external function with
  three call sites. Giving every function `internal` linkage changes
  nothing here (76.5 against 76.1 Mops/s).

What inlining it is worth: with `alloc` marked `alwaysinline` in the LLVM
text, the same build goes from 73.9 to 105.5 Mops/s (+43%), from 0.57 to
0.76 of Rust and 0.89 of C. Eliding checks on top of that adds nothing
(97 against 101, noise).

What is left after that, in order of size:

- **Ten stores per node.** `Expr` is laid out as `{ i8, [7 x i8],
  [2 x i64] }` (`compiler/backend/llvm/types.go`, the union case): the
  padding is an array of seven bytes, so storing the aggregate becomes a
  tag store, seven byte stores for the padding and two word stores. C
  stores the 24-byte node in three.
- **Node size.** `ArenaId` is `{ index: u32, phantom_tag: u32 }`, so an
  `Add` holds 16 bytes of handles and the node is 24 bytes, the same as
  C's; Rust's `Expr` is 16 bytes (two `u32` indices), and a 16 MB tree
  against 24 MB is most of Rust's lead over C in `eval`. This is the
  spec §5.6 question the btree report raised, not a compiler change.
- `Result[ArenaId, AllocErr]` is returned by value and matched on at every
  site; small.

The 17 reported copies are 8-byte `ArenaId` handles and the 24-byte
`Expr` passed to `alloc`, all bitwise, the same copies C makes. The 25
runtime overflow checks (`eval`'s `+` and `*`, the loop arithmetic) are
free, per the elided build.

## packet_parser: `parse` stays out of line, then byte loads

`handle` and `payload` inline into `main` and the payload `sum16` is
vectorised there (`llvm.vector.reduce.add`), but `parse` is a separate
225-instruction function returning a 36-byte `Result[Header, ParseErr]`
by value, called once per packet. The kern inliner skips it by size
(482 in the IR, with `sum16` inlined for the header checksum); LLVM
skips it because it is an external function near its cost limit. C's
clang inlines its `parse` into `main` (672 instructions, no calls).

Two experiments on the text, from the same build:

| variant | Mops/s |
|---|---|
| as emitted | 94 to 96 |
| `parse` marked `alwaysinline` | 117 |
| every non-entry function `internal` | 117 |
| `internal`, checks elided | 113 (noise) |

**Internal linkage alone recovers the whole inlining gain**: once LLVM
knows nothing else can call `parse`, `gen` and `sum16`, it inlines all
three into `main` (833 instructions) and drops the copies. The backend
already emits library instantiations as `internal` (the `linkage` value
in the `define` emission, `compiler/backend/llvm/emit.go`) for exactly
this reason; user functions other than the entry get no linkage, and the
change is to give them the same.

What is left after that (117 against C's 136 and Rust's 163):

- **Byte loads instead of byte-swapped word loads.** `rd32` is four byte
  loads, three shifts and three ors; LLVM recognises only the magic read
  at offset 0 as a single `i32` load. C reads the same way and gets no
  `bswap` either; Rust's `u32::from_be_bytes` compiles to one load and a
  `bswap` (26 of them in its text), and that is Rust's lead over C. The
  kern answer is a library or intrinsic read of a big-endian word from a
  view, or a backend pattern for the shift-or of adjacent byte loads.
- **Branch count.** The inlined kern `main` has 80 branches and 57
  compares against C's 44 and 33, with 12 trap sites: the slice
  constructions `data[off : total]` and `pkt[32 : 32 + len]` (the second
  is runtime-checked because its guard sits in `parse`, a different
  function until it is inlined), the `u32` to `u16` conversion check at
  the end of `sum16` that the fold loop makes impossible, and the loop
  arithmetic. The elided build says they cost about 2%; what they cost
  in code shape (the vectoriser's view of the loops) is inside that.

The ten remaining runtime bounds checks are the two `rd16` reads in the
`sum16` loop (an induction variable the proof pass does not range), the
`wr16` stores in `gen` (untimed), the two slice constructions above and
the `offsets` reads in `main`.

## wal: eighty pushes per record

`append` writes a record as 16 header bytes through `put_u64` and
`put_u32` and 64 payload bytes through `for b in payload { w.log.push(b) }`,
eighty `Vec.push` calls. Each is inlined to: load `size`, load `cap`,
compare, load `data`, store the byte, add, store `size`, with the `grow`
call keeping `size` and `cap` in memory between pushes (the `put_u64`
body after `clang -O2` is this sequence eight times). C does two
`memcpy` calls behind one capacity test; Rust does four
`extend_from_slice`.

| variant | Mops/s |
|---|---|
| kern as emitted | 4.4 to 4.7 |
| `put_u64`, `put_u32`, `append` marked `alwaysinline` | 4.4 |
| every non-entry function `internal` | 4.5 |
| C as written (`memcpy`) | 7.20 |
| C rewritten to push one byte at a time | 5.03 |

**Calls are not the cost; the per-byte push is.** The C version loses
30% when it pushes bytes the way the kern version does, and lands within
10% of kern. The remaining difference is the payload generation loop
(`payload[j * 8 + k] = ...`, 64 checked byte stores where C stores eight
words) and `get_u64`/`get_u32` in `replay`, which LLVM does not combine
into word loads either.

The six runtime bounds checks are the CRC table index (masked with 255,
which the proof pass does not use), the byte reads in `get_u64` and
`get_u32` (a decrementing index), the replay sub-slice, and the payload
store; all free per the elided build. `crc32` itself is inlined and
matches the C loop instruction for instruction.

The kern answer is a library operation: `Vec.extend(view[]T)` (one
capacity test, then `ptr_copy`), and the header written into a `[16]u8`
local first. The prelude has `ptr_copy` but no way to take a `RawPtr`
from a view, so the operation needs an intrinsic for that, used once in
`core.kern` inside a contracted unsafe block, like `resize`.

## What would close each gap

Ranked by expected gain per change, with the evidence above:

1. **Internal linkage for non-exported functions** (backend, one change).
   packet_parser 96 → 117 Mops/s measured, 0.59 → 0.72 of Rust and 0.86
   of C. No effect on arena_ast or wal, no risk, and it removes the
   retained copies from every binary.
2. **Inliner sizing that survives the callee's own inlining** (the kern
   inliner): size a callee by its pre-inlining size, or raise the limit
   for callees with few call sites. arena_ast 74 → 105 measured with
   `alloc` forced inline, 0.57 → 0.76 of Rust. `parse` (482 in the IR)
   would need the second rule; the first change already covers it.
3. **`Vec.extend(view[]T)` in the prelude**, with the wal workload using
   it as Rust uses `extend_from_slice` and C uses `memcpy`. The C
   experiment bounds the gain at about 30% of C's time: wal 4.7 → roughly
   6.5 of 7.5, 0.62 → about 0.87.
4. **Union layout without a byte-array padding slot** (backend): fold
   tag and padding into one integer of the payload's alignment so an
   `Expr` store is three stores, not ten. Predicted here as a few percent
   on arena_ast's allocation half; measured below at 34%, because the
   aggregate also crosses the call to `alloc` by value, as ten scalars
   under the old shape and three words under the new.
5. **Big-endian word reads** as a library or intrinsic operation, or a
   backend pattern for shift-or of adjacent byte loads: Rust's lead over C
   on the parser (163 against 136) is this alone.
6. **Handle size** (spec §5.6): `ArenaId` at four bytes in release would
   bring the arena_ast node to Rust's 16 bytes; the same decision as
   btree's `PoolId`.

Not worth doing on this evidence: bounds or overflow check elimination
(no measurable cost in any of the three), and forcing `put_u64`-style
helpers inline (no effect).

## Applied with this report: internal linkage

The first change is in this commit: the backend now emits a program's own
functions with `internal` linkage, as it already did library
instantiations, except a function exported under a C name (`f.Extern`)
and the entry wrapper. `TestEmitShapes` keeps the exported case external.
`go test ./...`, `golangci-lint run ./...` and the Prism review (OpenAI
gpt-6.1-sol, staged diff) pass.

Full harness run (`go run ./bench/harness -experiment`, five runs per row,
95% intervals, load 3.19 at start with the five-minute average still at
7.9 from Spotlight), kern release rows against the previous report's:

| workload | before (Mops/s) | after | best (lang) | ratio before → after |
|---|---|---|---|---|
| packet_parser | 93.4 ± 1.6 | 114.9 ± 9.4 | 160.9 (rust) | 0.59 → 0.71 |
| arena_ast | 75.0 ± 3.9 | 65.8 ± 17.3 | 105.9 (rust) | 0.57 → 0.62 |
| wal | 4.6 ± 0.0 | 4.9 ± 0.1 | 7.8 (rust) | 0.63 → 0.63 |
| lock_protected_map | 18.8 ± 2.0 | 16.4 ± 1.1 | 26.3 (c) | 0.68 → 0.62 |
| lock_free_queue | 725.7 ± 4.1 | 610.8 ± 143.4 | 795.7 (c) | 0.84 → 0.77 |

The parser gained the 23% the experiment predicted. Checksums agree on
every workload and five workloads meet the throughput target, as before.
Three rows read below the lower end of their previous interval
(arena_ast, lock_protected_map, lock_free_queue), and the change does
alter LLVM's inlining in all three (`run` folds into `main`, `push` into
`consumer`), so they were checked directly: binaries from the previous
compiler and this one, run five times each back to back on the same host,
agree at the median within 2% for arena_ast (75.7 against 75.2) and
lock_protected_map (15.2 against 14.9), and scatter between 500 and 730
for both on lock_free_queue. The three readings are the host's;
`bench/REPORT.md` is the run as measured.

## Applied next: unions as words of their alignment

The fourth change followed in the next commit. A union's LLVM type was
`{ tag, [pad x i8], [n x iUnit] }`; it is now `{ [Size/unit x iUnit] }`
with `unit` the alignment in bits (capped at 64), the same size and
alignment, `compiler/backend/llvm/types.go`. Nothing reads the shape:
the tag and every payload field are addressed by byte offset, so only
the aggregate value changes, and `Expr` goes from `{ i8, [7 x i8],
[2 x i64] }` to `{ [3 x i64] }`, `Result[ArenaId, AllocErr]` from
`{ i8, [3 x i8], [2 x i32] }` to `{ [3 x i32] }`.

In `arena_ast_bench.alloc` after `clang -O2` the stores go from 16 to 9,
and the `Expr` argument crosses the call as three words instead of ten
scalars, with the `Result` returned as `[3 x i32]`. `alloc` is still out
of line (103 instructions). Timed on a busy host (one-minute load 4 to
5, five runs each, the two compilers' binaries interleaved, best three
of five):

| workload | before (Mops/s) | after |
|---|---|---|
| arena_ast | 77.6, 78.4, 78.8 | 104.2, 105.1, 105.2 |
| packet_parser | 116.3, 120.3 | 115.4, 117.1, 117.4 |
| wal | 4.7 | 4.63, 4.67, 4.70 |

arena_ast gains 34%: against the quiet-host Rust figure in the method
table (139.7), that is 0.57 to 0.75. The parser and wal, whose unions
are already small, do not move.
Checksums agree on all three, and `go test ./...`, `golangci-lint run
./...` and the Prism review pass. The full harness was not run for this
change: the host was busy all evening (Spotlight), and the two
side-session runs were queued ahead of it.
