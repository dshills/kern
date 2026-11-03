# Why a bounds check is not proven: what was built, and the reasons offline

**Date:** 2026-11-03. **Spec:** `specs/unproven-reasons/SPEC.md`.
**Plan:** `specs/unproven-reasons/PLAN.md`, five phases, each reviewed and
committed on `main` (8afbd5c, 58dfa8f, 9a0b6ac, 090cc28, and this phase).
**No model was called:** the evaluation below is offline, and a paid run
of the plain arm with hints is the owner's decision.

## Why

In the paid run of the budgeted suite (`docs/reports/declared-budgets.md`),
20 of the plain arm's 30 failures ended with `check` reporting only
`BUDGET_EXCEEDED`. The agents were told where every remaining check was
and did not know which rewrite the proof rules accept; the arms that had
`docs/bounds-checks.md` got that from the document.

## What was built

- **Reasons.** Every runtime-checked bounds check gets one of nine
  reasons, judged from the source of the function that contains the
  access: `guard_not_on_every_path`, `guard_too_weak`, `other_length`,
  `type_range`, `remainder_by_value`, `computed_index`, `no_guard`,
  `slice_high_bound`, `slice_low_bound`. Each comes with a hint naming the
  access's own expressions (`docs/bounds-checks.md`, "Why a check is not
  proven"). The proof pass is unchanged except that it records which
  bound of a slice it failed.
- **`show-bounds-checks`** gives `reason` and `hint` at `schema_version`
  0.5. Requests at 0.2 to 0.4 get their old answer, byte for byte.
- **`BUDGET_EXCEEDED`** follows each location of a `bounds_checks` item
  with its hint, separated by `; `. A plain-text agent now reads, for the
  natural t02 solution of the earlier report:

```
function `histogram` has 1 runtime-checked bounds check, budget 0: 10:9 `counts[usize(b)]` (the range of `u8` is not a bound the rules use: mask the index, `usize(b) & 255usize`)
```

- **Cost.** Classification runs only for a 0.5 `show-bounds-checks` and
  for an exceeded `bounds_checks` item. `check` on the 452 budget-free
  files under `tests/` is byte-identical to the previous binary's.
- **Identity.** Against the compiler before this work (28f2751), with the
  wire version normalized, everything compiled is byte-identical: the
  LLVM IR `build` compiles for 316 single-file programs at both levels
  (`scripts/capture-ll.sh`; the same 48 build with neither binary),
  `emit-ir` at both levels and `emit-asm` for 182 files, `run` output and
  exit status for 158 programs of `tests/run` and `tests/trap`, and IR,
  assembly and `run` for the 36 other programs under `tests/` the old
  compiler builds and the 30 references of the three agent suites. The
  linked executables are not compared: two builds by one binary already
  differ on this host.

## The reasons offline

Three sets, all at `--opt=debug`. *Run*: the final program of every
trial directory of the paid run (`bench/agent/runs/20261102-134710`,
git-ignored), taking the 22 of 100 whose `check` reports
`BUDGET_EXCEEDED`, and every runtime-checked bounds check of those (every
function has `budget { bounds_checks 0 }`, so these are exactly the
over-budget locations, with no 10-location limit). *Natural*: the ten
natural solutions of suite 4, whose checks are exactly the locations the
budgeted suite reports for them. *Sweep*: every reference, start and
natural solution of `bench/agent/tasks` and `tasks-budgeted` that checks,
at both levels (the test `TestReasonsSuiteSweep`, 116 programs).

| reason | run | natural | sweep |
|---|---|---|---|
| `no_guard` | 42 | 41 | 278 |
| `computed_index` | 15 | 30 | 252 |
| `slice_high_bound` | 13 | 1 | 8 |
| `type_range` | 5 | 3 | 12 |
| `remainder_by_value` | 4 | 6 | 32 |
| `other_length` | 3 | 2 | 10 |
| `guard_not_on_every_path` | 0 | 0 | 0 |
| `guard_too_weak` | 0 | 0 | 0 |
| `slice_low_bound` | 0 | 0 | 0 |
| total | 82 | 83 | 592 |

The run's 82 are more than the 74 counted in the earlier report, which
read each agent's last `check` output: that output lists at most 10
locations per function and can precede the agent's last edit. The 22
directories are two more than the 20 failures the earlier report counted
as ending with only `BUDGET_EXCEEDED`; the trial directories are not
linked to their records, so the two counts are not reconciled here.

