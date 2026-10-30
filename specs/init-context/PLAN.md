# Plan: in-place initializers with a context argument

**Spec:** `specs/init-context/SPEC.md` v0.1. **Profile:** davin-go.
**Review status.** plancritic (davin-go, with the spec):
EXECUTABLE_AS_IS, score 100, every requirement covered.
**Order:** three phases, each ending with `golangci-lint run ./...` and
`go test ./...` green; one commit after Phase 3 unless the owner asks
for more.

**Baseline (before Phase 1):** build a scratch compiler from the current
commit (`go build -o <scratch>/kern-base ./cmd/kern` in a clean
worktree of HEAD) and capture its LLVM with `scripts/capture-ll.sh
<scratch>/kern-base <scratch>/ll-base`, for R8.

## Phase 1: the intrinsic (R1, R9)

**Files.** `compiler/resolve/prelude/core.kern`, `std/core.kern`,
`compiler/interp/intrinsics.go`, `compiler/backend/llvm/calls.go`,
`compiler/analysis/effects.go`.

**Steps.**

1. Declare `ptr_init_ctx` (spec §2) after `ptr_init` in both prelude
   copies, with a comment stating R1; the two files stay identical
   (the existing prelude-mirror test checks this).
2. Interpreter: `"ptr_init_ctx"` builds the same edit access on the raw
   storage as `ptr_init` and calls the initializer (argument 1) with
   that access and argument 2, the context access, as passed.
3. Backend: `case "ptr_init_ctx"` requires argument 1 to be a non-closure
   `*resolve.Func` (as `ptr_init` does) and emits
   `directCall(dsts, ft.Ret, fe.v(a[1]), "ptr "+fe.v(a[0])+", "+fe.typedArgs(a[2:3]))`,
   so the context is rendered exactly as a call's view argument is.
4. Effects: the condition that treats `ptr_init` as a call through a
   function value also matches `ptr_init_ctx`.
5. Negative fixture `tests/negative/ptr_init_ctx_safe.kern`: a call
   outside `unsafe`, expecting the code a `ptr_init` call outside
   `unsafe` gets (found by running the existing rule on a probe first).

**Acceptance.** The negative fixture passes; a scratch probe calling
`ptr_init_ctx` inside `unsafe` runs in both engines with the same
output.

## Phase 2: the methods and their fixtures (R2–R7)

**Files.** both prelude copies; `tests/run/box_new_with_ctx.kern`,
`pool_alloc_with_ctx.kern`, `arena_alloc_with_ctx.kern`,
`pool_alloc_with_ctx_oom.kern` and their `.out` files;
`tests/negative/alloc_with_ctx_conflict.kern` and
`tests/negative/ctx_conflict_user.kern`; the analysis test file that
holds the `_with` copy and effects tests (`compiler/analysis`).

**Steps.**

1. Write `Box.new_with_ctx`, `Pool.alloc_with_ctx` and
   `Arena.alloc_with_ctx` as copies of their `_with` methods with
   `ptr_init_ctx(…, init, ctx)` in place of `ptr_init(…, init)`, the
   same contracts plus nothing, in both prelude copies.
2. Run fixtures per spec §4 item 1; each `.out` is written by hand from
   the expected behavior, then compared with both engines' output (the
   harness runs every `tests/run` fixture in both engines and both opt
   levels; the oom fixture carries `//! test-hooks: true` like
   `pool_alloc_with_oom` and runs in debug only).
3. Negative fixtures per spec §4 item 2: first probe which code the
   checker gives a user function `f(p: edit[Pool[T]], c: view[Pool[T]])`
   called as `f(p, p)`-shaped arguments, then expect that code in both
   fixtures; each negative fixture names a twin that is accepted, as the
   negative fixtures do.
4. Analysis tests: extend the in-place-init analysis test (the one that
   lowers `large_object_out`) with a program building a 4,096-byte
   `Page` through each `_with_ctx` method, asserting no `Page` copy in
   both opt levels (R6), and assert `show-effects` labels a function
   calling `ptr_init_ctx` `unknown` (R9).

**Acceptance.** `go test ./tests/... ./compiler/analysis/...` green;
the four run fixtures pass in every configuration they run in.

## Phase 3: R8, documents, report

**Files.** `docs/ownership.md`, `tests/adversarial/NOTES.md`,
`docs/reports/in-place-init.md`, `docs/decisions/ADR-005-out-convention.md`,
`docs/decisions/ADR-010-function-pointers-and-closures.md`,
`docs/reports/init-context.md` (new).

**Steps.**

1. R8: `scripts/capture-ll.sh` with the new compiler into a second
   scratch directory; compare every module the baseline built with the
   new capture, excluding the new fixtures; any difference fails R8.
2. Documents per R10.
3. The report: what was added, R8's module count and result, the
   fixtures, reviews.

**Acceptance.** R8 shows every compared module byte-identical; lint and
the full suite are green.

## Risks

- **The backend's argument rendering.** A view of a scalar `C` and a
  view of a struct must render as the initializer's parameter expects.
  Using `typedArgs` on the same IR value a direct call would pass keeps
  them consistent; the run fixtures use a struct context and the probe a
  scalar one.
- **Generic instantiation of an unused method.** If the prelude's new
  methods were instantiated for programs that do not call them, R8
  would fail; generic methods are instantiated only at use, which R8's
  comparison confirms.
