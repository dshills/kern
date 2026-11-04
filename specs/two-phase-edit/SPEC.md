# Two-phase `edit` arguments

**Version:** 0.2 (2026-11-04). **Review status.** speccritic
(gpt-6.1-sol): VALID_WITH_GAPS, no critical finding; the spawn exclusion
is in the rule text and R5's comparison is defined. The remaining
warnings flag requirement labels as acronyms. **Scope:**
- the access checker (`compiler/check`);
- the lowering of call arguments (`compiler/lower/exprs.go`);
- the ownership rules 5.4.3 and 5.4.4 (`docs/ownership.md`), and the
  same rule in the language specification
  (`specs/initial/AI_Native_Systems_Language_SPEC.md` §5.4);
- fixtures under `tests/run`, `tests/negative` and `tests/adversarial`;
- ADR-016's decision;
- an audit of the agent suites' starting and reference programs;
- the report `docs/reports/two-phase-edit.md`.

**Language impact:** more programs are accepted, and no accepted program
changes meaning. The diagnostics' codes, rules and wire schema are
unchanged. **Follows:**
`docs/decisions/ADR-016-two-phase-edit-arguments.md`, alternative B,
accepted by the owner on 2026-11-04.

**Glossary.**
- R*n*: requirement *n* of §3.
- ADR: an architecture decision record (`docs/decisions/`).
- IR: the compiler's intermediate representation (`compiler/ir`), as
  `show-ir` prints it.
- *A field path*: an argument expression that is a name (a local, a
  parameter, an access binding or a module variable) followed by zero or
  more field selections, `x`, `self.a`, `p.l.r`, possibly in
  parentheses. It contains no index, slice, call or other operator.
- *A two-phase argument*: an argument passed to an `edit` parameter
  whose expression is a field path. A method call's receiver counts
  when the method takes `edit self`.
- *Evaluation, activation*: a two-phase argument's access is *reserved*
  from the argument's evaluation until every argument of the call has
  been evaluated. It is *active* from then until the call returns.

## 0. Goal and non-goals

Rule 5.4.3 starts an argument's access when the argument is evaluated.
So this call is rejected today:

```kern
self.setp(n, self.pstart(x))   // ACC_CONFLICT: view of self during the receiver's edit
```

Yet nothing is written through `self` before `setp` runs, and the view
taken by `pstart` has ended by then. The goal is to accept such calls
by reserving the `edit` and checking it against reads only from the
moment it becomes active.

Non-goals:
- **Arguments other than field paths.** These keep today's rule:
  - an indexed or sliced place (`xs[i].m(xs.len())`);
  - a projection returned by a call (`v.at(0usize).m(v.len())`,
    `m.lock().set(m.lock().get())`).
  
  Reserving a projection is excluded because evaluating one runs code
  and the projection can carry a release (a lock guard): accepting
  `m.lock().set(m.lock().get())` would take a second `lock` while the
  first is held.
- **`init` (`out`) and `consume` arguments**, and arguments to
  `thread.spawn` and `Scope.spawn`.
- **Writes during the reservation.** An `edit`, `consume` or `init` of
  an overlapping path in a later argument still conflicts, as Rust's
  two-phase borrows do.
- **Simplifying the kern-written parser's helper methods**
  (`selfhost/`). That is a follow-up, once this is in.

## 1. Rule text (normative)

Rule 5.4.3 gains this sentence after the one on argument accesses:

> An `edit` argument that is a field path (a name followed only by field
> selections) is *two-phase*. Its access is *reserved* from the
> argument's evaluation until every argument of the call has been
> evaluated, and *active* from then until the call returns. A reserved
> access conflicts like any `edit` access, except with a `view` access
> that ends before the access becomes active. Arguments to `thread.spawn`
> and `Scope.spawn` keep the access lifetimes above and are not two-phase.

Rule 5.4.4's definition of conflict is unchanged. The 5.4.3 sentence
decides only which accesses are simultaneously live.

## 2. Design (informative)

