# Floating-point text on bare metal

**Date:** 2026-11-04. **Spec:** `specs/freestanding-float/SPEC.md` v0.4,
plan `specs/freestanding-float/PLAN.md` (plancritic EXECUTABLE_AS_IS).
**Baseline:** `cff9538`.

## What changed

A freestanding program can now format and parse `f64` text:
`String.push_f64`, `str.parse_f64`, `json.as_f64` and `json.number_f64`
are no longer rejected with `ENV_HOSTED_ONLY`. The freestanding runtime
has the two entries the hosted runtime builds on `snprintf` and `strtod`,
in `runtime/freestanding/src/f64text.c`:

- `kern_str_f64_digits` generates the first 18 decimal digits of the
  exact value (plus a flag for a nonzero rest) with 160-limb big
  integers, rounds them at each precision from 1 to 17 (ties to even),
  and stops at the first that reads back as the value.
- `kern_str_parse_f64` keeps at most 800 significant digits and a sticky
  801st, scales the ratio so the quotient has 53 bits (fixed 2^1074 for
  a subnormal), divides by shift and subtract, and rounds the remainder
  to nearest, ties to even.

Neither allocates, traps or calls anything outside the file: compiled
alone with the runtime's flags, the object has no undefined symbol. The
sum of every function's frame is 5,536 bytes on AArch64 and 5,400 on
x86-64, against the 8 KiB bound and the 64 KiB stack.

The file is its own object, linked lazily (`ld.lld --start-lib`), so
only an image that formats or parses a float contains it. Built with
`cff9538` and with this change, all 20 other images of
`tests/freestanding` (10 fixtures, both targets) are byte-identical. A
first version compiled the file into `rt.c`'s object and grew every
image, `hello` from 72,992 to 79,088 bytes on AArch64, which is why it
moved. `float_text`'s image is 94,112 bytes on AArch64 and 32,376 on
x86-64.

## Tests

- **Differential** (`runtime/freestanding/f64text_test.go`). The file
  is compiled for the host with a driver that also runs the hosted
  runtime's libc code, and every answer is checked against libc and
  against `strconv` (the interpreter's reference). The default run
  checks:
  - 100,000 random finite bit patterns plus 2,750 special values (every
    power of two and ten, the subnormal and normal boundaries, carries
    such as 9.5 at one digit) for the digits;
  - each of those values' texts at every precision from 1 to 17
    (1,746,750 parses), plus 23 special texts;
  - 100,000 random JSON numbers;
  - 10,000 exact halfway texts, each with a text just above and just
    below it (30,000 parses);
  - long texts: a halfway point followed by 2,000 zeros, with and
    without a final 1, and the cancellation texts below.

  That is 1,876,773 parses in all. The run takes about 6 seconds with
  no mismatch, and `-f64text-scale=10` (one million random values,
  about 18.5 million parses) also passes.
- **Self-containment.** No undefined symbols; exactly the two entries
  defined; the frame sum, on both targets.
- **`TestProvidedMatchesObjects`.** It compiles `f64text.c` too, so
  `Provided()` still equals what the objects define.
- **Fixtures.**
  - `tests/freestanding/float_text.kern` covers every `push_f64` layout
    branch, the boundary values, a rejected and an overflowing text,
    JSON, a checksum of 500 written and reread values, and the two long
    texts. It prints the same transcript in the interpreter and under
    QEMU on both targets.
  - `tests/run/float_text_long.kern` checks the long texts in hosted
    mode, where the native build uses `strtod`. Without the interpreter
    fix it fails.

## Two defects in Go's `strconv.ParseFloat`

The differential test found two shapes of valid JSON number that
`strconv.ParseFloat` (Go 1.27.3) reads wrong. On both, the hosted
runtime's `strtod` and the new code give the correctly rounded value:

1. **A long digit run that cancels a large exponent.** `strconv` stops
   accumulating an exponent's digits near 10000, before it adds the digit
   position. `0.` then 100,001 zeros then `1e100002` is exactly 1, and
   `strconv` returns 0.
2. **An integer part of more than 800 digits near a halfway point.** On
   its slow path `strconv` sets the decimal point from the count of
   digits it stored, which stops at 800. Each further digit makes the
   value 10 times too small: an 801-digit integer just above the
   midpoint of 1 and the next double, scaled by `e-800`, reads as 0.1
   instead of 1.0000000000000002.

The interpreter used `strconv` directly, so before this change kern's two
engines disagreed on such text in hosted mode too. Its `str_parse_f64`
now rewrites the text first, keeping the significant digits (at most 800
and a sticky 801st) in `d.ddd` form with the exponent of the first digit.
On that text `strconv` is exact (`compiler/interp/clock.go`,
`canonicalNumber`, R9). `TestParseF64Canonical` pins the texts and checks
50,000 random numbers against `strconv` directly.

## Not done

Nothing the spec asks for is left. `f32` text and a faster algorithm are
non-goals.
