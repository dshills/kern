# The human message on the wire

**Date:** 2026-10-29. **Spec:** `specs/diag-message/SPEC.md` v0.2
(speccritic VALID_WITH_GAPS, no critical finding). **Plan:**
`specs/diag-message/PLAN.md` (plancritic EXECUTABLE_WITH_CLARIFICATIONS,
no critical finding). **Scope:** the §7 machine API's diagnostic object
and its schema; the language is unchanged.

## What changed

A diagnostic on the wire carries `message`, the one sentence the compiler
formats for it and the human rendering prints after the code and rule.
The schema version is 0.3. A request at 0.2 is still answered, at 0.2 and
in the 0.2 shape: `schema_version` says 0.2 and no diagnostic carries a
`message`, so a consumer validating against the 0.2 shape ("exactly these
fields and nothing more") sees what it saw before. A request at 0.3, and
every invocation with files on the command line (`kern check a.kern`,
`run`, `build`), answers at 0.3. Any other version is
`API_UNSUPPORTED_SCHEMA`, answered at 0.3; a failure after a request of a
known version is decoded is at that version.

`diag.WriteAt` is the one writer of responses and makes the version's
shape from a copy, so no command can emit a 0.2 response with a message
and a caller's diagnostics are never changed; every machine command
passes the negotiated version to it explicitly (no package state), and
`run`, `build` and `debug`, which take no request, write at 0.3.
`tools/agent-api/schema.json` requires `message` on a diagnostic and
says 0.3; `schema.md` is regenerated from the types; spec §16 lists the
field and the 0.2 rule.

## Measured

`kern check` on a program that uses a moved vector: at 0.3,
`{"code":"OWN_USE_AFTER_MOVE", …, "message":"v is used after being
moved"}`; at 0.2 the same diagnostic with the eight fields of before and
no `message`. `kern check --human` prints exactly what it printed before
(a golden line is pinned). The three agent suites' dry runs pass
unchanged: they judge codes, outputs and counts.

## For the owner

The agent harness's `check` tool runs the command line with files, so
from the first run after this change the structured arm's diagnostics
carry messages; its prompt now names the message among the fields. The
plain arm's `--human` text has always carried the message. The ablation
therefore no longer withholds the message from the structured arm, and
a future matrix run compares structure plus queries against text, both
with messages; the second suite's record (`docs/reports/agent-tasks-v2.md`)
was made without messages in the structured arm.

## Reviews

speccritic's warnings were wording and one scope note on the cache key;
plancritic asked for copy semantics in the 0.2 writer and for the
negotiated version to be explicit rather than package state, both done.
prism's findings and the phase review are recorded below.

## Phase review

- prism (gpt-6.1-sol, staged): one medium finding, the successful
  structured-edit response still written at 0.3, fixed with a test; the
  final pass had none.
- realitycheck (gpt-6.1-sol, spec and plan against `compiler/diag` and
  `cmd/kern`): PARTIALLY_ALIGNED, score 100, no drift and no violation;
  every item IMPLEMENTED, PARTIAL or UNCLEAR for the tool's usual reason,
  an inventory of declarations without bodies.
- verifier (offline): risk score 41, forty-one low name-matching findings
  on helpers that predate this change (`InsertAt`, `Replace`,
  `MarshalJSON`, the generators), exercised by the schema and edit tests
  under other names; the functions this change added are not among them.
  Nothing acted on.
- clarion: not used by this project.
