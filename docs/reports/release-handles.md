# Handles without identity tags in release builds

**Date:** 2026-10-28. Follows `docs/reports/btree-rss.md`, which left btree's
peak RSS at 1.44 of C, and gap-diagnosis item 6 (handle size).

## What the spec allows

§5.6 says that "in debug builds" every container carries a 32-bit identity
tag "that is also stored in handles it issues". A release build already
gives every container tag 0 (`-DKERN_RELEASE`, `SetIdentityTags(false)`), and
the identity check skips tag 0, so in release the tag in a handle was four
bytes that were always zero and never checked. Dropping it changes no
behaviour and stays within the current text. The generation, and with it
stale-handle detection, is untouched.

## Change

- The prelude carries both declarations, marked by a line suffix:
  `struct PoolId[T] { index: u32; gen: u32; phantom_tag: u32 } //@debug` and
  `struct PoolId[T] { index: u32; gen: u32 } //@release`, likewise for
  `ArenaId` and the two lines in `alloc` and `check` that read or write the
  tag. `resolve.Prelude(release)` blanks the other mode's lines with spaces,
  so both texts have the same length and every span means the same in both.
- `resolve.ResolveFor(files, release)` is used by `kern build`, by every
  command that takes `--opt` (`run`, `emit-ir`, the `show-*` commands,
  `show-layout` included), and by the native backend's debug-info sources.
  `Resolve` stays the debug form (check, edit, incremental, explain).
- So that debug and release builds accept the same programs, `phantom_tag`
  is private to `std/core` in both modes, and a `PoolId`/`ArenaId` literal
  outside `std/core` is `NAME_PRIVATE` in both (handles are issued by their
  container; `zero()` still gives a zero handle). Each mode reports the same
  single diagnostic. Fixtures: `tests/negative/handle_private(_ok)`,
  `tests/negative/handle_literal(_ok)`; `TestHandleLayoutByMode` checks the
  sizes (PoolId 12 → 8, ArenaId 8 → 4) and the equal prelude lengths.
  `tests/trap/identity.kern` (debug) and its release twin pass unchanged.

## Measured

`kern build --opt=release`, binaries before and after this change
interleaved with C (`clang -O2`), five runs each, `/usr/bin/time -l`, on a
busy host (one-minute load 5.5 to 7.2), so treat the times as indicative:

| | C | kern before | kern after |
|---|---|---|---|
| btree `Node` (`show-layout --opt=release`) | 312 B | 440 B | 376 B |
| btree peak RSS | 61.3 MiB | 88.1 MiB (1.44) | 76.1 MiB (1.24) |
| btree wall time, ms | 343-471 | 435-555 | 410-520 |
| arena_ast peak RSS | 26.3 MiB | 26.3 MiB | 19.2 MiB |
| arena_ast wall time, ms | 8.4-8.6 | 8.9-9.1 | 8.1-8.4 |

**btree still misses the 10% RSS target** (1.24), and its throughput is
still about 0.8 of C. arena_ast's node falls to Rust's 16 bytes, and its
RSS moves below C's. The checksums of every release benchmark are
unchanged. Debug builds are unchanged (same layouts, same text).

## Question for the owner: 4-byte handles

Meeting btree's RSS target needs a `PoolId` of four bytes, C's `uint32_t`
index. With that, a node is 312 bytes like C's, plus the pool's 12 bytes
of slot metadata and free list per slot: about 324 bytes per node against
312, a ratio near 1.04. That is an estimate from the layout, not a
measurement. The current §5.6 text rules this out, because a handle must
carry a 32-bit generation:

- **Index and generation sharing 32 bits** (for example 20 + 12): a pool
  then holds at most about a million slots, and a slot retires after 4,096
  frees instead of four billion, so long-lived pools with churn leak slots.
  It needs §5.6's "generations are 32-bit" changed and a capacity limit added.
- **No generation in release handles**: stale-handle detection becomes
  debug-only, like identity. Memory safety still holds (every slot is valid
  memory of the type), but §5.6's "a stale generation traps" would become a
  debug guarantee. This is the ADR-002 trade-off reopened.
- **Keep 8-byte release handles** (this change) and report btree's RSS row
  as missing the target by the width of the generation.

Nothing here has been changed in the spec.

## Next: slot metadata beside the element

`Pool` kept each slot's generation and flags (`PoolSlotMeta`, 8 bytes) in an
array separate from the elements, so every handle access touched two cache
lines: the metadata for `check`, then the element. A slot is now the
metadata followed by the element at its alignment, in one buffer
(`Pool.meta_at`, `Pool.elem_at`; the stride is the two rounded up to the
larger alignment). For btree's 376-byte node the stride is 384, the same
bytes per slot as before, and the separate buffer and its growth copy are
gone. An element aligned above 8 pays padding after the metadata.
`tests/run/pool_slots.kern` covers a 1-byte element (stride 12) and a
40-byte one through growth and slot reuse. It agrees in the interpreter and
both native builds, as does every existing Pool fixture.

btree release, seven runs interleaved with C on a very busy host (one-minute
load 12.6 to 13.8):

| | C | separate metadata | metadata beside element |
|---|---|---|---|
| wall time, ms | 463-499 (median 484) | 543-635 (median 572) | 420-518 (median 465) |
| p50 insert latency, ns | 208-209 | 250-292 | 166-209 |
| peak RSS, MiB | 61.3 | 76.2 | 75.3 |

On this host btree's throughput now matches C's, from about 0.85 of it.
The checksum is unchanged. Memory stays at 1.23 of C: the question above
still stands, and nothing here changes it. A harness run on a quiet host
should confirm the throughput before `bench/REPORT.md` reflects it.
