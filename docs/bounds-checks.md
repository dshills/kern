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

## What does not prove a check

Nothing beyond the rules above is assumed. In particular:
- **A signed comparison** proves nothing, and a wrapping operation is
  not read as an offset.
- **A store to the index, a call that takes it by `edit`, or a store
  through an access that may reach it** forgets what the guard
  established, even when the stored value is the same (`i = i0` after
  `if i < len(xs)` with `i := i0`). A call that cannot reach the index
  forgets nothing about it, and the lengths of arrays and sequences
  survive every call.
- **Arithmetic the rules do not track**: a midpoint `(lo + hi) / 2`, a
  reversed index `n - 1 - i`, `len(xs) - 1` under `len(xs) > 0`, a grid
  index `y * w + x` under `x < w && y < h`, a digit offset `c - 48`
  under a digit test.
- **A value's range from its type**: a `u8` converted to index a
  `[256]` array.
- **A remainder by a value**: `x % len(xs)`.
- **A relation between two variables** established elsewhere: `j <= i`
  with `i < len(xs)`, an index a helper function returned, two pointers
  that meet, `len(ps) == len(xs) + 1`.

To make such an access proven, compute the index into a local and
compare that local against `len` of the indexed place on the path just
before the access, with no store to the local, and no call that edits
it, in between.

## Declaring a budget

A budget block before a function turns these rules into a check error.
With `budget { bounds_checks 0 }`, `kern check` and `kern build` fail
when the function has a runtime-checked bounds check, and the error
names every one (spec §7, rule 7.6; `docs/ownership.md` Rule 7.6):

```kern
module rank

// rank is the number of elements of the sorted xs below key.
budget { bounds_checks 0 }
func rank(xs: view[]u64, key: u64) -> usize {
    if len(xs) == 0usize || xs[len(xs) - 1usize] < key { return len(xs) }
    lo := 0usize
    hi := len(xs)
    while lo < hi {
        mid := (lo + hi) / 2usize
        if xs[mid] < key { lo = mid + 1usize } else { hi = mid }
    }
    return lo
}
```

```
rank.kern:176-196: error [BUDGET_EXCEEDED rule 7.6]: function `rank` has 2 runtime-checked bounds checks, budget 0: 6:29 `xs[len(xs) - 1usize]`, 11:12 `xs[mid]`
```

Both are forms the rules do not track: `len(xs) - 1` under a length
test, and a midpoint. Rewritten as the previous section says, with each
index in a local compared against `len(xs)` just before the access, the
function meets its budget:

```kern
budget { bounds_checks 0 }
func rank(xs: view[]u64, key: u64) -> usize {
    if len(xs) == 0usize { return 0usize }
    last := len(xs) - 1usize
    if last < len(xs) && xs[last] < key { return len(xs) }
    lo := 0usize
    hi := len(xs)
    while lo < hi {
        mid := (lo + hi) / 2usize
        if mid < len(xs) && xs[mid] < key { lo = mid + 1usize } else { hi = mid }
    }
    return lo
}
```

The count is the function's `runtime-checked` entries of
`show-bounds-checks` at the command's `--opt` level, `debug` by default.
A check in a helper counts in the helper, also where release inlining
copies it into the caller, and release inlining can prove a helper's
check that debug leaves checked, so a budget met with `--opt=release`
may fail at `debug`. `kern run`, `kern test` and the show queries do
not enforce budgets, so a program over its budget still runs. The same
block takes `copies` and `allocations`, counted from `show-copies` and
`show-allocations`.
