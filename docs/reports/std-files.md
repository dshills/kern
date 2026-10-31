# Standard library: directories, temporary files, JSON

**Date:** 2026-10-31. **Spec:** `specs/std-files/SPEC.md`. **Closes:**
three items of the "Still not done" of `docs/reports/std-gaps.md`
(directories, temporary files, JSON). The iterator protocol is a separate
change.

## What was added

- **`std/io`.**
  - `kind_of`, `make_dir`, `remove_dir`, `list_dir`, `make_temp_dir` and
    `make_temp_file`, over five new runtime entries (`kern_io_mkdir`,
    `_rmdir`, `_kind`, `_list`, `_temp`) and matching interpreter
    intrinsics.
  - `list_dir` sorts bytewise in both engines: `qsort` with `strcmp`
    natively, `os.ReadDir`'s order in the interpreter. It retries with a
    buffer of the reported size when the names do not fit.
- **A parity fix.** The interpreter's `remove_file` used `os.Remove`,
  which also removes an empty directory, while native `unlink` refuses
  one. It now unlinks; the fixture checks that `remove_file` on a
  directory fails in all four configurations.
- **`std/json`.** It is written in kern: a recursive-descent parser over
  a `Parser { at, depth }`, a writer, `find`, `as_u64`, `as_i64` and three
  value builders. Numbers keep their text, so no precision is lost and no
  float formatting is needed.

## Found on the way

- **`out` is a keyword** (`out` parameters). A local named `out` in
  `std/io` went unnoticed until a program imported the module, because a
  std module is parsed only on import. The new `TestStdModulesCheck`
  resolves and checks every std module.
- **Owned union results need `with`.** Matching a `Result[Value, JsonErr]`
  directly is `OWN_DESTRUCTURE_DESTRUCTOR`, so the fixtures bind it with
  `with r := json.parse(...)`, as `std_io` does with `read_file`.
- **Variants of another module** are built as `json.Value.Null`; the
  bare variant name does not resolve across modules.
- **My first differential generator** did not catch an accepted trailing
  comma, and recorded lone surrogates per generated text, which a
  mutation could invalidate. The test now adds near-valid mutations
  (trailing comma, leading zero, cut escape) and decides lone surrogates
  from the final text.

## Verification

- **`tests/run/std_dirs`** and **`tests/run/std_json`** give the same
  output in the interpreter and natively, in debug and release.
- **`TestJSONDifferential`.** 702 texts: the 700 generated ones, one
  nested 600 deep (`TooDeep`) and one nested 500 deep (accepted). Each of
  three widened parsers fails it: a trailing comma accepted (1 case),
  lone surrogates accepted (117), leading zeros accepted (6).
- **`TestStdModulesCheck`** passes for `str`, `map`, `io` and `json`.
- **Gates.** `golangci-lint run ./...` reports 0 issues.

## Reviews

prism, four passes, every finding fixed:
- `as_i64("-0")` returned `None`; it is 0.
- `list_dir` checked `errno` after allocations that may set it; it is
  now reset before each `readdir`.
- `qsort` was given a null base for an empty directory.
- A temporary file's `close` error was ignored; it now fails the call
  and removes the file.
- `write` copied invalid UTF-8 from a `Text` built from arbitrary bytes,
  and the text of a hand-built `Number` as is. It now writes U+FFFD for
  an invalid byte and `null` for a non-number, so its output always
  parses.
- The differential test's decode accepted trailing data. Its generator
  also wrote raw `"` and `\` into strings, which made intended-valid
  texts invalid.

## Not done

- **Linux** runs of the new runtime entries. They use POSIX calls only
  (`mkdir`, `rmdir`, `stat`, `opendir`, `mkdtemp`, `mkstemp`), and the
  macOS host runs them in all four configurations.
- **JSON numbers** are not read as floats; `as_u64` and `as_i64` cover
  integers.
