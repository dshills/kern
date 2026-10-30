# Allocation sites through library calls

**Version:** 0.1 (2026-10-30). **Scope:** the allocation analysis
behind `kern show-allocations` (`compiler/analysis`); its §16 note in
`compiler/diag/schema.go` and the generated `tools/agent-api/schema.md`;
fixtures and tests; the report. **Language impact:** none. The §16
result shape is unchanged; which call sites it lists changes.
**Related:** `specs/initial/AI_Native_Systems_Language_SPEC.md` §3
principle 2 ("make heap allocation … explicit or compiler-inspectable")
and §16 (`show-allocations`, `show-effects`); `docs/reports/agent-tasks-v4.md`
("Defects found on the way"), where the gap was found.

**Review status.** speccritic (OpenAI gpt-6.1-sol, profile general):
VALID_WITH_GAPS, no critical finding; its one substantive warning (R3
stated a function-level property as equivalent to the per-call one) is
folded in. Implementing it showed the table also reports constructors
that allocate nothing (`Vec.new`, `Pool.new`, `Arena.new`) as proven
allocations; R1 was rewritten to correct the table and R8 added, and the
revision was gated again: VALID_WITH_GAPS, no critical finding, only
the preflight's prose warnings.

**Glossary.** R*n*: requirement *n* of §3. Site: one entry of
`show-allocations`. User function: an IR function that is neither
library code (`ir.Func.Library`) nor `Inlined`. Library function: an IR
function with `Library` set: a body from the core prelude or a `std/`
module. Allocation table: the analysis's fixed map from allocating
operations (intrinsics and the core containers' methods by their
unspecialized name) to whether the allocation is unconditional
(`compiler/analysis/analysis.go`, `allocating`). Effect summary: the
`allocates` flag `show-effects` computes for a function, transitively
over statically resolved callees. Table site: a site the analysis
reports today. Library site: a site this change adds (R2).

## 0. Goals and non-goals

Goal: a call from user code into library code is a site exactly when
the callee may allocate, so that `show-allocations` and `show-effects`
agree: an allocation hidden in a `std/str`, `std/map` or `Vec.extend`
call is visible, and a constructor that allocates nothing is not
reported as an allocation.

Non-goals: sites inside library functions (the library's own
allocations stay attributed to the user-level call, as today); a size
for a library site; telling a call that allocates on every path from
one that allocates on some; changing `show-effects`; changing the
benchmark report's recorded numbers, which change at its next run.

## 1. The defect

Measured on 2026-10-30 at b49589f with this program, which checks
cleanly:

```kern
func only_str() -> usize { s := str.String.new(); s.push_u64(42u64); return s.len() }
func only_map() -> usize { m := map.Map[u64, u64].new(); m.insert(3u64, 4u64); return m.len() }
func only_extend(v: edit[Vec[u64]], xs: view[]u64) { v.extend(xs) }
```

`show-effects` reports all three functions as `allocates: true`. Their
allocations reach the core allocator through `push_u64`, `Map.new`,
`insert` and `Vec.extend`. `show-allocations` reports no site in any
of them, because the table lists none of those names.

The table is also wrong the other way. Since the containers became kern
code, `Vec.new`, `Pool.new` and `Arena.new` build an empty container
with capacity 0 and allocate nothing (their effect summaries say
`allocates: false`), yet the table reports each call as an
unconditional, `proven` allocation with a size. `Vec.with_capacity(n)`
allocates only when `n` is above 0, yet the table calls it
unconditional. `String.new`, which wraps `Vec.new`, allocates nothing
either, and is rightly not a site.

## 2. Definition

A call instruction in a user function is a site when either:

1. **Table site.** It is an intrinsic in the allocation table, or a deep
   `clone` of a non-`Copy` value, exactly as today; or a call to a
   library function whose unspecialized name is in the table **and whose
   effect summary allocates**. The table's library entries are corrected
   (R1); or
