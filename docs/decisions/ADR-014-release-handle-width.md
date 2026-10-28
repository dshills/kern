# ADR-014: Release handles keep their generation; a `PoolId` is 8 bytes

**Status:** Decided
**Date:** 2026-10-28
**Spec:** §5.6 (handles, validity, generation exhaustion), §2.3 (the 10% memory target).
**Depends on:** ADR-002 (handles as library types). **Closes:** the question in
`docs/reports/release-handles.md`.

## Problem

After the containers were rewritten in kern and the identity tag was dropped from
release handles, the B-tree workload still uses 1.23 times the peak memory of the C
version (`bench/REPORT.md`, 2026-10-28), the only memory target still missed. The
whole difference is the handle: a C node holds sixteen 4-byte child indices, a kern
node sixteen 8-byte `PoolId`s (index and generation), 64 bytes more per node, plus
12 bytes of slot metadata and free list in the pool. Meeting the target needs a
4-byte release handle, and §5.6 rules that out because a handle carries a 32-bit
generation. Phase 5 must decide what a release handle is.

## Alternatives

- **Index and generation sharing 32 bits** (for example 20 bits of index and 12 of
  generation). Rejected: a pool is then capped at about a million slots, which a
  systems language cannot impose on a library container, and a slot retires after
  4,096 frees instead of four billion, so a long-lived pool with churn leaks its
  slots. Both are new failure modes in exchange for one benchmark row.
- **No generation in release handles**, as Vale's generational references are a
  debug-build check in some configurations [vale-genrefs]. Rejected: §5.6's "a stale
  generation traps" would become a debug-only guarantee, so a release program that
  dereferences a freed handle would silently read whatever the reused slot holds.
  That is memory-safe (every slot is valid memory of the right type) but it is the
  logic-error class the handle design exists to catch, and the §8 workloads are run
  in release. It also reopens ADR-002 invariant 3, which fixes the check order for
  every build.
- **A per-pool handle kind** (a second container with 32-bit handles and no
  generation). Rejected for now: two pool types with two handle types double the
  library and the audit for one workload; a program that wants it can keep a
  `Vec[T]` of slots and `u32` indices today, as the ring-buffer and run-queue
  workloads do.
- **Chosen: keep the 8-byte release handle** (index and generation), with the
  identity tag debug-only as already implemented, and report the B-tree memory row
  as missing the target by the width of the generation.

## Decision

A release `PoolId[T]` is `{ index: u32; gen: u32 }`, 8 bytes; a debug `PoolId[T]`
adds the 32-bit identity tag, 12 bytes. A release `ArenaId[T]` is `{ index: u32 }`,
4 bytes; a debug `ArenaId[T]` adds the tag, 8 bytes. These widths are part of the
layout the machine API reports (`show-layout` under `--opt`) and are normative from
this record on. Stale-handle detection through the generation is a guarantee of
every build configuration; identity (foreign-handle) detection is a debug-build
guarantee, as §5.6 already says.

## Consequences

- The B-tree workload keeps about 1.2 times the C version's peak memory, 64 bytes
  of generations per node, and `bench/REPORT.md` reports that row as missing the
  10% memory target. The research report states the miss as the cost of checked
  handles rather than as an open item.
- `arena_ast` meets the memory target with its 4-byte release `ArenaId`, since an
  arena never frees a single element and needs no generation.
- Rust's index-based arenas and graph crates take the same 8-byte position when
  they carry a generation and the 4-byte one when they do not [rust-reference];
  kern chooses the checked one and does not offer the other in the prelude.
- Reopening: a program that needs 4-byte pool handles is the evidence that would
  reopen this record, as a second container kind, not as a change to `PoolId`.
