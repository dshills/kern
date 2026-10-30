# Agent benchmark, fourth task suite

**Version:** 0.1 (2026-10-30). **Scope:** a new task suite under
`bench/agent/tasks/`; the third suite moved to `bench/agent/tasks-v3/`
with its prompts corrected; calibration solutions and their validation
in the harness's dry run (`bench/agent/harness`); the report
`docs/reports/agent-tasks-v4.md`; the README's status paragraph.
**Language impact:** none. No compiler change. **Cost rule:** no model
is called. The suite is validated with `-dry-run` only, by the owner's
standing decision that matrix runs are theirs to order
(`docs/reports/agent-tasks-v3.md`, "What this leaves unknown").
**Related:** `specs/initial/AI_Native_Systems_Language_SPEC.md` §2.3
(the primary agent metric) and §9 (task kinds);
`docs/reports/agent-tasks-v2.md` (the only matrix that separated the
arms, and why); `docs/reports/agent-tasks-v3.md` (the suite this one
replaces); `docs/reports/prove-guards.md` (the proof pass that made the
third suite's design idioms provable); `docs/reports/agent-metric-revision.md`
(the win rule).

**Review status.** speccritic (OpenAI gpt-6.1-sol, profile general,
with `docs/reports/agent-tasks-v3.md` as context): the first draft was
INVALID on one critical finding (a natural solution had no objective
test); R5's construction rules N1 to N5 and the statement that
representativeness is a hypothesis answer it. Its warning that a
solution could rewrite `main` to print the expected output added R10;
the second round, VALID_WITH_GAPS with no critical finding, asked that
the frozen text be the whole function, which the canonical form
guarantees. The remaining warnings are the preflight's flags on prose.

**Glossary.** R*n*: requirement *n* of §3. Arm: the structured arm
(JSON diagnostics and the semantic query tools) or the plain arm (text
diagnostics, `run`, the files). Reference: the language reference,
grammar, core prelude and two examples both arms receive in the system
prompt (`bench/agent/harness/trial.go`, `systemPrompt`). Runtime check:
an entry of `kern show-bounds-checks` of kind `bounds` labelled
`runtime-checked`, which the harness counts against a task's
`max_runtime_bounds_checks`. Site: one indexing expression of a
program. Idiom: a common way of indexing whose safety a programmer
reads from arithmetic, a value's range or a helper's result, listed in
§1.2. Hoisted guard: the rewrite that proves a site, computing the
index into a local and comparing that local against `len()` of the
indexed place immediately before the access. Natural solution: a
solution written the way a programmer without the query tools would
write it, before any measurement (R5). Matrix: a paid run of the
harness against models.

## 0. Goals and non-goals

Goal: a suite on which a plain-arm agent's habitual program fails the
hidden suite for want of a measurement only the structured arm can
make, at several independent sites per task, so that a plain-arm pass
needs luck at all of them; with the discrimination checked offline,
task by task, against natural solutions.

Non-goals: running a matrix; changing the win rule, the prompts' shared
reference, the tools of either arm, or the compiler; fixing the
analysis gaps §1.3 records; re-scoring the second suite's records.

## 1. Evidence the design rests on

### 1.1 What separated the arms before

The second matrix (200 trials) separated the arms only on the two tasks
that bounded runtime checks: the plain arm failed 10 of its 100 trials,
all there, each time with a correct program it had declared done; on
the copy, allocation, layout, ownership and review tasks both arms met
the bound at or near their first attempt, because the reference states
the rules those bounds follow from. The reference says nothing about
which bounds checks the compiler proves (`docs/ownership.md` has no
mention of proofs), so a bounds budget is the one requirement a
plain-arm agent cannot derive.

### 1.2 Idioms that stay runtime-checked

Measured on 2026-10-30 with the compiler at 54b0a39, after the proof
pass of `docs/reports/prove-guards.md`. Each is safe by reasoning the
pass does not do; each is proven by the hoisted guard (measured on the
same day: every rewritten probe reports 0 runtime checks).

| idiom | example | why it is safe to a reader |
|---|---|---|
| transitive bound | insertion sort's `xs[j - 1usize]`, `xs[j]` with `j <= i < len(xs)` | `j` never exceeds `i` |
| mirrored index | `xs[n - 1usize - i]` with `i < n / 2usize` | `i < n` |
| midpoint | binary search `xs[lo + (hi - lo) / 2usize]` with `lo < hi <= len(xs)` | the midpoint is below `hi` |
| byte into a table | `counts[usize(b)]` for `b: u8`, `counts: [256]u64` | a byte is below 256 |
| offset digit | `counts[usize(c - 48u8)]` under `c >= 48u8 && c <= 57u8`, `counts: [10]u64` | the range check |
| window | `xs[i + k]` with `k < w` and `i + w <= len(xs)` | the window fits |
| last element | `xs[len(xs) - 1usize]` under `len(xs) > 0usize` | the length is positive |
| grid | `g[y * w + x]` under `x < w && y < h`, `len(g) == w * h` | the grid's shape |
| modulo | `xs[i % len(xs)]` under a non-empty check | a remainder is below its divisor |
| argmax | `xs[best]` where `best` is only ever assigned a proven `i` | `best` is an earlier `i` |
| returned index | `xs[k]` where `k := argmax(xs)` | the helper returns an index of `xs` |
| two pointers | `xs[lo]`, `xs[hi - 1usize]` with `lo + 1usize < hi <= len(xs)` | both inside |
| heap children | `xs[i]`, `xs[m]` in a sift-down with `m` a checked child | `i` was a checked child |
| length relation | `ps[i + 1usize]` with `len(ps) == len(xs) + 1usize`, `i < len(xs)` | the stated relation |

### 1.3 Why the other analyses are not used

- **Overflow checks** are never proven (every one in
  `tests/run/prove_forms.kern` is runtime-checked), so their number is
  the number of checked operations, which a reader can count.
- **Allocation sites** come from a fixed table of core operations
  (`compiler/analysis/analysis.go`, `allocating`); calls into `std/str`
  and `std/map` that allocate (`String.new`, `push_u64`, `Map.new`,
  `insert`) and `Vec.extend` are not reported. A budget on them would
  reward moving allocations into those calls. Recorded as a defect of
  the analysis for the report, not fixed here.
- **Library-internal bounds checks** are not reported at the call
  either: a loop over `Vec.get(i)` shows no check at all. Tasks index
  sequences and arrays directly, not through `Vec` methods.
- **Copies, layout, effects and ownership** follow rules the reference
  states, and the second matrix's tasks on them did not separate the
  arms.

## 2. The suite

Ten tasks under `bench/agent/tasks/t01_*` to `t10_*`, in the existing
format (`task.md` with a `Kind:` line, `hidden.json`, `start/`,
`reference/`), plus `calibration/natural/` (R5). Kinds, by §9:
implementation ×3, debugging ×2, refactoring ×1, optimization ×3,
unsafe-contract review ×1.

## 3. Requirements

**R1 — Sites.** Each task's program has at least 6 sites drawn from at
least 3 idioms of §1.2 that its natural solution leaves runtime-checked
(R5), spread over at least 2 required functions, so that a plain-arm
pass needs the right guess at several independent places.

**R2 — Hidden suite.** Each `hidden.json` sets `must_check_clean`, the
exact `expected_output`, `max_runtime_bounds_checks` equal to the
reference's count, `required_functions` naming every function the
prompt says to keep or write, and `no_unsafe_added` (except in the
review task, whose reference must add `unsafe` blocks). `main`
exercises every required function on at least 2 inputs with different
results, so a stub that returns a constant prints a wrong line. Each
`hidden.json` also sets `frozen_functions: ["main"]` (R10), so the
output must come from the given driver calling the solution's
functions, not from a rewritten `main` that prints the expected lines.

**R3 — Prompts.** Each `task.md` states the behaviour, the functions to
keep or write with their signatures, the exact output, and the bound
in the words the harness measures ("at most N runtime-checked bounds
checks, as the compiler's bounds-check analysis reports them"), with N
equal to the hidden budget. It states no proof rule, no idiom of §1.2
and no site.

**R4 — Programs.** Each program is one file of 60 to 160 lines, checks
cleanly in the reference, and uses only the core prelude (no `std/`
import), so that both arms' reference covers everything it calls. Its
reference's `kern run`, compile included, finishes within the harness's
60-second run limit with at least a factor of 10 to spare on the
development host, as the dry run's run of it shows.

**R5 — Natural solutions.** Each task has `calibration/natural/` with
the task's files: a solution with the expected output, the required
functions, the given `main` and a clean check, built by these
inspectable rules:

- **N1.** Every condition in it is needed for the program's behaviour
  on some input: no guard whose false branch no input of the stated
  domain reaches (a reviewer checks each `if` and loop condition that
  mentions `len`).
- **N2.** No index is computed into a local that the program uses only
  to compare and then index; locals hold values the algorithm names
  (`mid`, `best`, `j`, a child index).
- **N3.** No index loop is turned into a `for` over the sequence
  unless the algorithm uses the element and not its position.
- **N4.** For an optimization, refactoring, debugging or review task,
  it is the start with the change the prompt asks for made under N1 to
  N3 and nothing else; for an implementation task, the stubs filled
  under N1 to N3.
- **N5.** It is written from the prompt and the start only, and
  measured after it is written; the report records its measure.

Its runtime checks must exceed the budget by at least 3. That a plain-arm
agent writes programs like it is a hypothesis these rules make
inspectable, not a measured fact; only a matrix tests it (R9 records it
as unknown).

**R6 — Dry-run validation.** `go run ./bench/agent/harness -dry-run`
checks, for every task: the reference passes the hidden suite; the
start fails it; the natural solution, when present, prints the
expected output, checks cleanly and fails the hidden suite on the
bounds budget alone (a failure on anything else is a calibration
error). It prints each one's measures and exits 1 on any violation.

**R7 — Third suite archived and corrected.** `bench/agent/tasks/` of
the third suite moves to `bench/agent/tasks-v3/` unchanged except its
prompts: since f15f773 each `hidden.json` allows 0 runtime checks
while each `task.md` still states the pre-f15f773 budget, so each
prompt's stated bound becomes the hidden budget. Its references still
pass and its starts still fail under `-dry-run -tasks-dir
bench/agent/tasks-v3`.

**R8 — Harness tests.** `go test ./bench/agent/harness` covers the
calibration loading (a task with and without `calibration/natural/`)
and the rule that a calibration failing on anything but the budget is
reported as an error.

**R10 — Frozen functions.** `hidden.json` gains `frozen_functions`, a
list of top-level function names whose source text in the solution must
equal the start's: from the line that begins `func <name>(` to the next
line that is exactly `}`, compared byte for byte. Every start is in
the canonical form of `kern format`, which indents every brace inside a
function, so in the start that line is the function's own closing
brace and the extracted text is the whole function; a solution whose
text stops early at an unindented brace of its own differs from it and
fails. A missing function or
a different text fails the hidden suite with a message naming it. The
dry run applies it like every other hidden check.

**R9 — Documents.** `docs/reports/agent-tasks-v4.md` records §1's
measurements, each task's idioms and measures (start, reference,
natural solution), the R7 correction, the analysis gaps of §1.3, and
what stays unknown without a matrix. The README's status paragraph
names the fourth suite as written and validated, not run.

## 4. Acceptance criteria

1. `go run ./bench/agent/harness -dry-run` exits 0 with ten tasks, each
   reporting reference passes, start fails, natural solution fails on
   the budget only (R1, R2, R5, R6).
2. `go run ./bench/agent/harness -dry-run -tasks-dir bench/agent/tasks-v3`
   exits 0, and each v3 prompt's stated bound equals its hidden budget
   (R7).
3. `go test ./bench/agent/harness` passes (R8), including a test of
   R10's comparison: an unchanged `main` passes, an edited one and a
   missing one fail.
4. A script or test reports, per task, the idioms of §1.2 its natural
   solution leaves runtime-checked and their count (R1), recorded in
   the report.
