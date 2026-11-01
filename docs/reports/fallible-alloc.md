# Fallible allocation: `try_` forms of `Box`, `Vec` and `String`

**Date:** 2026-11-01. **Spec:** `specs/fallible-alloc/SPEC.md` v0.1.
**Plan:** `specs/fallible-alloc/PLAN.md`. **Prompt:** a reader of the
kern article argued that "stop and report it precisely" rules kern out
of kernel work, where the system must survive as many errors as it can.
Checking the library against that showed a narrower, real gap: allocation
failure is an error everywhere in the design (spec §6, ADR-006) except in
the convenience operations, which trapped `oom` with no alternative.

## Before

`SystemAllocator.alloc`, `Pool.alloc`, `Arena.alloc` and the `alloc_with`
forms returned `Err(OutOfMemory)`, and the freestanding allocator never
trapped on exhaustion (rule 6.8). `Box.new`, `new_with`, `new_with_ctx`,
`Vec.with_capacity`, `reserve`, `push`, `extend`, `resize`, and
`String.from`, `push`, `push_str` and the formatters all trapped `oom`,
through the prelude's `heap_alloc` / `heap_realloc` / `heap_alloc_zeroed`
or `Vec`'s own unrepresentable-size and growth-guard checks. A program
that had to survive allocation failure had to write its own container
over the unsafe allocator. Separately, an append into spare capacity
already made no allocation call, but nothing said so and the capacity was
not observable.

## The library

All of it is kern code in the prelude (`core.kern`, `str.kern`, mirrored
in `std/`), beside the operations it mirrors; no existing body changed.

- **Helpers.** `heap_try_alloc_zeroed` joins `heap_try_alloc` and
  `heap_try_realloc` (null on failure); `Vec.try_grow` and `try_grow_to`
  mirror `grow` and `grow_to` with the `2^63 - 1` guard returning `Err`.
- **`Vec`.** `capacity()`; `try_reserve` is `reserve` writing `data` and
  `cap` only after the allocation succeeds (a failed `realloc` leaves the
  old buffer valid); `try_with_capacity` reserves into a fresh vector;
  `try_push` destroys its value on `Err`, as `Pool.alloc` does;
  `try_extend` adds with `checked_add`, so a length sum past `usize` is
  `Err`, not an `overflow` trap; `try_resize` (`T: Copy`, so copies of `x`
  run no element code) keeps `resize`'s zeroed-storage path and runs
  every fallible step before writing the vector.
- **`Box`.** `try_new` destroys its value when the allocation fails;
  `try_new_with` and `try_new_with_ctx` return `Alloc(OutOfMemory)` before
  the initializer is called and `Init(e)` for the initializer's own
  failure, freeing the allocation, as `new_with` does.
- **`String`.** `capacity`, `try_from`, `try_reserve`, `try_push` and
  `try_push_str` call the `Vec` forms on the byte vector and add no logic.

The guarantee that makes the forms a complete pattern is spec §2's
"reserve, then append": after `try_reserve`, the convenience appends
(including every `String` formatter) cannot allocate or trap `oom`, so a
program keeps a value it must not lose by reserving first.

## Verification

