# Proof pass: masked indexes

**Date:** 2026-10-30. **Spec:** `specs/prove-masks/SPEC.md`. **Plan:**
`specs/prove-masks/PLAN.md`. **Proposed in:**
`docs/reports/thread-pool-job-path.md`, "What a compiler could do".

## What changed

The proof pass (`compiler/opt/prove.go`) knows that a value masked by a
constant, `x & c` with unsigned operands, is at most `c`. The bound
survives a conversion to an unsigned type, a local that holds the
masked value, and a constant offset. An index check whose index is
such a value is proven in one of two ways:

- `constant`, when `c` plus a positive offset is below a length the
  array's type gives;
- `dominated_by_check`, when it is at most a literal that a guard such
  as `len(p) >= 1024` put below the length.

The mask rules run after the literal-index rule and before the general
guard rule, so a masked index that is also guarded is labelled
`constant`. The §16 `proven_by` values are unchanged; the schema note
says what `constant` now covers.

Out of scope by design:
- a mask by a value that is not a constant (`i & mask`);
- a masked value carried around a loop or across a call (a field masked
  in one method and read in another);
- remainders (`x % c`);
- a byte's range (`u8` into a 256-entry table), which the fourth agent
  suite counts on staying checked.

## Effect on the repository's programs

Compared with the compiler at 18e0acc, over every program under
`bench/kern/`, `examples/` and `tests/run/` that checks, ten bounds
checks change from `runtime-checked` to proven at each opt level. All
ten are masked indexes:

| program | site |
|---|---|
| `lock_free_queue` (×2 sites), `lock_free_queue_fail` (×2) | `q.slots[usize(c.pos & 4095u64)]` |
| `packet_parser`, `packet_parser_simd` | `offsets[usize(i & 4095u64)]` |
| `page_frame_allocator`, `page_frame_allocator_fail` | `held[(hh + hn) & 4095usize]` |
| `thread_pool` | `g.ring[(g.head + g.len) & 2047usize]` |
| `wal` | `c.table[usize((acc ^ u32(b)) & 255u32)]` |

No existing check lost its proof. The other runtime-checked sites of
the comparison are the new not-proven fixtures added below. The
thread-pool report expected no change in machine code from these,
because LLVM already folds the checks. The labels change, and with them
the counts `show-bounds-checks` and the agent benchmark report.

Two masked sites keep their checks:
- `ring_buffer`'s `self.slots[self.tail]` reads a field that another
  method masked.
- `page_frame_allocator`'s `w = (w + 1) & 1023` is carried around a
  loop.

Both are outside the scope above.

## A harness gap found on the way

The fixture harness executed only fixtures of phase 5 or earlier
(`const Phase = 5` in `tests/harness_test.go`). The three phase-6
fixtures, `prove_forms`, `prove_forms_not` and `short_circuit`, had
been checked but never run in either engine since phase 6 began. The
constant is now 6, and all three run and agree in both engines and both
opt levels.

## Verification

- **Unit tests** (`TestProveMasks`) cover:
  - proven: a direct mask, a masked local, a converted mask (from `u8`,
    `u32` and `u64`), a positive and a negative offset, a mask by 0, a
    guarded mask on a slice, and a masked index under a guard (labelled
    `constant`);
  - not proven: a mask equal to the length, an offset that reaches it,
    a mask by a parameter, and the largest admitted constant.
- **Fixtures.** `prove_forms` gains five masked functions, all proven,
  and `prove_forms_not` four, all runtime-checked. Both run in both
  engines and both opt levels. `TestProveMaskFixtures` pins their
  labels.
- **The differential test of proven checks** gains masks below, at and
  above an `[8]` array's length, with offsets, through a local, a
  conversion, and a guarded and an unguarded slice. It passes at its
  default count and with 400 programs.

  Widening the rule from `<` to `<=`, which would prove `x & 8` into an
  `[8]`, makes it fail: the native build stops trapping where the
  interpreter traps.
- **The agent suites.** The dry runs of the fourth and third suites exit
  0. The fourth suite's natural solutions keep their exact counts (9,
  6, 7, 14, 8, 7, 9, 9, 8, 6), so no idiom of it was erased.
- **Cost.** `BenchmarkProve` ran with the test binaries before and after
  alternating, five rounds each, median ns/op:

  | benchmark | before | after | ratio |
  |---|---|---|---|
  | lexer | 1.766 ms | 1.751 ms | 0.99 |
  | sequential guards | 2.464 ms | 2.479 ms | 1.01 |

  Both are well inside the 1.10 limit. The host's load was high (60) but
  identical for both, since the runs were interleaved.
- **Gates.** `golangci-lint run ./...` reports 0 issues; `go test ./...`
  is green.

## Reviews

- **speccritic:** VALID_WITH_GAPS, no critical finding. It asked which
  label wins when both rules hold, and how the cost is aggregated; both
  are folded in.
- **plancritic:** the first plan was NOT_EXECUTABLE, because the
  general guard rule ran before the mask's `constant` rule, which
  contradicted the spec. With the order made explicit and tested, it
  was EXECUTABLE_WITH_CLARIFICATIONS at 93.
- **prism:** no finding.
- **realitycheck:** PARTIALLY_ALIGNED, score 100, no violations.
- **verifier:** package and `Prove` entries from the single-file
  include. The package is tested.
