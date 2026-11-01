# Plan: containers over a caller's allocator (`BoxIn`, `VecIn`)

**Spec:** `specs/alloc-containers/SPEC.md` v0.1. **Profile:** davin-go.
**Review status.** plancritic (davin-go), four rounds: no critical
finding; added on the way: edge-case fixtures, a table-driven marker
test, the growth-guard hook and fixture, R8's frozen manifest, staging
before review. Its out-of-scope notes are the owner's workflow (review
tools, commits on `main`).

**Order:** Phase 1 then Phase 2; each ends with `golangci-lint run ./...`
and `go test ./...` green and `std/core.kern` byte-identical to
`compiler/resolve/prelude/core.kern` (`TestPreludeSync`). One commit after
Phase 2, on `main`, not pushed.

**Baseline (before any prelude edit).** Build `./cmd/kern` at the current
`HEAD` (7e6d609) into `~/.cache/kern-alloc-containers/kern-base` from a
temporary `git worktree`, run `scripts/capture-ll.sh` into
`~/.cache/kern-alloc-containers/ll-base`, make it read-only, remove the
worktree. No fixture is added under `tests/` until the capture finishes
(the script globs the fixture directories when it runs).

## Phase 1: the types, markers and hosted fixtures (R1–R6, R9)

**Files.** `compiler/resolve/prelude/core.kern` (copied to
`std/core.kern`); `compiler/resolve/decls.go` (`markerOf`);
`tests/run/vecin_edges.{kern,out}`, `tests/run/vecin_guard.{kern,out}`; `tests/trap/boxin_released`,
`vecin_bounds` and their twins;
`compiler/resolve/resolve_test.go` (a marker test);
`compiler/analysis/try_alloc_test.go`; `tests/run/vecin_system`,
`vecin_budget`, `vecin_exhaust` (`.kern`, `.out`);
`tests/negative/vecin_send.kern`, `vecin_send_ok.kern`.

**Steps.**

1. Declare `BoxIn[T, A: Allocator]` and `VecIn[T, A: Allocator]` after
   `Vec` in the prelude, with the methods of spec §2. `VecIn` mirrors
   `Vec`'s `try_` bodies with three differences: `a.alloc` / `a.free`
   in place of the `heap_*` helpers; growth by `alloc`, `ptr_copy` of
   the `size` live elements, then `free` of the old buffer (only when
   `cap > 0`); and `release` frees through `a`. `try_reserve` does the
   byte-size check with `elems_bytes` before calling `alloc`. Each
   method with `unsafe` gets a contract block.
2. `markerOf`: a `core.BoxIn` / `core.VecIn` case: `Copy` false; `Send`
   and `Share` are `markerOf(arg(0)) && markerOf(arg(1))`; `Interior` is
   `markerOf(arg(0)) || markerOf(arg(1))` (spec §2).
3. Fixtures, each with a budget allocator declared in the fixture
   (`struct Budget nocopy { left: usize; live: usize; bytes: usize;
   calls: usize; sys: SystemAllocator; ... }`, `impl Allocator for
   Budget` drawing on `SystemAllocator` and keeping a small table of live
   allocations to check each `free`):
   - `vecin_system.kern`: the pairs of spec R2 against `Vec`.
   - `vecin_budget.kern`: R3's sequence, printing each `alloc`/`free`
     (size, alignment) and ending with live count and bytes 0.
   - `vecin_exhaust.kern`: R4's cases, including a destructor-printing
     element for `try_push` and `BoxIn`, and the `elems_bytes` overflow
     case showing the `alloc` call count unchanged.
   - `vecin_edges.kern` (all four configurations): a zero-size element
     (`struct Z { }`) whose `try_reserve(8)` and `try_push`es reach the
     budget allocator as 0-byte requests, counted and freed like any
     other; after `release`, `allocator()` still reads the counters
     (live 0) and the vector can grow again from `a`; a failed
     `BoxIn.try_new_in` with a value and an allocator that both print on
     destruction shows the value's line before the allocator's; `pop` on
     an empty vector is `None`.
   - `tests/trap/boxin_released.kern` (`//! expect: trap explicit`, with
     a completing twin): `get` on a `BoxIn` after `release`; and
     `tests/trap/vecin_bounds.kern` (`trap bounds`, with a twin): `get`
     past `len()`.
   - The growth guard: `VecIn` has no `resize`, so a test hook
     `seed_len(edit self, n: usize)` (after `testing.require_hooks()`, as
     `Pool.seed_generation`; traps `explicit` unless `size_of[T]() == 0`
     and `n <= capacity()`) sets a zero-size vector's length.
     `tests/run/vecin_guard.kern` (`//! test-hooks: true`): with
     capacity exactly 2^63 - 1 and full, `try_push` grows (no guard at
     equality: the doubled capacity is 2^64 - 2); with capacity 2^63 and
     full, `try_push` and `try_extend` yield `Err` with len, capacity and
     the allocator's `alloc` count unchanged (rejected before doubling);
     `try_reserve(2^64 - 1)` on that vector succeeds (no guard: 0 bytes).
     The fixture ends with `exit(0i32)`, as `vec_try_size` does.
   - `vecin_send.kern` (`//! expect: reject SEND_NOT_SEND`) and the twin
     `vecin_send_ok.kern` (accept), in `tests/negative/` as the existing
     twins are.
