# hash_table: what the last ~10% is

**Date:** 2026-10-28. **Starting point:** `bench/REPORT.md` reads kern release
at 18.2 ± 1.3 Mops/s against C's 21.2 (0.86, outside the 10% target); peak RSS
already meets it (1.06; 1.05 measured here, 287.5 against 273.7 MiB).

## Method

`kern build --opt=release` against `clang -O2` on `bench/c/hash_table/main.c`,
binaries interleaved, five to seven runs, one-minute load 3.3 to 8 (busy
host; differences below 3% are not resolved). Experiments were made on the
LLVM text `--emit-llvm` wrote, linked with `runtime/hosted/src/kern_rt.c` as
the harness does. On this host kern ran at about 0.88 of C (290-330 ms
against 250-280 ms).

## Two causes

**`Map.probe` stays out of line (about 7%).** LLVM inlines `probe` into
`main` (through `find`) but not into `Map.insert`: cost 250 against a
threshold of 225 at `-O2` (and 250 against 250 at `-O3`). With `probe`
marked `alwaysinline` in the text, kern goes from 298-313 ms to 272-292 ms
against C's 249-277. The C source writes `static inline size_t probe` and
the Rust source `#[inline] fn probe`; kern has no inline hint, and the kern
inliner skips `probe` twice over (it has a loop, and it is 61 against
`MaxCalleeSize = 48`, with two call sites). Things tried that do not get it
under the threshold:

- `wrapping_add` for the probe step: no change (that check was already gone).
- One packed `i64` argument to the trap call instead of three: cost 250 → 240.

So the remaining gap is an inline-hint question, which is a language or
PLAN.md 6.2 decision (a hint, or an inliner rule for small callees with a
loop), not a fix made here.

**Zero-filling the tables (2-4% in C's terms).** `new_map` fills `keys` and
`vals` with `resize(cap, 0)`; C leaves them uninitialised. Adding the same
`memset` to the C version costs it 2-4%. Rust gets zeroed tables for free:
`vec![0; n]` allocates with `calloc`.

## Applied: zeroed storage in `Vec.resize`

`Vec.resize` on a vector with nothing allocated now takes zeroed storage
(new `SystemAllocator.alloc_zeroed`, `calloc` in the hosted runtime,
allocate-and-clear in the freestanding one and the interpreter), writes one
copy of the fill value and, when that copy's bytes are all zero, skips the
fill. The other elements already hold that value, and a copy with all-zero
bytes owns no allocation: a `Box` or `Shared` copy is never all zero, and an
all-zero `Vec` copy is the empty vector. Otherwise the ordinary fill runs
from element 1. `tests/run/vec_resize.kern` covers the zero path for `u64` and
for a struct with a destructor (each element is still destroyed once), and
gives the same output in the interpreter and both native builds.

Measured here, kern release went from 291-309 ms to 292-302 ms (seven
interleaved runs, load about 7): within noise, in line with the C
experiment's bound. Checksums are unchanged for hash_table and
lock_protected_map, the two benchmarks that call `resize`.
