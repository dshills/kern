# Plan: floating-point text on bare metal

Implements `specs/freestanding-float/SPEC.md` v0.4 in two phases, one
commit. Phase 1 alone would leave the report's generated inventory stale
(`TestHostedOnlyInventory` fails), so both phases land together.

## Phase 1: the entries, the interpreter fix, the tests

**Goals.** R1 to R7 and R9.

**Files.**
- Create `runtime/freestanding/src/f64text.c`: the two entries over
  160-limb big integers (§2 of the spec); every helper `static`; only
  `<stdint.h>` and `<stddef.h>`.
- `runtime/freestanding/freestanding.go`: embed it as `F64TextC`;
  `RuntimeC` stays `rt.c` alone.
- `runtime/freestanding/provided.go`: `Provided()` reads both C sources.
- `compiler/backend/native/build.go` (`buildFreestanding`): compile
  `f64text.c` with the runtime's flags as a fourth object; pass it to
  `ld.lld` between `--start-lib` and `--end-lib`; put it in the archive
  when there is no `ld.lld`.
- `compiler/interp/clock.go`: `parseF64` parses `canonicalNumber(b)`
  (R9).
- Tests:
  - `runtime/freestanding/f64text_test.go` and
    `runtime/freestanding/testdata/f64driver.c`: the differential test
    (R5), the truncation test (R1), the self-containment test (R3:
    no undefined symbols, only the two entries defined, and the sum of
    every function's `-fstack-usage` frame under 8 KiB with every frame
    `static`, on both targets; `f64text.c` has no recursion, so the
    sum bounds every call chain);
  - `runtime/freestanding/provided_test.go`: the llvm-nm match compiles
    `f64text.c` too (R4);
  - `compiler/interp/clock_test.go`: `TestParseF64Canonical` (R9);
  - fixtures `tests/freestanding/float_text.kern` (R6) and
    `tests/run/float_text_long.kern` (R9 against the hosted native
    build), each with `.out`.
- `compiler/resolve/prelude/str.kern` and `std/str.kern` (kept
  identical): remove "Hosted only." (R8). The JSON sources
  (`compiler/resolve/prelude/json.kern`, `std/json.kern`) carry no such
  note (checked with `rg -i hosted`), so R8 needs no change there.

**Key decisions.** Exact big-integer arithmetic, not a fast algorithm:
the entries run once per formatted or parsed number on bare metal, and
exactness is checkable against libc. 18 digits are generated once and
rounded at each *p*, instead of regenerating per *p*: the 18 digits are
the exact value truncated, plus a sticky flag for a nonzero remainder
after them, so for any *p* ≤ 17 the part beyond *p* digits is exactly
`dig[p..17]` and the flag, and its comparison with one half of the last
place is exact: above when `dig[p] > 5`, or `dig[p] = 5` and a later
digit or the flag is nonzero; a tie only when `dig[p] = 5`, every later
digit is 0 and the flag is clear (then ties to even). No digit is
rounded twice. The powers of two and ten, the nudged halfway texts at
every *p* and the carry case (9.5 → `1e+01` at *p* = 1, in the
differential test's formatted texts) cover it. The object is
linked lazily so other images do not change (R7).

**Acceptance.**
- `go test ./runtime/freestanding ./compiler/interp ./compiler/analysis
  ./tests ./cmd/kern` passes; the differential test reports no mismatch
  at `-f64text-scale=10` as well (run once, not in CI).
- Every `tests/freestanding` fixture other than `float_text` builds to a
  byte-identical image with the previous compiler, built from the fixed
  baseline commit `cff9538` in a scratch worktree, for both targets
  (R7); the report records the baseline and candidate revisions.
- `float_text` prints the same transcript under QEMU on both targets as
  in the interpreter (the harness, R6).
- `golangci-lint run ./...` and `gofmt -l .` are clean.

**Risks.** A rounding bug that random inputs miss: covered by the halfway
and nudged texts and the powers of two and ten. Go's own `strconv`
defects (found by this test) make it a wrong reference on two text
shapes: those texts are pinned with their values.

## Phase 2: documents and the inventory

**Goals.** R4's regenerated inventory and R8.

**Files.**
- `docs/reports/freestanding-availability.md`: regenerate the inventory
  (`go test ./compiler/analysis -run TestHostedOnlyInventory
  -update-hosted-inventory`); replace the "Floats are hosted" finding
  with a pointer to this work.
- `docs/adr/` ADR-011 and `specs/float-text/SPEC.md`: a dated note that
  the freestanding runtime now provides the two entries.
- `tests/freestanding/README.md`: the fourth object.
- Create `docs/reports/freestanding-float.md`: what changed, the test
  counts, the two `strconv` defects, image sizes (`hello` unchanged;
  `float_text`'s size) and the fixture transcripts' agreement.

**Acceptance.** `go test ./tests` (which includes the documentation
lint) passes; `TestHostedOnlyInventory` passes without the update flag.

**Risks.** None beyond stale prose: the inventory is generated and
checked.
