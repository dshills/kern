# kern machine API (`schema_version` 0.3)

Generated from `compiler/diag/schema.go` by `go generate ./compiler/diag`; do not edit.
This is the human-readable copy of spec §16; `tools/agent-api/schema.json` is the normative JSON Schema and the two must agree.

Every command reads one JSON request on standard input and writes one JSON response on standard output (§7). Failure: `ok=false`, a non-empty `diagnostics` array, and exit code 2 for user errors or 3 for internal errors.

## Common types

- `span`: `{"start": int, "end": int}`, byte offsets, half-open.
- `loc`: `{"file": string, "span": span}`.
- `label`: one of `proven`, `runtime-checked`, `assumed-unsafe`, `estimated`, `unknown`; derived from the IR provenance tags, never from heuristics.

#### `Span`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `Start` | `start` | int | Byte offset, inclusive. |
| `End` | `end` | int | Byte offset, exclusive. |

#### `Loc`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `File` | `file` | string |  |
| `Span` | `span` | span |  |

## Request

#### `Request`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `SchemaVersion` | `schema_version` | string | Always `"0.2"`; anything else is `API_UNSUPPORTED_SCHEMA`. |
| `Command` | `command` | string | One of the eleven commands. |
| `Files` | `files` | [string] | Source file paths; an empty array is `API_NO_FILES`, an unreadable file `API_FILE_NOT_FOUND`. |
| `Target` | `target` | string | Optional. LLVM-style target triple; when absent, the host target is used. Every result is computed for that target. |
| `Span` | `span` | `RequestSpan` | Optional. Names one of `files`. Required by `explain-ownership` (`API_MISSING_SPAN` otherwise) and ignored, never validated, by every other command. |
| `Edit` | `edit` | `StructuredEdit` | Optional. The structured edit applied by `apply-structured-edit`. |

#### `RequestSpan`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `File` | `file` | string | One of the request `files`. |
| `Start` | `start` | int | Byte offset, inclusive. |
| `End` | `end` | int | Byte offset, exclusive. |

## Structured edit

`edit` in the request, and the `fix` of a diagnostic, is `{"ops": [op]}`. Ops apply to the files as they were before the edit; two non-insert ops in one file conflict iff their spans intersect, and an insert at `k` conflicts with a non-insert span `[s, e)` iff `s < k < e`. Application is all-or-nothing (`EDIT_OVERLAP`, `EDIT_UNRESOLVED_SYMBOL`, `EDIT_INVALID_SPAN`).

#### `StructuredEdit`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `Ops` | `ops` | [`StructuredEditOp`] |  |

#### `StructuredEditOp`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `Op` | `op` | string | `replace_span`, `insert`, `delete_span` or `rename_symbol`. |
| `File` | `file` | string |  |
| `Span` | `span` | span | Optional. `replace_span`, `delete_span`, `rename_symbol`: the non-empty span `[start, end)` (an empty span is `EDIT_INVALID_SPAN`; use `insert`). |
| `At` | `at` | int | Optional. `insert` only: the offset whose original byte the text is placed before. |
| `Text` | `text` | string | Optional. `replace_span` and `insert`: the replacement or inserted text. |
| `NewName` | `new_name` | string | Optional. `rename_symbol` only: renames the declaration at `span` and every resolved use of it across the request files (`EDIT_UNRESOLVED_SYMBOL` if `span` is not a resolvable declaration). |

## Response

#### `Response`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `SchemaVersion` | `schema_version` | string |  |
| `OK` | `ok` | bool | `false` on failure, with a non-empty `diagnostics` array and exit code 2 (user error) or 3 (internal error). |
| `Result` | `result` | object \| null | The command's result object, or `null` on failure before a result exists. |
| `Diagnostics` | `diagnostics` | [`Diagnostic`] |  |

#### `Diagnostic`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `Code` | `code` | string | Stable code; every code maps to one numbered rule in the language reference (`spec/rules.md`). |
| `Severity` | `severity` | string |  |
| `Rule` | `rule` | string | The violated rule, `section.bullet`. |
| `File` | `file` | string | Empty only for `API_*` and `INTERNAL` diagnostics that have no source location, with `span` `{0, 0}`. |
| `Span` | `span` | span |  |
| `Related` | `related` | [`Related`] | Secondary locations in the same file. |
| `Fix` | `fix` | `StructuredEdit` \| null | A machine-applicable structured edit, or `null`. |
| `OpIndex` | `op_index` | int \| null | Zero-based index into the request's `edit.ops` for `EDIT_*` diagnostics; `null` otherwise. |
| `Message` | `message` | string | Optional. The compiler's one-sentence message for the diagnostic, the text the human rendering prints; present from `schema_version` 0.3 and absent in a 0.2 response. Not stable across compiler versions, unlike `code` and `rule`. |