4. Resolver test, table-driven over both `BoxIn` and `VecIn` and over
   `Send`, `Share` and `Interior`: an element and an allocator that have
   the marker (both), only the element, only the allocator, neither;
   the container has `Send` or `Share` exactly in the first case and
   `Interior` in all but the last, and is never `Copy`.
5. R5: add every `BoxIn` and `VecIn` method at `T = u64`, `A =
   SystemAllocator` to `tryAllocProgram` and `want`; if the allocator
   calls are not statically resolved calls in the unoptimized IR, follow
   the form they take and record it (spec §6 risk).

**Acceptance.** The fixtures pass in their configurations, the resolver
and R5 tests pass, lint and `go test ./...` green.

## Phase 2: freestanding, identity, documents, review (R7, R8, R10)

**Files.** `tests/freestanding/vecin.{kern,out}`; `docs/ownership.md`
(§5.6), `specs/initial/AI_Native_Systems_Language_SPEC.md` (§6),
`docs/unsafe-audit.md`, `docs/decisions/ADR-011-freestanding-runtime-facilities.md`,
`docs/reports/alloc-containers.md`.

**Steps.**

1. `tests/freestanding/vecin.kern`: a budget allocator over the
   freestanding `SystemAllocator` (a budget well under the 1 MiB arena,
   so exhaustion is the budget's, identical in both engines); grow, fail
   at the budget, release to zero live bytes; checksum to the console.
   Run with `KERN_REQUIRE_QEMU=1`.
2. R8: capture with the new compiler into `ll-new`; `cmp` every
   baseline `.ll` with its namesake (missing is a failure); every
   baseline `failed.txt` line present in the new one and no other
   baseline program added; both opt levels, for exactly the frozen
   baseline manifest (`ll-base/*.ll` and `ll-base/failed.txt`). New
   fixtures are listed apart with their capture outcome; only the
   `test-hooks` fixture `vecin_guard` may fail to build (the capture
   builds without `--test-hooks`).
3. Documents of R10; recount the unsafe-audit table against the prelude
   with the script used for `specs/fallible-alloc`.
4. Review. Stage every file of the change (`git add` of the paths in
   both phases' file lists and the spec, plan and report) and check
   `git status --short` shows nothing of this change unstaged; after a
   fix, re-stage and re-run the affected tests and that tool. The tools are the owner's installed CLIs (`~/.claude/CLAUDE.md`;
   prism, realitycheck and verifier ran in this session on 2026-11-01 with
   the commands below and the owner's configured providers). `golangci-lint`, `KERN_REQUIRE_QEMU=1 go test ./...`,
   `prism review staged --paths 'compiler/**,std/**,tests/**'` (the
   owner's default settings; findings fixed or justified in the report),
   `realitycheck check --max-tokens 65536`, `verifier analyze -mode
   offline` excluding `.claude/**`; clarion skipped (no
   `docs/clarion-meta.json`). If a review tool cannot run, the commit
   waits and the owner is told.
5. Commit `feat(core): BoxIn and VecIn over a caller's allocator` with
   spec, plan, report and code.

**Acceptance.** R7 passes in the emulation and under QEMU; R8 is
byte-identical with the same baseline failures; documents updated;
reviews clean or justified; committed.

## Risks

- A generic method calling an interface method of a type-parameter
  field may need the field bound with `with` before the call, or the
  checker may reject `self.a.alloc(...)` inside `edit self`; the
  planning probe accepted it in a user module, so the prelude should too.
- A budget allocator that tracks live allocations needs fixed storage
  in the fixture (an array of 64 entries is enough for these sequences);
  overflowing it traps `explicit`, which would be a fixture bug.
