# Language server: rename across programs, and several unfinished dots

**Version:** 0.2 (2026-11-04). **Review status.** speccritic
(gpt-6.1-sol): VALID_WITH_GAPS, no critical finding; manifest coverage,
the text discovery reads and discovery failures are now stated. **Scope:**
- `compiler/lsp/rename.go`, `compiler/lsp/complete.go`, and the
  workspace root kept by `compiler/lsp/server.go`;
- tests in `compiler/lsp`;
- the "Not done" entries of `docs/reports/editor-names.md` and
  `docs/reports/editor-complete.md`;
- the report `docs/reports/editor-workspace.md`.

**Language impact:** none. **Follows:** `specs/editor-names/SPEC.md`
(rename within one program) and `specs/editor-complete/SPEC.md`
(completion).

**Glossary.**
- R*n*: requirement *n* of §3.
- *The workspace root*: the folder of `initialize`'s first workspace
  folder, else its `rootUri`, else none.
- *A program*: what the server loads for a document today (`load`: the
  document's module directory, module discovery, test files).
- *A candidate entry* is a `.kern` file under the workspace root that is
  either:
  - a `kern.mod` manifest's `entry`; or
  - a file that is not a test file and declares a module-level
    `func main`.

  Files under `std/` and the prelude are excluded. Imports resolve under
  the project root (the nearest ancestor holding `kern.mod`), so programs
  that import the same library module are entries under one root. A
  manifest's entries count wherever the manifest is, nested manifests
  included; a manifest decides only which files are entries, not which
  are skipped. Discovery reads an open document's unsaved text, and the
  disk otherwise, as `load` does.

  Directories named `.git` or `testdata` are skipped, as are those under
  `.claude`. At most 200 candidates are loaded, in sorted path order.
- *An affected program*: the program of a candidate entry, or of the
  document, whose loaded files include the file that declares the
  rename's target.
- *An unfinished dot*: a `.` token that ends its line (only spaces,
  tabs, a comment or the end of the file follow it before the newline).
- *The same program*: two entries whose loaded programs have exactly the
  same files. They are renamed once.

## 0. Goal and non-goals

- **Rename.** Renaming a name declared in a module that several programs
  import must edit every affected program, not only the document's.
  Today a rename of an exported library function from one program
  leaves the others importing it broken.
- **Completion.** Completion must work in a document holding more than
  one unfinished dot. Today `p.` on one line and `lib.` on the next
  parse as `p.lib`, so completion at either is wrong.

Non-goals:
- renaming across a workspace without a root, which keeps today's
  behaviour of one program;
- watching the file system;
- rename of a name in the standard library (refused today, unchanged).

## 1. Background

`rename` builds the document's program in memory with the new name. It
refuses the rename when that program gains errors or a name changes
meaning, and otherwise returns edits for that program only.

`completion` inserts a placeholder member (`kern_complete_`) after the
dot at the cursor when nothing follows it, then resolves the text. Other
unfinished dots in the same document are left as they are.

## 2. Design (informative)

- **Rename.**
  - The server keeps the workspace root from `initialize`.
  - `rename` finds the candidate entries, loads each program (with open
    documents' unsaved text, as `load` does), and keeps the affected
    ones. The document's own program is always one of them.
  - For each affected program it runs today's per-program rename (the
    sites, the in-memory rebuild, the error and meaning checks). It
    refuses the whole rename if any program refuses.
  - It returns the union of the edits. The same file edited by several
    programs gives the same edits, so they are merged by position.
- **Completion.**
  - Before resolving, a placeholder is inserted after every unfinished
    dot, and after the cursor's dot when nothing follows it.
  - The cursor's offset is moved by the length of the placeholders
    inserted before it, so the lookups find the same expression.
  - Dots inside strings and comments are not tokens, so they are not
    touched. A `..` is a different token, and so is the dot of a float
    literal.

## 3. Requirements and acceptance criteria

**R1. Rename reaches every affected program.** The test workspace's root
holds an empty `kern.mod` and `lib/lib.kern` (module `lib`, exporting
`func Make`). It also holds two programs that each import `lib` and call
`lib.Make`: `a/main.kern` and `b/main.kern`, each declaring
`func main`.

Renaming `Make` from `a/main.kern` returns edits for:
- `lib/lib.kern` (the declaration);
- the calls in `a/main.kern`;
- the calls in `b/main.kern`.

No other file is edited. The same rename from `lib/lib.kern` itself
gives the same edits.

**R2. A refusal anywhere refuses everywhere.**
- When `lib` already declares `Helper`, renaming `Make` to `Helper` is
  refused, and no edit is returned for any program. The message is
  today's; when a program other than the document's refuses, the message
  adds `(in the program of <entry>)`.
- A program outside `lib` refers to a library name only through its
  module (`lib.Make`), so a collision can only arise inside the shared
  module, and every affected program sees it alike.
- The per-program check still runs for each affected program, so a
  program that gains an error refuses the rename for all.

**R3. Unaffected programs are untouched.** A third program `c` that
does not import `lib` is not edited and need not load successfully: a
candidate whose program fails to load is skipped, as unaffected.

**R4. No root, one program.** Without a workspace root the rename
behaves as today. The existing rename tests pass unchanged.

**R5. Several unfinished dots.** In a document containing `p.` on one
line and `lib.` on the next (both unfinished), completion:
- at the first dot lists `p`'s members (`Sum`, `X`, `Y`);
- at the second lists `lib`'s exported declarations (`Make`, `Limit`,
  `Point`).

A third unfinished dot after the cursor does not change either list.
`..` in a range and a dot inside a string or comment are not treated as
unfinished.

**R6. Documents.** The two "Not done" entries point here, and the
report records the tests and limits (the candidate rule, the 200-entry
cap, no file watching).

## 4. Failure modes

- A directory or a manifest that cannot be read during discovery is
  skipped, with a `window/logMessage` warning naming it; the rename goes
  on over what was found.
- A candidate that fails to load or resolve is skipped. If it could be
  affected but cannot be checked, the rename still proceeds for the
  others, and the report names this limit.
- A workspace with more than 200 candidates is cut at 200 in sorted
  order. The response then carries no edits for the rest, and the
  server logs a warning.
