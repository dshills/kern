# Interpreter: three allocations per step removed

**Date:** 2026-10-31. **Host:** Apple silicon (arm64, macOS), busy.
Instructions retired (`/usr/bin/time -l`) carry the comparison, with
each program's own timed `ns` beside them. Profiles are Go CPU profiles
of `kern run --opt=release` on the benchmark programs. They were taken
with a scratch test, which is not committed.

## Where the time went

`arena_ast` in the interpreter, 8.5 s in all:

| share | where |
|---|---|
| 27.6% | `layout.Of` |
| 17% | `resolve.(*Named).String` (within `layout.Of`) |
| 12.5% | `frameSize` |
| 15.5% | `decode`: `layout.VariantLayout` |
| 13% | `resolve`: `rawRef.variantField` |

All of it was recomputing what the function or the type fixes once:
- **`frameSize`** charges each call the frame's size: every local's
  layout, recomputed at every call.
- **`VariantLayout`** recomputes a union variant's field offsets at
  every payload read. That includes the whole union's layout and each
  field's, through `layout.Of`, whose memo key is the type's printed
  name. For a generic field such as `ArenaId[Expr]`, that name is built
  on every lookup.

`vector` and `hash_table` (18–20% in `resolve`, 20–24% allocating)
showed two more:
- **Struct and union fields** resolved to an `idxRef` holding a closure
  over the value's field slice: two allocations per field step. A
  field's slot never moves (a struct's fields are fixed once it is
  built; another variant is another `*Union`), so the slot itself
  serves, as a pointer-shaped `slotRef` with no allocation.
- **Every constant instruction** built and boxed its value afresh: 6.5%
  of `vector`.

## The change

- **`compiler/interp`:** each function's first call computes a
  `funcInfo` (in the shared state, so threads share it): its frame size,
  and the boxed values of its scalar constants by value ID. Every later
  call reads both. A scalar constant is immutable, so every activation
  can share its value. A byte-string constant still builds its array at
  each execution.
- **`compiler/interp`:** a field or payload step resolves to the field's
  own slot.
- **`compiler/layout`:** `VariantLayout` is memoized by the union's
  identity and instantiation, as `Of` already is for named types, plus
  the variant. Its callers only read the offsets, so the cached slice is
  shared.

## Effect

Two interleaved runs of each:

| program | before | after | instructions |
|---|---|---|---|
| arena_ast | 6.28–6.50 s | 2.84–2.94 s (2.2x) | 170.6G → 86.2G |
| vector | 23.55–23.65 s | 17.59–17.68 s (1.34x) | 617G → 453G |
| hash_table | 54.9–56.2 s | 43.5–44.5 s (1.27x) | 1375G → 1043G |

The checksums are unchanged. `tests/run/field_slots.kern` writes
through struct and union fields before and after replacing the whole
value, and the interpreter and native builds agree on it in both
configurations.

## What remains

`vector`'s profile after the change: `exec` 21% flat, allocation 19%
(`binary` and `conv` box every integer result, since `Int` is a 24-byte
struct behind the `Value` interface), `resolve` 12%, drops 5%, and an
intrinsic's lookup by name at each call (2%). The boxing is the next
lever. Removing it means a `Value` representation that holds an integer
without allocating, a change across the whole interpreter.
