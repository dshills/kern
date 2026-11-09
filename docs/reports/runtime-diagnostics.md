# Runtime, phase 4: failure diagnostics

**Date:** 2026-11-09. **Spec:** `specs/runtime/SPEC.md` v0.6, area E (RE1 to
RE3). **Plan:** `specs/runtime/PLAN.md`, phase 4. **Hosts:** macOS 27.2 on
arm64 and Ubuntu 24.04 on arm64 in the `kern-linux-check` container.

## What changed

- **A trap's record has a line and a column (RE1).** It named a byte span:
  `{"trap":{"kind":"bounds","file":"app.kern","span":{"start":65,"end":70},"thread":0}}`.
  It now carries `"line":3,"col":12` between the span and the thread: the
  1-based line of the span's first byte and its 1-based byte column.
  - The compiler keeps, per source file of the program, the byte offset of
    each line's first byte (`ir.Module.Files`) and emits them as constants;
    the entry code registers them with `kern_set_lines`. A lookup is one
    scan of the files for the name's address and a binary search: no
    allocation, no lock, so it runs in the fault handler.
  - The hosted runtime, the freestanding runtime and the interpreter each
    compute it, and write the same record for the same trap.
  - Line and column are 0 when there is no table for the file, when the
    offset is past the file's end, and for the span 0-0, which the runtime
    uses for "no location".
  - `TrapInfo`, what a custom handler receives, is unchanged.
- **A backtrace on request (RE2).** With `KERN_BACKTRACE=1`, or in a
  program built with `--backtrace` and not run with `KERN_BACKTRACE=0`, the
  default handler follows the record with one line:
  `{"backtrace":{"thread":0,"frames":[{"function":"t.third","file":"bt.kern","line":2},{"function":"t.second","file":"bt.kern","line":5},...]}}`.
  - The frames are the kern functions on the trapping thread's stack,
    innermost first, at most 64, then `"truncated":true`. Each has its
    qualified name and its declaration's file and line. The location table
    the fault handler already used gained one pointer per function, the
    name.
  - One walker serves the fault handler and the backtrace: from the
    registers of a fault, or from `kern_trap`'s own frame for a trap raised
    by a call. It follows the frame-pointer chain, reading every word
    through the probe pipe, so a broken chain ends the walk and cannot
    fault.
  - Library and C frames are left out. A function the optimizer inlined
    has no frame. A debug build made with `--backtrace` marks every user
    function `noinline`, so its line lists the calls as written; a release
    build lists the functions that still have frames.
  - `kern run --backtrace` (and `KERN_BACKTRACE` for `kern run`) makes the
    interpreter write a line of the same shape from its own activations. For
    a debug `--backtrace` build the two lines are byte-identical.
- **The entry thunk is no longer a frame.** Phase 1's thunk carried
  `main`'s location marker, so a backtrace listed `main` twice. The thunk
  now has no marker and calls `main` with `noinline`: `main` keeps its own
  frame and marker.

## Requirement to test

| Clause | Test |
|---|---|
| RE1: the record's shape; line and column of the span's first byte | `TestTrapLineAndColumn` (seven programs; the expected values are counted from the source, independently of the tables) |
| RE1: the same record from the interpreter and from native code, debug and release | same (four runs per program, compared byte for byte) |
| RE1: byte columns | same ("after a multi-byte character on the line") |
| RE1: a trap the library or the runtime raises names the user's call | same (`Vec.get`, a recursive `Mutex.lock`) |
| RE1: a program of several files | same ("a trap in the program's second file") |
| RE1: an empty file, a file with and without a final newline, a missing table, an offset past the end, the span 0-0 | `TestLineColRules` (the rule, in `ir.Module.LineCol`); the C implementations follow it line for line and are held to it by the equality above |
| RE1: the freestanding runtime | `tests/freestanding/trap_bounds.kern` and `stack.kern`, whose expected console holds the record, under QEMU on both architectures |
| RE1: the table is emitted and registered | `TestEmitFunctionMarkers` |
| RE2: off by default | `TestBacktrace/off_by_default` |
| RE2: the frames, names, files and lines of a three-call chain | `TestBacktrace/a_debug_build_with_--backtrace_lists_the_calls_as_written` |
| RE2: the interpreter writes the same line | same; `TestRunBacktraceFlag` (`kern run --backtrace`) |
| RE2: `KERN_BACKTRACE=1` turns it on, `=0` turns a `--backtrace` build's off | `TestBacktrace/the_environment_turns_it_on_and_off`, `TestRunBacktraceFlag` |
| RE2: a release build lists the functions that still have frames | `TestBacktrace/a_release_build_...` |
| RE2: at most 64 frames, then `truncated`; the trapping thread's identifier | `TestBacktrace/64_frames,_then_truncated,_on_the_trapping_thread` (a spawned thread, 100 activations; native and interpreter lines equal) |
| RE2: a fault's walk starts from its registers | `TestBacktrace/a_stack_overflow,_found_through_the_fault's_registers` |
| RE2: library and C frames are left out | the three-call test (`kern_trap` and the thunk are C and unmarked; `Vec` methods in the 64-frame program are library) |
| RE3: without the setting the handler writes exactly one line | `TestTrapLineAndColumn` (counts the lines), `TestBacktrace/off_by_default` |
| a custom handler gets no backtrace | `TestBacktrace/a_custom_handler_gets_no_backtrace` |

## Deviations and open points

- **The span 0-0 gives line 0, column 0.** The spec lists two cases for 0
  (no table, an offset past the end). A trap with no location, such as a
  stack overflow the handler could not attribute, has the span 0-0 and the
  program's file; reporting line 1, column 1 for it would point at the
  `module` line.
- **A plain debug build inlines.** clang runs at `-O1` for a debug build
  and inlines small functions, so `KERN_BACKTRACE=1` on a build made
  without `--backtrace` lists fewer frames than the source has calls. The
  spec's "exact frames at debug" holds for a `--backtrace` build.
- **Unreadable-chain safety** has no test of its own. The walker reads
  each word through the same probe the fault handler has used since
  `specs/native-trap-loc`, whose tests cover it.
- **`kern test`'s result** still carries a trap's kind, file and span, not
  its line and column: the schema is unchanged.

## Verification

`go test ./...` passes on macOS, the freestanding fixtures under QEMU on
both architectures included; the phase's tests and the area-A checks pass
in the Linux container; `golangci-lint run ./...` reports no issue. The
static analyzer (RA6) flagged the first form of the freestanding lookup,
which read the table through globals a call could have changed; the lookup
now works on local copies.