2. **Library site (new).** It is a statically resolved call to a
   library function that is not covered by 1 and whose effect summary
   has `allocates: true`.

Calls through a function value are not sites (their effects are
`unknown`), and calls to user functions are not sites (the callee's own
calls are reported where they occur), both as today.

## 3. Requirements

**R1 — The table corrected.** The intrinsic entries (`Shared.new`,
`Mutex.new`, `Condvar.new`, `thread.scope`, `thread.spawn`,
`scope.spawn`, the `SystemAllocator` operations, `clone`) are unchanged.
Among the library entries, `Box.new` stays unconditional, and
`Vec.push`, `Pool.alloc` and `Arena.alloc` stay conditional.
`Vec.with_capacity` becomes conditional (label `unknown`, size `null`).
`Vec.new`, `Pool.new` and `Arena.new` are removed: they allocate
nothing. Every other site the analysis reports at b49589f is still
reported with the same `loc`, `allocator`, `size` and `label`.

**R8 — The table cannot drift.** A test instantiates every library entry
of the table in a program and fails when an entry's callee has an
effect summary that does not allocate, so a future container change
that makes a constructor lazy, or a renamed method, is caught.

**R2 — Library sites.** Each library site of §2 item 2 is one entry
with `loc` the call's location in the user function, `allocator`
`"system"`, `size` `null` and `label` `unknown`: the callee may or may
not allocate on a given call. A location is reported once (the existing
deduplication).

**R3 — Agreement with effects.** For every user function *F* of a
program, if *F* contains a statically resolved call to a library
function whose effect summary allocates, `show-allocations` reports a
site at that call. A consequence, checked separately: every user
function whose `show-effects` entry allocates has at least one site of
its own, or calls a user function that allocates, or calls through a
function value, or calls an `extern` function.

**R4 — Both opt levels.** R1 to R3 hold for `--opt=debug` and
`--opt=release`. In release, a library call the release inliner has
copied into a user function is not a call any more; its copied
instructions are attributed by the existing rules. The inliner does not
inline library bodies today, so no case of this arises.

**R5 — The §16 note.** The schema's note for `show-allocations` states
the two kinds of site and that a library site is `unknown` with a
`null` size. `tools/agent-api/schema.md` is regenerated from it
(`go generate ./compiler/diag`), and the existing staleness test
passes.

**R6 — Agent suites.** The dry runs of every suite with allocation
budgets (`bench/agent/tasks-v1`, `bench/agent/tasks-v2`) pass. Where a
reference's count rises above its budget, the budget is raised to the
reference's new count, since a budget is the reference's measure. Where
a start's count falls to its budget or below, so that the start would
pass, the budget is lowered to the reference's new count when that
still separates them; otherwise the report states that the task no
longer measures what it was built for. Every change of budget is
listed in the report with its old and new value. The recorded matrix runs are not re-scored: they
were judged by the analysis as it was.

**R7 — Benchmark programs.** The report lists, for every program under
`bench/kern/` and `examples/`, its site count before and after this
change, in debug. It does not re-run the timed harness.

## 4. Acceptance criteria

1. A test on §1's program: `push_u64`, `Map.new`, `insert` and
   `extend` are sites labelled `unknown` with a `null` size;
   `String.new()` and `Vec[u64].new()` are not sites; a `Box.new` call
   keeps its proven table site with its size; and a function that only
   sums a view has none.
2. A test over every `tests/run/*.kern` program that checks cleanly, in
   both opt levels: the table sites before this change (computed by the
   old table rule), less those R1 removes or relabels, are a subset of
   the sites after, with equal fields (R1); every statically resolved call from a user function to
   a library function whose summary allocates is a site (R3); and R3's
   consequence holds for every user function.
3. `go generate ./compiler/diag` leaves no diff after the note change,
   and the schema tests pass (R5).
4. The two dry runs exit 0 (R6), and the report has R7's table.
5. The R8 test passes, and fails when `Vec.new` is put back in the
   table (checked once by hand while writing it).
