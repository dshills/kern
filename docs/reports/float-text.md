# Floating-point text: `push_f64`, `parse_f64`, and JSON floats

**Date:** 2026-11-01. **Spec:** `specs/float-text/SPEC.md` v0.1.
**Plan:** `specs/float-text/PLAN.md`. **Item:** the fifth of the
ten-item list ("`f32`/`f64` exist, but `String` formats only integers and
`std/json` cannot read float numbers").

## Design

- **The digits.** Both engines compute the same thing by the same
  search: for *p* from 1 to 17, *v* correctly rounded to *p* significant
  digits in exponent form, stopping at the first that reads back
  exactly. Natively `kern_str_f64_digits` uses `snprintf("%.*e")` and
  `strtod`; the interpreter uses `strconv.FormatFloat(v, 'e', p-1, 64)`
  and `ParseFloat`. All four are correctly rounded, and the runtime never
  calls `setlocale` or `fesetround`, so the C locale and round-to-nearest
  hold. The digits are the strict shortest except in rare cases:
  speccritic found one, 2^-24, whose correctly rounded 16 digits do not
  read back while another 16-digit string does, so the search writes 17
  (`5.9604644775390625e-8`, where ECMAScript writes
  `5.960464477539063e-8`). The spec defines the output as the search's
  result, and `TestF64DigitsLongerCase` pins that value.
- **The layout** is kern code (`String.push_f64`): ECMAScript's
  `Number::toString` rule, plain up to 21 integer digits and down to
  `0.000001`, exponent form otherwise, with `NaN`, `Infinity`,
  `-Infinity`, and `-0` kept (ECMAScript writes `0`). Infinity is detected
  as `v - v` being NaN, since kern float literals have no exponent form.
- **Parsing.** `parse_f64` checks the JSON number grammar in kern, then
  `kern_str_parse_f64` (`strtod` on a NUL-terminated copy, on the stack
  up to 1,023 bytes and on the heap beyond, where a failed allocation
  traps `oom`, which the analysis lists) or `ParseFloat`; an infinite
  result is `None`.
- **JSON.** `as_f64` and `number_f64`; the existing writer writes `null`
  for a number whose text is not a JSON number, which covers NaN and the
  infinities.
- **Hosted only.** A freestanding build calling `push_f64` fails to link
  (`undefined symbol: kern_str_f64_digits`), and one using only the
  integer formatting of `std/str` builds; checked by hand.

## Verification

| Requirement | Evidence |
|---|---|
| R1 layout | `tests/run/float_text`: the 19 values match `node`'s `String(v)` (0, 1, -1, 0.1, 1/3, 100, 1e+21, 100000000000000000000, 123456789012345680000, 0.000001, 1e-7, 1.5e-7, 9007199254740992, 1.7976931348623157e+308, 5e-324), with `-0`, `NaN`, `Infinity`, `-Infinity` |
| R2 round trip | `TestF64Digits`: 10,000 random finite bit patterns read back bitwise, no fewer correctly rounded digits do, and none was one longer than the strict shortest; `float_text`'s 2,000-value sweep (layout included) reads every text back, 0 mismatches, with one checksum in both engines |
| R3 parsing | `float_text`: 9 accepted (with 1e-400 → 0 and 4.9e-324 → 5e-324) and 12 rejected inputs |
| R4 JSON | `tests/run/json_float` |
| R5 engines | the fixture harness, all four configurations |

## Open items

`f32` text; a precision or fixed/scientific mode; the strict-shortest
digits (an algorithm such as Ryū in kern, which would also remove the
libc dependency and allow a freestanding formatter).

## Review

- **speccritic** (gpt-6.1-sol): two critical findings, both correct and
  resolved: the underflow sentence contradicted correct rounding, and the
  search is not always the strict shortest (2^-24); the output is now
  defined as the search's. The runtime entries' contracts were added.
- **plancritic** (davin-go): no critical finding; folded in: the parse
  buffer for long input, the locale and rounding invariant, signed zero,
  the freestanding check, R2's exact assertions.
- **Deviation from the plan.** R2's Go test checks the digit search (the
  intrinsic both engines share) directly over 10,000 bit patterns, and
  the layout's round trip is the kern sweep's, run in both engines,
  rather than running `push_f64` through the interpreter from Go.
- **R6.** Baseline from 2ffbb4f before any edit (310 `.ll` files, 46
  failure lines): all 310 byte-identical after, the same failures; new:
  `float_text`, `json_float` (4 `.ll` files).
- **prism** (default settings, medium effort): one medium and five low.
  Adopted: a guard against a negative buffer length in
  `kern_str_f64_digits`. Not adopted: the non-finite, invalid-text and
  short-buffer cases of the intrinsics (preconditions the kern code
  establishes: it filters NaN and the infinities, validates the grammar,
  and passes a 40-byte buffer for at most 24 bytes); `number_f64` holding
  `NaN` text (the spec's choice: the writer writes it as `null`).
- **realitycheck** PARTIALLY_ALIGNED, score 100, no warning;
  **verifier** offline: 17 low findings, none on this change; clarion
  skipped. `golangci-lint` and `KERN_REQUIRE_QEMU=1 go test ./...` clean.
