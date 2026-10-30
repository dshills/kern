# Editor follow-ups: definitions, references, test files, cached checks

**Date:** 2026-10-30. **Spec:** `specs/editor-nav/SPEC.md`. **Plan:**
`specs/editor-nav/PLAN.md`. **Follows:** `docs/reports/language-server.md`,
whose "Not done" this closes in part.

## What changed

- **Go to definition and find references** (`compiler/lsp/nav.go`).
  - The server indexes a resolved program's names: identifiers the
    resolver gave a symbol, the declarations of locals, parameters and
    bindings, method calls and calls of imported functions
    (`module.name`), field accesses, imported constants, and the names
    in function, method, type, interface, constant, global and field
    declarations.
  - Each name carries a key that identifies its target: file and
    position for functions and locals, module and name for types,
    interfaces, constants and globals, and owner type and name for
    fields. This works even when a generic call refers to an
    instantiated copy.
  - `textDocument/definition` returns the declaration's name.
    `textDocument/references` returns every name with the same key,
    across the program's files, with or without the declaration.
  - Prelude and `std/` declarations give `null` and `[]`, since they
    have no file to open.
  - Declaration names are found in the declaration's text after its
    keyword, outside `//` comments, with string literals tracked.
- **`kern check` checks test files.** It loads with `kern test`'s
  test-file rule. It reports the test files' check errors plus their
  `TEST_SIGNATURE` and `TEST_DIRECTIVE` diagnostics, with or without
  `--cache`. Those diagnostics now come from parsing alone
  (`testrun.StaticDiags`, sharing `FindFiles` with `kern test`), so the
  cached path needs no resolution. A program without test files gets a
  byte-identical response.
- **The editor's programs include test files**, so a test file's errors
  appear when any file of its module is open.
- **Cached checks in the editor.** The server checks through the same
  per-module cache as `kern check --cache`, in a directory it creates at
  start and removes at exit. An edit marks every open document stale,
  as before, but a program that contains none of the edited files is now
  served from the cache.
  - The cache serves diagnostics only. Navigation and hover always
    resolve afresh from the editor's text.
  - When the directory cannot be made, or a cache read or write fails,
    the server checks uncached and logs one warning per session.

## Changed by design

- **`initialize` advertises 7 capabilities**, not 5:
  `definitionProvider` and `referencesProvider` are new. The
  capabilities test now expects both.
- **`kern check`'s result `files`** now lists a project's test files,
  and its diagnostics include theirs (spec R5).
- **The quick-fix test** checked its fixed text with the old uncached
  check function, which is now `uncachedCheck`.
- **Spec texts updated:** `specs/test-command/SPEC.md` §2.1 and §3
  (`kern check` loads test files) and `specs/language-server/SPEC.md`
  §2 rule 3 and R12 (test files in every program; the cache).

## Verification

- **Navigation** on a two-module project:
  - definition from a function, a method, a field, a constant, a global
    and two locals, across files, and from a declaration's own name;
  - `null` on whitespace, on `print_u64` and on a document that is not
    a kern file;
  - references of a function, a field, a method and a local, with and
    without the declaration, in file and offset order;
  - `[]` for a prelude name;
  - a panicking resolver answers `-32603` for both requests.
- **Unit tests** cover the name search past a comment, a type parameter
  that shares the name's prefix, and `//` inside a string.
- **The cache.** Three programs (`main` importing `lib`, `lib` on its
  own, `util` on its own) and a program with a resolution error are open
  together. After `util` changes, `main` and `lib` are full cache hits,
  while `util` and the erroneous program are not. A new file in
  `util`'s directory makes `util` miss.
- **The fallback.** An unusable cache directory still publishes the
  same diagnostics as a cached server, with one warning over two
  checks.
- **Test files.**
  - A test file's `TEST_SIGNATURE` is published when only `app.kern`
    is open.
  - `kern check` reports a broken test file's unresolved name and
    `TEST_SIGNATURE`, the same with `--cache` on a miss and a hit.
  - The parse-only diagnostics equal `kern test`'s, and an unparsable
    test file contributes none.
- **Gates:** `golangci-lint run ./...` reports 0 issues; `go test ./...`
  is green; `go test -race ./compiler/lsp` passes twice over.

## Not done

- **Names in type annotations** (`v: Vec[u64]`, `-> Point`) and
  **struct-literal field names** (`Point { X: 1u64 }`) are not names.
  The resolver records the types they denote, not occurrences, so finding
  them would mean re-resolving names. References to a type or a field
  therefore omit those sites. The spec lists this as a non-goal.
- **Rename**, which would build on references, is not provided.
- **The cache is per program.** An edit re-checks the whole program
  that contains it, not only the edited module.
- **No timing was taken** of a batch with and without the cache. The
  host was loaded, and the plan made the timing optional.

## Reviews

- **speccritic:** the first draft was INVALID on two critical findings,
  both about the cache: hits promised for changed programs, and nothing
  said about a directory that cannot be created. Both are answered.
  Then VALID_WITH_GAPS, with no critical finding.
- **plancritic:** EXECUTABLE_WITH_CLARIFICATIONS, score 77. Folded in.
- **prism:** three passes.
  - Fixed: a declaration name matched inside a comment, and then a `//`
    inside a string read as a comment.
  - Left open by decision: indexing type-annotation names (the non-goal
    above).
- **realitycheck:** PARTIALLY_ALIGNED, score 100, no violations.
- **verifier:** name-matching recommendations for `testrun` functions.
  The new `FindFiles` is exercised through `Find` and `StaticDiags`.
