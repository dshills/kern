# Editor: document symbols and signature help

**Version:** 0.1 (2026-10-31). **Scope:** the language server
(`compiler/lsp` and its tests), `docs/editors.md`,
and the report `docs/reports/editor-outline.md`. **Language impact:**
none.

## 0. Terms, goals and non-goals

**Terms.**
- **LSP:** the Language Server Protocol, version 3.17; "kind" values
  are its `SymbolKind` numbers.
- **URI:** the document identifier an LSP request names.
- **T1, R:** in a label, a parameter's and a result's type as printed.
- **Rn:** requirement n of §1.
- **Span:** a byte range of the document, reported as an LSP range in
  the negotiated position encoding, as every other feature does.

**Goals.** The language server gives an editor a kern file's outline
and the parameters of the call being typed.

**Non-goals.**
- Workspace-wide symbol search (`workspace/symbol`).
- More than one signature per call. kern has no overloading, so a call
  has at most one.
- Documentation text in either result: kern declarations carry no doc
  comments that the resolver keeps.

## 1. Requirements

**R1 — Document symbols.** `textDocument/documentSymbol` returns the
document's own top-level declarations, in source order, as hierarchical
`DocumentSymbol` items. Each item has:
- `range`: the declaration's span;
- `selectionRange`: the span of its name, the first occurrence of the
  name as a whole identifier inside the declaration;
- `detail`: as the table gives.

| declaration | name | kind | children | detail |
|---|---|---|---|---|
| function | its name | Function (12) | none | its type, as hover prints it |
| struct | its name | Struct (23) | fields: Field (8) | — |
| union | its name | Enum (10) | variants: EnumMember (22) | — |
| interface | its name | Interface (11) | methods: Method (6) | — |
| `impl` block | `impl T` or `impl I for T` | Object (19) | functions: Method (6), a destructor: Method named `destructor` | — |
| constant | its name | Constant (14) | none | its type |
| `thread_local` | its name | Variable (13) | none | its type |

- **Order:** children are in source order, as the top level is.
- **Child details:** a field's detail is its type and a method's its
  type. A variant's detail is its payload's types, comma-separated.
- **`impl` names** use the source text of the interface and target,
  with whitespace runs collapsed to one space. An `impl` block's
  `selectionRange` is its target's span.
- **Types** come from the resolved program; where a type did not
  resolve, the detail is empty.
- **No document:** a URI that is not an open kern document, or a
  document that does not parse, gives an empty list.

`initialize` reports `documentSymbolProvider: true`.

**R2 — Signature help.** `textDocument/signatureHelp` at a position
inside a call's argument list returns the signature of the function
called. The position is inside the list when it is after the call's `(`
and at or before its `)`.
- **The call:** the innermost such call in the document as it stands.
  When there is none, the server retries with `)` inserted at the
  position, so that an unclosed call being typed is found.
- **The function** is the one the resolver records for that call. When
  it records none (a call through a function value, an intrinsic
  without a declaration), or no call contains the position, the result
  is `null`.
- **The label** is `name(p1: T1, p2: T2) -> R`. The parameters are the
  declaration's names with the call's instantiated types, an `own` or
  `out` parameter prefixed by its convention as the function type
  printer does. A method call leaves out its `self` parameter. A
  function returning nothing has no arrow; one returning several values
  shows them as a tuple, as the type printer does.
- **Parameters:** each parameter's label is the `[start, end)` offsets,
  in UTF-16 code units, of its text `p: T` in the signature label: from
  its name's first character to its type's last, the separating `, `
  and the parentheses excluded.
- **Unresolved types:** a parameter or result whose type did not
  resolve prints as `?`; the signature is still returned.
- **`activeParameter`** is the number of commas between the `(` and the
  position that are outside every nested bracket, brace and parenthesis
  and every string and character literal. It is clamped to the last
  parameter, and is 0 for a function without parameters.
- **`activeSignature`** is 0.

`initialize` reports `signatureHelpProvider` with trigger characters
`(` and `,`.

**R3 — Robustness.** Neither request fails on any text: a panic inside
either is answered as the server already answers one (an internal
error), and the server keeps running.

## 2. Acceptance criteria

The change is accepted when every check of §3 passes, the existing
language-server tests and the full `go test ./...` pass, and
`golangci-lint run ./...` reports no issue.

## 3. Verification

- **Document symbols:** a document with one of each kind of declaration
  of R1 gives the names, kinds, children, details and ranges of the
  table. A document that does not parse gives an empty list.
- **Signature help:**
  - a plain call at its first and second argument (active parameter 0
    and 1);
  - a method call, without `self`;
  - a call nested in another call's argument, which gives the inner
    call;
  - an unclosed call being typed;
  - a comma inside a nested call or a string, not counted;
  - a position outside any call, and a call through a function value,
    which give `null`.
- **Capabilities:** `initialize` reports both providers.
