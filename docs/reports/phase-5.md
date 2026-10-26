# Phase 5 report — Hardening

**Date:** 2026-10-26. **Plan:** `specs/initial/PLAN.md` Phase 5. **Spec:** v0.2 → v1.0 candidate. **Builds on:** Phase 4 (commit `de47f20`).

## Deliverables

| Deliverable | Location | Status |
|---|---|---|
| Specification stabilization: every `[unproven]` marker resolved with its experiment, `spec/memory-model.md` from §5.5, rule numbers frozen | `specs/initial/AI_Native_Systems_Language_SPEC.md` (two markers resolved: the reserved extension by the Phase 0 count of 0, the aliasing claim by the Phase 2–4 experiment), `docs/ownership.md` 5.6.3, `spec/memory-model.md`, `spec/rules.lock` with `tests/rules_lock_test.go` | done |
| Standard library completion against the workloads, audit kept current | `yield_now()` (the spin-wait hint the thread pool and SPSC queue lacked), `tests/concurrency/yield_spin.kern`; `docs/unsafe-audit.md` unchanged (no new unsafe) | done for the thirteen workloads; strings, maps, file and process I/O and JSON remain absent (ADR-013) |
| Debugger: DWARF emission and `kern debug` over lldb with ownership state at breakpoints | `compiler/backend/llvm/debug.go` (compile unit, files, subprograms, locations), `kern build --debug`, `cmd/kern/debug.go` + `kern_lldb.py` (`kern-own`), `kern explain-ownership --line=N` | done |
| Packaging: reproducible release builds for Linux and macOS on both architectures | `scripts/release.sh`, `scripts/check-reproducible.sh` | done (all four targets byte-identical across two builds) |
| Security review of unsafe boundaries, runtime and machine API input handling | `docs/security-review.md`, `cmd/kern/fuzz_test.go` (`FuzzRequest`) | done |
| Target expansion as justified by freestanding users | none justified: no freestanding user yet; the C-emitting backend is not built | recorded |
| Self-hosting decision | `docs/decisions/ADR-013-self-hosting.md`, `examples/selfhost/lexer.kern` | decided: deferred |

## Acceptance criteria

1. Spec v1.0 candidate with no `[unproven]` markers — **met** (the status line defines the marker and states none remain; both resolutions name their experiment).
2. Release artifacts built reproducibly — **met** (`scripts/check-reproducible.sh`: darwin/arm64, darwin/amd64, linux/arm64, linux/amd64 identical hashes on rebuild; `scripts/release.sh` emits tarballs with `SHA256SUMS`).
3. Security review findings closed or accepted in writing — **met** (`docs/security-review.md`: every row closed or accepted with its rationale).
4. ADR-013 recorded — **met**.

## What was built

- **Memory model.** `spec/memory-model.md` states threads, happens-before, data races and why safe code cannot express one, the atomic mapping onto C++20, trap termination, and the evidence (litmus fixtures, TSan runs, the static rules).
- **Frozen rules.** `spec/rules.lock` lists every diagnostic code with its rule number; the test fails when a code changes rule or a new code is not added deliberately.
- **Debugging.** The backend emits `DICompileUnit`/`DIFile`/`DISubprogram`/`DILocation` metadata for every function and positioned instruction when building with `--debug` (`-g -O0` through clang); lldb resolves breakpoints to kern lines (`run.main at pool_handles.kern:5`). `kern debug file.kern` builds and launches lldb with a Python command module whose `kern-own` runs `kern explain-ownership --line=N` for the current frame's line and prints each place's state, drop point and accesses.
- **Packaging.** `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid="` per target with `SOURCE_DATE_EPOCH` from the commit; the tarballs carry the compiler and both runtime sources (clang is needed at use time for native builds).
- **Security.** The review's open items are the documented ones: the API is a local tool with the user's permissions (paths are opened as given, no size limits), unsafe code is as safe as its audit, releases are checksummed, not signed.
- **Self-hosting experiment.** The kern lexer in kern (107 lines against 274 in Go) tokenizes an embedded source and prints a checksum, identically in both engines; the compiler's later phases need strings, maps, I/O, JSON and closures that the library does not have, so ADR-013 keeps the compiler in Go through v1.0.

## Deviations from the plan

- **Standard library completion is scoped to the workloads' needs.** The thirteen workloads' only missing primitive was a scheduling hint; general-purpose strings, maps and I/O are recorded in ADR-013 as the prerequisite of self-hosting rather than added now.
- **DWARF carries line tables only.** Variables and types are not described (no `DILocalVariable`); `kern-own` provides the ownership view the plan asks for from the compiler's own analysis rather than from DWARF.
- **No target expansion.** Nothing justified one; the freestanding runtime supports the two bare-metal triples of Phase 3.

## Reviews

- **prism** (OpenAI gpt-6.1-sol, staged diff, docs/fixtures/benchmarks excluded): 8 medium findings, all addressed: `kern debug` forwards the build options it supports and rejects the freestanding environment; the saved lldb command file configures the Python module's environment itself and the module matches the frame's full source path; `--line` scans bytes without copies; DWARF metadata strings are escaped and line tables are precomputed per file; the self-hosting lexer handles escapes and unterminated strings; the release script requires GNU tar for deterministic archives.
- **realitycheck** (spec + plan, OpenAI gpt-6.1-sol, `--max-tokens 65536`): PARTIALLY_ALIGNED, score 100, no drift, no violations.
- **verifier** (offline): 98 low-severity unit gaps, unchanged from Phase 4; `FuzzRequest`, `TestRulesLock` and the debug-build check in the command tests were added this phase.
