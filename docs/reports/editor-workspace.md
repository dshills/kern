# Language server: rename across programs, several unfinished dots

**Date:** 2026-11-04. **Spec:** `specs/editor-workspace/SPEC.md` v0.2
(speccritic VALID_WITH_GAPS, no critical finding). **Plan:**
`specs/editor-workspace/PLAN.md` (plancritic
EXECUTABLE_WITH_CLARIFICATIONS, no critical finding). Its warning was
that skipping an entry found inside an already renamed program assumed
the two were the same program. That is fixed: programs are now compared
by their exact file sets.

## What changed

### Rename reaches every program that loads the declaration

`initialize` records the workspace root: the first workspace folder,
else `rootUri`. A rename then works in five steps:

1. Run the existing per-program rename (`renameIn`) on the document's
   program.
2. Walk the root for candidate entries: every `kern.mod`'s `entry`
   files, and every non-test `.kern` file that parses and declares a
   module-level `func main`.
   - Open documents' unsaved text is used.
   - `.git`, `testdata`, `.claude`, the root's `std` and the prelude are
     skipped.
   - Entries are sorted and capped at 200.
3. Load each entry's program and keep those whose loaded files include
   the file that declares the target. Programs with the same file set
   are renamed once.
4. Run the same check on each kept program: no new errors, and every
   name still means what it meant. If any program refuses, the whole
   rename is refused, and the message names that program's entry.
5. Merge every program's edits by file and position.

Without a workspace root, rename covers the document's program only, as
before.

### Completion with several unfinished dots

Before resolving, completion now inserts its placeholder member after
every dot that ends its line or the file, not only at the cursor's.
The cursor offset moves past the placeholders inserted before it. Dots
in strings and comments and the `..` of a range are not dot tokens, so
they are left alone. `p.` followed by `lib.` on the next line no longer
reads as `p.lib`.

## Tests

- **`TestRenameWorkspace`.** The workspace has a root `kern.mod`,
  `lib/lib.kern` declaring `Make`, two programs `a` and `b` that call
  `lib.Make`, and three that should be left alone:
  - `c`, which declares its own `Make`;
  - `d`, which imports a missing module;
  - a copy of `b` under `testdata/`.

  Renaming `Make` to `Build` edits `lib` once, `a` twice and `b` once,
  and nothing else. This holds whether the rename starts from `a` or
  from `lib`. Renaming to `Helper`, which `lib` already declares, is
  refused with no edits. Without a root, only `a` and `lib` are edited.
- **`TestCompletionSeveralDots`.** One document has `p.` and
  `lib.  // a comment` on consecutive lines, a range, a dot in a string,
  a dot in a comment, and a final `p.`. Each of the three dots gets its
  own members.
  - **The mutation:** with only the cursor's dot given a placeholder,
    the test fails 9 times.
- The existing rename and completion tests pass unchanged.

## Limits

- **Discovery cost.** Discovery runs on every rename and parses every
  candidate file under the root. The 200-entry cap bounds the programs
  loaded, not the walk.
- **No file watching.** A program created on disk is found by the next
  rename.
- **Programs that don't load are skipped.** A program that fails to load
  (an unresolved import, for example) is not checked or edited, even if
  it would have been affected. Its references to the old name are left
  behind.
- **Logging.** An unreadable directory or manifest is skipped, with a
  `window/logMessage` warning naming it. The same warning is sent when
  the 200-entry cap cuts the list.
- **A member access split across lines.** `x.` at the end of a line
  followed by `y` on the next line is valid kern. Completion now treats
  it as an unfinished dot, so inside parentheses that line can no
  longer parse while completing elsewhere in the document. No corpus
  file writes a member access that way: every line of the corpus that
  ends in `.` is a comment. Only completion's scratch text is affected,
  never diagnostics.
- **An unbounded walk.** The candidate walk has no file or time limit.
  Opening a very large folder as the root makes each rename walk all of
  it.
