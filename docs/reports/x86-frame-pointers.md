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

## The speed cost (measured 2026-11-04)

Keeping the frame pointer takes a register, and a store and a load per
call. It was measured on the 13 `bench/kern` workloads at `--opt=release`,
built by two compilers from `dd16b89`: as committed, and with the
attribute removed by hand. The host was given over to timing, but
Spotlight was indexing, so the one-minute load was 19 to 30 throughout.

The method: 10 rounds, each running every workload once with each build,
alternating which build ran first. Round 1 is dropped, because Rosetta
translates each x86-64 binary on its first run. That leaves nine paired
samples. Instructions retired come from `/usr/bin/time -l`, and time is
the workload's own `ns`. Every run of a workload gave the same checksum.

**arm64 macOS (native).** There is no measurable cost.
- The kern text section changes by −4 to +8 instructions per program,
  since Apple's arm64 ABI already keeps the frame chain in non-leaf
  functions.
- Instructions retired are within ±0.3% on 11 of 13 workloads.
  `arena_ast` is +1.8% and `hash_table` +0.9%.
- Time, as the geometric mean of the per-workload medians of paired
  ratios, is 0.994. That is inside the noise; the per-workload medians
  range from 0.86 to 1.05 at this load.

**x86-64 macOS (Rosetta).** The cost is about 1%.
- The kern text section grows by 17 to 39 instructions per program,
  1.5% to 7%: `rbp` is saved, set and restored in every function.
- Rosetta's instruction counts vary from run to run by more than the
  effect (`arena_ast` gave 0.78), so only the static count and time
  count here.
- Time, as the same geometric mean, is 1.009. The per-workload medians
  range from 0.96 to 1.04; `vector` (+2.4%) and `lock_protected_map`
  (+3.6%) are the largest.

**What this leaves.** Native x86-64 Linux is not measured: this host
runs x86-64 only under Rosetta, and the Linux containers are emulated
too. Rosetta's figure is an estimate of the order of the cost, about 1%,
and not a bound on it. The frame pointer stays on: about 1% on x86-64
buys a named stack-overflow trap from any depth.
