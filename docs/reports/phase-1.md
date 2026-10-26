# Phase 1 report — Semantic prototype (interpreter, no native backend)

**Date:** 2026-10-26. **Plan:** `specs/initial/PLAN.md` Phase 1. **Spec:** v0.2. **Builds on:** Phase 0 (commit `ee1d885`).

## Deliverables

| # | Deliverable | Location | Status |
|---|---|---|---|
| 1.1 | Semantic IR, textual printer, validator (five §7 invariants) | `compiler/ir/{ir,print,validate}.go`; lowering with on-demand specialization in `compiler/lower/` | done; the IR text *parser* (round trip of hand-written IR) is deferred to Phase 2, see deviations |
| 1.2 | Interpreter over the IR with §4.1 execution semantics, §5.7 destruction, §6 traps, library intrinsics | `compiler/interp/{values,interp,intrinsics}.go`, `std/core.kern` | done |
| 1.3 | `spec/format.md`, `format` command; `emit-ir`, `explain-ownership`; `tools/agent-api/schema.{json,md}` | `compiler/format/`, `cmd/kern/{run,explain}.go`, `compiler/diag/schema.go` | done |
| 1.4 | Trap fixtures with twins, expected-output fixtures, differential harness executing from each fixture's phase | `tests/trap/` (13 kinds), `tests/run/`, `tests/harness_test.go` | done |
| 1.5 | Reserved extension (`view struct`) | not implemented: the Phase 0 count is 0 | not needed |
| — | ADR-010 closed | `docs/decisions/ADR-010-function-pointers-and-closures.md` | decided: function pointers now, non-escaping closures later, escaping closures a non-goal |

## Acceptance criteria

1. All Phase 0 fixtures still pass — **met**.
2. Trap fixtures `bounds`, `overflow`, `div_zero`, `shift`, `conversion`, `generation`, `vacant`, `identity`, `disjoint`, `oom`, plus `stack`, `explicit` and `unreachable`, each with a normally completing twin, trap in the interpreter with the expected `TrapInfo` kind — **met** (`tests/trap/`; `unreachable` is driven by a hand-built IR module in `compiler/interp/interp_test.go`, see deviations).
3. Every accepted fixture whose executable phase is 1 and that has a `main` produces interpreter output matching its expected-output file — **met** (`tests/run/*.out`, `tests/trap/*_ok.out`; fixtures without `main` are check-only and must not trap).
4. The IR validator passes on every accepted fixture and rejects deliberately corrupted IR — **met for acceptance** (`compiler/lower/lower_test.go` validates every accepted fixture); the corrupted-IR corpus under `tests/ir/` depends on the IR text parser and moves with it to Phase 2.
5. `format` is idempotent and diagnostic-code preserving on every fixture and example — **met** (`compiler/format/format_test.go`).
6. `explain-ownership`, `emit-ir`, `format`, `check` conform to §16 and `tools/agent-api/schema.md` matches the Go types — **met** (`compiler/diag/schema_test.go` validates sample responses against `schema.json`; `cmd/kern/cli_test.go` exercises each command).
7. The reserved-extension decision is recorded — **met**: not needed (0 of 13 workloads).

## What the interpreter implements

- Values: two's-complement integers of every width with trapping, wrapping, saturating and checked forms; IEEE float arithmetic without reassociation; §4.1 conversions (`T(x)` traps on range, `truncate[T]` never, `saturate` for floats); aggregates with value semantics for `Copy` types and unique ownership otherwise.
- Places and accesses: every `view`/`edit` is an open access over a slot or a sequence window; projections returned by library functions (`Vec.at`, `Pool.get2`, `Mutex.lock`) are accesses whose release function runs at `access_end`.
- Destruction: `Drop` is type-directed (user destructor first, then fields in reverse declaration order); static points come from the checker's new per-exit annotations (`resolve.Info.Drops`, `OutFieldDrops`, `OwnedSubject`), which the lowering combines with its own scope frames so that temporaries (subject accesses, owned subject temporaries, sequence accesses, scoped-spawn arguments) close on every exit path.
- Traps: §6 semantics, one JSON record line on standard error, status 134, a `trap.set_handler` hook, and the Phase 2 record format already in place.
- Library: `Box`, `Vec`, `Pool` (validation order bounds → identity → vacant → generation, generation retirement at `u32` wrap, `get2` with the §13 order), `Arena`, `Shared` (count overflow trap), `Mutex` (recursive acquisition traps `explicit`), `Atomic`, `thread.spawn`/`scope.spawn`/`Join` as single-threaded stand-ins that run the function eagerly, `SystemAllocator` stub, `zero`, `len`, `clone`, output functions, and the test hooks `testing.seed_generation`, `testing.seed_shared_count`, `testing.fail_next_alloc` gated by `--test-hooks`.

