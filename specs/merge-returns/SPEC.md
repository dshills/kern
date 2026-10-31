# Merging returns that differ in a tag or a constant

**Version:** 0.1 (2026-10-31). **Scope:** a pass in `compiler/opt`
(`merge_returns.go`) run in release execution before the user inliner,
its tests, and the report `docs/reports/merge-returns.md`. **Language
impact:** none.

## 1. Requirements

**R1 — When.** Within one function other than a library instantiation,
blocks that end in `ret` merge when all of the following hold:
- **They match.** They are the same instructions and the same returned
  values under a mapping of one block's values onto the other's.
- **They differ only in a tag or a constant.** At the instructions where
  they differ, each block builds a union of the same type with another
  tag, or a constant of the same type with another value. That value is
  a Copy type.
- **Positions agree where a trap reports one.** These are checks, traps,
  drops, and calls into the library, the runtime or an extern function.
  Loads, accesses and calls to user functions may carry different
  positions, which only place a debug line. The merged block keeps the
  first block's positions.
- **The shared part calls a function with a body.** Merging is for a
  callee the inliner would otherwise copy once per return.

**R2 — Rewrite.** Each block keeps the differing instructions and the
pure instructions they read. The pure instructions are constants,
constructors, copies, and loads of a local's fields that nothing before
them in the block writes, opens or passes to a call. Each block then
stores the differing value into a Copy temporary and branches to one new
block, `ret.merge`. That block loads the temporaries and runs the shared
instructions.

**R3 — Validity.** When the rewritten function fails the IR validator,
the function is left as it was. The validator passes after the pass on
every program.

**R4 — Where.** The pass runs only in the exec pipelines
(`ExecOptionsFor("release")`: the interpreter, native builds,
`kern test`). It does not run for the analysis commands, whose reports
read the positions of loads and calls.

**R5 — Effects.** The report gives arena_ast's cycles and timed
throughput against C and Rust before and after the pass. It also lists
the other benchmark programs whose machine code changes.

**R6 — Tests.**
- Two returns that differ in a union tag, and two that differ in a
  constant argument, merge. The interpreter's output is the same as
  without the pass.
- In the release pipeline the callee of the merged returns is copied
  once.
- Returns whose checks carry different positions, returns with no call,
  and returns that differ in another operand are left as they were.
- A trap in a merged return reports the same record as without the
  pass.
- The full suite passes.
