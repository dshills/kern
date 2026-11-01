# Floating-point text: `push_f64`, `parse_f64`, and JSON floats

**Version:** 0.1 (2026-11-01). **Scope:** `std/str`
(`compiler/resolve/prelude/str.kern`, mirrored in `std/str.kern`) and
`std/json` (`json.kern`, mirrored); two runtime entries in
`runtime/hosted/src/kern_rt.c` with their backend calls and interpreter
intrinsics; fixtures; documents. **Language impact:** none.
**Related:** `docs/reports/std-files.md` ("JSON numbers are not read as
floats"); ECMAScript's `Number::toString` (the layout rule below);
RFC 8259 §6 (the JSON number grammar).

**Review status.** speccritic (gpt-6.1-sol): two critical findings,
both correct and resolved: the underflow sentence contradicted correct
rounding (now: correctly rounded, which underflows to ±0), and the search
does not always give the strict shortest string (2^-24); the output is
now defined as the round-trip digits the search gives. The runtime
entries' contracts are stated.

**Glossary.** R*n*: requirement *n* of §4. Round-trip digits of a finite
`f64` *v*: for the smallest *p* from 1 to 17 such that *v* correctly
rounded to *p* significant decimal digits reads back (correctly rounded)
as exactly *v*, that *p*-digit string *d* and its decimal exponent *e*
(`v ≈ d[0].d[1..] × 10^e`). This is the shortest string in almost every
case; a value can have a shorter string that reads back but is not the
correctly rounded one at that length (2^-24 reads back from 16 digits
ending `...063` while its correctly rounded 16 digits end `...062`), and
there the round-trip digits are one longer. *p* ≤ 17 always reads back. Correctly rounded: the nearest representable value, ties to
even. Hosted: not `--env freestanding`. Abbreviations: NaN, not a number;
Inf, infinity; JSON, JavaScript Object Notation.

## 0. Goals

kern programs can write a float as text that reads back exactly, read
decimal text as a float, and use JSON numbers with fractions or
exponents, with the same output in both engines. Non-goals: `f32`
formatting, a precision or fixed-point argument, hexadecimal floats,
locales, and a freestanding implementation.

## 1. Background

`std/str` formats only integers; `std/json` keeps a number's text and
reads it only as `u64` or `i64`. The language has `f64` and `f32`, and
the interpreter represents a float as a Go `float64`.

## 2. Design

```kern
// std/str
impl String { func push_f64(edit self, v: f64) }
func parse_f64(s: view[]u8) -> Option[f64]
// std/json
func as_f64(v: view[Value]) -> Option[f64]
func number_f64(v: f64) -> Value
```

**`push_f64(v)`** appends:
- `NaN`, `Infinity` or `-Infinity` for those values;
- `0` for +0 and `-0` for −0 (ECMAScript writes `0` for both; kern keeps
  the sign, so the text reads back to the same value);
- otherwise the round-trip digits *d* (*k* digits) and exponent *e*, with
  *n* = *e* + 1, laid out by ECMAScript's rule: if *k* ≤ *n* ≤ 21, *d*
  followed by *n* − *k* zeros; if 0 < *n* ≤ 21, *d*[0..*n*] `.`
  *d*[*n*..]; if −6 < *n* ≤ 0, `0.` then −*n* zeros then *d*; otherwise
  *d*[0], then `.` and *d*[1..] when *k* > 1, then `e`, `+` or `-`, and
  |*n* − 1| in decimal; a `-` in front of a negative value.

**`parse_f64(s)`** is `Some` of the correctly rounded value of *s* when
*s* is exactly one JSON number (RFC 8259 §6: an optional `-`, `0` or a
digit 1–9 followed by digits, an optional `.` and digits, an optional
`e`/`E`, sign and digits) whose correctly rounded value (nearest, ties
to even, subnormals included) is finite; `None` for any other text, and
for a number whose rounded value is infinite. A number whose magnitude
rounds below the smallest subnormal is ±0, which is `Some`.

**JSON.** `as_f64` is `parse_f64` of a `Number`'s text, `None` for other
variants; `number_f64(v)` is a `Number` holding `push_f64`'s text. The
writer already writes `null` for a `Number` whose text is not a JSON
number, which is what `NaN` and the infinities become.

**Both engines produce the same text**, because both compute the
round-trip digits by their definition: for *p* from 1 to 17, format *v*
with *p* correctly rounded significant digits in exponent form, and stop
at the first that reads back as *v*; kern code then lays the digits out.
The runtime entries, declared in `std/str` as intrinsics:
- `str_f64_digits(v: f64, buf: edit[]u8) -> usize`: for a finite *v*,
  writes into `buf` (the caller passes 32 bytes) the exponent-form text
  of the round-trip digits, `[-]d[.ddd]e±XX` with at least two exponent
  digits, and returns its length; it cannot fail.
- `str_parse_f64(s: view[]u8) -> f64`: for text the kern code has already
  checked to be one JSON number, the correctly rounded value, ±Inf when
  it overflows (which `parse_f64` turns into `None`). Text longer than
  1,023 bytes is copied into a heap block for the C call; if that block
  cannot be allocated the call traps `oom` (the effect analysis lists it).
*Implementation guidance (non-normative):* natively `snprintf("%.*e")`
and `strtod`; in the interpreter `strconv.FormatFloat(v, 'e', p-1, 64)`
and `strconv.ParseFloat`, all correctly rounded, so the searches agree.

**Hosted only**: the freestanding runtime has no `snprintf`/`strtod`; a
freestanding program that calls these functions fails to link, while one
that uses the rest of `std/str` is unaffected (the backend declares only
the runtime entries a program calls).

## 3. Out of scope

`f32` text; precision, fixed or scientific modes; hexadecimal floats;
a freestanding formatter; changing how `std/json` parses (numbers keep
their text).

## 4. Requirements

**R1. Layout.** `push_f64` writes exactly the text of §2 for: 0, −0, 1,
−1, 0.1, 1/3, 100, 1e21, 1e20, 123456789012345680000, 1e-6, 1e-7,
1.5e-7, 2^53, the largest finite `f64`, the smallest subnormal, NaN,
±Inf. *Verified by:* `tests/run/float_text.kern` (all four
configurations) with an expected file written from ECMAScript's output
for the same values (with −0 written `-0`).

**R2. Round trip.** For 10,000 pseudo-random finite `f64` bit patterns,
`parse_f64` of `push_f64`'s text is the same value and the text has the
round-trip digits (at most 17). *Verified by:* an interpreter-engine Go
test that also counts how often the digits are one longer than
`strconv.FormatFloat(v, 'g', -1, 64)`'s strict shortest (expected rare;
the count is recorded in the report), and `float_text.kern` running a
deterministic sweep (a linear congruential generator, 2,000 values) that
prints a checksum, identical in both engines.

