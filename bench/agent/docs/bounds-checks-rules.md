<!-- The rules section of docs/bounds-checks.md, without its list of
     forms that stay checked and without its rewrite advice: the `rules`
     arm's document (docs/reports/agent-four-arms.md). -->
# Which bounds checks the compiler proves

Every index `xs[i]` and every slice `xs[a..b]` has a bounds check. The
compiler removes a check it can prove always passes and keeps the rest,
which run when the program runs and trap if they fail. `kern
show-bounds-checks` lists every check as `proven`, with the reason, or
`runtime-checked`.

The proof is local to one function (after release inlining of small
helpers) and follows the function's control flow. It proves only what
the rules below give. A check the rules do not cover stays checked even
when the index is obviously in range to a reader.

## What proves a check

A check is proven when one of these holds on every path that reaches
it.

**A guard on the same length** (`dominated_by_check`). An earlier branch
condition or an earlier check compares the index against the length of
the place being indexed:
- `if i < len(xs) { ... xs[i] ... }`, and a `while i < len(xs)` loop's
  body;
- a `for i in 0usize..len(xs)` loop indexing `xs[i]`;
- `&&` conditions count: `if i < len(xs) && j < len(ys)` guards both.

What counts as "the same index":
- the same local, or a local copied from it (`j := i`) with no store in
  between;
- the same pure expression recomputed (`xs[a + b]` under `a + b <
  len(xs)`);
- an index plus or minus a literal: `i + 1 < len(xs)` covers `xs[i]` and
  `xs[i + 1]`; `i < len(xs)` covers `xs[i - 1]`; `x <= y` with `x` at
  least one more than something gives `x - 1 < y`.

What counts as "the same length":
- `len` of the same access (it never changes) or of the same array;
- an array's length from its type, so a literal equal to it is that
  length;
- a guard `len(a) == len(b)` makes the two lengths one, and `len(xs) ==
  K` gives `K - 1 < len(xs)`.

**A constant bound** (`constant`). A literal index into an array whose
type gives its length, or an index bounded by a constant below that
length:
- a mask by a literal, `x & 7u64` into a `[8]` array;
- a remainder by a literal, `x % 16u64` into a `[16]` array;
- a local every store to which, on every path, was so bounded;
- through conversions to an unsigned type and plus a constant offset.

A mask by a value one below the length, `x & (len(xs) - 1usize)`, is
also proven: the subtraction is checked, so for an empty `xs` it traps
before the index is reached.

Nothing beyond these rules is assumed: a check they do not cover stays
runtime-checked. A signed comparison proves nothing, and a store to the
index, or a call that takes it by `edit`, forgets what a guard
established about it.
