# ADR-011: Which freestanding runtime facilities are mandatory

**Status:** Decided (Phase 3, PLAN.md 3.1)
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

## Decision (Phase 3 closing entry, 2026-10-26)

The mandatory freestanding runtime is the symbol set that `runtime/freestanding/src/rt.c`
and the startup code provide, and nothing else:

| Symbol | Role |
|---|---|
| `_start` (startup code per architecture) | sets the stack, writes `kern_stack_limit`, calls `kern_start`, `kern_main`, `kern_exit` |
| `kern_stack_limit` (data) | the lowest valid stack address; every function prologue compares the stack pointer against it and traps with kind `stack` (the compiler-inserted check, disabled only by `--unsafe-no-stack-check`) |
| `kern_trap` | trap entry: builds `TrapInfo`, runs the registered handler at most once, writes the record to the console when no handler is installed, halts with code 134 |
| `kern_set_handler`, `kern_set_file` | the `trap.set_handler` exchange and the program name for records |
| `kern_start`, `kern_exit` | startup hook (empty) and the halt protocol: `{"halt": {"code": n}}` on the console, then PSCI `SYSTEM_OFF` (AArch64) or `isa-debug-exit` (x86-64) |
| `print_u64`, `print_i64`, `print_bool`, `print_str`, `println` | console output through the device (`0x0900_0000` PL011 on `virt`, port `0x3F8` on `q35`) |
| `kern_fence` | `fence` lowered to a compiler fence |
| `memcpy`, `memmove`, `memset` | required by LLVM for aggregate moves |
| `trunc`, `truncf` | the float-to-integer conversion's `llvm.trunc`, a libm call on baseline x86-64 (added 2026-10-27 when the q35 images first ran) |
| `kern_sysalloc_new`, `kern_sysalloc_alloc`, `kern_sysalloc_realloc`, `kern_sysalloc_free` | the `SystemAllocator` behind the library containers: a first-fit free list over a static 1 MiB arena (`KERN_HEAP_SIZE`) in `.bss`, with the hosted signatures and the §6 obligations (alignment a power of two at most 4096, size a multiple of it, `realloc` keeps the common prefix and leaves the old block on failure, `free` with one allocation's values); exhaustion returns `Err(OutOfMemory)`, never a trap (added 2026-10-27, amendment below) |
| `kern_next_identity` | the container identity tag counter (spec §5.6): from 1 in debug builds, 0 in a release build (`-DKERN_RELEASE`), as on the hosts (added 2026-10-27) |

No threads, no TLS (none of the three freestanding workloads needs it). The interpreter's
`--env freestanding` emulation implements exactly this contract. The undefined-symbol experiment
was run by building `tests/freestanding/*.kern` against the empty runtime: the set above is what
the images demand. QEMU execution is wired in the harness and ran nowhere in this phase (no
`ld.lld` or QEMU on the development host, `docs/reports/phase-3.md`).

## Amendment (2026-10-27): the system allocator and the library containers

The Phase 3 decision provided no allocator: the containers were C code in the hosted runtime,
and a freestanding program allocated only through an `Allocator` it supplied. Once `Box`, `Vec`,
`Pool` and `Arena` became kern code over the raw-pointer core (`docs/reports/containers-in-kern.md`),
the only thing between a freestanding program and the containers was `SystemAllocator`, which
the containers call through `heap_alloc`/`heap_realloc`/`heap_free`. The freestanding runtime
now provides it, so freestanding programs have the containers natively and the interpreter's
emulation no longer traps `explicit` on `SystemAllocator.new`.

- **Storage.** A static array of `KERN_HEAP_SIZE` (1 MiB) bytes, 4096-aligned, in `.bss`;
  both linker scripts (`virt.ld`, `q35.ld`) place `.bss` after the image, so no script changed
  and both targets link. The arena is not in the image file (`NOBITS`); the loaders zero it.
- **Algorithm.** A first-fit free list sorted by address. Block boundaries are multiples of 16
  bytes, so a free block always has room for its header (size and next); an allocated block
  has no header, since `free` and `realloc` receive the allocation's size and alignment (the
  §6 obligation). A release coalesces with both neighbours; `realloc` shrinks and grows in
  place when the bytes after the block are free (a `Vec` growing alone in the arena never
  copies), otherwise takes a new block, copies the common prefix and releases the old one, and
  on failure leaves the old block. Every allocation is 16-byte aligned at least, as on the hosts.
- **Failure.** Exhaustion returns `Err(OutOfMemory)`; the convenience constructors (`Box.new`,
  `Vec.push`) then trap `oom` as in hosted mode. A request that violates the obligations
  (alignment, size, or a pointer the arena does not hold) traps with kind `explicit`.
- **Emulation.** The interpreter's freestanding emulation serves `SystemAllocator` from its
  raw heap exactly as hosted mode does, so a program's console transcript is the same in the
  emulation and under QEMU on both targets (`tests/freestanding/containers.kern`). The
  emulation's heap is not bounded by `KERN_HEAP_SIZE`: only the point of exhaustion differs.
- **Alignment on AArch64.** The images run with the MMU off, where every access is to Device
  memory and must be aligned to its size; LLVM's vector widening of struct copies (a 16-byte
  store to an 8-aligned `[N]Vec[T]` element) raised alignment faults as soon as containers were
  stored in arrays. AArch64 freestanding builds now pass `-mstrict-align`, so compiled code
  emits only naturally aligned accesses; x86-64 has no such requirement.
- **Rejected: a bounded emulation heap.** Making the interpreter fail at the same point as the
  arena would still not agree with it (fragmentation differs), and a program that depends on
  the exhaustion point is relying on the arena size, which is a build-time constant to change.
