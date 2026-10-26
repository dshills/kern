# Phase 2 report — Native MVP (LLVM backend, hosted runtime)

**Date:** 2026-10-26. **Plan:** `specs/initial/PLAN.md` Phase 2. **Spec:** v0.2. **Builds on:** Phase 1 (commit `d4e90f7`).

## Deliverables

| # | Deliverable | Location | Status |
|---|---|---|---|
| 2.1 | Textual LLVM backend, `kern build`, `emit-asm`, `--target`, layout per §4.1, aliasing attributes, strict floating point | `compiler/backend/llvm/{types,emit,calls}.go`, `compiler/backend/native/build.go`, `compiler/layout/`, `cmd/kern/build.go` | done |
| 2.2 | Hosted runtime: startup, traps with the signal-safety contract, `SystemAllocator`, stack-overflow detection, stack probes, `--opt debug\|release` identity tags, ADR-012 | `runtime/hosted/src/kern_rt.c` (embedded by `runtime/hosted`), `docs/decisions/ADR-012-trap-handler-obligation.md`, `docs/ownership.md` beside `trap.set_handler` | done |
| 2.3 | Unsafe core and the library in kern; thread-local storage | TLS: `thread_local` declarations end to end (`tests/run/tls.kern`); library: **implemented as a hosted C runtime, not rewritten in kern** (see deviations) | partial |
| 2.4 | C ABI subset: import and export, FFI fixtures | `tests/ffi/{scalars,pointers}.kern` with C counterparts, `extern "C" func … { }` exports, `zeroext` bools | done natively; interpreter bridge deferred |
| 2.5 | Machine API: `show-layout`, `show-copies`, `show-allocations`, `show-bounds-checks`, `emit-asm` | `compiler/analysis/analysis.go`, `cmd/kern/commands.go` | done |
| 2.6 | Benchmark harness, C/Rust/kern workloads, `toolchain.lock`, `REPORT.md` | `bench/` | see Benchmarks |

## Acceptance criteria

1. Every `phase: 1` and `phase: 2` accept fixture produces identical output from the interpreter and the native build; every trap fixture traps natively with the expected kind — **met on AArch64 (darwin/arm64)**: `tests/harness_test.go` builds every executable fixture with clang after the interpreter run and compares exit status, standard output byte for byte, and the trap record's kind and span (stack traps by kind only, ADR-012). x86-64 is not covered: no x86-64 runner or emulator is available on this host (see deviations).
2. FFI fixtures pass; `FFI_UNSUPPORTED_TYPE` fixtures reject — **met natively** (`tests/ffi/`, `tests/negative/ffi_unsupported_type.kern`).
3. `show-layout` matches C `offsetof`/`sizeof`; the TLS fixture and the generation-retirement fixture pass natively and in the interpreter — **met** (`compiler/layout/layout_c_test.go` compiles generated C for nested structs, arrays and tagged unions; `tests/run/tls.kern`; `tests/trap/generation*.kern`).
4. Benchmark report with all metrics, CIs and separate rows — see Benchmarks.
5. Machine API commands of 2.5 conform to §16 — **met** (`cmd/kern/cli_test.go` `TestPhase2Commands`; results are the Go types validated against `tools/agent-api/schema.json` in `compiler/diag/schema_test.go`).
6. ADR-012 recorded and the obligation stated beside `trap.set_handler` — **met**.

## What the native path implements

