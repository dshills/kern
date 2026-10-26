# ADR-006: Panic is a trap; no unwinding, no destructor cleanup, single trap-handler hook

**Status:** Decided
**Date:** 2026-10-26
**Spec:** §4.1 (traps), §5.3 (release on trap), §5.7 (trap inside a destructor), §6 (trap
semantics, `TrapInfo`, `trap.set_handler`), §7 (IR invariant 1, trap paths); evaluation §2
(P9), §4.10; PLAN.md §0 (trap record contract), 2.2 (ADR-012 handler obligation).
**Depends on:** ADR-003. **Interacts with:** ADR-004 (release), ADR-011 (freestanding halt).

## Problem

Evaluation P9: unwinding forces every destructor-bearing scope to carry landing pads, makes
every `unsafe` block reason about exception safety, and turns destructor call sites into
runtime questions. With errors as values (`Result[T, E]` and `?`) there is no need for
recoverable panics. The record must fix what a trap does, what runs before termination, and
what the single hook for external-resource cleanup is, in both hosted and freestanding modes.

## Alternatives

- **Rust unwinding with `panic = "abort"` as an option** [rust-reference] [rustonomicon].
  Rejected: two panic strategies double the obligations of unsafe code (the Rustonomicon's
  exception-safety chapter exists because of unwinding), and `catch_unwind` makes a panic a
  control-flow construct.
- **C++ exceptions with RAII cleanup** [iso-cpp20]. Rejected: landing pads, exception
  tables, and dynamic destructor execution; every destructor becomes a potential throw site.
- **Zig: no unwinding, error unions, overridable panic handler** [zig-ref]. Adopted in
  substance: a trap terminates, errors are values, and one handler may be installed.
- **Hare: abort on assertion failure, errors as tagged unions** [hare-spec]. Same shape;
  consistent with the choice.
- **Ada exceptions / SPARK proof of absence** [ada-rm] [spark-rm]. Rejected: Ada's
  exceptions are unwinding; SPARK's answer (prove run-time errors absent) is a program-level
  discipline, not a runtime design, and ADR-008 keeps checks on instead.
- **Evaluation's hosted-cleanup option** ("in hosted builds a trap may run pending
  destructors before `abort`", evaluation §4.10 and P9). Rejected in spec v0.2 §6: running
  destructors after a trap re-enters code whose invariants the trap just proved broken, and
  it makes hosted and freestanding behavior differ. The trap handler is the only hook.

## Invariants

1. A trap runs no destructors and no release functions, unwinds no frames, and cannot be
   caught or resumed. No safe construct observes a trap and continues (§4.1).
2. The runtime invokes the registered handler `func(view TrapInfo)` at most once on the
   trapping thread (a trap inside the handler terminates immediately), then terminates: by
   halting in freestanding mode, by aborting the process in hosted mode.
3. `trap.set_handler(f) -> func(view TrapInfo)` is a single `seq_cst` atomic exchange
   returning the previous handler; a trapping thread selects its handler by one `seq_cst`
   load, so concurrent replacements are totally ordered and each trap runs exactly one
   handler. The default handler writes `TrapInfo` to standard error (hosted) or is a no-op
   (freestanding).
4. `TrapInfo` carries the kind (`bounds`, `overflow`, `div_zero`, `shift`, `conversion`,
   `generation`, `vacant`, `identity`, `disjoint`, `oom`, `stack`, `explicit`,
   `unreachable`), the source location, and the thread identifier.
5. IR invariant 1: trap paths terminate and carry no `drop` obligations; destructor points
   are static (ADR-003); no landing pads exist in the backend.
6. The custom handler runs under a documented trusted-boundary obligation, not a runtime
   check (ADR-012, PLAN.md 2.2): it may run in signal context, so it must not allocate, lock,
   spawn, perform buffered I/O, or call FFI that does any of these.

## Hard counterexample

A program that relies on a destructor or a release function to run after a failure. Under
C++ or Rust-with-unwinding the flush and the unlock happen; under kern they do not:

```text
struct Wal { file: File }
impl Wal {
    destructor(edit self) { self.file.flush() }          // never runs on a trap
}

func append(w: edit[Wal], m: edit[Mutex[u64]], rec: view[]u8, n: usize, d: u64) {
    with g := m.lock() {                                  // release unlock is pending
        g = g / d                                         // d == 0: trap `div_zero`; unlock never runs
        w.file.write(rec[0:n])                            // n > len(rec): trap `bounds`; flush never runs
    }
}
```

The sanctioned form moves external-resource cleanup into the one hook, written to the
ADR-012 obligation:

```text
func on_trap(info: view[TrapInfo]) {
    raw_write_marker(info)                                // unbuffered write only; no lock, no allocation
}

func main() {
    prev := trap.set_handler(on_trap)                     // process-wide atomic exchange
    ...
}
```

Every trap fixture is a sentinel for exactly this: its destructors, release functions, and
the statement after the trapping one each write a distinctive line, and the expected output
asserts none of them appears (PLAN.md §0).

## Expressiveness cost

No recoverable panics: a library cannot contain a failure of its callee, a test harness
cannot catch a trap in-process (the fixture harness runs subprocesses), and a trap on one
thread terminates the whole process rather than that thread. Cleanup of external resources
after a trap is possible only in the handler and only with the restricted operations of
ADR-012. Everything a program is expected to handle must be a `Result`.

## Performance cost

Negative: no landing pads, no exception tables, no unwind metadata, smaller text sections
(reported per spec §2.3), and unsafe code with no exception-safety obligations. Trap checks
themselves (ADR-008) are a compare and a branch to a terminating block; the handler lookup is
one atomic load on the trap path only.

## Complexity

The runtime's trap machinery is one atomic pointer, one `TrapInfo` record, and a
termination call. Hosted stack-overflow detection needs a guard page, an alternate signal
stack, and an async-signal-safe default handler (PLAN.md 2.2), which is the only intricate
part and is confined to the runtime. The language and IR gain nothing: trap edges are
terminating edges.

## Experiments

- PLAN.md 1.2 and 1.4: trap fixtures for every kind (`bounds`, `overflow` including minimum
  signed value by `-1` and `Shared` count overflow, `div_zero`, `shift`, `conversion`,
  `generation`, `vacant`, `identity`, `disjoint`, `oom`, `stack`, `explicit`,
  `unreachable`), each with a completing twin; sentinel lines proving no destructor,
  release, or following statement runs; interpreter exit status 134; `trap.set_handler`
  exchange semantics.
- PLAN.md 2.2: hosted runtime subprocess tests: default and custom handlers, a conforming
  handler after a `stack` trap taken while holding a `Mutex` and mid buffered write, a
  non-conforming handler demonstrating the obligation, nested trap inside a handler (one
  record, death by `SIGABRT`), overflow during allocation, concurrent `set_handler`; stack
  probes for frames larger than a page.
- PLAN.md 3.1: freestanding halt protocol (`{"halt": {"code": n}}` after the trap record)
  under QEMU and the interpreter's emulation; `std/testing.console_trap_handler`.
- PLAN.md 4.1: `show-effects` `traps` flag with the `proven`/`unknown` labels.

## Conclusion

A panic is a trap: terminate after one handler invocation, run nothing else. Errors are
values. The trap handler is the only cleanup hook, and its obligations are documented, not
enforced (ADR-012, Phase 2). The hosted-cleanup option from the evaluation is not adopted.
