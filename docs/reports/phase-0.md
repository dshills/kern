# Phase 0 report — Feasibility (go/no-go on ADR-1)

**Date:** 2026-10-26. **Plan:** `specs/initial/PLAN.md` Phase 0. **Spec:** v0.2.

## Decision: GO

Second-class access as the sole ownership mechanism (ADR-001) accepts all five required programs and every accepting twin, rejects every statically enforced rule's negative fixture with exactly one diagnostic citing exactly one rule, and no §8 workload requires the reserved extension. Phase 1 may start, subject to the owner sign-off recorded below.

## Deliverables

| # | Deliverable | Location | Status |
|---|---|---|---|
| 1 | Language reference draft | `spec/grammar.ebnf`, `spec/types.md`, `docs/ownership.md`, `spec/rules.md` (generated), `docs/references.md` | done |
| 2 | Checker prototype (no codegen) | `compiler/{source,lexer,parser,ast,resolve,check,diag}`, `cmd/kern check` | done |
| 3 | Negative fixtures + twins, adversarial fixtures | `tests/negative/` (46 pairs), `tests/adversarial/` (19 files + `NOTES.md`), `tests/api/` (5 request fixtures) | done |
| 4 | Required programs | `examples/{vector,btree,arena_ast,packet_parser,thread_handoff}` | done, all accepted |
| 5 | Workload notes and classification | `docs/workloads/*.md` (13), `docs/reports/phase-0-workload-summary.md` | done |
| — | ADRs | `docs/decisions/ADR-001..011` | done (1–9 decided, 10–11 open with closing experiments) |

## Reviews

- **prism** (OpenAI gpt-6.1-sol, staged diff, docs excluded): first pass 50 findings (19 high); every compiler finding was fixed and locked in with a regression fixture (two lexer non-termination bugs, nested-field initialization checked against the wrong field, `?` not consuming a move-only `Result`, loop conditions not re-evaluated on back edges, out-storage merges losing fields, marker derivation without a cycle guard and later keyed by declaration instead of instantiation, missing receiver permission checks on constrained calls, owner type parameters not validated, forward references to generic types, constraint checks run before implementations were collected, specialized or conditional impls registered globally, inexact literal bounds, private fields in literals, unification with the error type, trailing JSON data, command validation before reading stdin, short-circuit operands as conditional paths). Findings against the workload sketches are recorded as open risks in `docs/reports/phase-0-workload-summary.md`.
- **realitycheck** (spec + plan): PARTIALLY_ALIGNED, score 100, no drift, no violations (later phases unimplemented, as expected).
- **verifier** (offline): 53 low-severity unit-test gaps; unit tests were added for `compiler/resolve` types and markers, `compiler/source` and `compiler/diag`.

## Acceptance criteria

