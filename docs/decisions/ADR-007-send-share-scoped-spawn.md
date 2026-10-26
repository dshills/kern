# ADR-007: Derived `Send`/`Share`/`Interior` markers; scoped spawn as the only cross-thread borrow; thread results must be `Send`

**Status:** Decided
**Date:** 2026-10-26
**Spec:** §5.2 (`ACC_LIVE_ACROSS_SPAWN`), §5.3 (runtime-exclusive escalation), §5.4
(`Interior`, aliasing guarantees), §5.5; `spec/types.md` §3; evaluation §3 (concurrency
column), §4.7, §5 (concurrent region access).
**Depends on:** ADR-001, ADR-004. **Interacts with:** ADR-010 (spawn takes a function
pointer and owned arguments).

## Problem

The safety contract requires data-race freedom in safe code without a garbage collector and
without lifetime annotations. Three things cross threads: owned values (transfer), read-only
access to shared data (sharing), and exclusive mutation of a caller's data by a worker
(scoped mutation). Rust answers with the `Send`/`Sync` auto traits and a `'scope` lifetime
on scoped threads; Pony with six reference capabilities. kern must answer with derived
markers and no lifetime, and must say what the single cross-thread borrow is.

## Alternatives

- **Rust `Send`/`Sync` auto traits and `std::thread::scope`** [rust-reference]
  [rustonomicon]. The markers are adopted in substance (structural derivation, unsafe opt-in).
  The `'scope` lifetime is rejected: kern hard-codes the relation to the `with s` block so
  there is no lifetime variable, and there is no user trait system behind the markers.
- **Pony reference capabilities (`iso`, `trn`, `ref`, `val`, `box`, `tag`)** [pony-caps-2015].
  Rejected: six capabilities with transition rules are high semantic complexity and the model
  is garbage collected. `iso` (unique, sendable) and `val` (immutable, shareable) survive as
  `Send` and `Share`.
- **Austral: linear values are `Send` for free** [austral-spec]. Consistent with transfer by
  move; it offers no sharing or scoped-mutation story and is not sufficient alone.
- **Unrestricted first-class borrows across threads.** Rejected by ADR-001: an access live in
  another thread is an escaping reference and needs a lifetime.
- **Message passing with copied data only** (no sharing, no scoped mutation). Rejected: the
  parallel-sum and thread-pool workloads (spec §8) need views of one buffer from several
  threads and exclusive slices from one.

## Invariants

1. `Send` (may move to another thread) and `Share` (a `view` may be read concurrently) are
   derived structurally: primitives and function types are both; an aggregate is `Send`
   (`Share`) iff every field is; `RawPtr` is neither; `Interior` types are `Share` only by
   `unsafe assert Share`. `unsafe assert Send/Share/Interior for T` are top-level
   declarations permitted only in `T`'s module, with documented obligations.
2. `thread.spawn(f: func(own args...) -> R, own args...) -> Result[Thread[R], SpawnErr]`:
   every argument type and `R` must be `Send` (`SEND_NOT_SEND`). On `Err` the arguments are
   destroyed and no thread exists. Destroying a `Thread[R]` detaches it.
3. Scoped sharing: `with s := thread.scope() { ... s.spawn(f, args...) ... }`. `view`
   arguments require `Share` (`SHARE_NOT_SHARE`); `edit` and `own` arguments require `Send`;
   `R` must be `Send`. Every access passed to `s.spawn` is live until the end of the
   `with s` block, which joins every thread it spawned (also when a later `s.spawn` fails);
   each such access must be rooted in a variable or binding whose scope encloses the block
   (`ACC_LIVE_ACROSS_SPAWN`). This is the only cross-thread borrow in the language.
4. `Join[R]` is `Send`; `get(own self) -> R` blocks; results are stored or destroyed per the
   §5.5 rules. `Shared[T]` is `Share` iff `T: Share`, `Send` iff `T: Send` and `T: Share`;
   `Mutex[T]` is `Send` and `Share` iff `T: Send`.
5. Data-race freedom: within a thread `edit` is exclusive (§5.4); across threads only `Share`
   values are viewed and only `Send` values are moved; cross-thread mutation happens only
   through an exclusive scoped `edit` argument, inside `Mutex`, or through atomics under the
   ISO/IEC 14882:2020 memory model [iso-cpp20].

