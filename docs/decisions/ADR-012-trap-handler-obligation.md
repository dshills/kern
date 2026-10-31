# ADR-012: The trap handler is a documented trusted-boundary obligation, not a runtime-enforced restriction

**Status:** Decided
**Date:** 2026-10-26
**Spec:** §6 (trap semantics, `TrapInfo`, `trap.set_handler`, stack exhaustion); PLAN.md §0
(trap record contract), 2.2 (hosted runtime signal-safety contract).
**Depends on:** ADR-006 (panic is a trap). **Interacts with:** ADR-011 (freestanding halt).

## Problem

A hosted stack-overflow trap is detected by a guard-page fault, so the registered trap
handler can run in signal context on an interrupted thread: the thread may hold a `Mutex`,
be inside the allocator, or be mid-way through buffered output. A handler that allocates,
locks, spawns, performs buffered I/O or calls FFI from that context may deadlock the process
or fault again. The spec (§6) makes `trap.set_handler(f: func(view TrapInfo))` the only
cleanup hook and says nothing about what `f` may do. Either the runtime enforces a safe
subset (a sandboxed handler calling convention, a check at `set_handler`, or a watchdog) or
the obligation is documented and left to the program.

## Alternatives

- **Runtime enforcement**: a flag the allocator, `Mutex.lock`, `spawn` and buffered output
  check and trap on when set during a handler. Rejected: the check adds a load to every
  allocation and lock in the hot path to protect a path that already ends in `abort`, and it
  cannot cover FFI or custom allocators, so it would be incomplete as well as costly.
- **Restrict the handler type** to a function marked as signal-safe by the checker. Rejected
  for Phase 2: it needs an effect system the language does not have, and it would forbid
  ordinary kern code (a loop over `TrapInfo`, a `print_u64`) that is safe in practice.
- **Run the handler on a fresh thread** after a stack trap. Rejected: it changes the trap's
  observable thread identity and still cannot make a held `Mutex` safe to take.
- **Documented obligation (chosen)**: the handler is ordinary kern code under a stated
  contract; the runtime performs no checks for it; violations end in the nested-trap path.

## Decision

A custom trap handler installed with `trap.set_handler` is a **trusted boundary**. It may
run in signal context on an interrupted thread, so it must not allocate, take a lock,
spawn, perform buffered I/O, or call FFI or a custom allocator that does any of these. The
guaranteed-safe operations are unbuffered output through the runtime's `print_*`
functions (which write with `write(2)` from a preallocated buffer), atomics, and computation
on `TrapInfo` and pre-existing data. The obligation applies uniformly to every trap kind, is
stated in `docs/ownership.md` beside `trap.set_handler`, and the runtime performs no checks
for it. A second fault or any trap during the handler restores the default `SIGABRT`
disposition and calls `abort()`, so every hosted trap path ends in death by `SIGABRT`;
memory safety is unaffected by a violation. The default handler satisfies the obligation by
construction: it writes one JSON record with `write(2)` from a per-thread preallocated
buffer and uses no lock, allocation or `stdio` call.

## Consequences

- `tests/trap/handler_*.kern` cover a conforming handler on an overflow trap and a nested
  trap inside a handler; `tests/trap/stack_big_frame.kern` covers a frame larger than one
  page with stack probes so the guard page cannot be skipped.
- Stack traps detected natively carried the span `(0, 0)` until 2026-10-31: the fault
  handler knew no source location. Since then (`specs/native-trap-loc/SPEC.md`) every
  hosted kern function carries a marker the handler reads without allocating or faulting,
  and a native `stack` record, or an `unreachable` one from a fault in safe code, names the
  declaration of the user function the fault happened in (found through the faulting
  instruction, the link register and the frame chain), or `(0, 0)` when none is found. The
  interpreter reports the span of the function whose activation exceeded its budget, which
  can be a different function (mutual recursion, a library frame, code LLVM inlined), so
  the differential harness still compares trap kinds, and spans only for non-`stack` traps
  (recorded in `docs/reports/phase-2.md`).
- Phase 3 freestanding builds use a compiler-inserted stack-limit check in every prologue,
  so the handler never runs in signal context there; the obligation is kept uniform anyway.
