# Editor follow-ups: definitions, references, test files, cached checks

**Version:** 0.1 (2026-10-30). **Scope:**
- the language server (`compiler/lsp`): two requests, the program of a
  document, cached checks;
- `kern check` (`cmd/kern`): loading test files;
- the test-function diagnostics (`compiler/testrun`), made available
  without resolution;
- the documents `docs/editors.md`, `README.md`,
  `specs/test-command/SPEC.md` §2.1 and §3, `specs/language-server/SPEC.md`
  §2 and R12, and the report `docs/reports/editor-nav.md`.

**Language impact:** none. The §16 result shapes are unchanged. Which
files `kern check` loads changes (R5), and so does which diagnostics it
reports. **Related:** `specs/language-server/SPEC.md` (the server, its
§2 program of a document, R1 and R12); `specs/test-command/SPEC.md`
(§2.1 test files, R3 and R4 the `TEST_SIGNATURE` and `TEST_DIRECTIVE`
diagnostics); `compiler/incremental` (the per-module cache behind
`kern check --cache`, PLAN.md 4.1).

**Review status.** speccritic (OpenAI gpt-6.1-sol, profile general,
with the language-server spec as context): the first draft was INVALID
on two critical findings. R8 promised cache hits for documents whose
programs had changed, and said nothing about a cache directory that
cannot be created. Both are answered in R8 and §4 item 4. Its warnings
on field declarations, documents that are not kern documents (R2a) and
`kern test`'s exemption are also folded in.

**Glossary.** R*n*: requirement *n* of §3. Document, kern document,
program of a document, position encoding, overlay and check: as
`specs/language-server/SPEC.md` defines them. Name: an identifier
occurrence the resolver gave a meaning to, or the name in a declaration
(§2). Target: what a name means (a local, parameter or binding, a
function or method, a type, an interface, a constant, a global, a struct
field). Library declaration: a target declared by the core prelude or a
`std/` module. Test diagnostics: the `TEST_SIGNATURE` and
`TEST_DIRECTIVE` diagnostics of `specs/test-command/SPEC.md` R3 and R4.

## 0. Goals and non-goals

Goals:
- jump from a name to its declaration;
- list every use of a declaration across the program;
- report the errors of test files wherever the program's other errors
  are reported, by `kern check` and in the editor;
- avoid re-checking, on every edit, the open documents whose programs
  did not change.

Non-goals:
- rename;
- references to library declarations;
- definitions inside the prelude or `std/` sources (they are embedded in
  the compiler, not files the editor can open);
- type-annotation names (`v: Vec[u64]`), which the resolver records
  only as types, not as occurrences;
- incremental re-checking of the module that changed (the cache is per
  program: a changed module re-checks the program).

## 1. What exists

The resolver records, per program: the symbol of every resolved
identifier (`Info.Idents`); the declaring node of every local,
parameter and binding (`Info.Decls`); the callee of every call
(`Info.Calls`), including method calls and calls of imported functions
through `module.name`; the field of every field access (`Info.Fields`);
and the constant behind every `module.CONST` (`Info.ImportedConsts`).
Functions carry their file. Types, interfaces, constants and globals
carry a position, and their file is the module file whose declarations
contain them.

## 2. Names and targets

A name is, in a file of the program:

1. an identifier with an `Info.Idents` entry, meaning its symbol's
   declaration: the local, parameter or binding itself, or the
   function, type, interface, constant or global the symbol names;
2. the declaration of a local, parameter or binding in `Info.Decls`;
3. the field-name part of a field expression `x.name` that is the
   callee of a call with a resolved function (a method, or an imported
   function), meaning that function; that is a field access
   (`Info.Fields`), meaning the field; or that names an imported
   constant (`Info.ImportedConsts`), meaning the constant;
4. the name in the declaration of a function, method, type, interface,
   constant, global or struct field, meaning what it declares.

A name's span is the identifier, or, for item 3, the bytes of `name`
at the end of the field expression, or, for items 2 and 4, the first
occurrence of the declared name in the declaration's text (for a field,
the field declaration `name: Type`).

## 3. Requirements

**R1 — Definition.** `textDocument/definition` at a position of a kern
document returns the location of the declaration of the innermost name
whose span contains the position's byte offset (an offset equal to the
span's end included). The location is the declaration's name span in
its file, on the URI R2 of the language-server spec chooses for that
file. The result is `null` when no name is there, when the target is a
library declaration, or when the target's file is not a file of the
program.

