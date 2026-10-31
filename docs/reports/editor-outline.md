# Editor: document symbols and signature help

**Date:** 2026-10-31. **Spec:** `specs/editor-outline/SPEC.md`
(speccritic VALID_WITH_GAPS; the remaining warnings flag acronyms on
the lines that define them).

## What changed

`kern lsp` answers two more requests (`compiler/lsp/outline.go`).

**`textDocument/documentSymbol`** gives the file's top-level declarations
in source order, each with its children:
- functions, with their types as hover prints them;
- structs with their fields, and unions with their variants (the
  payload's types as detail);
- interfaces with their method signatures;
- `impl` blocks, named `impl T` or `impl I for T`, with their methods
  and destructor;
- constants and `thread_local`s, with their types.

The selection is the declaration's name, or for an `impl` block its
target. A file that does not parse has no symbols.

**`textDocument/signatureHelp`** finds the innermost call whose argument
list holds the cursor and gives the resolved function's signature:
- the declaration's parameter names with the call's instantiated types,
  and a method's `self` left out;
- the active parameter, counted by the commas outside nested brackets
  and literals.

Unclosed calls being typed are found by resolving the text with every
parenthesis and bracket still open at the cursor closed there. `pair(scale(`
gives `scale`'s signature. A call through a function value, or a cursor
outside any call, gives `null`.

`initialize` reports `documentSymbolProvider` and
`signatureHelpProvider` (triggers `(` and `,`), eleven capabilities in
all.

## Tests

`compiler/lsp/outline_test.go` checks:
- **Symbols:** every kind of declaration with its children, details and
  selections, and the empty list for a file that does not parse.
- **Signature help:**
  - a plain call at its first and second argument;
  - the inner of two nested calls;
  - a method call without `self`;
  - an unclosed call, and two nested unclosed calls;
  - whitespace between the callee and its `(`;
  - commas in nested calls, strings and comments;
  - `null` outside a call and through a function value.

prism (gpt-6.1-sol) found three issues, all fixed:
- **Nested unclosed calls:** the first version closed only one call.
- **Comments before `(`:** the first version found the call's `(`
  without skipping comments. kern's comments are line comments, which
  end the call's line, so the case cannot arise, but the search skips
  whitespace and comments regardless.
- **Names in comments:** an outline item's name was its first textual
  occurrence in the declaration, which a contract clause or a comment
  could hold. Comments and literals are now skipped.

`TestInitialize` checks the two capabilities. The language-server tests
and the full suite pass.
