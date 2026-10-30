# Plan: proof pass, masked indexes

**Spec:** `specs/prove-masks/SPEC.md` v0.1. **Profile:** davin-go.
**Review status.** plancritic (davin-go, with the spec): the first
version was NOT_EXECUTABLE on one critical contradiction, because the
existing guard rule ran before the mask's `constant` rule. The rule
order is now explicit, with a regression test. The other points folded
in are the overflow argument, a mask above the length in the
differential test, and the enforced R7 limit.
**Order:** two phases; each ends with `golangci-lint run ./...` and
`go test ./...` green; one commit after Phase 2.

**Baseline (taken):** `show-bounds-checks` of every `.kern` file under
`bench/kern/`, `examples/` and `tests/run/` in debug and release with a
compiler built from 18e0acc (session scratch `mask/base-*.json`: 105 and
82 runtime-checked bounds checks). A copy of the `compiler/opt` test
binary at 18e0acc is built for R7 before any edit
(`go test -c -o <scratch>/opt-base.test ./compiler/opt`).

## Phase 1: the fact (R1, R2)

**Files.** `compiler/opt/prove.go`, `compiler/opt/prove_test.go`.

1. `func (p *prover) maskBound(s *facts, v *ir.Value) (uint64, bool)`:
   the canonical value's definition (`p.def`) is a `*ir.Binary` with
   `Op == "&"`, an unsigned type, and one operand whose canonical value
   is a constant (`p.constant`), giving that constant; or a `*ir.Conv`
   to an unsigned type of a value `maskBound` accepts, giving the same
   bound (a conversion never raises an unsigned value that passes its
   range check, and a truncation only lowers it).
2. In `indexCheck`, one deterministic order, the first rule that holds
   giving the label:
   1. the existing literal-index rule (`constant`), which applies only
      when `base == nil`, so it never meets a masked index;
   2. the mask's known-length rule (`constant`): `base != nil`,
      `maskBound(s, base)` gives `c`, `m = c + max(off, 0)`, and
      `classLen(s, ln)` gives `n > m`;
   3. the mask's lower-bound rule (`dominated_by_check`): the same `m`
      and `s.bound(nil, ln)` gives `k >= m`;
   4. the existing `dominated_by_check` rule.

   So a masked index under a guard gets `constant` when rule 2 holds,
   as spec §1 requires, and every check that matches neither mask rule
   is decided by rules 1 and 4 exactly as before (R1). Arithmetic:
   `m` is a `uint64`, `c + uint64(off)` when `off > 0` and `c`
   otherwise; `p.constant` admits constants up to 2^62 and `p.index`
   keeps offsets at most 2^62, so `m <= 2^63` fits. Rule 2 compares `m <
   n` in `uint64`; rule 3 proves only when the bound `k` is non-negative
   and `uint64(k) >= m`.
3. Tests (spec §3 item 1), each a small program lowered and proven as
   the existing `prove_test.go` cases are, plus a regression where both
   a mask rule and the existing guard rule hold (a masked index into an
   array under `if x < 8usize`), which must be `constant`; and boundary
   cases: a mask by 0 into `[1]` (proven), masks of `u8`, `u32` and `u64`
   values converted to `usize`, a negative offset `(x & 7usize) - 1usize`
   into `[8]` (proven), and the largest admitted constant (`x & 2^62`)
   into an array, which is not proven.

**Acceptance.** `go test ./compiler/opt` green; the existing prove
tests unchanged.

## Phase 2: fixtures, differential, measurements, report (R3–R8)

1. Fixtures per R4. `prove_forms.kern` and `prove_forms_not.kern` gain
   the listed functions, called from `main` with inputs that print
   values. Their `.out` files are regenerated from the interpreter and
   checked by eye; the fixture harness runs every `tests/run` program in
   both engines and in both debug and release.
2. `tests/prove_diff_test.go`: the generator gains `masked(p: view[]u64,
   x: usize)` style functions over a fixed `[8]u64` array and a slice:
   - `arr[x & 7usize]`;
   - `arr[(x & 7usize) + d]` for `d` in 0..2;
   - `arr[x & 8usize]` (equal to the length, which can trap);
   - `arr[x & 15usize]` (above the length, which can trap);
   - a masked local;
   - `usize(y & 7u64)`;
   - a guarded slice with `p[x & 3usize]` under `len(p) >= 4usize`.

   It calls them with random `x` and `y`. Run with the default count and
   once with `KERN_DIFF_PROGRAMS=400`.
3. R5: recount with the new compiler; list each label change (program,
   opt level, site text, new `proven_by`); confirm every one is a mask.
4. R6: `go run ./bench/agent/harness -dry-run` and `-tasks-dir
   bench/agent/tasks-v3`, both exit 0.
5. R7: build the new `compiler/opt` test binary; run the two binaries
   alternately, 5 rounds each with `-test.bench BenchmarkProve
   -test.count 1 -test.run '^$'`; for every benchmark, the median ns/op
   before and after and their ratio, which must be at most 1.10.
6. R8: the schema note; `go generate ./compiler/diag`.
7. `docs/reports/prove-masks.md`, and the mandatory gates
   (`golangci-lint run ./...` 0 issues, `go test ./...` green). The
   advisory reviews are `prism review staged --provider openai --model
   gpt-6.1-sol --format json`, `realitycheck check . --spec
   specs/prove-masks/SPEC.md --plan specs/prove-masks/PLAN.md --provider
   openai --model gpt-6.1-sol --format json` (unrelated directories
   ignored) and `verifier analyze -root . -mode offline -spec … -plan …
   -include 'compiler/opt/prove.go' -format text`, with findings fixed or
   justified in the report.

**Acceptance.** Every item above; the report records R5, R6 and R7;
every benchmark's R7 ratio is at most 1.10, or the change is reworked
until it is.

## Risks

- **A wrong proof.** Mitigated by the interpreter's independent
  execution in the differential test, with masks at and above the
  length so traps are expected and must survive.
- **Erasing an agent-suite idiom.** No fourth-suite idiom uses `&`; the
  dry run's calibration check (R6) is the guard.
