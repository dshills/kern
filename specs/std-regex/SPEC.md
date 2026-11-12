# Standard library: regular expressions (`std/regex`)

**Version:** 0.16 (2026-11-12). **Review status.** speccritic
(gpt-6.1-sol) round 1 on 0.1: INVALID, two critical findings, both
resolved here: a repeated group keeps the span of its last passage even
when an enclosing group's last iteration does not contain it, so the
nesting property 0.1 claimed is withdrawn (R13); and the oracle reads
`.`, negated expressions and anchors as the module does with `newline`
set, so the comparison is made in that mode and through match
enumeration instead of offsets (glossary, R13, T4). Four warnings
resolved: an empty `groups` slice is written nowhere (R12), every
undefined escape is `BadEscape` (R5), `split` of an empty text by an
empty-only pattern gives no field (§4), and the non-goals no longer
name an internal representation. Round 2 on 0.2: VALID_WITH_GAPS, no
critical finding; three warnings resolved in 0.3: the no-trap statement
excepts R17's allocation failure (§4), the construct opened last is the
one reported when several are open at the end (R5), and a pattern's
size is defined from its text instead of from a compiled form (R3,
R18). Round 3 on 0.3: INVALID, one critical finding, resolved in 0.4:
agreement with the oracle is claimed for ASCII texts only (glossary,
R13); and one warning: `next_captures` leaves `groups` unchanged when
it returns false (R14). Round 4 on 0.4: INVALID, one critical finding,
resolved in 0.5: the common syntax writes interval counts as one digit,
since the oracle reads a count with a leading zero as ordinary bytes;
and one warning: T9 gives its input and programs in full. Round 5 on
0.5: VALID_WITH_GAPS, no critical finding; two warnings resolved in
0.6: a `Scratch`'s memory bound is that of the largest pattern it has
served (R18), and a backslash ending the pattern is `BadEscape` before
any open construct is reported (R5). 0.16, when phases 4 to 6 were implemented: an anchor holds where the
pattern places it (glossary); R18 says what one call is and that a
whole `replace_all` can be quadratic; T5's hook check covers
replacement and splitting; `kawk` takes the module's nesting limit
(R21). 0.15, when phase 3 was implemented: which of `BadRange` and
`BadClass` a class in a range is (R5), that a `-` before a byte other
than `]` is the range operator even when that byte is a `-` (R8), and
that the hook check of T5 runs where hooks exist. 0.14, with phase 3: an open interval `x{n,}` counts *n* copies, not
*n* + 1 (R3), so that `x{1,}` is the size of the `x+` it equals and
nesting it does not double the size. 0.13 was made when phase 2 was
implemented and settles what the
reader found open: a `-` after a range begins a range, a class as
either end of a range is `BadRange`, an unfinished `[:` is `BadClass`,
an interval with nothing to repeat is `NothingToRepeat`, and sizes and
lengths stop at 32 bits (R3, R5, R8). 0.7 to 0.11 were made while the
plan was written. 0.11: the common
syntax is, by definition, within what the oracle compiles (clause h),
which settles every finding of the form "the oracle refuses a pattern
the module accepts". The last round, on 0.11: VALID_WITH_GAPS, no
critical finding; one warning resolved in 0.12 (R17's condition covers
replacement and splitting too) and one left as it is: that the Scope
names files and forbids `unsafe` and runtime entries is deliberate,
those being the constraints the owner's question was about. 0.10: a round on
0.9 found two critical conflicts, resolved: agreement with the oracle
is claimed without `ignore_case` only (glossary, R13), and `captures`'
time bound counts the length of `groups`, every element of which it
writes (R18). 0.9: a round on 0.8
found one critical conflict, resolved: the common syntax keeps nested
intervals within what the oracle compiles (glossary). 0.8: T5 checks
R17 with the allocation-failure hook, since a `budget` block proved to
count only its own function's allocation sites (a probe accepted a
function whose callee grows a vector). 0.7 changes R22 alone: the marked cases run on macOS only, because their records hold
the output of that system's commands. The remaining warnings of every
round are the reviewer's text checks (names such as R1 or README read
as undefined acronyms, and "may" or "can" in non-normative sentences).
**Scope:**
- a new library module `std/regex` (`compiler/resolve/prelude/regex.kern`,
  mirrored identically in `std/regex.kern`), written in kern with no
  `unsafe` block, no new runtime entry and no new intrinsic;
