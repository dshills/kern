# Byte-order writes: `store_be` and `store_le`

**Date:** 2026-10-28. **Follows:** `docs/reports/byte-order-reads.md`
(`load_be`, `load_le`) and `docs/reports/gap-diagnosis.md`, whose wal section
left the record header written one byte at a time (`put_u64`, `put_u32`:
eight and four checked byte stores with shifts) and read back the same way
in `replay`, where the C version moves a word with `memcpy` and Rust uses
`to_le_bytes` and `from_le_bytes`.

## What changed

Two prelude intrinsics, the writing counterparts of the reads (spec §4.1):

```
func store_be[T: Copy](p: edit[]u8, at: usize, v: T)
func store_le[T: Copy](p: edit[]u8, at: usize, v: T)
```

`T` is explicit and integer-only, like the reads. A call lowers to the
edit argument, the index, the same one bounds check on the last byte
written (`at + size_of[T] - 1`, proven like an index), then the intrinsic
call; the lowering of the reads and the writes is one function
(`wideAccess`). The interpreter encodes the value in the requested order
into the sequence's bytes; the native backend emits one store of the
integer's width at the byte offset, unaligned, after `llvm.bswap` for the
big-endian form. The proof pass treats the call as a write through its
edit argument, as any call. `tests/run/store_order.kern` writes every width
and both orders into an array, an edit of a vector and a sub-slice, signed
types included, and reads the bytes back; `tests/trap/store_order_bounds`
and its twin trap `bounds` at the write in both engines and both
configurations; the differential test of proven checks now also generates
byte-order writes at offsets around the end of an eight-byte array.

`bench/kern/wal` writes and reads its header fields with the intrinsics
(`put_*`, `get_*`); `bench/kern/packet_parser` and its SIMD variant build
headers with `store_be` in `wr16` and `wr32` (setup, untimed). Checksums
are unchanged.

## Measured

Release builds before and after, interleaved, seven runs, one-minute load
about 5: wal 7.2 to 7.3 Mops/s (+1%, C 7.8), packet_parser unchanged
(146 against 145, the writes are outside the timed loop). The remaining
wal difference is the payload generation loop and the `crc32` table walk,
both already instruction-for-instruction with C.

## Not done: `Vec.push`

`docs/reports/throughput-gaps.md` noted that `push` keeps `size` and `cap`
in memory across a loop because the whole `Vec` is passed to the
out-of-line `grow`. Since then the hot byte pushes moved to `Vec.extend`
(wal) and `Vec.resize` (hash_table), and the vector workload, the one that
still pushes in its timed loop, reads 1.04 of C. Nothing measurable is
left for that change, so it is not made.
