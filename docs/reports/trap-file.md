# Trap records name the trapping function's file

**Date:** 2026-10-29. **Follows:** the fix of 250198b, which made the
functions of imported user modules run; the first multi-module trap probe
after it showed the record naming the wrong file. **Scope:** the LLVM
backend, both runtimes, the interpreter and the fixture harness; the
record's shape (`{"trap": {"kind", "file", "span", "thread"}}`) is
unchanged.

## Before

Both backends stamped every trap record with one program-wide file name.
The interpreter wrote the first source file's name; the native runtime
wrote the name `kern_set_file` had been given, which was nothing at all:
the call was spliced into the entry function by a text replacement whose
pattern (`define i32 @main() {`) stopped matching when `main` gained
`argc` and `argv`, so native records had carried `"file":""` since then,
and nothing compared the field. A trap inside an imported module's
function therefore named the program's first file (interpreter) or no
file (native) with a span that only made sense in the module's own file.
Such a record is what the agent harness's debugging tasks parse.

## After

- The LLVM backend passes a fourth argument to `kern_trap`, a pointer to
  the file's name, emitted once per file as a private constant. A function
  names its own file (`ir.Func.File`, now always the declaring file after
  250198b); a library function names the file its caller passed, beside
  the span of the user-level call, through a third hidden parameter; a
  library function taken as a function value, and drop glue, pass null.
- Both runtimes (`runtime/hosted/src/kern_rt.c`,
  `runtime/freestanding/src/rt.c`) take the argument and write it in the
  record and in `TrapInfo.file`; null falls back to the program's file,
  which the entry function now sets from the emitter itself
  (`kern_set_file` with the file declaring `main`) rather than by the
  text replacement, which is deleted with its test. Traps the runtime
  raises on its own (a stack check, a mutex or condition-variable misuse,
  an allocation failure) keep the program's file: their intrinsics carry
  the user-level span but no file.
- The interpreter keeps the running activation and reports its file; a
  library activation inherits its caller's, as natively.
- The fixture harness compares the file of the interpreter's and the
  native record (it compared kind and span), and a trap fixture may state
  `//! trap-file: <suffix>` to assert which file the record names.
  `tests/trap/import_trap` traps inside an imported module and asserts
  `lib/lib.kern`.

## Measured

Every trap fixture's records agree between the interpreter and the native
build on kind, span and file. The probe that found the fault, a bounds
trap in an imported module's function, reports `lib/lib.kern` with the
span of the access in that file, in both backends.

## Not done

A trap the runtime raises itself (stack, mutex, condition variable, oom)
names the program's file even when the user-level call is in another
module; carrying a file through those intrinsics is a wider ABI change.
