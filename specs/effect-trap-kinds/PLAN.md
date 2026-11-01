# Plan: trap kinds in `show-effects`, and a fallible `Map` insert

**Spec:** `specs/effect-trap-kinds/SPEC.md` v0.1. **Profile:** davin-go.
**Review status.** plancritic (davin-go), three rounds: no critical
finding; added on the way: 0.2/0.3 byte-identity against the baseline,
the error paths at 0.4, per-operation runtime-object tests, `place`'s
contract, the Map fixture's replaced values and destruction order. Its
out-of-scope notes are the owner's workflow (review tools, commits).

**Order:** Phase 1 (analysis and wire), then Phase 2 (`Map`), then
Phase 3. Phase 2's library code and fixtures do not depend on Phase 1,
but its R4 test reads `trap_kinds`, so Phase 2's acceptance needs Phase
1. Each phase ends with `golangci-lint run
./...` and `go test ./...` green. One commit after Phase 3, on `main`,
not pushed.

**Baseline (before any prelude edit).** As for `specs/alloc-containers`:
build `./cmd/kern` at `HEAD` (ab9112a) from a temporary worktree into
`~/.cache/kern-effect-trap-kinds/kern-base`, capture with
`scripts/capture-ll.sh` into `ll-base`, make it read-only, remove the
worktree; add no fixture under `tests/` until it finishes.

## Phase 1: `trap_kinds` and schema 0.4 (R1–R5)

**Files.** `compiler/analysis/effects.go`, `compiler/analysis/analysis_test.go`
(or a new `effects_kinds_test.go`), `compiler/diag/diag.go`,
`compiler/diag/schema.go`, `compiler/diag/schema_test.go`,
`compiler/diag/diag_test.go`, `tools/agent-api/schema.json`,
`tools/agent-api/schema.md` (generated), `cmd/kern/*_test.go` that name the
newest version.

**Steps.**

1. `effects` gains a kind set (a bitmask over spec §6's order); the
   instruction cases of the spec's table set kinds; `trapping` becomes
   `map[string][]string` with the table's kinds, the runtime object
   operations added with `oom`; function-value, FFI and `asm` calls set
   every listed kind; `join` unions the sets; `traps` is derived as
   "set non-empty".
2. `diag.FunctionEffects` gains `TrapKinds []string \`json:"trap_kinds"\``;
   `analysis.Effects` fills it (never nil). `diag.SchemaVersion` = 0.4,
   `Accepted` = 0.2, 0.3, 0.4; `WriteAt` at 0.2 or 0.3 writes a
   `ShowEffectsResult` without `trap_kinds` (a copy through a 0.3 struct
   type without the field), leaving the caller's value untouched, as it
   copies diagnostics today.
3. `schema.json`: version note 0.4, `trap_kinds` (array of the twelve
   listed kinds, required) on the show-effects function entry, the
   `Request.schema_version` enum; edit by hand, surgically. Run `go
   generate ./compiler/diag`; extend `schema_test.go` with the 0.3/0.4
   shapes; update tests that assert "0.3" as the newest version.
4. Tests (R2, R3): one small program per table row (the runtime object
   row as six separate programs, one per operation, each asserting
   `traps: true` and exactly `oom`), lowered without
   optimization, asserting the function's `trap_kinds` exactly; a caller
   asserting the union; `traps` iff non-empty.
5. R5: a test with a fixed list of the runtime's trapping entry points
   (from `kern_rt.c`: `kern_alloc` callers for the runtime objects,
   `kern_mutex_lock`/`unlock`, `kern_condvar_wait`, `kern_shared_clone`,
   the `kern_sysalloc_*` obligation checks; and the interpreter's
   `runtimeAlloc` and intrinsic traps) with their kinds, each checked to
   appear in the source file by name, and compared with the analysis
   table.
6. `cmd/kern` tests: a missing, an unsupported and a malformed request
   each answer with their existing code at `schema_version` 0.4; files
   on the command line answer at 0.4; a program that creates a `Mutex`
   reports `traps: true` at 0.2, 0.3 and 0.4. `show-effects` at 0.2, 0.3
   and 0.4 on one program
   with no runtime object operation: the 0.2 and 0.3 outputs are
   byte-identical to the baseline compiler's (`kern-base`, run by the
   test only when `KERN_BASE` names it; otherwise to a golden file
   recorded from it), and 0.4 adds `trap_kinds` on every function.
