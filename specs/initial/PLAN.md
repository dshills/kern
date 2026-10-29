# kern — Phased Implementation Plan

**Source spec:** `AI_Native_Systems_Language_SPEC.md` v0.2 (2026-10-26). **Status:** plancritic gate passed 2026-10-26 (EXECUTABLE_WITH_CLARIFICATIONS, score 72, 0 critical). The clarifications are answered in the text. Accepted WARNs: a concurrency observation policy for byte-for-byte differential fixtures (to be defined in 3.3 before the first concurrency fixture); a complete configuration-switch inventory (to be added to §0 when Phase 2 introduces `--test-hooks` and the stack-check option); the trap-handler contract (decided: documented obligation, ADR-012). Section references (§n) are to the spec.

## 0. Conventions that apply to every phase

**Repository.** Go module `github.com/dshills/kern`, Go 1.25, `golangci-lint run ./...` clean at every phase exit. Layout follows §12 with Go packages under `compiler/`:

```text
cmd/kern/                      CLI; one subcommand per §7 machine command plus `run` (interpreter) and `build`
compiler/source/               file loading, UTF-8 byte offsets, spans
compiler/lexer/                tokens
compiler/parser/               canonical AST (compiler/ast/) per spec/grammar.ebnf
compiler/resolve/              modules, names, types, generic constraints per spec/types.md
compiler/check/                ownership, initialization, access, Send/Share/Interior checker
compiler/ir/                   semantic IR, textual printer, validator
compiler/lower/                AST -> IR
compiler/interp/               IR interpreter (Phase 1)
compiler/backend/llvm/         textual LLVM IR emitter (Phase 2)
compiler/diag/                 diagnostic registry: code -> rule number, JSON encoding
compiler/format/               canonical formatter (Phase 1)
runtime/                       hosted and freestanding runtime (Phase 2/3; C and kern sources)
std/                           standard library written in kern (Phase 1 onward)
spec/{grammar.ebnf,types.md,memory-model.md,format.md}
docs/decisions/ADR-*.md
tests/{positive,negative,trap,ffi,concurrency,freestanding}/   fixtures
examples/{vector,btree,arena_ast,buffer_pool,allocator,...}/
bench/{c,rust,kern}/ plus bench/harness/
tools/agent-api/schema.md
```

**Build configuration.** Two independent dimensions, both present on every command that compiles or runs and both part of every cache key: `--opt debug|release` (default `debug`; `debug` enables container identity tags, `release` disables them; every other check is on in both, ADR-8) and `--env hosted|freestanding` (default `hosted`; `freestanding` has no `SystemAllocator`, no OS, halting traps). All four combinations are valid from Phase 3; before Phase 3 the CLI rejects `--env freestanding` with exit 2 and diagnostic `CLI_INVALID_FLAGS`, and the fixture harness's default matrix is the two hosted configurations. From Phase 3 the default matrix adds the freestanding configurations only for fixtures whose `config:` line names `env=freestanding` (freestanding fixtures use the console device and are not valid hosted programs, and vice versa). A `test-hooks: true` fixture is automatically restricted to `opt=debug`. `--target T` is a third, orthogonal key. The CLI and incremental-cache test matrices enumerate exactly the combinations valid in the current phase.

**CLI contracts for `run` and `build`.** These two subcommands are not §7 machine-API commands; they take positional arguments. `kern build [--target T] [--opt O] [--env E] -o <out> <files...>` writes an executable at `<out>`; `kern run [--opt O] [--env E] <files...> [-- args...]` interprets the program (`--env freestanding` runs the freestanding emulation of 3.1). Exit codes: 0 on success; 2 when `check` produced diagnostics (the §7 JSON response is written to standard output and nothing is built or run); 3 on internal error (validator failure, toolchain missing); for `run`, and for an executable produced by `build`, the program's own exit status otherwise.

**Machine-command invocation.** `kern <command> [--target T] [--opt O] [--env E] < request.json > response.json`: the request is the §16 JSON on standard input; `--opt` and `--env` are CLI-only (the 0.2 request schema is not extended) and default as above; `target` precedence is: the JSON `target` when `--target` is absent, `--target` when the JSON field is absent, and when both are present they must be equal or the command fails with exit 2 and one diagnostic `API_TARGET_CONFLICT` (category `api`); when neither is present the host triple is used. Contract tests cover omitted, single, equal, and conflicting cases.

**Request validation.** Before any compiler pass runs, the request is decoded and validated: malformed JSON → `API_MALFORMED_REQUEST`; `schema_version` other than `0.2` → `API_UNSUPPORTED_SCHEMA`; empty `files` → `API_NO_FILES`; a file that cannot be read → `API_FILE_NOT_FOUND`; for `explain-ownership` only, a missing `span` → `API_MISSING_SPAN` and a `span` that does not name one of `files` or violates the §16 span rules → `API_INVALID_SPAN`; every other command ignores `span` entirely, as §7 states, and never validates it; all category `api`, exit 2, `ok=false`. Contract tests cover missing, valid, and semantically invalid spans on `explain-ownership` and on `check`. A diagnostic that has no source location (these, and `INTERNAL` before decoding succeeded) uses `file: ""` and `span: {"start": 0, "end": 0}`; the JSON Schema permits the empty file name only for `API_*` and `INTERNAL` codes. CLI contract tests inject failures both before decoding (malformed input) and after it (toolchain missing).

**Standard output.** For the eleven §7 machine-API commands, standard output carries exactly one JSON response, on success and on every failure path (a test asserts this per exit path). For `run` and `build`: when `check` fails, the same single JSON response is written to standard output and the exit status is 2; otherwise `build` writes nothing to standard output, and `run`'s standard output belongs to the program.

**Trap record contract.** The spec's §6 handler semantics are implemented exactly: the hosted default handler writes the `TrapInfo`, the freestanding default handler is a no-op, and nothing runs before the handler. The plan only fixes the *format* of the hosted default handler's output: one line `{"trap": {"kind", "file", "span", "thread"}}` on standard error, which the interpreter's default handler also uses. Termination then differs by mechanism: the interpreter exits with status 134; a hosted native executable calls `abort()` and dies by `SIGABRT`; a freestanding image halts. Trap fixtures in hosted configurations must not install a custom handler (fixtures that test `trap.set_handler` are ordinary output fixtures that assert their own output). Freestanding trap fixtures install the test handler `std/testing.console_trap_handler`, which writes the same record line to the console device of 3.1, under both QEMU and the interpreter's freestanding emulation. The Go fixture harness verifies the termination mechanism and the record separately: the outcome is `trapped(kind)` only if (a) exactly one trap record line is present on standard error (or the console), and (b) the process terminated by the mechanism promised for its mode: exit status 134 for the interpreter, death by `SIGABRT` for a hosted native executable, and the halt protocol of 3.1 for a freestanding image. Any other combination is a failure. Every trap fixture is additionally a sentinel: its destructors and release functions, and the statement after the trapping one, each write a distinctive line, and the expected output asserts that none of those lines appears, proving that execution, destruction, and release do not continue after a trap (§6). Contract tests in `cmd/kern/cli_test.go` cover each exit code, each build-configuration combination, and the record format. These contracts are fixed in Phase 0 (for `check`) and Phase 1 (for `run`), and reused unchanged by `build` in Phase 2.

**Error reporting contract (Go).** Every error crossing a file, compiler-pass, or subprocess boundary is wrapped with context (`fmt.Errorf("pass %s: file %s: %w", ...)`); an internal error becomes `ok=false`, exit 3, and one diagnostic with code `INTERNAL`, severity `error`, rule `7.failure`, the request's first file, an empty span at offset 0, empty `related`, `fix: null`, `op_index: null` — exactly the §16 fields and nothing more. The wrapped error chain and, for subprocess failures, the captured tool standard error truncated to 4 KiB go to the structured log only. Logs are JSON lines on standard error carrying an internally generated `request_id` that is also logged alongside the response, so a client correlates the two by order within the invocation; the response schema is not extended. Conformance is validated against a hand-maintained JSON Schema `tools/agent-api/schema.json` transcribed from §16 (the normative copy for `schema_version` `0.2`); the generated `tools/agent-api/schema.md` is diffed against it, and every response in the test suite is validated against the JSON Schema.

**Source file extension:** `.kern`. The `main.ai` in the §7 example is illustrative; the diagnostic schema is unaffected.

