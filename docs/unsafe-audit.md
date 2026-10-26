# Unsafe audit (PLAN.md 3.4)

Every `unsafe` block and `unsafe assert` in the standard library, the benchmark
programs and the executable fixtures, with the contract that covers it and the
run that exercises its failure path. Rule 6.4 requires a contract block on every
function that contains `unsafe`; `UNSAFE_MISSING_CONTRACT` enforces it
(`tests/negative/unsafe_missing_contract.kern`) and `UNSAFE_OP_OUTSIDE_UNSAFE`
rejects every enumerated operation outside a block
(`tests/negative/unsafe_op_outside_unsafe.kern`,
`tests/negative/packed_access_outside_unsafe.kern`).

## Standard library (`std/core.kern`)

The prelude contains no `unsafe` blocks: every library operation is a
bodyless intrinsic implemented by the runtime (C, `runtime/hosted/src/kern_rt.c`)
and by the interpreter. Its unsafe surface is the marker assertions:

| Declaration | Obligation | Exercised by |
|---|---|---|
| `unsafe assert Interior for Atomic` | every `Atomic` operation through a `view` is an atomic read-modify-write with the stated ordering (runtime `kern_atomic_*`, interpreter `atomicMu`) | `tests/concurrency/atomic_counter.kern` (two threads, 10,000 `fetch_add`), the litmus fixtures under `tests/concurrency/litmus/` |
| `unsafe assert Interior for Mutex` | `lock` is runtime-exclusive escalation (Rule 5.3.6): one holder at a time, recursive acquisition traps `explicit` (`PTHREAD_MUTEX_ERRORCHECK` natively, the owner thread identifier in the interpreter) | `tests/concurrency/mutex_counter.kern`, `tests/native_test.go` (`TestNativeStackTrapUnderMutexWithHandler`) |
| `unsafe assert Interior for Shared` | clones and drops update the count atomically (compare-exchange loop natively); overflow traps `overflow` | `tests/trap/overflow.kern` family (`testing.seed_shared_count`), `tests/concurrency/shared_handoff.kern` |

## Benchmarks (`bench/kern/`)

`bench/kern/UNSAFE.md` is the per-block inventory written with the programs;
in summary:

| Program | Blocks | Contracts | Failure path |
|---|---|---|---|
| `intrusive_run_queue.kern` | 13 (field address arithmetic over a raw `Task` array, link manipulation, allocation and release) | `requires` on every accessor ("p addresses a live Task in the task array"), `invariant` on the list structure for `insert_sorted`/`pop_head`/`walk` | a violated invariant exits 1 before printing; `Err(OutOfMemory)` exits 2; any wrong offset changes the checksum compared with `bench/c` |
| `lock_free_queue.kern`, `lock_free_queue_fail.kern` | 2 (the slot write in `push`, the slot read in `pop`) plus `unsafe assert Interior for Spsc` and `unsafe assert Share for Spsc` | invariants stating the release/acquire protocol that makes the slot access data-race-free | `_fail`: 1024 pushes on a full ring return `false` before the block; every consumer spin is a `pop` on an empty ring returning `None` |
| every other workload | none | — | — |

## Executable fixtures

| Fixture | Operation | Contract | Failure path |
|---|---|---|---|
| `tests/run/raw_ptr.kern` | allocator, `ptr_store`/`ptr_load`/`ptr_offset`/`ptr_cast`/`ptr_addr`/`ptr_null` | "the allocator obligations of §6 hold for every call" | the interpreter traps `explicit` on an unmapped address or a bad `free`; the allocator obligations trap `explicit` natively and interpreted (`kern_sysalloc_alloc`) |
| `tests/run/raw_take.kern` | `ptr_store` of a `Vec`, `ptr_take` back | "the slot holds a stored Vec before ptr_take" | a double take would hand out a dead handle: covered by the ownership rules of the surrounding safe code (the value is moved into raw memory once) |
| `tests/run/packed.kern` | unaligned field access, raw stores of a packed struct | "unaligned access is supported by the target" | `show-layout` and `compiler/layout/layout_c_test.go` pin the packed layout |
| `tests/run/asm_nop.kern` | `asm("nop")` | "the template is valid for the build target" | native-only; the interpreter traps `explicit` |
| `tests/freestanding/mmio.kern` | `ptr_store_volatile` to the console device | "0x0900_0000 is the console device of the target machine" | the emulation maps the address; natively the UART register |
| `tests/ffi/{scalars,pointers}.kern` | imported `extern "C"` calls, raw buffer handed to C | "the C counterpart is linked", "p addresses n writable bytes owned by the caller" | the C side never frees; `c_sum` over the filled range is checked |
| `tests/concurrency/race_raw_ptr.kern` | `unsafe assert Send for Slot`, racing `ptr_store` | "the data race is intentional" | the ThreadSanitizer run must report it (`tsan: race`) |
| `tests/negative/interior_write_unsafe.kern` | write through a `view` of an `Interior` type in its module | invariant "hits is written only while lock is 1" | the twin without `unsafe` is `ASSIGN_IMMUTABLE` |

## Rules the audit relies on

- Rule 6.4: the enumerated operations and the contract obligation.
- Rule 6.6: raw pointers and the allocator obligations.
- Rule 6.7: packed layout, volatile access, inline assembly, Interior escalation.
- ADR-012: trap handlers are trusted boundaries, not enforced.
