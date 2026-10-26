# AI-Native Systems Programming Language
## Research and Implementation Specification — v0.1
**Status:** Proposal; hypotheses require evaluation. **Date:** 2026-10-26. **Working name:** TBD.

## 1. Vision
Create an AI-first, human-readable, native ahead-of-time systems language capable eventually of building operating systems, databases, compilers, embedded software, and performance-critical applications. Seek C/Rust-class runtime performance, no mandatory garbage collector, explicit resource costs, strong memory and concurrency safety for safe code, and minimal freestanding runtime dependencies.

The hypothesis is that AI agents benefit from predictable semantics, structured compiler feedback, and verifiable invariants more than from syntactic shortcuts. Do not remove useful features simply because they are advanced: retain features when their semantics are small and their costs predictable.

**Critical unresolved research problem:** Achieve useful compile-time ownership/lifetime safety without Rust's general lifetime-annotation and borrowing complexity, while retaining expressiveness for real systems data structures. This is not guaranteed to be possible without tradeoffs.

## 2. Goals and exclusions
**Required eventually:** x86-64 and AArch64 native code; Linux/macOS development; freestanding targets; deterministic destruction; configurable allocators; C ABI; structs and layout control; atomics, SIMD, volatile MMIO, raw pointers and inline assembly in unsafe code; predictable allocation and dispatch; structured agent-facing tooling.

**Not initially required:** full operating system, production database, self-hosting, package registry, async runtime, class inheritance, reflection, elaborate macros, general theorem proving, or a feature-complete IDE.

**Success targets, not promises:** within roughly 10% of optimized equivalent C/Rust throughput on a diverse benchmark suite; memory use comparable for equivalent representations; robust negative safety tests; improved agent completion rate and/or reduced time-to-green versus Go/Rust with equivalent assistance. Record code size, allocations, compilation latency, and unsafe surface area.

## 3. Governing principles
1. Minimize *semantic* complexity, not keyword count.
2. Make heap allocation, deep copies, dynamic dispatch, synchronization, and bounds checks explicit or compiler-inspectable.
3. Never confuse AI-produced claims with compiler-established proofs.
4. Canonical formatting, unambiguous resolution, stable error codes.
5. Safe subset has precise guarantees; unsafe boundaries have documented obligations.
6. Design around realistic workloads before fixing syntax.
7. Keep the required runtime small enough for freestanding environments.
8. Allow machine-readable semantic edits without abandoning readable source.

## 4. Candidate surface language
Go-inspired grammar: functions, nominal structs, tagged unions, exhaustive matching, arrays, slices, loops, modules, local type inference, explicit errors, and simple monomorphized generics. No inheritance, implicit coercions, function/operator overloading, or unrestricted macros initially. Closures and dynamic interfaces deferred until their ownership consequences are understood.

Illustrative pseudocode only:

```text
module storage
struct Page { id: u64; data: [4096]u8 }
union Result[T,E] { Ok(T); Err(E) }
func checksum(data: view[u8]) -> u64 {
    total := 0u64
    for b in data { total = total + u64(b) }
    return total
}
```

A language reference must specify integer widths, arithmetic overflow and shifts, float semantics, layout, alignment, padding, endianness, bounds behavior, error propagation, destructor behavior, and panic/abort behavior. Undefined behavior must not be possible through safe language constructs.

## 5. Ownership model — highest priority
**Safety contract:** Safe code must prevent use-after-free, double-free, dangling references, uninitialized reads, unsynchronized mutable aliasing, and data races. This does *not* imply all array bounds or overflow conditions can be proved statically; runtime checks may be necessary. Unsafe code and FFI are trusted boundaries.

**Unique ownership:** Heap resources have one owner; moves invalidate the source; destruction occurs exactly once on normal scope exit unless ownership is transferred. No silent deep copy or hidden heap promotion. Specify partial moves, reassignment, early return, recursion, destructor ordering, and failure handling.

