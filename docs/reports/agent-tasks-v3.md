# Agent benchmark, third task suite

**Date:** 2026-10-28. **Follows:** the full matrix on the second suite
(`docs/reports/agent-tasks-v2.md`: 1 win of 10 under the revised rule of
`docs/reports/agent-metric-revision.md`, the win being the one task whose
requirement only the compiler can measure and whose starting program is
far from meeting it). **Status:** written and validated with
`go run ./bench/agent/harness -dry-run` (every reference solution passes
its hidden suite, every starting source fails it); **not run.** The owner
has set the matrix aside for cost, so this report records the design and
what it leaves unknown. **Plan:** PLAN.md 4.2, spec §2.3 and §9.

## Why every task is a bounds-check task

The second matrix separated the arms on exactly two tasks, t03 and t10,
and on nothing else. Both bound the number of runtime-checked bounds
checks; the other eight bound copies, allocation sites, a type's size, a
clean check or a destruction order, and both models met those bounds at or
near their first attempt in both arms, because the language reference the
arms share (`docs/ownership.md`) states the rules those bounds follow from.
It says nothing about which bounds checks the proof pass
(`compiler/opt/prove.go`) accepts: the plain arm can only guess, and the
structured arm has the `show_bounds_checks` tool, whose description names
the three proof forms and whose output labels every check.

The second suite's own lesson is in its t10, where GPT's plain arm passed
all five trials: its habitual guard, `if at + 1usize < len(buf)` inside
the helper, is exactly the form the pass accepts, so the plain arm met the
budget without knowing why. The third suite is built on index forms whose
habitual guards the pass does not accept. A program that is correct, checks
cleanly, prints the expected output and guards every index in the way a
careful programmer would is still over budget on every task, and the only
way to find out is to measure.

## What the proof pass does not accept

Measured with `kern show-bounds-checks` on probe programs while writing
the suite (the harness counts the entries of kind `bounds` labelled
`runtime-checked`). The pass accepts a comparison of the index, or the
index plus a constant, or a local stored exactly once with either, against
`len()` of the same place, on the path to the access, with nothing in
between that joins two paths, stores through an access or calls a function
with an access argument; a literal lower bound on a length (`len(xs) >= 4`
covers the constant indexes 0 to 3); a `for` over a sequence; and a
constant index into an array. Everything below looks safe and is
runtime-checked:

| idiom | why it is not proven |
|---|---|
| `n := len(xs)` … `while i < n { xs[i] }` | the cached length is not `len(xs)` in a loop head |
| `while i + 2usize <= len(xs)` | `<=` establishes nothing |
| `if len(xs) == 4usize { xs[3usize] }` | `==` establishes nothing |
| `while i < len(a) && j < len(b) { a[i] }` | the `&&` joins two paths before the body |
| `if i >= len(xs) \|\| xs[i] == 0u8 { } … xs[i]` | the same join |
| `xs[i * 6usize + 5usize]`, `buf[(head + i) % len(buf)]`, `tab[usize(b)]` | the index is recomputed: a different value from the one compared |
| `xs[i - 1usize]` | subtraction is not read as an offset |
| `for i in 0usize..len(xs) { xs[i] }` | a range loop gives no bound on the index |
| `if i + 1usize < len(dst) { dst[i] = a; dst[i + 1usize] = b }` | the first store forgets the guard; the second is unproven |
| `if i < len(xs) { f(xs, 0usize); xs[i] }` | a call with an access argument forgets the guard |
| `while i < len(tab) { c := …; if … { }; tab[i] = c }` | the `if` joins two paths before the store |
| `while i < len(a) { a[i] * b[i] }` | `b[i]` needs a check against `len(b)` |
| `if idx < 8usize { self.buf[idx] }` for `buf: [8]u32` | the literal is not `len(self.buf)` |
| `if i < v.len() { v.as_view()[i] }` | the method's length is not the view's |

Each is proven by the same move: compute the index into a local once, and
compare that local against `len()` of the place being indexed immediately
before the access, re-guarding a second store or an access after a call.
Slicing (`xs[lo:hi]`) is itself a runtime-checked bounds check in every
form, which is why each budget counts the slices in `main`.

## The tasks

Every task's hidden suite checks a clean compile, the exact output, the
bound on runtime-checked bounds checks, the required functions (so a
solution cannot delete the helpers whose signatures force a proof inside
them) and, except in the review task that must add `unsafe` blocks, that
no `unsafe` was added. The budgets are exactly what the reference
achieves, with the slices in `main` the only checks left. The kinds follow
§9 (implementation ×3, debugging ×2, refactoring, optimization ×3,
unsafe-contract review).

