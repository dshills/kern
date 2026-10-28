# kern memory model (spec §5.5, Phase 5 stabilization)

This document restates the concurrency rules of `docs/ownership.md` 5.5.1–5.5.5 as the
language's memory model. It is normative together with those rules; where it is more
precise, it refines them. The model is the ISO C++ 2020 model [iso-cpp20] restricted to the
operations kern exposes.

## 1. Threads and happens-before

- A *thread* is created by `thread.spawn` (detached ownership transfer, rule 5.5.2) or
  `Scope.spawn` (scoped, rule 5.5.3). The start of a thread's function happens after the
  `spawn` call that created it; the `spawn` call happens after every evaluation of its
  arguments, so a thread observes every write its creator made before spawning it.
- `Thread.join` and `Join.get` return after the thread's function returned; every write the
  thread made happens before the join's return. The end of a `with s := thread.scope()`
  block joins every thread the scope spawned, in creation order, before the block exits.
- Within one thread, program order is happens-before (sequenced-before).
- An atomic *release* operation on an object *synchronizes with* an atomic *acquire*
  operation on the same object that reads the value written by the release or by a later
  write in its release sequence (an atomic read-modify-write chain started by the release
  store, as in C++20). `seq_cst` operations are release and acquire operations in one
  single total order consistent with happens-before. `fence(acquire)`, `fence(release)`,
  `fence(acq_rel)` and `fence(seq_cst)` are the C++20 fences with the same names.
- Happens-before is the transitive closure of sequenced-before and synchronizes-with.

## 2. Data races and what safe code can express

- Two accesses *conflict* when they address the same memory and at least one is a write.
  A *data race* is two conflicting non-atomic accesses on different threads not ordered by
  happens-before. A program with a data race has undefined behaviour **only in `unsafe`
  code**: safe kern cannot express one (rule 5.5.1), and the runtime never relies on the
  absence of a race for memory safety of its own structures.
- Why safe code cannot race: within a thread `edit` is exclusive (rule 5.4.4); a value
  crosses to another thread only as an `own` argument of a `Send` type (the source no
  longer has it), as a `view` of a `Share` type (readers only), or as an `edit` argument of
  a scoped spawn (exclusive until the scope joins, rule 5.5.3); mutation shared between
  threads happens only inside `Mutex` (one holder, rule 5.5.4; a `Condvar` wait
  releases and reacquires that one holder's lock) or through `Atomic`
  (rule 5.5.5). `Interior` types are the only values whose `view` admits mutation, and
  every prelude `Interior` type synchronizes internally; a user `Interior` type
  (`unsafe assert Interior`) takes on that obligation (rule 6.7, `docs/unsafe-audit.md`).
- `Shared[T]`'s reference count is an atomic read-modify-write with `acq_rel` on
  decrement, so the last owner's destruction of `T` happens after every other owner's
  last use.

## 3. Atomics

`Atomic[T]` operations map one to one onto C++20 atomic operations with the given
ordering: `load` (relaxed, acquire, seq_cst), `store` (relaxed, release, seq_cst),
`swap` and the `fetch_*` read-modify-writes (every ordering; integer `fetch_*` wrap),
`compare_exchange` (strong) and `compare_exchange_weak` with the permitted failure
orderings of rule 5.5.5. Narrow atomics (`u8`, `u16`, `u32`, `bool`) are single
atomic read-modify-writes, never a wider read followed by a masked write. Volatile MMIO
(rule 6.6) is not an atomic operation and establishes no synchronization.

## 4. Traps and termination

A trap on any thread terminates the process after at most one handler run (ADR-006,
ADR-012); writes of other threads that did not happen before the trap are not observable
afterwards. The interpreter (`kern run`) implements this model with sequentially
consistent atomics (a global lock), which is a valid implementation of every ordering;
the native backend lowers each operation to the LLVM atomic of the same ordering and the
hosted runtime's `pthread` mutexes and condition variables.

## 5. Evidence

- `tests/concurrency/litmus/message_passing.kern` (release/acquire message passing) and
  `store_buffering.kern` (seq_cst store buffering) run 200 trials each, natively and
  interpreted, and under ThreadSanitizer.
- `tests/concurrency/race_raw_ptr.kern` is the deliberate race in `unsafe` code that
  ThreadSanitizer must report (`tsan: race`).
- `tests/negative/{send_not_send,share_not_share,live_across_spawn}.kern` and their twins are
  the static rules.

Rule numbers of `docs/ownership.md` are frozen from this version (`spec/rules.lock`,
checked by `tests/rules_lock_test.go`).