| Requirement | Evidence |
|---|---|
| R1 `Box` | `tests/run/box_try_new` (all four configurations), `box_try_new_oom` (hooks: `try_new`'s value destroyed once, the `_with` initializers never run) |
| R2 `Vec` success | `tests/run/vec_try`: 17 mirrored pairs print equal len, capacity and checksum; capacities are spec §2's |
| R3 `Vec` failure | `tests/run/vec_try_oom` (hooks, including the reallocation and zeroed paths); `vec_try_size` (sizes past `usize`, a length sum past `usize`, the growth guard; zero-size `Vec[Z]` at length 2^63 and 2^64 - 1, ending with `exit` because destroying them would not finish) |
| R4 no `oom` trap | `compiler/analysis/try_alloc_test.go`: walks the unoptimized call graph from all twelve `try_` methods; its `Vec.push` control must reach an `oom` trap. Mutation checks: pointing `try_reserve`, then `Box.try_new`, at the trapping allocator fails the test with the chain (`Vec.try_push -> Vec.try_grow -> Vec.try_reserve -> core.heap_alloc`) |
| R5 reserve, then append | `tests/run/vec_reserved_append`: each part arms the hook once, appends with every form, and one `Box.try_new` probe still fails; the `String` part's 98 reserved bytes are exactly filled, so each formatter's bound is tight |
| R6 `String` | `tests/run/string_try`, `string_try_oom` |
| R7 freestanding | `tests/freestanding/try_alloc`: the emulation and both QEMU targets print the same (run with `KERN_REQUIRE_QEMU=1`) |
| R8 identity | below |
| R9 engines agree | the fixture harness; `TestPreludeSync` for both files |
| `T: Copy` on `try_resize` | `tests/negative/vec_try_resize_copy` (`CONSTRAINT_UNSATISFIED`) and its accepting twin |

**R8.** The baseline is `scripts/capture-ll.sh` run with a compiler built
from 207248d in a temporary worktree, before any prelude edit: 278 `.ll`
files and 34 failure lines, kept read-only as the manifest. The capture
with this change: every one of the 278 files is byte-identical to its
namesake (`cmp`), none is missing, and the 34 failure lines are all
present with no other baseline program added. The new capture also holds
this change's fixtures, listed apart: 8 `.ll` files (`box_try_new`,
`string_try`, `vec_try`, `vec_try_size`, at both opt levels) and 8
failure lines, all `//! test-hooks: true` fixtures (`box_try_new_oom`,
`string_try_oom`, `vec_reserved_append`, `vec_try_oom`), which the
capture builds without `--test-hooks`, as it does the existing ones.

## Found while planning

- A zero-size element type works (`struct Z { }`), and `resize` of
  a fresh `Vec[Z]` reaches any length at once through the zeroed-storage
  path, in both engines; its destructor then destroys every element, so
  such a program must end with `exit`. Spec §6 had listed this as a risk.
- `show-allocations` needs no table entry for the new methods: it already
  reports any library call whose effect summary allocates.
- An owned `Result` holding a `Vec`, `Box` or `String` cannot be matched
  (`OWN_DESTRUCTURE_DESTRUCTOR`); the fixtures unwrap it with `?` in a
  helper.

## Open questions for the owner

- **The effect schema.** `show-effects` reports a `try_` form as
  `traps: true`, because `SystemAllocator.alloc` checks its §6
  obligations with an `explicit` trap. Telling "may trap `oom`" from other
  traps needs a §16 schema change.
- **`map.Map`.** Insertion rehashes into a new table, so a `try_insert`
  needs a fallible rehash: a design of its own.
- **Runtime objects.** `Shared.new`, `Mutex.new`, `Condvar.new` and the
  spawns allocate in the C runtime (`kern_alloc`, which traps); fallible
  forms are a runtime ABI change.
- **Merging the bodies.** The trapping operations could be rewritten over
  the `try_` forms to remove the duplication, if a new R8 comparison shows
  no change in generated code. Until then `vec_try`'s paired lines are
  the guard against the two drifting apart.

## Review

- **speccritic** (gpt-6.1-sol, five rounds): two critical issues in the
  first (the "no initializer has run" rule contradicted `Init(e)`; R4
  covered user code), fixed; the fifth round had no model finding.
- **plancritic** (davin-go, five rounds): EXECUTABLE_AS_IS, score 100,
  every requirement covered.
- **Phase 1** (prism, default settings; realitycheck; verifier): prism's
  code-only run raised one valid issue (the R4 walk shared a path slice
  between sibling calls), fixed; its other findings did not hold against
  the code (a "missing" `heap_try_realloc` that exists, a checksum that
  Python reproduces). realitycheck PARTIALLY_ALIGNED 72 with no drift
  (the missing items were later phases'); verifier had nothing on the new
  code.
- **Phase 2:** prism found one style issue (a no-op sort in the R4 test),
  fixed; realitycheck 86, no drift; verifier nothing on the new code.
  prism's responses cut off on the spec and plan, so code was reviewed
  with `--paths 'compiler/**,std/**,tests/**'`.
- **Phase 3:** prism (on the freestanding fixture and the documents) had
  two low findings: name the reservation arithmetic in the freestanding
  fixture, done; spell unused match bindings `_`, not adopted, since
  every prelude and fixture match names them. realitycheck
  PARTIALLY_ALIGNED with score 100 and no warning (the remaining items
  are UNCLEAR only because its index does not show `.kern` bodies);
  verifier offline: 40 low findings, none on this change. `clarion pack`
  skipped (no `docs/clarion-meta.json`). `golangci-lint run ./...` and
  `KERN_REQUIRE_QEMU=1 go test ./...` clean.
