# ADR-011: Which freestanding runtime facilities are mandatory

**Status:** Open (closes in Phase 3, PLAN.md 3.1)
**Date:** 2026-10-26
**Spec:** §2.1 (freestanding targets), §3 (principle 7), §6 (runtime, stack exhaustion,
trap semantics, allocator interface, OOM); PLAN.md §0 (`--env freestanding`, trap record
contract), 2.3 (TLS), 3.1 (freestanding mode, halt protocol, console device), 3.4.
**Depends on:** ADR-006 (trap = halt), ADR-008 (checks stay on).

## Problem

Spec §6 requires a freestanding mode with no OS assumptions, no mandatory GC or scheduler,
and a runtime small enough for embedded and kernel targets (§3 principle 7). It names the
facilities a runtime must define (startup, trap behavior, TLS, stack checks, OOM handling,
allocation APIs) but leaves open which of them a freestanding image must provide, which the
compiler inserts, and which the user supplies. The answer determines what a freestanding
program can call, what symbols the linker demands, and what `--env freestanding` means for
the fixtures and the three freestanding workloads (page-frame allocator, ring buffer,
intrusive run queue).

## Alternatives

- **Rust `#![no_std]` with `core`, a required `#[panic_handler]`, optional `alloc` behind a
  `#[global_allocator]`** [rust-reference] [rustonomicon]. The closest model: a required
  panic path, no default allocator, allocation as an opt-in. kern differs in that the trap
  handler has a no-op default rather than being required.
- **Zig freestanding target: no libc, overridable `panic`, explicit allocators everywhere,
  `std.start` optional** [zig-ref]. Consistent with "no default allocator"; Zig's choice to
  make the entry point and panic handler overridable is the leading option's shape.
- **Hare freestanding builds (`-Ffreestanding`, no `rt`)** [hare-spec]. Evidence that a
  minimal systems language can ship with a replaceable runtime.
- **Ada runtime profiles (zero-footprint, Ravenscar)** [ada-rm]. Informative: profiles that
  enumerate which runtime facilities exist are an alternative to a single minimum; rejected
  for now as more configuration than Phase 3 needs.
- **C and C++ freestanding implementations** [iso-cpp20]: the standard enumerates the
  headers a freestanding implementation must provide. Informative for the enumeration
  style; kern's list is of runtime symbols, not library surface.
- **Mandatory default allocator in freestanding mode.** Rejected by spec §6: the freestanding
  mode provides no `SystemAllocator`; containers take `own A` where `A: Allocator`.
- **Mandatory threads or TLS in freestanding mode.** Threads are a non-goal until a target
  needs them (PLAN.md 3.3 key decisions); freestanding TLS is decided here (PLAN.md 2.3).

## Invariants

Fixed by the spec and plan regardless of how this record closes:

1. `--env freestanding` has no OS assumptions and no `SystemAllocator`; `Box.new` and the
   other convenience constructors have no allocator to trap on, so freestanding code
   allocates only through an `Allocator` it supplies (§6).
2. A trap halts the machine after the registered handler runs at most once; the default
   freestanding handler is a no-op (§6, ADR-006). The halt path writes
   `{"halt": {"code": n}}` to the console device and powers off (PLAN.md 3.1).
3. Stack exhaustion is a trap of kind `stack` via a compiler-inserted stack-limit check in
   every function prologue, disabled only by an explicit unsafe build option that documents
   stack overflow as a trusted-boundary obligation (§6).
4. All checks of ADR-008 remain on; `--opt` and `--env` are orthogonal (PLAN.md §0).
5. Freestanding fixtures run under QEMU (`virt` PL011 UART on AArch64, 16550 on x86-64) and
   under the interpreter's freestanding emulation, differentially (PLAN.md 3.1).
6. PLAN.md 3.1 names the minimum: trap entry, the stack-limit symbol, and `TrapInfo`
   construction.

## Hard counterexample

A kernel entry point that assumes hosted facilities. Each marked line is a facility this
record must either make mandatory, make user-supplied, or reject:

```text
module kernel
import "std/mmio"

struct Frames { bitmap: [4096]u8 }

func kmain() {
    var frames: Frames                       // 4 KiB frame: prologue stack-limit check needs the limit symbol (mandatory)
    init_frames(out frames)
    b := Box.new(Frames { bitmap: zeros() }) // no SystemAllocator in --env freestanding: not available (user supplies own A)
    trap.set_handler(uart_trap)              // handler registration: mandatory (ADR-006); default is a no-op
    buf := Vec[u8].new(BumpAlloc.new(heap_start(), heap_len()))   // allocation through a user allocator: available
    t := thread.spawn(idle, ())              // threads: not provided in freestanding mode
    counter := thread_local_counter()        // TLS: open; decided by this record
    trap()                                   // halts; writes the halt line to the console device if one is configured
}
```

A user allocator over a linker-provided range is the sanctioned replacement for `Box.new`;
the console device and the halt line are a fixture convention, not a language facility.

## Expressiveness cost

Freestanding programs cannot use the convenience constructors or any container without
passing an allocator, cannot spawn threads, and cannot rely on the default handler to report
a trap (they install one that writes to their device). Depending on the outcome, they may
also lack thread-local declarations. None of these limit hosted programs.

## Performance cost

The stack-limit check is one compare-and-branch per function prologue, measured on the three
freestanding workload rows (PLAN.md 2.6 matrix extended in Phase 3). Everything else is
either absent or user-supplied, so the runtime's text footprint is the trap entry, the
`TrapInfo` builder, and the halt path; the benchmark report records text-section bytes per
configuration.

## Complexity

The candidate list is short, and each item is a symbol the linker either finds or reports:
trap entry and `TrapInfo` construction; the stack-limit symbol; the handler slot; the halt
path; whether the memory-copy and memory-set helpers that the backend may emit for large
aggregates count as runtime facilities the image must provide; and TLS. The risk is not in
any item but in leaving one undocumented so that a freestanding link fails with an
unexplained undefined symbol.

## Experiments

- PLAN.md 3.1: `kern build --env freestanding` for x86-64 (`q35`) and AArch64 (`virt`);
  freestanding fixtures booting, trapping, and halting under QEMU with identical output
  from the interpreter's emulation; `stack` traps firing in freestanding builds; the unsafe
  build option that disables the prologue check.
- PLAN.md 3.1 closing experiment: link each freestanding fixture and the three freestanding
  workloads against an empty runtime and record the set of undefined symbols; that set,
  minus what the user program supplies, is the mandatory list.
- PLAN.md 2.3: the hosted TLS fixture; the freestanding TLS decision recorded here.
- PLAN.md 3.4 and 2.6: page-frame allocator, ring buffer, and intrusive run queue
  benchmark rows under `--env freestanding`, with text-section bytes and the prologue-check
  cost reported.
- PLAN.md 3.1 acceptance criterion 5 and `docs/reports/phase-3.md`: the closing entry.

## Conclusion

Leading option: the mandatory freestanding runtime is exactly the PLAN.md 3.1 minimum (trap
entry, the stack-limit symbol, `TrapInfo` construction) plus the handler slot and halt path
that ADR-006 already requires; no default allocator, no threads, and TLS provided only if
the undefined-symbol experiment shows a workload needs it. The record closes in Phase 3 when
the freestanding fixtures and the three freestanding workloads build and run under QEMU with
only that symbol set provided by the runtime, and `docs/reports/phase-3.md` records the
final list.