**Fixture format (all phases).** One `.kern` file per fixture with a header block of `//!` lines:
```text
//! phase: 1                      accept and trap fixtures only: earliest phase in which the fixture is executable (§7 Testing), at least 1; reject fixtures omit it and are check-only by nature
//! expect: accept                or: reject CODE rule=N.N.N   |   trap KIND   |   output <name>.out
//! twin: <fixture-name>          the accepting twin of a reject/trap fixture, or the reject/trap twin of an accept
//! config: opt=debug env=hosted  optional; restricts the fixture to the named configuration(s); default is every valid combination
//! test-hooks: true              optional; links the test-only runtime (see 2.3) and makes the fixture invalid in --opt release
```
**Diagnostic registry categories.** `compiler/diag/codes.go` records, per code, its rule number, its category, and the phase that introduces it. Categories and their required tests: `lang` (every spec §4–§6 code; one source reject fixture plus accepting twin), `api` and `edit` (`API_*`, `EDIT_*`; one JSON request fixture under `tests/api/` per code, asserting the exact response), `cli` (`CLI_*`; contract tests in `cmd/kern/cli_test.go`), `internal` (`INTERNAL`; injected-failure tests that break the toolchain path and corrupt IR). An independent inventory test extracts every backticked `[A-Z]+_[A-Z_]+` token from the spec and `docs/ownership.md` and fails if any is missing from the registry, so an omitted registry entry cannot evade coverage; the per-category coverage test has three levels: (1) inventory completeness: from Phase 0 every spec code is registered with its category and introduction phase; (2) declaration completeness: from Phase 0 every registered code names its fixture or test file, which must exist; (3) executable coverage: a code's exact-response or fixture test runs only from its introduction phase, and the test for a later-phase code is skipped with that phase recorded. The Phase 0 acceptance criterion applies levels 1 and 2 to every code and level 3 to Phase 0 codes only. The fixture harness, `go test ./tests/...`, drives every source fixture through `kern check` from Phase 0 onward (checker availability is independent of execution: `extern "C"` declarations and `thread` signatures are checkable in Phase 0). From the fixture's phase it also runs `kern run` (interpreter) and, from Phase 2 or the fixture's phase whichever is later, `kern build` + execute, comparing both against `<name>.out` byte for byte. The interpreter must therefore execute every fixture class from its phase: FFI accept fixtures are tagged `phase: 2` (the interpreter calls the C counterpart through the bridge of 2.4), concurrency fixtures `phase: 3` (the interpreter backs `thread.spawn` with OS threads, see 3.3), and freestanding fixtures `phase: 3` (the interpreter's freestanding emulation of 3.1 runs them, so they are differential too). A `phase: 0` tag is rejected by the harness. A `reject` fixture must produce exactly one diagnostic whose code and `rule` match the header (§2.3). A `trap` fixture must terminate with a `TrapInfo` of the named kind on standard error and a non-zero exit. Configuration-dependent behavior uses the `config:` selector: the `identity` trap fixture is restricted to `opt=debug`, and its `opt=release` twin is an accept fixture whose expected output shows the foreign-handle access completing (memory-safely, per §5.6) with no identity trap. Every statically enforced rule and every trapping rule in the spec has at least one fixture and one twin; the registry in `compiler/diag/codes.go` is the single list of codes, with the category rules above.

**Diagnostic rule numbering.** Each code maps to one rule identifier of the form `§section.bullet` (for example `OWN_USE_AFTER_MOVE` → `5.1.4`). The mapping lives in `compiler/diag/codes.go` and is rendered into `spec/rules.md` by `go generate`; the Phase 0 language reference (§11 deliverable 1) restates §5 with these numbers.

**Definition of done for every phase.** All acceptance criteria below are met, `go test ./...` and `golangci-lint run ./...` are clean, the phase report is committed under `docs/reports/phase-N.md`, and no spec requirement scheduled for the phase is deferred without an entry in that report.

---

## Phase 0 — Feasibility (go/no-go on ADR-1)

**Goal.** Demonstrate that second-class access as the sole ownership mechanism (§1, ADR-1) accepts the five required programs and rejects every negative fixture with one diagnostic, before any code generation exists (§11 Phase 0).

**Depends on:** nothing. **Delivers:** a go/no-go decision.

### 0.1 Language reference draft
Files: `spec/grammar.ebnf`, `spec/types.md`, `spec/rules.md` (generated), `docs/ownership.md`.
- `grammar.ebnf`: the Phase 1 subset: modules, `struct`, `union`, `interface`/`impl`, functions with the four conventions (§5.2), `with`/`match`/`for` (§5.3), `unsafe` blocks, `contract` blocks (§6), `extern "C"` declarations (parsed and type-checked against the §7 type restrictions from Phase 0; linking and execution from Phase 2), `destructor` (§5.7), `release` accessors (§5.3), integer/float/bool literals with type suffixes, `?` propagation, `move x`, `take`/`replace`/`drop`/`copy` as built-in operators.
- `types.md`: module resolution (one directory = one module, explicit `import`), nominal types, local type inference for `:=` only, generic parameters `[T: Constraint]` with nominal interface constraints and the built-in constraints `Copy`, `Send`, `Share` (ADR-9 decided here as "monomorphized, nominal, no dyn"), `[]T` slices, `view[]T` shorthand (§5.2).
- `docs/ownership.md`: §5 restated rule by rule with rule numbers, every example from `ARCHITECTURE_EVALUATION.md` §8, and the accepted-WARN decisions from spec §13 (remainder of the minimum signed value by `-1` traps `overflow`; `get2` validates `a`, then `b`, then compares).

### 0.1a References
Files: `docs/references.md`.
Primary specifications and papers for every §14 reference direction (Hylo, Osvald et al. 2016 and 2017, Swift SE-0446 and the lifetime-dependency pitches, Mojo origins, Rust reference and Rustonomicon, Zig, Austral, Hare, Vale, Cyclone, Tofte–Talpin, Pony, Ada/SPARK, ISO/IEC 14882:2020 memory model), each with title, authors, venue or version, and URL or DOI. Every ADR and workload note cites entries from this file by key; a lint test fails on an ADR citing a key that is not in the file.

### 0.2 Architecture decision records
Files: `docs/decisions/ADR-001.md` … `ADR-007.md` (decided), `ADR-008.md` … `ADR-011.md` (open, with the experiment that closes each).
Each follows the §10 template: problem, alternatives, invariants, hard counterexample, expressiveness cost, performance cost, complexity, experiments, conclusion. ADR-8 (checks on by default) and ADR-9 (generics) are closed in Phase 0 because the checker prototype needs both; ADR-10 (function pointers now, closures later) is closed in Phase 1; ADR-11 (mandatory freestanding facilities) is closed in Phase 3; ADR-012 (trap handlers are a documented trusted-boundary obligation with no runtime enforcement) is written in Phase 2 before the hosted runtime, recording the decision taken at planning time; its alternatives are runtime entry-point checks (rejected: cannot cover FFI or custom allocators, and would change observable language behavior beyond spec v0.2) and a statically enforced effect restriction on handler functions (deferred: needs the effect analysis of Phase 4).

### 0.3 Checker prototype
Files: `compiler/source`, `compiler/lexer`, `compiler/parser`, `compiler/ast`, `compiler/resolve`, `compiler/check`, `compiler/diag`, `cmd/kern` (`check` only).
- Lexer and parser for the full Phase 1 subset grammar (so Phase 1 reuses them), producing the canonical AST with byte-offset spans.
- Resolver: modules, names, nominal types, `Copy` derivation (§5.1), `Send`/`Share`/`Interior` derivation (§5.4, §5.5), generic constraint checking with monomorphization deferred (constraints are checked, bodies are not instantiated).
- Checker, in this order per function body: definite initialization (§5.1, including `out` per-field tracking and the caller-side rule of §5.2); static moved-ness with `OWN_CONDITIONAL_MOVE` and the `drop(x)` machine-applicable fix; no partial moves; access classification (`view`/`edit`/`consume`/`init`), derived accesses, in-statement access liveness following the §4 evaluation order, and the path-overlap conflict rule with the constant-index and constant-slice exceptions (§5.4); lexical access scopes for `with`/`match`/`for`, including owned-subject forms (§5.3); elision, permission, release-escape, and multi-access rules (§5.3); second-class escape checks (`ACC_ESCAPE_STORE`, `ACC_ESCAPE_RETURN`, `ACC_UNBOUND_PROJECTION`); scoped-spawn root-scope rule (`ACC_LIVE_ACROSS_SPAWN`) and `SEND_NOT_SEND`/`SHARE_NOT_SHARE` on `thread.spawn`/`Scope.spawn` signatures (library declarations are stubs with the §5.5 signatures); `UNSAFE_OP_OUTSIDE_UNSAFE` and `UNSAFE_MISSING_CONTRACT` (§6).
- `kern check` emits the §7 JSON response (`schema_version` `0.2`, `ok`, `result`, `diagnostics` with `code`, `severity`, `rule`, `file`, `span`, `related`, `fix`, `op_index`).

