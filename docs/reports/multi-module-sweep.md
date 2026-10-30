# Multi-module and feature sweep

**Date:** 2026-10-29. **Method:** small programs written against the
spec, run under `kern run` and under `kern build` (debug and, for some,
release), their outputs and trap records compared. A program that the
checker rejects is rewritten until it is the checker's rule and not the
program that is wrong; a difference between the backends, an internal
error, or a wrong record is a fault. No model was called.

## Multi-module programs: four faults, all fixed

No fixture had run a program with an imported user module (the two
existing ones, `tests/positive/import_const` and the `name_private`
twins, read a constant or were only checked). The first probe that called
an imported function crashed both backends.

| fault | commit | fixture |
|---|---|---|
| an imported module's functions were declared with no body, so the first call crashed the interpreter and the backend; its destructors never ran | 250198b | `tests/positive/import_call` |
| trap records named one program-wide file: the first source in the interpreter, none natively (the stamping call's pattern had stopped matching `main`'s signature) | c4a8aca | `tests/trap/import_trap`, `//! trap-file:` |
| `impl lib.Shape for Rect` and `T: lib.Shape` looked the interface up in the implementing module only | 640f258 | `tests/positive/import_iface`, `tests/negative/import_iface_private` |
| `thread.spawn(lib.Worker, n)` and `lib.Apply(lib.Twice, x)` failed in lowering: an imported function was not a value | 640f258 | `tests/positive/import_funcval` |

Probes that passed once those were fixed: a module spanning two files
(a trap in the second names it); methods, generic functions and a
destructor-bearing type of an imported module; a generic container of an
imported module holding the importing module's type with destructors, and
a library trap inside the import reporting the import's file with the
user-level span; unions and payload-free variants of an imported module
matched through `lib.Shape.Circle(r)`; a nested import path
(`import "lib/inner"`) from both the importer and its import; two modules
exporting the same names, and a private function shadowing an exported
one; an imported `thread_local` read from a spawned thread; closures and
`out` parameters across modules; `std/str` used inside an import; and
every machine-API command (`show-copies`, `show-allocations`,
`show-layout`, `show-effects`, `show-bounds-checks`, `explain-ownership`,
`format`, `check`) on a two-file program, with locations naming their
file.

## Single-module features: no fault in eleven probes

The least-covered constructs in the fixtures were probed next (counts of
fixtures using each: `std/map` 1, `std/io` 1, `str` 2, nested arrays 1,
`Arena` 3, `f32` 4, unions 5): a `Map` with a struct key implementing
`map.Hashable` and destructor-bearing values (insert, get, at, remove,
replace, growth, destruction order); a `Pool` linked list through
`Option[PoolId[T]]` with `free` and a vacant-slot trap; an `Arena` tree
through handles stored in nodes with a cross-arena identity trap; arrays
of unions in a struct in a nested array, `zero()`, `Option` of a struct;
`f32`/`f64` arithmetic and conversions; a union with destructor-bearing
payloads consumed by an owned `match`; `str.trim`, `find`, `parse_u64`,
`push_u64`, `io.write_stdout`, `io.args`, `io.read_file` on a missing
path; `thread.scope` with a `Mutex`, an `Atomic` and views; closures
capturing an edit, nested and passed down; `where`-clause methods on a
generic struct, a `Box` linked list of destructor-bearing cells; fallible
`out` initialisers forwarded with `?`. Every probe printed the same thing
under both backends, and every trap was the same kind, span and file.

## Found, not done

- `Slab[T]` is named among the built-ins in `spec/types.md` and in
  `docs/ownership.md` but no longer exists in `std/core.kern`; `Pool`
  carries the retired-slot flag (`PoolSlotMeta.retired`) that PLAN.md 1.x
  describes for `Slab`. Either the name is stale in the documents or the
  container is missing; the spec is the owner's. The history and the two
  options are in `docs/reports/slab-drift.md`.
- `alloc_with` and `new_with` (PLAN.md 1.x fixed signatures) are not in
  the library; an earlier proposal to implement them is open. *Resolved
  2026-10-30* (`docs/reports/in-place-init.md`).
