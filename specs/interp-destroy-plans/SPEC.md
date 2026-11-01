# Interpreter: cached destroy plans and layouts

**Version:** 0.1 (2026-11-01). **Scope:** the interpreter
(`compiler/interp`); tests; a report. **Language impact:** none; no
output, trap record or status changes.
**Related:** `docs/reports/interp-compile.md` ("what remains:
allocation, the call path, `copyValue`"), `docs/reports/interp-speed.md`
(the method: profile, then measure instructions retired, since the host
is often loaded).

**Review status.** speccritic (gpt-6.1-sol): its one critical finding was
the preflight's missing out-of-scope section, added; the warnings (the
baseline revision, mechanism in normative text) folded in.

**Glossary.** R*n*: requirement *n* of §4. Destroy: `Interp.destroy`, run
for every `drop` and for every field and element a destroyed value holds.
Plan: what destroy needs to know about one type, computed once. IR type:
a `resolve.Type` value held by the lowered module (stable for the run).
Instructions: the "instructions retired" count `/usr/bin/time -l`
reports. Abbreviation: IR, intermediate representation.

## 0. Goals

Profiling `kern run --opt=release` on `bench/kern/arena_ast` shows 13.5%
of the run in `resolve.IsCopy` called from `destroy`, which re-derives
the type's markers (a visiting map keyed by the printed type name) on
every drop, and more time in string building (`Named.String()` for the
destructor's name, on every call) and in `resolve.Subst` of each field's
type (fresh types, so nothing downstream can be cached by identity). The
goal is to compute those once per type. Non-goals: other interpreter
costs (the call path, `copyValue`, boxed integers), the native backend.

## 1. Design

- **Destroy plans.** Each interpreter thread keeps a map from a type
  (by identity) to its plan: whether it is `Copy` (then destroy does
  nothing), whether it is a library-opaque type, its destructor's
  function name, and the substituted types of its fields (for a struct)
  or of each variant's fields (for a union), or its element type (for an
  array). Destroy looks the plan up instead of calling `IsCopy`,
  `String` and `Subst`; recursion into fields uses the plan's field
  types, which are the same values every time, so they hit the map too.
- **Bounded.** The map holds at most 65,536 plans; past that, plans are
  computed and used without being stored, so a program that keeps
  producing new type values cannot grow it without bound.
- **Threads.** The map belongs to one interpreter thread (`*Interp`), so
  it needs no lock.
  *Implementation guidance (non-normative):* the shape above (a map keyed
  by type identity, per thread) is one way to meet R1-R3; what is
  required is the unchanged behaviour, the bound, and the counts.
- Behaviour is exactly the old destroy's: the same destructors in the
  same order, the same field order (last to first).

## 2. Out of scope

The interpreter's other costs (the call path, `copyValue`, boxed
integers, `layout.Of`'s string-keyed memo); the native backend; caching
anything shared between interpreter threads.

## 3. Requirements

**R1. Behaviour unchanged.** Every fixture, the interpreter-versus-native
differential tests and `TestCompiledMatchesGeneric*` pass; the benchmark
programs print the same checksums. *Verified by:* `go test ./...`.

**R2. Fewer instructions.** `kern run --opt=release` of
`bench/kern/arena_ast` retires at least 5% fewer instructions than at
8885e74 (the commit this work starts from), and `vector` and `hash_table` do not retire
more than 1% more. *Verified by:* `/usr/bin/time -l` before and after,
each program once (instruction counts vary well under 1% run to run),
recorded in the report.

**R3. Bounded cache.** A unit test destroys values of many distinct
fresh type values (more than the cap) and checks the map stops at the
cap. *Verified by:* that test.

**R4. Documents.** `docs/reports/interp-destroy-plans.md` records the
profile, the change and R2's counts.

## 4. Acceptance criteria

`golangci-lint run ./...` and `KERN_REQUIRE_QEMU=1 go test ./...` clean;
R2 met.
