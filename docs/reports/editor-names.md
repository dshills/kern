# Editor: names in annotations, rename, per-module checks

**Date:** 2026-10-31. **Spec:** `specs/editor-names/SPEC.md`. **Closes:**
the "Not done" of `docs/reports/editor-nav.md`.

## What changed

- **Names in annotations.** The resolver records what each `TypeName`
  resolves to (`Info.TypeNames`, `Info.IfaceNames`). The editor's index
  counts those as names, as it does struct literals' field names and
  interface methods.
  - The resolver's own record is used rather than re-resolving in the
    editor, so type parameters that shadow a type are handled by the
    resolver's scoping.
- **Rename** (`compiler/lsp/rename.go`), with `prepareRename`. After
  collecting the target's names, the server builds the renamed program
  in memory and refuses the rename when either check fails:
  - **Errors.** Resolution and checking must report no more errors than
    before. This catches a duplicate declaration and an exported name
    made private.
  - **Meaning.** Every old name, shifted by the edits before it, must be
    a name of the renamed program with the shifted declaration, or the
    same library key, and no name may appear or disappear. This catches
    a local renamed to the global it then shadows, which compiles
    cleanly.
- **Interface methods as declarations.** The first rename of an
  interface was refused, wrongly. A call through a constraint keyed the
  interface's method, which had no declaration, so the key's position
  shifted with the edit and failed the meaning check. Indexing interface
  methods fixed it, and gave go-to-definition for such calls.
- **Per-module checks.** On any miss the cache used to re-check the
  whole program. It now takes modules in dependency order:
  - A module whose key has an entry is reused.
  - Any other module is checked alone with the new
    `check.CheckModules` against the program resolved once.
  - An edit to a module body re-checks that module only. A change to its
    interface also re-checks the modules importing it.
  - The embedded library is hashed into every key, so modules importing
    `std/...`, never cached before, are now cached.

## Changed by design

- `initialize` reports 8 capabilities, not 7: `renameProvider` is new.
- References of a struct field include struct literals' field names.
  The editor-nav test expected otherwise and was updated.
- `TestTargetedInvalidation` expected every module to miss after any
  change to `util`. A body-only change now re-checks `util` alone and
  serves `geo` and `app` from their entries.

## Verification

- **`TestDefinitionAndReferences`:** the extended type and field
  references. **`TestAnnotationNames`:** a qualified generic argument,
  results, literals and an impl; a constraint; definition through a
  constraint; renaming an interface.
- **`TestRename`:** a field (1 edit in `main`, 3 in `lib`), a type (4)
  and a local (3), each result resolving cleanly; six refusals, each
  with its reason; `prepareRename` with its placeholder and its `null`.
- **`TestPerModuleReuse`:** an edit to `app` re-checks `app` alone
  (`Hits` 2), with the full check's diagnostics, also when the edit
  introduces a use after move. A module importing `std/io` hits.
  `TestRandomizedEdits` still matches the full check after random edits.
- **`TestCachedChecks`:** an edit to `main.kern` re-checks `app` and
  serves `lib`.
- **Gates.** `golangci-lint run ./...` reports 0 issues. `go test -race
  ./compiler/lsp` passes. `go test ./...` passes except the
  ThreadSanitizer fixture `race_raw_ptr`, whose symbolizer hangs on this
  host (it fails the same way on 7e5f638).

## Reviews

- **prism:** one finding, fixed. Each per-module check enumerated, sorted
  and collected the files of every module before filtering. Modules are
  now filtered first.

## Not done

- **Rename does not reach other programs** importing a renamed exported
  name. They are outside what the server loads.
- **Resolution still covers the whole program** on a partial miss. Only
  checking is per module.
