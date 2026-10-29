# Aggregate constants as read-only globals

**Date:** 2026-10-29. **Follows:** the fix of 768537b, which stopped the
lowering from failing on `TAB[i]` for a module-level array const by giving
the const's value a temporary place at each use. **Scope:** lowering, the
interpreter, the LLVM backend and the proof pass; no change to the
language or to what a program prints.

## Before

A module-level `const` was lowered as its initializer expression at every
use. For a scalar that is a literal in the IR, which is what the design
wants. For an array or a struct it meant that every use rebuilt the whole
value: a 256-entry lookup table was rebuilt on each lookup, and `len(TAB)`
and `TAB[i]` built two different temporaries, so the proof pass saw two
places and never proved a guard on a const array
(`show-bounds-checks` labelled `TAB[i]` under `if i < len(TAB)` as
runtime-checked).

A second fault was found on the way and fixed first (250198b): a function
of an imported user module was declared in the IR with no body, so the
first call to one crashed the interpreter and the backend, and a
destructor declared by an imported module never ran. The lowering queued
bodies only for the main module and the embedded library. Every function
with a body is now lowered, against the file that declares it.

## After

An array or struct const whose initializer folds at compile time is one
read-only global per program, built once and shared by every function and
thread:

- `resolve.FoldConst` folds an initializer made of literals, `zero()`,
  other constants that fold (of this module or an imported one), fields
  and literal-index elements of such constants, array literals and struct
  literals. A scalar folds as a `thread_local` initializer does; an
  aggregate folds to its elements or fields in declaration order, with nil
  for a zero value at any depth. A call, an operator or a conversion does
  not fold, and such a const keeps the per-use lowering (the values are
  the same either way).
- `ir.Global` gains `ReadOnly`; the lowering creates one per folded const
  of an array or struct type that is `Copy` (`constGlobal`), and a use of
  the const is a `view` place rooted at it. The checker already rejects a
  write to a const element (`ASSIGN_IMMUTABLE`) and an edit access to a
  const slice (`ACC_PERMISSION_ESCALATION`), so the global is never
  written.
- The LLVM backend emits it as a `constant` with its data rendered from
  the folded value; the interpreter builds the value once per thread from
  the same data.
- The proof pass keys a place rooted at a read-only global like one rooted
  at an access parameter (immutable, so a length fact holds everywhere),
  and reads the length of an array global, or of an array element on a
  path, from its type.

## Measured

`kern show-bounds-checks` on `tests/run/const_array.kern`, the fixture of
768537b, before and after:

| check | before | after |
|---|---|---|
| `TAB[i]` under `if i < len(TAB)` | runtime-checked | proven, `dominated_by_check` |
| `TAB[0usize]`, `ORIGIN.ys[1usize]` | proven, `constant` | proven, `constant` |
| `TAB[1usize:3usize]` (a slice) | runtime-checked | runtime-checked |

`tests/run/const_globals.kern` (nested arrays, an array of structs, a
struct holding a struct and an array, consts built from other consts and
from their fields and elements, `zero()`, bool and `f32` arrays, a const
read from two functions and from a spawned thread, and one const that does
not fold) reports 19 bounds checks, all proven; its output is the same
under the interpreter and the debug and release native builds.
`tests/positive/import_const_array` reads an imported array const in a
guarded loop and through the exporting module's function: both guards are
proven, and the global is one object for both modules.

## Not done

- A const of a non-`Copy` type, of a union type, or of a `view[]u8` string
  stays an expression at each use, as before.
- The IR printer shows a global only where it is used (`len @run.TAB`,
  `load @run.TAB[%v3]`); there is no header listing the module's globals.
- `docs/ownership.md` does not describe constants; the spec is the owner's.
