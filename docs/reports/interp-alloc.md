# Interpreter: allocation in aggregates, library calls and pointer intrinsics

**Date:** 2026-11-04. **Spec:** `specs/interp-alloc/SPEC.md` v0.3
(speccritic VALID_WITH_GAPS, no critical finding). **Plan:**
`specs/interp-alloc/PLAN.md`. **Host:** Apple M4 Pro, given over to
timing, but with Spotlight indexing throughout (one-minute load about
20). Instructions retired carry the comparison; wall times are given with
their load. **Follows:** `docs/reports/interp-call-path.md`.

## What the profiles showed

At `dd16b89`, `kern run --opt=release` on `bench/kern/arena_ast` made
114 million heap allocations, and on `bench/kern/vector` 596 million.
- **Aggregates: two allocations each.** A struct or union value and its
  `Fields` slice were separate objects. Each copy on a load
  (`copyValue`), each decode from raw memory and each `MakeStruct`
  allocated twice, and `decode` grew `Fields` with `append`. In
  `arena_ast`, `copyValue` alone was a quarter of all allocations.
- **Library calls: one allocation each.** The call op passed a pointer
  to a copy of its position, which the callee's frame kept, so the copy
  escaped to the heap on every call into `std`.
- **Pointer intrinsics: two allocations each.** A compiled intrinsic
  call built an argument slice, and the intrinsic a one-element result
  slice. `ptr_offset`, `ptr_load`, `ptr_store` and their kin run inside
  every container method, so in `vector` the two were 27% of all
  allocations.
- **Integer bytes.** `getInt` and `putInt`, the raw-memory integer
  accessors, looped over bytes. They showed as a quarter of
  `arena_ast`'s CPU time, but almost all of it was waiting on memory.

## The change

- **`newStruct(n)` and `newUnion(tag, n)`** build a value with up to
  four fields as one object: the value and a field array of exactly `n`
  slots. Larger values allocate as before. `Fields` has length and
  capacity `n`, so no code can extend it in place, and a `slotRef` into
  it stays valid. `copyValue`, `decode`, `zeroOf`, `MakeStruct`,
  `MakeUnion` and `deepClone` use them, and `decode` assigns fields by
  index.
- **Stable span pointers.** `frame.spanAt(pos)` is the caller's own span
  inside the library and otherwise the call site's position, held once
  per compiled call op (or in the IR for the generic engine). A call
  without a call site points at one shared empty position. The frame
  keeps its pointer, so it does not grow.
- **Fast intrinsics.** The twelve hot pointer intrinsics have the type
  `fastIntrinsic`: one result or none, no reference kept to the
  arguments, and no kern code run.
  - A compiled call copies its arguments into a per-thread buffer, calls
    the intrinsic, clears the buffer and stores the result.
  - Each is also registered as an ordinary intrinsic, wrapped, so the
    generic engine and calls by name run the same code.
  - Spawning intrinsics keep their argument slice and `ptr_init` runs
    kern code, so neither is in the table.
- **`getInt` and `putInt`** use one `encoding/binary` access for 1, 2, 4
  and 8 bytes.

## Measurements

**Allocations (R6).** These are exact counts: `runtime.MemStats.Mallocs`
and `TotalAlloc` across one in-process `kern run --opt=release`, from a
throwaway test in each tree.

| program | `dd16b89` | this change | change |
|---|---|---|---|
| `arena_ast` allocations | 113,916,470 | 76,449,225 | −32.9% |
| `arena_ast` bytes | 7.20 GB | 7.03 GB | −2.4% |
| `vector` allocations | 596,191,369 | 435,487,782 | −27.0% |
| `vector` bytes | 19.94 GB | 16.40 GB | −17.7% |

A first version used a two-slot array for one-field values and a
four-slot one for three. It made the same number of allocations but 2.2%
more bytes on `arena_ast`. The arrays are now sized exactly.

**Instructions retired (R3).** Three compilers were run interleaved,
rotating which ran first:
- 74d339e, before the call-path change;
- `dd16b89`, the baseline;
- this change.

There were five rounds of `fib(29)`, `arena_ast` and `vector` and three
of `hash_table`, each `kern run --opt=release`. The load was 17.9 to
28.9 (median 22.9). The table gives medians; each program's output
(`ops`, `checksum`, or `fib`'s result) was the same for every run.

| program | 74d339e | `dd16b89` | this change | change from `dd16b89` |
|---|---|---|---|---|
| `fib(29)` | 4.598 G | 3.124 G | 3.139 G | +0.5% |
| `arena_ast` | 61.05 G | 59.49 G | 52.43 G | −11.9% |
| `vector` | 320.3 G | 313.1 G | 281.2 G | −10.2% |
| `hash_table` | 756.0 G | 748.9 G | 633.0 G | −15.5% |

R3 holds: `arena_ast` and `vector` are more than 5% below the baseline,
and `fib`, whose calls stay outside the library, is within 1%. Within
each compiler the runs agree to 0.5% or better (`vector`'s five final
runs span 281.20 to 281.31 G).

**Wall time** (medians, seconds, under the same load):

| program | 74d339e | `dd16b89` | this change |
|---|---|---|---|
| `fib(29)` | 0.20 | 0.14 | 0.15 |
| `arena_ast` | 2.70 | 2.68 | 2.45 |
| `vector` | 12.59 | 12.42 | 11.34 |
| `hash_table` | 34.23 | 33.76 | 29.88 |

The bench programs are 8.6% (`arena_ast`), 8.7% (`vector`) and 11.5%
(`hash_table`) faster. `fib`'s time is mostly start-up (checking the
prelude and lowering), so its rounding hides any difference.

## Behaviour (R1, R2)

- `go test ./compiler/interp` passes, including
  `TestCompiledMatchesGeneric*` and the new unit tests in
  `alloc_test.go`. Those cover:
  - the helpers' shape for sizes 0 to 6, and their allocation counts:
    one up to four fields, two above;
  - copies that share no slot, nested aggregates included, and a slot
    reference that stays put;
  - `zeroOf`'s slots, `putInt`/`getInt` round trips for sizes 1 to 8,
    and `spanAt`;
  - every fast intrinsic registered as an ordinary one, with the same
    result.
- `go test ./...` passes. Its `tests` package runs every fixture in the
  interpreter (unoptimized) and natively at debug and release.
- **Both levels in the interpreter.** Every program in `tests/run`,
  `tests/trap` and `tests/concurrency` was run with `kern run` at debug
  and at release (debug only for test-hook fixtures) by the `dd16b89`
  compiler and by this one. Standard output, standard error (the trap
  record) and exit status were compared over 345 runs.
  - 337 are identical.
  - The 8 that differ are the four fixtures added with
    `docs/reports/in-forms.md`, which the older compiler cannot check.

## Found on the way

- **The frame must not grow.** Holding the span by value in the frame
  was tried first. It removed the allocation, but `fib(29)` then retired
  about 3% more instructions. The frame had grown by 16 bytes, which moved
  `smallFrame` into the next allocation size class. Stable pointers keep
  the old frame.
- **The byte loops were not the cost.** `getInt` and `putInt` took a
  quarter of `arena_ast`'s CPU samples. Replacing the loops cut 0.5% of
  its instructions and no time: the samples were cache misses on the
  arena's million nodes.

## Still open

- **Integer boxing**, the largest allocation left: 211 million of
  `vector`'s were `intVal` results of 256 or more. Removing it needs a
  new `Value` representation (`docs/reports/interp-speed.md`).
- **`Access` objects**, one per `view` and `edit` argument: 80 million in
  `vector`. Reusing them needs a proof that the previous access is dead.