- **Checker.** In `call`, an `edit` argument that is a field path, not
  an access result, records `reserved` on its access. Once the
  arguments are evaluated, the call's activation tick (the one that ends
  the argument accesses) is stored on it. In `conflicts`, a reserved
  access and a `view` access that is not a binding's freeze do not
  conflict when the view ended before activation. Every other pair is
  judged as today.
  - When this exception excuses a pair, the checker records the call and
    the argument's index, counting a receiver as index 0, in the
    checked program's info (`Info.TwoPhase`).
  - Only a call that the old rule rejected has such an entry.
- **Lowering.** For a recorded argument, the `access_begin` is emitted
  after the last argument is evaluated and before the `call`, in
  argument order.
  - Its place has already been computed: a field path's place has no
    operands, so this moves nothing observable.
  - Every other argument, recorded or not, is lowered as today, so a
    program accepted before this change lowers to the same IR.
  - The IR validator's open-access check (invariant 2) is unchanged. It
    now sees the view begin and end before the edit opens.
- **The interpreter and the backend** follow the IR and need no change.

## 3. Requirements and acceptance criteria

**R1. Accepted shapes.** A run fixture `tests/run/two_phase_edit.kern`
checks, runs in both engines and prints a fixed transcript. It covers:
- a method on `edit self` with an argument that calls a `view self`
  method returning a `Copy` value (`self.setp(n, self.pstart(x))`);
- a field receiver with an argument read from a sibling field method
  (`self.a.alloc(self.stride())`);
- a Copy field read of the edited place (`self.set(self.count)`);
- a projection's Copy read (`v.push(v.get(0usize))` on `Vec[u64]`);
- a plain function's `edit` parameter (`bump(x, x + 1u64)` with `x`
  a `u64`);
- a two-phase argument through an `edit` parameter and through a `with`
  binding, `p.set(p.len())` inside `with p := ...`;
- two-phase arguments on disjoint fields of one root in the same call.

The output shows that each callee saw the values its arguments had
before the call.

**R2. Still rejected.** Negative fixtures, each `ACC_CONFLICT`
(rule 5.4.4) unless stated, with `//! twin:` partners where the suite
requires them:
- a `view` argument of the edited place, live for the call
  (`both(v, v)` with `edit` then `view` parameters);
- an `edit` during the reservation (`self.set(self.bump())`, where
  `bump` takes `edit self`);
- a `take` during the reservation (`self.m(take(self.x))`);
- a live `with` binding of the place (`with e := v.at(0usize) {
  v.push(e) }`, as today);
- a closure argument that captures the place
  (`v.each(|n| v.len())`-shaped, as today);
- a non-field-path receiver: `v.at(0usize).set(v.len())`, and an indexed
  place `xs[0usize].set(xs.len())`.

The existing negative and adversarial fixtures keep their codes, except
one: `tests/negative/conflict_edit_view.kern` (`v.push(v.get(0usize))`)
is the R1 shape, so it is accepted. It is rewritten to R2's first case,
with the same code and twin.

**R3. Lowering.** The IR of every R1 fixture validates. A lowering test
checks two things:
- a recorded argument's `access_begin` follows the other arguments'
  instructions;
- an unrecorded two-phase argument's `access_begin` keeps its place.

The native and interpreted transcripts of R1 are equal (the run suite's
existing comparison).

**R4. No accepted program changes.** For every `.kern` program the
baseline (ac43f72) accepts under `tests/`, `bench/kern/` and
`examples/`, `emit-ir` output from the baseline and from this change is
byte-identical at `--opt=debug` and `--opt=release`.
- `examples/` holds the kern-written parsers (`examples/selfhost`,
  `examples/selfhost_parser`).
- A multi-module program is checked from its own directory.
- The report gives the number of programs and of comparisons.

**R5. The agent suites.** The report lists every starting and reference
program under `bench/agent/tasks*/` whose `kern check` result differs
between the baseline and this change. A result differs when the exit
status differs, or when the list of diagnostics, each taken as its code
and span, differs. Message text is not compared. It also says what that means for
each affected task's calibration. An affected task's files are not
changed; that is the owner's call.

**R6. Documents.**
- `docs/ownership.md` carries §1's rule text and an accepted example.
- ADR-016's status is Accepted (B), with its decision dated.
- The report records the fixtures, R4 and R5.
