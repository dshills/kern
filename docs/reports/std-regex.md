# Standard library: regular expressions (`std/regex`)

**Date:** 2026-11-12, with the nesting limit as changed on 2026-11-13.
**Spec:** `specs/std-regex/SPEC.md` v0.18.
**Plan:** `specs/std-regex/PLAN.md`, seven phases. **Hosts:** macOS 27.2
on arm64, Ubuntu 24.04 on arm64 in the `kern-linux-check` container, and
the three systems of the project's CI. **Reference:** `docs/regex.md`.

Two of the example programs needed regular expressions and each wrote its
own; `examples/kawk`'s was a full engine of 630 lines. The library had
none. This work adds one module, written in kern with no `unsafe` block
and no runtime entry, moves `kawk` onto it, and brings `kawk`'s comparison
corpus into the repository.

## What was built

- **`std/regex`** (1,650 lines of kern): `compile` and `compile_with`
  (POSIX extended syntax over bytes, `ignore_case`, `newline`, a size
  limit, eleven kinds of error each at a defined index); `is_match` and
  `find` (the leftmost-longest match, in one pass over the text);
  `captures` (each group's span, by a second pass over the matched
  stretch); `Cursor`, `next`, `next_captures` (every match, left to right);
  `replace_first`, `replace_all` (templates with `&` and `\1` to `\9`);
  `split`. A `Scratch` holds the working memory, and a search with a
  reserved one makes no allocation call.
- **`examples/kawk` on the module.** `lang/regex.kern` is gone;
  `lang/awkre.kern` rewrites an awk pattern into one the module reads to
  the same effect and holds the two search helpers awk's rules need.
  `kawk.out` is unchanged.
- **`examples/kawk/corpus`**: the 555 programs `kawk` was compared with
  the system awk on, each with what awk printed and returned, and
  `TestKawkCorpus`, which runs them without an awk.

## How it was checked

| check | size | result |
|---|---|---|
| `tests/run/regex_syntax`: every row of the error table, a backslash before each of the 256 bytes inside and outside brackets, the twelve classes against all 256 bytes, sizes at the limit and one below | 337 lines of expected output | passes in the four configurations |
| `tests/run/regex_match`: `find` and `is_match`, each row with a new and with a shared `Scratch` | 246 + 17 rows | passes |
| `tests/run/regex_groups`: captures, cursors, templates, replacement, splitting. Its expected output was derived by hand from the spec by someone who had not seen the implementation, before the functions existed | 298 cases | 297 agreed on the first run; the one that did not is a case the spec leaves open (below) |
| `TestRegexOracle`: every match, every group span and `replace_all`, against Go's `regexp.CompilePOSIX` on generated pairs | 20,000 pairs in the suite; 200,000 once by hand (662,490 lines natively, 20,000 pairs in the interpreter) | no difference |
| `tests/run/regex_cost`: `(a*)*b`, `(a|aa)*c`, `a*a*a*a*a*b` against 20,000 bytes, by `find` and by `captures` | | about 2 s in the interpreter, a few milliseconds natively |
| `tests/run/regex_noalloc`: the allocation-failure hook stays armed across `is_match`, `find`, `captures`, both cursor runs, `replace_all` and `split`; its control in `tests/trap` shows the hook does fire | 3 patterns | passes where hooks exist (the interpreter and native debug) |
| `tests/concurrency/regex_shared`: four threads on one `Regex` | 1,000 searches each | passes, clean under ThreadSanitizer |
| `tests/freestanding/regex` | | passes on both QEMU targets; `kern check --env=freestanding` reports nothing |
| `TestKawkCorpus` | 555 cases | all agree on macOS; on Linux the 528 unmarked ones run and agree |
| `kawk`'s old engine against the module through `awkre.kern`: acceptance and every search `kawk` makes, pattern by pattern | 242,837 patterns, about 30 million searches | 2 differences in acceptance (below), none in a search |

The implementer's own differentials against Go, outside the suite, agree
with these: the pattern language and program length on 12,000 patterns,
`find` from every offset on 393,414 searches, captures and cursors on
60,000 patterns (1.2 million matches), and 118,101 pairs that repeat an
operand able to match the empty string.

The fourth configuration, the interpreter at release, is not one the
fixture harness runs; every fixture above was run in it by hand and
compared with the same expected output.

## The measurement (T9)

`kawk` on the module against `kawk` at 146f4df, both built natively at
release, on the 200,000 lines the spec's generator prints; instructions
retired, the least of three runs each, measured twice (by the port's
author and again on the finished module):

| program | old | on the module | ratio |
|---|---|---|---|
| `{ s += $3 } END { print s }` | 388,475,063 | 389,753,761 | 1.003 |
| `{ for (i = 1; i <= NF; i++) c[$i]++ } END { ... }` | 3,501,350,267 | 3,498,738,733 | 0.999 |
| `{ n += gsub(/a[a-z]/, "X") } END { print n }` | 1,621,092,197 | 1,513,958,841 | 0.934 |

