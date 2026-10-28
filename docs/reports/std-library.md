# Standard library: `std/str`, `std/map`, `std/io`

**Date:** 2026-10-28. **Decides part of:** ADR-013's list of what a
self-hosted compiler needs (strings, maps, file and process I/O, JSON,
closures); closures came with `docs/reports/closures.md`, JSON and process
control remain.

## Modules

Three modules embedded in the compiler beside the prelude
(`compiler/resolve/prelude/*.kern`, mirrored under `std/` for readers and
kept identical by `TestStdCoreMatchesPrelude`), loaded when a file imports
them. A std module's declarations are all exported, its bodyless functions
are runtime intrinsics, its bodies are library code (a trap inside reports
the user-level call), and the ownership checker checks them as it checks
the prelude.

- **`std/str`.** `String` is an owned, growable byte string over `Vec[u8]`
  (`new`, `from`, `len`, `as_view`, `push`, `push_str`, `push_u64`, `clear`,
  `clone`, `byte`, `equals`); byte-view functions `equal`, `hash`
  (64-bit FNV-1a), `starts_with`, `find`, `parse_u64`, `trim` (returning a
  `Bounds`). Safe kern throughout.
- **`std/map`.** `Map[K, V]`: open addressing with linear probing and
  tombstones (the hash-table workload's design), slots kept as three
  vectors (state, `Option[K]`, `Option[V]`), doubling at three quarters
  full. Keys satisfy the interface `Hashable` (`hash`, `eq`), implemented
  for `u64`, `u32`, `usize`, `i64`, `u8` and `str.String`. The methods that
  touch keys carry `where K: Hashable` (the map type itself is
  unconditional). `insert` returns the replaced value, `get` and `at` view
  or edit a present value (trap when absent), `contains`, `remove`, `len`.
  Lookups take the key by value: a `Copy` key is copied, an owned key is
  given up, so literals and temporaries may be passed where a `view`
  parameter would need a place.
- **`std/io`.** `read_file`, `write_file`, `remove_file`, `read_stdin`,
  `write_stdout`, `write_stderr` (results in `IoErr`: `NotFound`,
  `Permission`, `Exists`, `Other(code)`), and `args()` (the program's
  arguments, name first). Over seven descriptor-level runtime intrinsics
  (`io_open`, `io_read`, `io_write`, `io_close`, `io_remove`, `io_argc`,
  `io_arg`), which the hosted runtime implements with POSIX calls (errors
  as negative errno) and the interpreter with Go's file operations; a
  hosted program's `main` now receives `argc` and `argv` and hands them to
  the runtime. Writes to descriptor 1 go through the output buffer, in
  order with `print_str`. A freestanding image has no runtime behind these
  and fails to link if it imports `std/io`.

## Compiler changes

- Modules carry a `Std` flag; `import "std/<name>"` loads the embedded
  module (and the std modules it imports) before the program's modules are
  collected. `module.Type.method(...)` and `module.Type[Args]...` resolve
  in call position (`typeExprOf`).
- `check.Check` checks every module of the program (the prelude, the std
  modules, imported user modules and the main module), not only the
  prelude and the main module.
- Lowering treats every std module as library code; `show-layout` skips
  std types as it skips the prelude's.
- A `where` clause's bounds are now visible in the method body (the owner
  parameter in scope carries them), which `Map`'s methods rely on when
  they call each other and `with_bits[K, V]`.

Fixtures: `tests/run/std_str`, `tests/run/std_map`, `tests/run/std_io`
(the last writes, reads and removes a file in the working directory), in
both engines.

## Not done

- Borrowed lookups (`get(view self, key: view[K])`) would need a returned
  access with two access parameters, which the elision rule forbids; a
  `find(key) -> Option[usize]` slot index plus `get_at(i)` would give them.
- Iteration over a map, `String` formatting beyond `push_u64`, UTF-8
  decoding, directories, `remove`-free temporary files, JSON.

## Fixed on the way

- `match move x` never marked `x` as consumed in the ownership checker (only
  an owned temporary subject was), so a generic body matching an
  `Option[T]` by move lowered, for a `T` with a destructor, to a drop of the
  moved variable (an invalid-IR internal error). The checker now consumes
  the variable; `Map.grow` relies on it.
- A call through an interface constraint (`key.hash()` with
  `K: Hashable`) had no lowering: the interface's bodyless signature was
  treated as a runtime intrinsic. Lowering now dispatches such a call to the
  implementing method of the receiver's type under the instantiation.
