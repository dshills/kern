# Interpreter: compiled instructions

**Version:** 0.1 (2026-10-31). **Scope:** `compiler/interp` (the
instruction loop and a compile step, with tests) and the report
`docs/reports/interp-compile.md`. **Language impact:** none.

## 0. Terms, goals and non-goals

**Terms.**
- **Rn:** requirement n of §1.
- **IR:** kern's semantic IR (`compiler/ir`), which the interpreter
  executes.
- **Op:** a compiled instruction, a Go closure taking the interpreter
  and the frame.

**Goals.** The interpreter runs a function's instructions without
re-deciding, at every execution, what kind each instruction is and
where its operands are. The result of every program is unchanged. The
compiled representation is this spec's subject, so it names it (ops,
R1); nothing observable changes (R2).

**Non-goals.**
- A new value representation; integer boxing stays as it is
  (`docs/reports/interp-speed.md`).
- Changes to traps, intrinsics, threads or the trap handler.
- Changes to the native backend.

## 1. Requirements

**R1 — Compile once.** At a function's first call, each block is
compiled into a list of ops and a resolved terminator. The result is
kept with the function's existing per-function record, shared by every
thread, and is never changed after it is built. Every later call runs
the compiled form.

**R2 — Same behaviour.** An op does exactly what the interpreter's
generic execution of its instruction does: the same values, the same
traps at the same positions, the same calls and the same effects in the
same order. Each specialised op handles one case of the generic
execution and delegates every other case to it. The specialised cases:
- **A constant** with a precomputed value stores that value.
- **A load or store** of a local with no path and no access convention
  (not `view`, `edit` or `out`) reads or writes the local's slot
  directly.
- **Integer arithmetic and comparisons** take the operation from the
  instruction's operator and its operands' static integer type. When an
  operand at run time is not an integer of that type, the op delegates
  to the generic execution.
- **Overflow and bounds checks** take their operands by ID.
- **A direct call** holds its target, the intrinsic or the callee, and
  makes the call the generic execution makes for that target.
- **Instructions without effect** (`unsafe_begin`, `unsafe_end`,
  `init`) compile to no op.
- **Terminators** hold their successor blocks compiled.

Every other instruction compiles to an op that runs the generic
execution. The block being executed is recorded before its ops run, as
now, so that trap records name the right file.

**R3 — Cost.** Instructions retired by `kern run --opt=release` are
reported before and after for `bench/kern/arena_ast`,
`bench/kern/vector` and `bench/kern/hash_table`, measured on the same
host back to back.

## 2. Acceptance criteria

The change is accepted when:
- every benchmark program's checksum is unchanged;
- the `compiler/interp` tests, the run and trap fixtures (interpreter
  against expected output and trap records), the interpreter-versus-
  native differential tests and the full `go test ./...` pass;
- a test compares the output and trap record of the compiled form with
  those of the generic execution alone, over every program in
  `tests/run` and `tests/trap` and over generated programs. Between them,
  the programs exercise every specialised case of R2: constants, plain
  loads and stores, each integer operator on signed and unsigned types,
  overflow, shift, division and bounds checks that trap, and calls to
  user functions, library functions and intrinsics;
- `golangci-lint run ./...` reports no issue;
- the report gives R3's figures.