**R3. Parsing.** `parse_f64` accepts and rounds `0`, `-0`, `1.5`,
`1e3`, `1E-3`, `2.5e+10`, `123456789012345678901234567890`, `4.9e-324`,
`1e-400` (to 0) and rejects `` (empty), `-`, `01`, `1.`, `.5`, `1e`,
`+1`, `1e400`, `NaN`, `Infinity`, `1_0`, ` 1`. *Verified by:*
`float_text.kern`.

**R4. JSON.** `as_f64` of the numbers in `[1.5, -2e3, 7]` is 1.5,
−2000 and 7; of `"x"` and `null`, `None`; `number_f64(0.25)` writes
`0.25`; `number_f64(NaN)` writes `null`. *Verified by:*
`tests/run/json_float.kern`.

**R5. Engines agree.** Every fixture passes the harness in both engines.

**R6. Existing programs unchanged.** The LLVM IR of the capture corpus is
byte-identical. *Verified by:* a baseline capture and `cmp`.

**R7. Documents.** `docs/reports/std-files.md`'s note on floats points
here; the README's `std/str` and `std/json` descriptions mention floats;
`docs/reports/float-text.md` records the design, R2's results and the
review.

## 5. Acceptance criteria

`golangci-lint run ./...` and `KERN_REQUIRE_QEMU=1 go test ./...` clean;
R6 byte-identical.

## 6. Risks

- **libc rounding.** The search relies on `snprintf` and `strtod`
  rounding correctly, which glibc and macOS's libc do; R2's checksum in
  both engines would expose a host that does not.
- **Float comparison in kern.** NaN is detected by `v != v`, the
  infinities by comparison with the largest finite value.
