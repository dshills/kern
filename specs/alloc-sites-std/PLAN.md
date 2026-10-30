# Plan: allocation sites through library calls

**Spec:** `specs/alloc-sites-std/SPEC.md` v0.1. **Profile:** davin-go.
**Review status.** plancritic (davin-go, with the spec):
EXECUTABLE_WITH_CLARIFICATIONS, 86, no critical finding; its
clarifications (what "start" is, the gate commands, the note's fields,
no re-scoring, a recursive inventory with exclusions listed, lowering
failures not skipped) are folded in.
**Order:** three phases, each ending with `golangci-lint run ./...` and
`go test ./...` green; one commit after Phase 3.

**Baseline (before Phase 1):** a compiler built from b49589f into the
session scratch (`kern-base`), used for R7's before column and for
cross-checking R1 by hand on the examples.

## Phase 1: the analysis (R1–R4)

**Files.** `compiler/analysis/effects.go`, `compiler/analysis/analysis.go`.

**Steps.**

1. Factor the summary computation out of `Effects` into
   `effectSummaries(mod *ir.Module) map[string]*effects`, returning the
   fixed-point summaries by function name; `Effects` builds its result
   from it unchanged (its output is byte-identical: the existing effects
   tests pin it).
2. In `Allocations`, compute the summaries once. For each call in a user
   function, apply the table rule exactly as now (R1); when it does not
   apply and the call is statically resolved (`Fn == nil`,
   `Intrinsic == ""`, `mod.Func(c.Callee)` non-nil) to a function with
   `Library` set whose summary allocates, add a site with `allocator`
   `"system"`, `size` nil and `label` `unknown` (R2).
3. Correct the table (spec R1): remove `Vec.new`, `Pool.new` and
   `Arena.new`; make `Vec.with_capacity` conditional. A library entry of
   the table yields a site only when its callee's summary allocates.
4. Keep the existing deduplication and ordering.

**Acceptance.** `go test ./compiler/analysis` green; §1's program shows
the four library sites, no site for `String.new()` or `Vec.new()`, and
a `Box.new` table site.

## Phase 2: tests and the note (R5, spec §4 items 1–3)

**Files.** `compiler/analysis/analysis_test.go`,
`compiler/diag/schema.go`, `tools/agent-api/schema.md` (generated).

**Steps.**

1. `TestLibraryAllocationSites`: §1's program plus a `pure` function
   that sums a `view[]u64`;
   asserts the five sites, their fields, `main`'s table site, and no site
   in `pure`, in debug and release.
2. `TestAllocationSitesAgreeWithEffects`: for every `tests/run/*.kern`
   that resolves and checks cleanly, in both opt levels (a program that
   checks cleanly but fails to lower fails the test; one that does not
   check is skipped, as the existing fixture loops do):
   - the table rule alone (a test helper `tableSites`, the rule as it
     stood, kept beside the analysis for this purpose) gives sites that
     all appear in `Allocations` with equal fields (R1);
   - every statically resolved call from a user function to a library
     function whose summary allocates is at a site location (R3);
   - every user function whose summary allocates has its own site, or
     calls an allocating user function, a function value or an extern
     function (R3's consequence).
3. `TestAllocationTableEntriesAllocate` (spec R8): one program that
   calls every library entry of the table (`Box.new`, `Vec.push`,
   `Vec.with_capacity`, `Pool.alloc`, `Arena.alloc`, a deep `clone`);
   each callee's summary must allocate. Checked once by hand to fail
   with `Vec.new` added back.
4. The note for `show-allocations` in `compiler/diag/schema.go` gains
   the two kinds of site and says a library site has label `unknown`
   and size `null`; `go generate ./compiler/diag` regenerates
   `tools/agent-api/schema.md`; the staleness test passes.

**Acceptance.** `go test ./compiler/...` green, `git diff` clean after
`go generate ./compiler/diag`.

## Phase 3: suites, programs, report (R6, R7)

**Steps.**

1. `go run ./bench/agent/harness -dry-run -tasks-dir bench/agent/tasks-v2`
   and `-tasks-dir bench/agent/tasks-v1`: each must exit 0, which the
   harness gives only when every task's reference (`<task>/reference/`)
   passes its hidden suite and every task's starting sources
   (`<task>/start/`, printed as "starting sources fail as intended")
   fail it. Where a reference now exceeds its `max_allocations`, raise
   the budget to the reference's count and record the old and new
   values and why. The recorded matrix runs under `bench/agent/runs/`
   are not re-scored (spec R6).
   Also run the default (fourth) suite's dry run, which has no
   allocation budget, to confirm nothing changed there.
2. Count sites with `kern-base` and the new compiler for every `.kern`
   file found recursively (`find bench/kern examples -name '*.kern'`),
   each run as `show-allocations <file>` in debug from its own
   directory so a `kern.mod` project is loaded as `kern check` would;
   tabulate before and after, and list every file that does not check
   (a test file, a module file of a project) with the reason, so the
   table accounts for every file. No timed harness run (spec R7).
3. `docs/reports/alloc-sites-std.md`: the defect, the rule, the counts,
   the suite outcomes, the reviews. `docs/reports/agent-tasks-v4.md`'s
   "Defects found on the way" points to it.
4. Mandatory gates, not waivable: `golangci-lint run ./...` with 0
   issues; `go test ./...` green; `go generate ./compiler/diag` leaving
   no diff; the dry runs above exiting 0.
5. Advisory reviews, as for every change in this project, findings
   fixed or justified in the report: `prism review staged --provider
   openai --model gpt-6.1-sol --format json`; `realitycheck check .
   --spec specs/alloc-sites-std/SPEC.md --plan
   specs/alloc-sites-std/PLAN.md --provider openai --model gpt-6.1-sol
   --format json` with unrelated directories ignored; `verifier analyze
   -root . -mode offline -spec … -plan … -include
   'compiler/analysis/**' -format text`.

**Acceptance.** Every mandatory gate passes; the report's table
accounts for every file; advisory findings are recorded.

## Risks

- **Counts rise in the benchmark report.** Workloads that use `std/`
  calls gain sites at the next harness run. The report's before/after
  table gives the reader the explanation in advance.
- **A library function that allocates only on an error path** (a growth
  that never happens for the program's inputs) is still a site; it is
  labelled `unknown`, which is what the label means.
