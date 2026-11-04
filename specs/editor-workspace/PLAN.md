# Plan: rename across programs, and several unfinished dots

Implements `specs/editor-workspace/SPEC.md` v0.2 in one phase and one
commit.

## Phase 1

**Goals.** R1 to R6.

**Files.**
- `compiler/lsp/protocol.go` and `server.go`: `initialize` records the
  workspace root (the first workspace folder's path, else `rootUri`'s).
- `compiler/lsp/check.go`: `load(d)` becomes `loadPath(d.path)`, so a
  program can be loaded from an entry no document holds. Overlay text
  is still used for open files.
- `compiler/lsp/rename.go`:
  - **`renameIn`** is today's per-program rename, unchanged in what it
    checks: the sites of the target key, the in-memory rebuild, the
    error count and the meaning check. It returns the sites or the
    reason for a refusal.
  - **`rename`** runs it for the document's program, then for each
    workspace entry whose program loads without diagnostics and holds
    the declaring file. Entries already inside a renamed program are
    skipped. It merges the sites by file and offset, and fails with the
    first refusal, naming the entry.
  - **`workspaceEntries`** walks the root:
    - every manifest's `entry` files, and every non-test `.kern` file
      that parses and declares a module-level `func main` (overlay
      text);
    - it skips `.git`, `testdata` and `.claude`, the root's `std`, and
      the prelude directory;
    - an unreadable directory or manifest is logged and skipped;
    - the entries are sorted and capped at 200, with a log line when
      the cap cuts.
- `compiler/lsp/complete.go`: `withPlaceholders` inserts the
  placeholder after every dot token that ends its line or the file, and
  at the cursor when completing a member with an empty prefix. It
  returns the moved `start`. The lexer's tokens exclude strings,
  comments and `..`.
- `compiler/lsp/workspace_test.go`:
  - **`TestRenameWorkspace`** covers R1 from both sides, R2 (renaming
    to `Helper`, which `lib` declares), and R3 (`c` declares its own
    `Make`; `d` fails to load; `testdata/` is skipped). It also covers
    R4, with no root.
  - **`TestCompletionSeveralDots`** covers R5.
- Documents: `docs/reports/editor-workspace.md`, and pointers in the
  "Not done" entries of `editor-names.md` and `editor-complete.md`.

**Key decisions.**
- **Renaming is keyed by declaration.** A target is identified by its
  navigation key (module and name), which is the same in every program
  under one root, so the other programs' indexes find the same target.
- **The check is not weakened.** Each affected program runs the full
  check, so any program that would gain an error or change a meaning
  refuses the whole rename.

**Acceptance.**
- The new tests pass, and so do the existing `TestRename` and the
  completion tests, unchanged.
- **Mutation:** when only the cursor's dot gets a placeholder,
  `TestCompletionSeveralDots` fails.
- `go test ./...`, `golangci-lint run ./...` and `gofmt -l` are clean.

**Risks.**
- **Cost.** A large workspace makes rename walk and parse every file;
  the cap bounds the programs loaded, not the walk.
- **No file watching.** A program created on disk after the server
  started is found at the next rename, since discovery runs per
  request.
