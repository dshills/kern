# Plan: fallible allocation (`try_` forms of `Box`, `Vec` and `String`)

**Spec:** `specs/fallible-alloc/SPEC.md` v0.1. **Profile:** davin-go.
**Order:** Phase 1 (`Vec`) comes first because `String`'s forms in
Phase 2 are built on it; Phase 2's `Box` part depends on nothing in
Phase 1 but lands with it to keep the phases at three. Each phase ends
with `golangci-lint run ./...` and `go test ./...` green and with
`std/core.kern` and `std/str.kern` byte-identical copies of their
`compiler/resolve/prelude/` sources (`TestPreludeSync`,
`compiler/resolve/prelude_sync_test.go`). The change lands as one
commit after Phase 3 unless the owner asks for one per phase; nothing
is pushed unless the owner asks.

**Review status.** plancritic (profile davin-go, with the spec), five
rounds: EXECUTABLE_AS_IS, score 100, coverage 15 covered and none
partial or uncovered. Fixed on the way: the R5 probe consumed the hook
twice, the R4 count (twelve methods), a negative fixture for
`try_resize`'s `T: Copy`, every `String` append form in R5, the review
tools' invocations and failure policy, and R8's fixed manifest. Its one
out-of-scope note is Phase 3's review tools, which are the owner's
standard workflow rather than a spec requirement.

**Baseline (before any prelude edit).** The R8 corpus is the capture of
207248d, the commit this work starts from. Build it with
`git worktree add <scratch>/base 207248d` and
`go build -o <scratch>/kern-base ./cmd/kern` inside that worktree, then
run `scripts/capture-ll.sh <scratch>/kern-base <scratch>/ll-base` from
the repository. `<scratch>` is any directory outside the repository that
survives until Phase 3 (the session scratchpad if the work stays in one
session; otherwise `~/.cache/kern-fallible-alloc`). The worktree is
removed after the capture (`git worktree remove`).

**Facts checked while planning (2026-11-01).**

- A zero-size element works: `struct Z { }` is accepted, and
  `Vec[Z].new()` followed by `resize(2^63, Z { })` sets `len()` to 2^63
  at once, natively at both opt levels and in the interpreter (`kern
  test --engine=interp`), through `resize`'s zeroed-storage path. The
  vector's destructor then destroys its elements one by one, which for
  2^63 elements never finishes, so a fixture that builds such a vector
  ends with `exit(0i32)` (the prelude's `exit`, which runs no
  destructors) after its last print. Spec §6's zero-size risk is
  therefore resolved: both R3 cases stay.
- The analysis already reports a call of any library function whose
  effect summary allocates as a `show-allocations` site, so the new
  methods need no entry in the allocation table
  (`compiler/analysis/analysis.go`, `allocating`).
- `testing.fail_next_alloc()` is consumed by `SystemAllocator.alloc`,
  `alloc_zeroed` and `realloc` in both engines
  (`compiler/interp/intrinsics.go`; natively `kern_sysalloc_*` through
  `kern_fail_next`), so it reaches every `try_` form.

## Phase 1: `Vec` (R2, R3, R4 for `Vec`, R5 for `Vec`)

**Goals.** `Vec.capacity` and the five `Vec` `try_` forms exist with the
semantics of spec §2, with fixtures for success, failure and the
reserve-then-append guarantee, and the R4 call-graph test exists and
passes for them.

**Files.** `compiler/resolve/prelude/core.kern` (copied to
`std/core.kern`); `tests/run/vec_try.{kern,out}`,
`tests/run/vec_try_oom.{kern,out}`, `tests/run/vec_try_size.{kern,out}`,
`tests/run/vec_reserved_append.{kern,out}` (its `Vec` part; Phase 2
adds the `String` part); `tests/negative/vec_try_resize_copy.kern` and
its accepting twin `tests/negative/vec_try_resize_copy_ok.kern` (twins sit beside the rejecting fixture, as `vec_extend_copy_ok` does);
`compiler/analysis/try_alloc_test.go` (new).

**Steps.**

