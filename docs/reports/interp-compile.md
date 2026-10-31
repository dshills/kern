# Interpreter: compiled instructions

**Date:** 2026-10-31. **Spec:** `specs/interp-compile/SPEC.md`
(speccritic VALID_WITH_GAPS; the remaining warnings flag acronyms on
the lines that define them). **Host:** Apple M4 Pro, busy. Instructions
retired (`/usr/bin/time -l`) carry the comparison, with two interleaved
runs of each.

## The change

`exec` decided, at every execution of every instruction, what kind it
was, where its operands were, which function a call reached, and which
arithmetic an operator meant. It did all of that through a type switch,
place resolution and string switches.

At a function's first call, each block is now compiled into a list of
ops: closures with those decisions taken once (`compiler/interp/compile.go`).
The compiled form is kept in the function's existing `funcInfo`.
Specialised ops cover:
- **constants:** the precomputed value;
- **loads and stores** of a plain local's slot;
- **places** rooted at a local, an access or a global whose path has
  only field and variant steps, resolved by a closure with the root
  and the steps decided. Loads, stores, access begins, moves and drops
  through such places use it.
- **an `access_end` without a release**, which compiles to nothing;
- **integer arithmetic and comparisons**, whose operation is chosen once
  from the operator and the static type;
- **overflow and bounds checks**, with operands by ID;
- **conversions**;
- **direct calls**, which hold their target;
- **terminators**, which hold their successor blocks.

Each specialised op mirrors one case of `exec` and hands every other
case to it. Every remaining instruction, such as an index or slice
place, compiles to an op that calls `exec`. The block being executed is
recorded before its ops run, as before, so trap records name the same
file.

How the specialised set was chosen: a temporary counter of the
instructions still taking the generic path in `vector` found loads
through an access with a field path (240M executions), `access_begin`
(80M), `access_end` (60M, nearly all without a release), stores through
an access (60M), conversions (40M) and drops (20M). All of these are
now specialised.

## Behaviour unchanged

`TestCompiledMatchesGenericFixtures` runs every program in `tests/run`
and `tests/trap` (test-hooks fixtures in debug only) in both
configurations, compiled and through `exec` alone, and compares output,
trap record and status.

`TestCompiledMatchesGenericGenerated` does the same for 60 generated
programs, in both configurations. They cover:
- every integer operator, on signed and unsigned types of every width;
- shifts that exceed the width, and bounds checks that trap;
- calls to user functions, `Vec` (library) and intrinsics.

Both pass, as do the fixture harness, the interpreter-versus-native
differential tests and the full suite. The benchmark checksums are
unchanged.

## Effect

| program | before | after | instructions |
|---|---|---|---|
| arena_ast | 2.66 s | 2.33 s | 81.7G → 74.1G (−9.2%) |
| vector | 16.0 s | 12.3 s | 415.9G → 322.2G (−22.5%) |
| hash_table | 39.6–41.6 s | 30.5 s | 955.3G → 756.8G (−20.8%) |

Over the day, the interpreter went from 6.4 s to 2.3 s on arena_ast,
23.6 s to 12.3 s on vector and 55.6 s to 30.5 s on hash_table.

## What remains

The generic path is left with index and slice places, and with the
instructions too rare to specialise. The larger costs now are:
- allocation: boxed integers, `Access` objects and frames;
- the call path (`callAt`'s per-call setup);
- `copyValue` on loads.
