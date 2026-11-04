# Self-hosting, stage two: spans in the kern parser

**Version:** 0.2 (2026-11-04). **Review status.** speccritic
(gpt-6.1-sol, stage one's spec as context): VALID_WITH_GAPS, no critical
finding; R1's corpus check is limited to accepted files and the kern
storage is informative. **Scope:**
- `compiler/ast/dump.go`: a second dump, `DumpSpans`, that writes every
  node's span and every extra position field;
- the kern parser in `examples/selfhost_parser/` (`syntax/node.kern`,
  `parse.kern`, `stmt.kern`, `expr.kern`, `dump.kern`, `main.kern`):
  record the same spans and print them in `DumpSpans`'s format when
  asked;
- `tests/selfhost_parser_test.go`: compare the span dumps over the
  corpus;
- the report `docs/reports/selfhost-spans.md`, and a note in
  `docs/reports/selfhost-parser.md` ("Next stage").

**Language impact:** none. **Follows:** `specs/selfhost-parser/SPEC.md`
(stage one: the tree without spans), whose report names spans as the
next stage; ADR-013 (self-hosting).

**Glossary.** R*n*: requirement *n* of §3. *Span*: a node's half-open
byte range `[Start, End)` in its file (`ast.Pos`). *Extra positions*:
the position fields a node has besides its span: `BudgetItem.NamePos`,
`With.NamePos` (one per name), `Arm.BindPos` (one per binding),
`For.VarPos` and `For.Var2Pos`. *The Go parser*: `compiler/parser`, the
reference. *The corpus*: the files `tests/selfhost_parser_test.go`
already compares (every `*.kern` under `tests/`, `std/`, `examples/`,
`bench/kern/`, and the prelude).

## 0. Goal and non-goals

The kern parser must produce, for every file, the same spans as the Go
parser: every node's span and every extra position, byte for byte. This
is what a kern-written diagnostic pass would quote and point at, and it
is the stage the parser report names after the tree.

Non-goals: diagnostics (message and position parity, the stage after
this); the resolver; changing the Go parser or the spans it produces
(this stage reproduces them, including any that look odd, and records
oddities in the report instead).

## 1. Background

`ast.Dump` writes the tree without spans. The kern parser's `Node`
stores a kind, up to two source ranges used for names and token text, an
integer, flags and children; it has no node span. The Go parser sets
each node's `Pos` from token positions (`tokPos`) and child spans
(`span(a, b)`), at about a hundred places in `compiler/parser/parser.go`
and its conversions (`convert.go`, `exprToTypeName`, `ExprToType`).

## 2. Design

**`ast.DumpSpans(f)`** is `Dump(f)` with two additions. After each
node's name comes its span as `@S:E` (decimal byte offsets), so
`(ident x)` becomes `(ident@12:13 x)`. The extra positions follow the
field they belong to, in the same form: a `BudgetItem`'s name is
`Name@S:E`; a `With`'s names and an `Arm`'s binds are lists of
`Name@S:E`; a `For`'s `Var` and `Var2` are `Name@S:E` (`_` with no
position when there is no second name). Lists, nil nodes and every other
field are unchanged. `Dump` itself does not change.

**The kern parser** (informative: the representation is not required,
only R2 and R3's output) gets a node span (two `usize` fields on `Node`) and
the extra positions (stored where the names are stored). Every place the
Go parser sets a `Pos`, the corresponding kern function sets the span
from the same tokens or child spans, ported function by function like
stage one. `main.kern` takes an optional leading argument `spans`, which
selects the span dump; without it the output is stage one's, so the
existing fixture and test do not change.

## 3. Requirements and acceptance criteria

**R1. The Go side.** `ast.DumpSpans` writes the format of §2 for every
node type in `Dump`'s table and panics, as `Dump` does, on a node type
not in it. A test pins `DumpSpans` of `testdata/all_nodes.kern` (every
node type), and checks that removing every `@S:E` and every extra
position from `DumpSpans`'s output gives `Dump`'s output exactly, for
every corpus file the Go parser accepts (parsed as R3 parses it,
`bodyless` for the standard library and the prelude); a rejected file
has no dump and is not compared.

**R2. The kern side.** With `spans`, the kern program prints the span
dump; without it, the stage-one dump, unchanged:
`examples/selfhost_parser/selfhost_parser.out` and stage one's corpus
comparison still pass.

**R3. Parity.** `tests/selfhost_parser_test.go` gains a comparison of the
span dumps: the kern program (built natively, release) with `spans` on
every corpus file, against `ast.DumpSpans` of the Go parser's tree;
every file the Go parser accepts must produce an identical span dump,
and none may be skipped. The test reports the number compared.

**R4. A span mutation is caught.** A test or a recorded run shows that
changing one span rule in the kern parser (the report names which) makes
R3 fail on at least one corpus file, so the comparison exercises spans
and is not vacuous.

**R5. Documents.** The report records the corpus counts, any span the Go
parser produces that looks inconsistent (reproduced, not fixed), any
kern defect found, and the next stage; the stage-one report's "Next
stage" points to it.

## 4. Failure modes

- A span rule ported wrongly fails R3 with the file named; the test
  prints the first differing byte offset and both dumps around it.
- A Go parser span that differs between equivalent forms (for example a
  node built by a conversion keeping its source expression's span) must
  be reproduced exactly; R5 records it for a later decision.
