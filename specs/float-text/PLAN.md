# Plan: floating-point text

**Spec:** `specs/float-text/SPEC.md` v0.1. **Profile:** davin-go.
**Review status.** plancritic (davin-go): no critical finding; its
warnings (the parse buffer for long input, the locale and rounding
invariant, signed zero, the freestanding check, R2's assertions) folded
in.
**Order:** Phase 1 then Phase 2; each ends with `golangci-lint run
./...` and `go test ./...` green. One commit, on `main`, not pushed.

**Baseline.** Before any edit: a compiler built from `HEAD` (2ffbb4f) in
a temporary worktree, `scripts/capture-ll.sh` into
`~/.cache/kern-float-text/ll-base`, read-only; no fixture under `tests/`
until it finishes.

## Phase 1: runtime, intrinsics, `std/str`, `std/json` (R1–R5)

**Files.** `runtime/hosted/src/kern_rt.c`; `compiler/backend/llvm/calls.go`;
`compiler/interp/intrinsics.go` and a Go test in `compiler/interp`;
`compiler/resolve/prelude/str.kern`, `json.kern` (copied to `std/`);
`tests/run/float_text.{kern,out}`, `tests/run/json_float.{kern,out}`.

1. Runtime: `int64_t kern_str_f64_digits(double v, char *buf, int64_t n)`
   (the search with `snprintf("%.*e")` and `strtod`, at most `n` bytes)
   and `double kern_str_parse_f64(const char *p, int64_t n, const kern_loc
   *at)`: `strtod` needs a NUL-terminated string, so the text is copied
   into a 1,024-byte stack buffer when it fits and otherwise into a
   `malloc` block of `n + 1` bytes (checked for overflow), freed after
   the call; a failed `malloc` traps `oom` at the call, so the analysis
   lists `oom` for `str_parse_f64` (only text longer than 1,023 bytes can
   reach it). The runtime never calls `setlocale` or `fesetround`, so
   `snprintf` and `strtod` run in the C locale with round-to-nearest; the
   report records this invariant, and R2's checksum in both engines
   tests it.
2. Backend cases passing `double`, the buffer's pointer and length.
3. Interpreter intrinsics: the same search with `strconv.FormatFloat(v,
   'e', p-1, 64)` and `strconv.ParseFloat`; `ParseFloat`'s range error
   yields its ±Inf result.
4. `str.kern`: the intrinsic declarations; `push_f64` (NaN, ±Inf, then
   the digits from the intrinsic, parsed for sign, digits and exponent;
   all-zero digits are ±0 and write `0` or `-0` from the text's sign;
   otherwise laid out by the spec's rule); `parse_f64` (the JSON number
   grammar checked in kern, then the intrinsic, then `None` for ±Inf).
5. `json.kern`: `as_f64`, `number_f64`.
6. Go test (R2): 10,000 finite values from a seeded generator, written by
   `push_f64` in the interpreter; for each, `math.Float64bits` of
   `ParseFloat(text)` equals the value's bits, the digit count equals the
   smallest *p* the test's own search finds, and the count of values
   whose digits are one longer than `FormatFloat('g', -1)`'s is logged.
7. Fixtures: `float_text.kern` (R1's values, R3's inputs, and R2's
   2,000-value linear-congruential sweep printing a checksum of the
   texts), expected R1 text from `node` (`String(v)`) with `-0` for
   negative zero; `json_float.kern` (R4).
8. Hosted only: a freestanding build calling `push_f64` fails to link,
   and one using only the integer `std/str` functions builds; both
   checked by hand and recorded in the report.

**Acceptance.** Fixtures pass in all configurations; the Go test passes.

## Phase 2: identity, documents, review (R6, R7)

1. R6: capture and compare against the frozen manifest.
2. Documents of R7.
3. Review: stage; `golangci-lint`, `KERN_REQUIRE_QEMU=1 go test ./...`,
   `prism review staged --paths 'compiler/**,std/**,runtime/**,tests/**'
   --reasoning-effort medium`, `realitycheck check --max-tokens 65536`,
   `verifier analyze -mode offline`; clarion skipped; if a tool cannot
   run, the commit waits and the owner is told.
4. Commit `feat(std): floating-point text and JSON floats`.

**Acceptance.** R6 byte-identical; documents updated; reviews clean or
justified; committed.
