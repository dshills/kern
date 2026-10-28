# Inliner sizing: judge a callee as lowered, and single-site callees

**Date:** 2026-10-27. **Starting point:** `0193abc` (the inliner wired,
`docs/reports/inliner.md`) and the `bench/REPORT.md` it carries.
**Diagnosis:** `docs/reports/gap-diagnosis.md`, which found that the
callees-first order defeats `MaxCalleeSize` in arena_ast.

## The defect

`Inline` visits functions callees first, and `eligible` measured the
callee as it stood when the caller was visited. In arena_ast, `alloc` is
31 as lowered (28 instructions, 3 blocks); the pass inlines `Sampler.start`
(27) and `Sampler.stop` (30) into it first, which takes it to 95, and
`build`, visited afterwards, finds a callee twice the limit and keeps its
three calls. LLVM then keeps the 124-instruction external `alloc` out of
line too, and the sampled operation pays a call, a 24-byte `Expr` passed
by value and a `Result` returned by value for every node.

## What changed (`compiler/opt/inline.go`)

- **Sizes as lowered.** `Inline` records every function's size before
  any rewrite and `eligible` judges the callee by that number. What the
  copy costs the caller is still the callee as it is when copied
  (`growthOf`), charged against `MaxGrowth = 512` per caller, so a small
  function that absorbed its helpers is admitted as the small function it
  was written as, and the caller's growth stays bounded.
