# The single-threaded rows near the 0.90 line

**Date:** 2026-10-31. **Host:** Apple silicon (arm64, macOS), busy.
Instructions retired and cycles (`/usr/bin/time -l`) carry the
comparison: medians of 5 to 11 interleaved runs. Experiment builds are
scratch, not committed.

The committed `bench/REPORT.md` (2026-10-30) has three single-threaded
rows at 0.93–0.94 of the best counterpart: btree, buffer_pool and
page_frame_allocator. They were measured again at b6aeed4, by timed `ns`
against the best of C and Rust:

| workload | then | now |
|---|---|---|
| buffer_pool | 0.93 | 0.97 (copying Vec.get, f2e6b7c) |
| btree | 0.93 | 0.91 |
| page_frame_allocator | 0.94 | 0.92 |

## What the gaps are made of

Building each with `--unsafe-elide-checks` (measurement only) gives the
share of kern's own checks: bounds, overflow and conversion. The Pool's
handle validation is library code and stays.

| workload | kern | without checks | Rust | C |
|---|---|---|---|---|
| btree | 1,424M ins, 1,648M cyc | 1,109M, 1,556M | 1,033M, 1,569M | 934M, 1,482M |
| page_frame_allocator | 722M, 254M | 681M, 247M | 598M, 236M | 634M, 254M |

- **btree:** the checks are 22% of its instructions and 6% of its
  cycles. Without them it is at Rust's cycles. Most of their cost is one
  check: `slot`'s `n.keys[i - 1]` in the scan `while i > 0 && key <
  n.keys[i - 1]`. Its bound depends on the node's runtime `count`, so it
  cannot be proven. LLVM reduces it to a loop-invariant test (`count` is
  at most 15), but at `-O2` leaves it inside the 9-instruction loop.
  C's loop is 5 instructions.
  - The scan's subtraction checks (`i - 1` under `i > 0`) are already
    gone from the machine code: LLVM removes them from the branch, so a
    kern-level proof would change nothing.
  - Each level of the descent also validates a Pool handle (index, live
    flag, generation), which the spec requires.
- **page_frame_allocator:** the checks are 6% of instructions and 3% of
  cycles. The rest of its gap to Rust (681M against 598M instructions
  with checks gone) is outside the checks.

## Levers measured, not taken

- **Release at `-O3` instead of `-O2`** (Rust's release profile is
  opt-level 3):
  - btree: −7.7% instructions, −2% cycles;
  - thread_pool: +11% instructions, +15% cycles, a multi-threaded row
    this host cannot judge;
  - the rest within ±1.5%;
  - every binary grows by 400–800 bytes.
- **Non-trivial loop unswitching alone at `-O2`**
  (`-mllvm -enable-nontrivial-unswitch`, the part of `-O3` that hoists
  btree's invariant check):
  - btree's machine code changes, along with hash_table's and two lines
    of ring_buffer's; the other ten programs are identical;
  - with 11 interleaved runs: btree −7.7% instructions, −1.0% cycles,
    −0.8% timed; hash_table −0.7%, −0.3%, −1.0%; ring_buffer unchanged.

  About 1% is not worth an LLVM-internal option on every release
  compile, which a clang without it would reject.

## Decision

No change. All three rows meet the 10% target as measured here. btree's
remaining gap is a required check that LLVM does not hoist at `-O2`,
plus the handle validation the spec requires. Hoisting such checks is a
proof-pass design question, not a flag: a check in a loop whose index
only moves away from its first value is covered by the first
iteration's. It is left for a decision of its own.