- every place the library modules are enumerated (the resolver's list of
  embedded modules, `spec/types.md` §1, `README.md`, `docs/runtime.md`,
  the generated inventories);
- `examples/kawk`, which gives up its own engine (`lang/regex.kern`) for
  the module;
- a comparison corpus for `examples/kawk` kept in the repository with its
  expected outputs, and the test that runs it (R22);
- fixtures under `tests/run`, `tests/concurrency` and `tests/freestanding`,
  a Go test that compares the module with Go's `regexp` (§5), the report
  `docs/reports/std-regex.md`, and `CHANGELOG.md`.

**Language impact:** none: a library addition.
**Follows:** `examples/kawk` (commit 146f4df), whose engine is the starting
point, and `specs/fallible-alloc/SPEC.md` (the test hook
`testing.fail_next_alloc`, which T5 checks R17 with).

**Glossary.**
- R*n*: requirement *n* of §3. *Engine*: the interpreter or the native
  build; *configuration*: an engine at an opt level (the interpreter at
  debug and at release, native debug, native release).
- *Pattern*: the bytes given to `compile`. *Text*: the bytes searched.
  Both are sequences of bytes; the module knows no encoding (§0).
- *Match*: a pair of offsets (*s*, *e*) into a text, 0 ≤ *s* ≤ *e* ≤
  `len(text)`, such that the bytes `text[s:e]` are in the language of the
  pattern under R6 to R10, each anchor of the pattern holding (R9) at
  the offset of the text where the pattern places it, which is *s*,
  *e* or anywhere between (`(a$)\n(^b)` with `newline` matches `a\nb`).
  A match is *empty* when *s* = *e*.
- *The match from f* (0 ≤ *f* ≤ `len(text)`): among the matches with
  *s* ≥ *f*, the one with the least *s*, and among those the one with the
  greatest *e*. There is at most one. This is POSIX's leftmost-longest
  rule.
- *Group*: a parenthesized subexpression. Groups are numbered from 1 in
  the order of their opening parentheses; group 0 is the whole match.
- *ASCII letter*: a byte in `A`-`Z` or `a`-`z`. *Digit*: a byte in
  `0`-`9`. *Punctuation*: a byte from 33 to 126 that is neither an ASCII
  letter nor a digit.
- *POSIX*: IEEE Std 1003.1, whose "extended regular expressions" §2
  follows. *ASCII*: the 128 characters of ISO 646, one byte each.
  *UTF-8*: the byte encoding of Unicode text.
- *The oracle*: `regexp.CompilePOSIX` of the Go toolchain that builds
  the repository (`go.mod` asks for Go 1.25 or later), used through
  `FindAllSubmatchIndex(text, -1)`. It reads `.`, negated bracket
  expressions, `^` and `$` as this module does with `newline` set (R6,
  R8, R9), and it enumerates matches as a cursor does (R14).
- *The common syntax*: the patterns of §2 that (a) hold only bytes below
  128, (b) use no escape other than a backslash before punctuation, (c)
  apply no repetition operator directly to another repetition operator's
  result, (d) apply no repetition operator to an operand that matches
  the empty string, (e) write every interval count as one digit from
  `0` to `3`, (f) name no class but `[:alpha:]`, and (g) nest intervals
  so that the product of the greatest counts of the intervals around
  any atom, that atom's own included, is at most 1000, and (h) that
  the oracle compiles. Clause (h) is what makes the comparison well
  defined: the oracle refuses some patterns this module accepts (nested
  intervals past 1000, a `-` directly after a range as in `[a-b-c]`),
  and a pattern it refuses is outside the common syntax whatever the
  other clauses say. T4's generator produces only patterns the oracle
  compiles, and treats a refusal as its own defect. (The oracle takes
  a count with a leading zero, `a{01}`, for ordinary bytes; this module
  takes it for the count 1, as awk does.) On an *ASCII text* (one holding only bytes
  below 128) the oracle and this module compiled with `newline` set
  and `ignore_case` not set read these patterns the same way; on other texts the oracle matches
  characters where this module matches bytes, and nothing is claimed.

