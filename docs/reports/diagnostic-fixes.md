# Machine-applicable fixes on diagnostics

**Date:** 2026-10-27. **Spec:** §7 ("Diagnostics carry stable codes, spans,
related locations, the violated rule number, and optional
machine-applicable fixes") and §16 (`"fix": edit | null`; "machine-
applicable `fix` entries on diagnostics use this format"). **Starting
point:** the schema carried the field and the agent benchmark's structured
arm received it, but no code path in the compiler ever set it: every
diagnostic went out with `"fix": null`.

## What changed

- **One edit type.** `Diagnostic.Fix` is now a `*StructuredEdit`, the
  same type as the `edit` of an `apply-structured-edit` request, so an
  agent sends a fix back exactly as received. The Phase 0 `Edit`/`EditOp`
  subset, which nothing produced, is gone; `tools/agent-api/schema.md` is
  regenerated.
- **Attach helpers.** The resolver and the checker gained `fix(ops...)`,
  which attaches an edit to the diagnostic just reported, and
  `diag.Replace` / `diag.InsertAt` build the two operations fixes use.
- **Six producers**, chosen because the fix is mechanical and the
  compiler knows it is right:

  | code | when | fix |
  |---|---|---|
  | `TYPE_MISMATCH` | a suffixed numeric literal against a numeric type (`var x: u64 = 5u32`) | the literal re-suffixed with the expected type, sign kept |
  | `NAME_UNRESOLVED` | an unknown identifier within one edit (two from five characters) of a visible name: locals, the module's functions, constants, thread-locals, types and imports, the prelude's functions | the name |
  | `NAME_UNRESOLVED` | a method call on a type that has a method within the same distance | the method name (the field expression's tail span) |
  | `PARAM_OWN_REQUIRED` | a by-value parameter of a move-only type | `own ` inserted before the type |
  | `UNSAFE_MISSING_CONTRACT` | a function with unsafe and no contract block | a contract block with a `requires` placeholder inserted before the declaration |
  | `ATOMIC_INVALID_ORDERING` | a load, store, fence or compare-exchange failure ordering the operation does not permit | the weakest permitted ordering that keeps the operation's meaning: `acquire` for a load, `release` for a store, `seq_cst` for a fence, `relaxed` for a failure ordering |
  | `INIT_OUT_RESULT_UNHANDLED` | an unconsumed fallible out call inside a function returning a `Result` with the same error type | `?` appended to the call |

  Ties among equally close names go to the lexically first, so the fix is
  deterministic. A `TYPE_MISMATCH` on anything but a bare literal, an
  unresolved name with no close candidate, and an unhandled out call in a
  function that cannot propagate the error carry no fix rather than a
  wrong one.
- **Every fix is tested to leave the program clean.** The fixture harness
  now applies the fix of every reject fixture whose diagnostic carries one
  (`compiler/edit.Apply` on the fixture's files) and checks the result,
  which must have no diagnostics; a fixture may declare `fix: partial` to
  opt out and `fix: clean` to require a fix. Five new reject fixtures with
  accepting twins cover the literal suffix (positive and negative
  literals), a local-name typo, a method typo and the `?` fix; the existing
  fixtures for the other codes now exercise their fixes too.

## Why these and not others

A fix must be something the compiler can assert, not guess. Use after
move, access conflicts and non-exhaustive matches have no single right
repair; wrapping an unsafe operation in an `unsafe` block needs the
enclosing statement's extent, which the resolver does not keep; a
`TYPE_MISMATCH` inside an expression could be fixed at several places. For
those the diagnostic's span and rule stay the agent's guide.

## Verification

`go test ./...` (15 packages) and `golangci-lint run ./...` pass; the
Prism review (OpenAI gpt-6.1-sol, staged diff) is recorded in the commit.
`kern check` on a two-line program with a typo now returns the diagnostic
with `"fix": {"ops": [{"op": "replace_span", ...}]}` that
`apply-structured-edit` accepts unchanged.