7. The schema text for `trap_kinds` says `stack` is never listed because
   every function's prologue can raise it.

**Acceptance.** All tests green; `schema.md` not stale.

## Phase 2: `Map.try_new` and `try_insert` (R6, R4)

**Files.** `compiler/resolve/prelude/map.kern` (copied to
`std/map.kern`); `tests/run/map_try.{kern,out}`,
`tests/run/map_try_oom.{kern,out}`; the R4 analysis test.

**Steps.**

1. `try_with_bits[K, V]` (the range check, then `Vec.try_resize` of the
   states and `Vec.try_reserve` of the key and value vectors, then the
   `None` fills; on any `Err` the partly built map is destroyed by
   scope exit, which frees what was allocated); `Map.try_new` returns
   `try_with_bits[K, V](4u32)`.
2. `Map.place(edit self, key: own K, val: own V) -> Option[V] where K:
   Hashable` (the slot
   write of `insert` without the growth check) and `try_rebuild(edit
   self, bits) -> Result[(), AllocErr]` (builds the table with
   `try_with_bits`, then moves the live entries with `place`).
   `try_insert(edit self, key: own K, val: own V) -> Result[Option[V],
   AllocErr] where K: Hashable` checks growth as `insert` does, calls
   `try_rebuild`, and on `Err` destroys `key`, then `val`, and returns
   `Err(OutOfMemory)` at once; otherwise it returns `Ok(self.place(key,
   val))`, `place` returning the replaced value as `insert` does. The
   signatures are spec §2's (`try_new() -> Result[Map[K, V], AllocErr]`,
   `try_with_bits[K, V](bits: u32) -> Result[Map[K, V], AllocErr]`).
3. Fixtures: `map_try` (no `config:` line: all four configurations)
   runs `insert` and `try_insert` on twin maps over a sequence of
   inserts, replacements and removes that forces a same-size rebuild and
   two doublings, printing after each step the replaced value each
   returned, `len()`, and the entries in key order; `map_try_oom`
   (`//! test-hooks: true`): arm `testing.fail_next_alloc()` before
   `try_new` (prints `Err`), and before a `try_insert` that must rebuild
   (prints `Err`, then the map's entries and `len()` as before), with a
   key and a value type whose destructors print, so the `.out` shows
   the key's line and then the value's, once each.
4. R4: one program calling, at `T = K = V = u64` and `A =
   SystemAllocator`, every `try_` method of `Box`, `Vec` and `String`,
   every `BoxIn` and `VecIn` method, and `Map.try_new` / `try_insert`;
   assert no `oom` in any of their summaries and `oom` in `Box.new`,
   `Vec.push` and `Map.insert`.

**Acceptance.** Fixtures pass in their configurations; R4 test passes.

## Phase 3: identity, documents, review (R7, R8)

**Acceptance.** R7 byte-identical for the frozen manifest; the R8
documents updated and `schema.md` not stale; reviews clean or justified;
committed.

1. R7 capture and comparison against the frozen manifest (as before).
2. Documents of R8; README's version paragraph (0.4 newest; 0.3 without
   `trap_kinds`; 0.2 without `message`).
3. Stage everything, check `git status`; `golangci-lint`,
   `KERN_REQUIRE_QEMU=1 go test ./...`, `prism review staged --paths
   'compiler/**,std/**,cmd/**' --reasoning-effort medium` (default
   settings), `realitycheck check --max-tokens 65536`, `verifier analyze
   -mode offline` excluding `.claude/**`; clarion skipped. Fixes
   re-staged and re-tested. The tools are the owner's installed CLIs and
   ran in this session with these commands; if one cannot run, the
   commit waits and the owner is told (the spec's gates are lint, tests
   and R7).
4. Commit `feat(analysis): trap kinds in show-effects, schema 0.4;
   Map.try_insert`.

## Risks

- `WriteAt` gets an `any` result; down-converting needs a type switch on
  `diag.ShowEffectsResult` (and its pointer, if any command passes one).
- A same-size rebuild that the placing step cannot complete would be a
  bug in the room argument; `map_try`'s rebuild sequence and an assertion
  in `place` (an `explicit` trap if no free slot) catch it.