1. Helpers beside the existing ones, none of them changed:
   `heap_try_alloc_zeroed(size, align) -> RawPtr[u8]` (null on failure,
   as `heap_try_alloc`), with the same contract text as
   `heap_alloc_zeroed`.
2. `Vec.capacity(view self) -> usize { return self.cap }`.
3. `try_reserve(edit self, n)`: `reserve`'s body with three changes:
   `elems_bytes[T](n)` `None` returns `Err(OutOfMemory)`; the
   reallocation uses `heap_try_realloc` and a null result returns
   `Err(OutOfMemory)` before `self.data` or `self.cap` is written (the
   old buffer stays valid by `realloc`'s contract); the first allocation
   uses `heap_try_alloc` likewise. The `elems_bytes(self.cap)` `None`
   branch stays `trap_kind(unreachable)`, as in `reserve` (it is not
   `oom`, and the capacity was representable when it was set).
4. `try_with_capacity(n)`: `v := Vec[T].new()`, `v.try_reserve(n)?`,
   `Ok(v)`; on `Err` the empty `v` is destroyed and holds no allocation.
5. `try_grow(edit self) -> Result[(), AllocErr]` and
   `try_grow_to(edit self, n) -> Result[(), AllocErr]`: `grow` and
   `grow_to` with the `2^63 - 1` guard returning `Err(OutOfMemory)` and
   `reserve` replaced by `try_reserve`.
6. `try_push(edit self, v: own T)`: when `self.size == self.cap`, call
   `try_grow`; on `Err` run `drop(v)` and return `Err(OutOfMemory)`
   (the `Pool.alloc` pattern); otherwise store as `push` does.
7. `try_extend(edit self, src) where T: Copy`: `checked_add(self.size,
   len(src))`, `None` → `Err(OutOfMemory)`; when the sum exceeds
   `self.cap` (and only then) `try_grow_to(total)?`; then the block
   copy and size update of `extend`.
8. `try_resize(edit self, n, x: own T) where T: Copy`: the shrinking
   loop of `resize` when `self.size > n` (no allocation, then `x` is
   destroyed and `Ok`); for `n > self.size`, every fallible step runs
   before any write to the vector: with no allocation (`self.cap ==
   0`), `elems_bytes(n)` `None` or a null `heap_try_alloc_zeroed` →
   `drop(x)`, `Err`; otherwise `try_reserve(n)`, `Err` → `drop(x)`,
   `Err`. Then `resize`'s zeroed-storage detection and fill. Each new
   method containing `unsafe` carries a contract block copied from its
   mirrored method.
9. Fixtures:
   - `vec_try.kern` (all four configurations): for each mirrored pair
     run on equal inputs (empty vector, full vector, vector with spare
     capacity; `reserve`/`try_reserve` below and above the capacity;
     `with_capacity`/`try_with_capacity` of 0 and of 10; `extend`/
     `try_extend` that fits, that needs doubling and that needs more
     than doubling; `resize`/`try_resize` from no allocation, growing,
     shrinking), print `len()`, `capacity()` and an element checksum
     for each side; the `.out` file shows the two lines of each pair
     equal, and the capacities match spec §2.
   - `vec_try_oom.kern` (`//! test-hooks: true`): for each failure of
     R3, print the state, arm `testing.fail_next_alloc()`, call, print
     the result tag and the state again; `try_push` and `try_resize` use
     an element or argument whose destructor prints, so the `.out` shows
     the destroyed argument's line once (for `try_resize`, a `Copy`
     element cannot have a destructor, so its `x` destruction is shown
     by the vector's unchanged state alone and the `try_push` case
     carries the destructor check).
   - `vec_try_size.kern` (no hooks, all four configurations):
     `Vec[u64].try_reserve(2^61 + 1)` and `try_with_capacity` of the
     same (8-byte elements, product past `usize`), `try_resize` of the
     same; then the zero-size cases: `Vec[Z]` grown by `resize` to
     `usize` max then `try_extend` of one element, and a fresh `Vec[Z]`
     resized to 2^63 (so `len() == capacity() == 2^63`) then
     `try_push`; each prints its `Err` and the unchanged `len()`; the
     program ends with `exit(0i32)` (see the planning facts).
   - `vec_reserved_append.kern` (`//! test-hooks: true`):
     `try_reserve(16)` on a `Vec[u64]`, arm the hook, then `push`,
     `try_push`, `extend`, `try_extend` and `resize`/`try_resize` that
     stay within 16; then exactly one probe allocation, which yields
     `Err` and so shows the hook was not consumed by the appends. In
     Phase 1 the probe is `Vec[u64].new().try_reserve(1)`; Phase 2
     replaces it with `Box.try_new(1u64)` (the probe consumes the hook,
     so a second probe after it would prove nothing).
   - `tests/negative/vec_try_resize_copy.kern` (`//! expect: reject
     CONSTRAINT_UNSATISFIED rule=T.4`, the header shape of
     `tests/negative/vec_extend_copy.kern`, with `//! twin:
     vec_try_resize_copy_ok`): `try_resize` on a `Vec[Box[u64]]` is
     rejected; the twin calls it on a `Vec[u64]` and is accepted. This
     pins spec §2's `T: Copy` restriction; `resize` on a
     `Vec[Box[u64]]` stays accepted, which existing fixtures already
     exercise.
