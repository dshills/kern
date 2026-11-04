# A kern parser written in kern

**Date:** 2026-11-01. **Spec:** `specs/selfhost-parser/SPEC.md` v0.1.
**Plan:** `specs/selfhost-parser/PLAN.md`. **Item:** the third of the
ten-item list ("start self-hosting"), scoped by the owner to a parser in
kern checked against the Go parser.

## Design

- **The format.** `ast.Dump` (`compiler/ast/dump.go`) writes a parsed
  file as one line of S-expressions: every node type with its fields in a
  fixed order, names and literal text as written, string values as hex,
  no spans. Its doc comment is the table; `testdata/all_nodes.kern` uses
  every node type the parser produces and pins the dump.
- **The program** (`examples/selfhost_parser/`, a project with a
  `kern.mod`): `main.kern` reads the file named on the command line
  (`bodyless` for the standard library and the prelude) and prints the
  dump, `error` on a syntax error (exit 1), or `cannot read` (exit 2);
  with no arguments it parses an embedded sample, so it runs as a
  fixture. Module `syntax` holds the lexer (`lex.kern`, 328 lines), the
  node table (`node.kern`), the parser (`parse.kern`, `stmt.kern`,
  `expr.kern`, about 1,580 lines) and the dumper (`dump.kern`).
- **The port.** Function by function from `compiler/parser`: the nesting
  counter that makes newlines insignificant inside brackets, the
  no-struct-literal flag for statement headers, the same lookahead, and
  `exprToTypeName` / `ExprToType` for struct literal types and generic
  arguments. Nodes are one `Vec[Node]` addressed by index, index 0 the nil
  node, a list a node whose children are its elements. Go's bailout panic
  became a `failed` flag: once set the current kind reads as end of file,
  every list loop stops, and each parse function returns.

## Result

`tests/selfhost_parser_test.go` builds the program natively (release),
runs it on every `*.kern` file under `tests/`, `std/`, `examples/`,
`bench/kern/` and the prelude, and compares accept/reject and the dump
with the Go parser's: **490 files compared, 489 accepted and 1 rejected
by both, every dump identical, none skipped** (no corpus file has a
non-ASCII identifier). Nine inline syntax errors (unterminated string,
unknown escape, missing brace, integer past `u64`, array length with a
suffix, `truncate` of a non-primitive, a struct literal type that is not
a type name, an interface position that is not a type name, a non-ASCII
byte outside a string) are rejected by both. Renaming one node in the kern
dumper fails 39 files, so the comparison is live. The example also runs
under the fixture harness, interpreter and native at both opt levels.

The port agreed with the Go parser on the first corpus run: the bugs
were found by the type checker before the program ran.

## What porting found

- **Keywords as names.** `out` and `contract` are keywords, so the Go
  parser's `out` buffer and `contract` local needed new names; the
  checker reported both at once.
- **Calls inside calls on `self`.** `self.add(l, self.parse_x())` is an
  access conflict (the argument's `edit self` overlaps the callee's), so
  every parsed child is bound to a local first; `a() && b()` on `self`
  is fine. This is most of the port's extra lines.
- **A live binding blocks reads of `self`.** Inside `with s :=
  self.src.as_view() { ... }`, calling another `view self` method is a
  conflict, so offsets are read before the binding.
- **Owned results.** `match io.read_file(path)` cannot destructure an
  owned `Result` holding a `Vec`; a helper unwraps it with `?`.
- **No references** shaped the tree: children are indices into one
  vector, and a node is changed through `nodes.at(i)`, which is what the
  Go parser's in-place edits (`d.View = true`, a folded minus) became.
- No compiler defect was found.

## Next stage

Spans (every node's byte range, which the dump omits), diagnostics
(message and position parity), and then the resolver's first pass over
the same node table. *Since 2026-11-04* spans are done
(`docs/reports/selfhost-spans.md`): the kern parser's span dumps match
`ast.DumpSpans` on all 513 accepted corpus files; diagnostics are next.

## Review

- **speccritic** (gpt-6.1-sol, ADR-013 as context): no critical finding;
  warnings folded in (the dump table's ownership, the read-failure exit,
  porting choices in normative text).
- **plancritic** (davin-go): no critical finding; warnings folded in
  (the subprocess contract, the clang skip, the report's sections).
- **prism** (default settings, medium effort): four medium and 26 low
  findings. Adopted: `ast.Dump`'s nil check made generic (any nil
  pointer node, by reflection); `TestDumpCorpus` fails on a walk error
  instead of dropping it; a guard on the node-name split in
  `TestDumpAllNodes`; named flag bits (`F_EDIT`, `F_SEQ`) for access
  types. Not adopted, as not defects: escapes other than `\n \t \" \\ \0`
  in the dumper (the lexer rejects them first); "the match arm loop can
  spin" (a malformed arm fails `expect`, which ends every loop);
  `convNames` bounds (conventions are 0-4 by construction); the `return`
  after `while true` (kern requires it); inspecting node 0 after a failed
  parse (its output is discarded); exponent floats and suffix placement
  (they mirror the Go lexer, which has no exponent form); performance of
  the keyword scan and per-byte reads (not a goal of this stage).
- **realitycheck** PARTIALLY_ALIGNED, score 100, no warning;
  **verifier** offline: 6 low findings, the one on new code asking for a
  `Dump` test, which `TestDumpAllNodes` is. clarion skipped.
  `golangci-lint` and `KERN_REQUIRE_QEMU=1 go test ./...` clean.