1. `kern check` accepts all five programs and every accepting twin — **met** (`go test ./tests/`).
2. Every reject fixture produces exactly one diagnostic whose code and rule match its header — **met**.
3. Registry coverage: level 1 (every code named in the spec, `docs/ownership.md` and `spec/types.md` is registered) and level 2 (every registered code names its fixture or test) for every code; level 3 (executable coverage) for every Phase 0 code — **met** (`TestRegistryInventory`, `TestRegistryCoverage`; `TYPE_TOO_MANY_VARIANTS` is covered by the generated `TestTooManyVariants` because a 65,537-variant fixture is too large to commit).
4. Thirteen workload notes exist; reserved-extension count is **0**, within the limit of two — **met**. Classification: 10 expressible in the core; 3 require audited unsafe internals behind a safe API (buffer pool, intrusive run queue, lock-free queue).
5. ADR-1 through ADR-9 written in the §10 template; ADR-10 and ADR-11 drafted with their closing experiments — **met**.
6. `spec/grammar.ebnf` parses every fixture and example without ambiguity — **met** (the parser is the grammar's implementation; every fixture and example parses; the parser tests cover each production).
7. Language-reference acceptance — speccritic result: see "Reference gate" below. **Owner sign-off: pending.** The project owner records the commit hash of the three reference files here before Phase 1 work begins.

## Reference gate (speccritic on `spec/types.md` and `docs/ownership.md`)

Both files were gated with speccritic (OpenAI gpt-6.1-sol, `general` profile, with the specification as context) after the structural findings of the first run were fixed:

| File | Verdict | Critical | Warn | Accepted warnings |
|---|---|---|---|---|
| `spec/types.md` | VALID_WITH_GAPS (score 51) | 0 | 7 | acronym detector on the testing paragraph; import-alias scope across files; lookup precedence of implicit core names versus user declarations; equality on aggregates with function fields and on unions; legality of recursive value types; out-of-range literal outcome (it is `TYPE_MISMATCH`, stated in the resolver and `spec/rules.md`) |
| `docs/ownership.md` | VALID_WITH_GAPS (score 2) | 0 | 14 | the acronym detector flags the glossary's own entries; one restatement drops the spec's "or compose only such functions" wording on multi-access returns (deliberate, see `docs/ownership.md` 5.3.4); loop-subject release timing; combined ordering of cleanup classes; handle equality versus slot identity; release-callback signature (known gap 1 below) |

The first run also found a real contradiction, now fixed: the capitalization visibility rule contradicted the lowercase standard-library functions, so `std/*` modules now export every declaration regardless of case. The second run found that `zero[T]()` would have admitted function pointers and that `&&`/`||` had no short-circuit rule; both are now specified (`spec/types.md` §1 and §5), enforced by the compiler (`CONSTRAINT_UNSATISFIED`; the right operand of a short-circuit operator is a conditional path for ownership joins), and covered by fixtures. The accepted warnings are assigned to the Phase 1 language-reference revision.

## Deviations from the plan and spec, with reasons

- **Built-ins `len(x)` and `zero[T]()`.** The spec defines `len(v)` for sequence accesses (§5.2) but the grammar had no production for it; it is a built-in call. The grammar has no array literal, so arrays had no legal initializer; `zero[T]() -> T` for `Copy` types (the all-zero value, inferred from the expected type: `var d: [4096]u8 = zero()`) was added to the prelude. Both are recorded in `spec/types.md` and `std/core.kern`.
- **`release` and `trap` are contextual keywords.** `union Ordering` has a variant named `release` and `trap.set_handler` names a namespace, so both words lex as identifiers and the parser recognises them by position.
- **Consume/view conflict reports as `OWN_USE_AFTER_MOVE`.** Spec §5.4 states that a conflict involving a consume of a variable is `OWN_USE_AFTER_MOVE`; the plan's fourth `ACC_CONFLICT` variant (consume/view) therefore has fixture `tests/negative/conflict_consume_view.kern` expecting `OWN_USE_AFTER_MOVE`.
- **Match subjects are frozen only while an arm holds an access binding.** Arms that bind only `Copy` values copy them on entry, so the subject is not accessed afterwards; this is what lets the arena-AST evaluator recurse on `a` inside a `match a.nodes.get(id)` arm with handle bindings. The rule text in `docs/ownership.md` 5.3.1 is to be aligned in Phase 1.
- **A projection of a `Copy` value may be bound with `:=`.** `x := v.get(i)` reads the value (spec §5.3: "a read of a Copy field" consumes the projection); `ACC_UNBOUND_PROJECTION` applies to move-only elements and sequence accesses.
- **`NAME_PRIVATE` introduced in Phase 1.** Privacy across modules needs the import system, which Phase 0's single-module programs do not exercise.
- **Multi-file fixtures.** The harness accepts a directory of `.kern` files as one fixture (used by `MODULE_MISMATCH`).
- **Call-site permission checks live in the resolver.** `ACC_PERMISSION_ESCALATION` for arguments and receivers is reported once, by the resolver, which knows place mutability; the checker reports it for assignment, `take` and `replace` through a view binding.
- **Receiver type parameters.** `impl Vec[T] { ... }` introduces `T` implicitly when it is not a declared type, so the prelude can be written without repeating `[T]` on every impl.

## Known limitations carried into Phase 1

- Generic bodies are checked once against their constraints and not instantiated (as planned); specialization is Phase 1.
- The interior-pointer-after-growth case is rejected by the binding freeze rule rather than by a finer analysis, as the spec requires; this makes some correct programs (reading a container while a projection into it is bound) verbose. Measured by the workload notes' cursor rewrites.
- `thread.spawn` and `Scope.spawn` are typed by the checker as intrinsics; they are not expressible as ordinary generic functions in the Phase 1 subset.
- The `with` form over an owned value and `match move x` consume the subject eagerly; their destruction points are static as required but not yet surfaced by `explain-ownership` (Phase 1).

## Open design gaps surfaced by the workload notes (for Phase 1)

1. The release function of an accessor receives "the same receiver"; for buffer-pool `pin`/`unpin` the receiver must be the frame, not the pool (`docs/workloads/buffer_pool.md`).
2. `RawPtr[T].deref()` has no elision source; raw-pointer projections inside `unsafe` need a fresh-root rule (PLAN 2.3).
3. No condition variable in §5.5; the thread pool idles.
4. `Pool` must guarantee stable slot addresses for the intrusive queue's invariant.
