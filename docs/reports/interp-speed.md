# Interpreter: recomputation and allocation per step removed

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

## Second step: cheaper integers and calls

The same day's follow-up (profile of `vector` after the change above):
- **`Int.Ty`** was a string, so a boxed `Int` was a 24-byte object the
  collector had to scan. It is now a one-byte `IntTy` code. `Int` is 16
  bytes with no pointer, and width and signedness are table lookups
  instead of string switches.
- **Integer results below 256** come from a table of boxed values
  (`intVal`) in `binary`, negation and conversions.
- **A call's frame** allocates its locals and values as one slice, not
  two.
- **`funcInfo` holds each direct call's target**: the intrinsic's
  function or the callee. This replaces up to two lookups by name per
  call (the intrinsics map, then `Module.Func` twice for a user callee)
  with one pointer-keyed lookup.

| program | before | after | instructions |
|---|---|---|---|
| arena_ast | 2.78 s | 2.55 s | 86.5G → 81.6G (−5.6%) |
| vector | 17.11 s | 15.96 s | 451.9G → 415.8G (−8.0%) |
| hash_table | 42.98 s | 39.47 s | 1038.6G → 955.2G (−8.0%) |

The checksums are unchanged. Over the day the interpreter went from
6.4 s to 2.55 s on arena_ast, 23.6 s to 16.0 s on vector and 55.6 s to
39.5 s on hash_table.

## What remains

Integer boxing is still the largest allocation: 78% of `intVal`'s
allocations are results of 256 or more. `vector`'s container code
computes raw memory addresses, which are large. No boxed `Int` can avoid
the allocation while `Value` is an interface holding a struct.

The alternative is `*Int` from a per-thread slab, a pointer-shaped
value. But it would turn every comparison of two `Value`s holding
integers into an identity comparison, a semantic change across the
interpreter, so it was not taken. A `Value` struct with an inline word
for scalars is the representation that would remove the boxing. It means
rewriting every site that type-switches on a value.

`exec` itself is 21% flat. The next costs are `resolve` (11%), drops and
copies (8%), and the argument and return slices of each call (7%).

## Boxing integers: measured, not taken (2026-10-31, later)

The next step proposed was integers without boxing. Measured first, with
macOS `sample` on `kern run --opt=release` of `vector` at ea119b0 (Go's
CPU profiler misattributes on this host):

| part | share of busy samples |
|---|---|
| `exec`'s own dispatch | 26% |
| `resolve` | 12% |
| `callAt` | 8.5% |
| allocation in all | about 17% |
| unscanned 16-byte objects, the boxed `Int`s | about 9% |

The usual design keeps integer SSA values in typed frame slots and
boxes a value only when it escapes to a generic consumer: a store to a
place, a call or intrinsic argument, a constructor. In `vector`'s
release IR, 24 of the 31 integer-producing instructions have such a use,
so typed slots would remove about a quarter of the boxing, some 2% of
the run.

Removing the rest means holding scalars inline in every local, field,
argument and element: replacing `Value`, an interface, with a struct
across the interpreter (3,000 lines of type switches and assertions).
That is too much rewrite for at most the 9% plus its collection cost.
The larger costs are dispatch and place resolution. Compiling each
function's instructions once into closures (or a dense instruction array
with pre-resolved operands) is the lever there, measurable the same way.

