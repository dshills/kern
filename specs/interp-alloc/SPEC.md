# Interpreter: allocation in aggregates, library calls and pointer intrinsics

**Version:** 0.3 (2026-11-04). **Review status.** speccritic
(gpt-6.1-sol) round 1: INVALID on three structural findings (no goal,
non-goals or tests section), now added; its two warnings are answered
(R3 names its mode and statistic; §1 is informative). Round 2:
VALID_WITH_GAPS, no critical finding; R6 adds an allocation count and
§3 names how each optimization level is covered. **Scope:** `compiler/interp` only
(`values.go`, `rawmem.go`, `interp.go`, `compile.go`, `intrinsics.go`)
and a report `docs/reports/interp-alloc.md`. **Language impact:** none.
**Follows:** `docs/reports/interp-call-path.md`, whose "What remains"
lists `copyValue` on loads among the allocations left.

**Glossary.**
- R*n*: requirement *n* of §2.
- *The interpreter*: `kern run`, in compiled-op mode (the default) and
  in generic mode (`genericExec`, the reference loop).
- *Instructions retired*: the count `/usr/bin/time -l` reports for the
  whole `kern run` process.
- *A small aggregate*: a `Struct` or `Union` value with at most four
  fields.
- *A fast intrinsic*: one of `ptr_null`, `ptr_from_addr`, `ptr_addr`,
  `ptr_cast`, `ptr_offset`, `ptr_load`, `ptr_take`, `ptr_store`,
  `ptr_view`, `ptr_edit`, `size_of` and `align_of`.

## 0. Goal and non-goals

**Goal.** The interpreter makes fewer allocations, and so retires fewer
instructions, on programs that spend their time in library containers
and raw memory, with no change in what any program does.

**Non-goals.**
- **Integer boxing.** Every integer of 256 or more is boxed into its
  `Value`. It is the largest allocation left (`docs/reports/interp-speed.md`
  measured it and did not take it), and removing it needs a new value
  representation.
- **`Access` objects.** Every `view` and `edit` argument still allocates
  one. Reusing them needs a proof that the previous access is dead,
  which returned accesses and closure environments make subtle.
- **Wall-clock targets.** This host was not quiet (load about 20 from
  indexing). Wall times are reported with their load and are secondary
  to instructions.
- **Native code.** The compiler and its output do not change.

## 1. What changes (informative)

This section describes the design. The requirements are in §2; an
implementation that meets them by other means is acceptable.

Profiles of `bench/kern/arena_ast` and `bench/kern/vector` under
`kern run --opt=release` at `dd16b89` show four avoidable costs:

1. **Aggregates.** Copying a struct or union on a load (`copyValue`),
   decoding one from raw memory, building one (`MakeStruct`,
   `MakeUnion`), zeroing one and deep-cloning one each make two
   allocations: the value and its `Fields` slice. Decoding also grows
   `Fields` by `append`. A small aggregate is now one allocation: the
   value and a fixed array of four or fewer slots. Larger ones allocate
   as before. `Fields` has exactly the field count as both length and
   capacity, so it is never extended in place, and a field's slot never
   moves once the value is built.
2. **Library calls.** A call into the library passed a pointer to a copy
   of its position, which escaped into the callee's frame: one
   allocation per call. The callee's span is now a pointer to a value
   that already exists and never changes: the caller's own span inside
   the library, otherwise the call site's position, held once per
   compiled call op (or in the IR for the generic engine). A call
   without a call site points at one shared empty position.
3. **Pointer intrinsics.** A compiled intrinsic call made an argument
   slice and the intrinsic a result slice. A fast intrinsic takes its
   arguments in a per-thread buffer and returns its one result (or none)
   directly. Each fast intrinsic is also registered as an ordinary
   intrinsic, wrapped, so every other path (generic mode, calls by name)
   runs the same code. A fast intrinsic keeps no reference to its
   argument slice and runs no kern code, so no other intrinsic call can
   reuse the buffer while it runs. A thread's copy of the interpreter
   starts with no buffer of its own.
4. **Integer bytes.** Raw-memory integer loads and stores of 1, 2, 4 and
   8 bytes use one fixed-width little-endian access instead of a byte
   loop. Other sizes keep the loop.

## 2. Requirements

**R1. Same behaviour.** Every program gives the same output, exit status
and trap record (kind, span, file, thread) as before, in both modes and
at both optimization levels. A trap's position inside the library is
still the user-level call that entered it.

**R2. The engines agree.** `TestCompiledMatchesGeneric*` and the fixture
harness (`go test ./tests`, which compares the interpreter with native
builds) pass. One host failure is excepted: the `race_raw_ptr` TSan
fixture times out on this host because the symbolizer hangs, at
`dd16b89` as well. It is excepted only if it fails by timing out at both
commits; CI runs it on Linux.

**R3. Fewer instructions.** Each program is run with
`kern run --opt=release` in compiled-op mode (the default), on this host
and in one session, at least twice with each compiler (one built at
`dd16b89`, one with this change), the runs of the two compilers
interleaved. The statistic is each compiler's median instructions
retired over its runs:
- for `bench/kern/arena_ast` and `bench/kern/vector`, the new median is
  at least 5% below the baseline median;
- for the `fib(29)` program of `specs/interp-call-path`, the new median
  is within 1% of the baseline median, so calls outside the library pay
  nothing for item 2.
Each run's output is the same for both compilers (for the bench
programs, the `ops` and `checksum` fields; `ns` and the percentiles are
timings).

**R6. Fewer allocations.** In the same runs' programs, run in process
(the `kern run` entry point called from a Go test), the change in
`runtime.MemStats.Mallocs` across the run is at least 15% lower than the
baseline's for `arena_ast` and for `vector`.

**R5. Fixed field slots.** A struct's or union's field slots do not move
once the value is built: no code in the package extends a built value's
`Fields` in place.

**R4. Nothing else changes.** No file outside `compiler/interp` changes,
except the report and this spec's directory. The compiler's output is
unaffected (no package it uses changes).

## 3. Tests

- **R1, R2:** `go test ./compiler/interp` (including
  `TestCompiledMatchesGeneric*`) and `go test ./...`, whose `tests`
  package runs every run, trap and concurrency fixture in the
  interpreter (unoptimized IR) and natively at debug and release, and
  compares them.
- **R1 at both levels:** every program in `tests/run`, `tests/trap` and
  `tests/concurrency` run with `kern run --opt=debug` and
  `kern run --opt=release` (debug only for test-hook fixtures) by both
  compilers, comparing standard output, standard error (the trap
  record) and exit status. A program whose output varies between two
  runs of one compiler (thread interleaving, timings) is rerun and
  reported as such.
- **R6:** a throwaway Go test calling the `kern run` entry point between
  two `runtime.ReadMemStats` calls, built in each tree.
- **Unit tests** (`compiler/interp/alloc_test.go`): the helpers' shape
  for sizes 0 to 6 and their allocation counts (one up to four fields,
  two above); copies that share no slot, nested aggregates included, and
  a slot reference that stays put; `zeroOf`'s slots; `putInt`/`getInt`
  round trips for sizes 1 to 8; `spanAt`; every fast intrinsic
  registered as an ordinary one with the same result.
- **R3:** the measurement of R3, with every run's count, the medians and
  the load recorded in `docs/reports/interp-alloc.md`.
- **R4:** `git diff --stat` for the commit.
- **R5:** a search for `Fields = append` and `.Fields = append(` in
  `compiler/interp` finds nothing.