### 0.4 Fixtures
Files: `tests/negative/*.kern`, `tests/positive/*.kern`, `tests/adversarial/*.kern` plus `tests/adversarial/NOTES.md`.
- One reject fixture per code in the §11 Phase 0 list, with the four `ACC_CONFLICT` variants (edit/edit, edit/view, consume/view, enclosing scope), plus `ACC_PERMISSION_ESCALATION`, `ACC_RELEASE_ESCAPE`, `OWN_DESTRUCTURE_DESTRUCTOR`, `INIT_FIELD_OF_UNINITIALIZED`, `INIT_OUT_RESULT_UNHANDLED`, `INIT_CONDITIONAL`, `UNSAFE_MISSING_CONTRACT`, `ATOMIC_INVALID_ORDERING`, `FFI_UNSUPPORTED_TYPE`, `TYPE_TOO_MANY_VARIANTS`; each with an accepting twin. Reject fixtures carry no `phase:` line; every accepting twin carries the earliest phase in which it is executable (`phase: 1` for all Phase 0 twins except the FFI and concurrency twins, which are `phase: 2` and `phase: 3`). Nothing is executed in Phase 0; the harness only runs `check` until a fixture's phase is reached.
- One fixture per §5.8 adversarial case with a one-page note in `NOTES.md` classifying it (safe / unsafe-internals / rejected by design).

### 0.5 Required programs
Files: `examples/vector/vector.kern`, `examples/btree/btree.kern` (split and merge), `examples/arena_ast/ast.kern`, `examples/packet_parser/parser.kern`, `examples/thread_handoff/handoff.kern`.
Each is accepted by `kern check`. The B-tree uses `Pool.get2`; the arena AST uses `Arena` handles; the parser uses the elision rule on `view[]u8`; the handoff uses `thread.spawn` and `thread.scope()` against the stub declarations.

### 0.6 Workload classification and go/no-go report
Files: `docs/reports/phase-0.md`, `docs/workloads/<name>.md` for each of the thirteen §8 workloads.
Each note classifies the workload as expressible in the core, requiring the §5.6 reserved extension, or requiring unsafe internals, with the sketch that justifies it. The report states the extension count and applies the §5.6 and §11 rules: the extension is scheduled for Phase 1 iff the count is at least one; Phase 0 fails if the count exceeds two.

**Key decisions.** ADR-8: all checks on by default (decided). ADR-9: monomorphized generics with nominal interfaces, no `dyn` (decided). Fixture format and rule numbering as in §0 above. Library types (`Arena`, `Pool`, `Mutex`, `thread`) are declaration-only stubs in `std/` so the checker can type their signatures.

**Risks.** The in-statement access-liveness rule (§5.4) is the most intricate part of the checker and the most likely source of false rejections; mitigated by the accepting twins and the five required programs. A workload count above two for the reserved extension is a no-go and reopens ADR-1 (§11), which is the intended outcome of this phase, not a failure of the plan.

**Acceptance criteria.**
1. `kern check` accepts all five programs in 0.5 and every accepting twin.
2. Every reject fixture produces exactly one diagnostic whose code and rule match its header.
3. The registered-code coverage test passes at levels 1 and 2 for every code and at level 3 for every code introduced in Phase 0 (phase-filtering rule in §0).
4. All thirteen workload notes exist and the report records the extension count with at most two.
5. ADR-1 through ADR-9 are written in the §10 template; ADR-10 and ADR-11 are drafted with their closing experiments named.
6. `spec/grammar.ebnf` parses every fixture and example without ambiguity (a parser test enumerates them).
7. Language-reference acceptance (§4.1 last paragraph): `spec/types.md` and `docs/ownership.md` pass a speccritic run with no CRITICAL findings and every WARN accepted in writing, and the project owner records sign-off of the three reference files by commit hash in `docs/reports/phase-0.md`; Phase 1 work does not start before that entry exists.

---

## Phase 1 — Semantic prototype (interpreter, no native backend)

**Goal.** Execute checked programs on the semantic IR with the §4.1 execution semantics and §6 trap semantics, and ship the first machine-API commands (§11 Phase 1).

**Depends on:** Phase 0 go decision.

### 1.1 Semantic IR and validator
Files: `compiler/ir/{ir.go,print.go,parse.go,validate.go}`, `compiler/lower/`.
- IR instructions per §7: `move`, `drop`, `access_begin`/`access_end` (kind, root, path), `check_bounds`, `check_generation`, `check_disjoint`, `check_overflow`, `unsafe_begin`/`unsafe_end`, calls, loads/stores on places, control flow, with a provenance tag on every `check_*` (`runtime-checked` or `proven_by: loop_bound | dominated_by_check | constant`).
- Validator implementing the five §7 invariants, run after lowering and after every later pass; failure is an internal error (exit 3) with the offending instruction.
- Generics strategy (answers the Phase 1 execution question): the semantic IR is monomorphic. A specialization pass in `compiler/lower/mono.go` instantiates every generic function and type per concrete argument set before validation, so the interpreter never sees type parameters, and layout, destructors, and `Copy`/`Send`/`Share` derivation are computed on concrete types only. Phase 2 reuses this pass unchanged.
- Textual form for `emit-ir`, round-trippable through `ir/parse.go` so the interpreter can be tested on hand-written IR.