## Hard counterexample

Three rejected programs and the accepted scoped form:

```text
struct Dev { regs: RawPtr[u32] }                           // RawPtr is not Send

func start(own d: Dev) -> Result[Thread[()], SpawnErr] {
    return thread.spawn(poll, d)                           // SEND_NOT_SEND: argument type Dev
}

func par_sum(data: view[]u64, n: usize) -> Result[u64, SpawnErr] {
    with s := thread.scope() {
        var scratch: [64]u64 = zeros()
        j := s.spawn(fill, scratch)?                       // ACC_LIVE_ACROSS_SPAWN: scratch is rooted inside the block
        c := Counter { hits: 0u64 }                         // Counter: unsafe assert Interior, not Share
        s.spawn(count, c)?                                 // SHARE_NOT_SHARE: a view of a non-Share Interior type
        return Ok(0u64)
    }
}
```

```text
func par_sum(data: view[]u64, n: usize) -> Result[u64, SpawnErr] {
    with s := thread.scope() {
        j0 := s.spawn(sum_part, data[0:n])?                // view: u64 is Share; live until the block ends
        j1 := s.spawn(sum_part, data[n:2*n])?
        return Ok(j0.get() + j1.get())                     // Join[u64].get consumes the join; the block joins the rest
    }
}
```

Rust writes the accepted form with `thread::scope(|s| s.spawn(|| ...))` and a closure
capturing `data` under `'scope`; kern has no closure and no lifetime, only the block.

## Expressiveness cost

No detached thread may borrow anything; a worker that outlives the block owns its data.
Thread results must be `Send`, so a thread cannot return a thread-affine handle. There is no
user-defined marker trait: a type with interior mutability is `Share` only through an
`unsafe assert` in its own module. `Interior` propagates to every containing aggregate, so a
single atomic field removes the `noalias readonly` guarantee for views of the whole value.
No async: no access may be live across a future suspension point (§5.8).

## Performance cost

Markers cost nothing at runtime. A scope join at block exit is the same cost as Rust's. The
measurable cost is the aliasing attribute difference: views of `Interior` types lower with no
attribute, which the codegen experiment the spec marks unproven measures. `Shared[T]` is an atomic
reference count; `Mutex` is a lock; both are library types over unsafe internals.

## Complexity

Structural derivation is one pass over field types, the same pass that derives `Copy`. The
checker needs one additional rule for the block-long extension of accesses passed to
`s.spawn`, which reuses the enclosing-scope conflict machinery of §5.4. The runtime side
(OS threads, join-all destructor, `SpawnErr` paths) is confined to `std/thread` and arrives
in Phase 3.

## Experiments

- PLAN.md 0.3 and 0.4: `thread.spawn`/`Scope.spawn` stub signatures checked in Phase 0;
  reject fixtures `SEND_NOT_SEND`, `SHARE_NOT_SHARE`, `ACC_LIVE_ACROSS_SPAWN`, each with an
  accepting twin tagged `phase: 3`; adversarial fixture "concurrent container access".
- PLAN.md 0.5: `examples/thread_handoff/handoff.kern` using `thread.spawn` and
  `thread.scope()`.
- PLAN.md 1.2: single-threaded stand-ins that enforce the §5.5 rules dynamically.
- PLAN.md 3.3: OS-thread-backed `spawn` and `Scope.spawn`, `SpawnErr` paths (owned arguments
  destroyed, accesses released), `Join[R]` result ownership, `Mutex.lock` escalation,
  `Shared` overflow trap, `Atomic` with `ATOMIC_INVALID_ORDERING`; concurrency fixtures under
  ThreadSanitizer with deliberate-race twins that TSan must catch; the litmus-test suite
  under `tests/concurrency/litmus/`.
- PLAN.md 2.6: the codegen experiment comparing `Interior` and non-`Interior` views.
- PLAN.md 3.4: thread pool, lock-protected map, and lock-free queue workloads.

## Conclusion

`Send`, `Share` and `Interior` are derived marker properties with unsafe opt-in; transfer is
by move with `Send` arguments and results; the `with s := thread.scope()` block is the one
cross-thread borrow, hard-coded to the block; mutation across threads happens only through a
scoped `edit`, a `Mutex`, or atomics.
