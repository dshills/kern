# Byte-order reads: `load_be` and `load_le`

**Date:** 2026-10-28. **Starting point:** `docs/reports/gap-diagnosis.md`
item 5: the packet parser reads header fields as four byte loads, three
shifts and three ors, which neither kern nor C turn into a single
byte-swapped load, while Rust's `u32::from_be_bytes` does, and that was
Rust's whole lead over C on the workload (163 against 136 Mops/s).

## What changed

Two prelude intrinsics (`compiler/resolve/prelude/core.kern`, spec §4.1):

```
func load_be[T: Copy](p: view[]u8, at: usize) -> T
func load_le[T: Copy](p: view[]u8, at: usize) -> T
```

`T` is explicit and must be an integer (the resolver's integer-only rule
for the wrapping and bit-count forms covers them). A call lowers to the
view argument as for any view parameter, the index, one bounds check on
the last byte read (`at + size_of[T] - 1`, a checked addition in the same
shape as the source's own `p[at + k]`, so the proof pass reads it as base
plus offset and discharges it from a dominating check, a loop bound or a
constant length like any index), then an intrinsic call. The interpreter
assembles the bytes in the requested order and decodes them as the
integer; the native backend emits one load of the integer's width at the
byte offset, unaligned, and `llvm.bswap` for the big-endian form. The
proof pass treats the two intrinsics as read-only calls, so a read
between two checked reads forgets nothing. The trap, when the last byte is
past the end, is kind `bounds` at the call, in every engine and
configuration (`tests/trap/load_order_bounds.kern` and its twin);
`tests/run/load_order.kern` reads every width and both orders from an
array, a vector view and a sub-slice, signed types included. The
differential test of proven checks now generates byte-order reads of two,
four and eight bytes at offsets around the end of an eight-byte array.

`bench/kern/packet_parser/packet_parser.kern` and its SIMD variant define
`rd16` and `rd32` over the intrinsics; the checksum is unchanged.

## Result

Release IR of the parser (`emit-ir --opt=release`):

| | bounds checks proven | runtime-checked | total |
|---|---|---|---|
| before | 47 | 12 | 59 |
| after | 27 | 11 | 38 |

Each wide read is one check where it was two or four; the `main` after
`clang -O2` holds 20 `bswap` instructions against none before.

Timed interleaved against the previous compiler's build, five runs each,
on a busy host (one-minute load 18 to 28, so the absolute numbers are not
the harness's):

| | Mops/s (five runs) |
|---|---|
| before | 112.6, 115.1, 115.8, 117.1, 120.0 |
| after | 135.6, 136.1, 136.5, 139.3, 143.1 |

About 18% at the median. The full harness run is owed on a quiet host
with the other changes of 2026-10-27 and 28.

`go test ./...` and `golangci-lint run ./...` pass; the Prism review is
recorded in the commit.
