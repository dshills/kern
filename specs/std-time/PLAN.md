# Plan: `std/time`

**Spec:** `specs/std-time/SPEC.md` v0.1. **Profile:** davin-go.
**Review status.** plancritic (davin-go): no critical finding; its
warnings (chunked sleep, zero duration, the interpreter's failure path,
the freestanding check, the report's contents) folded in.
**Order:** Phase 1 then Phase 2; each ends with `golangci-lint run
./...` and `go test ./...` green. One commit after Phase 2, on `main`,
not pushed.

**Baseline.** Before any compiler, runtime or prelude edit: a compiler
built from `HEAD` (f49cf88) in a temporary worktree into
`~/.cache/kern-std-time/kern-base`, `scripts/capture-ll.sh` into
`ll-base`, made read-only; no fixture under `tests/` until it finishes.

## Phase 1: the module and its runtime (R1–R5)

**Files.** `compiler/resolve/prelude/time.kern` (copied to
`std/time.kern`); `compiler/resolve/decls.go` (`StdModules`);
`runtime/hosted/src/kern_rt.c`; `compiler/backend/llvm/calls.go`;
`compiler/interp/intrinsics.go` (and a test file there);
`compiler/analysis/effects.go` and `effects_internal_test.go` (the two
entries list `explicit`); `tests/run/std_time.{kern,out}`;
`tests/trap/time_overflow.kern` and `time_overflow_ok.{kern,out}`.

1. `time.kern`: the intrinsics `time_sleep_ns(ns: u64)` and
   `time_wall_ns() -> u64`, then the types and functions of spec §2 in
   kern over them and the prelude's `now_ns`.
2. Runtime: `kern_time_sleep_ns(uint64_t ns, const kern_loc *at)`
   (`nanosleep` with the remainder after `EINTR`, `kern_trap_loc(11, at)`
   on any other failure) and `kern_time_wall_ns(const kern_loc *at)`
   (`clock_gettime(CLOCK_REALTIME)`, the same trap on failure), declared
   with the other entries.
3. Backend: two `calls.go` cases passing the call's location record, as
   the other trapping entries do.
4. Interpreter: `time_sleep_ns` sleeps in chunks of at most
   `math.MaxInt64` ns (a `u64` duration does not fit one `time.Duration`),
   through a helper that returns the chunk sizes, unit-tested at the
   boundary without sleeping; `time_wall_ns`; `now_ns` becomes
   `startWall + time.Since(start)` from a reading taken once. Go's
   `time.Sleep` and `time.Now` have no failure to report, so the
   interpreter's trap path for these entries is the spec's guard only.
5. Analysis: `trapping` gains both entries with `explicit`, and
   `runtimeTrapSites` gains them.
6. Fixtures: `std_time.kern` prints R2's values and R3/R4's booleans,
   including `sleep(nanos(0))` returning; the trap pair for
   `seconds(u64 max)`. The hosted-only rule is checked by building a
   freestanding program importing `std/time` and confirming the link
   fails, recorded in the report (the harness has no link-failure
   fixture kind).
7. Interpreter unit test (R5): 10,000 `now_ns` readings never decrease,
   and the first is within a second of `time.Now().UnixNano()`.

**Acceptance.** Tests green in all configurations.

## Phase 2: identity, documents, review (R6, R7)

1. R6: capture with the new compiler and compare with the frozen
   manifest (every baseline `.ll` byte-identical, the same failure
   lines); new fixtures listed apart.
2. Documents: README's library row, `spec/types.md` §1,
   `docs/reports/std-process.md`'s note, `docs/reports/std-time.md`
   (design, R3's measured sleep on this host, the freestanding link
   check, R6's counts, the review).
3. Review: stage everything, `golangci-lint`, `KERN_REQUIRE_QEMU=1 go
   test ./...`, `prism review staged --paths
   'compiler/**,std/**,runtime/**,tests/**' --reasoning-effort medium`,
   `realitycheck check --max-tokens 65536`, `verifier analyze -mode
   offline`; clarion skipped. The tools are the owner's; if one cannot
   run, the commit waits and the owner is told.
4. Commit `feat(std): std/time`.

**Acceptance.** R6 byte-identical; documents updated; reviews clean or
justified; committed.