#### `Related`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `Label` | `label` | string |  |
| `Start` | `start` | int |  |
| `End` | `end` | int |  |

## Commands

| Command | `result` type | `target` in result | `label` |
|---|---|---|---|
| `check` | `CheckResult` | no | no |
| `format` | `FormatResult` | no | no |
| `explain-ownership` | `ExplainOwnershipResult` | no | yes |
| `show-layout` | `ShowLayoutResult` | yes | no |
| `show-allocations` | `ShowAllocationsResult` | no | yes |
| `show-copies` | `ShowCopiesResult` | yes | yes |
| `show-effects` | `ShowEffectsResult` | yes | yes |
| `show-bounds-checks` | `ShowBoundsChecksResult` | no | yes |
| `emit-ir` | `EmitIRResult` | no | no |
| `emit-asm` | `EmitAsmResult` | yes | no |
| `apply-structured-edit` | `ApplyEditResult` | no | no |

### check

Diagnostics carry all findings. Proven by construction; no `label`.

#### `CheckResult`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `Files` | `files` | [string] |  |

### format

Formatting per the normative rules in `spec/format.md`. Required properties: `format(format(x)) == format(x)`, and `check(format(x))` yields the same diagnostic codes as `check(x)`. Proven by construction; no `label`.

#### `FormatResult`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `Files` | `files` | [`FormattedFile`] |  |

#### `FormattedFile`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `File` | `file` | string |  |
| `Formatted` | `formatted` | string |  |

### explain-ownership

One place for every place that intersects the request `span` (`API_MISSING_SPAN` when the request has none, `API_INVALID_SPAN` when it does not name one of `files` or violates the span rules).

#### `ExplainOwnershipResult`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `Places` | `places` | [`Place`] |  |

#### `Place`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `Loc` | `loc` | loc |  |
| `Owner` | `owner` | loc |  |
| `State` | `state` | string | `owned`, `moved`, `uninitialized` or `access`. |
| `Accesses` | `accesses` | [`AccessEntry`] |  |
| `DropAt` | `drop_at` | loc \| null | Where the static drop occurs, or `null`. |
| `Contract` | `contract` | `Contract` \| null | The contract block in scope, or `null`. |
| `Label` | `label` | label |  |

#### `AccessEntry`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `Kind` | `kind` | string | `view`, `edit`, `consume` or `init`. |
| `Span` | `span` | span |  |
| `Root` | `root` | loc | The root place of the access path. |

#### `Contract`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `Invariant` | `invariant` | [string] |  |
| `Requires` | `requires` | [string] |  |
| `Ensures` | `ensures` | [string] |  |

### show-layout

Sizes, alignments and offsets are for the request target. Proven by construction; no `label`.

#### `ShowLayoutResult`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `Target` | `target` | string |  |
| `Types` | `types` | [`TypeLayout`] |  |

#### `TypeLayout`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `Name` | `name` | string |  |
| `Size` | `size` | int | Bytes. |
| `Align` | `align` | int | Bytes. |
| `Fields` | `fields` | [`FieldLayout`] |  |

#### `FieldLayout`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `Name` | `name` | string |  |
| `Offset` | `offset` | int | Bytes from the start of the enclosing type. |
| `Size` | `size` | int |  |

### show-allocations

Every allocation site in the request files' user code: a call of an allocating runtime operation or of a core container operation known to allocate (`Box.new` is unconditional, `proven`, with the stored size; `Vec.push`, `Vec.with_capacity`, `Pool.alloc`, `Arena.alloc` and a deep `clone` are conditional, `unknown`), and any other call into library code whose effect summary allocates (`unknown`, `size` `null`: the callee may or may not allocate on a given call). Library code's own allocations are attributed to the user-level call; constructors that allocate nothing (`Vec.new`, `Pool.new`, `Arena.new`) are not sites.

