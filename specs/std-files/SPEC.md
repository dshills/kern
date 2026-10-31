# Standard library: directories, temporary files, JSON

**Version:** 0.1 (2026-10-31). **Scope:**
- `std/io` (`compiler/resolve/prelude/io.kern`, mirrored in `std/`) and
  its runtime entries (hosted runtime, interpreter, backend);
- a new embedded module `std/json`;
- the documents `spec/types.md` §1 and `README.md`, and the report
  `docs/reports/std-files.md`.

**Language impact:** none; library additions. **Follows:** the "Still not
done" of `docs/reports/std-gaps.md`.

## 1. Requirements

**R1 — Directories.** `std/io` gains:
- `kind_of(path) -> Result[FileKind, IoErr]`, where `FileKind` is
  `RegularFile`, `Directory` or `OtherFile`, symbolic links followed;
- `make_dir(path)` (`Exists` when the path names anything);
- `remove_dir(path)` (empty directories only);
- `list_dir(path) -> Result[Vec[Vec[u8]], IoErr]`: the entries' names,
  `.` and `..` left out, in bytewise order, so both engines list alike.

Paths follow `std/io`'s existing rules: 4096 bytes or more is an error,
and so is an embedded NUL.

**R2 — Temporary files.** `make_temp_dir(prefix)` and
`make_temp_file(prefix)` create a fresh directory or empty file in
`$TMPDIR` (`/tmp` when unset), named the prefix plus random characters,
and return its path. A prefix holding `/` or a NUL is an error. Removing
it is the program's job.

**R3 — Parity.** The interpreter does what the hosted runtime does,
error for error. Its `remove_file` now unlinks, as natively, instead of
also removing an empty directory.

**R4 — JSON.** `std/json` (RFC 8259):
- **`Value`** is `Null`, `Bool(b)`, `Number(text)`, `Text(s)`,
  `Array(items)` or `Object(members)`, where a `Member` has a `key` and a
  `value`. A number keeps its text as written. An object keeps its members
  in order, a repeated key included.
- **`parse(text) -> Result[Value, JsonErr]`** accepts exactly one value
  with optional surrounding whitespace.
  - It decodes every escape, surrogate pairs included.
  - It rejects invalid UTF-8, lone surrogate escapes, control bytes in
    strings, leading zeros and trailing commas, with `Syntax(at)`.
  - It refuses nesting deeper than 512 with `TooDeep(at)`.
- **`write(v, dst)`** appends compact text. `"`, `\` and control bytes
  are escaped; everything else is written as is.
- **Helpers.** `find(v, key)` is the first member's index. `as_u64` and
  `as_i64` read integral numbers. `number_u64`, `number_i64` and `text`
  build values. The other variants are built by name
  (`json.Value.Null`).

**R5 — Tests.**
- **Fixtures** in both engines and both opt levels: `std_dirs` (listing,
  kinds, errors, cleanup, a temporary file, a bad prefix) and `std_json`.
- **A differential test against Go's `encoding/json`.** 700 generated
  texts, a third mutated by a byte or into a near-valid form. A text is
  valid for `std/json` exactly when Go finds it valid, it is UTF-8 and it
  has no lone surrogate escape. A written value decodes in Go to what the
  original decodes to. The test fails when a trailing comma, a lone
  surrogate or a leading zero is accepted.
- **Every std module** resolves and checks when imported.
