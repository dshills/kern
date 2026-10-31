# Editor: code completion

**Version:** 0.1 (2026-10-31). **Scope:** the language server
(`compiler/lsp/complete.go`), the lexer's keyword list,
`docs/editors.md`, `README.md`, and the report
`docs/reports/editor-complete.md`. **Language impact:** none.

## 1. Requirements

**R1 — Request.** `textDocument/completion` returns a `CompletionList`
whose items are filtered by the identifier prefix before the cursor and
sorted by label. Each item has a label, an LSP kind and a type as
detail. On a URI that is not a kern document the list is empty.
`initialize` reports `completionProvider: {triggerCharacters: ["."]}`.

**R2 — After a dot.** The server resolves the document's program with
the text as it stands. When nothing follows the dot, a placeholder
member is inserted so that the text parses. Then:
- after an imported module's name: that module's exported declarations
  (every declaration of a standard-library module);
- after a type's name, plain or qualified by a module: its functions
  without `self` and its variants;
- after any other expression: the fields and the `self` methods of its
  type, through any access. Another module's fields are offered only
  when exported.

**R3 — Elsewhere.**
- **Locals.** The locals, parameters and bindings declared before the
  cursor in the function that contains it, whose innermost enclosing
  block also encloses the cursor.
- **Declarations.** The module's own declarations, every declaration of
  the prelude, and the names of the imported modules.
- **Keywords.**

**R4 — Tests.**
- Members of a value (and with a prefix), an imported module's exports,
  a qualified type's static function, and a union's variants.
- Locals in scope only: not a later one, not one of an inner block.
- Prelude functions.
- The blank-prefix list, and the empty list on a non-kern document.