The limit was 1.10. Of eighteen other pattern-heavy programs on the same
input, sixteen are between 0.69 and 1.01; the two above are field
separators that can match the empty string (`FS = "x*"` at 1.15), where
`kawk` now makes one `find` a byte in place of the old engine's single
pass in its non-empty mode.

## Decisions taken while building, now in the spec

- **A pattern's size** is worked out from its text (R3), not from a
  compiled form, and an open interval `x{n,}` counts *n* copies, so that
  `x{1,}` is the size of the `x+` it equals.
- **Bracket expressions** (R5, R8): a `-` after a member and before a byte
  other than `]` is the range operator, even when that byte is a `-`; a
  `-` first or directly after a range may begin a range; a class as a
  range's end is `BadRange`; an unknown or unfinished `[:` is `BadClass`.
- **The comparison with Go** is made in the module's `newline` mode
  (which is how Go's POSIX mode reads `.`, negation and anchors), on ASCII
  texts, without `ignore_case`, and only for patterns Go compiles.
- **R17 is checked by the allocation-failure hook**, not by a `budget`
  block: a probe showed that `budget { allocations 0 }` counts the
  allocation sites of the function it is written on and accepts one whose
  callee grows a vector.
- **A repetition whose operand matches the empty string** (`(a*)*`) is the
  one place R13 does not fix the groups. The module gives what Go gives
  there (after a change to how such a `*` is emitted), on 118,101 pairs
  without a difference; the spec still leaves it open, and
  `regex_groups`'s last section records the module's answers.
- **`replace_all` can be quadratic** over a whole call for a pattern that
  matches the empty string at most offsets after looking far ahead; R18
  bounds one search, and now says so.

## For the owner

- **The nesting limit: settled on 2026-11-13.** As built, the module took
  groups 200 deep, so groups `kawk`'s rewriting cannot remove nested 200
  deep where `kawk`'s own reader took 201 and refused 202 (the system awk
  takes both). The owner had R4's limit raised so that `kawk` keeps its
  201. The limit is 202 (spec 0.18), not 201: the rewriting puts one
  group of its own around a piece whose count is above 1000 when another
  operator follows, so `(a{1001}+|b)` inside 200 more groups reaches the
  module 202 deep, and `kawk`'s own reader took it. `kawk` still refuses
  202 as written, itself. Corpus case 556 nests 201 deep both ways, which
  makes the corpus 556 cases (529 unmarked), and the differential above,
  run again on its fixed sets and three of its generated ones, finds no
  difference in acceptance (the two it found were these).
- **A malformed dynamic pattern** (`"x" ~ "("`) now ends `kawk` with status
  2, as the system awk does; the old `kawk` printed its message and then
  trapped.
- **Groups are not POSIX's.** The whole match is leftmost-longest; the
  groups are those of the first way a backtracking search finds, as in
  Go. `(a|ab)(c|bcd)` on `abcd` gives `a` and `bcd`.
- **A first search sizes the tables for groups too**, so that a `captures`
  after a `find` does not allocate: a `Scratch` used only for `find` on a
  pattern of 3,000 instructions and 1,000 groups holds about 96 MB. It is
  within R18. If that is unwanted, R17 would read "after `reserve` or an
  earlier `captures`".

## What was found on the way

- `tests/freestanding/README.md` gave `--env freestanding`, which the
  command line refuses; it is `--env=freestanding`.
- The fixture harness does not pass a project's `stack_bytes` to its
  native builds; `TestKawkCorpus` reads the manifest itself.
- `kawk`'s old engine could trap `overflow` after about 4.29 billion
  offsets through one scratch (its generation count); the module clears
  its marks at the wrap.
- Test hooks exist only at debug, so a hook-based check covers two of the
  four configurations.
- **The cost fixture was ten times too long for CI.** At 200,000 bytes
  it took 18 s in the interpreter here, the interpreter's own tests run
  every fixture four times, and on the CI machines that carried
  `compiler/interp` and `tests` past Go's ten minutes a package (the
  commit of phase 4 failed there on two of three systems). It is 20,000
  bytes now (spec 0.17): a quadratic search would still take hours in
  the interpreter, so the fixture still tells.
- The review of the port found one defect the differential had not: a
  bracket expression that negates every byte (reachable only with a zero
  byte in a dynamic pattern) was rewritten as `[]`, which the module
  refuses, where the old `kawk` matched nothing with it. It is written
  as the negation of the whole range now. The differential's generators
  put no zero byte in a pattern.

## Not done

The spec's non-goals stand: no POSIX basic syntax, backreferences,
collating elements, locales, UTF-8 classes, lazy operators or `\d`-style
escapes (`compile` refuses them, so they can be given a meaning later); no
fallible-allocation forms. `examples/ksearch` keeps its own matcher: its
pattern language is its own.
