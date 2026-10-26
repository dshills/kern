# kern canonical formatting

This document is normative for the `format` command (spec §16) and for
`compiler/format`. The formatter parses a file and reprints its syntax tree;
it never changes the program's meaning. A file that does not parse is not
formatted: `format` returns the parse diagnostics unchanged and no output.

## Required properties

For every source file `x` that parses:

1. **Idempotence.** `format(format(x)) == format(x)`, byte for byte.
2. **Diagnostic preservation.** `check(format(x))` yields the same multiset of
   diagnostic codes as `check(x)`. Spans may differ; codes may not.

Both properties are tested on every fixture under `tests/` and `examples/`.

## Lines and whitespace

- Indentation is four spaces per nesting level; tabs are never emitted.
- No line has trailing whitespace. The output ends with exactly one newline.
- A line break ends every statement, declaration, struct field, union
  variant, interface method, contract clause and match arm, except inside a
  body kept on one line (below). The `;` terminator is emitted only there.
- Expressions are printed on one line, however they were written, with the
  single exception of struct literals (below).
- Blank lines: where the source has one or more blank lines between two
  consecutive items (declarations, statements, members or comments), exactly
  one blank line is kept. Blank lines never follow an opening brace, precede
  a closing brace, or separate a contract block from its function. There is
  no blank line before the first line of the file.

## Declarations

- `module` and each `import` occupy one line each, in source order.
- Declarations appear in source order with their blank-line separation (at
  most one) preserved. `view struct`, `nocopy`, `extern "abi" func`,
  `unsafe assert M for T`, `const`, `impl [TP] Iface for T` and
  `impl [TP] T` are printed exactly in the grammar's keyword order.
- Type parameters print as `[T, U: Copy + Send]`: no space inside the
  brackets, `: ` before the first constraint, ` + ` between constraints.
- A contract block precedes its function on lines of its own, with no blank
  line between the closing `}` and the `func` line. Clauses keep source order.
- Function signatures print as `func name[TP](p: T, view self, q: own U,
  r: out V) -> R release rel`; receivers are `view self`, `edit self` or
  `own self`; tuple returns are `-> (A, B)`; unit is `()`.

## Brace bodies

A brace body is the member list of a struct, union, interface, impl or
contract, or a statement block.

- An empty body prints as `{ }`.
- A body whose source was written on one line stays on one line, printed as
  `{ item; item }`, provided it contains no comment and no `match`
  statement. This covers `struct P { x: u64; y: u64 }`, `union E { A; B }`
  and `func f() { a(); return b }` alike.
- Otherwise the opening brace ends the line it is on, each member is printed
  on its own line one level deeper, and the closing brace is on its own line
  at the enclosing level.
- `else` and `else if` continue the line of the preceding `}`.
- A `match` is always laid out with one arm per line. An arm body written as
  a block keeps its braces (one line or many, by the rule above); an arm with
  a single bare statement prints `pattern => statement`.
- Struct literals print `T { f: v, g: w }` on one line; a literal whose
  source spanned several lines prints one field per line with a trailing
  comma after each field and the closing brace at the enclosing level.

## Tokens and spacing

- One space on each side of binary operators (`+`, `==`, `&&`, …), of `=`,
  `:=` and `->`, and around `=>` in match arms. Unary operators and `?` are
  attached to their operand; `- -x` keeps a separating space.
- No spaces around `..` in ranges, `.` in field and method access, or `:` in
  slices (`a[lo:hi]`).
- A single space after every comma and after the colon of a field, parameter
  or struct-literal field; none before.
- Call arguments print as written (`out x`, `move x`); `copy(x)`, `take(x)`,
  `replace(p, v)`, `drop(x)`, `trap()`, conversions `u64(x)` and
  `truncate[u8](x)` have no inner spaces.
- Integer and float literals keep their source text, including `_`
  separators and type suffixes. String literals are re-escaped with the
  escapes the lexer accepts (`\n`, `\t`, `\"`, `\\`, `\0`).

## Comments

- Comments are never dropped. Each comment's text (from `//` to the end of
  its line, trailing whitespace removed) appears in the output.
- The leading `//!` fixture header lines are preserved verbatim at the top
  of the file, before `module`.
- A comment that is alone on its line is printed alone on its line, at the
  indentation of the item that follows it (or of the enclosing body when it
  is the last thing in it).
- A comment that follows code on the same line is appended to the line of
  the item it followed, separated by two spaces. A comment inside a
  multi-line expression or header is appended to the first line printed for
  that statement.
- A body containing a comment is never collapsed onto one line.