10. `try_alloc_test.go` (R4): lower, without optimization, a program
    that calls each `try_` method at `T = u64` (and, from Phase 2,
    `E = ()`, `C = u64`), with `lowerProgram` from
    `alloc_internal_test.go`. From each `try_` method's IR function,
    walk statically resolved calls (`ir.Call` with `Callee` naming a
    function of the module), skip calls through a function value, and
    fail on a trap of kind `oom` in any reached function. The test first
    runs the same walk from `Vec.push` and requires it to find an `oom`
    trap, so a walk that misses the form `trap_kind(oom)` lowers to
    cannot pass vacuously.

**Key decisions.** Existing bodies are not edited (R8 trivially holds
for the prelude's existing code); the duplication is guarded by
`vec_try.kern`'s paired lines. `drop(v)` is explicit on every `Err`
path that holds an owned argument, as in `Pool.alloc`.

**Risks.** The checker may reject `drop(x)` on a path where `x` is
already moved, or require it where the compiler destroys `x` at scope
end anyway; the fixtures' destructor lines show which, and the code
follows whichever makes the line appear exactly once. `?` on
`try_reserve` inside a method returning `Result[(), AllocErr]` needs
the error types to match exactly (they do: both `AllocErr`).

**Acceptance.** The four run fixtures and the negative pair pass in
their configurations;
`try_alloc_test.go` passes, including its `Vec.push` control;
`golangci-lint run ./...` and `go test ./...` green.

## Phase 2: `Box` and `String` (R1, R4 and R5 completed, R6)

**Goals.** The three `Box` forms and the `String` forms exist; R4's test
covers every `try_` method of spec §2; R5's fixture covers the `String`
formatters.

**Files.** `compiler/resolve/prelude/core.kern` (copied to
`std/core.kern`); `compiler/resolve/prelude/str.kern` (copied to
`std/str.kern`); `tests/run/box_try_new.{kern,out}`,
`tests/run/box_try_new_oom.{kern,out}`,
`tests/run/string_try.{kern,out}`, `tests/run/string_try_oom.{kern,out}`;
`tests/run/vec_reserved_append.{kern,out}` (extended);
`compiler/analysis/try_alloc_test.go` (extended).

**Steps.**

1. `Box.try_new(v)`: `heap_try_alloc`; null → `drop(v)`,
   `Err(OutOfMemory)`; otherwise `new`'s store.
2. `Box.try_new_with(init)` and `try_new_with_ctx(init, ctx)`:
   `heap_try_alloc`; null → `Err(Alloc(OutOfMemory))` before `init` is
   called; otherwise `new_with`'s `ptr_init` / `ptr_init_ctx` match, with
   `Err(e)` → free, `Err(Init(e))`.
3. `String.capacity()` returns `self.bytes.capacity()`;
   `try_reserve(n)`, `try_push(b)` and `try_push_str(s)` call the `Vec`
   forms on `self.bytes`; `try_from(s)`: `r := String { bytes:
   Vec[u8].new() }`, `r.bytes.try_extend(s)?`, `Ok(r)`. `std/str`
   reaches `Vec`'s methods as it reaches `push` and `extend` today.
4. Fixtures:
   - `box_try_new.kern` (all four configurations): each form succeeds
     and its value is read back; `try_new_with` and `try_new_with_ctx`
     whose initializer fails yield `Init`.
   - `box_try_new_oom.kern` (`//! test-hooks: true`, the shape of
     `pool_alloc_with_oom.kern`): arm the hook before each of the three
     forms; `try_new` of a value whose destructor prints yields `Err`
     with that line once; the two `_with` forms yield `Alloc` and their
     initializers' print lines do not appear.
   - `string_try.kern` (all four configurations): `try_from`/`from`,
     `try_push`/`push`, `try_push_str`/`push_str`, `try_reserve`
     followed by `capacity()`, with equal output lines per pair.
   - `string_try_oom.kern` (`//! test-hooks: true`): each `String` form
     after arming the hook leaves the bytes and `len()` unchanged and
     yields `Err`.
   - `vec_reserved_append.kern`: replace the `Vec` part's probe with
     `Box.try_new(1u64)`, and add a `String` part with its own arming
     and its own single probe: `try_reserve` of
     `len() + 1 + 1 + 3 + 3 + 20 + 20 + 16 + 4 + 30`, arm the hook,
     then `push(65u8)`, `try_push(66u8)`, `push_str` and `try_push_str`
     of a 3-byte literal each, `push_u64(u64 max)`, `push_i64(i64 min)`,
     `push_hex(u64 max)`, `push_rune(0x1F600)` and
     `push_padded(7, 30, 32u8)` (the bound of `push_padded` with width
     30 is 30), every public `String` append form; then one `Box.try_new(1u64)`
     yields `Err`. Each part arms the hook once, appends, and probes
     once; nothing re-arms between a part's appends and its probe.
5. `try_alloc_test.go`: add the `Box` and `String` methods to the walk
   (with `E = ()`, `C = u64`; the initializer is a module function, so
   its call is a call through a function value and is skipped).
6. If `E` or `C` is not inferred for `try_new_with_ctx` (spec §6), the
   fixture writes the type arguments explicitly and the report says so.

**Key decisions.** `String`'s forms add no allocation logic of their
own; every failure path is `Vec`'s, so R3's fixture covers their
mechanics and `string_try_oom.kern` checks only the delegation.

**Risks.** `std/str` is a separate module; if a `Vec` method added in
Phase 1 is not visible there, it is because core declarations are
visible everywhere (`spec/types.md` §1), so a failure here is a
resolver defect to report, not a reason to change the design.

**Acceptance.** The fixtures pass in their configurations; the R4 test
walks from all twelve `try_` methods (`Box`: `try_new`, `try_new_with`,
`try_new_with_ctx`; `Vec`: `try_with_capacity`, `try_reserve`,
`try_push`, `try_extend`, `try_resize`; `String`: `try_from`,
`try_reserve`, `try_push`, `try_push_str`), asserts that each root was
found in the lowered module, and passes its `Vec.push` control; `golangci-lint
run ./...` and `go test ./...` green.

## Phase 3: freestanding, identity, documents, review (R7, R8, R9, R10)

**Goals.** The freestanding fixture exists; R8's comparison is
byte-identical; the documents describe the forms; the review gates
pass; one commit.

**Files.** `tests/freestanding/try_alloc.{kern,out}`;
`specs/initial/AI_Native_Systems_Language_SPEC.md` (§6, the OOM
sentence); `docs/ownership.md` (§5.6); 
`docs/decisions/ADR-006-panic-is-a-trap.md` (amendment line);
`docs/reports/fallible-alloc.md` (new).

**Steps.**

1. `try_alloc.kern` (`//! env: freestanding`, the header shape of
   `tests/freestanding/containers.kern`): `Vec[u64].try_reserve(2^61 +
   1)` yields `Err`; `try_reserve(4096)` succeeds; the `Vec` appends of
   R5 within that capacity succeed; a checksum of the elements goes to
   the console. The harness compares the emulation with QEMU where QEMU
   is installed (it is on this host) and runs the emulation everywhere
   (R7). No arena exhaustion (spec R7).
2. R8: build `./cmd/kern` from the working tree and capture with
   `scripts/capture-ll.sh` into `<scratch>/ll-new`. `capture-ll.sh`
   globs `bench/kern/*/*.kern`, `tests/run/*.kern`, `tests/trap/*.kern`
   and `tests/positive/*.kern` when it runs, so the new capture also
   holds this change's fixtures; the comparison set is therefore fixed
   by the baseline, not by the new glob. The manifest is
   `<scratch>/ll-base/*.ll` plus `<scratch>/ll-base/failed.txt`, both
   written at the baseline capture and not edited. For every manifest
   `.ll` file, `cmp` it with its namesake in `ll-new` (a missing
   namesake is a failure). For every manifest line of `failed.txt`
   (program and opt level), the same line must appear in the new
   `failed.txt`, and no other baseline program may appear there. The
   new capture's files and failure lines that name a program absent
   from the baseline are this change's fixtures; they are listed apart
   in the report, and among them only the `//! test-hooks: true`
   fixtures may fail to build (the capture builds without
   `--test-hooks`, as it does for the existing hook fixtures). Any baseline file that differs is a defect to fix
   before continuing, not a file to exclude; spec R8's exclusion applies
   only to a corpus program this work edits, and the plan edits none.
   `go test ./...` covers `bench/REPORT.md`'s checksums.
3. R9: `go test ./...` runs every new fixture through the harness and
   `TestPreludeSync` for both files.
4. R10: the spec §6 OOM sentence names the `try_` forms as the
   non-trapping counterparts; `docs/ownership.md` §5.6 lists them with
   their failure state and the reserve-then-append guarantee;
   ADR-006 gains `*Amendment 2026-11-01:*` pointing to
   `specs/fallible-alloc/SPEC.md`, naming no other language (so it needs
   no new citation key); `docs/reports/fallible-alloc.md` records the
   design, the R8 comparison counts, the planning facts, the review
   results and spec §3's open questions (the effect schema's single
   `traps` flag, `Map`, the runtime objects). `go test ./tests` checks
   ADR-006's citations.
