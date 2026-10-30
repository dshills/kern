# Plan: std gaps (borrowed lookups, iteration, formatting, UTF-8)

**Spec:** `specs/std-gaps/SPEC.md` v0.1. **Profile:** davin-go.
**Review status.** plancritic (davin-go, with the spec):
EXECUTABLE_WITH_CLARIFICATIONS, 93; folded in: the count oracle of the
differential test and the rebuild note on the slot accessors.
**Order:** three phases, each ending with `golangci-lint run ./...` and
`go test ./...` green; one commit after Phase 3. Every module edit is
made in `compiler/resolve/prelude/<m>.kern` and copied byte for byte to
`std/<m>.kern` (the mirror test checks it).

## Phase 1: `std/map` (R1–R5)

**Steps.**

1. `find(view self, key: view[K]) -> Option[usize] where K: Hashable`:
   `slot(key)` as `contains` uses it; `Some(i)` when the state is 2,
   else `None`.
2. `key_at`, `value_at`, `value_at_mut`: `self.states.get(slot)` first
   (a bounds trap past the capacity), `trap()` unless it is 2, then the
   `Option` of the key or value matched as `get` and `at` do. The
   comment states that a slot is valid until the next `insert` or
   `remove`, and that `insert` may rebuild the table and move every
   entry to another slot.
3. `each(view self, f)` and `each_mut(edit self, f)`: a `while` over the
   slots in order, calling `f` with the key view and the value view (or
   edit) of each live slot, as the probe of 2026-10-30 did.
4. `find_text[V](m: view[Map[str.String, V]], key: view[]u8)`: the probe
   loop of `slot` with `str.hash(key)` for the start and `k.equals(key)`
   for the comparison, returning `Some` only at a full slot.
5. Fixtures: `tests/run/std_map_borrow.kern` and `.out` (spec §4 item
   1); `tests/trap/map_value_at_vacant.kern` (`explicit`) and
   `map_value_at_past.kern` (`bounds`) with their trap headers;
   `tests/negative/map_each_conflict.kern` with its accepted twin
   `map_each_conflict_ok.kern`, and `user_each_conflict.kern` with its
   accepted twin `user_each_conflict_ok.kern` for R5's same-code
   clause. Probe the checker's code on both before writing the
   expectations.

**Acceptance.** `go test ./tests/...` green; both copies identical.

## Phase 2: `std/str` (R6–R9)

**Steps.**

1. `push_i64`: `-` then the magnitude computed in `u64` so that `i64`'s
   minimum does not overflow (`0 - v` taken as `u64` wrapping, or
   `u64(-(v + 1)) + 1`), then the `push_u64` digits.
2. `push_hex`: digits of `v` base 16 into a `[16]u8` buffer, emitted in
   reverse, `0` for zero.
3. `push_padded`: count the decimal digits, push `fill` for
   `width - digits` when positive, then `push_u64`.
4. `struct Rune { code: u32; len: usize }` and `utf8_decode` per R7: the
   lead byte selects the length (1 for `< 0x80`; 2 for `0xC2`–`0xDF`; 3
   for `0xE0`–`0xEF`; 4 for `0xF0`–`0xF4`; `None` otherwise), every
   continuation byte must be `0x80`–`0xBF`, and the second byte's range
   is narrowed for `0xE0` (`0xA0`–`0xBF`), `0xED` (`0x80`–`0x9F`),
   `0xF0` (`0x90`–`0xBF`) and `0xF4` (`0x80`–`0x8F`): Table 3-7, which
   excludes overlongs, surrogates and values above U+10FFFF by
   construction.
5. `utf8_valid` and `utf8_count`: loop over `utf8_decode`, advancing by
   each rune's length.
6. `push_rune`: reject `0xD800`–`0xDFFF` and above `0x10FFFF`; append 1
   to 4 bytes by the value's range.
7. Fixtures `tests/run/std_str_format.kern` and `std_utf8.kern` with
   their `.out` files (spec §4 items 4 and 5), the expected outputs
   written from Go's `strconv` and `unicode/utf8` in a scratch program
   rather than by hand.
8. `tests/utf8_diff_test.go` (spec §4 item 6): generates the kern program
   with 300 byte strings from a fixed seed (half random bytes drawn from
   lead and continuation ranges, half valid strings built with
   `utf8.AppendRune`), runs it with the interpreter, and compares line
   by line. The program prints, per string, `valid` or `invalid` and the
   count or `none` (`utf8_count`'s `Some(n)` or `None`); the oracle is
   `invalid none` where `utf8.Valid(s)` is false and `valid n` with
   `n = utf8.RuneCount(s)` where it is true. The test also asserts that
   at least 50 strings of each kind were generated.

**Acceptance.** `go test ./tests/...` green.

## Phase 3: documents and gates (R10–R12)

1. README library row: the additions in one clause each.
2. `docs/reports/std-gaps.md`: the declarations, §1's constraints and
   why the design follows them, the fixtures, the differential test.
3. Gates (mandatory): `golangci-lint run ./...` 0 issues, `go test ./...`
   green. Advisory: `prism review staged --provider openai --model
   gpt-6.1-sol --format json`; `realitycheck check . --spec
   specs/std-gaps/SPEC.md --plan specs/std-gaps/PLAN.md --provider openai
   --model gpt-6.1-sol --format json` with unrelated directories
   ignored; `verifier analyze -root . -mode offline -spec … -plan …
   -include 'tests/utf8_diff_test.go' -format text`. Findings are fixed
   or justified in the report.

**Acceptance.** Mandatory gates pass; advisory findings recorded.

## Risks

- **Closure codegen through a generic method in the interpreter.** The
  probe ran natively only; the fixtures run both engines, which is the
  check.
- **`find_text` drifting from `String`'s `Hashable`.** It repeats the
  hash and equality the impl uses; the fixture looks up the same keys
  through `find` with a `String` and through `find_text` with bytes and
  requires equal slots.
