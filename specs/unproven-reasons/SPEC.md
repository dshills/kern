# Why a bounds check is not proven: reasons in `show-bounds-checks` and `BUDGET_EXCEEDED`

**Version:** 0.2 (2026-11-03; 0.1 the same day, before the slice hint's amendment in §3 item 8). **Scope:** a classification of
runtime-checked bounds checks over the AST and the lowered module
(`compiler/analysis`), the proof pass only to record which bound of a
slice check it failed to prove (`compiler/opt/prove.go`, `compiler/ir`),
the `show-bounds-checks` result and its schema (`compiler/diag/schema.go`,
`compiler/diag/diag.go`, `tools/agent-api/schema.json`, the generated
`tools/agent-api/schema.md`), the `BUDGET_EXCEEDED` message
(`compiler/analysis/budget.go`), the reference (`docs/bounds-checks.md`),
tests, and an offline evaluation on the budgeted agent suite.
**Language impact:** none. **Proof impact:** none: no check changes
label, and the emitted code is unchanged. **Wire impact:** schema version
0.5, which adds two fields to a `show-bounds-checks` entry; requests at
0.2 to 0.4 are answered in their own shape.
**Related:** `specs/declared-budgets/SPEC.md` (R4, the message this
extends; §5, the suite); `docs/reports/declared-budgets.md` (the paid run
whose failures this answers); `docs/bounds-checks.md` (the rules the
hints restate); `specs/effect-trap-kinds/SPEC.md` (how 0.4 was added).

**Review status.** speccritic (openai gpt-6.1-sol, with
`specs/declared-budgets/SPEC.md` and `docs/bounds-checks.md` as
context), six rounds, the last two with no critical finding. Resolved: artifact identity excludes the changed
diagnostic (R2); R5 names exactly when the classification runs; the
`other_length` hint no longer offers a length-equality rewrite it cannot
know holds; aggregation takes the earliest reason of §3 (R1), and
`type_range` excludes generic types so its hint is the same at every
occurrence; the `guard_not_on_every_path` and `computed_index` hints were
reworded to claims the label proves (an inverted guard, and a recomputed
pure expression the rules do relate, made the first wordings false);
parentheses and truncation are defined per fragment; R5 bounds only
classification work, not R2's recording. Kept as written:
R5's assertion that the classification is not entered, an observable
cost property the tests check, as `specs/declared-budgets` R6 does; the
preflight warnings that read R1 to R8 as undefined acronyms. Amended
while planning (plancritic): closures use only the innermost body's
guards; an unlocated entry's fixed hint (§2); a byte-order index's
offset; R2 compares the LLVM IR, not the linked executable.

**Glossary.** R1 to R8: the requirements of §4. Entry: an element of
`show-bounds-checks`' `checks`. Unproven entry: an entry with `kind`
`bounds` and `label` `runtime-checked`. Access: the source construct an
unproven entry's span covers (§2). Reason: the code of §3 assigned to an
unproven entry. Hint: the one-clause text of §3 that goes with a reason.
The rules: the proof rules of `docs/bounds-checks.md` as the proof pass
implements them at the command's `--opt` level. Abbreviations: AST,
abstract syntax tree; IR, intermediate representation; CLI, the `kern`
command line; LSP, the language server. In a template, each
substituted fragment (E, X, Q, V, L, H, `T`) is the source text of that
expression with runs of whitespace collapsed to one space and cut on its
own, like a `BUDGET_EXCEEDED` snippet, at 40 code points plus `...`; the
template's own text around it is never cut (with a 50-code-point E,
`` `E < len(xs)` `` is the first 40 code points of E, `...`, then
` < len(xs)`).

## 0. Goals

The plain arm of the budgeted suite (100 trials) succeeded 70 times; of
its 30 failures, 20 ended with `check` reporting only `BUDGET_EXCEEDED`.
Those 20 final `check` outputs name 74 locations. The agents knew where
every remaining check was and did not know which rewrite the rules
accept: the arms that had `docs/bounds-checks.md` got that advice from
the document. This specification puts the advice per location into the
two places a program's author already reads, the `BUDGET_EXCEEDED`
message and `show-bounds-checks`, so that an agent with only `check` is
told, for each access, why the rules leave it checked and a rewrite that
the rules prove where one exists.