**R2 — References.** `textDocument/references` returns the locations of
every name, in every file of the document's program, whose target is
the target at the position. With `context.includeDeclaration` true the
declaration's name span is included; otherwise it is not. The locations
come in file path order, then by offset, and each appears once. The
result is `[]` when no name is there, or when the target is a library
declaration.

**R2a — Other documents.** On a URI that is not a kern document,
definition returns `null` and references return `[]`, as hover and code
actions do.

**R3 — Capabilities.** `initialize` also reports `definitionProvider:
true` and `referencesProvider: true`.

**R4 — Errors are contained.** A request R1 or R2 that panics answers
error `-32603` and the server continues, as the language-server spec's
R5 requires for every request.

**R5 — `kern check` loads test files.** `kern check` loads its files
with the test-file rule of `kern test` (`specs/test-command/SPEC.md`
§2.1). This holds for the manifest's entry files and for files named on
the command line or in the request. Its diagnostics are the check's,
plus the test diagnostics of the loaded test files, sorted as every
diagnostic list is (`diag.Sort`). A program with no test file gets the
same response as before. `--cache` gives the same diagnostics as
without it. The other commands keep leaving test files out, except
`kern test`, which loads them as it always has.

**R6 — Test diagnostics without resolution.** The test diagnostics
come from parsing alone. A test file that does not parse contributes
its parse errors through the check and no test diagnostics. `kern test`
reports the same test diagnostics as before.

**R7 — The editor's program includes test files.** In the language
server, the program of every kern document is loaded with the test-file
rule. Previously only a test file's own program was. So a test file's
errors are published when any document of its module is open, and the
check includes the test diagnostics as R5 does.

**R8 — Cached checks in the editor.** The server checks a program
through the incremental cache in a directory it creates at start and
removes at exit. The configuration is the host target, debug and
hosted, as `kern check --cache` uses by default. The published
diagnostics are identical to an uncached check's.

A check is served entirely from the cache when it meets two conditions:
- no file of the program changed since the program's last check with
  the same configuration;
- the program had no resolution error then. A program with a resolution
  error is not cached, as `kern check --cache` does not cache it.

So after an edit, every open document whose program does not contain
the edited file, and had no resolution error at its last check, is
checked from the cache. The manifest affects the editor's check only
through the set of files it makes the program load (the configuration
is fixed), and that set is part of what the cache compares, so a
manifest change that alters the program is a miss. A test counts this through
the cache's statistics.

When the directory cannot be created, or a later cache read or write
fails, the server checks that program without the cache. It sends
one `window/logMessage` of type 2 for the session, the first time it
happens, and publishes the same diagnostics an uncached check gives.

**R9 — Documents.**
- `docs/editors.md` adds go-to-definition and find-references to what
  the server provides, and drops them from what it does not.
- `specs/test-command/SPEC.md` §2.1 and §3 say that `kern check` loads
  test files.
- `specs/language-server/SPEC.md` §2 says that every document's program
  includes test files, and its R12 mentions the cache.
- The README's `check` row says it checks test files.

## 4. Acceptance criteria

1. Language-server tests on a two-module project. A `lib` module
   declares a function, a struct with a field and a method, a constant
   and a global; `main` uses each of them and a local variable. The
   tests cover:
   - definition from each use to its declaration, across files;
   - definition from a declaration's name to itself;
   - `null` on whitespace and on a prelude name (`print_u64`);
   - references of the function, the field, the method and the local,
     with and without the declaration, across both files, in order;
   - `[]` for a prelude name.
2. A CLI test. In a project with `app.kern`, a clean `app_test.kern` and
   a broken `app_bad_test.kern` (a test function with a parameter, and an
   unresolved name), `kern check` reports the unresolved name and the
   `TEST_SIGNATURE`. It reports the same with `--cache`. A project with
   no test file gets the same response as before this change.
3. A language-server test: opening `app.kern` publishes the test file's
   diagnostics on its URI (R7).
4. A language-server test of R8 through a cache-statistics hook. The
   project has three programs: `main.kern` importing `lib`,
   `lib/lib.kern`, and `util/util.kern`, which is imported by nothing.
   With one document of each open and `util/util.kern` changed, the
   checks of the other two documents in that batch are full cache hits.
   The published diagnostics equal those of an uncached server given
   the same messages. A server whose cache directory cannot be created
   still publishes them and logs once.
5. The existing `kern test`, `kern check`, language-server and fixture
   tests pass unchanged, apart from expectations R5 and R7 change by
   design. The report lists those.
