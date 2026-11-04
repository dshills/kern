# Plan: frame pointers in hosted kern code

Implements `specs/x86-frame-pointers/SPEC.md` v0.3 in one phase and one
commit. The spec's name predates the finding that arm64 has the same
gap; the work covers every hosted build.

## Phase 1

**Goals.** R1 to R4.

**Files.**
- `compiler/backend/llvm/emit.go`: `fnAttrs` adds
  `"frame-pointer"="all"` when the build is not freestanding.
- `compiler/backend/llvm/emit_test.go`:
  - `TestEmitFramePointers` checks the attribute group for x86-64 and
    arm64, on Linux and macOS, and for a freestanding build;
  - `TestEmitShapes`'s expected group gains the attribute.
- `tests/native_loc_test.go`:
  - a `library_probe.kern` case in `TestNativeTrapLocations`;
  - `TestNativeTrapLibraryProbe`, which runs the R2 program at both
    levels under eight executable names.
- `docs/reports/x86-frame-pointers.md` (new) and the "Not done" entry
  of `docs/reports/native-trap-loc.md`.

**Key decisions.**
- **`all`, not `non-leaf`.** A library leaf in which the fault lands
  then has its own frame record. That is the same choice the C runtime
  makes with `-fno-omit-frame-pointer`.
- **Hosted only.** Freestanding images have no markers and no lookup,
  so they stay byte-identical.

**Acceptance.**
- **The new tests pass.** With the attribute removed,
  `TestNativeTrapLibraryProbe` fails; this was run once by hand and is
  recorded in the report.
- **Images.** The 22 freestanding images (11 fixtures, two targets)
  built by this compiler and by the baseline 3626bb0 are byte-identical.
- **Rosetta.** An `x86_64-apple-macosx` build of the R2 program names
  `down` under all eight names at both levels.
- **Gates.** `go test ./...`, `golangci-lint run ./...` and `gofmt -l`
  are clean.

**Risks.**
- **Speed.** Keeping the frame pointer costs a register and two
  instructions per call. This is measured later on a quiet host, and
  the report says so.
- **CI coverage.** The x86-64 Linux path is covered only by CI's run of
  the new test.
