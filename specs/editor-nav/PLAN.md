# Plan: editor follow-ups (definitions, references, test files, cache)

**Spec:** `specs/editor-nav/SPEC.md` v0.1. **Profile:** davin-go.
**Review status.** plancritic (davin-go, with the spec):
EXECUTABLE_WITH_CLARIFICATIONS, 77, no critical finding. Folded in: the
cache directory created at start, navigation that never relies on the
cache, the wire shapes of the two requests, selection and ordering
rules, and tests for a panic, an uncached resolution error, a changed
file set and the single fallback warning. Each phase also gained
acceptance criteria.
**Order:** four phases, each ending with `golangci-lint run ./...` and
`go test ./...` green; one commit after Phase 4.

## Phase 1: test diagnostics without resolution (R6)

**Files.** `compiler/testrun/find.go`, its test.

1. Split `Find(prog, srcs)` into a wrapper that collects the program's
   test files (as now) and `FindFiles(files []*ast.File, srcs)` holding
   the existing loop, so `kern test` is unchanged.
2. `StaticDiags(srcs []*source.File) []diag.Diagnostic`: parse every
   test file among srcs (`project.TestFile`), skip those with parse
   errors, run `FindFiles` and return its diagnostics.
3. Test: the diagnostics of `StaticDiags` equal `Find`'s for a program
   with a malformed test signature and a stray directive; a test file
   that does not parse contributes none.

**Acceptance.** `go test ./compiler/testrun ./cmd/kern` green (the
existing `kern test` tests unchanged).

## Phase 2: `kern check` loads test files (R5)

**Files.** `cmd/kern/main.go`, `cmd/kern/test_test.go` or a new
`cmd/kern/check_tests_test.go`.

1. In `run`, `check` uses `project.LoadTests` instead of `project.Load`;
   every other machine command keeps `Load` (format keeps `Entries`).
2. After the check (cached or not), `check` appends
   `testrun.StaticDiags(srcs)` and sorts. The cache path and the full
   path both append, so `--cache` agrees.
3. Tests (spec §4 item 2): the broken-test project with and without
   `--cache`; a project without test files gives a byte-identical
   response to the pre-change binary's (compared against a run of the
   same request through `project.Load` semantics captured in the test
   as the expected JSON).

**Acceptance.** The new CLI tests and every existing `cmd/kern` test
pass.

## Phase 3: the editor (R1–R4, R7, R8)

**Files.** `compiler/lsp/check.go`, `compiler/lsp/server.go`,
`compiler/lsp/protocol.go`, `compiler/lsp/nav.go` (new),
`compiler/lsp/nav_test.go` (new), `compiler/lsp/server_test.go`.

1. Program of a document (R7): `load` always passes `Tests: true`.
2. Default check (R8): `Run` creates the cache directory with
   `os.MkdirTemp` before it reads the first message (a failure leaves
   the server uncached and logs type 2 once, at the first check) and
   removes it when it returns. The check is `incremental.Check(dir, srcs, Config{Target:
   native.HostTriple(), Opt: "debug", Env: "hosted"})` plus
   `testrun.StaticDiags(srcs)`, sorted. On a directory or cache
   failure it falls back to `resolve`+`check` plus the test
   diagnostics, logging type 2 once per session however many checks
   fail. The cache serves diagnostics only: definition, references and
   hover always load the program through the overlay and resolve it
   afresh, so a full cache hit never leaves navigation without a
   resolved program or with stale text. `Options` gains `CacheStats
   func(uri string, s incremental.Stats)` for tests, and `CacheDir
   string` to force a directory (an unwritable one in the fallback
   test).
