# Self-hosting, stage three: syntax diagnostics in the kern parser

**Version:** 0.3 (2026-11-04). **Review status.** speccritic
(gpt-6.1-sol): VALID_WITH_GAPS, no critical finding; the overflow
fragment is corrected (v0.1's `1.` and 400 digits does not overflow). **Scope:** the kern parser in
`examples/selfhost_parser/` (`syntax/lex.kern`, `parse.kern`,
`stmt.kern`, `expr.kern`, `dump.kern`, `main.kern`);
`tests/selfhost_parser_test.go`; the report
`docs/reports/selfhost-diags.md` and a note in
`docs/reports/selfhost-spans.md` ("Next stage"). **Language impact:**
none. **Follows:** `specs/selfhost-spans/SPEC.md` (stage two),
ADR-013 (self-hosting). The Go lexer and parser are the reference and do
not change.

**Glossary.** R*n*: requirement *n* of §3. *The Go list*: the
diagnostics `parser.ParseWith` returns for a file, in order. That is
every lexical error when there is any, else at most one syntax error,
because the Go parser stops at its first. *A line*: a diagnostic written
as `S:E message`, its byte span and its message, with a newline. *The
corpus*: the files stage two compares. *A mutant*: a corpus file with
one token deleted, duplicated or replaced, or one fragment inserted
(§2).

## 0. Goal and non-goals

On a file that does not parse, the kern parser must report the same
diagnostics as the Go parser: the same spans and the same message text,
in the same order. Stage one already matches which files are accepted,
and stage two matches the tree's spans. This stage adds what a user of a
kern-written front end would see on an error.

Non-goals:
- files with a byte at or above 0x80 outside string literals and
  comments, which the kern lexer does not support (stage one); they are
  counted and skipped;
- the diagnostic code (always `SYNTAX`) and file name, which carry no
  information here;
- any diagnostic after the first syntax error (the Go parser stops
  there).

## 1. Background

The Go lexer records each lexical error and keeps lexing:
- an unknown escape (`unknown escape`, the backslash and the next byte),
  and the string continues;
- a newline inside a string (`unterminated string`, from the opening
  quote to the newline), and the string token ends there;
- end of file inside a string (`unterminated string`, to the end);
- a byte that starts no token (`unexpected character %q`).

`ParseWith` returns all of them and does not parse. Otherwise the parser
reports its first error at a token and stops. The 15 message forms are
in `compiler/parser/parser.go` (`errorf`, `expect`, `stmtEnd`,
`describe`) and use the kind names of `compiler/lexer/token.go`. The
kern lexer returns false at its first lexical error, and the kern parser
records only that it failed.

## 2. Design (informative)

- **The kern lexer** records lexical errors as the Go lexer does and
  keeps lexing with the same recovery. An unexpected ASCII character's
  message quotes it as Go's `%q` does: a printable character as itself
  (with `\` before `"` and `\`), `\a \b \f \n \r \t \v` for those
  controls, and `\xNN` for other controls and DEL.
- **The kern parser** records the first failure as a token span and a
  message, built at the site that corresponds to each Go `errorf`, from
  the same token and the same kind names.
- `main.kern` takes the word `diags`. It then prints the lines (nothing
  for a file that parses) and exits 1 when there is one, 0 otherwise.
  Without the word, the output is unchanged.
- **The contract.** `syntax.Parse` returns 0 when the file parsed, 1 on
  a lexical or syntax error, and 2 for an unsupported byte (the
  non-goal). Lexical errors take precedence: when there is any, all of
  them are the diagnostics and the file is not parsed, as `ParseWith`
  does. With `diags`, the program maps these as follows:
  - 0: print nothing, exit 0;
  - 1: print the lines, exit 1;
  - 2: print `unsupported`, exit 1.

  Without `diags`, 1 and 2 both print `error` and exit 1, as before.
- **The float check.** The Go parser rejects a float literal whose value
  overflows (`invalid float literal`), and the kern parser does not yet.
  It is added with `str.parse_f64` on the literal's digits.
- **Mutants**, made by the test from the Go lexer's tokens with a fixed
  seed: four per accepted corpus file, each one of:
  - delete token *i*;
  - duplicate token *i*;
  - replace token *i* with one of `)` `}` `]` `,` `:` `=` `func`
    `return` `1` `x` `.`;
  - insert before token *i* one of `$`, `@`, `#`, `` ` ``, `"a\qb"`, an
    unterminated `"ab` at the end of a line, or `1` followed by 400
    zeros and `.0` (a float above the largest finite `f64`, which the Go
    parser rejects with `invalid float literal`).

## 3. Requirements and acceptance criteria

**R1. Lexical errors.** For every input in R4's sets with lexical
errors, the kern program with `diags` prints the Go list's lines exactly,
all of them, in order.

**R2. Syntax errors.** For every input in R4's sets the Go parser rejects
after lexing, the kern program prints exactly one line, equal to the Go
list's line.

**R3. Unchanged output.**
- Without `diags`, the program's output is stage one's and stage two's:
  `selfhost_parser.out` and both corpus comparisons pass unchanged.
- With `diags` on a file that parses, it prints nothing and exits 0.
- For every corpus file and mutant, the program accepts exactly when the
  Go parser accepts.

**R4. The test.** `TestSelfhostDiags` (`tests/selfhost_diags_test.go`)
runs the native release build with `diags` on:
- every corpus file;
- stage one's reject list;
- every mutant (at least 1,500).

It compares each with the Go list rendered as lines, and fails on any
difference, printing the input's name and both outputs. It reports:
- the counts of inputs, of inputs with lexical errors and of inputs
  with a syntax error;
- the number of distinct message forms seen, which must be all 19 (15
  parser, 4 lexer), with targeted inputs for the forms mutants rarely
  make;
- float literals at the edges of `ParseFloat`'s range: zero, leading
  zeros, underflow, the largest finite value, just above it, far above
  it;
- the number skipped under the non-goal, which must be 0 for the
  corpus.

**R5. A message mutation is caught.** A recorded run shows that changing
one message form in the kern parser makes R4 fail. The report names the
form, the number of failing inputs and the first failure.

**R6. Documents.** The report records the counts, the message forms seen
and not seen (and why each unseen one is hard to reach), any
disagreement found and how it was resolved, and the next stage; the
stage-two report's "Next stage" points to it.

## 4. Failure modes

- A kern failure point that fires at a different token from Go's
  `errorf` shows as a span or message difference on a mutant, with the
  mutant's description in the failure.
- A message form that no mutant reaches is listed in the report and not
  claimed as checked.
