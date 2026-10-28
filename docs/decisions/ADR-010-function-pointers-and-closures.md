# ADR-010: Function pointers now; non-escaping closures later; no escaping closures

**Status:** Decided (Phase 1, 2026-10-26): capture-free function pointers now; non-escaping closures deferred; escaping closures are a non-goal. **Amended 2026-10-28:** the deferred option is implemented as `closure(P...) -> R` parameter types and `|x| expr` closure expressions (spec §4, `docs/reports/closures.md`); invariants 4 and 5 hold as written
**Date:** 2026-10-26
**Spec:** §2.2 (escaping closures are a non-goal), §4 (function values, closures deferred),
§5.5 (`spawn` takes a function and owned arguments), §5.8 (callbacks retaining references),
§16 (`show-effects` through function pointers); `spec/types.md` §2 (function types);
evaluation §2 (P6), §4.9, §8 example 4.
**Depends on:** ADR-001, ADR-007, ADR-009.

## Problem

Evaluation P6: deferring closures while requiring a thread pool, callbacks and scoped
threads is a contradiction unless the core states what a function value is. A closure that
captures an access is an escaping reference (ADR-001 forbids it); a closure that captures by
move is an owned aggregate plus a function; a non-escaping closure is a second-class value.
The record must fix what exists now, what is deferred, and what is excluded, and name the
Phase 1 evidence that closes it.

## Alternatives

- **Rust closures (`Fn`/`FnMut`/`FnOnce`, capture by reference or `move`, `Box<dyn Fn>`)**
  [rust-reference]. Rejected for the core: capture by reference is an escaping borrow that
  needs a lifetime when stored; boxed closures are dynamic dispatch (ADR-009).
- **Second-class functions (Osvald et al.)** [osvald-2016] [osvald-2017]. The paper's
  motivating example is exactly a closure that may not escape its stack frame. This is the
  deferred option: non-escaping closures as second-class values under the §5.3 rules,
  passable only as arguments, never stored or returned.
- **Hylo lambdas with explicit capture lists** [hylo-tour]. Informative for the deferred
  design (captures are conventions on the closure's hidden environment).
- **Zig and Hare: no closures; function pointers plus a context argument** [zig-ref]
  [hare-spec]. Adopted now: a `(function pointer, own context)` pair is the callback and
  thread-pool pattern (evaluation §8 example 4).
- **Swift closures: non-escaping by default, `@escaping` opt-in** [swift-se-0446]. The
  default matches the deferred option; the opt-in is the non-goal.
- **Go closures (garbage collected environments).** Rejected: a heap-allocated environment
  with no owner is the case that requires a collector.

## Invariants

As decided so far (spec §4, `spec/types.md` §2) and to be confirmed in Phase 1:

1. Function types `func(P...) -> R` are first-class `Copy` values that capture nothing. A
   function name used as a value has its declared function type; a generic function must be
   instantiated (`f[T]`) before use as a value.
2. Callback and thread-pool patterns use a function pointer paired with an owned context
   value; `thread.spawn(f, own args...)` and `Scope.spawn` take a function and arguments,
   never a closure.
3. A call through a function pointer makes every `show-effects` effect `true` with label
   `unknown`.
4. When closures are added, non-escaping closures are second-class values under the rules of
   §5.3 and introduce no new ownership machinery; they may be passed as arguments and may
   capture accesses only for the duration of the call.
5. Escaping closures are a non-goal (§2.2): no closure is stored, returned, captured by
   another value, or live across a thread spawn or a suspension point.

## Hard counterexample

The callback that retains a reference, which is not expressible and is listed in §5.8 for
that reason, and its sanctioned rewrite:

```text
func register(cb: func(view[Event]), own ctx: Box[Watcher]) { ... }

func watch_bad(log: edit[Vec[Event]]) {
    // Not writable: the grammar has no closure form. If it had one, a closure capturing `log`
    // and stored by `register` would be an access escaping into a struct (ACC_ESCAPE_STORE).
    // register(func(e: view[Event]) { log.push(copy(e)) })
}

struct Watcher { log: Vec[Event] }

func on_event(w: edit[Watcher], e: view[Event]) { w.log.push(copy(e)) }

func watch(own w: Watcher) {
    register_with(on_event, Box.new(w))              // (function pointer, own context): register owns the context
}
```

The case the deferred option would improve is the comparator that needs a table it does not
own, which today threads the table as an explicit argument:

```text
func less_by_key(keys: view[]u64, a: u32, b: u32) -> bool { return keys[a] < keys[b] }
func sort_by(v: edit[]u32, keys: view[]u64, less: func(view[]u64, u32, u32) -> bool) { ... }
// sort_by(items, keys, less_by_key)   today;   sort_by(items, |a, b| keys[a] < keys[b])   if non-escaping closures are added
```

## Expressiveness cost

No capturing callbacks: every callback is a function and an owned context, so an API that
takes a callback must also take and own the context. Iterator-adaptor styles (`map`,
`filter`, `for_each` with a captured accumulator) are not writable; loops are. A comparator
or visitor that needs environment takes it as an explicit parameter. Thread-pool jobs are
`(func, own Box[...])` pairs. These costs are the ones the Phase 1 workload notes must weigh
against the deferred option.

## Performance cost

A call through a function pointer is an indirect call and blocks inlining and effect
analysis (`unknown` label). No allocation is implied by a function value; the context is an
ordinary owned value whose allocation `show-allocations` reports. Non-escaping closures, if
added, would be stack-allocated environments passed by convention with no allocation.

## Complexity

Now: none beyond a `Copy` function type and the instantiation-before-use rule. Deferred:
non-escaping closures require a closure expression in the grammar, capture classification
(by `view`, `edit`, or move) under the §5.4 conflict rule, and the escape checks of §5.2
extended to closure values; evaluation §4.9 argues no new ownership machinery is needed.
Excluded: escaping closures would need lifetimes, which is the whole of what ADR-001 removes.

## Experiments

- PLAN.md 0.5: `examples/thread_handoff/handoff.kern` written with `(func, own context)`
  pairs against the `thread.spawn` and `thread.scope()` stubs.
- PLAN.md 0.6: the workload notes for thread pool, buffer pool, lock-protected map, and
  zero-copy packet parser must state whether the `(func, own context)` rewrite sufficed or
  a closure would have been required; this count is the input that closes the record.
- PLAN.md 1.2 and 1.4: Phase 1 accept fixtures for function values (function name as value,
  generic instantiation before use, call through a `func` field of a struct), and the
  `Box.new_with(init: func(out T) -> Result[(), E])` construction path (ADR-005).
- PLAN.md Phase 1 key decisions and `docs/reports/phase-1.md`: the closing entry, recording
  that function pointers are first-class `Copy` and that closures are not in Phase 1.
- PLAN.md 4.1: `show-effects` with the `unknown` label for calls through function pointers.

## Conclusion

Decided in Phase 1: plain capture-free function pointers plus owned context values. The Phase 1
report records the function-value fixtures passing (callback_retains_view_ok, thread_handoff,
hello) and the Phase 0 workload notes show that no §8 workload needed a closure for
correctness or for the 10% target (thread_pool uses `(func, own Box[Task])` jobs). The
record was:  The record closes in Phase 1 when the Phase 1 report
records (a) the function-value fixtures passing and (b) the workload notes showing that no
§8 workload needed a closure for correctness or for the 10% target. If a note shows the
`(func, own context)` rewrite fails for a workload, the next step is non-escaping,
second-class closures per Osvald et al., scheduled no earlier than Phase 2. Escaping
closures remain excluded regardless of that outcome.
