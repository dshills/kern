# Kern-level inlining before the proof pass

**Date:** 2026-10-27. **Plan:** PLAN.md Phase 6 (6.1 to 6.5; 6.6 deferred).
**Entry baseline:** commit `7f1ff54` (one trap block per function, proven
checks skipped natively, the proof pass) with the `bench/REPORT.md` it
committed, and `d5d1bdc` (the inliner package, then unwired). The baseline
report's checksum lines are repeated in the table below.

## What changed

- **Pipeline** (`compiler/opt/run.go`). Every consumer of IR now runs
  `lower → validate → inline → validate → prove → validate` through
  `opt.Run`: `native.Build` and `EmitAsm`, `kern run`, the machine commands
  that read IR (`emit-ir`, `show-copies`, `show-allocations`,
  `show-bounds-checks`, `show-effects`), and the test harness;
  `explain-ownership` keeps the lowered IR as written, since it matches
  source symbols to IR locals by name (a Prism finding). A validation failure names the pass and nothing later runs.
  `Inline` is on for `--opt release` and off for `--opt debug`; the proof
  pass moved from the lowering into the package and runs in both.
- **Inlined functions** (`ir.Func.Inlined`): a function nothing references
  after the pass is left out by the backend, the printer and every
  analysis.
- **Reporting policy** (`compiler/analysis`): `show-bounds-checks` reports
  one entry per source location and kind, aggregated over the IR
  occurrences of that location (generic instantiations, inline copies, the
  retained callee while reachable): proven only when every occurrence is
  proven, with the first proven occurrence's fact. `show-copies` and
  `show-allocations` report a location once. The policy is stated in
  `tools/agent-api/schema.md` (generated from `compiler/diag/schema.go`).
