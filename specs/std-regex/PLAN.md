# Plan: regular expressions (`std/regex`)

**Review status.** plancritic (gpt-6.1-sol, profile `davin-go`, with
the spec for coverage) round 1: EXECUTABLE_WITH_CLARIFICATIONS, no
critical finding, four warnings and twelve partly covered requirements,
answered in place: where the corpus comes from and that it is preserved
(phase 1); `record.py` refuses to run off macOS; R17 is checked with
the allocation-failure hook after a probe showed a `budget` block
counts only its own function's sites (phase 3, and the spec's T5
changed with it); the second pass of `captures` reads the whole text
(phase 4); and the edge cases each requirement names are listed in the
acceptance of the phase that owns it. Round 2: no critical finding,
three warnings answered: a preflight for phase 1, a time limit and a
process group for every corpus case, and each search shortcut's
condition stated on the parsed tree with its regression rows (phase
3); the fallbacks that would have departed from R13, R21 and R22 are
replaced by "the phase is not done until the owner changes the spec".
Round 3: EXECUTABLE_AS_IS, score 100, every requirement covered.

Implements `specs/std-regex/SPEC.md` v0.15 in seven phases. The order is
chosen so that each phase is checked by something already in the
repository: the `kawk` corpus goes in first (phase 1) and guards the
port in phase 6; the module is built from its parser outward (phases 2
to 5), each phase with its fixtures; the documents and the measurements
come last (phase 7).

Conventions that hold in every phase:
- `std/regex.kern` and `compiler/resolve/prelude/regex.kern` are
  byte-identical (edit `std/`, copy to the prelude;
  `TestStdCoreMatchesPrelude` compares them).
- A fixture's expected output is written from the spec before the
  fixture is run. `go test ./tests -run 'TestFixtures$/DIR/NAME'
  -count=1` runs a fixture in three of the four configurations (the
  interpreter, native debug, native release); the fourth, the
  interpreter at release, is run by hand for every fixture of this
  plan (`kern run --opt=release FILE`) and compared with the same
  expected output.
- After a fixture is added or removed:
  `go test ./compiler/analysis -run TestHostedOnlyInventory -update-hosted-inventory`.
- `gofmt -l` and `golangci-lint run ./...` are clean before a phase
  ends; a phase ends with `go test ./... -count=1 -timeout 60m` passing
  and a `prism review staged` whose findings are read and answered.
- One commit per phase, made when the owner asks; nothing is pushed
  unless the owner asks.
- The module has no `unsafe` block, calls no runtime entry and uses no
  intrinsic that `std/str` does not already use (spec, Scope).

## Phase 1: the `kawk` corpus in the repository (R22, T8 in part)

**Goal.** The 555 comparison cases and the test that runs them, with
`kawk` itself untouched, so that every later phase that touches `kawk`
is checked without the system awk.

**Files.**
- `examples/kawk/corpus/cases/NNN-name.awk` (555: the 548 of the
  session's scratch directory and seven new ones, numbered on from the
  last, for an empty bracket expression as a pattern, in `match`, in
  `gsub`, in `split` and as `FS`, and for `\x` with no digit outside
  and inside a bracket expression), with `NNN-name.in` and
  `NNN-name.args` where a case has them, `input.txt` (the shared input)
  and `prog2.awk` (the second program file two cases name).
- `examples/kawk/corpus/cases/NNN-name.out`: the standard output
  `/usr/bin/awk` gave, byte for byte.
- `examples/kawk/corpus/status.txt`: one line per case, `name status
  marks`, where `status` is the exit status awk gave and `marks` is
  `host` for a marked case (R22) and `-` otherwise.
- `examples/kawk/corpus/record.py`: runs every case through
  `/usr/bin/awk` exactly as the test runs `kawk` and rewrites the `.out`
  files and `status.txt`; it refuses to run when `awk -version` is not
  `awk version 20200816` or the system is not macOS (`uname -s` is not
  `Darwin`), since the marked cases' records are that system's
  commands' output. Both refusals are tried once by hand with a
  substituted `awk` path and recorded in the report.
