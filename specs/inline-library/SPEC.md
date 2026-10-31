# Inlining library bodies in kern's IR

**Version:** 0.1 (2026-10-31). **Scope:** `compiler/opt` (a new pass and
its options), the pipelines that run programs (`cmd/kern` run,
`compiler/backend/native`, `compiler/testrun`), the shared cold list
(`ir.ColdLibrary`), PLAN.md 6.6, and the report
`docs/reports/inline-library.md`. **Language impact:** none.
**Finishes:** PLAN.md 6.6's first deferred item.

## 1. Requirements

**R1 — Where.** The pass runs in release, after the proof pass, in the
pipelines that run a program:
- the interpreter (`kern run`);
- native builds;
- `kern test`.

It never runs for the `show-*` commands or `emit-ir`, whose user-level
facts read a library call as a call (allocation sites, generation
checks, clones, effects). Their results are therefore unchanged by
construction.

**R2 — What.** A direct call to a library function is replaced by a copy
of its body when the callee:
- returns no access;
- has no `out` or closure parameter;
- is not recursive, extern, an intrinsic, a release function or a
  destructor;
- is not one of the containers' cold growth paths (`ir.ColdLibrary`);
- has at most `MaxCalleeSize` instructions.

Callers are visited callees first and within `MaxGrowth`. A callee with
a loop is not copied into a loop.

**R3 — Positions.** In a copy into a user function, every instruction
takes the call's position, and every copied block takes the call's
block's file. A trap in the copy, or in a library function the copy
calls, therefore reports the user-level call and its file, as before.
Copies into library functions keep their positions, which a library
function's traps do not read.

**R4 — Proofs.** Labels are the proof pass's, so what `show-bounds-checks`
reports is what the copies check.

**R5 — Tests.**
- **A unit test:** `Vec.new`, `Vec.push`, `Vec.len` and `expect` are
  copied, the cold growth path stays a call, and the copied trap is at
  the `expect` call.
- **An options test** pins R1.
- **End to end:** a failing `expect`, in one file and across modules,
  gives the same trap record in the interpreter and natively, in debug
  and release.
- **The full suite and the agent suites' dry runs** pass unchanged.