## 0. Goal and non-goals

Two of the example programs needed regular expressions and each wrote its
own: `examples/ksearch` a reduced matcher (no alternation, no groups, slow
on some patterns by its own account), `examples/kawk` a full one of about
630 lines. The library has none. This spec adds one module that both kinds
of program can use: POSIX extended syntax, the leftmost-longest match,
capture groups, replacement and splitting, in time proportional to the
text, with no allocation during a search.

The engine of `examples/kawk` is the starting point: it already reads the
syntax of §2 (apart from its deliberate copies of the system awk's
mistakes), finds the match from *f* in one pass, and has been compared
with `/usr/bin/awk` on 548 programs. It has no groups; R12 to R14 are the
new work.

Non-goals:
- POSIX basic syntax (`\(`, `\{`), backreferences, collating elements
  (`[.x.]`) and equivalence classes (`[=x=]`): the last three are refused
  by `compile` (R5), the first is simply another language.
- Locales and encodings: a pattern and a text are bytes, `.` is one byte,
  the named classes are those of the C locale, and case is folded for
  ASCII letters only. UTF-8 text can be searched for UTF-8 literals; a
  class or `.` does not match a whole multi-byte character.
- The subexpression rule of POSIX (the longest match for each group in
  turn): R13 gives the rule this module has instead.
- A leftmost-first ("Perl") matching mode, lazy operators, look-around,
  `\d` `\w` `\s` `\b`: the escapes are refused (R5) so that they can be
  given a meaning later.
- Fallible-allocation forms (`try_compile`) and allocator-generic forms:
  `compile` and a `Scratch` that grows trap `oom` as `Vec` does.
- Porting `examples/ksearch`: its pattern language is its own, and
  changing it changes what the example accepts.

## 1. Interface

```kern
module regex

struct Options { ignore_case: bool; newline: bool; max_insts: usize }
func options() -> Options            // false, false, 100000

union ErrKind {
    UnclosedGroup; UnmatchedClose; UnclosedBracket; BadRange; BadClass
    BadEscape; BadInterval; NothingToRepeat; Unsupported; TooDeep; TooLarge
}
struct CompileErr { kind: ErrKind; at: usize }

struct Span { start: usize; end: usize }

struct Regex nocopy { ... }
func compile(pattern: view[]u8) -> Result[Regex, CompileErr]
func compile_with(pattern: view[]u8, o: Options) -> Result[Regex, CompileErr]

struct Scratch nocopy { ... }
struct Cursor { ... }                // Copy; made by cursor()
func cursor() -> Cursor

impl Scratch {
    func new() -> Scratch
    func reserve(edit self, re: view[Regex])
}

impl Regex {
    func groups(view self) -> usize
    func is_match(view self, sc: edit[Scratch], text: view[]u8) -> bool
    func find(view self, sc: edit[Scratch], text: view[]u8, from: usize) -> Option[Span]
    func captures(view self, sc: edit[Scratch], text: view[]u8, from: usize, groups: edit[]Option[Span]) -> bool
    func next(view self, sc: edit[Scratch], text: view[]u8, cur: edit[Cursor]) -> Option[Span]
    func next_captures(view self, sc: edit[Scratch], text: view[]u8, cur: edit[Cursor], groups: edit[]Option[Span]) -> bool
    func replace_first(view self, sc: edit[Scratch], text: view[]u8, template: view[]u8, dst: edit[Vec[u8]]) -> bool
    func replace_all(view self, sc: edit[Scratch], text: view[]u8, template: view[]u8, dst: edit[Vec[u8]]) -> usize
    func split(view self, sc: edit[Scratch], text: view[]u8, fields: edit[Vec[Span]]) -> usize
}
```

`Options`, `CompileErr` and `Span` are `Copy` and their fields are
readable and writable from any module. `Regex` and `Scratch` are move-only
and their fields are private to the module. A `Regex` is `Send` and
`Share`; a `Scratch` is `Send`.

## 2. The pattern language (normative)

A pattern is read left to right by this grammar, in which `|` binds
loosest, then concatenation, then repetition:

