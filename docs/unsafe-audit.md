# Unsafe audit (PLAN.md 3.4)

Every `unsafe` block and `unsafe assert` in the standard library (the kern
containers), the benchmark programs and the executable fixtures, with the
contract that covers it and the run that exercises its failure path. Rule 6.4 requires a contract block on every
function that contains `unsafe`; `UNSAFE_MISSING_CONTRACT` enforces it
(`tests/negative/unsafe_missing_contract.kern`) and `UNSAFE_OP_OUTSIDE_UNSAFE`
rejects every enumerated operation outside a block
(`tests/negative/unsafe_op_outside_unsafe.kern`,
`tests/negative/packed_access_outside_unsafe.kern`).

## Standard library (`std/core.kern`)

The library containers (`Box`, `Vec`, `Pool`, `Arena`) are kern code over the
raw-pointer core (spec §6, Rule 6.6): every raw operation sits in an `unsafe`
block inside a method that carries a contract, and the rest of each container
is ordinary checked kern. The destructors only call the contracted `release`
methods (a destructor cannot carry a contract). The allocation helpers
`heap_alloc`, `heap_try_alloc`, `heap_realloc`, `heap_try_realloc` and
`heap_free` wrap `SystemAllocator`, whose §6 obligations (alignment a power
of two at most 4096, size a multiple of it, `realloc`/`free` with the values
of one live allocation) they satisfy by construction: every
request is `n * size_of[T]()` bytes at `align_of[T]()`, and `elems_bytes`
reports an unrepresentable product as `None` (`oom`, or `Err(OutOfMemory)`
from `Pool.alloc`/`Arena.alloc`). Fields of a nocopy core struct are private
to the prelude (`NAME_PRIVATE`), so the invariants named in the contracts
cannot be broken from safe user code.

Returning an access opened on raw memory (`ptr_view`, `ptr_edit`,
`ptr_view_seq`, `ptr_edit_seq`, `ptr_edit2`) is permitted only from inside
`unsafe` and is covered by the function's contract in place of the elision
rule (Rule 6.6); the resolver classifies these intrinsics as unsafe
operations like every `ptr_*` function.

