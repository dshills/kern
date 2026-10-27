# Atomic operations as LLVM instructions

**Date:** 2026-10-27. **Builds on:** the containers rewrite
(`docs/reports/containers-in-kern.md`) and the throughput work
(`docs/reports/throughput-gaps.md`). **Starting point:** `bench/REPORT.md` at
`4b62a7c`: lock_free_queue at 0.39 of C, lock_protected_map at 0.66 and
thread_pool at 0.73, the three lowest rows after packet_parser.

## Problem

`Atomic[T]` was an opaque runtime object: `Atomic.new` allocated a 16-byte
cell on the heap (never freed), and every `load`, `store`, `swap`,
`fetch_*` and `compare_exchange` was a call into `kern_atomic_*` in
`runtime/hosted/src/kern_rt.c`, which took the element size and the memory
ordering as run-time arguments and switched on them; narrow widths were
emulated on a 64-bit word with a compare-exchange loop. The ordering is a
keyword at every call site (the checker requires a constant, spec §5.5), so
none of that dispatch was necessary, and LLVM could neither inline nor
reorder around the calls. It was the same out-of-line pattern that held the
containers back.

## Changes

- **Inline cell.** `Atomic[T]` is no longer in `layout.Opaque`: it is the
  struct of its one field, laid out and aligned as `T`, so a field
  `head: Atomic[u64]` is an inline aligned word and the padding in a
  struct like the benchmark's `Spsc` separates the cells as intended.
  `Atomic.new` is an `insertvalue`; there is nothing to free.
- **Instructions.** `compiler/backend/llvm/calls.go` emits, on the cell's
  address and in `T`'s in-memory type (`bool` as `i8`):
  `load atomic` / `store atomic` for `load`/`store`, `atomicrmw xchg`
  for `swap`, `atomicrmw add|sub|and|or|xor` for the `fetch_*` family,
  `cmpxchg` (with `weak` for `compare_exchange_weak`) whose `{T, i1}`
  result builds the `Result[T, T]`, and `fence` for `fence`. The ordering
  comes from the payload-free `Ordering` literal the lowering passes (the
  emitter records the tag of every such literal): relaxed → `monotonic`,
  and acquire, release, acq_rel, seq_cst by name. The checker's rules on
  failure orderings are exactly LLVM's, so no clamping is needed.
- **Runtime.** `kern_atomic_new/load/store/swap/rmw/cmpxchg` and
  `kern_fence` are deleted from the hosted runtime (48 lines), and
  `kern_fence` from the freestanding one. Freestanding programs now have
  atomics and `fence` with nothing from the runtime (ADR-011 amended;
  `tests/freestanding/atomic.kern` boots on both targets).
- **Interpreter.** An `Atomic` is a `Struct` of its cell updated under
  `atomicMu`. A cell in raw memory (a container element, a boxed value) is
  decoded afresh by every read of its access, so each update is written
  back through the access (Prism caught the first version mutating the
  decoded copy; `tests/run/atomic_in_vec.kern` exercises a
  `Vec[Atomic[u64]]` and a `Box[Atomic[u32]]` in both engines).
  `compare_exchange_weak` was not registered at all (an interpreter run of
  a program using it failed) and now shares the strong implementation,
  which the spec permits.
- **Analysis.** `Atomic.new` is no longer reported by `show-allocations`.
- **Packed structs.** Prism (gpt-6.1-sol) pointed out that an `Atomic`
  field of a `packed` struct would now sit at an unaligned offset while the
  instructions promise natural alignment. `TYPE_PACKED_ATOMIC` (rule 4.1.5,
  spec §4.1, `docs/ownership.md`) rejects a packed struct whose fields hold
  an `Atomic` inline, at the declaration and at every generic
  instantiation written as a type, including a non-packed wrapper around a
  packed generic, a packed generic carried in an inline `Result` or
  behind a `Box` (`tests/negative/packed_atomic*.kern`; Prism's later
  rounds added those reaches, the bound against expanding recursive
  instantiations and the sibling-instantiation case). An instantiation that exists only through
  inference is not seen by the resolver; the backend then refuses to emit
  an atomic operation on an access it opened inside a packed struct, a
  build error rather than an unaligned instruction.
- **Fixtures.** `tests/run/atomic_widths.kern` covers every width and
  `bool`, wrapping `fetch_add`/`fetch_sub`, the strong and weak exchange and
  `fence`; the interpreter and the native build agree, and the LLVM text
  has no runtime call. The concurrency and litmus fixtures pass under
  ThreadSanitizer as before.

## The benchmark finding

The first run after the change made lock_free_queue 3.5× *slower*
(stand-alone, same host, same checksum: 90 ms → 310 ms per 8,000,000
elements). The push and pop assembly was textbook (`ldr`, `ldapur`, `str`,
`stlr`), so the cause was the dynamics, and bench/c's header names it: a
single-producer single-consumer ring that publishes its index on every
element falls into a lock-step regime where each release store stalls on
the line the other thread polls, 20-40× slower on Apple silicon.
bench/CHECKSUMS.md specifies the B-Queue batching the C and Rust versions
use (each side keeps its index in a private `pos` and publishes it with a
release store every 64 elements and before waiting); the kern version had
published every element, and the call overhead of the old atomics had been
hiding the lock step. `bench/kern/lock_free_queue/lock_free_queue.kern` and
its fail twin now implement the specified protocol (`Cursor { pos, cache }`,
`publish_tail`/`publish_head`), with the contracts and `bench/kern/UNSAFE.md`
updated; the checksums of both runs are unchanged. Stand-alone, the same
binary then runs 8,000,000 elements in 12-20 ms.

## Results

`go run ./bench/harness`, five runs per row, release rows against the better
of C and Rust (`bench/REPORT.md`; load average at start 3.27). Checksums
agree on all thirteen workloads.

| workload | before (Mops/s) | after | best (lang) | ratio before → after | throughput target |
|---|---|---|---|---|---|
| lock_free_queue | 194.5 | 632.3 | 790.3 (c) | 0.39 → 0.80 | no (ahead of Rust, 456.4) |
| thread_pool | 20.5 | 19.5 | 27.8 (c) | 0.73 → 0.70 | no |
| lock_protected_map | 12.0 | 18.5 | 26.8 (rust) | 0.66 → 0.69 | no |

The other ten rows are unchanged code; buffer_pool read 0.93 (meets) and
page_frame_allocator 0.90 (does not), both inside the ranges they have
shown across every run, so the count of workloads meeting the throughput
target stays at three and RSS at eleven. thread_pool and lock_protected_map
did not move: their synchronization is `Mutex`, a `pthread_mutex_t` behind
`kern_mutex_lock`/`kern_mutex_unlock`, and their atomics were never on the
hot path. The remaining lock_free_queue gap to C (0.80) is in the
benchmark plumbing around the ring (the sampler and the checksum fold per
pop), not in the atomics.

`go test ./...` and `golangci-lint run ./...` pass. Prism (OpenAI
gpt-6.1-sol, staged diff) took eight rounds: the packed-struct alignment
above, five refinements of how far that check reaches through generic
instantiations, and the interpreter write-back; the last round is clean.

## Follow-ups

- `Mutex.lock`/`unlock` are still out-of-line calls into the C runtime
  around a `pthread_mutex_t`; the C and Rust versions pay the same, so the
  thread_pool and lock_protected_map gaps need their own diagnosis.
- `Shared` keeps its count in a heap cell updated by the runtime; its clone
  and drop could be the same inline `atomicrmw` operations.