```
pattern  := branch ('|' branch)*
branch   := piece*
piece    := atom repeat*
repeat   := '*' | '+' | '?' | interval
interval := '{' n '}' | '{' n ',}' | '{' n ',' m '}'       n, m: 1 to 4 digits
atom     := '(' pattern ')' | '[' bracket ']' | '.' | '^' | '$'
          | '\' escaped | ordinary
```

An *ordinary* byte is any byte that does not begin another form of atom
at that place and is not one of `*`, `+`, `?`, `|`, `)`. A `{` that does
not begin an `interval` as written above is ordinary, and so is every
`}` and every `]` outside a bracket expression. An empty `branch` (as in
the empty pattern, `a|`, `()`) matches the empty string.

## 3. Requirements

### Compiling

**R1. `compile` and `compile_with`.** `compile(p)` is
`compile_with(p, options())`. `compile_with` returns `Ok` with a `Regex`
for every pattern the grammar of §2 derives that R5 does not refuse, and
`Err` otherwise. The pattern is not kept: the caller may change or
destroy it after the call. `options()` returns `ignore_case` false,
`newline` false and `max_insts` 100000.

**R2. `groups()`.** The number of groups in the pattern, group 0 not
counted: the number of `(` that open a group.

**R3. Size.** A pattern has a *size*, a whole number worked out from
its text alone: an ordinary byte, an escape, `.`, a bracket expression,
`^` and `$` are each 1; a group `(p)` is the size of `p` plus 2; a
branch is the sum of its pieces, 0 when empty; an alternation of *k*
branches is their sum plus 2 × (*k* − 1); `x*`, `x+` and `x?` are the
size of `x` plus 2; `x{n}` is *n* times the size of `x`; `x{n,}` is
*n* times it plus 2 when *n* is 1 or more and the size of `x` plus 2
when *n* is 0 (so `x{0,}` and `x{1,}` have the sizes of `x*` and
`x+`); and `x{n,m}` is *m* times it plus 2 × (*m* − *n*); and the pattern is the size of its alternation plus 1.
When the size exceeds `max_insts`, `compile_with` returns `TooLarge`
with `at` equal to the pattern's length; the comparison is made on the
whole number, which does not wrap however large nested intervals make
it. A `max_insts` of 0 refuses every pattern. Whatever `max_insts` is,
a pattern of 2^32 − 1 bytes or more, or of that size or more, is
`TooLarge`.

**R4. Nesting.** Groups nested more than 200 deep are refused: `TooDeep`,
with `at` the index of the `(` that is the 201st open at once.

