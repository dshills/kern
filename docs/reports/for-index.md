# `for i, x in xs`, and one diagnostic per conflict

**Date:** 2026-10-31. **Spec:** `specs/for-index/SPEC.md`.

## What changed

- **`for i, x in xs`** over arrays and sequences: the index is a `usize`
  local and the element is bound as before.
  - The resolver declares the index under the key `For.IndexKey()`, so
    the element keeps the `For` node's own declaration and needs no
    change.
  - The lowering stores the loop's counter into the index local.
  - The checker treats the index as an initialized owned local.
- **One diagnostic per conflict.** `check.CheckModules` drops exact
  duplicates after sorting. A nested cursor loop over the same map had
  reported the same `ACC_CONFLICT` five times, once per desugared
  statement that reads the map; it is now reported once.
- **No follow-on errors.** A `for` over something it cannot iterate
  declares its names with the error type. A conversion of an error-typed
  value (`u64(i)`) is no longer reported again, which it had been for
  every use of such a name.

## Changed by design

`tests/negative/for_two_names` iterated an array with two names, which
is now valid. It iterates a struct instead, and its accepted twin uses
the index form.

## Correction

`docs/reports/for-cursor.md` said that nested loops over the same
collection were rejected "as for sequences". Two loops over the same
array are accepted: the checker treats two loop freezes as compatible.
Only the cursor form is rejected, because its inner loop reads the map
through method calls. That report now says so.

## Verification

- **`tests/run/for_index`** prints `44` (0·5+1·6+2·7+3·8), `11` and
  `a0 c2` in the interpreter and natively, in debug and release.
- **The negative fixtures** `for_two_names` and `for_cursor_nested`
  each give exactly one diagnostic, and their twins are accepted.
- **A loop over a struct** with one and with two names gives one error
  each and nothing else.
- **Gates.** `go test ./compiler/... ./tests` passes;
  `golangci-lint run ./...` reports 0 issues.
