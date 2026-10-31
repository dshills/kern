# Native locations for stack traps and faults in safe code

**Version:** 0.1 (2026-10-31). **Scope:** the LLVM backend
(`compiler/backend/llvm/emit.go`), the hosted runtime's fault handler
(`runtime/hosted/src/kern_rt.c`), ADR-012's consequence on stack spans,
and the report `docs/reports/native-trap-loc.md`. **Language impact:**
none; the trap record shape is unchanged. **Finishes:** the "Not done"
of `docs/reports/runtime-trap-file.md`.

## 0. Goals and non-goals

Goal: a hosted native `stack` record, and an `unreachable` record raised
by a fault in safe code, names the user function the fault happened in:
its declaration's span and its file, the location the interpreter gives
a `stack` trap.

Non-goals:
- comparing these records' spans or files with the interpreter's: the
  interpreter's budget and the machine's stack run out in different
  frames, so `stack` stays compared by kind (ADR-012, runtime-trap-file
  R7);
- freestanding builds, which already pass the function's location from
  their prologue stack check;
- naming a function LLVM inlined: its code reports the function it was
  inlined into;
- a location inside a library function: it reports its user-level
  caller's declaration.

## 1. Design constraints

- A table of function addresses is a use of every function. It would keep
  LLVM from discarding a function it inlined into its one caller, which
  the backend relies on (`docs/reports/gap-diagnosis.md`).
- macOS refuses text relocations, so a function's code cannot hold a
  pointer.
- The handler runs in signal context, often on an overflowed stack, with
  `SIGSEGV` blocked. A second fault there would kill the process by
  `SIGSEGV` instead of `SIGABRT` (ADR-012), so it may read only memory
  known to be mapped.

## 2. Requirements

**R1 — Markers.** Every function of a hosted build is placed in the kern
text section (`__TEXT,__kern_text` on Mach-O, `kern_text` elsewhere) with
a 16-byte prefix: the word `0x21434f4c4e52454b`, then the index of the
function's declaration span and file in a location table, or -1 for a
library function. The entry wrapper carries the entry function's index
(LLVM inlines `main` into it) and passes the table to the runtime before
`kern_start`. A test function is called `noinline` from the `kern test`
dispatcher, so it keeps its own marker. A freestanding build carries no
marker and no table.

**R2 — Lookup.** On `SIGSEGV`/`SIGBUS` the handler tries candidate code
addresses in order:
1. the faulting instruction;
2. the link register (AArch64), or else the word at the stack pointer;
3. each return address of the frame-pointer chain.

The first candidate in a user function's code decides the location.
Return addresses are looked up one byte before. A candidate is in kern
code when it lies in the kern text section. Its function's marker is the
nearest magic word before it, at a 4-byte boundary.

**R3 — Safe reads.** The handler reads only:
- the kern text section;
- stack words at or above the stack pointer, the thread's recorded stack
  base and, for a stack fault, the end of the faulting page (with the
  system page size), and below the stack's top.

A chain that leaves this range, does not grow upward, or exceeds 4096
frames ends the search.

**R4 — Records.** A found user function gives the record its declaration
span and file. When none is found, the record keeps span 0-0 and the
program's file, as before. The kind (`stack` or `unreachable`) is
classified as before.

**R5 — Tests.**
- A backend test checks the markers, the table and the wrapper's marker
  on an AArch64 Mach-O target. It also checks the ELF section name and
  that a freestanding build has neither.
- Native tests, in debug and release, check that each record names the
  expected function's declaration and that the process dies by `SIGABRT`:
  - plain recursion;
  - recursion through `Vec.push`;
  - recursion on a spawned thread;
  - recursion whose overflow happens inside the runtime's C code, which
    only the frame chain finds;
  - a fault in unsafe code.

**R6 — Documents.** ADR-012's consequence on native stack spans, and the
comments in the harness, `kern test` and `tests/trap/import_stack`,
describe the new behaviour.
