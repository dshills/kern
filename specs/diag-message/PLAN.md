# Plan: the human message on the wire

**Source spec:** `specs/diag-message/SPEC.md` v0.2, speccritic
VALID_WITH_GAPS with no critical finding. **Plan gate:** plancritic
(profile davin-go, with the spec) EXECUTABLE_WITH_CLARIFICATIONS, no
critical finding, no open question; its two warnings (copy semantics of
the 0.2 writer, the negotiated version as package state) are answered in
§0. **Conventions:** every phase
ends with `go test ./...` and `golangci-lint run ./...` clean, a `prism
review staged` with no high or medium finding left unanswered, and one
conventional commit on `main`. Labels F1–F13, C1–C2 and A1–A5 are the
spec's.

## 0. Decisions that apply to every phase

- **Versions.** `diag.SchemaVersion` becomes `"0.3"`; `diag.Accepted`
  (`[]string{"0.2", "0.3"}`) lists the request versions the compiler
  answers; `diag.Supported(v string) bool` reports one.
- **The field.** `Diagnostic.Message` gets the tag `json:"message,omitempty"`.
  A response is written through `diag.WriteAt(w io.Writer, version
  string, resp Response)`: at 0.3 it writes the response with
  `SchemaVersion` set to 0.3; at 0.2 it writes a copy with `SchemaVersion` 0.2 and every diagnostic's
  `Message` blanked (so the key is absent), the copy being a new
  `Response` with a newly allocated diagnostics slice, so the caller's
  response and its diagnostics are never changed; `diag.Write` keeps its signature and means `WriteAt`
  at 0.3, so the `run`, `build`, `debug` and files-on-the-command-line
  paths need no change. The machine command path in `cmd/kern/main.go` holds the negotiated
  version in a local `wire` (0.3 until the request is decoded; then the
  request's version once `Supported` admits it, in that order:
  decode, validate the version, assign) and passes it explicitly to
  every writer of a machine command's response: `fail`, `respond`,
  `endWith` and `internal` take the version as a parameter, as do the
  `check` and `format` writers, `emitIR`, `explainOwnership` and the
  `analysisCommands` (through the `Request`, which gains no field: the
  version is a parameter beside it). No package variable holds it, so
  one `run` call cannot leak a version into the next; a test runs an
  invocation at 0.2 and then one with files on the command line in the
  same process and asserts the second answers at 0.3 with messages.
- **Schema.** `tools/agent-api/schema.json`: the diagnostic definition
  gains, as its last property, `"message": {"type": "string",
  "minLength": 1, "description": "The compiler's one-sentence message
  for the diagnostic, the text the human rendering prints; not stable
  across compiler versions, unlike code and rule."}`, `required` gains
  `"message"` last, and the document's version string moves to 0.3
  (nothing else in the file changes, A4). The rendered `schema.md` gains
  the matching last row of the diagnostic's table, from the struct
  field's tag and comment, and its title's version. `schema.md` is regenerated. The schema test's
  0.2 check builds a fragment by deleting `message` from the loaded
  definition (`defs(...)`) and validating a blanked diagnostic against
  it.
- **Spec §16.** Line 311 reads `(normative, `schema_version` 0.3)`; the
  `diagnostic` type gains `, "message": string` before the closing brace;
  one sentence follows: "A request at `schema_version` 0.2 is answered
  at 0.2, whose diagnostics lack `message` and are otherwise the same."
  Made under the owner's decision to implement the item.

## Phase 1 — The field, the versions and the writers

**Goal.** Every response carries messages at 0.3 and none at 0.2;
every command negotiates.

**Files.** `compiler/diag/diag.go` (tag, `Accepted`, `Supported`,
`WriteAt`; `Write` delegating), `compiler/diag/schema.go` (the field's
row in the rendered schema), `cmd/kern/main.go` (accept 0.2 and 0.3,
`wire`, writers), `cmd/kern/commands.go`, `cmd/kern/explain.go`,
`cmd/kern/run.go` (`emitIR`), `cmd/kern/build.go`, `cmd/kern/debug.go`
(these use `Write`, unchanged), `tools/agent-api/schema.json`,
`tools/agent-api/schema.md` (generated), `compiler/diag/schema_test.go`
(F11), `cmd/kern/cli_test.go` or a new `cmd/kern/message_test.go` (F12,
F13: the golden `--human` line of `tests/negative/ambiguous_source.kern`
before, compared after), `compiler/incremental` (nothing: the key
includes the constant).

**Acceptance.** A1, A2, A3, A4; `go generate ./compiler/diag` leaves
`schema.md` as committed.

**Risks.** A writer left on `Write` at 0.3 while the request said 0.2
would leak a message into a 0.2 response: the contract test of F12 sends
every `tests/api` fixture at 0.2 and asserts no diagnostic has a
`message` key (by decoding into `map[string]any`), which covers every
failure writer those fixtures reach; the success writers are covered by
sending a passing `check`, `format`, `emit-ir`, `explain-ownership` and
`show-bounds-checks` request at 0.2 and asserting the same.

## Phase 2 — Spec, harness and documents

**Goal.** The three documents that describe the wire format agree, and
the structured arm's description is true.

**Files.** `specs/initial/AI_Native_Systems_Language_SPEC.md` §16 (F8),
`bench/agent/harness/trial.go` (F9: the `check` tool's sentence),
`README.md` (F10), `docs/reports/diag-message.md` (what changed, A1's
before/after, the structured-arm note for the owner, the dry runs).

**Acceptance.** A5 (`go run ./bench/agent/harness -dry-run` on the three
suites exits 0); the report is committed.

## Traceability

| requirement | phase |
|---|---|
| F1–F7, F11–F13, C1–C2, A1–A4 | 1 |
| F8–F10, A5 | 2 |