**Temporary access:** Explore `view[T]` (read-only) and `edit[T]` (exclusive mutable) as non-escaping function parameters. Initially prohibit returning, heap-storing, or asynchronous capture of temporary access. Compiler still must enforce aliasing: eliminating lifetime annotations does not eliminate borrow checking. Compare lexical scopes with inferred non-lexical access windows.

**Regions:** Region allocations share a lifetime; compiler prohibits escaping references and owners. Research nested regions, cross-region references, stable addresses, destructor policy, allocator ownership, and concurrency.

**Stable handles:** Owner-managed index/generation handles for B-trees, graph structures, buffer pools, and object registries. A generation validity test is a runtime check; distinguish logical validity from memory safety.

**Concurrency:** Ownership transfer, immutable sharing, and synchronization-mediated scoped mutation. Define thread-transfer/share capabilities, atomics, memory ordering, and happens-before. Lock-free structures may use audited unsafe internals.

**Adversarial cases:** intrusive linked lists; self-reference; parent pointers; cycles; interior pointers after vector growth; callbacks retaining references; FFI lifetime mismatch; partially initialized objects; destructor reentrancy; async suspension; cancellation; concurrent region access.

## 6. Runtime and low-level support
No mandatory GC or scheduler. Define startup, panic/trap behavior, TLS, stack checks, OOM handling, allocation APIs, and a freestanding mode with no OS assumptions. Support explicit heap allocators, arenas, slab pools, and custom page allocators. Track allocator lifetime versus object lifetime.

Unsafe features: raw pointers, pointer arithmetic, memory-mapped registers, volatile access, packed/unaligned access, inline assembly, allocator internals, and selected FFI operations. `unsafe` authorizes narrowly specified operations, not a blanket waiver of type checking. Each safe wrapper over unsafe internals must document invariants, preconditions, and postconditions.

## 7. Compiler architecture
```text
source / structured agent edits
 -> lexer/parser -> canonical AST
 -> name and type resolution
 -> ownership / initialization / region / effect checker
 -> typed, ownership-aware semantic IR
 -> verified lowering and optimization
 -> LLVM (candidate) -> native objects / freestanding binaries
```
Preserve moves, scoped access, destructor points, region boundaries, required checks, and unsafe operations through verified lowering. Compare LLVM with a C-emitting bootstrap and fast debug backend. Implementing the prototype compiler in Go is reasonable; the compiler's own GC does not imply GC in produced programs.

**Machine API:** versioned JSON diagnostics with stable codes, spans, related locations, violated rule, and optional machine-applicable fixes. Commands: `check`, `format`, `explain-ownership`, `show-layout`, `show-allocations`, `show-copies`, `show-effects`, `show-bounds-checks`, `emit-ir`, `emit-asm`, and `apply-structured-edit`. Explicitly distinguish `proven`, `runtime-checked`, `assumed-unsafe`, `estimated`, and `unknown`.

Example:
```json
{"schema_version":"0.1","diagnostics":[{"code":"OWN_USE_AFTER_MOVE","severity":"error","file":"main.ai","span":{"start":128,"end":134},"related":[{"label":"moved here","start":91,"end":97}]}]}
```

## 8. Representative workload acceptance suite
Implement comparable C, Rust, and candidate-language versions of: vector; hash table; B-tree split/merge; arena AST; database WAL; buffer pool with pin/unpin; zero-copy packet parser; ring buffer; page-frame allocator; intrusive run queue; thread pool; lock-protected map; and an audited unsafe lock-free queue.

For each, report ownership graph, unsafe requirements, copies, allocations, peak RSS, throughput, p50/p99 latency where applicable, code size, and compilation time. Test failure and concurrency paths, not only successful hot loops.

## 9. Performance and agent methodology
Use equivalent algorithms and safety/error semantics, pinned toolchain versions, optimization flags, repeated runs, confidence intervals, hardware details, and profiling. Separate backend immaturity from intrinsic language overhead. Use differential testing, fuzzing, sanitizers, and positive/negative compiler fixtures.