- **Single-site callees** (`InlineSingleSite`, on). A callee past
  `MaxCalleeSize` is admitted at its only direct call site when nothing
  else references it (no func_ref, not a release function, the entry, a
  destructor or an exported symbol; counted over the module as lowered,
  which the callees-first order keeps accurate when the callee is
  judged). The copy replaces the body, so the module does not grow by it.
  For the same reason the copy is not charged against the caller's
  `MaxGrowth`: the rule was measured both ways (below), and charging it
  starves the small helpers at the sites after it. The copy is outside
  the budget only while the caller's size after it stays within
  `MaxCallerSize = 2048` (four times the budget, and well above the 808
  of packet_parser's `main`, the largest function the workloads produce);
  past that the site is judged by the budget like any other. Without the
  bound a chain of functions each calling its predecessor once is copied
  level by level into its last member, quadratic work in the chain's
  length (a Prism finding); with it the work is linear in the module.
- The other rules are unchanged (loops were later admitted, `docs/reports/inliner-loops.md`): no loops, no `out` parameters, no
  returned accesses, no recursion, same file, not library.

Both decisions read state recorded at the start of the pass, so the
result is still a function of the module alone.

## Code growth across `bench/kern`

Release IR size (instructions plus blocks over the functions the backend
keeps) and the binary's `__text` bytes, per workload, for the three
drivers: the baseline, sizes as lowered alone, and with the single-site
rule.

| workload | IR size: baseline | sizes as lowered | with single site | text bytes: baseline | sizes as lowered | with single site |
|---|---|---|---|---|---|---|
| arena_ast | 1412 | 1725 | 1741 | 9824 | 10188 | 9980 |
| btree | 2269 | 2269 | 2275 | 13892 | 13892 | 13816 |
| buffer_pool | 2170 | 2170 | 2170 | 17100 | 17100 | 17100 |
| hash_table | 1566 | 1566 | 1572 | 10192 | 10192 | 10116 |
| intrusive_run_queue | 1407 | 1407 | 1420 | 9000 | 9000 | 8856 |
| lock_free_queue | 1376 | 1376 | 1380 | 9468 | 9468 | 9248 |
| lock_protected_map | 1732 | 1732 | 1751 | 11800 | 11800 | 11556 |
| packet_parser | 2254 | 2562 | 2581 | 11028 | 10988 | 10432 |
| page_frame_allocator | 1266 | 1266 | 1275 | 29504 | 29504 | 29352 |
| ring_buffer | 976 | 976 | 982 | 8312 | 8312 | 8244 |
| thread_pool | 1276 | 1276 | 1282 | 10876 | 10876 | 10796 |
| vector | 916 | 916 | 922 | 8660 | 8660 | 8588 |
| wal | 1673 | 1673 | 1679 | 14352 | 14352 | 14276 |
| total | 20293 | 20914 (+3.1%) | 21030 (+3.6%) | 164008 | 164332 (+0.2%) | 162360 (-1.0%) |

Sizes as lowered change two workloads. arena_ast: `alloc` is copied at
`build`'s three sites and `run`'s one, +313 in the IR and +364 text
bytes. packet_parser: `wr32` (22 as lowered, past the limit once `wr16`
is inside it) is copied at its sites in `gen`, and `handle` (36 as
lowered, with `parse` still a call inside it) into `main`; the IR grows
by 308, the text shrinks by 40 bytes. No other workload has a function
that crosses the limit by absorbing its callees.

The single-site rule takes `report` (79 to 82) into `run` or `main` in
every workload, plus `run` itself (arena_ast, lock_protected_map),
`pop_head` (intrusive_run_queue), `pop` (lock_free_queue),
`FrameAlloc.free_frame` (page_frame_allocator), and in packet_parser
`parse` into `handle` and then `handle`, carrying it, into `main` (808,
the largest function in the set; `gen` was 686 before). The IR grows by
the prologue and result plumbing of each copy, 4 to 19 per workload, and
the text shrinks in every workload but arena_ast, where the extra copies
of `alloc` outweigh it (+156 bytes net). Total over the thirteen main
workloads: IR +3.1% from sizes as lowered, +3.6% with the single-site
rule; text +0.2% and -1.0%.

## Throughput

Built with `kern build --opt=release` from each driver, best of three
with the six binaries interleaved, every run gated on a one-minute load
below 3.5 (3.07 to 3.38 as read before each run; the Spotlight bursts on
this host were at 6 to 12 for the hour before). The single-site rule was
timed both ways: the copy charged against the caller's `MaxGrowth`, and
outside it.

| binary | Mops/s (best of three) | runs |
|---|---|---|
| arena_ast, baseline | 78.5 | 75.6, 77.0, 78.5 |
| arena_ast, sizes as lowered | **110.5** | 107.2, 110.5, 106.3 |
| packet_parser, baseline | 94.3 | 93.3, 93.5, 94.3 |
| packet_parser, sizes as lowered | 96.0 | 93.2, 95.7, 96.0 |
| packet_parser, single site charged to the budget | 115.5 | 111.2, 115.5, 114.5 |
| packet_parser, single site outside the budget | **133.8** | 121.9, 133.7, 133.8 |

arena_ast: +41%, against the +43% the diagnosis measured with `alloc`
forced inline in the LLVM text; `emit-ir --opt=release` shows `build`
calling `Arena.alloc` at its three sites and no call to `alloc`. The
single-site rule does not touch its timed path (`run` and `report` are
outside the timed section).

packet_parser: sizes as lowered alone are worth nothing (`handle` was
already inlined by LLVM). With the single-site copy charged to the
budget, `parse` goes into `handle` (36 as lowered, about 490 with
`parse` inside), and `main`, which has already taken `report` by the time
its sites are visited in block order, has no budget left for `handle`;
the result is one call per packet to `handle` instead of `parse`, +20%.
Outside the budget, `handle` with `parse` inside goes into `main` too,
which is the diagnosis's `alwaysinline parse` experiment (117 there) and
beyond it, 133.8 against C's 136.4 and Rust's 163.5 in the diagnosis
table: 0.82 of Rust, up from 0.59. That measurement is why the copy is
outside the budget.

## Full harness

**Not adopted; `bench/REPORT.md` is unchanged.** The host was busy all
afternoon (Spotlight bursts at a one-minute load of 6 to 12, and a
fifteen-minute load that never dropped below 7.7), and the owner chose to
commit on the direct timings above and leave the harness for a quiet
day. Two full runs were made while waiting and are recorded here only
as corroboration of the two workloads the change targets, with the load
the report read at its start (one, five and fifteen minutes):

| run | load at start | arena_ast (Mops/s) | packet_parser | packet_parser_simd |
|---|---|---|---|---|
| previous report (`0193abc`) | 3.91 5.65 11.71 | 75.0 ± 3.9 | 93.4 ± 1.6 | 94.2 ± 2.0 |
| first run | 2.56 4.00 9.77 | 88.5 ± 22.1 | 130.9 ± 14.3 | 124.2 ± 13.2 |
| second run | 4.72 4.18 8.47 | 105.9 ± 4.3 | 133.2 ± 6.9 | 125.2 ± 9.5 |

Checksums agreed on every workload in both runs and every failure-path
count was unchanged. In the second run four release rows read below the
lower end of their previous interval, page_frame_allocator (152.0
against 153.5), intrusive_run_queue (65.3 against 66.0), thread_pool
(21.2 against 22.1) and lock_protected_map (12.3 against 16.8); the
debug build, which does not inline and whose IR is byte-identical to
before on every workload, fell by as much or more on thread_pool (9.2 →
8.5), lock_protected_map (18.7 → 14.3) and intrusive_run_queue (65.7 →
63.9). The two single-threaded rows did take a single-site inline in
their timed path (`FrameAlloc.free_frame` in page_frame_allocator,
`pop_head` in intrusive_run_queue), and a back-to-back timing of those
binaries against the baseline's, gated on the load, did not get to run
before the host was given up for the day. That check, and the harness
run that replaces `bench/REPORT.md`, are what a quiet day owes this
change; the expectation from the debug rows is that the four are the
host's.

## Verification

`go test ./...` and `golangci-lint run ./...` pass. New tests in
`compiler/opt/inline_test.go`: `TestInlineOrderKeepsLimit` (a helper that
calls two helpers, itself small as lowered and past the limit once they
are inside it, called twice from main: all three end up inlined, and the
test asserts both sizes so it cannot pass by accident) and
`TestInlineSingleSite` (a straight-line helper past the limit is inlined
at its only site and marked Inlined; with two sites, or one site and a
func_ref, it stays a call; with the rule off it stays a call),
`TestInlineSingleSiteOutsideBudget` (a budget that admits only two small
helper sites still sees the big single-site callee inlined before them
and the helpers after it) and `TestInlineSingleSiteChainBounded` (a chain
of forty functions each calling its predecessor once, under a small
`MaxCallerSize`: no function grows past it plus the budget, the retained
IR is linear in the chain, and `main` still holds more than one level).
`TestInlineThenProve` now reads its checks from `main`: `parse` there is
small as lowered and is inlined at both sites, and the bytes come from a
`Vec` so that the guard, not a constant array length, is what proves them.
`TestInlineChainBounded`'s bound allows `main` its single-site callee's
copy on top of the budget.

The release IR of every `bench/kern` file is byte-identical between the
driver the throughput numbers were taken with and the committed code, and
the debug IR of every file is byte-identical to the baseline's (the debug
build does not inline).

Prism (OpenAI gpt-6.1-sol, staged diff) raised one medium finding on the
first round, the quadratic chain above, answered by `MaxCallerSize` and
its test; the second round is clean.
