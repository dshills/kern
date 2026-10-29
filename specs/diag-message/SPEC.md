# The human message on the wire

**Version:** 0.2 (2026-10-29). **Scope:** the §7 machine API's diagnostic
object (`compiler/diag`), the normative JSON Schema and its rendered copy
(`tools/agent-api/schema.json`, `schema.md`), spec §16's `diagnostic`
type, the commands that write responses (`cmd/kern`), and the agent
harness's description of the `check` tool. **Language impact:** none.
**Related:** `specs/initial/AI_Native_Systems_Language_SPEC.md` §7 and
§16; `compiler/diag/diag.go` ("Message is not part of the wire format");
`docs/reports/agent-tasks-v2.md` and `-v3.md` (what the structured arm
sees).

**Review status.** speccritic (OpenAI gpt-6.1-sol, profile general, with
`compiler/diag/diag.go` as context): VALID_WITH_GAPS, no critical finding;
the warnings were the preflight's flags on upper-case labels and three
wordings it read as non-binding, reworded, and one scope note on F6,
answered by moving the cache-key choice to guidance.

**Glossary.** API: the §7 machine application programming interface (one
JSON request on standard input, one JSON response on standard output).
JSON: JavaScript Object Notation. CLI: the `kern` command line. Wire
format: the JSON shape of a response. Schema: the JSON Schema in
`tools/agent-api/schema.json` that the wire format must satisfy.

## 1. Problem

