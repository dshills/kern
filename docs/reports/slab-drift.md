# `Slab[T]`: named in the spec, absent from the library

**Date:** 2026-10-29. **Raised by:** the feature sweep of the same day
(`docs/reports/multi-module-sweep.md`, "Found, not done"). **Status:** option
(a) chosen by the owner on 2026-10-29 and applied in the commit that
carries this line: the name is dropped from the spec, the types reference,
the ownership doc, the ADR, the plan and the two compiler lists, with
`Pool` named as the container that retires slots. The Phase 0 evaluation
(`specs/initial/ARCHITECTURE_EVALUATION.md`) keeps its wording as the
historical record.

## What the documents say

| document | line | text |
|---|---|---|
| `specs/initial/AI_Native_Systems_Language_SPEC.md` | 150 | §5.6: "`Arena[T]` (bump, freed as a whole), `Pool[T]` (free list with generation counters), and `Slab[T]` are standard-library owned containers with audited unsafe internals." Every rule that follows (handles, dereference order, validity, widths under ADR-14) names only `ArenaId[T]` and `PoolId[T]`. |
| same | 271 | §13 accepted WARN 1: "`Arena`/`Pool`/`Slab` construction and insertion signatures (§5.6) — Phase 1 standard-library reference." |
| `spec/types.md` | 20 | `Slab` in the list of built-in names of the implicit module `std/core`. |
| `docs/ownership.md` | 424 | the §5.6 sentence, verbatim. |
| `docs/decisions/ADR-002-arenas-and-handles.md` | 38, 120, 130 | the same sentence; "PLAN.md 1.2 and 1.4: `Arena`/`Pool`/`Slab` intrinsics with the check order, tag counter and …"; "`Arena[T]`, `Pool[T]` and `Slab[T]` are owned library containers". |
| `specs/initial/PLAN.md` | 136 | Phase 1 library intrinsics: "… `Pool` (with generation, vacancy and identity checks, identity tag counter and tag-0 rule, `get2` validation order), `Slab` (including generation-wrap retirement, tested in Phase 1 through the `kern_test_pool_seed_generation` interface …), …"; and "Construction and insertion signatures for `Arena`/`Pool`/`Slab` are fixed here, closing spec §13 accepted gap 1: `new(own A) -> Self`; `alloc(edit self, own T) -> Result[Id, AllocErr]`; `alloc_with(…)`". |
| `specs/initial/ARCHITECTURE_EVALUATION.md` | 144 | the Phase 0 evaluation, historical. |

No document defines what a `Slab` is beyond its name: no `SlabId`, no
dereference, free or growth rule, no width, no distinguishing property.
The one behaviour PLAN.md ascribes to it, retirement of a slot whose
generation would wrap, is tested "through the `kern_test_pool_seed_generation`
interface", that is, on a pool.

## What the library has

`std/core.kern` declares `Box`, `Vec`, `Pool` (with `PoolId`), `Arena`
(with `ArenaId`), `Shared`, `Mutex`, `Condvar`, `Atomic`, `Thread`,
`Join`, `Scope`, `SystemAllocator`, `thread`, `trap` and `testing`. There
is no `Slab` and no `SlabId`. Generation-wrap retirement is `Pool`'s:
`PoolSlotMeta { gen: u32; live: bool; retired: bool }` (line 474), set
in `Pool.free` when the generation would wrap (line 643), with the Phase 1
report recording "`Pool` (validation order bounds → identity → vacant →
generation, generation retirement at `u32` wrap, `get2` with the §13
order)" (`docs/reports/phase-1.md` line 32) and the Phase 2 report "the
generation-retirement fixture pass[es] natively and in the interpreter".

## What happened

`git log --all -S'Slab'` finds seven commits, none of which adds or
removes a declaration:

- d46938d, 4378e58, df85d0e, 5306209 (2026-10-26) wrote the evaluation,
  the spec, the plan and the reference, each naming `Slab` beside `Arena`
  and `Pool`.
- 164d988 (Phase 2, 2026-10-26) added the string `"Slab"` to
  `compiler/layout/layout.go`'s list of opaque core types; b1fef21
  (2026-10-27) carried it into `compiler/resolve/decls.go`'s `outOfLine`
  list while moving `Atomic` out. Both entries are dead: no core type of
  that name exists for them to match.
- The Phase 1 report lists the containers implemented and does not name
  `Slab`, and no phase report records a deferral.

So `Slab` was never implemented, and the plan's one concrete requirement
for it was met by `Pool`. The documents carry a name that nothing
defines; `realitycheck` and the phase reviews did not flag it because the
plan's acceptance criteria name Pool's fixture, not a Slab.

## Options

### (a) Remove the name; Pool carries retirement

Spec changes are the owner's; the edits, file by file:

- `specs/initial/AI_Native_Systems_Language_SPEC.md` line 150: "`Arena[T]`
  (bump, freed as a whole) and `Pool[T]` (free list with generation
  counters; a slot whose generation would wrap is retired) are
  standard-library owned containers …"; line 271: "`Arena`/`Pool`
  construction and insertion signatures".
- `spec/types.md` line 20: drop `Slab` from the built-in names.
- `docs/ownership.md` line 424: the sentence as on spec line 150.
- `docs/decisions/ADR-002-arenas-and-handles.md` lines 38, 120, 130: the
  same two-container wording.
- `specs/initial/PLAN.md` line 136: move "(including generation-wrap
  retirement, tested … `kern_test_pool_seed_generation` …)" into `Pool`'s
  parenthesis and drop "`Slab`"; "signatures for `Arena`/`Pool` are fixed
  here".
- `specs/initial/ARCHITECTURE_EVALUATION.md` line 144: leave, as the
  record of the Phase 0 evaluation, or add "(`Slab` was folded into
  `Pool`'s retirement in Phase 1)".
- `compiler/layout/layout.go` line 123 and `compiler/resolve/decls.go`
  line 1063: delete `"Slab"` from both lists (dead entries; one commit,
  `go test ./...` unchanged).

### (b) Add a `Slab[T]` to the library

The spec would first have to say what a slab is, since nothing today
distinguishes it from `Pool`. The nearest meaningful reading of the name
is a fixed-slot container without generation counters: a `SlabId[T]` of
the slot index alone (4 bytes in every build, plus the identity tag in
debug), dereference checking bounds, identity and vacancy but not
generation, so that a freed-and-reused slot is reachable through an old
handle (a logic error, memory-safe, as §5.6's validity paragraph already
frames handle misuse). That is a new spec decision (§5.6, ADR-14's width
table, `spec/types.md`), then: `Slab[T]`, `SlabId[T]` (`//@debug` and
`//@release` variants), `new`, `alloc`, `get`, `at`, `get2`, `free`,
`len`, `release` and a destructor in `std/core.kern` over the raw-pointer
core, in the pattern of `Pool` (about 200 lines); `Slab` and `SlabId` in
`compiler/layout`'s and `compiler/resolve`'s tables as `Pool` is; entries
in `docs/unsafe-audit.md`; fixtures under `tests/run` and `tests/trap`
(bounds, identity, vacant, `get2` disjointness) and a `show-layout`
check of the handle widths; `docs/ownership.md` §5.6 and the ADR. If the
spec instead gives `Slab` the same semantics as `Pool`, it is a second
name for one container and option (a) is the better outcome.

## Recommendation

(a). The plan's only requirement for `Slab` is met by `Pool`, the spec
gives the name no meaning of its own, and adding a container to a public
language for the sake of a name would add surface without a use.