#### `ShowAllocationsResult`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `Sites` | `sites` | [`AllocationSite`] |  |

#### `AllocationSite`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `Loc` | `loc` | loc |  |
| `Allocator` | `allocator` | string |  |
| `Size` | `size` | int \| null | Static size in bytes, or `null` when runtime-dependent. |
| `Label` | `label` | label |  |

### show-copies

Every copy for the request target.

#### `ShowCopiesResult`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `Target` | `target` | string |  |
| `Copies` | `copies` | [`Copy`] |  |

#### `Copy`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `Loc` | `loc` | loc |  |
| `Type` | `type` | string |  |
| `Bytes` | `bytes` | int | The inline size of the type per `show-layout`. |
| `Kind` | `kind` | string | `bitwise` or `explicit-deep`. |
| `Heap` | `heap` | bool | `true` iff the copy also duplicates owned heap storage, whose size is runtime-dependent and not reported. |
| `Label` | `label` | label |  |

### show-effects

Effects are transitive over statically resolved callees. `true` means the effect is possible; `false` means the compiler proved it impossible and is only emitted with label `proven`. A call through a function pointer or into FFI makes every effect `true` with label `unknown` (FFI: `assumed-unsafe`).

#### `ShowEffectsResult`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `Target` | `target` | string |  |
| `Functions` | `functions` | [`FunctionEffects`] |  |

#### `FunctionEffects`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `Name` | `name` | string |  |
| `Allocates` | `allocates` | bool | Possible unless proven impossible (`false` only with label `proven`). |
| `Traps` | `traps` | bool | Possible unless proven impossible (`false` only with label `proven`). |
| `Spawns` | `spawns` | bool | Possible unless proven impossible (`false` only with label `proven`). |
| `Unsafe` | `unsafe` | bool | Possible unless proven impossible (`false` only with label `proven`). |
| `Label` | `label` | label |  |

### show-bounds-checks

Every `check_*` instruction of the IR, kept or replaced by a recorded proof fact. One entry per source location and kind: the IR occurrences of a location (generic instantiations, the copies the release inliner makes in each caller, and the retained callee while it is still reachable) are aggregated, and the entry is `proven` with the first proven occurrence's `proven_by` only when every occurrence is proven, `runtime-checked` otherwise. The result is computed for the `--opt` level the command runs with: `release` inlines small helpers into their callers before the proof pass, `debug` keeps every call. `show-copies` and `show-allocations` likewise report a location once however many occurrences it has.

#### `ShowBoundsChecksResult`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `Checks` | `checks` | [`BoundsCheck`] |  |

#### `BoundsCheck`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `Loc` | `loc` | loc |  |
| `Kind` | `kind` | string | `bounds`, `generation`, `disjoint` or `overflow`. |
| `Label` | `label` | label | `runtime-checked` or `proven`. |
| `ProvenBy` | `proven_by` | string \| null | Non-null iff `label` is `proven`: `loop_bound`, `dominated_by_check` or `constant`. |

### emit-ir

Textual semantic IR for the request files. Proven by construction; no `label`.

#### `EmitIRResult`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `IR` | `ir` | string |  |

### emit-asm

Native assembly for the request target. Proven by construction; no `label`.

#### `EmitAsmResult`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `Target` | `target` | string |  |
| `Asm` | `asm` | string |  |

### apply-structured-edit

The new source of every changed file plus the diagnostics of a `check` on the result. Coordinate, overlap and symbol failures are atomic and set `ok=false` with one `EDIT_OVERLAP`, `EDIT_UNRESOLVED_SYMBOL` or `EDIT_INVALID_SPAN` diagnostic naming the offending op by `op_index`. Once the ops are applied the edit has succeeded (`ok=true`, `applied` = number of ops) even if the result does not parse or does not check. Proven by construction; no `label`.

#### `ApplyEditResult`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `Files` | `files` | [`EditedFile`] | Every changed file. |
| `Applied` | `applied` | int | Number of ops applied. |

#### `EditedFile`

| Field | JSON | Type | Notes |
|---|---|---|---|
| `File` | `file` | string |  |
| `Source` | `source` | string | The new source text. |
| `Formatted` | `formatted` | bool | `true` when the new source parses and is returned formatted; `false` when it is returned verbatim with the parse diagnostics. |

