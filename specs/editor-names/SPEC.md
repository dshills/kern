# Editor: names in annotations, rename, per-module checks

**Version:** 0.1 (2026-10-31). **Scope:**
- the language server (`compiler/lsp`);
- the resolver's record of nominal type references (`compiler/resolve`);
- the incremental cache (`compiler/incremental`) and
  `check.CheckModules`;
- the lexer's keyword query;
- `docs/editors.md`, `README.md` and the report
  `docs/reports/editor-names.md`.

**Language impact:** none. **Follows:** `specs/editor-nav/SPEC.md`,
whose non-goals (type-annotation names, rename, per-program cache) this
takes up.

## 1. Requirements

**R1 — More names.** These count as names of their declarations:
- the name of every nominal type reference the resolver resolves to a
  type or an interface, qualified or not, wherever it appears (an
  annotation, a generic argument, a result, a struct literal's type, an
  impl, a constraint);
- every field name of a struct literal;
- every method of an interface, as a declaration.

Definition and references use them as they use every name.

**R2 — Prepare rename.** `textDocument/prepareRename` returns the range of
the name at the position and its declaration's text as `placeholder`, or
`null` when no name the program declares is there. `initialize` reports
`renameProvider: {prepareProvider: true}`.

**R3 — Rename.** `textDocument/rename` returns a `WorkspaceEdit` replacing
every name of the target in the document's program, the declaration
included, in file then offset order. Renaming to the same name gives an
empty edit. It fails with code -32803 and a message when:
- no name the program declares is at the position;
- the new name is not an identifier or is a keyword;
- a name of the target does not spell the old name;
- the renamed program, resolved and checked in memory, has more errors
  than the original;
- any name of the renamed program refers to a different declaration than
  it did, or to a different library declaration, or a name appears or
  disappears. Positions are compared through the edits' offset shift.

**R4 — Scope.** A rename covers the document's program, as references
do. Other programs that import a renamed exported name are not edited.

**R5 — Per-module checks.**
- The cache takes modules in dependency order. A module whose key has an
  entry is served from it. A module without one is checked alone
  (`check.CheckModules`) against the program resolved once, and stored.
- The diagnostics equal a full check's.
- When every module hits, nothing is resolved.
- Imports of the embedded library are covered by a hash of its sources,
  so modules importing `std/...` are cached too.
- A cycle or an import outside the request checks in full, uncached.

**R6 — Tests.**
- References of a type include an impl, a result and a struct literal.
  References of a field include a struct literal's field.
- A qualified generic argument and a result, an interface in a
  constraint, and definition through a constraint.
- Renames of a field, a type, a local and an interface give clean
  programs.
- Each refusal of R3 is checked with its reason.
- The cache re-checks only an edited module whose interface is unchanged,
  with the full check's diagnostics, also when the edit introduces an
  error. A module importing `std/io` hits.
- The editor's cache statistics show that an edit to `main` re-checks
  `app` alone.
