# ADR-013: Self-hosting is deferred; the compiler stays in Go through v1.0

**Status:** Decided
**Date:** 2026-10-26
**Spec:** §11 Phase 5 (self-hosting decision), §12 (repository layout), §7 (machine API).
**Depends on:** ADR-001 (second-class access), ADR-009 (monomorphized generics).

## Problem

Spec §11 asks Phase 5 to decide whether the compiler should be rewritten in kern, with
the decision backed by a measured feasibility of a compiler written in kern.

## Experiment

`examples/selfhost/lexer.kern` is the kern lexer rewritten in kern: it tokenizes kern
source held in a byte array (string literals are `view[]u8`), classifying identifiers,
keywords, integer and float literals with suffixes, strings, comments and every operator
of `spec/grammar.ebnf`, and prints the token count and a checksum of the token kinds.
It is run by the fixture harness against the interpreter and the native build.

| | Go (`compiler/lexer/lexer.go`) | kern (`examples/selfhost/lexer.kern`) |
|---|---|---|
| lines | 274 | 107 |
| language features used | slices, maps, strings, closures over a lexer struct, error values | `view[]u8`, `[N]u8` tables, `Vec[Token]`, `edit self` methods, unions for token kinds |
| what is missing for the rest of the compiler | — | file I/O and command-line access (no `std/fs`, `std/io`); dynamic strings and hashing (no `String`, no `Map`); closures (ADR-010 reserves non-escaping closures for later); `any`-like heterogeneous trees (unions over `Pool` handles do this, at the cost of a handle per node); JSON for the machine API; LLVM text output (strings again) |

The lexer was expressible without friction (handles and unions cover the token stream and
the keyword table); every later phase of the compiler depends on the three missing
libraries (file and process I/O, strings and maps, JSON), none of which exists in `std/`
yet, and on closures for the resolver's and checker's callback-heavy walks, which ADR-010
left for a later phase.

## Alternatives

- **Rewrite now in kern.** Rejected: the standard library lacks strings, maps, file and
  process I/O and JSON, so the rewrite would first have to build them; the Go compiler is
  the reference implementation every fixture is validated against and the agent
  experiment depends on; and a second implementation doubles the surface the differential
  harness must keep identical.
- **Hybrid (kern front end, Go back end, or the reverse).** Rejected: two languages in one
  pipeline forfeits the maintenance benefit of self-hosting and keeps the I/O gap.
- **Stay in Go through v1.0, revisit after the library exists (chosen).**

## Decision

The compiler remains in Go through the v1.0 candidate. Self-hosting is revisited once
`std/` provides strings, maps, file and process I/O and JSON, and once ADR-010's
non-escaping closures exist; `examples/selfhost/lexer.kern` is kept as the measured
starting point and must continue to pass the differential harness.

## Consequences

- No `kern`-in-`kern` build target in Phase 5; packaging ships the Go binary.
- The standard-library items named above are the first work of a self-hosting phase. *(2026-10-28: non-escaping closures now exist, ADR-010 as amended; the library items remain.)*