## 0.1 Non-goals

- **No new proof rule.** Four gaps found while writing this are listed
  in §6 and left for their own specification: the agent suites' natural
  solutions and calibration count on today's rules (`specs/agent-suite-v4`),
  so a rule change is a suite change.
- No reason for overflow, generation or disjointness entries, or for
  `copies` and `allocations` budgets.
- No machine-applicable fix: `BUDGET_EXCEEDED` keeps `fix` null. A guard
  changes what the program does when the index is out of range, which
  only the author can decide.
- No reasons in `kern check` without a budget, in hover or in other LSP
  features beyond the message the LSP already shows.
- A reason explains one function's facts as the rules see them; it does
  not search for the cause across functions (a helper that returns an
  index is `no_guard` at the caller).

## 1. Background

The proof pass (`compiler/opt/prove.go`) marks a check proven when the
facts on every path to it give a bound; otherwise the check keeps the
lowering's `runtime-checked` provenance, with no record of why. The 74
remaining locations of the paid run fall into these forms (counted by
their source text):

| form | examples | count |
|---|---|---|
| a variable or a variable plus or minus a literal, no guard that covers it on every path | `xs[j]`, `a[i]`, `xs[mid]`, `ps[i + 1usize]`, `ps[0usize]` | 40 |
| a computed index: a subtraction or sum of two variables, a product, a call | `s[n - 1usize - i]`, `src[up + x]`, `h[len(h) - 1usize]`, `xs[argmax_from(xs, 0usize)]` | 22 |
| a slice | `h[0usize:n - 1usize]`, `h[0usize:last]`, `a[s:s + w]` | 8 |
| a `u8` converted to `usize` | `counts[usize(b)]`, `table[usize(data[i])]` | 2 |
| a remainder by a length | `src[i % len(src)]`, `src[(i + k) % len(src)]` | 2 |

For every form but the slice there is a rewrite the rules prove: compute
the index into a local, if it is not one, and test that it is below the
indexed place's length just before the access; for the `u8` form, a mask
by 255. Probes of 2026-11-03 confirmed each rewrite, and the covering
forms of §3, on the current proof pass (R8 makes them tests).

## 2. The access of an entry

An unproven entry's span is the span of one of:
- an index expression `X[E]`: the access's place is X and its index is E;
- a slice expression `X[L:H]`: place X, low bound L, high bound H (an
  omitted bound is treated as written: `0usize` and `len(X)`);
- a call of `load_be`, `load_le`, `store_be` or `store_le` of a
  `W`-byte type with buffer argument X and index argument I: place X and
  index `I + (W - 1)usize` (written with the decimal value of W − 1), or
  I itself when W is 1. This E is one fragment (truncated as a whole).
  Its variable and offset are I's, with W − 1 added to the offset, when
  I has a variable; it has none otherwise.

An entry whose span is none of these is not expected (the lowering
emits bounds checks only for them); should one occur, it gets
`no_guard` with the hint ``test that the index is below the length on
every path to it``, and R8's suite sweep asserts that none occurs.

Parentheses around E, L, H or X are dropped. The *variable* of an index
is defined when the index is a name or a field path of a name (`v`,
`v.f.g`), optionally plus or minus an integer literal (`v + 2usize`,
`v - 1usize`): it is that name or path, and the literal, negated for a
minus, is the index's *offset* (0 without one). An integer literal index
has no variable.

## 3. Reasons