- **Proof pass gains** (`compiler/opt/prove.go`). Two things the inlined
  bodies needed: places are resolved through reborrows (a call argument is
  an access opened on the caller's access, so the guard's `len(pkt)` and
  the copied body's `len(p)` name the same place), and the Else edge of
  `len < K` (or the Then edge of `len >= K`) establishes `K - 1 < len`, so
  the header reads at literal offsets under `if len(pkt) < 32` are proven.
  One soundness fix found on the way, in two steps (the second Prism's):
  a call that receives a view of a local may write it (an `Interior`
  value's unsafe code), so the pass now resolves every access to the
  place it was opened on and forgets each local an argument reaches, and
  a local any access is opened on is no longer treated as holding its
  single stored value everywhere; `prove_test.go` pins both with an
  `Interior` counter advanced through a view, as a parameter and as a
  local.
- **Harness.** Every executable fixture now also builds and runs natively
  under `--opt release` (not fixtures with test hooks, identity-tag traps or
  a `config: opt=debug` line), compared against the interpreter's output and
  trap record exactly as the debug build is. That is the differential test
  of the inliner: the interpreter runs the un-inlined program.

## Measurements

The inliner's size metric counts instructions plus one per block, with
`MaxCalleeSize = 48` and `MaxGrowth = 512`. In the packet parser the sizes
are `rd16` 21, `rd32` 47, `wr16` 20, `wr32` 22 (which grows past the limit
once `wr16` is inside it), `payload` 12, `lcg` 6, `handle` 36; `sum16` (77,
a loop) and `parse` (101) stay calls, which LLVM inlines on its own.

Under `--opt release`, `emit-ir` of the parser shows no call to `rd16` or
`rd32` (eleven under `--opt debug`), and the proof pass marks 35 bounds
checks `dominated_by_check` against 3 in debug. `show-bounds-checks`, per
location:

| configuration | constant | dominated_by_check | loop_bound | runtime-checked (bounds) |
|---|---|---|---|---|
| debug (before and after) | 2 | 1 | 1 | 15 |
| release, before this change | 2 | 1 | 1 | 15 |
| release, after | 2 | 9 | 1 | 7 |

The twelve newly proven reads the plan predicted are the ten header
offsets in `parse` and the two `rd16` reads under `sum16`'s guard.

Full harness run (`go run ./bench/harness -experiment`, five runs per row,
95% intervals), kern release rows against the baseline report's:

| workload | before (Mops/s) | after | best (lang) | ratio before → after | throughput target |
|---|---|---|---|---|---|
| vector | 918.2 ± 226.2 | 1159.9 ± 310.4 | 1199.0 (c) | 0.85 → 0.97 | **now meets** |
| hash_table | 17.9 ± 0.7 | 18.5 ± 0.9 | 20.6 (c) | 0.87 → 0.90 | no |
| btree | 4.8 ± 0.2 | 4.1 ± 0.5 | 5.9 (c) | 0.76 → 0.69 | no |
| arena_ast | 64.6 ± 18.0 | 75.0 ± 3.9 | 130.7 (rust) | 0.58 → 0.57 | no |
| ring_buffer | 931.2 ± 148.5 | 914.9 ± 149.5 | 943.0 (rust) | 1.02 → 0.97 | yes |
| wal | 4.6 ± 0.1 | 4.6 ± 0.0 | 7.3 (rust) | 0.62 → 0.63 | no |
| packet_parser | 89.6 ± 7.2 | 93.4 ± 1.6 | 157.5 (rust) | 0.59 → 0.59 | no |
| buffer_pool | 29.5 ± 1.7 | 29.4 ± 0.1 | 32.2 (rust) | 0.94 → 0.91 | yes |
| page_frame_allocator | 147.8 ± 21.6 | 154.0 ± 0.5 | 170.7 (rust) | 0.88 → 0.90 | **now meets** |
| intrusive_run_queue | 66.9 ± 2.3 | 66.4 ± 0.4 | 66.0 (rust) | 1.02 → 1.01 | yes |
| thread_pool | 19.0 ± 1.6 | 22.2 ± 0.1 | 35.6 (c) | 0.69 → 0.62 | no |
| lock_protected_map | 24.8 ± 0.5 | 18.8 ± 2.0 | 27.8 (c) | 0.78 → 0.68 | no |
| lock_free_queue | 593.4 ± 164.1 | 725.7 ± 4.1 | 865.6 (c) | 0.82 → 0.84 | no |

Load average at start 3.42 (the baseline's 3.04). Checksums agree on every
workload, and every failure-path count is unchanged. Five workloads meet
the throughput target against three before; vector and
page_frame_allocator now do, though both sit inside their intervals of
the baseline rows, so the honest reading is that the change is small on
the harness: the parser's twelve proven reads are worth about 4% there,
since LLVM had already inlined `rd16` and `rd32` and folded most of their
checks, and the plan's prediction of a third of the gap does not hold.
What the inliner buys is proof, not instructions: the IR the backend
receives has 35 bounds checks marked proven in the parser against 3, and
the trap record and `show-bounds-checks` say which.

Two rows read below the lower end of their baseline interval, btree
(4.1 against 4.6) and lock_protected_map (18.8 against 24.3), the plan's
gate. Both fell in the debug build by the same amount (btree 4.4 → 4.0,
lock_protected_map 22.1 → 18.7), and the debug build does not inline;
the LLVM text the baseline compiler (`7f1ff54`) and this one emit for the
two workloads is byte-identical under `--opt debug`, and the two
compilers' release binaries, run five times each back to back on the
same host, time within 2% of each other (btree 3.56 against 3.48 Mops/s
at the median, lock_protected_map 13.35 against 13.51). The two rows are
the host's, as lock_protected_map's kern-to-C ratio of 0.67 to 0.78
across the last three reports already showed; `bench/REPORT.md` is the
run as measured.

## Verification

`go test ./...` and `golangci-lint run ./...` pass. The new fixtures:
`tests/trap/inlined_bounds.kern` (a trap inside a helper the release build
inlines; the same record in both builds and the interpreter) with its
twin, `tests/run/inlined_params.kern` (an `own` parameter with a
destructor, a reassigned local, a view of a struct field, two return
paths), and `tests/trap/bounds_const_guard.kern` with its twin (the
`len >= K` facts at the boundary). `cmd/kern/cli_test.go` checks the
reporting policy: a helper inlined at two sites is one entry, proven in
release and not in debug; a helper also taken as a function value keeps
its reads runtime-checked.

The first full harness run found one bug the fixtures had not: the release
build of intrusive_run_queue failed validation ("unsafe depth 2 on one
path and 3 on another" at a short-circuit join), because its pointer
accessors `return` from inside their `unsafe` block. A function may do that
(the exit closes the region), but once inlined the return is a branch to
the join, so `inlineInto` now closes the callee's open regions with
`UnsafeEnd` before that branch, counted in the growth estimate;
`TestInlineReturnInsideUnsafe` pins it with returns at depth one and two
inside a short-circuit condition in the caller's own unsafe block.

Prism (OpenAI gpt-6.1-sol, staged diff) took seven rounds: `explain-ownership`
had been put on the inlined IR although it matches source symbols to IR
locals by name (reverted), a release-only fixture would have run in debug
first, and three refinements of what a call may write, each with its
regression in `prove_test.go`: an access opened directly on a local now
resolves to it, a projection a call returns is a may-alias for forgetting
locals and never the argument's place (a helper returning a shorter slice
must not inherit the argument's length), and every access argument is a
candidate. The round after the unsafe-depth fix raised two findings on
`Init`, which marks a place initialized and has no runtime effect: the
prepass now says why it is not a write, and an `Init` through an access
of unknown origin forgets the locals it may reach, like the other writes
(free, and symmetric). The last round is clean.

## Deferred (PLAN.md 6.6)

Library callees, cross-file inlining and inlining through function values
stay as planned. (Cross-file inlining landed on 2026-10-30,
`docs/reports/cross-file-inlining.md`: a copied block carries its file.) `parse` itself (size 101) is not inlined and remains the
call per packet that separates the parser from Rust.
