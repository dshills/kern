# Plan: spans in the kern parser

Implements `specs/selfhost-spans/SPEC.md` v0.2 in three phases, one
commit: the Go dump first, since it is the reference, then the kern side
against it.

## Phase 1: `ast.DumpSpans`

**Goals.** R1.

**Files.**
- `compiler/ast/dump.go`: the dumper gets a `spans` flag. `open` writes
  `@S:E` after the node name when it is set; the four node types with
  extra positions write them with their names (`BudgetItem`'s name,
  `With`'s names, `Arm`'s binds, `For`'s `Var` and `Var2`, `_` without
  a position when `Var2` is empty). `DumpSpans(f)` sets the flag; `Dump`
  does not. The doc comment states the format.
- `compiler/ast/dump_test.go` (or the existing dump test file):
  - `TestDumpSpansAllNodes` pins `DumpSpans` of
    `testdata/all_nodes.kern` in `testdata/all_nodes.spans` (a new
    golden file, rewritten with `-update`, like stage one's);
  - `TestDumpSpansStrips`: over the corpus, every file the Go parser
    accepts (parsed as `tests/selfhost_parser_test.go` parses it),
    removing every `@S:E` from `DumpSpans` gives `Dump`. The removal is
    the regular expression `@[0-9]+:[0-9]+`, exact for this format: a
    string value is written as hex, and a name or literal text (an
    identifier, a number) cannot contain `@`, so no source text in the
    dump looks like an annotation. A unit case dumps a file whose string
    literal and comment contain `@1:2` to show it.

**Acceptance.** Both tests pass; `Dump`'s golden file is unchanged.

## Phase 2: spans in the kern parser

**Goals.** R2.

**Files.**
- `examples/selfhost_parser/syntax/node.kern`: `Node` gets `ps` and
  `pe`, the span.
- `parse.kern`: helpers to set and read a span (`setp`, `setps`,
  `setpe`, `pstart`, `pend`) and `tspan(t)` for a token; `mk` leaves
  the span empty.
- `parse.kern`, `stmt.kern`, `expr.kern`: at each place the Go parser
  sets a `Pos` or its `Start` or `End` (the list from
  `rg -n 'Pos|\.End =|\.Start =' compiler/parser/parser.go convert.go`),
  the matching kern function sets the same span from the same token or
  child, in the same order; conversions (`expr_to_tname`,
  `expr_to_type`) copy the source expression's span as Go does.
- `dump.kern`: a `spans` flag on the dumper, writing the format of R1;
  the extra positions come from the name ranges the nodes already hold
  (the same tokens Go's `NamePos`, `BindPos`, `VarPos` record).
- `main.kern`: an optional leading argument `spans`.

**Key decisions.** Spans live beside the existing name ranges rather
than replacing them, so stage one's dumper and output are untouched.

**Acceptance.** `examples/selfhost_parser/selfhost_parser.out` and the
stage-one corpus comparison pass unchanged.

## Phase 3: parity, the mutation check, the report

**Goals.** R3, R4, R5.

**Files.**
- `tests/selfhost_parser_test.go`: `TestSelfhostSpans` runs the native
  release build with `spans` on every corpus file and compares with
  `ast.DumpSpans`; on a difference it prints the file, the first
  differing offset and 80 bytes of each dump around it; it fails if
  any accepted file is skipped, and logs the count.
- R4: one span rule in `expr.kern` (the call's end, set from the
  closing parenthesis) is changed by hand to the callee's end, the test
  is run and must fail, and the change is reverted; the report records
  the rule, the failing file count and the first failure.
- `docs/reports/selfhost-spans.md`: counts, Go-side span oddities found
  (reproduced), kern defects found, the next stage (diagnostics);
  `docs/reports/selfhost-parser.md`'s "Next stage" points to it.

**Acceptance.** `TestSelfhostSpans` passes with every accepted file
compared; `go test ./...`, `golangci-lint run ./...` and `gofmt -l` are
clean.

**Risks.** The port misses a span site: the corpus comparison finds it
with the file and offset. A Go span that depends on token positions the
kern lexer computes differently (for example a newline token's range):
the lexer is already compared token-exactly through the dumps of names
and texts, but spans of newline-skipping constructs may expose a
difference, which the port then matches.