3. `nav.go`, the name index of a resolved program (spec §2):
   `names(prog, srcs) []nameRef`, each `{file, span, target any}`, built
   by walking every non-std module file:
   - `*ast.Ident` with an `Idents` entry, the target being the symbol's
     declaration (`sym` for locals, parameters and bindings; `sym.Func`,
     `sym.TDecl`, `sym.Iface`, `sym.Const`, `sym.Global` otherwise);
   - `Info.Decls` symbols (name span by searching the name in the
     declaration);
   - `*ast.CallExpr` whose callee is a `*ast.FieldExpr` with a resolved
     `Calls` entry, the target being the `*FuncDecl`;
   - `*ast.FieldExpr` in `Fields`, the target being the `*FieldDecl`,
     and in `ImportedConsts`, the target being the `*ConstDecl`;
   - top-level `*ast.FuncDecl` (via `Info.Funcs`), the resolver's
     type, interface, constant and global declarations, and struct
     fields, found by matching the module's AST declarations by name and
     position.

   `declOf(target) (file string, span, ok bool)` returns the
   declaration name span (`ok` false for library declarations or files
   outside the program).
4. Requests, in `request` under its existing `recover` (R4), with the
   LSP 3.17 shapes:
   - `textDocument/definition`: params `{textDocument: {uri}, position}`;
     result one `Location` `{uri, range}` (example: `{"uri":
     "file:///p/lib/lib.kern", "range": {"start": {"line": 1,
     "character": 5}, "end": {"line": 1, "character": 8}}}`) or `null`.
   - `textDocument/references`: params `{textDocument: {uri}, position,
     context: {includeDeclaration}}`; result `Location[]`, `[]` when
     empty.
   Malformed params answer `-32602`, as the other requests do. The
   position is read in the negotiated encoding; the name is the
   innermost name whose span holds the offset, an offset at the span's
   end included (hover's rule, `nameAt`). A location's URI is chosen by
   `uriOf` (the document's own URI when the file is open). References
   are deduplicated by (file, start) and sorted by file path, then
   offset; the declaration is added when `includeDeclaration` is true.
   Capabilities gain `definitionProvider` and `referencesProvider`
   (R3).
5. Tests (spec §4 items 1, 3, 4):
   - the two-module project's definition and references cases;
   - R7's publication;
   - the cache test with three programs and the hit counts;
   - a program with a resolution error, checked twice, never hits;
   - an edit that changes a program's file set misses;
   - the fallback with an unwritable cache directory, over two checks,
     logging exactly one warning;
   - an uncached server's publications compared with the cached
     server's;
   - a `Resolve` hook that panics making definition and references
     answer `-32603`, with the next request answered (R4).

**Acceptance.** `go test -race ./compiler/lsp` green.

## Phase 4: documents, gates, report (R9)

1. `docs/editors.md`, `README.md`, `specs/test-command/SPEC.md` §2.1 and
   §3, `specs/language-server/SPEC.md` §2 and R12 per R9.
2. Mandatory gates: `golangci-lint run ./...` 0 issues, `go test ./...`
   green. Advisory: `prism review staged --provider openai --model
   gpt-6.1-sol --format json`; `realitycheck check . --spec
   specs/editor-nav/SPEC.md --plan specs/editor-nav/PLAN.md --provider
   openai --model gpt-6.1-sol --format json` with unrelated directories
   ignored; `verifier analyze -root . -mode offline -spec … -plan …
   -include 'compiler/lsp/**,compiler/testrun/**' -format text`.
3. `docs/reports/editor-nav.md`, including the expectations R5 and R7
   change by design, and an optional timing of the batch with and
   without the cache (informative, not a gate).

**Acceptance.** The mandatory gates pass; the documents say what R9
requires; advisory findings are recorded.

## Risks

- **Name spans found by text search.** A declaration whose name also
  appears earlier in its text (a type parameter named like the
  function) would get the wrong span; the search starts after the
  declaration's keyword (`func`, `struct`, `union`, `interface`,
  `const`, `thread_local`) to avoid that.
- **`kern check` behaviour change.** Agents and scripts that check a
  project see test-file diagnostics now. That is the point; the report
  names it.