Test multiple strong AI models on implementation, debugging, refactoring, optimization, and unsafe-contract review. Give all models the same language reference and examples. Measure correctness on hidden tests, iterations, token use, wall-clock time, regressions, and unsafe code introduced. Ablate structured diagnostics and semantic query features to establish their independent value. Account for training-data advantage of Go/Rust.

## 10. Key decisions requiring evidence
- Unique ownership plus scoped access versus linear/affine types versus region inference.
- Whether any escaping safe references are allowed.
- Explicit moves versus inferred moves.
- Static-only bounds proofs versus safe runtime bounds checks (recommend checked by default).
- How region references interact with owned containers.
- How generics are constrained and monomorphized.
- Whether and how dynamic dispatch is introduced.
- How thread safety and atomics are specified.
- What requires unsafe and what can be wrapped safely.
- What freestanding runtime facilities are mandatory.

For each decision produce an ADR: problem, alternatives, invariants, hard counterexample, expressiveness cost, performance cost, complexity, experiments, and conclusion.

## 11. Implementation roadmap
**Phase 0 — Feasibility:** compare ownership models, define invariants, design 20 adversarial fixtures, model vector/B-tree/arena AST/thread handoff. Deliver go/no-go decision and ADRs.

**Phase 1 — Semantic prototype:** lexer, parser, types, moves, temporary access, regions, JSON diagnostics, positive and negative tests. Interpreter acceptable; no full optimizing backend required.

**Phase 2 — Native MVP:** LLVM or comparable backend, deterministic destruction, explicit allocator, arrays/slices, error unions, simple generics, C ABI subset, Linux/macOS builds. Benchmark representative containers.

**Phase 3 — Systems features:** freestanding output, layout, unsafe pointers, atomics, volatile/MMIO, inline assembly, thread primitives, allocator and buffer-pool prototypes. Audit unsafe boundaries.

**Phase 4 — AI/compiler integration:** semantic queries, structured transformations, incremental checking, differential testing, agent benchmarks, reproducible performance reporting.

**Phase 5 — Hardening:** specification stabilization, standard library, debugger, packaging, security review, target expansion, self-hosting decision.

Exit each phase only on measurable acceptance tests. Do not build a large compiler before validating the ownership hypothesis.

## 12. Suggested repository
```text
docs/{vision,ownership,compiler,unsafe,agent-protocol}.md
docs/decisions/ADR-*.md
spec/{grammar.ebnf,types.md,memory-model.md}
compiler/{lexer,parser,types,ownership,ir,backend}/
tests/{positive,negative,ffi,concurrency,freestanding}/
examples/{vector,btree,arena_ast,buffer_pool,allocator}/
bench/{c,rust,candidate}/
tools/agent-api/
```

## 13. Instructions to the evaluating high-performance model
Act as a skeptical language researcher and senior compiler architect. Treat this document as a hypothesis, not approved design. First identify the ten highest-risk assumptions and any impossibility/expressiveness tradeoffs. Compare at least three ownership models using B-trees, intrusive structures, arenas, FFI callbacks, and concurrency. Produce (1) feasibility verdict; (2) alternatives matrix; (3) proposed semantic core and safety boundaries; (4) example programs; (5) compiler/IR invariants; (6) positive and negative tests; (7) performance and AI-agent evaluation plan; (8) prioritized ADRs; (9) a 90-day prototype plan with explicit exit criteria. Label unproven claims and unresolved choices. Do not implement a full compiler before the semantic model has been reviewed.

## 14. Reference directions
Study Rust (ownership, unsafe, Send/Sync), Zig (explicit allocation and low-level model), Go (syntax/tooling), Austral (linear types), Hare (minimal systems language), Vale/Valen (ownership alternatives), Cyclone (region and pointer safety), Pony (reference capabilities), Ada/SPARK (contracts/proofs), and region-inference research. Cite primary specifications and papers in the subsequent research report; this document makes no claim that any one approach satisfies all requirements.

**Core thesis:** The product is not “Go with fewer features”; it is a compact, predictable systems language plus a compiler that can establish and explain safety and cost properties to AI agents. The feasibility of its simplified ownership model must be demonstrated.
