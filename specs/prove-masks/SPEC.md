# Proof pass: masked indexes

**Version:** 0.1 (2026-10-30). **Scope:** the bounds-check proof pass
(`compiler/opt/prove.go`) and its tests; fixtures under `tests/run/`; the
differential test of proven checks (`tests/prove_diff_test.go`); the
schema note on `proven_by` (`compiler/diag/schema.go`, generated
`tools/agent-api/schema.md`); the report `docs/reports/prove-masks.md`.
**Language impact:** none. The §16 result shape and its `proven_by`
values (`loop_bound`, `dominated_by_check`, `constant`) are unchanged.
**Related:** `specs/prove-guards/SPEC.md` (the pass as it stands);
`docs/reports/thread-pool-job-path.md` ("What a compiler could do",
where this was proposed); `docs/reports/agent-tasks-v4.md` (the fourth
agent suite, built on idioms the pass leaves checked);
`specs/initial/AI_Native_Systems_Language_SPEC.md` §7 invariant 3 (every
check remains or is replaced by a recorded proof fact).

**Review status.** speccritic (OpenAI gpt-6.1-sol, profile general,
with `specs/prove-guards/SPEC.md` as context): VALID_WITH_GAPS, no
critical finding. Its two substantive warnings, on which label wins when
both rules hold and how the cost is aggregated, are folded into §1 and
R7. The rest are the preflight's prose flags.

**Glossary.** R*n*: requirement *n* of §3. Mask: a bitwise and `x & c`
of unsigned operands where one operand `c` is a constant: an integer
literal, or a constant the front end has folded to one. Masked value:
the result of a mask, or of any conversion to an unsigned type of a
masked value, which is at most `c`. Index check: a `bounds` check of an
index expression against a length. Known length: an array's length from
its type, or a literal the pass treats as the same length (as today).
Lower-bound fact: what the pass knows today from a guard such as
`len(xs) >= K`, that the literal `K - 1` is below the length.

## 0. Goals and non-goals

Goal: an index computed by masking with a constant, the standard ring
and hash-table idiom (`ring[(head + len) & 2047usize]` into a
`[2048]u64`), is proven in range when the mask is below the length, so
`show-bounds-checks` labels it `proven` and the check is left out.

Non-goals:
- masks by a value that is not a constant (`i & mask` with `mask` a
  parameter or `cap - 1`);
- masked values carried around a loop or across a call (a field
  assigned `(t + 1) & 1023` in one method and read in another);
- a value's range from its type (`u8` into a 256-entry table), which
  the fourth agent suite counts on staying checked;
- remainders (`x % c`);
- any change to machine code beyond leaving out the newly proven
  checks. LLVM already folds most of them, as the thread-pool report
  measured.

## 1. The fact

For an index check whose index is a masked value plus a constant offset
`d` (the pass's existing reading of `v + d` and `v - d`), the index is
at most `c + max(d, 0)`. The check is proven when that bound is below
the length:

- **`constant`.** The length is a known length `n` and
  `c + max(d, 0) < n`.
- **`dominated_by_check`.** A lower-bound fact on every path to the
  check says the literal `K` is below the length, with `K >= c +
  max(d, 0)`.

When both hold, the label is `constant`, as for a literal index today.
Otherwise the check keeps whatever the existing rules give it. A
masked value is recognised through the pass's value numbering and
known-value tracking as today: `j := x & 1023usize; buf[j]` is a masked
index, as is `buf[usize(x & 1023u64)]`.

## 2. Requirements

**R1 — Proofs.** Every index check matching §1 is proven, with the
`proven_by` §1 gives. A check that §1 does not match keeps exactly the
label and `proven_by` it had before this change.

**R2 — Soundness.** No check is proven whose index can reach the
length. The masked value's bound holds for every unsigned type and
every constant, including a constant at or above the length (no proof)
and a mask by 0 (the index is 0).

**R3 — Both engines agree.** The interpreter keeps executing proven
checks (as today), so a wrong proof shows as a native build that does
not trap where the interpreter does. The differential test of proven
checks gains masked indexes: masks below, equal to and above the
length, with offsets, through a local, through a conversion, and
against a guarded slice. It passes with its default program count, and
with 400 programs once during this change.

**R4 — Fixtures.**
- `tests/run/prove_forms.kern` (every check proven) gains a masked
  index into an array, the same through a local and through a
  conversion, a mask with an offset that stays below the length, and a
  mask under a `len(xs) >= K` guard on a slice.
- `tests/run/prove_forms_not.kern` (every check runtime-checked) gains a
  mask equal to the length, a mask whose offset reaches the length, a
  mask by a non-constant, and a masked value read in another function.

Both run in both engines and both opt levels.

**R5 — Which existing checks change.** The report lists every bounds
check in the programs under `bench/kern/`, `examples/` and `tests/run/`
whose label changes, in debug and in release, compared with the
compiler before this change. Every one of them is a masked index.

**R6 — Agent suites unchanged.** The dry runs of the third and fourth
agent suites pass unchanged, and every natural solution of the fourth
suite still exceeds its budget by at least 3 (its calibration check).

**R7 — Cost.** `go test ./compiler/opt -bench BenchmarkProve` is
recorded before and after: the test binaries of both compilers are
built once and run alternately, 5 rounds of `-count 1` each, and each
benchmark's median ns/op is compared. The change adds at most 10% to
each median.

**R8 — Schema note.** The `BoundsCheck.proven_by` note says that
`constant` also covers an index bounded by a constant mask below an
array's length. `tools/agent-api/schema.md` is regenerated.

## 3. Acceptance criteria

1. Unit tests in `compiler/opt/prove_test.go`:
   - a direct mask into an array, `constant`;
   - a masked local, a converted mask, and `(x & 7) + 1` into `[9]`,
     each `constant`;
   - `(x & 8)` into `[8]`, runtime-checked;
   - a mask under `len(xs) >= 1024` on a slice, `dominated_by_check`;
   - a mask by a parameter, runtime-checked.
2. The fixtures and the differential test of R3 and R4 pass.
3. The report's R5 table and R7 timings, and the R6 dry runs exiting 0.
