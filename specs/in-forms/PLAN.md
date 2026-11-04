# Plan: construct-in-place and pair access for `ArenaIn` and `PoolIn`

Implements `specs/in-forms/SPEC.md` v0.3 in one phase and one commit.
The change is library code and fixtures only, all in one file pair, so
a second phase would only split one review.

## Phase 0: baseline (before any library edit)

**Goal.** Capture R7's IR baseline from the commit before the change.

**Steps.**
1. Build the compiler at `dd16b89` (the current `HEAD`, whose prelude
   has none of the new methods) from a temporary `git worktree`.
2. Run `scripts/capture-ll.sh <that compiler> <baseline dir>` and keep
   the directory and its `failed.txt`.

**Acceptance.** The baseline directory holds the `.ll` files and the
list of programs that do not build.

## Phase 1: the methods, fixtures and documents

**Goals.** R1 to R7.

**Files.**
- `compiler/resolve/prelude/core.kern` and `std/core.kern` (identical):
  - **`ArenaIn.try_alloc_with` and `try_alloc_with_ctx`,** after
    `try_alloc`. They are `Arena.alloc_with` and `alloc_with_ctx` with
    `grow()` replaced by `try_grow()`, whose `Err(e)` becomes
    `Err(Alloc(e))`.
  - **`PoolIn.try_alloc_with` and `try_alloc_with_ctx`,** after
    `try_alloc`. They grow first, under `try_alloc`'s condition (no
    vacant slot and `count == cap`), then choose the slot and run the
    initializer as `Pool.alloc_with` does: a new slot is written with
    `live: false` before the initializer runs, and a failed slot is
    pushed on the free list.
  - **`PoolIn.get2`,** after `at`: `Pool.get2`'s body.
  - Each carries the contract §2 of the spec names.
- Fixtures (spec §4), each with its `.out` where it has one:
  - `tests/run/in_alloc_with.kern` (R1, R2, R4);
  - `tests/run/poolin_get2.kern` (R3);
  - `tests/trap/poolin_get2_same.kern` (`disjoint`) and
    `tests/trap/poolin_get2_stale.kern` (`generation`, after the slot is
    reused).
- `compiler/analysis/try_alloc_test.go` (R5): the five method names are
  added to `fallibleForms`, and the test program calls each of them so
  they are instantiated.
- Documents (spec §5): `docs/ownership.md`,
  `docs/reports/alloc-containers.md`,
  `docs/reports/alloc-containers-more.md`, and the new
  `docs/reports/in-forms.md`. If the hosted-only inventory test reports
  a stale inventory, it is regenerated with its `-update` flag.

**Key decisions.**
- **Grow before choosing the slot** in `PoolIn`. `try_grow` is the only
  step that can fail before the initializer runs, so doing it first keeps
  the pool unchanged on `Alloc`. `Pool.alloc_with` grows inside the
  new-slot branch; the order of effects is the same, since growth only
  happens when there is no vacant slot.
- **The `_with` forms are walked by R5's test.** The walk does not enter
  calls through function values, so the initializer is excluded, as
  `Box.try_new_with`'s is.

**Steps and verification.**
1. Add the methods and copy the prelude to `std/core.kern`; `cmp` the
   two.
2. Write the fixtures. Run each with `kern run` and with native builds
   at debug and release, and compare the outputs (R6). Record each
   `.out` from the interpreter run.
3. Extend `try_alloc_test.go`. Run it, then turn one `Err(Alloc(e))` in
   `PoolIn.try_alloc_with` into `trap_kind(oom)` and confirm the test
   fails; restore and `cmp` the mirrors.
4. R7: run `scripts/capture-ll.sh` with the new compiler and
   `diff -r` against the Phase 0 baseline. The fixtures added in step 2
   are new files with no baseline counterpart and are excluded by name.
5. Gates: `go build ./...`, `gofmt -l`, `golangci-lint run ./...`,
   `go test ./...`, and `go test ./tests` before the document commit.
6. Review: an internal code review of the diff, with findings fixed or
   recorded, then one commit.

**Acceptance.**
- Every fixture passes in the harness (`go test ./tests`), which runs
  run fixtures in the interpreter and natively at both levels and trap
  fixtures in each engine.
- The mutation in step 3 fails R5's test.
- The IR diff of step 4 is empty.
- All gates are clean.

**Risks.**
- **Duplication grows.** Two more methods repeat `Pool`'s slot logic in
  `PoolIn`. The spec keeps sharing it out of scope; the report records
  that the duplication grew.
- **A known host failure.** The `race_raw_ptr` TSan fixture times out on
  this host for an unrelated reason (the symbolizer, `atos`, hangs; it
  fails the same way on commits before this one). The gate policy for it
  is in the next section.

## Gate policy

Every gate is mandatory and must pass, with one named exception:
- **The exception.** If `go test ./tests` fails only in the
  `race_raw_ptr` TSan fixture, and only by timing out, the failure is
  confirmed as the host's by running the same fixture at the Phase 0
  baseline commit: `go test ./tests -run 'TestFixtures/concurrency/race_raw_ptr' -timeout 40m` in the
  baseline worktree. If the baseline also times out, the commit may
  proceed. The report names the fixture, both commands and both
  outcomes. CI then runs the fixture on Linux after the push.
- **No other exception.** Any other failure blocks the commit, including
  any failure in a new fixture and a `race_raw_ptr` failure that is not a
  timeout.
