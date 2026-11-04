# Two-phase `edit` arguments

**Date:** 2026-11-04. **Spec:** `specs/two-phase-edit/SPEC.md` v0.2
(speccritic VALID_WITH_GAPS, no critical finding). **Plan:**
`specs/two-phase-edit/PLAN.md`. **Decision:** ADR-016, alternative B,
accepted by the owner on 2026-11-04.

## The change

An `edit` argument written as a *field path* (a name followed only by
field selections) is now *two-phase*:
- it is *reserved* while the call's other arguments are evaluated;
- it becomes *active* when the call starts.

While reserved, it does not conflict with a `view` that ends before
then. So these calls are accepted:

```kern
self.setp(n, self.pstart(x), self.pend(y))
self.a.alloc(self.st.bytes() + self.a.used)
v.push(v.get(0usize))
store_be[u32](buf, 0usize, load_be[u32](buf, 4usize))
```

Everything else conflicts as before:
- an `edit`, `consume`, `take` or `replace` of the place in a later
  argument;
- a `view` argument of it, which is live for the call;
- a live binding of it;
- a closure that captures it.

Only field paths qualify. A projection receiver (`v.at(0usize).set(..)`)
or an indexed place (`xs[i].set(..)`) keeps the old rule. Evaluating a
projection runs code, and it can carry a release:
`m.lock().set(m.lock().get())` would take a second lock while holding
the first. A lock guard read in an argument (`s.put(s.m.lock())`) stays
rejected, because a lock's result is an `edit` projection. A `view self`
method that locks and unlocks inside (`s.put(s.peek() + 1u64)`) is
accepted.

## How

- **Checker** (`compiler/check`).
  - An `edit` argument whose expression is a field path, and whose
    result is a place, is marked reserved. Its *active* tick is the one
    that ends the call's argument accesses.
  - In `finish`, a conflicting pair made of a reserved access and a
    `view` that ended before that tick is skipped. A binding's freeze is
    not a `view`, so it still conflicts.
  - The skipped argument is recorded in `Info.TwoPhase`.
- **Lowering** (`compiler/lower`). A recorded argument's `access_begin`
  is emitted after the other arguments and before the `call`.
  - The byte-order stores have their own lowering path (`wideAccess`).
    Their last-byte check still reads the length before the value
    argument is evaluated, now from the place itself.
  - A recorded argument that does not lower to a plain place stops the
    lowering with a named internal error. This would mean the checker
    and the lowering disagree about eligibility.
  - Nothing else moves. The IR validator's open-access check is
    unchanged; it now sees each `view` end before the `edit` opens.
- **The interpreter and the backend** follow the IR and need no change.

Only a call that the old rule rejected gets a record. So a program the
baseline accepts lowers to exactly the IR it did before.

## Fixtures

- `tests/run/two_phase_edit.kern` runs each accepted shape and prints
  what every callee received. Each value matches what the arguments held
  before the call. The shapes:
  - a method's own reads;
  - a sibling field;
  - a Copy field;
  - a projection's Copy read;
  - a plain function;
  - an `edit` parameter;
  - a `with` binding;
  - the byte-order stores;
  - two disjoint fields.

  The interpreter, debug native and release native transcripts are equal.
  The baseline rejects the program with ten `ACC_CONFLICT`s.
- `tests/negative/two_phase_write`, `two_phase_replace`,
  `two_phase_projection` and `two_phase_index` stay rejected (5.4.4).
  `two_phase_ok` is their hoisted twin, and the baseline rejects it.
- `tests/negative/conflict_edit_view` was `v.push(v.get(0usize))`, which
  is now accepted. It is rewritten to `both(v, v)`, with `edit` then
  `view` parameters, and keeps its code and twin.
- The fixtures for live bindings and closures are unchanged and still
  rejected: `conflict_enclosing`, `interior_pointer_growth`,
  `pool_get2_reject`, `closure_conflict`, `map_each_conflict`,
  `user_each_conflict`.
- `compiler/lower/twophase_test.go` checks where the edit access
  begins:
  - after the argument, for a recorded argument;
  - at its own evaluation, for an unrecorded one.

  With the deferral disabled, the test fails.

## No accepted program changes (R4)

`emit-ir` from the baseline (ac43f72) and from this change was compared
for every program the baseline accepts under `tests/`, `bench/kern/` and
`examples/`, at `--opt=debug` and `--opt=release`:
- 347 single-file programs and 14 multi-module programs (each checked
  from its own directory);
- 722 comparisons in all, all byte-identical.

The files left out were library sources, programs that are rejected by
design, and the two new fixtures.

## The agent suites (R5)

`kern check` was run on all 120 `.kern` files under `bench/agent/tasks*/`
(starting and reference programs), comparing the exit status and each
diagnostic's code and span. No file differs.

The two conflict tasks are v1 t04 and v2 t05.
- **v1 t04** conflicts through a live `with` binding, which still
  conflicts.
- **v2 t05**'s starting program is first rejected for
  `PARAM_OWN_REQUIRED`. Its conflict is also through a binding.

No task's calibration moves.

## Uses simplified

The workarounds the old rule forced are gone:
- **The kern-written parser** (`examples/selfhost_parser/syntax`). The
  six span helpers (`span_nn`, `span_tn`, `span_nt`, `setpe_n`,
  `setpe_kid`, `setpe_prev`) are removed, and their 51 call sites now
  call `setp` or `setpe` with the reads inline:
  `self.setp(n, self.pstart(a), self.pend(b))`. Seven more hoisted reads
  were inlined (`set2(tn, self.ns(b), self.ne(b))`,
  `mk(N_TNAME, self.ns(x), self.ne(x))`). Reads of methods that take
  `edit self` (`expr_to_type`, `list`, `cur`) stay hoisted: an edit in a
  later argument still conflicts. `TestSelfhostSpans`,
  `TestSelfhostParser` and `TestSelfhostDiags` pass unchanged.
- **The prelude** (`PoolIn.try_grow` and `PoolIn.release`, in both
  mirrors): `self.a.alloc(bytes, self.slot_align())` and the matching
  `free` calls read `slot_align()` and `stride()` inline.

## What remains

- Two-phase projections are not attempted (see above).
