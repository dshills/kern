# Plan: proof of the common bounds guards

**Source spec:** `specs/prove-guards/SPEC.md` v0.3, speccritic
VALID_WITH_GAPS with no critical finding. **Plan gate:** plancritic
(profile davin-go, with the spec) EXECUTABLE_WITH_CLARIFICATIONS, no
critical finding; its question on equality classes of three or more
lengths and its three warnings (solver tests, a compile-time gate,
short-circuit tests) are answered in §0 and the phases. **Conventions:** every phase
ends with `go test ./...` and `golangci-lint run ./...` clean, a `prism
review staged` with no high or medium finding left unanswered, and one
conventional commit on `main`; `tests/prove_diff_test.go` is run with
`KERN_DIFF_PROGRAMS=400` before the final commit. Form labels (F1–F13),
constraint labels (C1–C3) and acceptance labels (A1–A6) are the spec's.

## 0. Decisions that apply to every phase

- **State.** `facts` gains `equiv map[*ir.Value]*ir.Value` (the
  load/length equalities and F6's value numbering, today the prover's
  global `equiv`), `sameLen map[*ir.Value]*ir.Value` (F4), and keeps
  `lt`, `locals`, `lens`; `lt`'s value becomes `int64` (F7). `meet(a, b
  *facts) *facts` is the intersection of §2: `lt` keeps a key present in
  both with the smaller k; the maps keep an entry with the same value on
  both sides. `canon(s, v)` follows `s.equiv` (flow-sensitive), then the
  global `alens` equivalence for access-rooted lengths and the array
  length canonicalisation of F5. The global `clen` (array length by value) stays. F4's same-length facts form equivalence classes: `s.sameLen` maps a
  length key to its class representative (the member with the smallest
  value ID), `lt` facts are recorded and looked up under the
  representative, and an `==` edge unions two classes and rekeys the
  `lt` facts of the merged class to the new representative, so three or
  more equal lengths share one key. A `constant` proof against a class
  succeeds when `clen` is known for any member (the class keeps the
  smallest known constant length). The meet of two states keeps two keys
  in one class only when both states have them in one class (the
  partition is intersected) and rekeys the surviving facts.
- **Iteration (F1, C1).** The solver is a round-based must-analysis
  with intersection as the meet. Blocks are visited in reverse
  postorder; kern's CFGs come from structured control flow and are
  reducible, so a back edge always targets a loop header. Round 1:
  `in[b]` is the meet over the predecessors already visited in the
  round of `out[p]` plus `edgeFacts(p, b)`; a back-edge predecessor
  (not yet visited) is left out, which is the optimistic start. Round k
  > 1: `in[b]` is the meet over every predecessor, using `out[p]` of
  this round when p was visited earlier in the round and `out[p]` of
  round k - 1 otherwise (the back edges). Because the meet only removes
  facts and `instr` is monotone (fewer facts in, fewer facts out),
  every `out[b]` of round k is a subset of round k - 1's, so the rounds
  stop when no `out[b]` changed, and at that point `in[b]` equals the
  meet over all predecessors of their current `out`, which is the
  sound fixed point. The rounds are capped at 8. If the cap is reached
  without stabilising, one final pessimistic round runs in which every
  loop header (a block with a back-edge predecessor) starts from no
  facts and every other block's `in` is the meet of its predecessors'
  `out` from this same round (all visited earlier in RPO): every state
  of that round holds on every path, so marking from it is sound.
  Provenance is assigned in a marking pass over the final round's `in`
  states only; the rounds never touch `CheckBounds.Prov`. States are
  compared by a canonical rendering (sorted keys, value IDs).
- **Array lengths (F5).** `lenOf(s, v)`: when `clen[canon(v)]` is known,
  or `v` is a literal, the length key is a synthetic value per length
  (`p.constLen[n]`, one `*ir.Value` per n, allocated by the prover), so
  a `len` of an `[8]u32` place and the literal `8usize` share a key. The
  synthetic values never enter the IR.
- **Edge facts (F2–F4).** `edgeFacts` handles `<`, `>`, `>=`, `<=` on
  both edges (strict on the strict edge, F3's offset rule on the
  non-strict edge) and `==` (literal: `K-1 < other` on the then edge;
  two lengths: `sameLen` both ways on the then edge). Branch conditions
  are read through `canon`.
- **Value numbering (F6).** In `instr`, `*ir.Binary` and `*ir.Conv`
  (checked and truncating) look up `(op, mode/to, canon(L), canon(R))` in
  `s.vn map[string]*ir.Value` (part of `facts`, intersected like the
  rest); a hit records `s.equiv[dst] = hit`, a miss records the value.
  Literals: `*ir.Const` of unsigned type keys by `(type, value)` the same
  way, so equal literals are one value.
- **Index reading (F7).** `index(s, v) (base *ir.Value, off int64)`
  reads checked `+` and `-` of a constant on unsigned operands (mode
  empty), recursively, with overflow of the offset arithmetic refusing
  the reading. `addLt` and the lookup use `int64`.
- **Stores and calls (F8).** `invalidate` splits into forgetting
  `locals` (a write to the local's contents) and forgetting `lens`; a
  store whose path ends in an index element, a call receiving a sequence
  access, and a call receiving a scalar access to an array place forget
  `locals` under the root but not `lens`; `lens` for array places are
  constant through F5 anyway, so the only `lens` that can still be
  forgotten are those of non-array places, which the existing rules
  forget as today.
- **Slices (F9).** `*ir.CheckBounds` with `Lo`/`Hi` (index nil) is
  decided by the predicate of F9 over `index(s, Lo)`, `index(s, Hi)`,
  `lenOf(s, Len)` and `s.lt`; label `constant` when both bounds are
  literals under an array length, else `dominated_by_check`. Past a
  passed slice check, `Hi` is in range (`addLt(base(Hi), off(Hi)-1,
  len)` when off ≥ 1) as an index check records its index.
- **Conditions (F2).** `lower.fnLower.condBr(e ast.Expr, then, els
  *ir.Block)`: for `*ast.Binary` with `&&`/`||` and `*ast.Unary` with
  `!`, branches as the spec says, recursively; otherwise evaluates the
  value and emits `CondBr`. `ifStmt` and `whileStmt` call it; `binary`'s
  value-position lowering is unchanged.
- **Labels (F11).** Only `loop_bound`, `dominated_by_check`, `constant`.

## Phase 1 — The pass: state, meet, iteration, forms F1, F3–F9

**Goal.** Every form provable from the IR as lowered today is proven;
the differential stays green.

**Files.** `compiler/opt/prove.go` (restructured as §0 says; `prove`
becomes the round driver and `mark` the final pass; `instr` takes the
state and a `mark bool`); `compiler/opt/prove_test.go` (new: unit tests
over small IR built by lowering kern snippets with the existing
`lowerSrc` helper of `inline_test.go`, asserting the provenance of each
check by its position: cached-length loop, `<=` and `==` guards, a
recomputed index, `i - 1`, a range loop, a literal against an array
length, two stores under one guard, an access after a call, guarded
slices, an `if` between guard and access, and the negative cases of §3
staying runtime-checked); `tests/prove_diff_test.go` (F10: the generator
gains helper functions for each form and call sites that pass and fail
their guards).

**Acceptance.** `go test ./compiler/opt ./tests -run
'TestProve|TestProvenChecksDifferential'` passes, also with
`KERN_DIFF_PROGRAMS=400`; every example prints the same output; the
example counts (A4) do not rise.

**Solver tests** (`compiler/opt/prove_test.go`): a loop whose body
stores to the guarded index variable (the back edge removes the fact and
the check stays runtime-checked); a nested loop where the inner guard
depends on the outer; a loop whose first round proves a check that the
back edge later invalidates (the fixed point must not keep it);
convergence in two rounds on the cached-length loop; and the forced
fallback (an exported `maxRounds` variable set to 1 in the test) leaving
every loop-body check runtime-checked while straight-line checks stay
proven.

**Compile-time gate.** `go test ./compiler/opt -bench BenchmarkProve
-run ^$` over `examples/selfhost/lexer.kern` and a generated function
with 200 consecutive guarded accesses, recorded before and after in the report; the
change is accepted when the after time is at most twice the before time
on the same host (the lexer takes 3.5 ms of `kern check` end to end
today, so the budget is generous).

**Risks.** Termination of the rounds (bounded, with the pessimistic
fallback); a stale `equiv` from an optimistic round leaking into marking
(marking reads only the stable `in` states, and `equiv` lives in them);
the pass's cost on the largest workload (gated above).

## Phase 2 — Conditions as control flow (F2)

**Goal.** `&&`, `||` and `!` in `if` and `while` conditions branch
directly, so their comparisons give edge facts.

**Files.** `compiler/lower/stmts.go` (`condBr`, used by `ifStmt` and
`whileStmt`), `compiler/lower/lower_test.go` (A6: the `emit-ir` text of
the spec's function holds no `sc.rhs` block and two proven checks),
`tests/run/prove_forms.kern` and `tests/run/prove_forms_not.kern` (A1,
with their `.out` files, run through the fixture harness under the
interpreter and both native levels), `docs/reports/agent-tasks-v3.md`
(a note under its idiom table pointing at the new report).

**Short-circuit tests** (`tests/run/short_circuit.kern`, run under the
interpreter and both native levels): nested `&&`, `||` and `!` in `if`
and `while` conditions whose operands print a letter when evaluated and
whose second operand would trap (`xs[i]` out of range, division by zero)
when the first is not short-circuiting; the expected output pins the
order and count of evaluations and the absence of the trap.

**Acceptance.** A1 and A6; `go test ./...`; the inliner's tests (which
inspect lowered blocks) unchanged in outcome.

**Risks.** Evaluation order and count of the operands are unchanged by
construction (each operand is evaluated once on the path that reaches
it); the `sc.join` block's disappearance changes block numbering in
`emit-ir` golden strings, if any test holds one (checked by running the
suite).

## Phase 3 — Reporting, the harness description and the suites

**Goal.** What the pass proves is described truthfully where users and
agents read it, and the suites that count checks are re-calibrated.

**Files.** `bench/agent/harness/trial.go` (F12: the `show_bounds_checks`
tool description), `bench/agent/tasks/*/hidden.json`, `bench/agent/tasks-v2/*/hidden.json`
and `bench/agent/tasks-v1/*/hidden.json` (F13: `max_runtime_bounds_checks`
lowered to each reference's new count where it fell; a task whose start
now meets its budget is listed in the report), `docs/reports/prove-guards.md` (before/after counts on the examples and
the three suites, the compile-time gate's numbers, the forms proven, the
structured-arm wording change for the owner, A2's calibration programs).

**Acceptance.** A2, A4's table, A5: `go run ./bench/agent/harness
-dry-run` on `bench/agent/tasks`, `tasks-v2` and `tasks-v1` exits 0; the
report lists each v3 task's start and reference counts before and after.
No model is called.

**Risks.** A v3 task whose start passes its (lowered) budget has lost its
discriminating power; the report says so and leaves the task's fate to
the owner (its removal would change the suite's composition, which is
the experiment's design).

## Traceability

| requirement | phase |
|---|---|
| F1, F3–F9, F11, C1–C3 | 1 |
| F2, A1, A6 | 2 |
| F10, A3 | 1 |
| F12, F13, A2, A4, A5 | 3 |
