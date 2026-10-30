# Plan: agent benchmark, fourth task suite

**Spec:** `specs/agent-suite-v4/SPEC.md` v0.1. **Profile:** davin-go.
**Review status.** plancritic (davin-go, with the spec): three rounds of
EXECUTABLE_WITH_CLARIFICATIONS (84, 79, then this revision), no
critical finding; folded in: budget-independent calibration
validation, the prompt frozen before the natural solution, a third
idiom for t04, t05 and t09, the enforced idiom mapping with `other`
sites, the mechanical size, import, time and `unsafe` checks, the
frozen check before the output check, and the gate commands.
**Order:** five phases; Phases 1 and 2 end with `golangci-lint run ./...`
and `go test ./bench/agent/...` green, Phase 3 with the dry run green on
the new suite, Phase 5 with the full gates. One commit after Phase 5
unless the owner asks for more. **No model is called in any phase.**

## Phase 1: harness (R6, R8, R10)

**Files.** `bench/agent/harness/main.go`, `trial.go`,
`harness_test.go`.

**Steps.**

1. `hiddenSpec` gains `FrozenFunctions []string \`json:"frozen_functions"\``.
   `hiddenPasses` checks it before the output check, so an edited
   `main` that also changes the output is reported by name: for each name,
   `frozenText(dir, file, name)` scans the task's files in order for the
   first line beginning `func <name>(` and returns the lines through the
   next line that is exactly `}`; the solution's text must equal the
   start's (`filepath.Join(t.Dir, "start")`); missing in the solution is
   "function <name> is missing", different is "function <name> was
   changed", with the frozen function's own name.
2. `task` gains `Natural string`: `filepath.Join(t.Dir, "calibration",
   "natural")` when that directory exists, else "". `loadTasks` sets it.
3. Dry run, for a task with `t.Natural != ""`, in two independent
   steps so that no failure masks another:
   - **Everything but the budget.** `hiddenPasses` runs on the natural
     solution with a copy of the task whose `MaxRuntimeBoundsChecks` is
     nil; it must pass (expected output, clean check, required and
     frozen functions, no unsafe added). A failure here is a
     calibration error naming the check.
   - **The budget.** `countRuntimeBoundsChecks` on the natural solution
     must be at least the budget plus 3; less is a calibration error
     ("no discrimination" when it is within the budget).
   The two results feed a pure function `classifyNatural(rest bool,
   restDetail string, runtime, budget int) (ok bool, why string)`.
   The dry run prints the natural solution's measures beside the
   start's and the reference's, then lists every runtime-checked site of the natural
   solution as `function: source text` (spans from `kern
   show-bounds-checks`, the function found by the enclosing
   `func` line), and when `calibration/idioms.json` exists checks it
   (step 4). A calibration error sets the exit status to 1.
4. `calibration/idioms.json` is the audited mapping
   `[{"function": f, "site": text, "idiom": name}]`, one entry per
   runtime-checked site of the natural solution. The dry run requires:
   the entries are exactly the listed sites (same function and text,
   same multiplicity); every idiom is one of spec §1.2's fourteen names
   (kept as a list in the harness) or `other`; the entries other than
   `other` (the qualifying sites) number at least 6, with at least 3
   distinct idioms and at least 2 distinct required functions (R1). An
   `other` site is listed and does not count.
5. Mechanical checks in the dry run for every task of the default
   suite (`-tasks-dir bench/agent/tasks`): each start, reference and
   natural file has 60 to 160 lines and no `import "std/`; the
   reference's `kern run` (compile included) takes at most 6 seconds;
   for a task of kind `review`, the reference's `unsafe` line count
   exceeds the start's. Violations are errors.
6. Tests in `harness_test.go`:
   - `TestFrozenText`: a canonical `main` is extracted whole and an
     unchanged copy compares equal; an edited `main` and a missing one
     fail with messages naming the function; an indented nested `}`
     does not end the extraction.
   - `TestIdiomAudit`: the mapping check accepts an exact mapping and
     rejects a missing site, an extra site, an unknown idiom, fewer than
     6 sites, fewer than 3 idioms and one function.
   - `TestLoadTasksNatural`: a task directory with and without
     `calibration/natural/` sets `Natural` accordingly.
   - `TestClassifyNatural`: rest failing is an error even when the
     budget is also exceeded (the simultaneous case); rest passing with
     runtime ≥ budget + 3 is intended; within the budget, and over it by
     less than 3, are errors.
   - An integration test runs the two-step validation on a scratch task
     whose natural solution both exceeds the budget and prints a wrong
     line, and expects the error to name the output, not the budget.

**Acceptance.** `go test ./bench/agent/harness` green; lint clean.

## Phase 2: archive the third suite (R7)

**Steps.**

1. `git mv bench/agent/tasks bench/agent/tasks-v3`.
2. In each `tasks-v3/*/task.md` that states "at most N runtime-checked
   bounds checks", set N to the task's `hidden.json` budget (0) and
   remove the parenthetical that counts `main`'s slices, which f15f773
   made proven; t01 and t06 state the budget in other words, which are
   checked and corrected the same way. Nothing else in the suite
   changes.
3. Verify: `go run ./bench/agent/harness -dry-run -tasks-dir
   bench/agent/tasks-v3` exits 0, and a grep of every prompt's stated
   bound against its `hidden.json` shows equality.

**Acceptance.** Both checks above.

## Phase 3: the ten tasks (R1–R5)

**Files.** `bench/agent/tasks/t01_*` … `t10_*`, each with `task.md`,
`hidden.json`, `start/`, `reference/`, `calibration/natural/`.

