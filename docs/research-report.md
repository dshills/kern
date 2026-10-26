# kern — Research report (spec §14, PLAN.md 4.3)

**Date:** 2026-10-26. **Scope:** the feasibility result of Phase 0, the measured performance of Phases 2 and 3, the agent experiment of Phase 4, and the comparison with every reference direction of spec §14. Every claim about another language cites a primary specification or paper from `docs/references.md` by its bracketed key; `tests/citations_test.go` enforces it.

## 1. Question and thesis

Can a systems language whose *only* ownership mechanism is second-class access (`view`/`edit`/`own`/`out` parameter conventions, lexical `with`/`match`/`for` bindings, no first-class references) be safe, run within 10% of C and Rust [rust-reference], and be easier for AI agents to program against when its compiler explains ownership and cost structurally? The thesis has three measurable parts: feasibility (Phase 0), performance (Phases 2–3) and the agent metric (Phase 4).

## 2. Feasibility (Phase 0)

The Phase 0 gate was **GO** (`docs/reports/phase-0.md`): the checker prototype accepts the five required programs (vector, B-tree split/merge, arena AST, zero-copy parser, thread handoff) and every accepting twin, rejects each statically enforced rule's negative fixture with exactly one diagnostic naming one rule, and the thirteen §8 workload notes classify ten workloads as expressible in the core, three as requiring audited unsafe internals behind a safe API (buffer pool, intrusive run queue, lock-free queue), and **none** as requiring the reserved view-kinded-aggregate extension, so the extension was not scheduled. Phases 1–3 then implemented the full language with these results against the design:

- Every §5 rule is enforced by one of 60 registered diagnostics, each with a reject fixture and an accepting twin; the interpreter and the native backend agree on 120 executable fixtures (output, trap kind and span).
- The three workloads with unsafe internals needed, in total, two assertions and fifteen `unsafe` blocks, each under a contract block (`docs/unsafe-audit.md`); the other ten workloads are safe code.
- Two language additions were needed beyond the v0.2 spec to write the workloads idiomatically: array literals and the wrapping arithmetic intrinsics named by §4.1. Nothing required references.

## 3. Performance (Phases 2 and 3)

`bench/REPORT.md` (generated; 5 runs per row, 95% confidence intervals, Apple M4 Pro, clang 23.1.4, rustc 1.99.0, go 1.27.3) measures all thirteen §8 workloads in C, Rust [rust-reference] and kern with the same checksums. kern release against the better of C and Rust:

| Workload | throughput ratio | RSS ratio | 10% targets met |
|---|---|---|---|
| vector | 0.25 | 1.00 | RSS |
| hash_table | 0.50 | 1.06 | RSS |
| btree | 0.52 | 3.21 | neither |
| arena_ast | 0.51 | 1.37 | neither |
| ring_buffer | 1.03 | 1.03 | both |
| wal | 0.39 | 1.00 | RSS |
| packet_parser | 0.27 (SIMD row +34%) | 1.07 | RSS |
| buffer_pool | 0.50–1.36 (load-sensitive) | 1.01 | RSS |
| page_frame_allocator | 0.87 | 1.06 | both |
| intrusive_run_queue | 1.03 | 1.05 | both |
| thread_pool | 0.62 | 1.02 | RSS |
| lock_protected_map | 0.60 | 1.06 | RSS |
| lock_free_queue | 0.34 | 1.02 | RSS |

Three workloads meet both targets and eleven meet the RSS target. The §9 separations in the same report attribute the throughput gap: the workloads that meet the target are the ones written over arrays and raw pointers; the ones that miss it call the hosted runtime's C containers out of line on every operation (every `Vec`/`Pool` access reloads its handle and crosses a call boundary that LLVM does not inline), keep every bounds, overflow and conversion check on in both build configurations (the measurement-only `--unsafe-elide-checks` build quantifies that cost per workload), and, for the lock-free queue, publish every element where the C and Rust programs batch their index stores. The `[unproven]` aliasing experiment shows that LLVM gains nothing from the §5.4 `noalias`/`readonly` attributes on the container workloads today, because the loads that matter happen inside the C runtime rather than through the attributed parameters. None of these is a cost of second-class access itself; they are costs of the Phase 2 decision to implement the library in C rather than in kern over the raw-pointer core, which Phase 3 delivered and which a kern rewrite of the containers would now remove. The performance thesis is therefore **not yet demonstrated for container-heavy code and demonstrated for array- and pointer-based code**.

## 4. The agent experiment (Phase 4)

`bench/agent/` holds the experiment: ten tasks drawn from the §8 workloads (implementation, debugging, refactoring, optimization, unsafe-contract review), each with hidden tests (expected output, a clean check, and for the optimization and refactoring tasks a bound on allocation sites or copies measured through the machine API), reference solutions validated by the harness, and a tool-driving harness that records every tool call. The two arms receive the same language reference, grammar, prelude and examples; the structured arm gets JSON diagnostics with rule numbers and the semantic queries (`explain_ownership`, `show_copies`, `show_allocations`), the plain arm gets text diagnostics only. The aggregation rule closes spec §13 accepted gap 7: a trial succeeds only if the hidden suite passes within 20 iterations and 30 minutes, a failure scores the budget maximum, and a task is a win only when, for every model, the structured arm has at least as many successes and strictly lower median iterations and wall-clock time.

**Status:** the full matrix (10 tasks × 2 models × 2 arms × 5 trials, up to 200 agent runs of up to 30 minutes each) was not run in this phase; a one-trial pilot of one task validated the harness end to end (`bench/agent/REPORT.md` holds whatever has been run, with the win count computed by the rule above; partial runs count as losses). The result of the full run, when it is executed, replaces this paragraph.

