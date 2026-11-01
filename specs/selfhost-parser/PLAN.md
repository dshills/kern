# Plan: a kern parser written in kern

**Spec:** `specs/selfhost-parser/SPEC.md` v0.1. **Profile:** davin-go.
**Review status.** plancritic (davin-go): no critical finding; its
warnings (the subprocess contract, the clang skip, the report's
sections) are folded in; its out-of-scope note is the owner's review
workflow.

**Order:** Phase 1 (the Go dumper, which fixes the format) before Phase 2
(the kern program, which must match it); Phase 3 closes. Each phase ends
with `golangci-lint run ./...` and `go test ./...` green. One commit after
Phase 3, on `main`, not pushed. No compiler or prelude source changes, so
no IR baseline is needed (the capture corpus does not include
`examples/`); if a compiler defect must be fixed on the way, a baseline is
taken first and R-numbered in the report.

## Phase 1: `ast.Dump` (R1)

**Files.** `compiler/ast/dump.go`, `compiler/ast/dump_test.go`.

1. The doc comment's table: one line per node type, its name and fields
   in order, following spec §2's rules (names raw, string values `x`+hex,
   `_` for nil or empty optional names, `t`/`f`, decimal integers, the
   five conventions).
2. `Dump(f *File) string`, a type switch per node; an unknown node type
   panics (a parser change that adds a node must add its form).
3. Tests: a hand-written file using every node type, parsed and dumped,
   compared with an expected string; every accepted corpus file dumps to
   a non-empty, repeatable string.

**Acceptance.** Tests green; the table and the code agree (reviewed).

## Phase 2: the kern program (R2–R4 code)

**Files.** `examples/selfhost_parser/kern.mod`, `main.kern`,
`syntax/lex.kern`, `syntax/parse.kern`, `syntax/dump.kern`,
`main.out` (Phase 3).

1. `lex.kern`: token kinds as constants in the Go lexer's order; the
   keyword table; the newline rule; strings with the five escapes and
   both unterminated cases; numbers with `_`, a fraction, and the suffix
   rule; the punctuation table, longest first; a byte at or above 0x80
   outside strings and comments is an error.
2. `parse.kern`: a `Node` table (`Vec[Node]`, index 0 the nil node; a
   node holds its kind, up to two source ranges for names and text, an
   integer, flag bits and a `Vec[usize]` of children in fixed slots), and
   the Go parser ported function by function, with a `failed` flag that
   every loop condition and parse function checks.
3. `dump.kern`: the table of Phase 1, writing into a `str.String`.
4. `main.kern`: arguments (path, optional `bodyless`), `io.read_file`
   (exit 2 and `cannot read <path>` on failure), lex, parse, dump to
   standard output (exit 0), or `error` (exit 1); with no arguments the
   embedded sample.

**Acceptance.** The program builds natively and runs on a handful of
corpus files with output equal to `ast.Dump` (checked by hand before the
test exists).

## Phase 3: corpus test, fixture, documents, review (R2–R5)

**Files.** `tests/selfhost_parser_test.go`;
`examples/selfhost_parser/main.out`; `docs/decisions/ADR-013-self-hosting.md`;
`docs/reports/selfhost-parser.md`.

1. The corpus test: build once with `project.Load` and `native.Build`
   (release) into a temporary directory; for each corpus file decide the
   mode, run the Go lexer and parser, skip files whose tokens include a
   non-ASCII identifier (counted), run the program, and compare accept /
   reject and the dump. The subprocess contract: exit 0 with exactly the
   dump is an accept, exit 1 with exactly `error` a reject; exit 2, any
   other status, a signal, other output or a failure to start fails the
   test as a harness or runtime error, naming the path and the captured
   output. The test skips when `native.Available()` is false (no clang); the inline rejection inputs of R3 the same way;
   report the counts with `t.Logf`; fail under 400 compared files.
2. Fix every mismatch in the kern program (the Go parser is the
   reference); record each class of mismatch in the report.
3. The fixture: the embedded sample's `.out` equals `ast.Dump` of the
   sample (the test checks it); the entry file's header is
   `//! expect: output main`.
4. ADR-013 gains a note dated 2026-11-01; the report has sections for
   the design, the corpus counts (compared, accepted, rejected, skipped
   for non-ASCII identifiers), what porting found (language friction and
   any defects), and the next stage (spans, diagnostics).
5. Review: stage everything; `golangci-lint`, `KERN_REQUIRE_QEMU=1 go
   test ./...`, `prism review staged --paths
   'compiler/**,examples/**,tests/selfhost_parser_test.go'
   --reasoning-effort medium` (the owner's default settings),
   `realitycheck check --max-tokens 65536`, `verifier analyze -mode
   offline` excluding `.claude/**`; clarion skipped. The tools are the
   owner's installed CLIs, run in this session with these commands; if
   one cannot run, the commit waits and the owner is told (the spec's
   gates are lint and tests).
6. Commit `feat(selfhost): a kern parser written in kern`.

**Acceptance.** Every compared corpus file and every R3 input agrees;
the fixture passes in its configurations; documents written; reviews
clean or justified; committed.

## Risks

- A mismatch rooted in a Go parser quirk (for example `isTypeLike`'s
  upper-case rule) is reproduced in kern, not fixed in Go.
- Native build time for a 2,000-line program is paid once per test run.