## Deviations from the plan

- **IR text parser deferred to Phase 2.** The printer exists (`emit-ir`); the parser for hand-written IR, and with it the corrupted-IR corpus under `tests/ir/` and the source-level `unreachable` fixture, move to Phase 2 where the native backend needs them. The `unreachable` trap is covered now by a programmatic IR module in the interpreter's tests.
- **`oom` is reproduced through a test hook.** `testing.fail_next_alloc()` makes the next convenience allocation (`Box.new`, `Vec.push`) trap with kind `oom`; the fixture is tagged `test-hooks: true`.
- **Multi-module resolution.** `NAME_PRIVATE` needed a second module, so `Resolve` now groups request files by module, resolves modules in import order, and selects the main module (the one declaring `main`, else the last given). Imported functions and types must be exported; `std/*` modules export everything (spec/types.md §1).
- **Checker annotations drive lowering.** Rather than re-deriving ownership state in the lowering, the checker records the symbols whose scope ends at every exit point; this keeps one source of truth for static destruction (spec §5.1).
- **Formatter conventions** (from `spec/format.md`): one-line bodies stay one line; comments are re-attached by position; multi-line expressions are canonicalized to one line.

## Reviews

- **prism** (OpenAI gpt-6.1-sol, staged diff, docs and fixtures excluded): 26 findings (6 high, 20 medium), all addressed: assignment cleanup limited to the assigned out parameter; unsigned comparisons in bounds checks and `Vec` indexing; typed destruction in `Vec.clear`, `Vec.pop` and `Pool.free` (intrinsic calls now carry their type arguments); `Shared` reference counting under `clone`; release obligations kept across every exit path; `f32` rounding; exclusive power-of-two float-to-integer bounds; stack budget checked before charging; validator checks for moves against open accesses, edit reborrows through read-only accesses, returned accesses being open, unsafe depth at joins, and reborrow exemptions limited to the ancestor chain; field selection on temporaries; parent/child access end order; wildcard arms of owned subjects; path-scoped expansion depth; the lowering returns its diagnostics on recovery; `explain-ownership` filters by file. Not changed: `explain-ownership` matches IR locals by name within a function (identity mapping is a Phase 2 item) and consume entries use the declaration span.
- **realitycheck** (spec + plan): PARTIALLY_ALIGNED, score 100, no drift, no violations (Phases 2–5 unimplemented, as expected); the run needs `--max-tokens 65536` because the default output budget truncates on this codebase.
- **verifier** (offline): 70 low-severity unit gaps (new packages `ir`, `lower`, `interp`, `format`); unit tests exist for the interpreter's arithmetic and trap semantics, the lowering (every accepted fixture lowers and validates), and the formatter's properties; per-function unit tests are a Phase 2 cleanup item.

## Known limitations carried into Phase 2

- Threads are eager stand-ins; data-race freedom is checked statically only. Real threads arrive in Phase 3.
- `destroyAny` (Pool.free) destroys library objects structurally; user types with destructors inside a freed pool element are destroyed when the pool is destroyed rather than at `free` (the element type is not available to the intrinsic). The Phase 2 kern rewrite of `Pool` has the type.
- The validator's ownership analysis exempts compiler temporaries and does not yet track out-storage fields.
