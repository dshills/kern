# Trap kinds in `show-effects`, and a fallible `Map` insert

**Date:** 2026-11-01. **Spec:** `specs/effect-trap-kinds/SPEC.md` v0.1.
**Plan:** `specs/effect-trap-kinds/PLAN.md`. **Item:** the second of the
ten-item list ("a compiler-checked guarantee that code cannot trap
`oom`: split `oom` out of the effect summary, and add `Map.try_insert`").

## Before

`show-effects` reported one `traps` flag per function. Every `try_` form
set it, because `SystemAllocator.alloc` checks its §6 obligations with an
`explicit` trap, so the summary could not tell a fallible form from a
trapping one. `std/map` had no fallible insert. And the flag was wrong in
one place: the runtime object operations (`Shared.new`, `Mutex.new`,
`Condvar.new`, `thread.scope`, `thread.spawn`, `scope.spawn`) allocate
through `kern_alloc`, which traps `oom`, natively and in the
interpreter's `runtimeAlloc`, but were not in the analysis's `trapping`
table, so a function that only created a `Mutex` was reported
`traps: false`.

## The analysis and the wire

- **Kind sets.** `effects` carries a twelve-bit set over spec §6's kinds
  without `stack` (every prologue can raise it). `CheckBounds` adds
  `bounds`; `CheckOverflow` adds `overflow`, or `div_zero` (and
  `overflow` for a signed type) for `/` and `%`, `shift` and `overflow`
  for `<<`, `shift` for `>>`; `CheckConv` adds `conversion`; a `Trap` its
  kind; an `Unreachable` terminator `unreachable`, unless a `Trap`
  precedes it in its block. The `trapping` table maps each intrinsic to
  its kinds, now including the runtime object operations (`oom`); a call
  through a function value, into FFI or of `asm` adds every kind. Sets
  join by union in the existing fixed point; `traps` keeps its meaning.
- **The dead terminator.** `trap()` lowers to a `Trap` followed by an
  `Unreachable` terminator that can never run; the first version listed
  `unreachable` for it, which the per-source test caught.
- **Schema 0.4.** `FunctionEffects.trap_kinds` exists at 0.4 only.
  `diag.WriteAt` writes a `ShowEffectsResult` at 0.2 or 0.3 through a
  copy without the field, as it already copied diagnostics to drop
  `message` at 0.2. `schema.json` became the 0.4 schema (`trap_kinds`
  required, an enum of the twelve kinds); `schema.md` is regenerated.
  The doc-map entry was placed at the end of the map behind a blank line,
  so `gofmt` did not realign fifty unrelated lines.

## `Map`

`try_with_bits` is `with_bits` over `Vec.try_resize`, `try_reserve` and
`try_push` (plain `push` after a reservation does not allocate, but its
growth path is statically reachable and would put an `oom` trap in the
call graph). `try_insert` makes `insert`'s growth decision and calls
`try_rebuild`, which builds the new table first and moves the entries
with `place`, `insert`'s slot write without the growth check; the room
argument (a same-size rebuild only when `count + 1 <= cap / 2`, a
doubled table under 3/8 full) means the move never grows. On failure
the key and then the value are destroyed and the map is unchanged.

## Verification

| Requirement | Evidence |
|---|---|
| R1 versions | `compiler/diag`: 0.4 validates, 0.3 has no `trap_kinds`, 0.2 no `message`, the caller's result untouched; `cmd/kern`: `show-effects` at 0.2, 0.3 and 0.4, a missing or unsupported version and a malformed request answered at 0.4, files on the command line at 0.4. Against the baseline compiler (`ab9112a`), `show-effects` at 0.2 and 0.3 is byte-identical for all 160 run fixtures that create no runtime object (320 comparisons) |
| R2, R3 contents | `TestTrapKindsPerSource`: one program per source row (19 cases, the six runtime object operations separately, a function-value call listing all twelve), a caller's union in §6 order, an `Unreachable` terminator |
| R4 no `oom` | `TestFallibleFormsListNoOOM`: every `try_` form of `Box`, `Vec`, `String`, every `BoxIn`/`VecIn` method and `Map.try_new`/`try_insert` list no `oom`; `Box.new`, `Vec.push`, `Map.insert` do. The `_with` constructors call the caller's initializer through a function value and so report every kind with label `unknown` (element code); their own code is covered by `TestTryFormsReachNoOOMTrap`, which now walks the `Map` forms too |
| R5 table | `TestTrappingMatchesRuntime`: 20 trap sites named in `kern_rt.c` and the interpreter's intrinsics, each found by name and present in the table with its kind |
| R6 `Map` | `tests/run/map_try` (72 paired steps of `insert` and `try_insert` through two doublings and a same-size rebuild, equal replaced values, `len()` and checksums); `tests/run/map_try_oom` (test hooks: `try_new` fails; a `try_insert` that must rebuild fails, destroying key 99 then value 99, with `len()` and the entries unchanged) |

## Open items

- `kern_alloc` still traps; fallible runtime objects are a C ABI change.
  *(2026-11-04: the `try_new` forms of `Shared`, `Mutex` and `Condvar`
  allocate without it, `docs/reports/fallible-runtime-objects.md`.)*
- `Map.try_reserve` and an allocator-generic `Map`.
- Per-call-site kinds (today a function's kinds are the union over its
  body).

## Review

- **speccritic** (gpt-6.1-sol, `specs/diag-message` as context), three
  rounds: one critical finding (byte-identical 0.3 output contradicted
  the corrected `traps` of R3; resolved: the correction applies at every
  version), warnings folded in.
- **plancritic** (davin-go), three rounds: no critical finding.
- **R7.** Baseline: `scripts/capture-ll.sh` with a compiler built from
  ab9112a before any prelude edit (302 `.ll` files, 44 failure lines).
  After: all 302 byte-identical, the 44 failure lines present, no other
  baseline program added; new: `map_try` (2 `.ll` files) and
  `map_try_oom` (2 failure lines, a test-hooks fixture).
- **prism** (default settings, code paths, medium effort): one medium and
  ten low findings. Adopted: `allKinds` derived from `trapKinds` with a
  test that the set fits; the empty-`Release` check before the lookup;
  a comma-ok name assertion in the version test; one shared list of
  fallible forms for the two analysis tests; checking every lowered
  function of a name rather than one picked by map order; a test pinning
  the pre-0.4 shape to `FunctionEffects`. Not adopted: an explicit `move`
  of `v` in `try_rebuild` (passing an owned local moves it; the code is
  `rebuild`'s, and `map_try_oom` shows each value destroyed once);
  sharing `place` with `insert` and `place_opt` with `insert_opt` (the
  spec leaves the trapping forms untouched, R7); `try_push` per element
  after the reservation (`push` there would put an `oom` trap in the call
  graph); reassigning `self` (as `rebuild` does).
- **realitycheck** PARTIALLY_ALIGNED, score 100, no warning.
  **verifier** offline: 25 low findings; the two on changed code ask
  for tests of `WriteAt` and `Effects`, which `TestWriteAtVersions`,
  `TestEffectsShapeByVersion`, `TestTrapKindsPerSource` and
  `TestFallibleFormsListNoOOM` already are (its match is by name).
  clarion skipped (no `docs/clarion-meta.json`). `golangci-lint` and
  `KERN_REQUIRE_QEMU=1 go test ./...` clean.