- `examples/kawk/corpus/README.md`: what the directory is, the form of
  `.args` (options before a line `--`, operands after; `@IN` is the
  case's input file, `@PROG2` is `prog2.awk`), how to add a case and
  regenerate.
- `tests/kawk_corpus_test.go`: `TestKawkCorpus`.

**Where the cases are.** The 548 cases, their inputs and arguments,
`input.txt`, `prog2.awk`, `diff.py` and the generators and fuzzers that
wrote them were made in a session scratch directory. They are
preserved in `specs/std-regex/kawk-corpus-source.tar.gz` (made
2026-11-12; SHA-256
`3ff864bce54bdf4fa0fe4d7571ab27e3b0df3b7950499c89b874ea47c113f382`;
`cases/` holds 548 `.awk`, 90 `.in` and 28 `.args` files), which is the
source for step 1. The archive is committed with the spec and the plan
and removed by phase 1's commit, once its contents are in
`examples/kawk/corpus/`.

**Preflight, before step 1.** The archive's checksum is the one above
and its three counts are 548, 90 and 28; `/usr/bin/awk -version` prints
`awk version 20200816` and `uname -s` prints `Darwin` on the host that
records. If any of these fails the phase stops and the owner is told:
the plan has no substitute for the cases or for that awk, and using
either would be a change to R22 that only the owner can make.

**Steps.**
1. Unpack the cases from the archive, write the seven new ones, and
   write `record.py` from the archive's `diff.py` (same environment: `LC_ALL=C`, `TZ=UTC`,
   `KAWK_TEST=yes`, a fresh empty working directory per case, the
   case's `.in` or `input.txt` on standard input). Each case has 20 s;
   `record.py` starts it in a process group of its own and, at the
   limit, kills the group, so that a command the case started does not
   outlive it, and stops with the case's name: a case that awk does not
   finish is not recorded.
2. Mark a case `host` when its program text contains `system(`, a `|`
   outside a regular expression and a string that is a pipe (`print …
   | "cmd"`, `"cmd" | getline`), or `ENVIRON`. The list is made by a
   pattern search and then read through by hand, case by case, since a
   wrong mark only costs coverage off macOS and a missing one fails the
   Linux run of step 5.
3. Record twice, in two runs at least ten seconds apart. A case whose
   two records differ depends on the time or the process: it is
   rewritten so that it does not (the scratch corpus was written to
   avoid this; none is expected) and recorded again.
4. `TestKawkCorpus` builds `examples/kawk/kawk.kern` natively at
   release once (as `tests/program_diff_test.go` builds a program, into
   `t.TempDir()`), then runs the cases through a pool of `testJobs(t)`
   workers (`tests/build_ahead_test.go`), each in its own empty
   directory under `t.TempDir()` with that environment, and reports
   every case whose output or status differs from the record (the first
   differing line, as `diff.py` prints it). A case has 20 s
   (`exec.CommandContext`); it runs in a process group of its own
   (`SysProcAttr.Setpgid`) and at the limit the test kills the group and
   fails with the case's name and "timed out", then goes on to the
   other cases. On a system other than
   macOS (`runtime.GOOS != "darwin"`) it skips the `host` cases and
   logs their number. Without clang it skips, as the other native
   tests do.
5. Run the test on macOS, and on Linux through
   `scripts/linux-check.sh` (both architectures it covers).

**Acceptance.** `go test ./tests -run TestKawkCorpus -count=1` passes on
macOS with 555 cases run, and on Linux with the unmarked cases run and
the marked ones counted as skipped. `examples/kawk`'s fixture still
passes (the harness reads only `.kern` files of a project directory, so
`corpus/` is invisible to it; `kern build examples/kawk/kawk.kern`
ignores a directory with no `.kern` file).

**Risks.**
- An unmarked case fails on Linux though it starts no command and
  reads no `ENVIRON`: `kawk` then behaves differently between systems,
  which is a defect to fix in `kawk`. R22 marks only the command and
  `ENVIRON` cases, so such a case is not marked to make it pass;
  marking it would be a change to R22 for the owner to make.
- 1,200 small files: about 220 KB of text. If the owner prefers fewer
  files, the `.out` records can be one archive; the plan keeps them
  apart so a difference shows in `git diff`.

## Phase 2: the pattern reader (R1 to R10 as far as `compile` shows them, T1)

**Goal.** `compile`, `compile_with`, `options`, `groups`, the error
table and the size rule: everything that can be observed without a
search.

**Files.**
- `std/regex.kern` (and the prelude copy): the declarations of spec §1
  for `Options`, `options`, `ErrKind`, `CompileErr`, `Span`, `Regex`,
  `compile`, `compile_with`, `Regex.groups`.
- `compiler/resolve/decls.go`: `"regex"` appended to `StdModules`
  (`TestStdModulesCheck`, the prelude comparison and the incremental
  cache iterate that list; nothing else enumerates the modules in Go).
- `spec/types.md` §1 and `README.md` (the library row): `std/regex`
  added to the lists of import names and files.
- `tests/run/regex_syntax.kern` + `.out` (T1).

**Design.**
- The reader is `kawk`'s `RParser` and `RCompiler`
  (`examples/kawk/lang/regex.kern` at 146f4df), copied and changed:
  - every place it sets `bad` returns a `CompileErr` with the kind and
    index of R5 instead; the reader keeps the index of each open `(`
    and `[` for `UnclosedGroup`, `UnclosedBracket` and `TooDeep`;
  - awk's readings are removed: a bracket expression with no member, a
    bare `\x` as the byte 0, octal escapes, a reversed range dropped, a
    malformed interval kept literal beyond what §2 says, a class cut at
    a zero byte. Each becomes the error or the meaning the spec gives;
  - escapes are R7's, inside and outside brackets; the twelve class
    names are R8's;
  - a group records its number (`RNode.Group(inner, k)`), and the
    program gains `Inst.Save(slot)` before and after a group's code
    (slot 2k and 2k + 1), which the search of phase 3 passes over as an
    empty step;
  - `ignore_case` is applied while reading: an ASCII letter becomes a
    two-member set, and a bracket expression's set is closed under case
    before negation (R10); `newline` clears byte 10 from `.` and from
    negated sets, and selects line anchors (`Inst.Bol`/`Eol` carry a
    flag).
- The size of R3 is computed on the tree before any code is made, in a
  `u64` that saturates (`checked_mul`, `checked_add`; a product past
  `u64` is "above every `max_insts`"), and compared with `max_insts`;
  only then is the program emitted, so no large program is ever built.
  The emitted program's length is not the spec's size (it is at most a
  small multiple of it, which R18's constant covers).
- `{n,m}` is emitted as *n* copies followed by *m* − *n* nested
  optional copies, each preferring to continue (the order R13 needs).
- Errors are found in the order R5 fixes: the reader stops at the first
  one it meets left to right; the end of the pattern is examined only
  after its last byte (so `[\` is `BadEscape`); the size check runs
  last.

**Acceptance.** `regex_syntax` passes in the four configurations with
what `compile` alone shows: two patterns or more per row of R5's table,
each with its kind and `at`; every form of §2 accepted, with its
`groups()`; R7's escapes accepted in both places and each refused
escape refused; `max_insts` at a pattern's exact size and one below,
for five patterns whose size the fixture states from R3's rule; 200 and
201 nested groups; a pattern compiled from a `Vec[u8]` that is then
overwritten and destroyed, the `Regex` still reporting its `groups()`
(R1: the pattern is not kept; its matching is checked in phase 3); and
`Options`, `CompileErr` and `Span` copied by assignment with a field of
the copy changed (they are `Copy` with public fields, while `Regex`
and, in phase 3, `Scratch` are declared `nocopy`, which makes their
fields private to the module by the library's rule). The part of T1
that needs a search (R8's classes
against all 256 bytes, `{` and `}` matching themselves) is added to the
same fixture in phase 3, its expected output written from the spec
before it is run.

**Risks.** The size rule and the emitted program disagreeing in a way
that makes R18's constant large: the emitter is written so that each
unit of size emits at most 4 instructions, and the fixture prints
nothing about instructions, so this is checked by reading and by T5.

## Phase 3: searching (R11, R17 to R20; T2, T5, T6, T7)

**Files.**
- `std/regex.kern` (and the prelude copy): `Scratch`, `Scratch.new`,
  `Scratch.reserve`, `Regex.is_match`, `Regex.find`.
- `tests/run/regex_match.kern` + `.out` (T2), `tests/run/regex_cost.kern`
  + `.out` and `tests/run/regex_noalloc.kern` + `.out` (T5),
  `tests/concurrency/regex_shared.kern` + `.out` (T6,
  header `//! tsan: clean`), `tests/freestanding/regex.kern` + `.out`
  (T7, header `//! env: freestanding`), and the matching half of
  `regex_syntax`.

**Design.**
- `find` is `kawk`'s `re_search`: one pass over the text, a list of
  live states in order, each with the offset its thread began at, a
  generation stamp per instruction so that a state is entered once per
  position, the best match so far kept as (least start, then greatest
  end), no new thread begun once a match is known. Its three shortcuts
  are kept, since T9 depends on them, each decided from the parsed tree
  and never from the pattern's first bytes:
  - *literal*: the tree is a concatenation of ordinary bytes and
    nothing else (no group, so `groups()` is 0; not under
    `ignore_case` when a letter is present): the search is `str.find`
    from `from`;
  - *anchored*: every way through the tree begins with `^` (for an
    alternation, every branch does; for a concatenation, its first
    piece does and that piece is not optional), and `newline` is not
    set: threads begin at offset 0 only, so a `from` above 0 finds
    nothing. `^a|b` is not anchored;
  - *first bytes*: the set of bytes a non-empty match can begin with
    is computed from the tree; a start is skipped when the byte there
    is not in the set, and only when the tree cannot match the empty
    string (`a*` can, so no start is skipped for it).
- Differences from `kawk`'s: anchors always refer to the whole text
  (there is no "not at the beginning" mode: the callers that used it
  pass the whole text and a `from`); there is no "non-empty" mode (the
  cursor of phase 4 and `kawk`'s own code supply it); `Save`
  instructions are passed over.
- `is_match` is the same pass that returns at the first state reaching
  the end of the program.
- `reserve(re)` sizes every vector of the `Scratch` for `re` (the two
  state lists, the stamps, the work stack, and in phase 4 the span
  tables), so that the search itself only indexes them. The search
  calls `reserve` first; when nothing needs to grow that is a few
  comparisons and no allocation call (R17).

**Acceptance.**
- `regex_match`: the table of T2 (150 rows or more), expected spans
  written by hand from R6 to R11. Among the rows: the shortcuts'
  edges, each with `from` 0 and above 0 (`^a|b` on `xb`; `a*` on `b`;
  `^a` on `ba`; `(^a|^b)c` on `xbc` and on `bc`; `x^a`; a literal with
  and without `ignore_case`; `^$` on an empty text); `from` equal to
  `len(text)` (an empty match is possible there) and `from` one and
  many above it (`None`, no trap); a text and a pattern holding the
  byte 0; the pattern compiled from a vector that was then overwritten
  (R1); and every row searched twice, once with a new `Scratch` and
  once with one `Scratch` shared by all rows in order, the two giving
  the same spans (R11: a result does not depend on what the `Scratch`
  served before).
- `regex_cost`: the three patterns of T5 against 200,000 bytes finish
  in every configuration within the fixture harness's limit, by `find`
  (and by `captures` from phase 4).
- `regex_noalloc` (`//! test-hooks: true`), as T5 states it: for each of
  three patterns, `reserve`, then `testing.fail_next_alloc()`, then
  `is_match` and `find` over several texts, then a `try_push` on a new
  `Vec[u8]` that must return `Err(OutOfMemory)`; and the same with a
  first search in place of `reserve`. `captures` and the cursor calls
  are added to it in phase 4.
- `regex_shared`: four threads, one `Regex` behind a `Shared`, each
  thread with a `Scratch` moved into it, 1,000 searches each, results
  equal to one thread's. That it compiles is the check that a `Regex`
  is `Send` and `Share` and a `Scratch` `Send` (spec §1).
- `tests/freestanding/regex.kern` passes with `compile` and `find` on
  three patterns, its output equal to a hosted run of the same file
  (the harness compares both), and `kern check` of it in freestanding
  mode (the invocation `tests/freestanding/README.md` gives) reports no
  `ENV_HOSTED_ONLY` (R20). `captures` is added to it in phase 4 and
  `replace_all` in phase 5, which completes T7.

**Risks.**
- The interpreter's speed on `regex_cost`: 200,000 bytes × a program of
  about ten instructions is a few million steps; if the interpreter at
  debug needs more than the harness allows, the text is not shortened
  (the spec fixes it): the search's inner loop is made cheaper, or the
  problem is reported to the owner as a spec question.
- `testing.fail_next_alloc` arms the runtime's allocation entries and
  the interpreter's alike (`tests/run/string_try_oom.kern` relies on
  it in the four configurations). If some allocation path of `Vec` does
  not consult it, `regex_noalloc` would pass wrongly: the fixture
  therefore begins with a control, the same arming followed by a
  `Vec.push` that grows, which must trap `oom`. The control lives in
  its own fixture, `tests/trap/regex_noalloc_control.kern`, since a
  trap ends the program.

## Phase 4: groups and cursors (R12 to R14; T3 first half, T4 matches)

**Files.**
- `std/regex.kern` (and the prelude copy): `Regex.captures`, `Cursor`,
  `cursor`, `Regex.next`, `Regex.next_captures`.
- `tests/run/regex_groups.kern` + `.out` (T3: the `captures` and cursor
  cases).
- `tests/regex_oracle_test.go`: `TestRegexOracle` (T4), without its
  replacement half.

**Design.**
- `captures` runs `find` first, which fixes (*s*, *e*) and costs what
  it costs today. When there is a match and `groups` is longer than 1,
  a second pass runs from *s* and stops at *e*. It reads the original
  text with absolute offsets and takes no byte at or past *e*; `^` and
  `$` are decided by the whole text exactly as in `find` (offset 0,
  `len(text)`, and with `newline` the neighbouring bytes, including
  the byte at *e* and the byte before *s*), never by the ends of the
  stretch being replayed. It is the same
  simulation with the state lists kept in priority order (a `Split`
  adds its first branch before its second, and a state already on the
  list is not added again), each state carrying 2 × (`groups()` + 1)
  offsets copied when a thread advances, `Save` writing the current
  offset into its slot. No thread is cut when another matches. The
  spans are those of the first state, in list order, that reaches the
  end of the program at offset *e*. That is the rule of R13: list order
  is the order a backtracking search would try, restricted to threads
  that differ in state, and a thread dropped as a duplicate could only
  have continued as the one kept.
- A group's slots are not cleared when an enclosing repetition goes
  round again, which is what keeps a span from an earlier passage
  (`((a)|b)+` on `ab`).
- The second pass costs (*e* − *s* + 1) × program × (`groups()` + 1),
  within R18; its tables are sized by `reserve`.
- `Cursor` is `{ pos: usize; last: usize; have_last: bool }`; `next`
  and `next_captures` are R14's loop written out, the second calling
  `captures` only for the match it is about to return (so a passed-over
  match never writes `groups`).

**The oracle test.** `TestRegexOracle` generates, from seed 1, pairs in
the common syntax as T4 lists them (a recursive generator with a depth
limit that never applies a repetition to an operand able to match the
empty string: it tracks "can be empty" for each node it builds, and
never to a repetition's result). The texts and patterns are written,
hex-encoded and one pair per line, to the standard input of one kern
program held in the test as a string: it reads a line, compiles the
pattern with `newline` set (and, when the line's flag says the text has
no byte 10, also with `compile`), runs a cursor to its end with
`next_captures`, and prints each match's spans. The Go side prints the
same lines from `regexp.CompilePOSIX(p).FindAllSubmatchIndex(text, -1)`
and compares. The program is built natively at release once and fed
all pairs; every tenth pair is also run through the interpreter in
process, as `tests/json_diff_test.go` runs its programs. A pattern the
oracle refuses to compile is a generator defect and fails the test.
`KERN_REGEX_PAIRS` overrides the count (default 20,000).

**Acceptance.** `regex_groups` passes in the four configurations, with
these among its cases: `groups` of length 0 (nothing written, the
result still true), 1, `groups()`, `groups() + 1` and longer (the
surplus elements set to `None` from values that were `Some`); a call
that returns false leaving every element as it was; a match in the
middle of a text for `^`- and `$`-anchored groups with and without
`newline` (`(^b)` after a byte 10, `(a$)` before one, and the same
patterns where the anchor does not hold at the replay's ends), so that
the second pass is seen to use the whole text; `((a)|b)+` on `ab`; a
`from` above `len(text)`; `next_captures` returning false with `groups`
unchanged after a passed-over empty match (`a*` on `a`). `regex_cost`,
`regex_noalloc` and the freestanding fixture are extended as phase 3
says. `TestRegexOracle` reports no difference in 20,000 pairs, and none in a
second run of 200,000 pairs made once by hand
(`KERN_REGEX_PAIRS=200000`), whose result is recorded in the report.

**Risks.**
- A difference from the oracle that is the oracle's own simplification
  of a pattern (Go rewrites some repetitions before compiling) rather
  than R13's rule. Each such difference is reduced to the smallest
  pattern and compared with R13 by hand: if the module's answer is
  R13's and the oracle's is not, the pattern's shape is excluded from
  the generator and the case is written into `regex_groups` with the
  module's answer and a comment; if the module's answer is not R13's,
  it is a defect. A shape may be left out of the generator only when
  the spec's definition of the common syntax already excludes it. If a
  difference lies inside the common syntax and the module's answer is
  R13's, the spec is wrong (R13 claims agreement there): the phase is
  not done, and T4 is not passed, until the owner has changed the
  spec's definition or the rule.
- Reading 20,000 lines from standard input in the interpreter:
  `io.BufReader` over `io.stdin()`; one in ten pairs keeps it short.

## Phase 5: replacement and splitting (R15, R16; T3 second half, T4 replacement)

**Files.**
- `std/regex.kern` (and the prelude copy): `Regex.replace_first`,
  `Regex.replace_all`, `Regex.split`.
- `tests/run/regex_groups.kern` + `.out`: the template and `split`
  cases of T3 added.
- `tests/regex_oracle_test.go`: the kern program also prints
  `replace_all` with the template `<&|\1>`, and the Go side builds the
  same from the oracle's matches by R15.

**Design.** Both are a cursor loop. `replace_all` asks for groups only
when the template names one (a scan of the template for a backslash
before a digit `1`-`9`, done once per call), so a template of `&` and
plain bytes costs `find` and not `captures`. `split` uses `next` and
passes over empty matches. Neither allocates but through `dst.extend`
and `fields.push`.

**Acceptance.** `regex_groups` and `TestRegexOracle` pass as extended.
The added cases include every template form of R15 (`&`, `\0` to
`\9`, a group that took no part, a number above `groups()`, `\&`,
`\\`, a backslash before another byte, a backslash ending the
template); `replace_first` with and without a match; `replace_all` and
`split` with a pattern that matches only the empty string, on a text
and on an empty text (§4); a text and a template holding the byte 0;
`split` with separators at the start, at the end, adjacent, and none;
and `dst` and `fields` that already hold elements, which stay. The
freestanding fixture gains `replace_all` (T7 complete).

## Phase 6: `kawk` on the module (R21; T8, T9)

**Files.**
- `examples/kawk/lang/regex.kern`: removed.
- `examples/kawk/lang/awkre.kern` (new): what is awk's and not the
  module's.
- `examples/kawk/lang/{lex,interp,builtin,io}.kern`: the call sites
  (`regex_compile`, `re_search`, `split_text`, the `Scratch`).
- `examples/kawk/kawk.kern`: the header comment's paragraph on
  `regex.kern`.

**Design.**
- `awk_compile(text) -> Result[regex.Regex, regex.CompileErr]` rewrites
  an awk pattern into one the module reads the same way, byte by byte
  into a new pattern, then calls `regex.compile`:
  - an octal escape and a bare `\x` become `\xHH` (a bare `\x` the byte
    0; inside a bracket expression a zero byte from an escape ends the
    members, so the rest up to the `]` is left out);
  - a bracket expression that ends up with no member (ranges written
    backwards are dropped first) becomes `()`, which matches the empty
    string, and when negated becomes one that matches any byte;
  - a backslash before a byte the module refuses (an ASCII letter with
    no meaning, a byte outside 33 to 126) becomes that byte, escaped
    when it is punctuation;
  - an interval the system awk keeps literal where §2 reads a count, and
    the reverse, follow what the corpus cases for intervals record (the
    cases exist; the rule is read from `regex.kern`'s `interval`).
  What the rewriting cannot express is listed in `awkre.kern`'s header
  and in the report.
- The three search modes: "any match" is `is_match`; "not at the
  beginning" disappears, since every caller already holds the whole
  text and passes `from`; "non-empty" is a helper in `awkre.kern` that
  calls `find` and, on an empty match at *p*, calls it again from
  *p* + 1.
- `split_text` and the record separator keep their own loops over that
  helper (awk's field rules are not `regex.split`'s: a single space, a
  single character, paragraph mode).
- A pattern that does not compile is the same fatal error as today.

**Acceptance.**
- `examples/kawk`'s fixture passes with `kawk.out` unchanged.
- `TestKawkCorpus` passes with every case agreeing (555 on macOS).
- T9, measured once by hand as the spec states it: the generator's
  200,000 lines, the three programs, `kawk` at 146f4df and `kawk` on
  the module both built natively at release in the same session, the
  least of three runs each; each ratio is at most 1.10. The six
  numbers go in the report.

**Risks.**
- A corpus case that depends on a reading of awk's the rewriting does
  not reproduce. The case is fixed in `awkre.kern` if a rewriting
  exists. Otherwise the phase is not done: R21 and R22 allow neither a
  second matcher in `kawk`, nor a failing case, nor a changed record,
  and the owner is asked to change one of the two requirements before
  anything of that kind is written.
- T9 above 1.10 on the `gsub` program: the module's `find` has the same
  shortcuts as `kawk`'s (phase 3), so the likely cause is the extra
  `Save` steps; they are then dropped from the program `find` runs (two
  programs are emitted, one with `Save`, one without) before the phase
  is called done.

## Phase 7: documents, tables, the report

**Files.**
- `docs/runtime.md`: a subsection for `std/regex` in the library
  reference (§8's neighbour for pure-kern modules): the pattern
  language in brief with a pointer to the spec for the grammar, each
  function with its cost, the `Scratch` and the no-allocation rule,
  what it is not (bytes only, R13's rule, no backreferences).
- `README.md`: the library row (done in phase 2) and one sentence in
  the examples row that `kawk` matches with `std/regex`.
- `CHANGELOG.md` (Unreleased, Library and Examples): the module, the
  corpus, `kawk`'s port.
- `docs/reports/std-regex.md`: what was built, phase by phase; the
  numbers of T9 and of the 200,000-pair oracle run; the corpus cases
  marked `host`, with the count; what `awkre.kern` rewrites and what it
  cannot; every difference from the oracle found and how it was
  settled; what is left (the spec's non-goals, and `ksearch`).
- `docs/reports/freestanding-availability.md`: regenerated.

**Acceptance.** `go test ./... -count=1 -timeout 60m` passes on macOS;
`scripts/linux-check.sh` passes; `go test ./tests -run TestCitations`
passes; `golangci-lint run ./...` reports nothing.

## Requirement coverage

| requirement | phase |
|---|---|
| R1, R2, R3, R4, R5 | 2 |
| R6, R7, R8, R9, R10 | 2 (reading), 3 (matching) |
| R11, R17, R18, R19, R20 | 3 (R17 and R18 again for `captures` in 4) |
| R12, R13, R14 | 4 |
| R15, R16 | 5 |
| R21 | 6 |
| R22 | 1 |
| T1 | 2, finished in 3 |
| T2, T5, T6, T7 | 3 (T5's `captures` half in 4) |
| T3 | 4 and 5 |
| T4 | 4 and 5 |
| T8 | 1 and 6 |
| T9 | 6 |