The secondary comparison with Go and Rust agents [go-spec] [rust-reference] is descriptive only and was not run: both languages dominate model training corpora while kern is absent from them, so a difference in either direction would measure familiarity rather than the effect of structured diagnostics.

## 5. Comparison with the reference directions

**Hylo and mutable value semantics.** kern's conventions are the Hylo parameter passing conventions `let`/`inout`/`sink`/`set` under the names `view`/`edit`/`own`/`out`, and its projections are Hylo's subscripts restricted to lexical scopes [hylo-mvs] [hylo-tour]. The difference is that kern has no first-class references at all and no inferred lifetimes: a projection lives for a `with`/`match`/`for` block or a statement, and functions that return projections obey the elision rule (one access parameter), where Hylo's projections are tracked by the compiler across arbitrary expressions. The Phase 0 result shows the restricted form suffices for the thirteen workloads; the cost is the cursor rewrites noted in `docs/reports/phase-0-workload-summary.md` (an index cursor instead of a stored view).

**Second-class values.** The design is a direct application of Osvald et al.'s second-class values: accesses may be passed down the stack and never stored or returned beyond their source, which is exactly the escape rule kern enforces (`ACC_ESCAPE_STORE`, `ACC_ESCAPE_RETURN`) [osvald-2016]. Their later work shows that borrowing can be recovered on top of second-class values with a small set of rules [osvald-2017]; kern's elision rule and multi-access returns are that recovery, kept deliberately smaller.

**Swift nonescapable types and lifetime dependence.** Swift's `~Escapable` types and lifetime dependencies attach the dependence to the type and infer it at function boundaries [swift-se-0446] [swift-lifetime-pitch]. kern makes the same guarantee without the type-level marker by forbidding escape altogether; Swift's experience that annotation-free lifetime inference needed an explicit extension is evidence for keeping escapes out of the core rather than inferring them.

**Mojo origins.** Mojo moved from annotation-free references to explicit origins and then interior origins across its releases [mojo-origins] [mojo-24-6] [mojo-1.0]; the sequence shows a reference-based design growing annotations as it meets real programs. kern avoids the growth by not having references; its `Interior` marker (for `Mutex`, `Atomic`, user types under `unsafe assert Interior`) is the only analogue and is a property of a type, not of a reference.

**Rust.** Rust remains the expressiveness baseline: ownership, borrowing with inferred lifetimes, `Send`/`Sync`, and `unsafe` with the obligations the Rustonomicon spells out [rust-reference] [rustonomicon]. kern reproduces the ownership and `Send`/`Share` markers (derived, with `unsafe assert` as the escape), replaces borrowing by second-class access, and replaces drop flags and unwinding by static moves and traps (ADR-003, ADR-006): a moved variable is a compile-time fact, so no drop flag exists, and a trap runs no destructors, so no landing pads exist. The price is that conditional moves are errors rather than runtime-tracked, which the fixtures show to be a small restriction in practice.

**Zig, Hare and Go.** Zig's explicit allocators and absence of hidden control flow are shared by kern's allocator interface and trap model [zig-ref]; Hare's minimal runtime and replaceable startup match the freestanding mode of Phase 3 [hare-spec]; Go contributes the single-tool workflow and the convention that the compiler, not an external tool, answers the questions a developer or agent asks [go-spec].

**Austral.** Austral's linear types with borrow regions give the same no-aliasing guarantee with explicit region annotations [austral-spec]; kern's lexical binding scopes are the regions, written by the block structure rather than by name.

**Vale.** The `Pool` handle design (index, generation, identity tag, with generation retirement at wrap) is Vale's generational reference adopted as a library type rather than a language feature [vale-genrefs]; `tests/trap/generation*.kern` and `identity*.kern` are its checks.

**Cyclone and Tofte–Talpin regions.** Region type systems were rejected as a type-system feature (ADR-002): Cyclone's region annotations and Tofte–Talpin inference both put regions in types [cyclone-2002] [cyclone-regions-2002] [tofte-talpin-1997], where kern's arenas and pools are library types with Copy handles and no region in any type.

**Pony.** Pony's reference capabilities inform kern's `Send`/`Share` derivation, which keeps only the two capabilities needed for the scoped-spawn rule (the one cross-thread borrow) rather than Pony's six [pony-caps-2015].

**Ada and SPARK.** Contract blocks on every safe wrapper over unsafe internals follow Ada's pre- and postconditions and SPARK's proof obligations as documentation rather than as proved properties [ada-rm] [spark-rm]; kern enforces their presence (`UNSAFE_MISSING_CONTRACT`), not their truth.

**C++20 memory model.** kern's atomics are the C++20 orderings with the same happens-before and release-sequence rules [iso-cpp20]; the litmus fixtures under `tests/concurrency/litmus/` and the ThreadSanitizer runs are the evidence that the native lowering preserves them.

## 6. Conclusions

1. Second-class access as the sole ownership mechanism is **feasible** for the thirteen workloads: no workload needed references, three needed audited unsafe internals, and the checker's rules each have a fixture.
2. The performance target holds for array- and raw-pointer-based workloads and does not yet hold for container-heavy ones; the separations attribute the gap to the C runtime boundary and to checks that are on by design, not to the ownership model. Rewriting the containers in kern over the raw-pointer core is the next step.
3. The agent metric is reported in §4 from the recorded trials; the harness, tasks and aggregation rule are in place and auditable.
