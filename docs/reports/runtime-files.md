# Runtime, phase 7: files and streams

**Date:** 2026-11-09. **Spec:** `specs/runtime/SPEC.md` v0.6, area G (RG1 to
RG4). **Plan:** `specs/runtime/PLAN.md`, phase 7. **Hosts:** macOS 27.2 on
arm64 and Ubuntu 24.04 on arm64 in the `kern-linux-check` container.

## What changed

- **Error kinds (RG1).** `io.ErrKind` has twenty kinds and `io.kind(e)`
  classifies an `IoErr`. An `IoErr` still carries the system's number,
  which differs between hosts; the kind is found by the error's name, in
  one runtime entry natively and one table in the interpreter.
  `io.error_of(kind)` makes an `IoErr` of a kind, so that a `Reader` or
  `Writer` written in kern can fail without writing a number.
- **`File` (RG2).** `open(path, OpenOptions)` with the shorthands
  `open_read`, `create` and `open_append`; `read`, `write`, `write_all`,
  `read_all`, `seek`, `sync`, `set_len`, `metadata`, `flush` and `close`.
  The destructor closes. A file is opened close-on-exec on a descriptor
  above 2. An option set that names no access, truncates without write or
  with append, or creates without write or append is an error of kind
  `InvalidInput` before any system call. `sync` asks for durability
  (`F_FULLFSYNC` on macOS, where `fsync` stops at the drive's cache).
- **Metadata and paths (RG3).** `metadata` and `symlink_metadata` (kind,
  size, modification time in nanoseconds, the low 12 mode bits; `FileKind`
  gained `Symlink`), `exists`, `rename`, `hard_link`, `symlink`,
  `set_mode`, `read_link`, `canonicalize`, `current_dir` and `copy_file`.
- **Readers and writers (RG4).** The interfaces `Reader` and `Writer`;
  `File`, `Stdin`, `Stdout`, `Stderr` and `process.Pipe` implement them.
  `BufReader` reads through a buffer and by lines (`\n` or `\r\n`, a last
  line without a terminator is a line). `BufWriter` gathers small writes,
  sends large ones straight through, keeps what a failing writer did not
  take, and flushes when destroyed. `write_to` and `read_to_end` work on
  any `Writer` and `Reader`.
- **One entry per system call.** The runtime gained fourteen entries
  (`kern_io_seek`, `_sync`, `_set_len`, `_stat`, `_fstat`, `_rename`,
  `_link`, `_symlink`, `_chmod`, `_readlink`, `_realpath`, `_getcwd`,
  `_err_kind`, `_err_code`, and `kern_io_open` now takes the options as
  bits); all policy is kern code in `std/io.kern`. The interpreter makes
  the same calls through `syscall`.
- **A read or a write that a signal interrupts is made again** in the
  runtime, as `open`, `sync` and `set_len` are.
- **A new file's mode is 0666 less the umask**, the system's convention;
  it was 0644 less the umask.

## Language changes

Three things in the language stood in the way and were changed, each with
fixtures and reference text.

- **String escapes `\r` and `\xHH`.** A literal could not hold a carriage
  return. The lexer, the self-hosted lexer, the formatter and the grammar
  have both escapes (`tests/run/string_escapes.kern`,
  `tests/negative/string_escape_unknown.kern`).
- **A variant name several unions declare is the expected type's.**
  `ErrKind` and `IoErr` both have `NotFound` and `Other`; written without
  its union, such a name was unknown. It is now the variant of the
  expected type (a returned value, an argument, a payload, a typed
  initializer), and ambiguous only without one (`spec/types.md` §1,
  `tests/run/variant_by_expected_type.kern`,
  `tests/negative/variant_ambiguous.kern`).
- **`match take(place)` and `match replace(place, v)` are owned matches.**
  What they move out is a temporary, so an arm's binding owns its payload:
  how `into_inner` hands back what a wrapper wraps
  (`tests/run/match_replace_owned.kern`).

## Found on the way

- **The IR validator gave up on a correct function.** Its worklist allowed
  a block eight visits beyond its predecessors, which a long function
  with many early returns, inlined into its caller, exceeded:
  `ownership state does not converge` in a release build of
  `tests/run/io_file.kern`. The bound is now what the dataflow needs: one
  change per local on top of each predecessor.
- **The interpreter's `print_str` crashed on a whole array** (`var one:
  [1]u8`), which the native build accepts.

## Requirement to test

| Clause | Test |
|---|---|
| RG1: every kind survives `error_of` then `kind`, whatever the host's number; the three `IoErr` variants map to their kinds | `tests/run/io_paths.kern` (`round_trip`), both engines, both hosts |
| RG1: the kind of real failures: `ENOENT`, `EEXIST`, `EINVAL`, `ENAMETOOLONG`, `EISDIR`, `ENOTDIR`, `ENOTEMPTY`, `EPERM`, `EPIPE` | `io_file.kern` (`failures`, `exclusive`), `io_paths.kern` (`failures`), `io_buffered.kern` (`failing`) |
| RG2: write, read (0 at the end), `write_all`, `read_all`, `flush`, `sync`, `metadata`, `close` | `tests/run/io_file.kern` (`basics`) |
| RG2: `seek` from the start, the offset and the end; past the end; before the start and beyond the largest offset are `InvalidInput` | same |
| RG2: `open_append` keeps the file and writes at its end wherever the offset is; creates a missing file | same (`appending`) |
| RG2: `create_new` fails with `AlreadyExists` on a file and on a symbolic link, and `create` and `truncate` add nothing to it | same (`exclusive`) |
| RG2: the five refused option sets are `InvalidInput` and reach no file | same (`refused`) |
| RG2: `set_len` cuts and zero-fills; a write past the end leaves a gap of zeros; reading a write-only file and writing a read-only one fail | same (`lengths`) |
| RG2: close-on-exec, above 2 | `tests/run/std_process_threads.kern` and `TestProcInheritFile` (a child does not inherit an opened file), as before |
| RG3: `metadata` of a file, a directory, a link followed and not; size, a plausible time, the mode after `set_mode`, through a link too | `tests/run/io_paths.kern` (`meta`) |
| RG3: `exists` is `metadata` succeeding (a link to nothing does not exist) | same |
| RG3: `rename` (replacing a file), `hard_link` (one file, two names), `symlink`, `read_link` | same (`meta`, `names`) |
| RG3: `copy_file` replaces the destination, returns the count, copies a file of several buffers exactly | same (`names`) |
| RG3: `canonicalize` resolves links, `.` and `..` to an absolute path; `current_dir` is `canonicalize(".")` | same (`places`) |
| RG3: a missing path is `NotFound`; a NUL byte and a path of 4,096 bytes or more are `InvalidInput` | `io_paths.kern` (`failures`), `io_file.kern` (`failures`) |
| RG4: `read_line` with `\n`, `\r\n`, an empty line, a last line without a terminator, across every chunk boundary; a lone `\r` stays; it appends | `tests/run/io_buffered.kern` (`reading`: a reader that gives 1, 3 and 64 bytes a call through buffers of 2, 4 and 64) |
| RG4: `BufReader.read` from the buffer and around it; `into_inner` discards what was read ahead | same |
| RG4: `BufWriter` gathers, sends a large write straight through, `flush`, `into_inner`, the destructor's flush | same (`writing`, `dropped`) |
| RG4: a failing writer: `flush` returns the error and keeps the bytes not taken; `into_inner` destroys the writer and writes nothing more; a writer that takes nothing is an error; the destructor discards the failure | same (`failing`) |
| RG4: `File` through both | same (`files`: 5,000 lines) |
| RG4: `Stdout.write` is buffered in order with `print_str`; `flush` | same (`standard`) |
| RG4: `Stdin` by lines, `Stderr.write` returns the system's count | `TestStandardStreamsAsReadersAndWriters`, native and interpreter |
| RG4: `process.Pipe` as `Reader` and `Writer`, and against its direction | `tests/run/io_pipe_streams.kern` |

## Deviations and open points

- **`OpenOptions.trunc`, not `truncate`.** `truncate` is a reserved word
  (the conversion `truncate[T](x)`), and a reserved word cannot name a
  field.
- **Additions to the spec's list:** `io.options()` (an `OpenOptions` with
  nothing set), `io.error_of`, `io.write_to`, `io.read_to_end`,
  `BufReader.with_capacity`, `BufWriter.with_capacity` and
  `BufWriter.buffered`.
- **`PermissionDenied` from `EACCES`** has no test of a real failure: the
  container runs as root, for which the system checks no permission.
  `EPERM` is tested (a hard link to a directory), and the mapping of both
  by the round trip.
- **`copy_file` copies the bytes**, not the mode or the times.
- **`net.TcpStream`** implements the interfaces in phase 8.
- **`tests/run/std_io.kern` fails in the container**, as before this
  phase: it writes to the working directory, which is mounted read-only
  there.

## Verification

`go test ./...` passes on macOS; the phase's fixtures and tests, the
area-A checks, the sanitized corpus and the self-hosted parser pass in the
Linux container; `golangci-lint run ./...` reports no issue. The internal
review's one substantive finding was taken: `seek` with an origin outside
its three values is `InvalidInput` in the runtime too.