A diagnostic on the wire carries `code`, `severity`, `rule`, `file`,
`span`, `related`, `fix` and `op_index`, and nothing a reader can read
as a sentence: the message the compiler writes for the human rendering
(`kern check --human` prints `file:start-end: severity [CODE rule R]:
message`) is dropped from the JSON on purpose (`diag.go`: "Message is not
part of the wire format"). A consumer of the JSON, an editor or an agent,
learns *that* rule 5.1.4 was violated at a span and must look the rule
up to learn *what* happened (`value used after it was moved`, and for
most codes a sentence naming the identifiers involved: "parameter xs has
move-only type Vec[u64] and must be declared own"). The `related` entries
already carry a human label (`"moved here"`), so the format is not free
of prose; it lacks the one sentence that matters most.

## 2. Definitions

- **Message.** The text `diag.New` formats for a diagnostic: one
  sentence (no trailing newline, no leading severity or code), the same
  text the human rendering prints after `]: `. Every registered code
  produces a non-empty message.
- **Wire version.** The `schema_version` a response carries. Today every
  request and response says `"0.2"`.
- **Negotiation.** The rule that picks the response's wire version from
  the request.

## 3. Non-goals

- No change to codes, rules, spans, `related`, `fix` or `op_index`, and
  no change to the human rendering.
- No translation, no message catalogue, no stability promise on message
  text: message text is not an identifier and changes between compiler
  versions without notice; only `code` and `rule` are stable identifiers
  (spec §7).
- No change to the request shape.
- No change to the fixture harness's judgement of fixtures, which compares
  codes and rules, not messages.

## 4. Requirements

### 4.1 The field

F1. A diagnostic object gains the field `message` (JSON string): the
Message of §2, present and non-empty on every diagnostic of a response
whose wire version is 0.3 (F3), including the `API_*`, `CLI_*`,
`PROJECT_*` and `INTERNAL` diagnostics, whose message is the text they
format today ("request names no files", …).

F2. `message` is the last field of the object in the spec's listing and
in the schema; field order on the wire is not significant.

### 4.2 Versions

F3. The schema version becomes 0.3: §16's `diagnostic` type gains
`"message": string`, and `tools/agent-api/schema.json`'s diagnostic
definition requires `message` and keeps `additionalProperties: false`.
A response at version 0.3 carries `message` on every diagnostic.

F4. A request whose `schema_version` is `"0.2"` is still accepted, and is
answered at version 0.2: the response says `"schema_version": "0.2"` and
its diagnostics carry exactly the 0.2 fields, with no `message`, so that
a consumer validating against the 0.2 shape ("exactly these fields and
nothing more") sees what it saw before this document. A request whose
`schema_version` is `"0.3"` is answered at 0.3. Any other value is
`API_UNSUPPORTED_SCHEMA`, as today; that failure response, like every
failure response to a request of a known version, is at the request's
version, and at 0.3 when the request's version is unknown or unreadable
(`API_MALFORMED_REQUEST`, `API_UNSUPPORTED_SCHEMA`).

F5. A command given files on the command line, with no request on
standard input (`kern check a.kern`, and the files-first form every
machine command accepts), and the `run` and `build` commands, answer at
the newest version, 0.3.

F6. A cache of the incremental check mode written before this document
must not be read as valid after it, since its diagnostics lack messages.
(Guidance: the cache key already includes the schema version, which the
newest version, 0.3, changes.)

### 4.3 Documents and consumers

F7. `tools/agent-api/schema.md` is regenerated from the types
(`go generate ./compiler/diag`), and the schema test that holds the two
in step keeps passing; the diagnostic's section describes `message` as
the human sentence, not stable across compiler versions.

F8. Spec §16's header reads "normative, `schema_version` 0.3" and its
`diagnostic` type lists `message`; one sentence after the type states
the 0.2 compatibility rule of F4. (The spec is the owner's; this document
proposes the exact text and the change is made with their decision to
implement this item.)

F9. The agent harness's description of the `check` tool for the
structured arm names the message among the fields ("each has a stable
code, the violated rule number, a one-sentence message, the byte span,
…"); the harness's `check` tool runs the command line with files (F5),
so the structured arm receives messages from the first run after this
document. The plain arm's `--human` text has always carried the message.
The report records this as a change to the structured arm's input for
the owner, since the arm's input is part of the experiment.

F10. The README's `check` row says the diagnostics carry a message, and
its commands paragraph names the two wire versions.

### 4.4 Tests

F11. The schema tests validate a 0.3 response with messages against
`schema.json` and reject a 0.3 diagnostic without `message` and a 0.2
diagnostic with one (the 0.2 shape is checked by a schema fragment the
test builds from the 0.3 definition by dropping `message`).

F12. The API fixtures under `tests/api/` (requests at 0.2) keep passing
with their responses at 0.2 and without messages; a contract test sends
the same requests at 0.3 and asserts `message` present and non-empty on
every diagnostic, and that a command-line invocation answers at 0.3 with
messages; a request at `"0.4"` is `API_UNSUPPORTED_SCHEMA` at 0.3.

F13. `kern check --human` output is unchanged (a golden comparison on one
rejecting fixture before and after).

## 5. Implementation constraints

C1. The message is the text `diag.New` already stores; no diagnostic
site changes its text for this document. (Guidance: `Diagnostic.Message`
gets the JSON tag `message,omitempty`; a response at 0.2 is written from a
copy whose messages are blanked, by one function every response writer
goes through, so that no writer can emit a 0.2 response with a message.)

C2. The compiler must accept requests at 0.2 and 0.3 and answer every
other invocation at 0.3. (Guidance: the schema version constant becomes
0.3 and a list of accepted versions holds 0.2 and 0.3.)

## 6. Acceptance criteria

A1. `echo '{"schema_version":"0.3","command":"check","files":["x.kern"]}'
| kern check` on a file with `OWN_USE_AFTER_MOVE` yields a diagnostic
with `"message":"value used after it was moved"` (or the richer sentence
the code formats) and `"schema_version":"0.3"`; the same request at
`"0.2"` yields `"schema_version":"0.2"` and a diagnostic object without a
`message` key.

A2. `kern check x.kern` (files on the command line) yields 0.3 with
messages; `kern check --human x.kern` prints exactly what it printed
before this document.

A3. `go test ./...` passes, including the schema tests of F11 and the
contract tests of F12, with no change to any fixture under `tests/`
other than new ones; `golangci-lint run ./...` is clean.

A4. `tools/agent-api/schema.md` differs from its previous version only by
the diagnostic's `message` row and the version in its title, and
`schema.json` only by the diagnostic definition and its version string.

A5. The agent harness's dry run on the three suites passes unchanged (the
suites judge codes, outputs and counts, not messages).