Every one of the 592 sweep entries was located in the source; none fell
back to the fixed hint for an unlocated access.

### Ten hand checks

Each access below, from a run's final program, was rewritten in a
scratch copy as its hint says, and the copy's `show-bounds-checks` read
for the rewritten access.

| program | access | reason | rewrite | result |
|---|---|---|---|---|
| t01 3624232558 | `xs[mid]` | `no_guard` | `if mid < hi && mid < len(xs)` | proven |
| t01 3624232558 | `xs[j - 1usize]` | `no_guard` | `j - 1usize < len(xs) && ...` in its condition | proven |
| t06 2540823480 | `s[n - 1usize - i]` | `computed_index` | `k := n - 1usize - i; if k < len(s) { ... s[k] }` | proven |
| t03 681798752 | `xs[i - c]` | `computed_index` | `k := i - c` then `if k < len(xs)` | proven |
| t04 2444493704 | `src[i % len(src)]` | `remainder_by_value` | `k := i % len(src)` then `if k < len(src)` | proven |
| t10 3376311375 | `counts[usize(b)]` | `type_range` | `counts[usize(b) & 255usize]` | proven (`constant`) |
| t04 961917248 | `b[i]` (loop on `len(a)`) | `other_length` | `if i < len(b) { b[i] = a[i] }` | proven |
| t05 877116110 | `ps[i]` (loop on `len(xs)`) | `other_length` | `if i < len(ps)` | proven |
| t04 3450304529 | `u[0usize:w]` | `slice_high_bound` | v0.1 hint: `k := w - 1usize; if k < len(u) { u[0usize:k + 1usize] }` | **runtime-checked** |
| | | | v0.2 hint: `u[0usize:len(u)]`, `if x < len(r)` on each index | proven |
| t04 95975594 | `g[i:i + w]` | `slice_high_bound` | v0.1 hint: `e := i + w - 1usize; if e < len(g) { g[i:e + 1usize] }` | **runtime-checked** |
| | | | v0.2 hint: `if i < len(g) { g[i:len(g)] }` | proven |

Every index hint's rewrite is proven. The slice hint as first specified
(v0.1) was not enough: it said, correctly, that a high bound is proven only as `len(X)`, a literal
within an array's length, or `v + c` under `v + (c - 1) < len(X)`, but
the obvious way to write that form, a local `k := w - 1usize` and the
bound `k + 1usize`, is read back by the proof pass as `w` plus 0, so the
rule does not apply. The form works when `v` is a parameter or any value
that is not itself an offset (`if k < len(u) { u[0usize:k + 1usize] }`
with `k` a parameter is proven). What proves the two accesses above is to
slice to the length and guard the indexes instead
(`with r := u[0usize:len(u)] { ... if x < len(r) { r[x] } }`, and
`if i < len(g) { g[i:len(g)] }`), which changes what the code does when
`w` is out of range. The spec was amended (v0.2, §3 item 8) and the
hint now ends with that rewrite, written with the access's own place
and low bound:

```
... `w <= len(u)` does not prove it: slice to `len(u)` instead, `u[0usize:len(u)]`, and test each index below the slice's length
```

With it both accesses are proven (the v0.2 rows above), as are the
fixtures `f8` and `f8b` of `compiler/analysis/reasons_test.go`.

## What this shows, and what it does not

- For every index form the agents left over budget, `check` now names a
  rewrite the rules prove, and the eight index checks above confirm it on
  their own programs.
- `slice_high_bound` is 13 of the run's 82 locations (16%). Its v0.1
  hint did not lead to a proven rewrite; the amended hint does, at the
  cost of not trapping when the bound is past the end. Closing the proof
  gap of spec §6 (`n <= len(h)` proving `h[0usize:n]`) would keep the
  trap, and would also change the agent suites' calibration.
- None of the run's locations had a guard that missed a path or was too
  weak: the agents' remaining checks had no guard on the indexed length
  at all, or a computed index. The two guard reasons are covered only by
  the fixtures.
- Nothing here measures agents. Whether the plain arm, given these hints,
  closes the gap to the documented arm (Flash 28 vs 44 of 50, Kimi 42 vs
  45) needs a paid run of the plain arm on `bench/agent/tasks-budgeted`,
  the owner's decision. That run's query arms would also see the new
  fields: the harness report now says so.
