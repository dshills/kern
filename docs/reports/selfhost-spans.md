# Self-hosting, stage two: spans

**Date:** 2026-11-04. **Spec:** `specs/selfhost-spans/SPEC.md` v0.2
(speccritic VALID_WITH_GAPS, no critical finding). **Plan:**
`specs/selfhost-spans/PLAN.md` (plancritic EXECUTABLE_WITH_CLARIFICATIONS,
no critical finding; its one warning, that stripping annotations must
not touch source text, is answered in the plan and tested).
**Follows:** `docs/reports/selfhost-parser.md` (stage one).

## What changed

- **`ast.DumpSpans`** (`compiler/ast/dump.go`) is `Dump` with each node's
  span after its name, as in `(ident@12:13 x)`. The extra positions
  follow their names: a budget item's name, a `with`'s names, an arm's
  binds, and a `for`'s variables. `Dump` is unchanged.
- **The kern parser** (`examples/selfhost_parser/`):
  - every node now carries a span, set at the place the Go parser sets
    its `Pos`, `Start` or `End`;
  - the dumper writes the span form when the program gets the word
    `spans` after the path;
  - the extra positions come from the name ranges the nodes already
    held.
- **The port** follows the Go parser function by function, as stage one
  did. That includes:
  - the conversions (`exprToTypeName` and `ExprToType` give the new type
    node the expression's span);
  - the minus folded into an integer literal, which moves the literal's
    start;
  - an arm's qualifier, which keeps the first name's span even when the
    qualifier is `a.b`.

## Result

- `TestSelfhostSpans` (`tests/selfhost_parser_test.go`) builds the kern
  parser natively at release and runs it with `spans` on the corpus.
  **The Go parser accepts 513 files, and all 513 span dumps are
  identical; none was skipped.** One file is rejected and not compared.
- Stage one is unchanged: its fixture output and its 514-file
  comparison (513 accepted, 1 rejected) still pass.
- Go-side tests:
  - `TestDumpSpansAllNodes` pins the span dump of `all_nodes.kern`
    (every node type) in `testdata/all_nodes.spans`;
  - `TestDumpSpansStrips` checks, on every accepted corpus file and on
    one whose comment and string hold `@1:2`, that removing
    `@[0-9]+:[0-9]+` gives `Dump`.
- **R4, the mutation.** The call's span end was changed from the
  closing parenthesis to the callee's end (`span_nt(c, x, r.e)` to
  `span_nn(c, x, x)` in `expr.kern`). `TestSelfhostSpans` then failed
  on 399 of the 513 files. The first failure was
  `tests/adversarial/callback_retains_view_ok.kern`, where
  `return@203:225` became `return@203:211`. With the change reverted,
  the test passed again.

## Found on the way

- **One Go span that looks odd, reproduced as it is.** In a qualified
  arm pattern such as `a.b.V(x)`, the qualifier's span covers only the
  first name. The Go parser builds the qualifier from that token and does
  not extend it when it shifts in the second name (`parser.go`, the
  `Arm`'s `Qual`). The kern port does the same. It is not worth changing
  the Go parser for at this stage.
- **No compiler defect was found.** One language limit shaped the port.
  A call that edits `self` cannot take an argument that reads `self`
  (`self.setp(n, self.pstart(x), self.pend(y))` is `ACC_CONFLICT`
  5.4.4). The helpers therefore take node indices and read the nodes
  themselves: `span_nn`, `span_tn`, `span_nt`, `setpe_n`, `setpe_kid`
  and `setpe_prev`. This is the two-phase argument evaluation gap the
  access-rule ergonomics item would address.

## Next stage

Diagnostics: messages and positions that match the Go parser's for
syntax errors. Then the resolver's first pass over the same node table.
