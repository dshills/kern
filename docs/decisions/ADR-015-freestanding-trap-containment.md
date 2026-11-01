# ADR-015: Containing a trap to one task in freestanding mode

**Status:** Accepted (2026-11-01): option A. Option D is deferred, not rejected, until a
concrete freestanding program needs it; B and C are rejected. Nothing is implemented;
ADR-006 and ADR-012 stand unchanged.
**Date:** 2026-11-01
**Spec:** §6 (trap semantics, `trap.set_handler`, freestanding halt); `docs/ownership.md`
"Trap handlers" and rule 6.8.
**Depends on:** ADR-006 (panic is a trap), ADR-012 (the handler obligation). **Interacts
with:** ADR-011 (freestanding runtime), `specs/alloc-containers` (`BoxIn`, `VecIn`),
`specs/fallible-alloc` and `specs/effect-trap-kinds` (allocation failure as a value, and
the compiler showing which kinds a function can trap).

## Problem

A reader of the kern article argued that "stop and report it precisely" rules kern out of
an operating-system kernel, whose job is to stay up. The precise form of the objection is
about granularity, not about stopping: a kernel's response to a detected bug in one task
(an "oops") is to report it, kill that task, and keep the machine running. kern's
freestanding mode halts the whole machine on any trap (rule 6.8), and a trap handler must
end in termination (ADR-012). This record weighs letting a freestanding handler abandon the
current unit of work and return to a scheduler instead.

Two parts of the objection are already answered and are not this record's subject:
allocation failure is an error value through every allocating operation
(`specs/fallible-alloc`, `specs/alloc-containers`, `Map.try_insert`), and the compiler
reports which trap kinds a function can raise (`show-effects` `trap_kinds`), so kernel-style
code can be checked to have no `oom` path. What remains is the bugs the language detects at
run time: an out-of-bounds index, an overflow, a stale handle, an `explicit` check.

The kernel practice the objection appeals to is weaker than it sounds. Linux survives an oops
by killing the current task, but then marks itself tainted ("kernel died recently")
[linux-tainted], and its `panic_on_oops` setting turns every oops into a halt
[linux-sysctl-kernel], which deployments that will not trust a kernel after a detected bug
choose. Halting with a precise report is the conservative end of the policy kernels already
offer, not a position outside it.

## What a trap guarantees today

ADR-006's consequence is the reason this is hard: "no landing pads, no exception-safety
obligations in unsafe code, static destructor points". Every `unsafe` block in the prelude
(71 of them, `docs/unsafe-audit.md`) may leave its container's private invariant broken for
the instant between two raw operations, because a trap there ends the program and nothing
observes the broken state. `Vec.push` writes the element and then the length; `Pool.free`
updates the slot's metadata and then the free list; `Mutex` holds its owner word until
`unlock`. Resuming any code after a trap re-exposes those intermediate states.

## Options

**A. Keep the halt (ADR-006 unchanged).** Freestanding programs that need to survive bugs
run each component as its own image, under a supervisor outside kern (a hypervisor, a
separate core with a watchdog). Nothing in the language changes. Cost: kern itself cannot
host a kernel-style "kill the task, keep going" policy.

**B. Abandon to a recovery point.** `trap.set_recovery(f)` (freestanding only) names a
function the handler may transfer to instead of halting: the trapping task's frames are
discarded without running destructors or release functions, and `f` runs on a fresh stack
(a scheduler loop). Mechanically this is a `longjmp`-style switch, which kern's IR already
permits, since a trap edge is a terminating edge and nothing after it in the abandoned frames
runs. What it breaks:
- Every value the abandoned frames owned is leaked (no destructor runs). Memory-safe, but
  unbounded over many traps.
- Every lock the task held stays held; another task waiting on it deadlocks.
- Every object outside the task that the task was editing is left in whatever state the
  trap interrupted, including a prelude container with its private invariant broken
  mid-update. Using that container afterwards can be memory-unsafe, not just wrong. This is
  the cost ADR-006 avoided.

**C. Catch and return an error (`trap.contain(f) -> Result[T, TrapInfo]`).** A
`catch_unwind` analogue [rust-reference]. It needs either unwinding, which ADR-006 rejects
(landing pads, two strategies for unsafe code [rustonomicon]), or option B's frame discard
with the same breakage, and makes a trap a control-flow construct in all code rather than a
supervisor's last resort.

**D. B, restricted to a task that shares nothing (an isolated task).** A task is started
with everything it touches moved into it: its data, its own allocator (`BoxIn`/`VecIn` over
a per-task region or budget), and channels or atomics as its only connection to other
tasks; it is not given a view of any object another task uses, and holds no `Mutex` another
task can take (enforced by requiring the task's entry function to take only `own`
parameters whose types are `Send` and contain no `Shared` or `Mutex`). When it traps, the
handler abandons its frames as in B, and the scheduler reclaims its region whole (resetting
the region allocator), so nothing is leaked and nothing outside the task was mid-update. The
broken container invariants are inside the discarded region, which nothing reads again.
What it costs: a typed task boundary in the library (a `task.spawn_isolated` and the check
on its entry function), a region allocator that can be reset, a stack per task, and a
recovery entry in the freestanding runtime; and it covers only the bugs of isolated tasks,
not of code that shares state, which still halts.

## Recommendation

Keep A as the default and do not adopt B or C: each makes every `unsafe` block in the
library carry exception-safety obligations it was designed without, and C reintroduces the
control flow ADR-006 removed.

If the owner wants kernel-style containment, D is the version that keeps ADR-006's
guarantee where it matters: a trap still ends every frame that can observe broken state,
because those frames and that state belong to one task whose region is discarded whole. It
is a library and runtime change (an isolated-task type, a resettable region allocator, a
recovery entry), not a language change, and builds directly on `BoxIn`/`VecIn`. The work
would need its own spec.

## Decision

**A.** A trap in a freestanding image halts it, as rule 6.8 says; a system that must survive
a component's bug runs that component as its own image under a supervisor outside kern.
B and C are rejected for the reasons above.

**D is deferred.** Its guarantee depends on pieces the freestanding runtime does not have: a
stack per task, a scheduler to return to, a region allocator that can be reset, and a trap
handler and scheduler that cannot themselves trap. Designing those without a program that
needs them would fix their shape by guesswork, so D's spec is written when a concrete
freestanding consumer exists (for example a small scheduler under `examples/`), and is
designed against it.

**A condition D must add.** As written above, D lets isolated tasks connect through channels
and atomics. An atomic operation is indivisible, so a trap cannot interrupt it halfway. A
channel operation is not: a task that traps inside a send leaves the channel's shared state
mid-update, outside the region that gets discarded, which is the broken invariant D exists
to avoid. D's spec must therefore require every operation that crosses a task boundary to
raise no trap at all, checked by the compiler: the operation's `trap_kinds` in
`show-effects` must be empty (`specs/effect-trap-kinds`), the same check that shows code has
no `oom` path.

## Consequences

- Now: no change to the language, runtime or library. The article's answer is that a kern
  freestanding image is one isolation unit, supervision is outside it, and allocation
  failure and the compiler's trap-kind report already cover the parts of the objection that
  do not need resumption.
- If D is later specified and adopted: ADR-006 gains a freestanding amendment ("a trap in an
  isolated task ends that task; its region is reclaimed"), ADR-012's handler obligation
  gains the recovery case, rule 6.8's "traps halt" becomes "traps halt, or end the isolated
  task they occur in", and cross-task operations carry the no-trap requirement above.