### 1.2 Interpreter and traps
Files: `compiler/interp/`, `runtime/trap/` (Go side for the interpreter).
- Executes IR with §4.1 semantics: trapping, wrapping, saturating and checked integer forms with the exact §4.1 operation list; `truncate[T]`; float conversions; bounds; §4.1 layout for `show-layout` (computed, not yet used for memory); evaluation order of §4; destruction order of §5.7; `out` partial cleanup on error (§5.2); accessor `release` at access end (§5.3).
- Trap handling per §6: `TrapInfo` with the full kind list, `trap.set_handler` with atomic exchange semantics, default handler writing to standard error, process termination with no destructors. Interpreter stack exhaustion: the interpreter accounts frame sizes from §4.1 layout against a configurable stack limit (default 8 MiB, `--stack-bytes`) and traps with kind `stack` when a call would exceed it, so `stack` fixtures are differential with native builds.
- Library intrinsics implemented in Go for Phase 1: `Box`, `Vec`, `Arena`, `Pool` (with generation, vacancy and identity checks, identity tag counter and tag-0 rule, `get2` validation order, and generation-wrap retirement, tested in Phase 1 through the `kern_test_pool_seed_generation` interface of 2.3 implemented as a semantic hook over the Go intrinsic's slot table, same declaration and `//! test-hooks: true` gating; the representation-offset form arrives with the Phase 2 rewrite), `Option`, `Result`, `SystemAllocator` with the §6 `Allocator` contract, `Mutex`/`Shared`/`Atomic`/`thread` as single-threaded stand-ins that enforce the §5.5 rules dynamically (real threads arrive in Phase 3). Construction and insertion signatures for `Arena`/`Pool` are fixed here, closing spec §13 accepted gap 1: `new(own A) -> Self`; `alloc(edit self, own T) -> Result[Id, AllocErr]`; `alloc_with(edit self, init: func(out T) -> Result[(), E]) -> Result[Id, AllocOrInit[E]]` with `union AllocOrInit[E] { Alloc(AllocErr); Init(E) }`, so allocation failure (slot cannot be obtained; nothing was initialized) and initializer failure (slot obtained, `out` storage cleaned up per §5.2, slot returned to the free list) are distinguishable. Fixtures cover both failure points: OOM before initialization, and initializer error after partial initialization, verifying that no destructor runs for the partial value and the slot is vacant afterwards.

### 1.3 Formatter and machine API
Files: `spec/format.md`, `compiler/format/`, `cmd/kern` (`format`, `emit-ir`, `explain-ownership`, `run`), `tools/agent-api/schema.md`.
- `format` per `spec/format.md`, with the two required properties tested on every fixture: idempotence and diagnostic-code preservation (§16).
- `explain-ownership` per the §16 schema, including `init` accesses, `drop_at`, contract blocks, and `label`.
- `emit-ir` returns the textual IR.
- `tools/agent-api/schema.md` is generated from Go types in `compiler/diag/schema.go` so it matches §16 for `schema_version` `0.2` (a test diffs it against the committed copy).

### 1.4 Fixtures and differential test harness
Files: `tests/trap/*.kern`, expected-output `.out` files for every accept fixture tagged `phase: 1`, `tests/run_test.go`.
- Trap fixtures per §11 Phase 1 exit: `bounds`, `overflow` (including minimum signed value by `-1`, left-shift-out, and `Shared` reference-count overflow, set up with the test hook `kern_test_shared_seed_count(shared: RawPtr, count: usize)` seeding the maximum count so that one `clone` traps; same gating as 2.3), `div_zero`, `shift`, `conversion`, `generation`, `vacant`, `identity`, `disjoint`, `oom`, plus `stack` (unbounded recursion), `explicit` (the `trap()` built-in and allocator obligation violations), and `unreachable` (in Phase 1 a hand-written IR fixture under `tests/ir/` whose `match` is entered with an out-of-range discriminant constant; the source-level fixture that corrupts a discriminant through a raw pointer is tagged `phase: 2`), each with a normally completing twin.
- IEEE edge-case fixtures: NaN propagation through arithmetic and comparisons, round-to-nearest-even on `f32` arithmetic, float-to-integer `conversion` traps and `saturate` results for NaN and out-of-range values, and a reassociation-sensitive sum whose expected output is the strict left-to-right result; these run natively from Phase 2 under the backend's strict floating-point mode.
- The harness compares interpreter output with `.out` for every accepted fixture whose phase is 1 and keeps rejected fixtures `check`-only (§7 Testing).

### 1.5 Reserved extension (conditional)
Files: `compiler/check/viewstruct.go`, fixtures `tests/positive/view_struct_*.kern`.
Implemented only if the Phase 0 report's count is at least one (§5.6): `view struct` types that are second-class, producible by elision, bindable only by `with`/`match`/`for`, with `ACC_ESCAPE_STORE`/`ACC_ESCAPE_RETURN` extended to them.

**Key decisions.** ADR-10 closed: function pointers are first-class `Copy`; closures are not in Phase 1. The interpreter operates on IR, never on the AST (§7). Library types are Go intrinsics in Phase 1 and are rewritten in kern with unsafe internals in Phases 2–3; their signatures are frozen now.

**Risks.** Interpreter-level library stand-ins may hide layout bugs that only appear natively; mitigated by `show-layout` being computed from §4.1 now and asserted against native `sizeof` in Phase 2. The `release` mechanism interacting with early exits (`return`, `?`, `break`, `continue`) needs explicit fixtures.

**Acceptance criteria.**
1. All Phase 0 fixtures and programs still pass.
2. Every trap fixture terminates with the expected `TrapInfo` kind and its twin completes; every `phase: 1` accept fixture matches its `.out`.
3. The IR validator passes on every accepted fixture and rejects a set of deliberately corrupted IR files under `tests/ir/` (one per invariant).
4. `format` is idempotent and code-preserving on every fixture and example.
5. `explain-ownership`, `emit-ir`, `format`, `check` conform to §16 (schema test), and `tools/agent-api/schema.md` matches.
6. The reserved-extension decision is recorded in `docs/reports/phase-1.md` with the fixtures that exercise it, or the reason it was not needed.

---

## Phase 2 — Native MVP (LLVM backend, hosted Linux/macOS)

**Goal.** Produce native x86-64 and AArch64 objects with deterministic destruction, the explicit allocator, generics, the C ABI subset, and benchmark the container workloads (§11 Phase 2).

**Depends on:** Phase 1.

### 2.1 Backend
Files: `compiler/backend/llvm/{emit.go,types.go,abi.go}`, `cmd/kern` (`build`, `emit-asm`).
- Emits textual LLVM IR from the semantic IR and invokes `clang` (pinned major version recorded in `bench/toolchain.lock`) to produce objects and executables; `--target` accepts an LLVM triple (default host), surfaced as the machine-API `target` field.
- Reuses the Phase 1 specialization pass (1.1) unchanged; the validator runs after it as in Phase 1.
- Aliasing attributes per §5.4 implementation guidance: `edit` → `noalias`, non-`Interior` `view` → `noalias readonly`, `Interior` `view` → none. The `[unproven]` codegen comparison is an explicit benchmark item (2.5).
- Layout per §4.1 (struct order, union discriminant sizing and numbering, `[N]T`, sequence access pairs), verified by a test that compiles `show-layout` output against `offsetof`/`sizeof` from a generated C file.
- Static `drop` points, `out` partial cleanup, and `release` calls lowered directly; no landing pads (§6).
- Floating point per §4.1: no floating-point relaxation flags (`nnan`, `ninf`, `nsz`, `arcp`, `contract`, `reassoc`, or their combined form) on any floating-point instruction, `clang` invoked with `-ffp-model=strict`, and the IEEE edge-case fixtures of 1.4 are part of the differential suite from this phase.

### 2.2 Hosted runtime
Files: `runtime/hosted/{start.c,trap.c,alloc.c,stack.c}`, `runtime/include/kern_rt.h`.
- Process startup, `SystemAllocator` with the §6 contract (alignment and size obligations trap with kind `explicit`; zero-size allocations return unique pointers), trap termination via `abort` after the handler, `trap.set_handler` as a `seq_cst` exchange, stack-overflow detection with a guard page and a `SIGSEGV` handler installed on an alternate signal stack (`sigaltstack`, 64 KiB per thread). **Signal-safety contract:** each thread records its guard-page range in preallocated per-thread state at thread start; the handler classifies the fault as `stack` iff the faulting address lies in that range (any other `SIGSEGV` is trap kind `unreachable`, since safe code cannot fault); it builds the `TrapInfo` in a preallocated per-thread buffer and loads the handler pointer with one atomic load; that is the whole of the runtime's signal machinery, and it never writes output. The selected handler then runs on the alternate stack with the remaining depth: the *default* handler is the sole writer of the record and uses `write(2)` only (async-signal-safe) on the preallocated buffer; a *custom* handler is ordinary kern code under a **documented trusted-boundary obligation, not a runtime-enforced restriction** (ADR-012, written in Phase 2 before the hosted runtime): a trap handler may run in signal context on an interrupted thread, so it must not allocate, take a lock, spawn, perform buffered I/O, or call FFI or a custom allocator that does any of these; the only guaranteed-safe operations are `write(2)`-based unbuffered output, atomics, and computation on `TrapInfo` and pre-existing data. Violating the obligation may deadlock the process or trap a second time, in which case the nested-trap path below terminates it; memory safety is unaffected. The obligation is stated in `docs/ownership.md` beside `trap.set_handler`, applies uniformly to every trap kind, and the runtime performs no checks for it. The `std/testing.console_trap_handler` used by fixtures and the default handler both satisfy it by construction. A second fault or any trap during the handler restores the default `SIGABRT` disposition and calls `abort()` (not `_exit`), so every hosted trap path, nested or not, ends in death by `SIGABRT` exactly as the trap contract in §0 states; the interpreter's nested path exits 134 as its contract states. No lock, allocation, or `stdio` call occurs on the runtime's path. Subprocess tests cover: default and custom handlers on overflow; a conforming custom handler that writes with `write(2)` after a stack trap taken while the thread holds a `Mutex` and while buffered I/O is mid-write (asserting the handler's output appears and the process dies by `SIGABRT` within 5 s); a non-conforming handler that attempts `Mutex.lock` on the held mutex, run with a 5 s timeout and documented as demonstrating the obligation rather than enforcing it; a nested trap inside a custom handler (asserting `SIGABRT` and exactly one record when the default handler wrote it, zero when a custom one did not); overflow during an allocation; overflow while another thread is writing a record; and overflow concurrent with `trap.set_handler` replacement. Frames larger than one page are emitted with stack probes (`probe-stack` function attribute, as `clang -fstack-clash-protection` does) so every page of a new frame is touched in order and the guard page cannot be skipped; a fixture with a 1 MiB local array recursing once covers this case alongside the unbounded-recursion fixture.
- Debug build configuration (closing spec §13 accepted gap 5): `--opt debug` enables identity tags and keeps all checks; `--opt release` keeps all checks (ADR-8) but disables identity tags; the interpreter honors the same flag (see §0).

