# Containers over a caller's allocator: `BoxIn[T, A]` and `VecIn[T, A]`

**Date:** 2026-11-01. **Spec:** `specs/alloc-containers/SPEC.md` v0.1.
**Plan:** `specs/alloc-containers/PLAN.md`. **Item:** the first of the
ten-item list ("containers that take an allocator: spec §6 says every
allocating container takes `own A` where `A: Allocator`; `Box`, `Vec`,
`Pool` and `Arena` are hard-wired to `SystemAllocator`").

## Before

Spec §6 promised containers over an allocator the program supplies, and
ADR-011 named that as how freestanding code would allocate. Once the
containers became kern code over `SystemAllocator` and both runtimes
provided it, nothing took an allocator: freestanding and kernel-style
code could keep container storage only in the runtime's arena (1 MiB
freestanding). The `Allocator` interface itself was usable: a generic
struct can hold an `A: Allocator` field and call it, which a probe in a
user module confirmed before the spec was written.

## The library

`BoxIn[T, A: Allocator]` and `VecIn[T, A: Allocator]` in the prelude hold
their allocator inline and call only it. They are fallible throughout,
since a user allocator running out is expected: `BoxIn.try_new_in`,
`VecIn.new_in`, `try_reserve`, `try_push`, `try_extend`, and the
non-allocating `len`, `capacity`, `allocator`, `pop`, `get`, `at`,
`as_view`, `as_edit`, `clear`, `release`. The bodies are `Vec`'s `try_`
forms with `a.alloc` / `a.free` in place of the `heap_*` helpers. The
interface has no `realloc`, so growth allocates the new buffer, moves
the live elements with one `ptr_copy`, and frees the old buffer; a
failed `alloc` writes nothing. `VecIn.seed_len` is a test hook (zero-size
`Copy` elements only) that reaches the growth guard.

The impl blocks must restate the constraint (`impl[T, A: Allocator]
VecIn[T, A]`): a bare `impl VecIn[T, A]` leaves `A` unconstrained and the
prelude fails to resolve ("A does not satisfy Allocator").

## The compiler piece

`namedMarker` derives the markers of the core containers by name, since
a `RawPtr` field alone would make them neither `Send` nor `Share`. A
`core.BoxIn` / `core.VecIn` case makes them `Send` and `Share` iff both
the element and the allocator are, and `Interior` iff either is (the
allocator is an inline field, and a type is `Interior` when any field
is). The spec's first draft said "iff both" for `Interior` too; writing
the rule exposed the mistake and the spec was corrected.

## Verification

| Requirement | Evidence |
|---|---|
| R2 mirrors `Vec` | `tests/run/vecin_system`: 11 pairs against `Vec` print equal len, capacity and checksum |
| R3 allocation through `a` | `tests/run/vecin_budget`: the budget allocator prints every `alloc` and `free` (sizes 32, 64, 128, 256, each freed after the next is allocated) and traps on a `free` naming no live allocation; vector and box end with no live allocation |
| R4 exhaustion | `tests/run/vecin_exhaust`: `try_reserve`, `try_push` (value destroyed once), `try_extend` and `BoxIn.try_new_in` past the budget; a byte size past `usize` refused without an `alloc` call |
| edge cases | `tests/run/vecin_edges` (zero-size elements as 0-byte requests, growth after `release`, a failed `try_new_in` destroying the value before the allocator, `pop` on empty); `tests/trap/boxin_released`, `vecin_bounds` and twins |
| growth guard | `tests/run/vecin_guard` (test hooks): grows at capacity 2^63 - 1, refuses at 2^63 before doubling, `try_reserve` unguarded |
| R5 no `oom` | `compiler/analysis/try_alloc_test.go` walks every `BoxIn` and `VecIn` method; pointing `VecIn.try_reserve` at `heap_alloc` fails it (`VecIn.try_push -> VecIn.try_grow -> VecIn.try_reserve -> core.heap_alloc`) |
| R6 markers | `TestAllocContainerMarkers` (both types, `Send`, `Share`, `Interior`, four element/allocator cases each, never `Copy`; making `Interior` "both" fails four cases); `tests/negative/vecin_send` and twin |
| R7 freestanding | `tests/freestanding/vecin`: 256 elements in a 4,096-byte budget, refused at the next growth, released to zero; emulation and both QEMU targets (`KERN_REQUIRE_QEMU=1`) |

**R8.** Baseline: `scripts/capture-ll.sh` with a compiler built from
7e6d609 in a temporary worktree before any prelude edit: 286 `.ll`
files and 42 failure lines, read-only. After: all 286 byte-identical,
none missing; the 42 failure lines present and no other baseline
program added. New: 16 `.ll` files (the four `vecin_*` run fixtures and
the four trap fixtures, both opt levels) and 2 failure lines, both
`vecin_guard`, a test-hooks fixture the capture builds without
`--test-hooks`.

**Documents.** `docs/ownership.md` rule 5.6.5; spec §6's allocator
paragraph notes what is implemented; ADR-011 note; `docs/unsafe-audit.md`
gains the 14 functions with unsafe blocks (67 functions, 71 blocks; the
raw count of blocks in `std/core.kern` is 71, up from 57).

## Open items

- `PoolIn`, `ArenaIn`, an allocator-generic `String` and `Map`. *(2026-11-04:
  `PoolIn` and `ArenaIn` exist, `docs/reports/alloc-containers-more.md`;
  their `alloc_with` forms, `get2`, `String` and `Map` remain.)* *(Later
  on 2026-11-04: `map.MapIn` exists, `docs/reports/map-in.md`; `String`
  and the `alloc_with` forms and `get2` remain.)*
- Sharing one allocator between containers: today through a handle type;
  an `impl Allocator for Shared[Mutex[A]]` would make it a library type.
- A failed `BoxIn.try_new_in` destroys the allocator it was given; with
  an owned allocator (not a handle) that loses it. A form that hands the
  allocator back on failure is a design question for the owner.
- `realloc` in the `Allocator` interface would let a vector grow in place.

## Review

- **speccritic** (gpt-6.1-sol, ADR-011 as context), three rounds: no
  critical finding; warnings folded in (the no-`oom` boundary, the
  counters R4 holds unchanged, the guard's predicate, zero-size elements,
  destruction order, mechanism in normative text).
- **plancritic** (davin-go), four rounds: no critical finding; added edge
  fixtures, the table-driven marker test, the guard hook, R8's frozen
  manifest and staging before review.
- **prism** (default settings, code paths, medium reasoning effort): one
  medium and six low findings. Adopted: return the allocator's own error
  (`Err(e)`) rather than a fresh `OutOfMemory` (both sites), a clearer
  comment in the R5 test, a nil check on `Atomic` in the marker test. Not
  adopted: a null check on `alloc`'s `Ok` pointer (a null success breaks
  the allocator's §6 obligation; `Vec`'s check exists because
  `heap_try_alloc` encodes failure as null); moving `seed_len` out of the
  prelude (`Pool.seed_generation` is the precedent, guarded by
  `testing.require_hooks`); "the guard cannot be reached" (`vecin_guard`
  reaches it: a zero-size element's byte size is 0).
- **realitycheck** PARTIALLY_ALIGNED, score 100, no warning; **verifier**
  offline: 40 low findings, none on this change; clarion skipped (no
  `docs/clarion-meta.json`). `golangci-lint` and
  `KERN_REQUIRE_QEMU=1 go test ./...` clean; R8 re-run after the prism
  fixes.
