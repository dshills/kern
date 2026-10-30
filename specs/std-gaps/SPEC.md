# Standard library: borrowed map lookups, map iteration, formatting, UTF-8

**Version:** 0.1 (2026-10-30). **Scope:** the embedded std modules
`std/map` and `std/str` (`compiler/resolve/prelude/map.kern` and
`str.kern`, mirrored identically under `std/`), fixtures under
`tests/run/` and `tests/negative/`, a differential test against Go's
`unicode/utf8`, the README's library row, and the report
`docs/reports/std-gaps.md`. **Language impact:** none. No grammar,
checker or §16 change; the two modules gain the declarations of §2.
**Related:** `docs/reports/std-library.md` ("Not done": borrowed
lookups, map iteration, formatting beyond `push_u64`, UTF-8 decoding);
`docs/reports/closures.md` (non-escaping closures, captures by access);
`spec/types.md` §1 (std modules export every declaration);
`specs/initial/AI_Native_Systems_Language_SPEC.md` §5.3 and §5.4 (the
elision rule and access conflicts).

**Review status.** speccritic (OpenAI gpt-6.1-sol, profile general,
with `docs/reports/std-library.md` as context): VALID_WITH_GAPS, no
critical finding; its two substantive warnings (which trap a slot past
the capacity gets, and how an invalid string's count is compared) are
folded into R2 and §4 item 6. The rest are the preflight's prose flags.

**Glossary.** R*n*: requirement *n* of §3. Slot: an index of a map's
table, from 0 to its capacity; a slot is live when it holds an entry.
Scalar value: a Unicode code point outside the surrogate range
U+D800–U+DFFF, at most U+10FFFF. Well-formed UTF-8: the encoding of
Unicode 15's Table 3-7: each scalar value in its shortest form of 1 to
4 bytes. Engine: the interpreter or the native build; opt level: debug
or release.

## 0. Goals and non-goals

Goal: the four gaps the std-library report lists, closed with safe kern
code in the two modules, so that a program can:
- look up a map entry without giving up an owned key;
- look up a `String`-keyed map by a byte view;
- visit every entry of a map;
- format signed and hexadecimal numbers and padded fields;
- decode, validate, count and encode UTF-8.

Non-goals: an iterator protocol or `for` over a map; ordered maps; a
general format string; Unicode properties (case, width, categories);
normalization; JSON; directories and temporary files.

## 1. Constraints that shape the design

- **Two access parameters.** A function with two access parameters
  cannot return an access (the elision rule, §5.3), so
  `get(view self, key: view[K]) -> view[V]` is not expressible. A lookup
  that borrows its key therefore returns a slot index, and separate
  accessors with one access parameter (`self`) turn a slot into a view
  or an edit of its key or value.
- **Closures.** They capture by access for the duration of a call and
  cannot be stored (`docs/reports/closures.md`). A visiting method that
  takes a closure is the iteration that fits. A closure that captures
  the map being visited conflicts with the visiting call's own access to
  it under §5.4 and is rejected statically, so a map cannot be changed
  while it is being visited.

## 2. Declarations

In `std/map`, methods of `Map[K, V]`:

```kern
func find(view self, key: view[K]) -> Option[usize] where K: Hashable
func key_at(view self, slot: usize) -> view[K]
func value_at(view self, slot: usize) -> view[V]
func value_at_mut(edit self, slot: usize) -> edit[V]
func each(view self, f: closure(view[K], view[V]))
func each_mut(edit self, f: closure(view[K], edit[V]))
```

and a function for maps keyed by `str.String`:

```kern
func find_text[V](m: view[Map[str.String, V]], key: view[]u8) -> Option[usize]
```

In `std/str`, methods of `String`:

```kern
func push_i64(edit self, v: i64)
func push_hex(edit self, v: u64)
func push_padded(edit self, v: u64, width: usize, fill: u8)
func push_rune(edit self, code: u32) -> bool
```

and, for byte views:

```kern
struct Rune { code: u32; len: usize }
func utf8_decode(s: view[]u8, at: usize) -> Option[Rune]
func utf8_valid(s: view[]u8) -> bool
func utf8_count(s: view[]u8) -> Option[usize]
```

## 3. Requirements

**R1 — `find`.** Returns `Some(slot)` for the live slot holding a key
equal to `key` under `Hashable.eq`, and `None` when no live slot holds
one. It does not change the map, and it takes the key by view, so the
caller keeps an owned key.

**R2 — Slot accessors.** `key_at`, `value_at` and `value_at_mut` view or
edit the key or value of a live slot. On a slot not below the capacity
they trap with kind `bounds`; on a slot below the capacity that is not
live they trap with kind `explicit`. A slot returned by `find` stays valid until the next
`insert` or `remove` on the map. `insert` can move every entry by
rebuilding the table; the documentation says so.

**R3 — `find_text`.** For a map whose keys are `str.String`, it returns
the slot `find` would return for a `String` holding the bytes of `key`,
without building one: it hashes with `str.hash` and compares with
`String.equals`, which is what `String`'s `Hashable` implementation
uses.

**R4 — `each` and `each_mut`.** Call `f` once for every live entry, in
increasing slot order, with views of the key and the value (`each`), or
a view of the key and an edit of the value (`each_mut`). A map with no
entries never calls `f`. The order depends only on the map's capacity
and the keys' hashes and insertion history: two maps built by the same
inserts and removals visit in the same order.

**R5 — Visiting is exclusive.** A closure passed to `each` or
`each_mut` that captures the map being visited is rejected by the
checker with `ACC_CONFLICT`, the code a user-written function with the
same parameter conventions gets.

**R6 — Formatting.**
- `push_i64` appends the decimal value with a leading `-` when it is
  negative, including `-9223372036854775808`.
- `push_hex` appends lowercase hexadecimal digits without a prefix or
  leading zeros, and `0` for zero.
- `push_padded` appends the decimal value right-aligned in `width`
  bytes, filling on the left with `fill`. A value with more digits than
  `width` is appended whole.

**R7 — `utf8_decode`.** Returns the scalar value encoded at byte `at` of
`s` and the length of its encoding when the bytes from `at` begin a
well-formed sequence. Otherwise it returns `None`: `at` not below
`len(s)`, a continuation byte where a sequence starts, a lead byte
`0xC0`, `0xC1` or above `0xF4`, a sequence cut off by the end of `s`, a
missing continuation byte, an overlong form, a surrogate, or a value
above U+10FFFF.

**R8 — `utf8_valid` and `utf8_count`.** `utf8_valid(s)` is true exactly
when `s` is well-formed UTF-8 (the empty view is). `utf8_count(s)` is
`Some(n)` with `n` scalar values when `s` is well-formed, `None`
otherwise.

**R9 — `push_rune`.** For a scalar value it appends its shortest
encoding and returns true. For a surrogate or a value above U+10FFFF it
appends nothing and returns false.

**R10 — Both engines.** Every fixture of §4 gives the same output in the
interpreter and natively, in debug and release (the fixture harness's
comparison).

**R11 — Existing behaviour unchanged.** The existing `std_map`,
`std_str` and `std_io` fixtures and every other test pass unchanged. No
existing declaration of the two modules changes.

**R12 — Documents.** The README's standard-library row names the
additions. The report lists them with the constraints of §1. The two
module copies (`compiler/resolve/prelude/` and `std/`) stay identical,
which the existing mirror test checks.

## 4. Acceptance criteria

1. `tests/run/std_map_borrow.kern`: `find` on present and absent keys;
   an owned `String` key used for a lookup and printed afterwards;
   `find_text` on present and absent byte keys; `key_at`, `value_at`
   and `value_at_mut` on found slots; `each` summing and printing
   entries; `each_mut` updating every value; `each` on an empty map; and
   the same visit order for two maps built alike.
2. `tests/trap/` fixtures: `value_at` on a slot that is not live
   (`explicit`) and on a slot past the capacity (`bounds`).
3. `tests/negative/` fixture with its accepted twin: a closure passed to
   `each_mut` that captures the map (`ACC_CONFLICT`), and the same shape
   through a user function, also `ACC_CONFLICT` (R5).
4. `tests/run/std_str_format.kern`: R6 on 0, 1, 9, 10, `u64` max,
   `i64` min, `i64` max, -1, widths shorter and longer than the number,
   and two fill bytes.
5. `tests/run/std_utf8.kern`: every boundary scalar value (U+0000,
   U+007F, U+0080, U+07FF, U+0800, U+D7FF, U+E000, U+FFFF, U+10000,
   U+10FFFF) encoded with `push_rune` and decoded back; and each `None`
   case of R7 on its own bytes. These are `C0 80`, `E0 80 80`,
   `F0 80 80 80` (overlong), `ED A0 80` (surrogate), `F4 90 80 80`
   (above U+10FFFF), `80` (stray continuation), `F5` and `FF` (invalid
   leads), `E2 82` (cut off) and `E2 28 A1` (bad continuation).
   `push_rune` also returns false for U+D800 and 0x110000.
6. A Go test that builds one kern program over at least 300
   pseudo-random byte strings (biased toward UTF-8 lead and continuation
   bytes) and well-formed strings, runs it in the interpreter, and
   compares each result with Go's: `utf8_valid` with `utf8.Valid`, and
   `utf8_count` with `None` where `utf8.Valid` is false and with
   `Some(utf8.RuneCount(s))` where it is true (R8). Go's `utf8` follows the same
   Table 3-7.