The idiom names are spec §1.2's. Each task's program is one file
(R4); `main` is the frozen driver (R10) and is identical in start,
reference and natural solution.

| task | kind | program | idioms its natural solution keeps |
|---|---|---|---|
| t01_sorted_set | implementation | insertion sort, binary-search `contains`, in-place `dedup`, `rank` | transitive bound, midpoint, two pointers |
| t02_byte_stats | implementation | byte histogram, digit counts, most frequent byte, longest run | byte into a table, offset digit, argmax, last element |
| t03_ring_log | implementation | fixed-capacity ring log: `push`, `get(i)` from the oldest, `last`, sum of the newest k | modulo, last element, window |
| t04_image_ops | optimization | 3×3 box blur with wrap-around edges, horizontal flip and row sums over a flat w×h grid | grid, mirrored index, modulo |
| t05_range_sums | optimization | prefix sums, range sums, best window, first window reaching a target by binary search over the prefix sums | length relation, window, midpoint |
| t06_text_tools | optimization | in-place reverse, palindrome test, rotate by k, trailing-byte test | mirrored index, two pointers, modulo, last element |
| t07_heap_queue | debugging | binary min-heap with a wrong child comparison that misorders pops | heap children, transitive bound (parent), last element |
| t08_top_scores | debugging | selection sort and top-k by an argmax helper, with a wrong-output bug | argmax, returned index, transitive bound |
| t09_matrix_helpers | refactoring | duplicated `r * n + c` arithmetic moved into required helpers `at` and `put`; flip and lower-triangle symmetrization | grid (inside the helpers), mirrored index, transitive bound |
| t10_buffer_review | review | raw-pointer wrappers missing `unsafe` and contracts, plus safe byte code | byte into a table, last element, modulo |

**Steps, per task, in this order** (N5: the natural solution is
written from a frozen prompt and start):

1. Write `start/` in canonical form (`kern format`): for an
   implementation task, the required functions as stubs that return a
   zero value and the full `main`; for the others, the working (or, for
   debugging and review, failing) program. `main` calls every required
   function on at least 2 inputs whose results differ.
2. Write `task.md` per R3 with the budget written as `N` to be filled;
   from here on the prompt and start are frozen except that number.
3. Write `calibration/natural/` under N1 to N5 from the prompt and the
   start only; only then measure it.
4. Write `reference/`: the natural solution with every runtime-checked
   site's index hoisted and guarded, and measure it; its count is the
   budget, which fills `N` in `task.md` (the only later prompt edit).
5. `hidden.json`: the reference's output and budget, the required
   functions, `frozen_functions: ["main"]`, `must_check_clean`,
   `no_unsafe_added` (not in t10).
6. `calibration/idioms.json` from the dry run's site listing, each site
   mapped to its §1.2 idiom by reading it.
7. `go run ./bench/agent/harness -dry-run -tasks <task>` until every
   check of Phase 1 steps 3 to 5 passes.
8. Record in the report, per task, how many inputs `main` gives each
   required function (R2, checked by reading `main`).

The ten tasks are independent (separate directories, no shared file),
so they may be written in parallel by separate workers, each given this
plan, the spec and the idiom table; every result is reviewed against
N1 to N5 and the dry run before it is accepted.

**Acceptance.** `go run ./bench/agent/harness -dry-run` exits 0.

## Phase 4: the idiom record (spec §4 item 4)

The `calibration/idioms.json` files, enforced by the dry run, are the
record; the report's table is generated from them (idioms and counts
per task). A site that matches no §1.2 idiom is mapped `other` and does
not count toward R1.

**Acceptance.** The report's table matches the ten mapping files.

## Phase 5: documents and gates (R9)

1. `docs/reports/agent-tasks-v4.md` per R9.
2. README status paragraph: the fourth suite is written and validated,
   not run; the third moved to `tasks-v3/`.
3. Mandatory gates, which must pass unconditionally:
   `golangci-lint run ./...` (0 issues), `go test ./...`,
   `go run ./bench/agent/harness -dry-run` and
   `go run ./bench/agent/harness -dry-run -tasks-dir bench/agent/tasks-v3`
   (both exit 0).
4. Advisory reviews, as in every earlier change (findings fixed or
   justified in the report): `prism review staged --provider openai
   --model gpt-6.1-sol --format json`; `realitycheck check . --spec
   specs/agent-suite-v4/SPEC.md --plan specs/agent-suite-v4/PLAN.md
   --provider openai --model gpt-6.1-sol --format json` with the other
   specs and the compiler ignored; `verifier analyze -root . -mode
   offline -spec specs/agent-suite-v4/SPEC.md -plan
   specs/agent-suite-v4/PLAN.md -include 'bench/agent/harness/**'
   -format text`. Each needs only the tools on the development host and
   the OpenAI key the earlier changes used.

**Acceptance.** The mandatory gates pass; the advisory findings are
recorded; the README names the fourth suite as not run.

## Risks

- **A natural solution written with knowledge of §1.2.** The writer of
  the calibration knows which idioms fail. N1 to N5 make the solution
  inspectable for added or omitted guards; the review in Phase 3
  checks every `len` condition against N1 and every local against N2.
- **The proof pass may grow again.** A later pass that proves these
  idioms erases this suite as f15f773 erased the third; the dry run
  then shows the natural solutions passing, which R6 reports as a
  calibration error, so the decay is visible rather than silent.
- **Workers in parallel.** Each worker touches only its task
  directories; the harness and the third suite are finished before any
  starts.
