# packet_parser against Rust: where the 9% goes

**Date:** 2026-10-31. **Host:** Apple silicon (arm64, macOS), busy.
Instructions retired and cycles (`/usr/bin/time -l`) carry the
comparison: medians of 5 to 9 interleaved runs. The variants below are
scratch copies, not committed; the benchmark programs stay as they are.

The committed report puts packet_parser at 0.91 of Rust's throughput,
the row closest to the target line. On this host:

| binary | instructions | cycles | timed ns |
|---|---|---|---|
| kern (b6aeed4) | 1,863M | 275M | 67.1 ms |
| Rust | 1,703M | 251M | 61.0 ms |

## Splitting the work

Copies of both programs with the payload sum replaced by the payload
length (the same checksum both ways) separate the payload loop from
everything else. In their own interleaved run the whole programs
measured 1,863M instructions and 278M cycles (kern), and 1,703M and
254M (Rust). The differences below are taken from those totals:

| part | kern | Rust |
|---|---|---|
| without the payload sum | 652M instructions, 89M cycles | 711M, 81M |
| the payload sum (difference) | 1,211M instructions, 189M cycles | 992M, 173M |

Two thirds of the cycle gap (16M of 24M) is the payload's `sum16`. The
rest (parsing, the slice and the loop, about 0.8 cycles per operation)
runs on fewer instructions than Rust's.

## The payload loop

Both compilers vectorize it, in different shapes:
- **kern:** the benchmark's `while i + 1 < n { ...; i = i + 2 }` gets a
  32-word main loop (64-byte `ldp` pairs), a 4-word epilogue (`ldr d`)
  and a scalar remainder.
- **Rust:** `chunks_exact(2)` gets a 64-word main loop and an 8-word
  epilogue (`ldr q`).

Payloads are 8 to 71 words, so most packets run the epilogues.

What was ruled out:
- **Overflow checks.** The loop's overflow checks are not left in the
  code: removing them from the emitted LLVM by hand changed no
  instruction.
- **Missing no-wrap flags.** Emitting `nuw`/`nsw` on arithmetic that
  directly follows its overflow check, which lets LLVM's trip-count
  analysis use the check, also changed no instruction. It was not
  committed.
- **Other equivalent loop shapes.** Iterating over word indices, or
  bounding the loop by `n / 2 * 2`, cuts instructions by 7% and 4%, but
  leaves cycles where they were (271M and 272M against 273M).

The remaining difference is therefore not instructions the compiler
emits. It is how LLVM's vectorized loop structure behaves on random
payload lengths. The epilogue widths and the branch paths between the
main loop, epilogue and remainder are a cost-model outcome for this
source shape.

## Decision

No compiler or library change. The row meets the 10% target. The one
lever that changes the loop's machine code is the source's own shape,
and benchmark programs are not rewritten (bench/PROTOCOL.md). The
next quiet-host harness run decides whether the 0.91 holds; the
scratch measurement here gives 0.91 as well.