- **Backend.** One LLVM function per IR instantiation (mangled names quoted), allocas for every slot, places as pointer arithmetic over `%"T.<type>"` structs and byte-offset union payloads, sequence accesses as `{ptr, i64}` pairs. Access parameters are passed as pointers with the §5.4 aliasing attributes (`edit` → `noalias`, `view` of a non-`Interior` type → `noalias readonly`, `Interior` → none); a sequence access parameter is passed as two scalars so the attributes apply to its pointer. Aggregates above 128 bytes stay in memory and move with `memcpy`/`memset` (LLVM does not cope with megabyte aggregate registers). Every function carries `probe-stack`. Checks become `icmp` + branch to a trap block calling `kern_trap(kind, start, end)`; overflow uses the `llvm.*.with.overflow` intrinsics; conversions trap through explicit range tests; floating point is emitted without fast-math flags and clang runs with `-ffp-model=strict`.
- **Runtime** (`kern_rt.c`, ~500 lines). Output goes through the runtime's own buffer flushed with `write(2)`, so the trap path never touches stdio. `kern_trap` builds `TrapInfo` in per-thread preallocated state, selects the handler with one `seq_cst` load, runs it at most once and dies by `SIGABRT` after restoring the default disposition; a second trap on the same thread terminates at once. Stack overflow is a guard-page fault classified by the thread's recorded stack bounds in a `SIGSEGV`/`SIGBUS` handler on a per-thread 64 KiB alternate stack; the record for it carries span `(0, 0)`. Containers (`Box`, `Vec`, `Pool` with generation retirement and identity tags, `Arena`, `Shared`, `Mutex`, `Atomic`, scoped and detached threads over pthreads, `SystemAllocator`) are opaque handles; the test hooks are always linked (the `--test-hooks` flag is accepted for parity with the interpreter). `--opt release` compiles the runtime with identity tags disabled; the interpreter mirrors the flag.
- **Threads are real.** `thread.spawn` and `scope.spawn` create pthreads running compiler-emitted thunks over a packed argument block; `Join.get`/`Thread.join` wait; a scope joins every spawned thread when it ends. `tests/run/scoped_sum.kern` runs the same program in the eager interpreter and natively with identical output; thread-local variables get a fresh instance per thread in both.
- **Interpreter changes.** Frame accounting for stack traps now uses the §4.1 size of every local (previously a flat per-slot estimate), so the 1 MiB-frame fixtures behave the same way natively and interpreted; `thread_local` globals are saved and replaced around each eagerly run thread.
- **Language additions.** `thread_local name: T = literal | zero()` at module level (Copy types only, `TLS_INVALID_DECL`, rule 6.5); `extern "C" func f(...) -> T { body }` exports `f` under its C symbol; `now_ns()` (monotonic clock) for benchmarks.
- **Fixture headers added.** `//! trap-record: none` (a custom handler writes no default record) and `//! c: file.c` (an FFI fixture's C counterpart, native-only execution).

## Deviations from the plan

- **The library is a C runtime, not kern.** Plan 2.3 rewrites the Phase 1 intrinsics in kern over a raw-pointer core (`RawPtr` load/store/offset, `alloc`/`free`, `unsafe assert`). The raw-pointer language surface does not exist yet, so the containers are implemented once in C with the Phase 1 signatures, and the interpreter keeps its Go implementations. The raw-pointer core, `std/raw.kern` and the kern containers move to Phase 3 with the freestanding runtime that needs them; the contract-block obligation on safe wrappers is enforced already (`UNSAFE_MISSING_CONTRACT`).
- **Interpreter FFI bridge deferred.** The purego shim bridge of plan 2.4 is not built; FFI fixtures (`//! c:`) execute natively only and compare with their expected output. The reverse direction (C calling an exported kern function) is covered natively by `tests/ffi/scalars.kern`.
- **One architecture.** Only darwin/arm64 is available here: no x86-64 host, no QEMU. `--target` is accepted and `emit-asm` cross-emits, but cross-linked executables need the target's linker and were not run. The CI matrix of plan 2.4 is a repository-setup task, not done in this working copy.
- **Stack trap spans.** Natively the fault handler knows no source location, so a stack trap's record carries span `(0, 0)` while the interpreter reports the span of the function whose activation overflowed; the harness compares stack traps by kind (ADR-012).
- **Thread identifiers.** The interpreter runs threads eagerly on one thread and always reports `thread: 0`; native records carry a per-thread counter. No fixture traps on a spawned thread, so the differential suite is unaffected.
- **The non-conforming-handler subprocess test** cannot take the held `Mutex` as the plan describes: second-class access makes a held mutex unreachable from a handler, so the test demonstrates the obligation with a handler that allocates after a stack trap taken inside an allocation (both outcomes, deadlock or `SIGABRT`, are accepted and logged).
- **IR text parser** (deferred from Phase 1) remains deferred: the native backend consumes the in-memory IR and never needed it; the corrupted-IR corpus under `tests/ir/` moves with it.
- **`show-effects` and `apply-structured-edit`** are later-phase commands and were not implemented.

## Benchmarks

`bench/` holds the harness (`bench/harness`, Go), the protocol (`bench/PROTOCOL.md`, `bench/CHECKSUMS.md`), C and Rust and kern implementations of the seven container workloads, `bench/toolchain.lock` (clang 23.1.4, rustc 1.99.0, go 1.27.3) and the generated `bench/REPORT.md` (5 runs per row, 95% confidence intervals, peak RSS, text bytes, compile time, unsafe lines, `show-allocations`/`show-copies` counts, the aliasing experiment). All three implementations of every workload print the same checksum. Host: Apple M4 Pro, darwin/arm64. kern rows are `--opt release` (primary) and `--opt debug`; every kern program is safe code (0 unsafe lines), so only the user-level row of §2.3(a) exists.

| Workload | C (Mops/s) | Rust | kern release | kern / best | RSS kern / lowest | 10% targets |
|---|---|---|---|---|---|---|
| vector | 1165 | 847 | 290 | 0.25 | 1.00 | RSS only |
| hash_table | 18.8 | 12.0 | 9.2 | 0.49 | 1.06 | RSS only |
| btree | 3.3 | 4.4 | 2.5 | 0.58 | 3.21 | neither |
| arena_ast | 119 | 136 | 65 | 0.48 | 1.37 | neither |
| ring_buffer | 651 | 947 | 956 | 1.01 | 1.03 | both |
| wal | 7.3 | 7.3 | 2.8 | 0.39 | 1.00 | RSS only |
| packet_parser | 130 | 157 | 31 | 0.20 | 1.07 | RSS only |

The throughput target is met on one workload of seven and the RSS target on five. Where the gap comes from (from reading the emitted LLVM, not measured per cause): every container operation is an out-of-line call into the C runtime that reloads the handle and re-derives the element pointer (`Vec.push`, `Vec.at`, `Pool.get`), with no inlining across the kern/C boundary; every integer operation carries its overflow check (ADR-008, both build configurations); sequence accesses are re-materialized from `{ptr, len}` pairs on each loop; and `btree`'s RSS comes from the runtime `Pool` growing three parallel arrays by doubling and never shrinking (`kern_pool_alloc`). None of these is a language-level cost; Phase 3's kern-side containers over the raw-pointer core (deferred from 2.3) remove the call boundary, and link-time optimization of the runtime is a build-system item.

**`[unproven]` aliasing experiment** (vector): with the attributes as emitted 295.2 ± 7.3 Mops/s, with every `noalias`/`readonly` stripped 301.1 ± 4.3, Rust 847 ± 43. LLVM gains nothing from the attributes on this workload: the loop's loads go through the runtime's `Vec` handle rather than through the attributed parameters, so the §5.4 attributes have no load to disambiguate yet. The experiment is re-run once containers are in kern.

## Reviews

- **prism** (OpenAI gpt-6.1-sol, staged diff, docs/fixtures/prelude excluded): 22 findings (13 high, 9 medium), all addressed: aggregate equality is structural through per-type equality glue (fields, elements, union tag then active payload, ordered float equality) instead of `memcmp`; floating-point constants are emitted as exact IEEE hexadecimal; signed left-shift overflow is checked with an arithmetic right shift; unsigned-to-signed conversions check the signed maximum (including `u64` → `i64`); spawned functions with multiple results are packed as a tuple; the entry wrapper uses the function's return convention; drop glue covers tuples; `thread_local` initializers are range-checked and `zero()` requires a zeroable type; the interpreter charges access parameters for their descriptor, not the referent; layouts are memoized by declaration and saturate at `layout.MaxSize` (the backend reports a type too large); the runtime checks vector size arithmetic, grows pools transactionally, increments `Shared` counts with a compare-exchange loop, uses an error-checking pthread mutex (recursive acquisition traps through `EDEADLK`) instead of racy owner bookkeeping, performs narrow atomic read-modify-writes in one compare-exchange, reserves the scope's join slot before starting a worker, and sizes and advances the generation-seeding hook correctly; `--test-hooks` now gates the test symbols natively (`-DKERN_TEST_HOOKS`), and accept fixtures without an expected-output file still run natively.
- **realitycheck** (spec + plan, OpenAI gpt-6.1-sol, `--max-tokens 65536`): PARTIALLY_ALIGNED, score 100, no drift, no violations; 129 spec items and 109 plan items inventoried, every one PARTIAL or UNCLEAR because the tool sees a symbol inventory rather than bodies or test results (Phases 3–5 are unimplemented as expected).
- **verifier** (offline): 86 low-severity unit gaps (exported symbols without a direct unit test, up from 70 in Phase 1 with the new `analysis`, `layout`, `backend` and `native` packages); the backend has shape tests (`compiler/backend/llvm/emit_test.go`), the layout package a C `offsetof` comparison, the native builder a unit test, and the analysis commands are covered through `cmd/kern/cli_test.go`.
- **Benchmark agent notes** (`bench/kern/COMPILER_ISSUES.md`): one real bug (untyped constant initializers crashed the interpreter) fixed with fixtures; one wrong-rejection report that is rule 5.4.2 behaving as specified; the missing wrapping forms of §4.1 added as `wrapping_add`/`wrapping_sub`/`wrapping_mul`.

## Known limitations carried into Phase 3

- Detached threads (`Thread` dropped without `join`) leak their result block.
- `Pool.free` destroys through the element's drop glue only when the element type needs destruction; the pool itself is destroyed in reverse slot order.
- Union payloads larger than 128 bytes are moved through a temporary alloca per `MakeUnion`.
- The validator still exempts compiler temporaries and does not track out-storage fields; globals are exempt from its ownership analysis (they are Copy by rule 6.5).