5. Review, per the phase-review workflow. The tools are the owner's
   installed CLIs (`~/.claude/CLAUDE.md`), confirmed present on
   2026-11-01 by `--help`; their LLM provider settings are the owner's
   configuration, as in earlier phases. In order, with the change
   staged:
   - `golangci-lint run ./...` and `go test ./...` (mandatory, from the
     spec);
   - `prism review staged` (findings fixed, or justified in the report);
   - `realitycheck check --spec specs/fallible-alloc/SPEC.md --plan
     specs/fallible-alloc/PLAN.md`;
   - `verifier analyze -spec specs/fallible-alloc/SPEC.md -plan
     specs/fallible-alloc/PLAN.md` (its gaps for the touched packages
     addressed or listed in the report);
   - `clarion pack` is skipped: the repository has no
     `docs/clarion-meta.json`.
   If prism, realitycheck or verifier fails to run (a missing binary, a
   provider error: exit codes other than findings), that is reported to
   the owner with the error, and the commit waits for the owner's
   decision; the spec's own gates (lint, tests, R8) are never skipped.
6. Commit `feat(core): fallible try_ forms of Box, Vec and String`
   with the spec, plan, report and code; ask the owner before pushing.

**Key decisions.** The R8 corpus is compared whole, with no exclusions,
because no corpus program is edited.

**Risks.** A difference in R8 would mean an existing method's generated
code changed, most likely through a shared helper's instantiation order
or a module-level symbol numbering shift caused by the new
declarations; the fix is to find what the new declarations perturbed
(for example by capturing one differing program at each phase's
commit), not to accept the difference.

**Acceptance.** `try_alloc.kern` passes in the emulation and under
QEMU on this host; every manifest `.ll` file is byte-identical to its
namesake, the baseline programs' failure lines are exactly the
baseline's, and the new fixtures' captures follow step 2's policy (only
test-hook fixtures fail), with the comparison commands recorded in the
report; the R10 documents are updated; the reviews are clean or
their findings justified in the report; the commit is made.
