# A kern parser written in kern

**Version:** 0.1 (2026-11-01). **Scope:** a kern program under
`examples/selfhost_parser/` (a project with a `kern.mod`) holding a
lexer, a parser and a tree dumper; a Go tree dumper in `compiler/ast`
that defines the comparison format; a corpus test in `tests/`.
**Language impact:** none.
**Related:** `docs/decisions/ADR-013-self-hosting.md` (self-hosting is
revisited once the library exists; `examples/selfhost/lexer.kern` is the
measured starting point); `spec/grammar.ebnf`; `compiler/lexer`,
`compiler/parser` (the reference implementation).

**Review status.** speccritic (gpt-6.1-sol, ADR-013 as context): no
critical finding; its three warnings (the dump table's ownership, the
read-failure exit, porting choices in normative text) are folded in.

**Glossary.** R*n*: requirement *n* of §4. Go parser: `parser.ParseWith`
over `lexer.Lex` (`compiler/parser`, `compiler/lexer`). kern parser: the
program of §2. Corpus: every `*.kern` file under `tests/`, `std/`,
`examples/`, `bench/kern/` and `compiler/resolve/prelude/`. Bodyless
mode: `parser.Options{AllowBodyless: true}`, the mode the resolver uses
for `std/` and the prelude. Dump: the text `ast.Dump` writes for a parsed
file (§2). Accept: the parser returns a file and no diagnostic; reject:
it returns diagnostics. ASCII identifier file: a corpus file in which no
identifier token contains a byte at or above 0x80. Abbreviations: ADR,
architecture decision record; AST, abstract syntax tree.

## 0. Goals

The first real stage of self-hosting: a kern program that parses kern
exactly as the Go parser does, over the whole corpus, checked by a test
that compares the two trees. It exercises the language on a program of
compiler size (recursive descent, a node table, string building, file
I/O, multi-module structure) and gives ADR-013 a measured second step.

Non-goals: spans in the comparison (this stage compares structure, names
and literal text; byte offsets are the next stage), diagnostic messages
and positions (only accept versus reject is compared), non-ASCII
identifiers (the kern lexer rejects any byte at or above 0x80 outside
strings and comments), resolution, checking, and replacing any part of
the Go compiler.

## 1. Background

The Go lexer (274 lines) emits newline tokens only where a statement can
end; the Go parser (1,373 lines) is recursive descent with a
newline-skipping nesting counter, a no-struct-literal flag for statement
headers, and post-hoc conversion of index expressions to types for
generic arguments and struct literal types. `examples/selfhost/lexer.kern`
tokenizes one embedded string and prints a checksum; it is not a
complete lexer. `std/io` reads files and arguments; `std/str` builds
strings.

## 2. Design

**The dump format** is defined by a new `ast.Dump(f *ast.File) string`:
one line, ending in a newline, of S-expressions. Each node is
`(name field ...)` with its fields in a fixed order; a list is `[a b]`;
a nil node or an empty optional name is `_`; a boolean is `t` or `f`; an
integer is decimal; a name or literal text (identifiers, operator
spellings, number text, suffixes) is written as it is; a string value
(an import path, a string literal's unescaped value, a contract clause,
an extern ABI) is `x` followed by the lowercase hex of its bytes; a
parameter convention is `value`, `view`, `edit`, `own` or `out`. Spans and
the file name are not written. Every AST node type the parser produces
has a form. The node names, the fields each writes and their order are
the Go dumper author's choice, written as a table in `ast.Dump`'s doc
comment before the kern dumper is written; that table, with the Go code,
is the normative definition the kern dumper must match byte for byte.

**The kern program** (`examples/selfhost_parser/`): `main.kern` (module
`main`) and module `syntax` (`syntax/lex.kern`, `syntax/parse.kern`,
`syntax/dump.kern`). Given a path (and optionally the argument
`bodyless`), it reads the file, lexes, parses and prints the dump to
standard output with exit status 0; on any lexical or syntax error it
prints `error` and exits with status 1. When the file cannot be read it
prints `cannot read` and the path to standard output and exits with
status 2, so a test tells a missing file from a rejected one. With no
arguments it parses an embedded sample, so the example runs as a
fixture.

- **Lexer:** the Go lexer's tokens and newline rule exactly, for ASCII
  identifier files; a byte at or above 0x80 outside a string or comment
  is a lexical error.
- **Parser:** accepts exactly the inputs the Go parser accepts and builds
  the tree it builds (R2), including its checks (integer literals within
  `u64`, array lengths a non-negative decimal within `i64` with no
  suffix, `truncate` of a primitive) and bodyless mode, and terminates
  on every input.
  *Implementation guidance (non-normative):* port the Go parser function
  by function (the nesting counter and newline skipping, the no-literal
  flag, the lookahead, `exprToTypeName`, `ExprToType`, `parseIndexArg`);
  keep nodes in one `Vec` indexed by position with index 0 as the nil
  node; on an error set a flag that ends every loop and makes every
  parse function return.
- **Dumper:** writes the format above from the node table.

## 3. Out of scope

Spans and diagnostics parity; non-ASCII identifiers; floating-point
range errors (a float literal whose value overflows `f64`; none is in
the corpus); performance; a kern resolver or checker.

## 4. Requirements

**R1. The Go dumper.** `ast.Dump` writes every node type the parser can
produce in the format of §2. *Verified by:* a `compiler/ast` test that
dumps a file exercising every node type and compares with an expected
string, and a test that `ast.Dump` of every corpus file the Go parser
accepts is non-empty and deterministic.

**R2. Parity on the corpus.** For every corpus file that is an ASCII
identifier file, the kern parser accepts it iff the Go parser does (in
bodyless mode for `std/` and `compiler/resolve/prelude/`, the normal mode
otherwise), and for an accepted file its output equals `ast.Dump` of the
Go parser's file. *Verified by:* `tests/selfhost_parser_test.go`, which
builds the program natively once (release) and runs it on each file;
skipped when clang is unavailable; it reports how many files it compared,
accepted and rejected, and fails if fewer than 400 files were compared.

**R3. Rejections.** A set of syntax-error inputs (an unterminated
string, an unknown escape, a missing brace, an integer literal past
`u64`, an array length with a suffix, `truncate` of a non-primitive, a
struct literal type that is not a type name, an interface name that is
not a type name before `for`) is rejected by both parsers. *Verified by:*
R2's test over these inline inputs.

**R4. The example runs as a fixture.** With no arguments the program
prints the dump of its embedded sample. *Verified by:* the fixture
harness (interpreter and native, both opt levels) with a `.out` file
equal to `ast.Dump` of the sample as the Go parser parses it, which R2's
test also checks.

**R5. Documents.** ADR-013 gains a dated note; `docs/reports/selfhost-parser.md`
records the design, the corpus counts, what porting found (language
friction, defects), and the next stage. *Verified by:* review;
`go test ./tests` (citation lint).

## 5. Acceptance criteria

`golangci-lint run ./...` and `KERN_REQUIRE_QEMU=1 go test ./...` are
clean, including R2's test with every compared file in agreement.

## 6. Risks

- **Size.** The port is about 2,000 lines of kern; mismatches are found
  by the corpus test, one file at a time.
- **Language friction.** No stored references means children are indices
  and a node is edited through `at`; an owned `Result` holding a
  container cannot be matched (unwrap with `?`). Friction found is a
  result, recorded in the report.
- **Recursion depth.** Deeply nested expressions recurse in both parsers;
  the corpus has none deep enough to matter for the default stack.
