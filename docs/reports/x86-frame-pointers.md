# Frame pointers in hosted kern code

**Date:** 2026-11-04. **Spec:** `specs/x86-frame-pointers/SPEC.md` v0.3
(speccritic VALID_WITH_GAPS, no critical finding). **Plan:**
`specs/x86-frame-pointers/PLAN.md`. **Follows:** the "Not done" of
`docs/reports/native-trap-loc.md`, which said that kern code on x86-64
keeps no frame pointer.

## What I found

The item was scoped to x86-64, but arm64 has the same gap. Kern's
emitted functions carried no `frame-pointer` attribute.
`-fno-omit-frame-pointer` applies to the runtime's C code only, so LLVM
left kern functions frameless on both architectures.

The test program recurses through `io.read_stdin()`, whose library body
has a 4 KiB buffer. Built once and run under eight executable names
(`a` to `abcdefgh`, which shift the initial stack), it gave:

| build | before: runs naming `down` | after |
|---|---|---|
| arm64 macOS, debug | 3 of 8 | 8 of 8 |
| arm64 macOS, release | 8 of 8 | 8 of 8 |
| x86-64 macOS (Rosetta), debug | 0 of 8 | 8 of 8 |
| x86-64 macOS (Rosetta), release | 8 of 8 | 8 of 8 |

Every failure was `"span":{"start":0,"end":0}`. For a failing arm64 run,
lldb and a temporarily instrumented handler showed why:
- the overflow lands in libc's `read`, called from `io.read_stdin`;
- `pc` is outside kern code;
- `lr` is in `io.read_stdin`, a library function;
- `fp` is stale, about 8 MB above `sp`, because no kern function on the
  way had set it.

So all three of the handler's lookups failed. A passing run is one where
the overflow lands in the probe of a frame whose `lr` is in `down`.

## The change

`fnAttrs` (`compiler/backend/llvm/emit.go`) adds `"frame-pointer"="all"`
to the attribute group of every hosted build. That is the C runtime's
choice. Every kern function now keeps the chain, and the handler's
frame-pointer walk reaches the first user frame from any depth.
Freestanding builds have no fault-handler lookup and are unchanged.

## Verification

- **`TestEmitFramePointers`** checks the attribute group for x86-64 and
  arm64 Linux and macOS (with the attribute) and for a freestanding
  build (without it). `TestEmitShapes` expects the new group.
- **`TestNativeTrapLibraryProbe`** runs the program at both levels under
  the eight names and requires `down` every time. It is also a
  `TestNativeTrapLocations` case.
  - With the attribute removed by hand, the test fails 8 times, on
    arm64 debug.
  - CI runs it on x86-64 and arm64 Linux and on macOS.
- **Images.** All 22 freestanding images (11 fixtures, both targets) are
  byte-identical to the baseline 3626bb0's. Hosted images change, since
  every function now saves and sets its frame pointer.

## Not done

**The speed cost is not measured.** Keeping the frame pointer takes a
register and a store and a load per call. The owner reserved timing for
a quiet host, so the cost is to be measured then, by instructions
retired and by time on the bench suite.
