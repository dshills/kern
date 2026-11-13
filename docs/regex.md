# Regular expressions (`std/regex`)

`std/regex` matches POSIX extended regular expressions against bytes. It
is kern code with no `unsafe` block and no runtime entry, so it runs the
same in the interpreter, in a native build and on a freestanding target.
The contract is `specs/std-regex/SPEC.md`; this page is the short form.

```kern
import "std/regex"

func main() -> i32 {
    match regex.compile("([a-z]+)=([0-9]+)") {
        Ok(re) => {
            sc := regex.Scratch.new()
            var groups: [3]Option[regex.Span] = [None, None, None]
            if re.captures(sc, "x, width=80;", 0usize, groups) {
                // groups[0] is (3, 11); groups[1] is (3, 8); groups[2] is (9, 11)
            }
        }
        Err(e) => { return 1i32 }     // e.kind and e.at say what and where
    }
    return 0i32
}
```

## What it guarantees

- **The leftmost-longest match.** Of the matches that begin earliest, the
  longest: `a|ab` in `ab` matches `ab`. This is POSIX's rule and awk's.
- **Time proportional to the text.** A search takes at most (text length)
  × (pattern size) steps whatever the pattern: `(a*)*b` against a long run
  of `a` is not slow.
- **No allocation in a search.** A `Scratch` holds the working memory. Once
  it has been reserved for a pattern (`sc.reserve(re)`, or any earlier
  search with that pattern), `is_match`, `find`, `captures` and the cursor
  calls make no allocation call. One `Scratch` serves any number of
  patterns in turn.
- **A `Regex` is shared freely.** It does not change after `compile`; any
  number of threads may search with one, each with a `Scratch` of its own.
- **No traps.** No text, offset or slice length makes a search trap; the
  one exception is running out of memory while a `Scratch` grows.

## What it is not

- It matches **bytes**. `.` is one byte, the named classes are ASCII's,
  and `ignore_case` folds ASCII letters only. UTF-8 text can be searched
  for UTF-8 literals; a class does not match a multi-byte character.
- **Groups do not follow POSIX's subexpression rule.** The whole match is
  leftmost-longest; among the ways of matching exactly that stretch, the
  groups are those of the first a backtracking search would find
  (alternatives left to right, repetitions as many as possible first).
  `(a|ab)(c|bcd)` on `abcd` gives `a` and `bcd`. This is what Go's
  `regexp.CompilePOSIX` does.
- No backreferences, no lazy operators, no look-around, no `\d` `\w` `\s`
  `\b`: `compile` refuses them, so a pattern never means something else
  than it says.

## The pattern language

| form | matches |
|---|---|
| any other byte | itself |
| `.` | any one byte (with `newline`, any but a line feed) |
| `[abc]`, `[a-z]`, `[^...]` | one byte in the set, or not in it |
| `[[:alpha:]]` | a named class inside a bracket expression: `alpha`, `digit`, `alnum`, `upper`, `lower`, `space`, `blank`, `punct`, `print`, `graph`, `cntrl`, `xdigit` |
| `^`, `$` | the start and the end of the text (with `newline`, of a line too) |
| `(p)` | what `p` matches, as a group |
| `p\|q` | either |
| `x*`, `x+`, `x?` | zero or more, one or more, zero or one |
| `x{n}`, `x{n,}`, `x{n,m}` | a count, each at most 1000 |
| `\.` and the like | a backslash before punctuation is that byte |
| `\n \t \r \f \v \a`, `\xHH` | those control bytes; the byte of that value |

A `{` that does not begin a count is an ordinary byte, as are `}` and a
`]` outside brackets. A `]` first in a bracket expression is a member; so
is a `-` first or last. A backslash escapes inside brackets too (POSIX
says otherwise; awk and Go agree with this). An empty alternative matches
the empty string. Several repetition operators apply in turn: `a+?` is
`(a+)?`.

`compile` returns a `CompileErr { kind, at }` for anything else, `at`
being the index of the offending byte:

| kind | for |
|---|---|
| `UnclosedGroup`, `UnmatchedClose` | parentheses |
| `UnclosedBracket`, `BadRange`, `BadClass` | bracket expressions |
| `BadEscape` | a backslash with no meaning here |
| `Unsupported` | a backreference, `[.x.]`, `[=x=]` |
| `BadInterval`, `NothingToRepeat` | counts out of order or above 1000; an operator with nothing before it |
| `TooDeep` | groups nested more than 202 deep |
| `TooLarge` | a pattern larger than `Options.max_insts` allows (100000 by default) |

## The functions

| | |
|---|---|
| `regex.compile(pattern)` | a `Regex`, or the error |
| `regex.compile_with(pattern, o)` | with `Options`: `o := regex.options(); o.ignore_case = true; o.newline = true; o.max_insts = n` |
| `re.groups()` | how many groups the pattern has |
| `regex.Scratch.new()`, `sc.reserve(re)` | the working memory, and sizing it ahead |
| `re.is_match(sc, text)` | whether the pattern matches anywhere |
| `re.find(sc, text, from)` | the match from offset `from`, as a `Span { start, end }` |
| `re.captures(sc, text, from, groups)` | the same, with each group's span written to `groups` (`groups[0]` is the match; `None` for a group that took no part) |
| `regex.cursor()`, `re.next(sc, text, cur)`, `re.next_captures(...)` | every match in turn, left to right, without overlap |
| `re.replace_first(sc, text, template, dst)`, `re.replace_all(...)` | the text with matches replaced, appended to `dst` |
| `re.split(sc, text, fields)` | the stretches between non-empty matches, appended to `fields` as spans |

`^` and `$` always refer to the whole text, whatever `from` is. A cursor
yields an empty match where one stands alone and never one that touches
the end of the match before it: `x*` in `abc` yields four empty matches,
and `a*` in `baaac` yields (0,0), (1,4) and (5,5).

In a template, `&` is the match, `\1` to `\9` a group (nothing when it
took no part), `\&` an ampersand and `\\` a backslash.

## An AWK on it

`examples/kawk` matches with this module. What the system awk reads
differently (octal escapes, a bracket expression with no member, its own
rules for fields) stays in `kawk`, as a rewriting of the pattern before it
is compiled.