**R5. Errors.** `at` is an index into the pattern, and the first error
met reading left to right is the one returned. When the pattern ends
with several groups or a bracket expression still open, the one opened
last is the one reported (`([` is `UnclosedBracket` at 1, `((` is
`UnclosedGroup` at 1). An error at a byte is met before the end of the
pattern is: a backslash that ends the pattern is `BadEscape` even
inside an open group or bracket expression (`[\` is `BadEscape` at 1).
`TooLarge` is reported only for a pattern with no other error:

| kind | when | `at` |
|---|---|---|
| `UnclosedGroup` | the pattern ends inside a group | the group's `(` |
| `UnmatchedClose` | a `)` with no group open | the `)` |
| `UnclosedBracket` | the pattern ends inside a bracket expression | its `[` |
| `BadRange` | a range whose first byte is greater than its last; a range whose second end is `[:`, whatever follows it (`[a-[:alpha:]]`, `[a-[:foo:]]`); a class R8 lists followed by a `-` and a byte other than `]` (`[[:alpha:]-z]`) | the range's first byte, or the `[` of the class that is its first end |
| `BadClass` | `[:` where a member begins that is not the start of `[:name:]` with a name R8 lists (an unknown name, or no closing `:]`), whatever follows it (`[[:foo:]-z]`) | the `[` of `[:` |
| `BadEscape` | a backslash that R7 gives no meaning and that is not `Unsupported`: at the end of the pattern; before an ASCII letter R7 does not list; before `0`; before a byte below 33 or above 126; or `\x` not followed by a hexadecimal digit | the backslash |
| `Unsupported` | a backslash before a digit `1`-`9`; `[.` or `[=` inside a bracket expression | the backslash, or the inner `[` |
| `BadInterval` | an `interval` with *n* > *m*, or with *n* or *m* above 1000 | its `{` |
| `NothingToRepeat` | a `repeat` with no atom before it in its branch; an `interval` there is `NothingToRepeat` whatever its counts (`{2,1}` at the start of a pattern) | the operator |
| `TooDeep`, `TooLarge` | R4, R3 | as stated there |

### What a pattern matches

**R6. Atoms and operators.** An ordinary byte matches itself. `.` matches
any one byte (with `newline`, any byte but 10). `(p)` matches what `p`
matches. `p|q` matches what either matches. A branch matches the
concatenation of what its pieces match. `x*` matches zero or more of `x`
in sequence, `x+` one or more, `x?` zero or one, `x{n}` exactly *n*,
`x{n,}` *n* or more, `x{n,m}` from *n* to *m*. Several `repeat`s apply in
turn, each to the result of the one before (`a+?` is `(a+)?`: there are
no lazy operators).

**R7. Escapes.** Outside and inside bracket expressions alike: a
backslash before punctuation is that byte; `\n`, `\t`, `\r`, `\f`, `\v`
and `\a` are the bytes 10, 9, 13, 12, 11 and 7; `\x` followed by one or
two hexadecimal digits (as many as there are, at most two) is the byte of
that value. Every other backslash is an error (R5). That a backslash
escapes inside a bracket expression departs from POSIX, where it is an
ordinary byte there, and agrees with awk and with the oracle.

**R8. Bracket expressions.** `[` … `]` matches one byte that is a member;
`[^` … `]` one byte that is not (with `newline`, never the byte 10). A
`]` directly after the `[` or the `[^` is a member, not the end. Members
are: a single byte (or escape); a range `a-b`, every byte from `a` to
`b` inclusive by value, `a` and `b` each a single byte or escape; and a
named class. A `-` directly after a single byte or escape and before a
byte other than `]` is the range operator, and that byte is the range's
last, even when it is itself a `-` (`[a--]` is the range from `a` to
`-`, which is `BadRange`). A `-` that is last and not a range's end is
a member. A `-` that is first, or directly after a range, is a member
too, and like any member byte it is
a range's first byte when a `-` and another byte follow it (`[--a]` is
the range from 45 to 97; `[a-c--e]` is `a` to `c` and `-` to `e`). A
`-` directly after a named class and not last makes a range with the
class as its first end, which is `BadRange` (R5). The named classes are those of the C locale: `[:alpha:]`
(ASCII letters), `[:digit:]`, `[:alnum:]`, `[:upper:]`, `[:lower:]`,
`[:space:]` (9 to 13 and 32), `[:blank:]` (9 and 32), `[:punct:]`
(punctuation as the glossary has it), `[:print:]` (32 to 126),
`[:graph:]` (33 to 126), `[:cntrl:]` (0 to 31 and 127), `[:xdigit:]`.

**R9. Anchors.** `^` matches the empty string at offset 0 of the text,
and with `newline` also directly after a byte 10. `$` matches the empty
string at offset `len(text)`, and with `newline` also directly before a
byte 10. They refer to the whole text whatever `from` is: `^a` has no
match from 1 in `aa`. An anchor is an atom anywhere in a pattern (`a^b`
is a pattern that matches nothing) and may be repeated.

**R10. `ignore_case`.** With `ignore_case`, an ordinary byte or escape
that is an ASCII letter matches both of its cases, and a bracket
expression's member set is closed under exchanging the cases of ASCII
letters before any `^` is applied. Bytes that are not ASCII letters are
unaffected.

### Searching

**R11. `find` and `is_match`.** `find(sc, text, from)` returns `Some` of
the match from `from` as a `Span`, `None` when there is none or when
`from > len(text)`. `is_match(sc, text)` is whether a match exists
anywhere in the text. Neither changes anything but `sc`, and a result
does not depend on what `sc` was used for before.

**R12. `captures`.** `captures(sc, text, from, groups)` returns whether
`find(sc, text, from)` would return `Some`. When it returns true, with
*k* = `min(len(groups), groups() + 1)`: when *k* > 0, `groups[0]` is
`Some` of the match; for 1 ≤ *i* < *k*, `groups[i]` is `Some` of group
*i*'s span by R13 or `None` when group *i* took no part; elements from
*k* on are set to `None`. A `groups` of length 0 is written nowhere.
When the call returns false, `groups` is unchanged.

**R13. Which spans the groups have.** The match (*s*, *e*) is fixed by
R11. Among the ways the pattern can match exactly `text[s:e]`, one is
chosen by trying, at each choice, in this order: for `p|q`, `p` before
`q`; for a repetition, one more iteration before stopping. The first
way, in the order a backtracking search making those choices would find
them, is the chosen one. Group *i*'s span is the stretch matched the
last time that way passed through the group, and `None` when it never
did. A span is kept when a later iteration of an enclosing repetition
does not pass through the group again: in `((a)|b)+` matching `ab`,
group 1 is (1, 2) and group 2 is still (0, 1), outside group 1. For
every pattern and text each `Some` span lies within the match. For a
pattern in the common syntax compiled with `newline` set and
`ignore_case` not set, and an ASCII text, the spans are exactly the
oracle's. For a repetition whose operand matches the empty
string, which passage counts as the last is not specified beyond the
span lying within the match.

**R14. `Cursor`, `next` and `next_captures`.** A `Cursor` from `cursor()`
enumerates the matches of one pattern in one text, left to right, without
overlap. It holds a position *pos*, 0 at first, and the end of the match
it last returned, none at first. `next` repeats: when *pos* >
`len(text)`, it returns `None`; it takes *m* = `find(sc, text, pos)`;
when *m* is `None` it sets *pos* to `len(text) + 1` and returns `None`;
when *m* is empty and its start equals the end last returned, it sets
*pos* to *m*'s start + 1 and repeats; otherwise it records *m*'s end,
sets *pos* to that end when *m* is not empty and to that end + 1 when it
is, and returns `Some(m)`. `next_captures` does the same, returns true
where `next` returns `Some`, and then leaves `groups` as
`captures(sc, text, s, groups)` leaves it for the returned match's
start *s*; where `next` returns `None` it returns false and `groups` is
unchanged, so the spans of a match that was passed over are never
seen. Using one cursor
with a different pattern or text between calls is not an error and its
results are then unspecified, within the text's bounds. (So `x*` in
`abc` yields the empty matches at 0, 1, 2 and 3, and `a*` in `baaac`
yields (0,0), (1,4) and (5,5).)

### Replacing and splitting

**R15. Templates and replacement.** In a template, `&` and `\0` stand for
the match, `\1` to `\9` for that group's span (nothing when the group is
`None` or its number is above `groups()`), `\&` for `&`, `\\` for a
backslash, a backslash before any other byte for that byte, and a
backslash that ends the template for itself; every other byte stands for
itself. `replace_all` appends to `dst`, for the matches a new cursor
yields in order: the text between the previous match's end (0 at first)
and the match's start, then the template's expansion; and after the last
match the rest of the text. It returns the number of matches. With no
match `dst` gains the whole text and the result is 0. `replace_first`
does the same for the first match only, appending the rest of the text
after it, and returns whether there was one. Neither reads `dst`'s
earlier contents or changes them.

**R16. `split`.** The *separators* are the non-empty matches a new cursor
yields. `split` appends to `fields`, as `Span`s into the text: the
stretch before the first separator, the stretch between each two
consecutive separators, and the stretch after the last; a text with no
separator is one field, the whole text; an empty text is no field. It
returns the number of fields appended. Empty fields are kept (a
separator at the very start gives an empty first field).

### Cost

**R17. No allocation in a search.** After `sc.reserve(re)`, or after any
earlier search with `sc` and the same `re`, the calls `is_match`, `find`,
`captures`, `next` and `next_captures` with that `sc` and `re` make no
allocation call, whatever the text. Under the same condition
`replace_first`, `replace_all` and `split` make none but those `dst` or
`fields` makes to grow; called with a `Scratch` that has served neither
`reserve` nor a search for that `re`, they grow it first as any first
search does. A `Scratch`
may be used with any number of patterns in turn; it then grows to the
largest need among them. `reserve` and a first search trap `oom` when
they cannot allocate.

**R18. Time and memory.** With *n* = `len(text)`, *m* the pattern's
size (R3) and *g* = `groups()`: `is_match` and `find`
do at most *c* × (*n* + 1) × *m* steps and `captures` at most
*c* × ((*n* + 1) × *m* × (*g* + 1) + `len(groups)`), for a constant
*c* that does not depend on the pattern or the text; a `Scratch` holds at most
*c* × *m* × (*g* + 1) machine words for the pattern, among those it has
been reserved for or searched with, for which that product is
greatest (it does not shrink): the tables for groups are sized by
`reserve` and by a first search of any kind, so that R17 holds for a
`captures` that follows a `find`. In particular no pattern takes time
exponential or quadratic in *n* for one call of `is_match`, `find` or
`captures`. A cursor step is one such call, so `replace_all`, `split`
and a cursor run to its end take at most that bound for each match
they pass: for a pattern that matches the empty string at most offsets
after looking far ahead (`(a*b)?` in a long run of `a`) that is
quadratic in *n* over the whole call.

**R19. Sharing.** Any number of threads may search with one `Regex` at
once, each with a `Scratch` of its own; their results are those of R11
to R16.

### Where it runs

**R20. Every configuration, and freestanding.** Every function of the
module gives the same results in the four configurations. The module
calls no runtime entry of its own, so a freestanding program may use it
wherever `Vec` is available to it: `kern check` of the fixture of T7 in
freestanding mode reports no `ENV_HOSTED_ONLY`.

### The examples

**R21. `examples/kawk` uses the module.** `lang/regex.kern` is removed
and every match, substitution and split in `kawk` goes through
`std/regex`. What the system awk does that this module refuses or reads
differently (among them: a bracket expression with no member matching the
empty string; `\x` with no digit as the byte 0; octal escapes; a `{` kept
literal where awk keeps it; `split`'s and `FS`'s own rules) stays in
`kawk`, as a translation of the awk pattern before it is compiled or as
code around the calls. `kawk.out` is unchanged. One limit of the
module's becomes `kawk`'s: groups that the rewriting cannot remove nest
200 deep at most (R4), where `kawk`'s own reader took 201 and refused
202; the system awk takes both, so `kawk` already differed from it
there, and no case of the corpus nests so deep.

**R22. The corpus.** The comparison programs written with `kawk` (548 at
commit 146f4df, and the seven added on 2026-11-12 for bracket and `\x`
edge cases) are kept under `examples/kawk/corpus/`, each with its input,
its arguments, and the standard output and exit status `/usr/bin/awk`
(version 20200816) gave, recorded once. A Go test builds `kawk` natively
at release and runs every case, comparing output and status byte for
byte with the record; it needs no awk on the host. Every case runs in
an empty working directory with the environment the records were made
under (`LC_ALL=C`, `TZ=UTC`, `KAWK_TEST=yes`). The cases that start a
command (a pipe or `system`) or read `ENVIRON` are marked: their
records hold what macOS's own commands print, so the test runs them on
macOS and skips them on every other system, saying how many it
skipped. A script kept beside the corpus regenerates the records on a
host that has that awk.

## 4. Errors and edges

- Except for the allocation failure of R17 (`oom`, in `reserve` or in
  a first search with a `Scratch`), `find`, `captures` and a cursor
  never trap for any text, `from` or `groups` length;
  `from > len(text)` is `None` (R11), not a trap.
- A `Regex` compiled with `max_insts` *k* has a size of at most *k*, so
  R18 bounds every later search by *k*.
- `replace_all` and `split` on a pattern that matches only the empty
  string: `replace_all` inserts the expansion before every byte and at
  the end (R14, R15); `split` finds no separator and gives one field
  for a text that is not empty and no field for an empty one (R16).
- A template is read at each call; no template is an error (R15).
- Byte 0 is an ordinary byte in patterns and texts.

## 5. Tests

**T1. Syntax and errors (`tests/run/regex_syntax.kern`).** For each row of
R5's table at least two patterns, with the expected kind and `at`; every
form of §2 accepted; the escapes of R7 inside and outside brackets; the
classes of R8 against all 256 bytes; `{` and `}` as ordinary bytes;
`max_insts` at the exact size of a pattern and one below it (R3); 200
and 201 nested groups (R4).

**T2. Matching (`tests/run/regex_match.kern`).** A table of at least 150
(pattern, options, text, from, expected span) rows covering R6 to R11:
each operator, the leftmost-longest choice where the first alternative
is shorter (`a|ab` in `ab`), anchors with and without `newline` and with
`from` above 0, `ignore_case` on literals, ranges and negated
expressions, bytes above 127 and the byte 0, an empty text, an empty
pattern.

**T3. Groups, cursors, replacement, split
(`tests/run/regex_groups.kern`).** `captures` with `groups` shorter,
equal and longer than `groups() + 1` (R12); groups that take no part;
nested and repeated groups; the two cursor examples of R14 and a cursor
run to its end twice; every template form of R15; `replace_first` with
and without a match; `split` with separators at the start, the end,
adjacent, and none, and on an empty text (R16).

**T4. Against the oracle (a Go test in `tests/`).** From a fixed seed, at
least 20,000 (pattern, text) pairs in the common syntax: patterns of up
to 12 atoms over the bytes `a`, `b`, `c` with groups, alternation,
bracket expressions (sets and ranges over `a`, `b`, `c`, their
negations, and `[[:alpha:]]`), `.`, anchors, and `*`, `+`, `?` and
intervals with counts from 0 to 3; texts of 0 to 24 bytes over `a`,
`b`, `c` and the byte 10. For each pair a kern program compiles the
pattern with `newline` set and prints every match `next_captures`
yields with all its group spans, and what `replace_all` with the
template `<&|\1>` gives; the test compares the matches and spans with
the oracle's `FindAllSubmatchIndex(text, -1)`, and the replacement with
one built from the oracle's matches by R15. For the pairs whose text
holds no byte 10, the same comparison is made again with the pattern
compiled by `compile` (the two readings agree on such a text). The
pairs are run natively at release, and one in ten in the interpreter.
No difference is permitted.

**T5. Cost (`tests/run/regex_cost.kern`,
`tests/run/regex_noalloc.kern`).** `(a*)*b`, `(a|aa)*c` and
`a*a*a*a*a*b` each against 200,000 bytes of `a`, by `find` and by
`captures`, finish within the harness's time limit in every
configuration (R18). `regex_noalloc` (header `//! test-hooks: true`)
checks R17 as it runs, in the configurations that have test hooks (the
interpreter and the native debug build: the toolchain refuses hooks at
release), for three patterns and one `Scratch` reserved
for each in turn: it arms `testing.fail_next_alloc()`, which makes the
next allocation call of the program fail; runs `is_match`, `find`,
`captures`, a cursor to its end with `next` and with `next_captures`,
and `replace_all` and `split` into vectors given room beforehand; and
then calls `Vec[u8].try_push` on a new vector,
which must return `Err(OutOfMemory)`: the failure was still armed, so
none of the searches made an allocation call. The same is done after a
first search in place of `reserve`. (A `budget { allocations 0 }` block
is not the check: it counts the allocation sites of the function it is
written on, not those of what the function calls.)

**T6. Sharing (`tests/concurrency/regex_shared.kern`).** Four threads
search with one `Regex` over different texts, 1,000 searches each, and
each thread's results equal those one thread gets (R19).

**T7. Freestanding (`tests/freestanding/regex.kern`).** Compile, `find`,
`captures` and `replace_all` on three patterns, the same output as the
hosted run (R20).

**T8. `kawk`.** The fixture `examples/kawk` passes unchanged (R21), and
the corpus test of R22 passes with every case agreeing.

**T9. Measured once, by instructions retired, recorded in the report.**
The input is the 200,000 lines this awk program prints, given no input
of its own:

```
BEGIN { for (i = 1; i <= 200000; i++) print "rec" i, i % 1000, (i * 7919) % 10007, "alpha beta gamma delta", "w" (i % 4999) }
```

and the three programs are `{ s += $3 } END { print s }`,
`{ for (i = 1; i <= NF; i++) c[$i]++ } END { n = 0; for (k in c) n++; print n }`
and `{ n += gsub(/a[a-z]/, "X") } END { print n }`. For each, `kawk` on
the module, built natively at release, retires at most 1.10 times the
instructions that `kawk` built the same way from commit 146f4df retires
on the same input, both measured in the same session with
`/usr/bin/time -l`, the least of three runs each.
