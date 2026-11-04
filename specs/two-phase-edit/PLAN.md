# Plan: two-phase `edit` arguments

Implements `specs/two-phase-edit/SPEC.md` v0.2 in one phase and one
commit. **Review status.** plancritic (davin-go): EXECUTABLE_AS_IS,
score 100, full coverage.

## Phase 1

**Goals.** R1 to R6.

**Files.**
- `compiler/resolve/decls.go`: `Info.TwoPhase`, a set of
  `TwoPhaseArg{Call, Arg}`, made by `newInfo`.
- `compiler/resolve/bodies.go`: `fieldPath` is exported as `FieldPath`
  for the checker. Its one existing use, the `for` desugaring, is
  unchanged.
- `compiler/check/state.go`:
  - `access` gains `reserved`, `active`, `call` and `arg`.
  - `twoPhaseExcused(a, b)` returns the reserved access of a pair when
    the other is an `accView` (not a freeze) whose `end` is before the
    reserved access's `active`.
- `compiler/check/exprs.go`, `call`:
  - An `edit` argument whose expression is a `FieldPath` and whose
    result is a place (not `isAccess`) is marked reserved, with the call
    and its index; a method's receiver is index 0, as in the argument
    loop.
  - After the loop, `active` is the tick that ends the argument
    accesses.
  - `spawnCall` is untouched, so spawn arguments are never reserved.
- `compiler/check/stmts.go`, `finish`: a pair that `conflicts` reports
  but `twoPhaseExcused` excuses is skipped, and its argument is
  recorded in `Info.TwoPhase`.
- `compiler/lower/exprs.go`, `call`:
  - A recorded argument gets its `access_begin` emitted after the loop,
    before the `call`, in argument order. It is appended to `argAccs`,
    so it ends with the others.
  - A recorded argument that does not lower to a plain place fails the
    lowering with an internal error naming the case. This happens when
    it is not an `edit` parameter, not a place, or a place with an owned
    parent access. The validator would reject the IR anyway; the failure
    makes the broken invariant explicit.
  - Every other argument is unchanged.
- `compiler/lower/exprs.go`, `wideAccess` (`store_be`, `store_le`): the
  byte-order stores are lowered on their own path. For a recorded first
  argument:
  - the last-byte check reads the length from the place itself, before
    the value argument, as today;
  - the `access_begin` follows the value argument, before the call.
- `compiler/lower/twophase_test.go`: R3's ordering test, checked by
  disabling the deferral (the test must fail).
- Fixtures:
  - `tests/run/two_phase_edit.kern` with its `.out` (R1);
  - `tests/negative/two_phase_write`, `two_phase_replace`,
    `two_phase_projection` and `two_phase_index`, with the accepted twin
    `two_phase_ok` (R2);
  - `conflict_edit_view` rewritten to `both(v, v)`, with its `_ok` twin
    to match.
- Documents:
  - `docs/ownership.md` 5.4.3 and the same sentence in
    `specs/initial/AI_Native_Systems_Language_SPEC.md`;
  - ADR-016's status and decision;
  - `docs/reports/two-phase-edit.md` (R4, R5, the fixtures).

**Agreement of eligibility.** The checker reserves an argument only when
its expression is a `FieldPath` (a name, field selections and
parentheses) and its checker result is a place, not an access result.
Such an expression lowers to a place rooted at:
- a local, a parameter or a module variable (`r.access` nil); or
- an `edit` parameter's or a `with` binding's access value, which that
  binding owns and ends (`ownsAccess` false).

It is never a place with an owned parent access, which only a
projection-returning call or a slice produces. R1's fixture exercises
the parameter (`via`) and binding (`with b := ps.at(..)`) roots, and the
internal error above catches any shape this argument misses.

**Contracts.**
- *Soundness.* An excused pair is always a reserved `edit` and a `view`
  that ended before the call starts. No write through the reserved
  access happens before activation, because an overlapping `edit`,
  `consume`, `init` or freeze still conflicts.
- *Agreement.* The lowering defers exactly the arguments that the
  checker recorded. The recorded argument's place is a field path, which
  has no operands, so moving its `access_begin` changes no evaluation.
- *No drift.* A program accepted before the change records nothing,
  because an excused pair would have been a conflict before. Its IR is
  therefore unchanged.

**Acceptance.**
- R1: the fixture runs equal in the interpreter and native at both
  levels. It includes the byte-order stores.
- R2:
  - Each new fixture reports one `ACC_CONFLICT` at rule 5.4.4.
  - The fixture harness (`go test ./tests`) checks every existing
    negative and adversarial fixture's expected code. Live bindings are
    covered by `conflict_enclosing`, `interior_pointer_growth` and
    `pool_get2_reject`, and closures by `closure_conflict`,
    `map_each_conflict` and `user_each_conflict`; all stay rejected.
- R3: the test asserts both orders, recorded (`rec`) and unrecorded
  (`plain`). The recorded assertion fails with the deferral disabled.
- R4: `emit-ir` is byte-identical, at debug and release, for every
  program the baseline (ac43f72) accepts under `tests/`, `bench/kern`
  and `examples/`. Multi-module programs are checked from their own
  directory. The report gives the number of programs and of
  comparisons.
- R5:
  - Every `.kern` file under `bench/agent/tasks*/` (starting and
    reference) is compared by exit status and by each diagnostic's code
    and span.
  - The report lists every difference, and for each affected task what
    it means for its calibration. If none differ, it says so with the
    file count.
- R6:
  - `docs/ownership.md` gains the rule sentence and the accepted
    examples (`v.push(v.get(0usize))`, `self.setp(n, self.pstart(x))`)
    beside the rejected ones.
  - ADR-016 reads "Accepted (2026-11-04): alternative B" with its
    decision section filled.
  - The language specification's §5.4 sentence matches.
- `go build`, `gofmt`, `golangci-lint run ./...` and
  `go test ./... -count=1` are clean.

**Risks.**
- A shape where the checker records an argument and the lowering cannot
  defer it, because its place has an owned parent access. The validator
  would then reject the IR. This is guarded by the `FieldPath` and
  `!isAccess` conditions and by R4's comparison. Any such ICE in the
  fixture suites fails the run.
- A reserved access that excuses a pair in one statement of a loop body
  checked more than once: the record is a set, so this is idempotent.