### 2.3 Unsafe core and library in kern
Files: `compiler/check/unsafe.go`, `compiler/backend/llvm/raw.go`, `std/raw.kern`, `std/{box,vec,arena,pool,slab,option,result,alloc}.kern`.
- The minimum unsafe feature set needed by containers lands here: `unsafe` blocks with `UNSAFE_OP_OUTSIDE_UNSAFE` enforcement, `RawPtr[T]` with load, store, offset arithmetic, cast to and from `usize`, `alloc`/`free` through the `Allocator` interface, and `unsafe assert Send/Share` (needed by `Box`/`Vec` of raw pointers). Contract blocks (§6) are mandatory on every safe wrapper from this phase. The interpreter gains the same raw-pointer operations over a byte-addressed heap so the fixtures remain differential.
- Rewrites the Phase 1 intrinsics in kern with these primitives, keeping the Phase 1 signatures. `Mutex`, `Shared`, `Atomic`, `thread` remain intrinsics until Phase 3. Generation-wrap retirement (§5.6) is carried over from the Phase 1 intrinsic into the kern `Pool` and tested through one test-only interface usable by both execution modes: the runtime symbol `kern_test_pool_seed_generation(pool: RawPtr, slot: usize, gen: u32)`, declared in `std/testing.kern` and provided only by the test runtime library `runtime/testhooks/`, which `kern build --test-hooks` links and which the interpreter implements as an intrinsic under the same flag. A fixture declares `//! test-hooks: true`; the harness passes the flag, and a fixture without the header that references the symbol fails to link (native) or to resolve (interpreter). `--opt release` refuses `--test-hooks` (exit 2, diagnostic `CLI_INVALID_FLAGS`), so release builds cannot link or invoke it. The fixture seeds the maximum generation, then exercises only the public API (`alloc`, `free`, `alloc` again) and asserts the slot was retired and never reused. The seeding writes the slot's generation field at the §4.1 layout offset, which is the same representation in both modes.
- Thread-local storage: `thread_local` module-level declarations lowered to LLVM TLS in hosted builds, with a fixture; freestanding TLS is decided by ADR-11 in Phase 3.

