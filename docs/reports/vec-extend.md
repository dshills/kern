# `Vec.extend`: one block copy per record in the WAL

**Date:** 2026-10-27. **Builds on:** the containers rewrite
(`docs/reports/containers-in-kern.md`) and the throughput-gap pass
(`docs/reports/throughput-gaps.md`). **Starting point:** `bench/REPORT.md`
at `0193abc`: wal at 4.6 Mops/s against 7.3 for C and Rust (0.63), the
widest single-threaded gap left in the suite.

## Where the time went

`bench/kern/wal/wal.kern` appended a record with eighty `Vec.push` calls:
sixteen header bytes through `put_u64`/`put_u32`, then
`for b in payload { w.log.push(b) }` for the 64-byte payload. Every push
is a capacity test and a `size` store; the C version builds the header in
a `uint8_t hdr[16]` and does two `memcpy` calls, the Rust version four
`extend_from_slice` calls. A C variant that pushes bytes the kern way loses
30% (7.2 to 5.0 Mops/s), so the loop of pushes, not the containers, was the
gap.

## Changes

- **`ptr_store_seq[T](p: RawPtr[T], src: view[]T)`** joins the raw-pointer
  intrinsics in `std/core.kern` beside `ptr_view_seq`: it writes the
  `len(src)` elements of a sequence view into raw memory at `p`, each in its
  §4.1 layout. Natively it is one `llvm.memcpy` of `len(src) * size_of[T]`
  bytes from the view's pointer (`compiler/backend/llvm/calls.go`); in the
  interpreter it encodes the elements one by one
  (`compiler/interp/intrinsics.go`), because a view of an array local has no
  raw address there (the interpreter keeps locals in Go slots and only the
  containers' storage in its byte heap). That is why the intrinsic takes
  the view rather than yielding its pointer: a pointer-yielding form could
  not be implemented for every view the interpreter can hand out. Like
  every `ptr_*` function it is an unsafe operation
  (`UNSAFE_OP_OUTSIDE_UNSAFE` outside a block) and is listed as possibly
  trapping in `compiler/analysis/effects.go`; the obligation is that the
  destination is writable for `len(src)` elements and does not overlap the
  source.
- **`Vec.extend(edit self, src: view[]T)`** appends the elements of `src`:
  `total := self.size + len(src)` (checked, so the sum cannot wrap), one
  reservation through the new `grow_to(n)` (at least `n`, doubling when
  that is more, so a run of small extends stays amortized like a run of
  pushes), then `ptr_store_seq` at `data + size` and the `size` update in
  one contracted unsafe block (`docs/unsafe-audit.md`: 35 functions, 36
  blocks). The element type must be `Copy`, since the elements are
  duplicated bitwise. A method cannot constrain its owner's type parameter
  (the resolver rejects conditional implementations), so the resolver
  checks the requirement at the call (`compiler/resolve/calls.go`, next to
  the integer-only check of the wrapping forms) and reports
  `CONSTRAINT_UNSATISFIED` (rule T.4) for a non-`Copy` element:
  `tests/negative/vec_extend_copy.kern` and its twin. The spec names the
  operation in §4.1 and `docs/ownership.md` lists the intrinsic with the
  other raw-pointer operations.
- **`tests/run/vec_extend.kern`** runs in the interpreter and natively in
  both configurations: an empty source, growth across a reserve boundary
  (capacity 4 holding 3, extended by 6), a vector extended twice from a
  view of another and once from a sub-slice, bytes from an array local the
  way a header is built, and a `Copy` struct with padding.
- **wal** builds the header in a `[16]u8` local (`put_u64`/`put_u32` now
  write into it at an offset) and calls `extend` twice, for the header and
  the payload, the way C does `memcpy` twice. The checksum is unchanged
  (`2630590959368386693`, `bench/CHECKSUMS.md`); the emitted LLVM text of
  `append` carries the two `llvm.memcpy` calls.

## Result

Direct timings of the two release builds (`kern build --opt=release`,
the two binaries run alternately, three runs each; one-minute load 5.9 at
the start and end of the runs, above the usual 3.5 gate: the owner chose to
commit with these rather than wait out a day of Spotlight bursts). The
protocol line's `ns` is the timed section; throughput is ops / ns.

| build | runs (ms) | best (Mops/s) | p50 ns | p99 ns | checksum |
|---|---|---|---|---|---|
| before (`0193abc`) | 435.2, 431.9, 432.9 | 4.63 | 125 | 208 | 2630590959368386693 |
| after | 304.4, 305.0, 297.0 | 6.73 | 83 | 166–208 | 2630590959368386693 |

A 1.45× gain on the row that stood at 0.63 of C and Rust (7.3 Mops/s in
`bench/REPORT.md`); against that figure the ratio is 0.92, inside the 10%
target, though the C and Rust rows were not re-measured here. p50 moves
from 125 ns to 83 ns, the C and Rust value.

The full harness (`go run ./bench/harness -experiment`) was not run: the
host carried a load of 10–25 for the afternoon and two other sessions'
harness runs were queued on the same gate, so `bench/REPORT.md` is left as
it was at `0193abc`. The other twelve workloads do not call `extend`; the
only shared code they touch is `Vec.grow_to`, which `push` does not use.

## Verification

`go test ./...` and `golangci-lint run ./...` pass; the Prism review
(OpenAI gpt-6.1-sol, staged diff) returned no findings.

## Follow-ups

- A per-method constraint on an owner's type parameter (`func extend(...)
  where T: Copy`) would let the prelude state the requirement in the
  signature instead of the resolver's special case; it is a grammar and
  `spec/types.md` change.
- `hash_table`'s `new_map` and other byte-building sites in the benchmarks
  still push element by element where a view is at hand; `extend` applies
  wherever the source is already a sequence.

## Addendum (2026-10-28): the other byte-building sites, and an inliner fix

The follow-up above was tried on the two remaining benchmarks that fill a
`Vec` element by element.

- **lock_protected_map** now zero-fills its three tables with `resize`, as
  hash_table does, instead of three pushes per slot. The table doubles
  once inside the timed section (from 2^19 to 2^20 slots as the workers'
  inserts arrive), so the fill is on the measured path, though only once in
  four million operations.
- **packet_parser: no change.** Its 32-byte header copy is in `gen`, which
  runs before the clock starts. `buf.extend(h)` kept the checksum and left
  the timed code identical (same kern IR for `main`), yet measured about 2%
  slower in interleaved runs. The likeliest cause is the shifted placement
  of the functions after the smaller `gen`, not anything the change does,
  and with nothing to gain on the measured path the benchmark was left as
  it was.

**Inliner bug found on the way.** With the push loop gone, `new_map` became
small enough for the release inliner to splice into `main`, and the build
failed in clang (`use of undefined value '%v4'`). The inliner appended the
callee's blocks and the continuation (the rest of the call's block) to the
end of the function. The continuation held the definition of the constant
`K`, which the loop after it uses as its bound, and the LLVM backend records
a constant's literal only when it reaches the definition in block order, so
the loop, now emitted first, named a value that was never defined.
`compiler/opt/inline.go` now places each splice right after its call block,
which keeps every definition ahead of the blocks that follow it, while
call sites are still considered in the same order as before (original
blocks, then spliced ones) so no inlining decision changes.
`tests/run/inline_const_order.kern` reproduces the failure on the old
compiler and runs in the interpreter and both native configurations.

Direct timings (release, three interleaved runs each, one-minute load 8.2;
lock_protected_map is a four-thread row whose runs scatter widely at this
load):

| workload | before (ms) | after (ms) | best before → after (Mops/s) | p99 ns before → after | checksum |
|---|---|---|---|---|---|
| lock_protected_map | 184.8, 198.1, 251.1 | 172.7, 194.1, 191.2 | 21.6 → 23.2 | 22,042–23,542 → 17,916–20,750 | 1619761243042050329 |
| packet_parser (`extend` tried, not kept) | 74.0, 72.6, 73.4 | 74.9, 74.6, 74.9 | 137.7 → 134.1 | 42 → 42 | 9459311143157445824 |

The throughput gain on lock_protected_map is inside the row's scatter at
this load; the p99 drop, consistent across the runs, is the one doubling
inside the timed section now being a block store. wal is unchanged by the
inliner fix (same checksum, 7.1 Mops/s in one run). The full harness is
still owed on a quiet host.