33 functions, 33 unsafe blocks. Every block is exercised by `tests/run/containers.kern` (nested vectors,
pool and arena elements with destructors, `Box.into`, `copy` of a vector,
`pop`, `clear`), `tests/run/pool_handles.kern`, `tests/run/hello.kern`, the
examples and the container benchmarks; the failure paths are the trap
fixtures `tests/trap/{bounds,vacant,generation,identity,disjoint,oom}.kern`
(the span reported is the user-level call that entered the library, as the
runtime's C containers reported it) and the `_fail` benchmark twins. The
interpreter runs the same kern code over its byte-addressed heap, so the two
executions of every fixture compare the containers' behaviour directly.

| Function | Blocks | Raw operations | Contract |
|---|---|---|---|
| `heap_alloc` | 1 | `heap_alloc`, `heap_try_alloc`, `ptr_addr` | requires "align is a power of two at most 4096 and size is a multiple of it" |
| `heap_try_alloc` | 1 | `a.alloc`, `heap_try_alloc`, `ptr_null` | requires "align is a power of two at most 4096 and size is a multiple of it" |
| `heap_realloc` | 1 | `ptr_addr` | requires "p, old_size and align are those of one live allocation; new_size is a multiple of align" |
| `heap_try_realloc` | 1 | `ptr_null` | requires "p, old_size and align are those of one live allocation; new_size is a multiple of align" |
| `heap_free` | 1 | `a.free`, `heap_free` | requires "p, size and align are those of one live allocation" |
| `Box.new` | 1 | `heap_alloc`, `ptr_cast`, `ptr_store` | requires "the allocation holds one T until into or the destructor takes it" |
| `Box.get` | 1 | `ptr_addr`, `ptr_view` | requires "p is null or addresses the live T owned by this Box" |
| `Box.at` | 1 | `ptr_addr`, `ptr_edit` | requires "p is null or addresses the live T owned by this Box" |
| `Box.into` | 1 | `heap_free`, `ptr_addr`, `ptr_cast`, `ptr_take` | requires "p is null or addresses the live T owned by this Box; the Box is consumed" |
| `Box.release` | 1 | `heap_free`, `ptr_addr`, `ptr_cast`, `ptr_take` | requires "p is null (after into) or addresses the live T owned by this Box" |
| `Vec.reserve` | 1 | `heap_alloc`, `ptr_cast` | requires "data addresses cap elements of which the first size are live" |
| `Vec.push` | 1 | `ptr_offset`, `ptr_store` | requires "data addresses cap elements of which the first size are live" |
| `Vec.pop` | 1 | `ptr_offset`, `ptr_take` | requires "data addresses cap elements of which the first size are live" |
| `Vec.get` | 1 | `ptr_offset`, `ptr_view` | requires "data addresses cap elements of which the first size are live" |
| `Vec.at` | 1 | `ptr_edit`, `ptr_offset` | requires "data addresses cap elements of which the first size are live" |
| `Vec.as_view` | 1 | `ptr_view_seq` | requires "data addresses cap elements of which the first size are live" |
| `Vec.as_edit` | 1 | `ptr_edit_seq` | requires "data addresses cap elements of which the first size are live" |
| `Vec.clear` | 1 | `ptr_offset`, `ptr_take` | requires "data addresses cap elements of which the first size are live" |
| `Vec.release` | 1 | `heap_free`, `ptr_cast` | requires "data addresses cap elements of which the first size are live" |
| `Pool.grow` | 1 | `heap_free`, `heap_try_alloc`, `ptr_addr`, `ptr_cast`, `ptr_copy` | requires "the three buffers hold cap entries; the first count slots are initialized" |
| `Pool.alloc` | 1 | `ptr_load`, `ptr_offset`, `ptr_store` | requires "the three buffers hold cap entries; the first count slots are initialized" |
| `Pool.check` | 1 | `ptr_load`, `ptr_offset` | requires "the three buffers hold cap entries; the first count slots are initialized" |
| `Pool.get` | 1 | `ptr_offset`, `ptr_view` | requires "data addresses cap slots; slot idx holds a live element after check" |
| `Pool.at` | 1 | `ptr_edit`, `ptr_offset` | requires "data addresses cap slots; slot idx holds a live element after check" |
| `Pool.get2` | 1 | `ptr_edit2`, `ptr_offset` | invariant "a != b is checked at runtime; the two accesses are disjoint" |
| `Pool.free` | 1 | `ptr_load`, `ptr_offset`, `ptr_store`, `ptr_take` | requires "data addresses cap slots; slot idx holds a live element after check" |
| `Pool.seed_generation` | 1 | `ptr_load`, `ptr_offset`, `ptr_store` | requires "the three buffers hold cap entries; the first count slots are initialized" |
| `Pool.release` | 1 | `heap_free`, `ptr_cast`, `ptr_load`, `ptr_offset`, `ptr_take` | requires "the three buffers hold cap entries; the first count slots are initialized" |
| `Arena.grow` | 1 | `heap_try_alloc`, `ptr_addr`, `ptr_cast` | requires "data addresses cap elements of which the first size are live" |
| `Arena.alloc` | 1 | `ptr_offset`, `ptr_store` | requires "data addresses cap elements of which the first size are live" |
| `Arena.get` | 1 | `ptr_offset`, `ptr_view` | requires "data addresses cap elements of which the first size are live" |
| `Arena.at` | 1 | `ptr_edit`, `ptr_offset` | requires "data addresses cap elements of which the first size are live" |
| `Arena.release` | 1 | `heap_free`, `ptr_cast`, `ptr_offset`, `ptr_take` | requires "data addresses cap elements of which the first size are live" |

The runtime-provided objects keep their marker assertions; their obligations
are met by the C runtime (`runtime/hosted/src/kern_rt.c`) and the interpreter:

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