| task | kind | the forms the natural program uses | start | reference, budget |
|---|---|---|---|---|
| t01_ring_buffer | implementation | `self.buf[self.slot(i)]`: a modulo index from a method call, into an array field of `self` | 0 (stubs) | 0 |
| t02_byte_classes | optimization | `tab[usize(b)]` into a 256-entry table; a literal loop bound on the fill; an `if` chain before the store; a cached length | 6 | 2 |
| t03_sorted_merge | implementation | `while i < len(a) && j < len(b)` two-cursor loops; stores to `dst[k]` with no guard on `k`; `xs[i - 1usize]` in the given `is_sorted` | 8 (stubs; the given helper) | 6 |
| t04_key_value | optimization | binary-search `keys[mid]`; lockstep `vals[i]` under a `keys` guard; `for i in 0usize..n`; a clamped index assigned twice | 10 | 5 |
| t05_packet_trap | debugging | a `<=` guard that reads one past a truncated packet (the trap); `buf[2usize + n]` and `buf[2usize + i]` recomputed; `while i < end` | 10, traps | 6 |
| t06_buffer_review | review | two raw operations outside `unsafe`, four missing contracts; then `src[i + k]` in a pattern match and a cached length in `count_byte` | rejected; 2 once it checks | 0 |
| t07_hex_codec | refactoring | `dst[2usize * i]` and `dst[2usize * i + 1usize]` under one `&&` guard; the same reads in `decode`; `dst[i]` after a call | 11 | 5 |
| t08_flat_matrix | implementation | `m[r * n + c]` under `if r < n && c < n`, or under `if r * n + c < len(m)`, both recomputed | 6 (stubs) | 6 |
| t09_series_window | optimization | centred windows `xs[i - 1usize]`, `xs[i + 1usize]`; `dst[n - 1usize]` with a cached `n`; `xs[i + k]` with a variable offset; `for i in 1usize..len(xs)` | 18 | 6 |
| t10_histogram | debugging | writes through a `view` (the rejection); `counts[usize(b)]` read and written; `for i in 0usize..256usize` over the table | rejected; 5 once it checks | 1 |

Two natural solutions were written for calibration, as a plain-arm agent
would write them: a ring buffer that indexes `self.buf[self.slot(i)]`
everywhere scores 6 runtime-checked checks against a budget of 0, and a
matrix `at` that guards `r * n + c < len(m)` and then indexes
`m[r * n + c]` scores 3 beyond `main`'s slices against a budget of 0
beyond them. On the implementation tasks the starting measure is the
stubs' (0 beyond `main`), so the dry run's "start fails" there is on
output; the budget bites once the stubs are filled.

`main` exercises every required function on two to six inputs, as in the
second suite, so a stub that prints constants fails on output.

## Harness and layout

No harness change. The second suite moved from `bench/agent/tasks/` to
`bench/agent/tasks-v2/` and this suite took `bench/agent/tasks/`, as the
first suite moved to `tasks-v1/` when the second was written, so the
default `-tasks-dir` is the current suite. Re-scoring the second suite's
records therefore needs the directory named:
`go run ./bench/agent/harness -report-from bench/agent/runs/20261028-104427
-tasks-dir bench/agent/tasks-v2`. `bench/agent/REPORT.md` is still the
second suite's matrix and is unchanged.

## What this leaves unknown

- **No pilot was run.** The second suite's pilots (one task, one model,
  one trial per arm) were what exposed the t03 prompt that stated the proof
  forms. This suite has had no model contact at all, by the owner's
  decision on cost. The cheapest check when one is wanted is one
  implementation task on one model,
  `go run ./bench/agent/harness -tasks t08_flat_matrix -models anthropic:claude-opus-5-5 -trials 1`,
  which is two trials.
- **The structured arm's path is untested.** The tool description names
  the three proof forms but not the move that makes them apply (hoist the
  index into a local, compare it against `len()`, re-guard after a store or
  a call). The structured arm has to find that from the labels by
  experiment within 20 iterations. t08, whose every access goes through
  one helper, should be the easiest; t07, with two stores and a read after
  a call, the hardest. If the structured arm also fails a task on some
  trials, the task is still a win when it fails less often than the plain
  arm, since the revised rule compares successes first.
- **The plain arm may still pass by defensive rewriting**: a plain-arm
  agent that happens to write the hoisted form, or that replaces an index
  loop with a `for` over the sequence where the task allows it (t02's
  `score`, t06's `count_byte`, t10's `total` already are), meets part of
  the budget by luck. The forms were chosen so that luck is needed on
  several sites per task, not one.
- **A found compiler fault, not used by the suite.** Indexing a
  module-level `const` array (`const TAB: [4]u8 = …; TAB[i]`) fails in
  lowering with an internal error, `index of a non-place`, in both `check`
  and `run`; the suite passes its tables as views instead.
