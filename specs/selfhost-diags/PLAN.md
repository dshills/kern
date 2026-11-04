# Plan: syntax diagnostics in the kern parser

Implements `specs/selfhost-diags/SPEC.md` v0.3 in two phases and one
commit.

## Phase 1: the kern lexer and parser

**Goals.** R1, R2 and R3.

**Files.**
- `examples/selfhost_parser/syntax/lex.kern`:
  - `LexErr` and its three kinds;
  - `lex` records errors with the Go lexer's recovery and returns false
    only for an unsupported byte at or above 0x80:
    - an unknown escape is the backslash and the next byte, and the
      string continues;
    - a newline or the end of the file ends an unterminated string token
      there, after an error from the opening quote;
    - an unexpected ASCII byte is reported and skipped;
  - `kind_name`, `keyword_text` and `punct_text` give the Go lexer's
    names, with `'kw'` for keywords and `token` as the fallback;
  - `go_quote` is Go's `%q` for one ASCII byte.
- `parse.kern`:
  - `P` gets the first failure's span and message (`es`, `ee`, `emsg`);
  - `fail_at` records only the first failure;
  - `fail_cur`, `fail_text` and `fail_found` record from the current
    token, a fixed text, or a head followed by `describe`;
  - `describe` is the Go parser's;
  - `expect` and `stmt_end` give their Go messages.
- `parse.kern`, `stmt.kern`, `expr.kern`: each bare failure becomes the
  message of the Go `errorf` at the same point, from the same token. That
  is the declaration (at the token that began it), the impl interface,
  the contract keyword, the budget placement, the array length, the
  type, the block's end of file, the struct-literal type name (both
  forms), the type argument (both places), the integer range, the
  truncate type, the expression, and `float_ok` for float literals.
- `dump.kern`: `Parse` returns 0 (parsed), 1 (a lexical or syntax
  error) or 2 (an unsupported byte). Lexical errors are checked first:
  when there is any, all of them are written and the file is not parsed.
  With `diags` it writes the lines (`diag_line`): every lexical error in
  order, or the parser's one recorded failure.
- `main.kern`: the word `diags`. With it: 0 prints nothing and exits 0;
  1 prints the lines and exits 1; 2 prints `unsupported` and exits 1.
  Without it, 1 and 2 both print `error` and exit 1, as before.

**Key decisions.**
- **Messages are built at the failure.** Each failure site builds its
  message where it happens, not from a code table, so each site reads
  like its Go `errorf`.
- **The float check uses `str.parse_f64`.** Leading zeros are dropped
  first so the digits are one JSON number. Go's `ParseFloat` rejects
  only overflow: underflow is 0 with no error, checked by hand.

**Acceptance.** `selfhost_parser.out`, `TestSelfhostParser` and
`TestSelfhostSpans` pass unchanged.

## Phase 2: the parity test and the report

**Goals.** R4, R5 and R6.

**Files.**
- `tests/selfhost_diags_test.go`: `TestSelfhostDiags`. It builds the
  program natively at release (`selfhostBin`) and runs it as
  `prog <path> diags [bodyless]` on each input.
  - **Inputs:** the corpus, four seeded mutants per accepted corpus file
    (§2 of the spec), stage one's reject list, and `diagCases`. The
    cases reach the forms mutants rarely make, and the float literals at
    the edges of `ParseFloat`'s range (zero, leading zeros, underflow,
    the largest finite value, just above it, far above it): the impl
    interface, the
    contract keyword, the type argument in both places, both
    struct-literal type-name forms, a control byte, and several lexical
    errors in one file.
  - **Comparison:** each input's output and exit status are compared
    with the Go list rendered as lines.
  - **Message forms:** each message is classified into one of 19 forms
    (15 parser, 4 lexer), and every form must be seen.
  - **Failures** print the input's name (with the mutation's
    description) and both outputs.
  - **Counts:** the test logs the inputs, the mutants, the inputs with
    lexical errors and those with a syntax error. It fails if a corpus
    file is skipped, or if there are fewer than 2,000 inputs or 1,500
    mutants.
- **R5, the mutation check.** Change `stmt_end`'s message, run the test,
  record the failure count and the first failure, then revert.
- `docs/reports/selfhost-diags.md`: the counts, each form with its
  number of hits and the input that reaches it, the disagreements found
  and how each was resolved, the R5 run, and the next stage (the
  resolver's first pass). Plus a pointer in the "Next stage" section of
  `docs/reports/selfhost-spans.md`.

**Acceptance.** `TestSelfhostDiags` passes with all 19 forms seen and no
corpus file skipped; `go test ./...`, `golangci-lint run ./...` and
`gofmt -l` are clean.

**Risks.** A failure point that fires at a different token from Go's
shows on a mutant with its description. Mutants with a non-ASCII byte
outside strings are skipped and counted.
