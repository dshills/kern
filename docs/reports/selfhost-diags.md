# Self-hosting, stage three: syntax diagnostics

**Date:** 2026-11-04. **Spec:** `specs/selfhost-diags/SPEC.md` v0.3
(speccritic VALID_WITH_GAPS, no critical finding). Its one model
finding was that the v0.1 overflow fragment did not overflow, which is
fixed. **Plan:** `specs/selfhost-diags/PLAN.md` (plancritic
EXECUTABLE_WITH_CLARIFICATIONS, no critical finding). Its warnings
covered the `Parse` contract, float edge cases, the mutant count and the
all-forms gate, and are folded in. **Follows:**
`docs/reports/selfhost-spans.md`.

## What changed

- **The kern lexer** now records lexical errors and recovers the way the
  Go lexer does:
  - an unknown escape is reported and the string goes on;
  - a newline or the end of the file ends an unterminated string token
    there;
  - an unexpected ASCII byte is reported and skipped.

  It quotes that byte as Go's `%q` does.
- **The kern parser** records its first failure's token span and
  message. Each of its failure points builds the message that the Go
  `errorf` at the same point builds, from the same token and the Go
  lexer's kind names (`kind_name`, `describe`).
- **A new rejection.** The kern parser now rejects a float literal whose
  value overflows (`invalid float literal`), which it had accepted
  before. The check uses `str.parse_f64`.
- **The `diags` word.** With `diags`, the program prints the diagnostics
  as `start:end message` lines: every lexical error in order, or the one
  syntax error. A file that parses prints nothing. Without the word,
  the output is unchanged.

## Result

`TestSelfhostDiags` (`tests/selfhost_diags_test.go`) runs the native
release build on 2,601 inputs:
- every corpus file;
- 2,052 mutants, four seeded ones per accepted corpus file;
- stage one's 22 reject cases;
- 14 targeted cases.

**Every output matches the Go parser's diagnostics exactly.** 437 inputs
have lexical errors and 1,518 have a syntax error. No corpus file was
skipped. One mutant was skipped because it put a non-ASCII byte outside
a string, which is outside this stage's scope. The test needed no fix
after it first ran: each kern failure point already fired at the same
token as Go's.

All 19 message forms are reached, with these hit counts:

| form | hits | form | hits |
|---|---|---|---|
| expected %s, found %s | 878 | unexpected character | 287 |
| expected end of statement | 229 | expected expression | 191 |
| expected type | 97 | unterminated string | 87 |
| expected declaration | 69 | unknown escape | 66 |
| end of file in block | 29 | invalid float literal | 9 |
| budget before func | 7 | expected type argument | 2 |
| array length | 1 | integer out of range | 1 |
| interface name expected | 1 | expected invariant/requires/ensures | 1 |
| module-qualified type name | 1 | type name before brace | 1 |
| truncate primitive | 1 | | |

Seven forms are reached only by a targeted case. The case for each:
- `impl () for T`;
- `contract { foo "x" }`;
- `A[1] { }` and `A[p.q.r] { }` (type argument);
- `A[b.c.D[u8]] { }` (module-qualified);
- `A[g()[u8]] { }` (type name);
- the reject list's array-length, integer-range and truncate cases.

The struct-literal type-name messages are reachable only through a
literal's type arguments. The literal's own type is filtered first by
`isTypeLike`.

**Float edge cases.** Both parsers accept zero, `0007.25`, an underflow
to 0 and the largest finite value. Both reject a value just above the
largest finite one and one far above it.

**R5, the mutation.** I changed `stmt_end`'s message from
`expected end of statement` to `expected end-of-statement`. The test
then failed on 229 inputs. The first failure was
`tests/adversarial/callback_retains_view.kern`, deleting bytes 263:265.
With the change reverted, the test passed again.

Stages one and two are unchanged. `selfhost_parser.out`,
`TestSelfhostParser` (514 files) and `TestSelfhostSpans` (513 files)
still pass.

## Next stage

The resolver's first pass over the same node table: declarations,
imports and name resolution. The goal is the Go resolver's diagnostics
for unknown and duplicate names.