Every unproven entry gets exactly one reason, the first of this list that
applies. *Guard candidates* of an access are, within the innermost body
that contains it (the closure literal's body when the access is in one,
else the function's body), and not inside any closure literal nested in
that body, the comparisons of a condition (of
`if`, `while`, or an operand of `&&`, `||` or `!` within one) and the
headers `for v in A..B`, where one side is `len(Q)` for some expression
Q and the other is a name or field path of a name plus or minus an
integer literal, compared with `<`, `<=`, `>` or `>=`. A `for v in A..B`
header counts as `v < B`. A candidate *bounds* a variable V by Q with
literal a when its non-`len` side has variable V and offset a. It
*covers* an index with offset o when it is written `V + a < len(Q)`,
`len(Q) > V + a`, `V + a >= len(Q)` or `len(Q) <= V + a` with a ≥ o, or
`V + a <= len(Q)`, `len(Q) >= V + a`, `V + a > len(Q)` or `len(Q) < V + a`
with a ≥ 1 and a − 1 ≥ o. Q and X are the same place when their token
sequences are identical after outer parentheses are dropped from both. When several candidates qualify for a reason,
the one cited is the last one that starts before the access, or, if none
does, the first one after it. L:C is its start as `line:column`, the
form `BUDGET_EXCEEDED` uses.

In the hints, X, E, L and H are the access's place, index and bounds
(§2), V the index's variable, Q the cited candidate's place, and `T` a
type's name.

**Index accesses** (an index expression or a byte-order call):

1. **`guard_not_on_every_path`**: the index has a variable V and a
   candidate on the same place X bounds V and covers the index. Hint:
   ``the guard at L:C does not establish `E < len(X)` on every path to
   it (a path may bypass it or take its other branch, or a store to `V`,
   a call that edits it or a write that may reach it may come after it):
   test `E < len(X)` just before it``.
2. **`guard_too_weak`**: the index has a variable V and a candidate on
   the same place X bounds V, but none covers it. Hint: ``the guard at
   L:C does not bound `E` below `len(X)` as the rules read it: test
   `E < len(X)` ``.
3. **`other_length`**: the index has a variable V and a candidate bounds
   V by a place Q other than X. Hint: ``the guard at L:C bounds `V` by
   `len(Q)`, not `len(X)`: test `E < len(X)` just before it``. (A test
   `len(X) == len(Q)` would also serve when the cited guard holds at the
   access, which the classification does not know, so the hint does not
   offer it.)
4. **`type_range`**: E is a conversion `U(e)` to an unsigned type U of
   an expression of a narrower unsigned type `T` of b bits, and X's type
   is an array whose length n satisfies 2^b ≤ n, where none of U, e's
   type and X's type involves a type parameter of an enclosing generic
   function or `impl` (so every occurrence of the access has the same T,
   U and n). Hint: ``the range of
   `T` is not a bound the rules use: mask the index, `E & Mu` `` where M
   is 2^b − 1 in decimal and u is U's literal suffix (`usize(b) &
   255usize`).
5. **`remainder_by_value`**: E is `a % b` where b is not an integer
   literal. Hint: ``a remainder by a value is not a bound the rules use:
   put `E` in a local and test it below `len(X)` just before the
   access``.
6. **`computed_index`**: the index has no variable and is not an integer
   literal. Hint: ``no guard establishes `E < len(X)` on every path to
   it, and the rules do not derive it from guards on the parts of `E`:
   put `E` in a local and test it below `len(X)` just before the
   access``.
7. **`no_guard`**: every other index access. Hint: ``test `E < len(X)`
   on every path to it``.

**Slice accesses**:

8. **`slice_high_bound`**: the proof pass did not prove the high bound
   (it records which bound failed; when both did, this one). Hint:
   ``the rules prove a high bound only when it is `len(X)`, a literal
   within an array's length, or `v + c` with a literal c ≥ 1 under a
   guard `v + (c - 1) < len(X)`, `v` not itself a value plus or minus a
   literal; `H <= len(X)` does not prove it: slice to `len(X)` instead,
   `X[L:len(X)]`, and test each index below the slice's length``,
   where, when L is not the literal `0usize` (or omitted), the text
   ``, after testing `L < len(X)` `` follows `` `X[L:len(X)]` `` if L is
   an integer literal or has a variable, and otherwise
   `` `X[L:len(X)]` `` is replaced by ``from `L` put in a local and
   tested below `len(X)` first`` (a call result, for one, is not the
   same value when written twice). (Version
   0.1 ended the hint at "does not prove it"; the evaluation of
   `docs/reports/unproven-reasons.md` found that the `v + c` form, written
   the natural way with a local `k := w - 1usize`, is read back as `w`
   plus 0 and stays checked, while slicing to the length and guarding the
   indexes is proven.)
9. **`slice_low_bound`**: the high bound was proven and the low one was
   not. Hint: ``the rules prove a low bound only when it is `0usize`, a
   literal not above a literal high bound, `v + a` under a high bound
   `v + b` with a ≤ b, or below `len(X)` under a guard when the high
   bound is `len(X)` ``.

Every hint is true of the program by construction: a reason other than
`guard_not_on_every_path` states a fact about the source text and the
rules, and `guard_not_on_every_path` states only that the guard does not
establish the bound on every path to the access, which the entry's label
shows; its parenthesis lists the ways that can happen without claiming
which one did. The guard's polarity is not examined: `if i >= len(xs) {
xs[i] }` cites the guard, which holds there with the other sense.

## 4. Requirements

**R1. Classification.** Every unproven entry is assigned a reason and a
hint as §3 defines them, from the AST of the function whose span
contains the access and the proof outcome; nothing else (no other
function, no file order, no map iteration order) affects them. An entry
that aggregates several IR occurrences can have occurrences with
different reasons only on a slice whose occurrences fail different
bounds, since every other condition of §3 depends only on the source
(`type_range` excludes generic types). It takes, among its
runtime-checked occurrences' reasons, the one earliest in the list of
§3, whatever the order of the occurrences, and that reason's hint,
which depends only on the source.

**R2. The proof pass.** The proof pass records, for a slice check it
does not prove, whether the high bound was proven. It changes no label
and no `proven_by`, and every compiled artifact is byte-identical to
the previous binary's for every program: the LLVM IR `build` compiles
(`--emit-llvm`), the `emit-ir` and `emit-asm` results, and what a
program `run` executes prints and its exit status (checked on the
programs under `tests/` that the previous binary builds, and the agent
suites' references, R8). The linked executable is not compared: two
builds of one program by one binary already differ on this host, so its
input, the LLVM IR, stands for it.
Diagnostics are not artifacts: R4 changes the `BUDGET_EXCEEDED`
message.

**R3. `show-bounds-checks` at 0.5.** Schema version 0.5 is the current
version. A `BoundsCheck` gains:
- `reason`: the §3 code, a string, non-null iff the entry is unproven;
- `hint`: the §3 hint, a string, non-null iff `reason` is.

A request at 0.2, 0.3 or 0.4 is answered at that version and in that
version's shape, without the two fields. CLI invocations answer at 0.5,
as they answer at the current version today. The schema descriptions
(`compiler/diag/schema.go`, `tools/agent-api/schema.json`) list the
codes and say that `reason` is stable across compiler versions and
`hint` is not, like `code` and `message`.

**R4. `BUDGET_EXCEEDED`.** For a `bounds_checks` item, every listed
location of the message is followed by ` (hint)`, its entry's hint, and
locations are separated by `; ` instead of `, `:

```
function `rank` has 2 runtime-checked bounds checks, budget 0: 6:29 `xs[len(xs) - 1usize]` (no guard establishes `len(xs) - 1usize < len(xs)` on every path to it, and the rules do not derive it from guards on the parts of `len(xs) - 1usize`: put `len(xs) - 1usize` in a local and test it below `len(xs)` just before the access); 11:12 `xs[mid]` (test `mid < len(xs)` on every path to it)
```

The 10-location limit, the `, and N more` tail (now `; and N more`), the
span, the related spans and the null fix of `specs/declared-budgets` R4
are unchanged, and so is the message of a `copies` or `allocations`
item. `specs/declared-budgets/SPEC.md` R4 is amended to point here.

**R5. Cost.** The classification runs in exactly two cases: a
`show-bounds-checks` request answered at 0.5, for its unproven entries,
and a `check` or `build` with an exceeded `bounds_checks` item, for that
item's entries. No other command or request runs it, so `check`,
`build`, `run` and `test` on a program without an exceeded
`bounds_checks` item do no classification work (R2's recording of which
slice bound failed, a field set during the proof pass, is not
classification work and is allowed everywhere); a test asserts that
the classification is not entered in those cases, and the output of
`check` on the budget-free files under `tests/` is byte-identical to the
previous binary's.

**R6. Reference.** `docs/bounds-checks.md` gains a section listing the
nine reasons, each with its condition in a sentence and one example
access, and its "Declaring a budget" example shows the R4 message. The
`docs/reports/declared-budgets.md` example output is not rewritten (it
records what the run saw); a note says the message has hints since this
specification.

**R7. Agent suites.** Nothing under `bench/agent/tasks*` is regenerated,
since no proof changes: calibrations, natural-solution counts and dry
runs are unchanged, and the offline budgeted-suite test passes, with its
expectations on messages, if any, updated to R4. The query tool of the
harness's `queries` arm runs `show-bounds-checks` through the CLI and so
receives `reason` and `hint`; the report of any later run says so, since
the four-arm results were measured without them.

**R8. Tests.** At least:
- for each of reasons 1 to 7, a fixture access that gets it and its
  exact hint, and the same function rewritten as the hint says, whose
  entry is then proven; for 8 and 9,
  a fixture each with the exact hint;
- the byte-order call form of §2, at W = 8 and W = 1;
- the choice among several candidates (the last before the access, else
  the first after), a candidate in a closure, a `for` header candidate,
  and each covering form of §3 at its boundary (`V + 1usize <= len(X)`
  covers `X[V]` but not `X[V + 1usize]`; `V <= len(X)` covers nothing);
- hint truncation at 40 code points;
- R3: a 0.5 response with both fields, a 0.4 request answered without
  them, `reason` and `hint` null on a proven entry and on an overflow
  entry;
- R4: the message of the example above, the `; ` separator, the
  `; and N more` tail at 11 locations, and an unchanged `copies`
  message;
- R2's byte-identity check, run as part of the plan's last phase;
- R5: the classification is not entered by `check` on a budget-free
  program, by `check` with only a met `bounds_checks` item, by a
  `copies` overrun, or by `show-bounds-checks` at 0.4;
- R1: a generic function's slice instantiated at two types with one
  occurrence failing each bound gets `slice_high_bound`, and a
  conversion index of a generic operand is `computed_index`, not
  `type_range`;
- every unproven entry of every reference, start and natural solution of
  `bench/agent/tasks` and `bench/agent/tasks-budgeted` gets a reason
  (no panic, no empty hint).

## 5. Evaluation

Offline, on the final programs of the paid run
(`bench/agent/runs/20261102-134710`, git-ignored, so on this host only)
and on the budgeted suite's natural solutions, report the reasons of
every remaining location as a table by reason, and check by hand, for
ten locations chosen across reasons, that the hint's rewrite proves the
access. The result goes in `docs/reports/unproven-reasons.md`. A paid run
of the plain arm on the budgeted suite, the experiment this
specification is for, is the owner's decision and is not part of it.

## 6. Proof gaps found, not addressed

The probes behind §1 found four forms the rules leave checked although
the check provably passes; each is a candidate for its own specification,
which must update the agent suites' calibration:
- `xs[len(xs) - 1usize]`, with or without a guard `len(xs) > 0usize`:
  the subtraction is checked, so the index is below the length whenever
  it is reached.
- `n <= len(h)` proves neither `h[n - 1usize]` nor `h[0usize:n]`; the
  rules read `x <= y` as `x - 1 < y` only when x is an offset of at
  least one.
- No guard proves a slice's low bound against a different variable's
  high bound (`a[s:e]` under `s <= e && e <= len(a)`).
- A literal high bound on a sequence is never proven, even `h[0usize:0usize]`:
  the literal rule needs an array length from the type.

## 7. Acceptance criteria

- R1 to R8 hold; `go test ./...`, `go test -race` on `compiler/analysis`
  and `cmd/kern`, and `golangci-lint run ./...` pass.
- The evaluation report exists, with the reason table and the ten
  hand checks.
