# Editor: code completion

**Date:** 2026-10-31. **Spec:** `specs/editor-complete/SPEC.md`.

## What changed

`kern lsp` answers `textDocument/completion`, triggered on `.`
(`compiler/lsp/complete.go`):

| Where | What it offers |
|---|---|
| after `x.` | `x`'s fields and `self` methods; another module's private fields left out |
| after a type (`Shape.`, `lib.Point.`) | its functions without `self` and its variants |
| after a module (`lib.`) | its exported declarations |
| elsewhere | locals in scope, the module's and the prelude's declarations, imported modules, keywords |

- **The receiver's type.** It comes from the resolver run over the
  current text. With nothing after the dot yet, a placeholder member is
  inserted so that the text parses; the unknown member's error does not
  stop the receiver's type from being recorded.
- **Locals in scope.** These are the ones declared earlier in the
  function, in a block that encloses the cursor (approximated by the
  innermost block holding the declaration). So a local of a finished
  inner block, or one declared below the cursor, is not offered.

## Verification

- **`TestCompletion`** writes one unfinished line at a time into a
  two-module program and checks:
  - `p.` offers `X`, `Y` and `Sum`, not `lib`'s private `hidden` nor the
    static `Origin`; `p.S` offers only `Sum`;
  - `lib.` offers `Make`, `Point` and `Limit`, not the private `helper`;
  - `lib.Point.` offers `Origin`, and `Shape.` the variants;
  - `to` offers `total`, not `later` or the inner block's `inner`;
  - `print_` offers the prelude's printers;
  - a blank prefix offers locals, the module's names, `lib` and the
    keywords;
  - a non-kern document gets an empty list.
- **The capabilities test** expects 9 capabilities, with the trigger
  character.
- **`go test -race ./compiler/lsp`** passes.

## Not done

- **Constrained type parameters.** Methods available through an
  interface constraint (`T: Hashable`) are not offered after a value of
  type `T`.
- **Several unfinished dots at once.** A document holding more than one
  can mis-parse (`p.` followed by `lib.` reads as `p.lib`); editing
  normally leaves one.
- **Loop variables after their loop.** A loop or `with` variable is
  still offered after its body, because its declaration sits in the
  enclosing block.
