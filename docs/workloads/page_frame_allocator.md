# Workload: page_frame_allocator

## Summary
A kernel physical-memory manager: a bitmap over 4 KiB frames with
`alloc_frame`, `free_frame`, contiguous `alloc_run(n)` for DMA, and
double-free detection. It hands out frame numbers, not pointers; translating a
frame number to a mapped address is the caller's (unsafe) MMU concern. This is
the freestanding workload of Phase 3 and the lowest-level pure-core program in
the suite.

## Classification
expressible in the core

## Data structures and ownership graph
```text
struct FrameAlloc { bits: [4096]u64; base: u64; free: usize; hint: usize }   // 4096 words = 262144 frames = 1 GiB
struct Frame { num: u32 }                                                    // Copy, nocopy-free handle
```
- `FrameAlloc` owns its bitmap inline (no heap: it is the thing that provides
  memory). A set bit means the frame is free.
- A `Frame` is a `Copy` number. The allocator does not track who holds it;
  `free_frame` checks the bit (a frame that is already free is a `Err(DoubleFree)`,
  a frame outside `[0, nframes)` is `Err(OutOfRange)`), which is the same
  logical-validity-at-runtime stance as handles in §5.6.
- The `impl Allocator for FrameAlloc` adapter that turns a frame number into
  `RawPtr` is one `unsafe` cast in a separate module and is not part of this
  workload's source (see Open risks).

## Access patterns
| Operation | Convention | Notes |
|---|---|---|
| `alloc_frame(edit self) -> Result[Frame, OutOfFrames]` | `edit` | scans from `hint` |
| `free_frame(edit self, f: Frame) -> Result[(), FreeErr]` | `edit` | checks the bit before clearing |
| `alloc_run(edit self, n: usize) -> Result[Frame, OutOfFrames]` | `edit` | first-fit over bit runs |
| `is_free(view self, f: Frame) -> bool`, `free_count(view self) -> usize` | `view` | `Copy` reads |
| `init(out self, base: u64, nframes: usize, reserved: view[]Frame)` | `out` | builds the bitmap in place; no 32 KiB copy |
No `get2`; no release accessor. In a multi-core kernel the allocator is
`Mutex[FrameAlloc]` or an `Interior` per-CPU cache; the single-owner form is
what this workload measures.

## Why this classification
Every operation is integer and bit arithmetic on an inline array behind one
`edit self`; bounds checks on `self.bits[w]` are the only runtime checks, and
`w < 4096` is provable from the loop bound. The `out` convention (§5.2) lets
the kernel construct the 32 KiB structure directly in its static storage with
per-field definite initialization inside `init`. No reference is stored, no
pointer is formed, no `unsafe` appears. The frame handle is `Copy` data, so
storing it in page tables, DMA descriptors, or another arena is legal (§5.6
spirit, applied to a user type).

## Sketch
```text
module pfa_bench

impl FrameAlloc {
    func alloc_frame(edit self) -> Result[Frame, OutOfFrames] {
        if self.free == 0 { return Result.Err(OutOfFrames{}) }
        w := self.hint
        for step in 0..4096 {
            word := self.bits[w]                                  // u64 Copy read; bounds-checked
            if word != 0u64 {
                bit := trailing_zeros(word)                        // u32
                self.bits[w] = word & (word - 1u64)                // clear lowest set bit
                self.free = self.free - 1
                self.hint = w
                return Result.Ok(Frame{ num: u32(w) * 64u32 + bit })
            }
            w = (w + 1) & 4095
        }
        trap()                                                     // unreachable: free > 0
    }

    func free_frame(edit self, f: Frame) -> Result[(), FreeErr] {
        if usize(f.num) >= 262144 { return Result.Err(FreeErr.OutOfRange(f)) }
        w := usize(f.num) / 64
        m := 1u64 << (f.num % 64u32)                               // shift count is u32 (§4.1)
        if self.bits[w] & m != 0u64 { return Result.Err(FreeErr.DoubleFree(f)) }
        self.bits[w] = self.bits[w] | m                            // read ends before the write (§5.4)
        self.free = self.free + 1
        if w < self.hint { self.hint = w }
        return Result.Ok(())
    }
}
```
`self.bits[w] = self.bits[w] | m` is accepted by the in-statement liveness
rule: the right-hand side is fully evaluated before the target's `edit`
begins.

## Benchmark notes
- C: the same bitmap with `__builtin_ctzll`, no bounds checks except the
  explicit range test in `free_frame`; a buddy allocator variant as a second row.
- Rust: `[u64; 4096]` with `trailing_zeros`, identical control flow; the
  `bitmap-allocator` crate as context.
- Measure (hosted in Phase 2, freestanding under QEMU in Phase 3 per PLAN 2.6):
  alloc/free pairs per second with a random free order, `alloc_run(16)`
  throughput under fragmentation, p50/p99 per operation, peak RSS (32 KiB
  bitmap, zero heap), allocation count (zero), copies (zero; `init` uses
  `out`), unsafe lines (zero), text bytes (the freestanding binary is what
  matters here), compile time, and overflow checks per call (`word - 1` is
  guarded by `word != 0`, so `show-bounds-checks` should report the overflow
  check as `dominated_by_check` or the kernel pays a branch per alloc).
- Failure paths: double free and out-of-range free return errors; exhaustion
  returns `OutOfFrames`; each a fixture.

## Open risks
- The `Allocator` adapter (frame number to `RawPtr`) needs one unsafe cast and
  a contract; it lives beside the workload, not in it. If the Phase 3 harness
  insists the workload implement `Allocator` directly, the classification
  becomes "requires audited unsafe internals behind a safe API" for that one
  function.
- Trapping arithmetic (§4.1) means `word - 1u64` and `w + 1` carry overflow
  checks that C does not; the mask form `(w + 1) & 4095` still traps if `w`
  were `usize::MAX`, which it never is, but the checker cannot know that.