### 2.4 C ABI subset
Files: `compiler/resolve/ffi.go`, `compiler/backend/llvm/abi.go`, `tests/ffi/*.kern` with C counterparts.
`extern "C"` import and export with the §7 type restrictions (`FFI_UNSUPPORTED_TYPE` otherwise), unmangled symbols, imported calls unsafe, exported calls safe. FFI fixtures execute from this phase (§7 Testing) both natively and in the interpreter.
**Interpreter FFI bridge** (`compiler/interp/ffi/`): no cgo. The interpreter generates one C shim per imported function, `void kern_shim_<name>(void* args, void* ret)`, which unpacks the §4.1-laid-out argument buffer, calls the real function, and writes the result to `ret`; the shim and the fixture's C counterpart are compiled by `clang` into one shared library, loaded with `github.com/ebitengine/purego` (`Dlopen`/`Dlsym`), and every call crosses with exactly two pointer arguments, so struct-by-value and all scalar types are marshaled by the shim, not by the Go side. Pointer arguments are passed as raw addresses into the interpreter's byte-addressed heap, which is allocated with page-aligned OS memory so C may read and write it; ownership never transfers across the bridge (an FFI pointee stays owned by whichever side allocated it, per the fixture's contract block). **Reverse bridge (C calling exported kern functions):** for each exported function the interpreter generates a C wrapper with the real C signature that packs its arguments into a §4.1-laid-out buffer and calls `kern_interp_dispatch(fn_index, args, ret)`, a function pointer the interpreter installs at load time from a `purego.NewCallback`; the dispatcher runs the exported function's IR on the current interpreter and writes the result to `ret`. Nested and re-entrant calls (kern → C → kern) are supported to the interpreter's stack limit; callbacks live as long as the loaded library. A proof of concept of both directions on all four hosts is the first task of Phase 2, before any other FFI work. **Supported matrix:** the interpreter bridge requires host == target. CI therefore runs host-native runners for linux/amd64, linux/arm64, and darwin/arm64 (darwin/amd64 under Rosetta on the arm64 runner), and each runner executes the full FFI differential suite for its own architecture; the Phase 2 gate fails if any of these runner pairs did not run. Cross-compiled FFI fixtures are additionally executed natively under emulation, without the interpreter half, and are reported as native-only rows, not as coverage of the differential requirement. Contract tests cover scalar, struct-by-value, pointer, import, export, and re-entrant cases.

### 2.5 Machine API commands
Files: `cmd/kern`, `compiler/analysis/{layout,copies,allocs,bounds}.go`.
`show-layout`, `show-copies` (inline `bytes`, `heap` flag), `show-allocations`, `show-bounds-checks` (with `proven_by` from IR provenance), `emit-asm`; all per §16 with `target` where required and labels derived only from IR provenance tags (§7).

### 2.6 Benchmark harness and container workloads
Files: `bench/harness/` (Go), `bench/{c,rust,kern}/{vector,hash_table,btree,arena_ast,ring_buffer,wal,packet_parser}/`, `bench/toolchain.lock`, `bench/REPORT.md` template.
- Comparable C, Rust and kern implementations of the seven container-class §8 workloads implementable without threads (vector, hash table, B-tree split/merge, arena AST, ring buffer, database WAL, zero-copy packet parser); the remaining six are Phase 3.
- Benchmark configuration matrix: every workload is built and measured in `--opt release` (the primary row) and `--opt debug` (identity tags on) under `--env hosted`, and additionally under `--env freestanding` from Phase 3 for the workloads that have a freestanding variant (page-frame allocator, ring buffer, intrusive run queue); the report groups rows by workload, then build configuration, then language. Harness runs at least 5 repetitions per row, reports 95% confidence intervals, and records every §2.3 and §8 metric: throughput, p50/p99 per-operation time, peak RSS, copies and heap allocation counts (from `show-copies`/`show-allocations` and runtime counters), binary text-section bytes, compile time, unsafe source lines, ownership graph (from `explain-ownership`), and whether the reserved extension was used; user-level handle code and library unsafe internals are reported as separate rows (§2.3(a)/(b)).
- Includes the `[unproven]` codegen experiment: the same loop bodies with and without the aliasing attributes, and against the Rust `&mut`/`&` version.

**Key decisions.** Textual LLVM IR plus `clang`, no bindings (§7 non-normative note); a C-emitting backend is not built unless a Phase 3 target needs it. Pinned toolchain versions for all three languages in `bench/toolchain.lock`.

**Risks.** LLVM may not honor the no-alias attributes as expected on sequence accesses, undermining the 10% target; the 2.6 experiment measures this before Phase 3 depends on it. Deterministic destruction with no drop flags requires the checker's static-move guarantee to hold through monomorphization; the validator after the pass guards it.

**Acceptance criteria.**
1. Every `phase: 1` and `phase: 2` accept fixture produces identical output from the interpreter and the native build on both x86-64 and AArch64 (one of them may be cross-compiled and run under emulation in CI); every trap fixture traps natively with the expected kind.
2. FFI fixtures pass; `FFI_UNSUPPORTED_TYPE` fixtures reject.
3. `show-layout` matches C `offsetof`/`sizeof` for every type in the fixtures; the TLS fixture and the generation-retirement fixture pass natively and in the interpreter.
4. The benchmark report exists for the seven workloads with all metrics, CIs, and separate user-level and library rows; the report states, per workload, whether the 10% throughput and RSS targets are met, and the codegen experiment's result (no threshold is imposed on the targets in this phase; they are §2.3 targets, not promises).
5. Machine API commands of 2.5 conform to §16.
6. ADR-012 (trap-handler obligation) is recorded and `docs/ownership.md` states the obligation beside `trap.set_handler`.

---

## Phase 3 — Systems features (freestanding, unsafe, threads)

**Goal.** Freestanding output, the unsafe feature set, real concurrency primitives, and the remaining §8 workloads; audit every unsafe boundary (§11 Phase 3).

**Depends on:** Phase 2.

### 3.1 Freestanding mode
Files: `runtime/freestanding/{start.S,trap.c}`, `compiler/backend/llvm/stackcheck.go`, `tests/freestanding/`.
- `kern build --env freestanding`: no OS assumptions, no default allocator (§6), halting trap with a user-registered handler, compiler-inserted stack-limit check in every prologue with trap kind `stack` and the unsafe build option that disables it (§6).
- Freestanding fixtures perform output through a console device at a fixed MMIO address (the QEMU `virt` PL011 UART at `0x0900_0000` on AArch64, machine `virt`, `-cpu cortex-a72`; the 16550 at I/O port `0x3F8` on x86-64, machine `q35`; both wired with `-serial stdio -display none -no-reboot`); `kern run --env freestanding` emulates the same device by mapping that address to standard output, uses no `SystemAllocator`, and halts on trap, so freestanding fixtures are differential between interpreter and QEMU.
- **Guest/harness completion protocol** (`tests/freestanding/README.md` records the exact invocations): the freestanding runtime's `exit(code)` and its halt-on-trap path both write a final console line `{"halt": {"code": n}}` and then power off the machine (PSCI `SYSTEM_OFF` via `hvc` on AArch64; the `isa-debug-exit` device on x86-64, which makes QEMU exit with `(code << 1) | 1`). The harness derives the outcome from the halt line (normal completion with `code`, or a trap when a trap record precedes it) and requires QEMU to exit within the budget: 60 s to the first console byte, 300 s total; a timeout kills QEMU and fails the fixture. The interpreter's freestanding emulation writes the same halt line and exits with `code`.
- ADR-11 closed here: which runtime facilities are mandatory in freestanding mode (minimum: trap entry, stack limit symbol, `TrapInfo` construction).
- Freestanding fixtures run under QEMU for x86-64 and AArch64 bare-metal targets.

### 3.2 Systems-only unsafe operations and SIMD
Files: `compiler/backend/llvm/{volatile,asm,packed,simd}.go`, `std/{mmio,simd}.kern`.
- The remaining enumerated unsafe operations (§6): volatile MMIO access, `packed`/unaligned access, inline assembly, `unsafe assert Interior` and writes through `Interior` views in the declaring module; conventions and moves still checked inside `unsafe`.
- SIMD: fixed-width vector types `[N]T` with `simd` intrinsics (`add`, `sub`, `mul`, `and`, `or`, `xor`, `shuffle`, `reduce_add`) lowered to LLVM vector instructions; a fixture per intrinsic and a benchmark row in the packet-parser workload comparing scalar and SIMD checksums.

### 3.3 Concurrency primitives
Files: `std/{thread,mutex,shared,atomic}.kern`, `runtime/hosted/thread.c`, `tests/concurrency/`.
- Interpreter concurrency: `thread.spawn` and `Scope.spawn` backed by OS threads with the interpreter's heap made thread-safe for `Share` data, so concurrency fixtures run differentially from this phase.
- `thread.spawn`, `Thread.join`/detach semantics, `thread.scope()` with join-all destructor, `Scope.spawn` returning `Join[R]` with the §5.5 result-ownership rules, `SpawnErr` paths (owned arguments destroyed, accesses released).
- `Mutex[T].lock` as runtime-exclusive escalation with `release unlock` (§5.3); recursive acquisition traps with kind `explicit` (spec §13 accepted gap 4).
- `Shared[T]` with `usize` count and `overflow` trap; `Atomic[T]` with the exact §5.5 operation set, ordering restrictions checked at compile time (`ATOMIC_INVALID_ORDERING`), `compare_exchange` strong and weak, `fence`; lowered to LLVM atomics with the C++20 orderings.
- Concurrency fixtures run under ThreadSanitizer in CI (§9 sanitizers).

### 3.4 Remaining workloads and unsafe audit
Files: `bench/{c,rust,kern}/{buffer_pool,page_frame_allocator,intrusive_run_queue,thread_pool,lock_protected_map,lock_free_queue}/`, `docs/unsafe-audit.md`.
- The six remaining §8 workloads, including the audited unsafe lock-free queue and the buffer pool's `pin`/`unpin` release accessor; the harness from 2.6 extended with concurrency and failure-path runs (§8).
- Unsafe audit: every `unsafe` block in `std/` and `bench/kern/` has a contract block, and the audit document lists each with its invariant and the fixture that exercises its failure path.

**Key decisions.** Hosted threads use the platform `pthread`; the freestanding mode has no threads (a non-goal until a target needs them). TSan and ASan are mandatory CI jobs for `std/` and the workloads.

**Risks.** The scoped-spawn rule (§5.5) is the one cross-thread borrow; a bug there is a data race, so these fixtures run under TSan with deliberate-race twins that TSan must catch. Lock-free queue correctness depends on the C++20 memory model mapping; verified with a litmus-test suite under `tests/concurrency/litmus/`.

**Acceptance criteria.**
1. Freestanding fixtures boot and trap correctly under QEMU on both targets and produce identical output in the interpreter's freestanding emulation; `stack` traps fire in freestanding builds.
2. Concurrency fixtures pass natively and under TSan; the deliberate-race twins are detected.
3. All thirteen §8 workloads have C, Rust and kern versions with a full benchmark report per §8 and §2.3.
4. `docs/unsafe-audit.md` covers every `unsafe` block, and `UNSAFE_MISSING_CONTRACT` fires on a fixture.
6. Every SIMD intrinsic has a passing fixture, and the packet-parser benchmark reports the scalar and SIMD rows.
5. ADR-11 is closed and recorded.

---

## Phase 4 — AI and compiler integration

**Goal.** Complete the machine API, structured edits, incremental checking, and run the agent experiment that is the primary success metric (§2.3, §9, §11 Phase 4).

**Depends on:** Phase 3 (all commands need the full language).

### 4.1 Remaining machine API
Files: `cmd/kern`, `compiler/analysis/effects.go`, `compiler/edit/`.
- `show-effects` with transitive effects over statically resolved callees and the `unknown`/`assumed-unsafe` rules for function pointers and FFI (§16).
- `apply-structured-edit` with the full §16 application semantics: rename resolution and expansion, conflict rules, insert placement, invalid-span rules, all-or-nothing failure, lowest-failing-op-index precedence with `EDIT_INVALID_SPAN` before `EDIT_OVERLAP` (spec §13 accepted gap 6), `formatted` flag, and diagnostics of the subsequent `check`; machine-applicable `fix` entries on diagnostics use this format.
- Incremental checking: per-module cache entries keyed by the module's source hash, the hashes of every imported module's *interface file* (a versioned, canonical serialization `compiler/resolve/iface.go` of everything checking or analysis consumes from an import: exported type definitions with layout, constants, function signatures with conventions and contract blocks, derived `Copy`/`Send`/`Share`/`Interior` markers, generic function and type bodies in full since they are specialized at the use site, and each exported function's transitive effect summary; its format carries its own version in the key), the target triple, the build configuration (both dimensions of §0: `--opt` and `--env`), the compiler version, and the `schema_version`; any key change invalidates the entry and its dependents. Incremental and full `check` must produce identical diagnostics, tested by randomized edit sequences, by targeted invalidation tests that change in an imported module one generic body, one constant, one marker-affecting field, one layout-affecting field, one contract block, and one transitive effect and assert re-checking of the dependents, and by alternating targets and configurations without editing source.

### 4.2 Agent benchmark
Files: `bench/agent/{tasks/,harness/,REPORT.md}`.
- Ten tasks drawn from the §8 workloads (implementation, debugging, refactoring, optimization, unsafe-contract review per §9), each with a hidden test suite, closing spec §13 accepted gap 7 by defining the suite and the aggregation rule: a trial succeeds only if the hidden test suite passes within the iteration budget (20) and time budget (30 minutes); a failed or timed-out trial scores the budget maximum for both iterations and wall-clock. At least two distinct strong models are tested. A task is a win when, for every model tested, over at least 5 trials per arm, the structured-diagnostics arm is strictly better than the plain-text arm in the first of these that differs: the number of successful trials, then the median iteration count (a failed or timed-out trial scoring the budget maximum); a task where both are equal for any model is a loss, and a task where the plain arm is better at the first differing criterion for any model is a loss. Wall-clock time is recorded and reported per cell, not compared. The primary metric passes at 8 wins of 10 (§2.3). *(Revised 2026-10-28; the rule as first written required a strictly lower median iteration count and a strictly lower median wall-clock time with successes as a floor only, `docs/reports/agent-metric-revision.md`.)*
- Recorded per trial (§9): hidden-test correctness, iterations, token use, wall-clock time, regressions (hidden tests that passed in an earlier iteration and fail in a later one), and unsafe code introduced (source lines inside `unsafe` blocks added by the agent, from `show-effects` and a diff of `unsafe` spans), with per-task medians in the report.
- Ablation arms: structured JSON diagnostics plus semantic queries, versus plain-text diagnostics only; all models receive the same language reference and examples.
- Secondary, descriptive-only Go and Rust comparisons with the training-data caveat stated in the report (§9).

### 4.3 Research report
Files: `docs/research-report.md`.
The §14 research report: the feasibility result of Phase 0, the measured performance of Phases 2–3, the agent experiment of 4.2, and the comparison with each §14 reference direction, citing primary specifications and papers from `docs/references.md` for every claim about another language.

### 4.4 Reproducible performance reporting
Files: `bench/harness/report.go`, `bench/REPORT.md`.
Final §8 report generation with hardware details, pinned toolchains, profiles, and the separation of backend immaturity from intrinsic overhead and handle indirection from library unsafe code (§9); fuzzing of the parser and checker (`go test -fuzz`) and differential testing are standing CI jobs.

**Key decisions.** Agent trials are run through a harness that records every tool call so the token and iteration counts are auditable. Models and versions are pinned per run.

**Risks.** The within-language ablation may show no effect, which is a valid negative result for the hypothesis in §1 and must be reported as such. Incremental checking must never diverge from full checking; the randomized test is the guard.

**Acceptance criteria.**
1. All eleven §7 commands conform to §16; structured-edit fixtures cover every `EDIT_*` code and the precedence rule.
2. Incremental and full `check` agree on a randomized edit corpus of at least 1,000 edits and across every combination of two targets and the four `--opt`/`--env` configurations without source edits.
3. The agent report states the win count out of ten with the per-task medians, trial counts, and models; the secondary comparison carries the caveat.
4. The performance report covers all thirteen workloads with the §9 separations.
5. `docs/research-report.md` exists and every cross-language claim in it cites a `docs/references.md` entry (lint test).

---

## Phase 5 — Hardening

**Goal.** Stabilize the specification and library, add the debugger and packaging, complete a security review, expand targets, and decide on self-hosting (§11 Phase 5).

**Depends on:** Phase 4.

Files: `spec/*` (v1.0 candidates), `std/`, `tools/debug/`, `docs/security-review.md`, `docs/decisions/ADR-013-self-hosting.md`.
- Specification stabilization: every `[unproven]` marker resolved with the experiment that resolved it; `memory-model.md` written from the §5.5 rules; rule numbers frozen.
- Standard library completion against the thirteen workloads' needs, with the unsafe audit kept current.
- Debugger: DWARF emission from the backend and a `kern debug` wrapper over `lldb`, with ownership state visible from `explain-ownership` at breakpoints.
- Packaging: reproducible release builds for Linux and macOS on both architectures.
- Security review of the unsafe boundaries, the runtime, and the machine API input handling (untrusted JSON, path handling).
- Target expansion as justified by the freestanding users; the C-emitting fallback backend only if a target requires it.
- Self-hosting decision recorded as ADR-013 with the measured compiler-in-kern feasibility.

**Acceptance criteria.** Spec v1.0 candidate with no `[unproven]` markers; release artifacts built reproducibly; security review findings closed or accepted in writing; ADR-013 recorded.

---

## Phase 6 — Kern-level inlining (IR passes)

**Goal.** Let the lowering's proof facts reach across calls: inline small leaf functions at the IR level, in a pass that runs before the proof pass, so that the checks inside helpers such as `rd16` are proven by kern (and reported by `show-bounds-checks`) rather than folded only by LLVM after its own inlining. Measured motivation: `docs/reports/bounds-check-elimination.md` (LLVM folds dominated checks only once it has inlined; the proof pass stops at a call) and its follow-ups.

**Depends on:** Phase 5. **Entry checklist** (all on `main` before 6.1 starts, and recorded as the first section of `docs/reports/inliner.md` with the exact commit hash and the baseline report's thirteen checksum lines): commit `b1fef21` (atomics as instructions, the current `bench/REPORT.md` baseline format) and the bounds-check commit that follows it (its hash is recorded at entry; the plan is written before it exists), which supplies `compiler/lower/prove.go` (the `dominated_by_check` and `constant` proof facts, with `compiler/lower/prove_test.go` as its contract: a check is marked proven only when its index is covered by a dominating check or branch on the same length, or is a literal into an array of known length), the shared trap block in `compiler/backend/llvm/emit.go`, the backend's skipping of proven checks, and `docs/reports/bounds-check-elimination.md` with the baseline `bench/REPORT.md` it regenerated. `compiler/backend/native` is the driver that runs the pipeline and clang; `compiler/backend/llvm` is the text emitter it calls; Phase 6 changes only the driver's pipeline call.

Files: `compiler/opt/inline.go`, `compiler/opt/prove.go` (moved from `compiler/lower/prove.go`), `compiler/opt/inline_test.go`, `compiler/opt/prove_test.go`, `compiler/backend/native/build.go`, `cmd/kern/{run,commands,explain}.go`, `tests/trap/inlined_*.kern`, `tests/run/inlined_*.kern`, `docs/reports/inliner.md`, `docs/decisions/ADR-008-checks-on-by-default.md` (implementation note).

### 6.1 The pass and where it runs
- A new package `compiler/opt` holds IR→IR passes: `opt.Inline(mod, opt.Options)` then `opt.Prove(mod)`. `lower.Lower` no longer calls `prove` itself. The pipeline of every command that consumes IR is exactly `lower.Lower → ir.Validate → opt.Inline → ir.Validate → opt.Prove → ir.Validate`, implemented once as `opt.Run(mod, opts) error` (which runs the validator between and after its passes) and called by `native.Build`, `kern run` (the interpreter executes the same IR, so the differential harness compares identical programs) and every machine command that reports on the compiled program (`emit-ir`, `show-copies`, `show-allocations`, `show-bounds-checks`, `show-effects`), so that what the API reports is what the build compiles; `explain-ownership` keeps the lowered IR as written, since it matches source symbols to IR locals by name and explains the program the author wrote. A validation failure at any point is an internal error (`ok=false`, exit 3, one `INTERNAL` diagnostic naming the pass, per the Phase 0 error-reporting contract) and no later pass runs; an injected-failure test (a pass that deliberately breaks an invariant) proves the stop.
- `Options.Inline` is true for `--opt release` and false for `--opt debug` (the existing Phase 0 configuration dimension; `--opt` already reaches every command and is part of every cache key, so no new switch is introduced). A debug build keeps every call so the Phase 5 debugger steps into helpers and `explain-ownership` at a breakpoint sees the callee's frame.

### 6.2 What is inlined
A direct call (`ir.Call` with `Fn == nil`, `Intrinsic == ""`) to a function `g` is inlined into `f` when all of the following hold; otherwise the call stays. Each condition names what breaks without it.
- `g` is a user function of the main module (`!Library`, `Extern == ""`, `!Intrinsic`): library bodies keep their "checks reported at the user-level call" contract and their trap-span parameters; inlining them is a later extension (6.6).
- `g.File == f.File`: positions are byte offsets within a function's file, and every analysis builds a location from `f.File`; a copied instruction keeps its own `Pos`, which is only right in the same file.
- `g` has no `out` parameter (`Conv == ConvOut`): the callee writes through the parameter slot, which has no value-level stand-in.
- `g` returns no access (`Rets` contain no `*resolve.Access`) and has `Release == ""`: a returned projection is tied to the caller's `AccessEnd` and release call.
- `g` has no loop (no back edge in its CFG) and a size of at most 48, where size is the number of `ir.Instr` values over all of `g`'s blocks plus one per block for its terminator (so that a chain of empty functions each calling its predecessor twice cannot double a caller's control flow at every level), measured after `g`'s own call sites have been processed. Thresholds are constants in `inline.go`; the report records the two values that make `rd16` and `rd32` inline and leave `sum16` and `parse` alone, which LLVM inlines anyway. *(Amended 2026-10-28, `docs/reports/inliner-loops.md`: a callee with a loop is judged like any other, and the size limit is 64, so that the hash table's `probe` (61, a loop, two sites) is copied; `sum16` at 77 still stays a call.)*
- The net growth of `f`, the difference between its size (instructions plus blocks) after and before the rewrite of a site (copied instructions plus the inserted parameter stores, inits, result stores, loads and moves, minus the removed call and the dropped parameter access begins and ends), summed over every site inlined into `f`, stays at or below 512; a site whose rewrite would exceed it is not rewritten and stays a call, as do all later sites in `f`.
- **Order, so that the result is a function of the module alone.** Functions are processed in the order of `mod.Funcs` sorted by name; the call graph's strongly connected components are computed first (Tarjan over direct calls), and a function in a component of more than one member, or that calls itself, is never inlined anywhere and inlines nothing from its component; the remaining functions are visited callees-before-callers (a topological order of the component DAG, ties broken by name), each function's call sites in block order then instruction order, and a site's eligibility is judged on the callee's size after the callee's own inlining. Two runs on the same module produce the same IR text.

### 6.3 How a call site is rewritten
Given `call %d1.. = g(%a1..)` in block `B` of `f`:
- **Split.** The instructions after the call move to a new block `C` (the continuation); `B` ends with `br g.entry'`.
- **Copy.** Every block of `g` is copied with fresh block IDs; every value gets a fresh ID (`f.NumValues`), every local of `g` a fresh local appended to `f.Locals` (same `Type`, `Conv`, `Temp`, `Name` suffixed with the call's position so `explain-ownership` and the IR printer stay unambiguous). Instructions are copied field by field through the value/local/block maps; `PathElem.Index/Lo/Hi` are remapped; `Const.Bytes` is shared; `Pos` fields are kept verbatim (the trap a copied check reports is byte-for-byte the one the call reported, which the trap fixtures assert).
- **Parameters.** A by-value or `own` parameter becomes `store %p' <- %a; init %p'` at the top of the copied entry block (the validator then sees an owned local, dropped or moved by the copied exits as the original was). A `view`/`edit` parameter is aliased: the `AccessBegin` on it in `g`'s entry block (the one the validator marks `param`) is dropped and its result value is mapped to the caller's argument access `%a`; the `AccessEnd`s of that value at `g`'s exits are dropped too (the caller ends `%a` after the call site, as it already does). Storing an access into a slot is rejected by the validator, which is why aliasing, not a store, is the mechanism. Root and path (spec §5.4) follow from the aliasing: every reborrow `access_begin ... *%p'[...]` in the copied body now names `%a`, so the validator resolves its root and path through the caller's open entry for `%a` (`openAccess.parent`), exactly as a reborrow of `%a` written in the caller would; liveness is unchanged because `%a` was open across the call already; disjointness and `Interior` writes were checked on the source by the checker before lowering and are not re-derived from IR.
- **Returns.** Each `ret v1..` becomes `store %r_i <- v_i; init %r_i` into fresh result locals followed by `br C`; `C` begins with, per destination, `load %d_i <- %r_i` for a `Copy` result or `move %d_i <- %r_i` otherwise, so the call's destination values keep their IDs and every later use is untouched. `unreachable` terminators are copied as they are.
- **Order.** Blocks are renumbered with `sortBlocks`; the copied entry block follows `B`. The callee function itself is kept in the module when it is still reachable from a root: a remaining direct call, a `func_ref` that spawns it or takes it as a value, the entry function, a destructor the module registers, or a function exported under the C ABI (Phase 2.4: any function with an exported symbol, which the pass never marks); a function left with no reference after the pass is marked `ir.Func.Inlined` and the backend does not emit it, the interpreter never runs it, and the analyses skip it.
- **Reporting policy** for the analyses that read IR (`show-bounds-checks`, `show-copies`, `show-allocations`): one entry per source location (file, span, kind), aggregated over every IR occurrence of that location that is not in an `Inlined` function: the inline copies in each caller, plus the retained callee body when the callee is still reachable. A bounds check is `proven` with the first occurrence's `proven_by` when every occurrence is proven, and `runtime-checked` otherwise; copies and allocations are reported once per location with no count, as before. The §16 schemas are unchanged; the policy is stated in `tools/agent-api/schema.md`, and `cmd/kern/cli_test.go` asserts that a check inside a helper inlined at two call sites is reported once, and that a helper with one remaining call keeps the weaker label when the retained body cannot prove it.

### 6.4 What the proof pass gains
- Run after inlining, `prove` sees the guard and the index in one function: in `sum16` the loop guard `i + 1 < n` now covers the two reads of the copied `rd16`, and in `parse` the header reads at constant offsets are covered once the pass learns the Else edge of a comparison against a constant: `!(len < 32)` establishes `31 < len`, so a literal index below 32 is `dominated_by_check`. Add that edge fact (constant on the left of `<`/`<=` on the Else edge, and the mirror forms) to `prove`.
- `show-bounds-checks` on `bench/kern/packet_parser/packet_parser.kern` then labels the ten header reads and both `rd16` reads in `sum16` as proven; the report records the before/after counts (before: 2 `constant`, 1 `dominated_by_check`, 1 `loop_bound`, 15 runtime-checked).
- Each new implication gets a positive and a negative case in `compiler/opt/prove_test.go`, table-driven over the comparison forms: `!(len < K)` proves index `K - 1` and not `K`; `!(K < len)` proves nothing about an index (it bounds `len` from above); `!(len <= K)` proves `K` and not `K + 1`; the Then edge of `K < len` proves `K` and not `K + 1`; a signed comparison proves nothing; and each has an executable twin under `tests/trap/` whose unproven access traps in both engines at the boundary value.

### 6.5 Tests and fixtures
- `compiler/opt/inline_test.go`: a table over every eligibility condition of 6.2 with one program per condition on each side of it: a library callee, a callee in another file, an `out` parameter, an access-returning callee and a release accessor, a callee with a loop, a callee of size exactly 48 (inlined) and 49 (a call), a caller whose next site would take its net growth from at most 512 to above it (that site and the ones after it stay calls), a self-recursive function, a mutually recursive pair, and an indirect call through a function value, each asserted to stay a call; and for the inlined cases, that the call is gone, the copied check keeps its `Pos`, the validator passes after each pass, by-value and `own` parameters are stored and dropped (an `own` parameter with a destructor is destroyed once), an access parameter is aliased and a nested reborrow of it validates against the caller's root, an `Interior` write through the aliased view keeps its `unsafe_begin`/`unsafe_end`, an early `return` from the callee reaches the continuation, and a callee with two return paths yields the right value on each.
- `tests/trap/inlined_bounds.kern` and `inlined_overflow.kern` (with accepting twins): the trap happens inside a helper that is inlined in release and not in debug; the fixture carries no `config:` line, so it runs in every valid configuration and both engines, and the trap record is the same in all of them. (A fixture restricted by `config:` or `test-hooks:` is compared only within the configurations it is valid in, as Phase 0 defines; identity-tag traps exist only in debug builds and are not expected to match release.)
- `tests/run/inlined_params.kern`: an `own` parameter with a destructor, a `Copy` parameter reassigned inside the helper, a `view` of a struct field, two return paths; the differential harness compares the engines.
- The existing fixtures and `bench/kern` checksums are unchanged; `emit-ir` on `packet_parser.kern` shows no call to `rd16` or `rd32` under `--opt release` and all of them under `--opt debug`.

### 6.6 Deferred
- Library callees (`Vec.get`, `Box.get`): would need the trap-span parameters of a library function resolved to the call site's span during the copy, and `show-bounds-checks` to keep reporting those checks at the user-level call. Not needed for the measured gap (LLVM inlines them).
- Cross-file inlining: needs a file on every position or on every instruction. Not needed by the benchmarks, every workload being one file.
- Inlining through `thread.spawn` function values: never (the thunk takes the function by name).

**Acceptance criteria.** `go test ./...` and `golangci-lint run ./...` clean; `ir.Validate` passes after each pass on every fixture and workload; every trap fixture passes the Phase 1 differential harness unchanged in each configuration the fixture is valid in, and for every fixture valid in both configurations the record the harness compares (kind and span, under its existing rules for the thread identifier and for stack traps, which Phase 6 does not touch) is the same in `--opt debug` and `--opt release`; the thirteen workload checksums are identical to the baseline report's; `show-bounds-checks` on the packet parser reports the twelve newly proven reads; the performance gate is measured with `go run ./bench/harness -experiment` on the same host and toolchain as the baseline (the `bench/REPORT.md` committed with the bounds-check commit; its host section names the hardware, clang, rustc and Go versions) at a one-minute load average below 4, and passes when the release throughput of every kern row is at or above the lower end of the baseline row's 95% interval, with packet_parser reported explicitly either way; any row below that bound is a regression named in `docs/reports/inliner.md`, which also records the thresholds, the before/after proof counts and the harness rows.

---

## Cross-phase traceability

| Spec area | Phase |
|---|---|
| §2.3 negative-safety fixtures (static) | 0 |
| §2.3 trap fixtures | 1 |
| §2.3 throughput/RSS targets, metric recording | 2 (containers), 3 (all), 4 (final report) |
| §2.3 primary agent metric | 4 |
| §4 evaluation order, §4.1 execution semantics | 1 (interpreter), 2 (native) |
| §4.1 layout | 2 |
| §5.1–5.4, §5.7 checker rules | 0 |
| §5.2 `out`, §5.3 release accessors, §5.7 destruction | 1 |
| §5.5 concurrency | 0 (signatures), 1 (single-thread stand-ins), 3 (real) |
| §5.6 arenas and handles | 1 (intrinsics), 2 (kern), §5.6 reserved extension 1 (conditional) |
| §5.8 adversarial cases | 0 |
| §6 traps, allocator, stack exhaustion, TLS, unsafe enumeration, contracts | 1 (interpreter), 2 (hosted, raw-pointer core, TLS), 3 (freestanding, MMIO/asm/packed/Interior) |
| §2.1 SIMD | 3 |
| §14 citations and research report | 0 (`docs/references.md`), 4 (`docs/research-report.md`) |
| §7 IR, validator, interpreter, testing rules | 1 |
| §7 IR passes after lowering (inliner, proof facts) | 6 |
| §7 C ABI subset | 2 |
| §7/§16 machine API | 0 (`check`), 1 (`format`, `emit-ir`, `explain-ownership`), 2 (layout/copies/allocations/bounds/asm), 4 (effects, structured edit) |
| §8 workloads | 2 (seven), 3 (six) |
| §9 methodology | 2–4 |
| §10 ADRs | 0 (1–9), 1 (10), 2 (12, trap-handler obligation), 3 (11), 5 (13, self-hosting) |
| §12 repository layout | 0 |
| §13 accepted gaps 1–7 | 1 (gap 1, 3), 2 (gap 5), 3 (gap 4), 4 (gaps 6, 7), 0 (gap 2) |
| §5.6 generation-wrap retirement | 2 |
