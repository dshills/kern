# Plan: in-place initializers

**Spec:** `specs/in-place-init/SPEC.md` v0.1. **Profile:** davin-go.
**Order:** the phases depend on each other in order; each ends with
`golangci-lint run ./...` and `go test ./...` green; the change lands as
one commit after Phase 3 unless the owner asks for more.

**Baseline (done, 2026-10-30):** the R8 corpus is the capture of
5a6114d (the commit before this change): build `./cmd/kern` at 5a6114d
(the scratch binary `kern-tc` was built from that tree), run
`scripts/capture-ll.sh <that binary> <dir>`; the new compiler is
captured the same way, and every baseline `.ll` file is compared with
`cmp` against its namesake, the two `failed.txt` with `diff`, and the
new capture's extra modules (this change's fixtures) are listed apart.

## Phase 1: the intrinsic and the `out` fix (R1, R2, R10)

**Files.** `compiler/resolve/prelude/core.kern` (and `std/core.kern`),
`compiler/interp/intrinsics.go`, `compiler/backend/llvm/calls.go`,
`compiler/analysis/effects.go`, `compiler/lower/lower.go`,
`tests/negative/ptr_init_outside_unsafe{,_ok}.kern`,
`tests/run/out_whole_then_err.{kern,out}`, an analysis test.

**Steps.**

1. Declare `ptr_init[T, E](p: RawPtr[T], init: func(out T) -> Result[(), E])
   -> Result[(), E]` beside `ptr_edit`, with its obligation; the
   resolver's `ptr_` rule makes it unsafe (R1).
2. Interpreter: `ptr_init` opens an edit access on the raw storage
   (`rawRef`, as `ptr_edit`) and calls the function value with it.
3. Backend: `ptr_init` is an indirect call through the function value
   with the raw pointer as its one argument (an `out` parameter is a
   pointer); a closure-typed or non-function argument is an emitter
   failure (the checker never produces one).
4. Effects: a call to `ptr_init` is labelled like a call through a
   function value (R2).
5. Lowering: before the whole-value drop of an `out` parameter on an
   error path, emit `init` of the parameter (R10).
6. Tests: the negative pair; `out_whole_then_err`; an analysis test that
   `show-effects` labels a caller of `Box.new_with` `unknown` (after
   Phase 2 adds it; the test lands with Phase 2).

**Acceptance.** `go test ./...` green.

## Phase 2: the methods and their fixtures (R3–R7)

**Files.** `compiler/resolve/prelude/core.kern` (and `std/core.kern`);
`tests/run/box_new_with`, `pool_alloc_with`, `arena_alloc_with`,
`pool_alloc_with_oom` (`.kern`, `.out`); `tests/trap/box_new_with_trap.kern`,
`tests/trap/pool_alloc_with_stale.kern`; `tests/adversarial/large_object_out.kern`;
`compiler/analysis/analysis_test.go`.

**Steps.**

1. `Box.new_with[E]`, `Pool.alloc_with[E]`, `Arena.alloc_with[E]` as in
   spec §2, each with a contract stating the initializer's obligation.
   Pool: the slot is taken as `alloc` takes it, its meta stays vacant
   until `init` succeeds, and on `Err` the index is pushed back on the
   free list (room exists: a slot comes off the list, or is appended only
   when the list is empty). Arena: `size` advances only on `Ok`.
2. Fixtures as R3–R5 list them; `pool_alloc_with_oom` uses `//!
   test-hooks: true` (debug only, both engines, as the harness runs test
   hooks).
3. `large_object_out.kern`: add a `Box[Page].new_with` and a
   `Pool[Page].alloc_with` path beside the local one; an analysis test
   lowers it and asserts `show-copies` has no `Page` entry (R6), and the
   effects test of Phase 1.
4. Run every new fixture through the harness in all its configurations
   (R7).

**Acceptance.** `go test ./...` green.

## Phase 3: identity, documents, review (R8, R9)

**Files.** `docs/ownership.md` (rule 5.6.1), `docs/decisions/ADR-005-out-convention.md`
(experiments), `tests/adversarial/NOTES.md` §9,
`docs/reports/multi-module-sweep.md`, `docs/reports/in-place-init.md`.

**Steps.**

1. R8 comparison as in the baseline note.
2. Documents of R9; the report records the open question of spec §3
   (initializers with context) for the owner, the R10 defect, and the
   review.
3. Review: `golangci-lint`, `go test ./...`, `prism review staged`,
   `realitycheck check` with this spec and plan, `verifier analyze` on
   the touched packages; clarion skipped (no `docs/clarion-meta.json`).
4. Commit `feat(core): in-place initializers Box.new_with,
   Pool.alloc_with and Arena.alloc_with`, ask to push.

**Acceptance.** Reviews clean or justified; commit made.
