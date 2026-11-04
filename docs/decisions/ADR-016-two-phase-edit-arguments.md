# ADR-016: Two-phase `edit` arguments

**Status:** Proposed (2026-11-04). Nothing is implemented; rule 5.4.3 and 5.4.4 stand
unchanged until the owner decides.
**Date:** 2026-11-04
**Spec:** `docs/ownership.md` rules 5.4.3 (access lifetimes within a statement) and 5.4.4
(conflict); §4 evaluation order.
**Interacts with:** the agent suites (tasks whose expected diagnostics include
`ACC_CONFLICT`), the language server's quick fixes, `explain-ownership`.

## Problem

Rule 5.4.3 starts an argument's access when that argument is evaluated, and the receiver
is evaluated first. So a call that edits a value cannot take an argument that reads the
same value, even a read whose result is a plain `Copy` value that holds no access:

```kern
self.setp(n, self.pstart(x), self.pend(y))   // ACC_CONFLICT 5.4.4: view of self while
                                              // the receiver's edit of self is live
```

The program is safe. Every read in the arguments has ended, leaving a `u64` or a
`usize`, before the callee starts and before anything is written through the `edit`. The
rule rejects it only because the `edit` is considered live from its evaluation, not from
the call.

**Evidence from this week's work:**
- **The kern-written parser.** Spans (`docs/reports/selfhost-spans.md`) needed about 40
  call sites of this shape. They became six helper methods (`span_nn`, `span_tn`,
  `span_nt`, `setpe_n`, `setpe_kid`, `setpe_prev`) that take node indices and read the
  nodes themselves. Diagnostics added more (`fail_found`, `fail_cur`).
- **The library.** `PoolIn.try_grow` (`docs/reports/alloc-containers-more.md`) had to
  hoist `self.slot_align()` and `self.stride()` out of `self.a.alloc(...)` and
  `self.a.free(...)`.
- **The agent suites.** They include conflict tasks (`docs/reports/agent-tasks-v2.md`,
  t05). Whether any of them is this shape has not been checked; that is part of B's cost
  below.

The workaround is always the same mechanical step: bind the read to a local before the
call. A rule that requires it adds noise without catching anything.

## Alternatives

**A. Keep the rule.** Programs keep hoisting reads into locals.
- *For:* no rule change and no suite recalibration.
- *Against:* the noise above, and a diagnostic that reads as a safety finding though it
  isn't one.

**B. Two-phase `edit` arguments.**
- An `edit` argument's access begins *reserved* when the argument is evaluated, and is
  *activated* when the call starts (after every argument is evaluated).
- While reserved, it conflicts only with accesses that are still live at activation.
  `view` accesses that begin and end within a later argument's evaluation are
  permitted: a `Copy` read, or a call returning a value with no access attached.
- An overlapping `edit`, `consume` or `init` during the reservation still conflicts, as
  does any access to an overlapping path that is still live when the call starts, such
  as a `view` argument of the same place.
- Rust's two-phase borrows (`&mut` auto-ref receivers) are the precedent, with the same
  limit: only reads, and only reads that end before activation.

**C. Evaluate the receiver last.** This changes §4's left-to-right evaluation order, which
is observable when the receiver expression has effects (`f().x.m(g())`). Rejected: it
fixes the rule by changing the language's evaluation semantics.

**D. Hoist automatically.** The compiler inserts the locals the programmer would. This is
B by another name, and B states it as a rule the checker and `explain-ownership` can
report directly.

## Invariants under B

- **Nothing written through the reserved access before the call.** Activation is at the
  call, and no argument can write through it, since an overlapping `edit` during the
  reservation still conflicts.
- **Reads that end before the callee runs.** A read permitted during the reservation has
  ended (rule 5.4.3's moment) before activation, so the callee's exclusive `edit` never
  coexists with a live `view`.
- **Evaluation order unchanged.** Arguments are evaluated left to right as today; only
  the start of one access's conflict window moves.

## Hard counterexample (what B must still reject)

```kern
with v := self.items.get(0usize) { self.push_view(v) }        // still rejected: v is live
self.replace_with(self.view_of_field())   // rejected if view_of_field returns an access
self.m(take(self.x))                        // rejected: consume during the reservation
```

## Cost

- **Expressiveness:** strictly more programs are accepted. No accepted program changes
  meaning.
- **Checker:** the argument loop of call checking records `edit` arguments as reserved
  and checks a reserved access only against accesses still live at the call. Roughly a
  day's work, with fixtures for each case above.
- **Suites:** any agent task whose calibration expects `ACC_CONFLICT` for this shape
  would change. Those tasks must be listed and checked before the change. This is the
  owner's call, since the suites' calibration was kept fixed by decision.
- **Docs:** rules 5.4.3 and 5.4.4 change text, the explanations in `explain-ownership`
  follow, and the kern-written parser's helpers can be simplified afterwards.

## Recommendation

B, specified and implemented as its own change: a spec with the rule text, fixtures for
the accepted shapes and for every counterexample above, a pass over the agent suites'
expected diagnostics, and a report. The decision is the owner's, because it changes an
ownership rule and possibly the suites' calibration.

## Decision

Pending.
