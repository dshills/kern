# Frame pointers in hosted kern code

**Version:** 0.3 (2026-11-04). **Review status.** speccritic
(gpt-6.1-sol): VALID_WITH_GAPS, no critical finding; the levels, the
baseline and the image corpus are now named. v0.3: arm64 has the
same gap (found while testing), so the attribute is on every hosted
build. **Scope:**
- `compiler/backend/llvm/emit.go` (`fnAttrs`);
- a native trap-location case in `tests/native_loc_test.go`;
- the "Not done" of `docs/reports/native-trap-loc.md`;
- the report `docs/reports/x86-frame-pointers.md`.

**Language impact:** none. **Follows:** `specs/native-trap-loc/SPEC.md`,
whose report records the gap.

**Glossary.**
- *Both levels*: `--opt=debug` and `--opt=release`.
- *The baseline*: the compiler built from commit 3626bb0, the commit
  before this change, in a scratch worktree.
- R*n*: requirement *n* of §3.
- *Hosted x86-64 build*: a native build for an `x86_64` triple without
  `--env freestanding`.
- *Frameless*: compiled without the frame-pointer chain (`rbp`), which
  LLVM's default for x86-64 IR allows.
- *The fault handler*: the runtime's lookup that names a stack or fault
  record (native-trap-loc). It tries the faulting pc, then the return
  address, then the frame-pointer chain.

## 0. Goal and non-goals

A stack overflow that lands in a library function's stack probe, or in C
code called from one, must name the user function that called it. Today
kern's emitted functions carry no `frame-pointer` attribute.
`-fno-omit-frame-pointer` reaches the runtime's C code but not the
program's IR. So LLVM leaves kern functions frameless on x86-64, which
the native-trap-loc report expected, and on arm64 as well, which it did
not.

Reproduction: the program of R2 at debug names `down` (25-120) in 3 of 8
runs on macOS arm64 and in 0 of 8 runs built for `x86_64-apple-macosx`
under Rosetta. The runs differ only in the executable's name, which
shifts the initial stack. A failing run gives `"span":{"start":0,"end":0}`.

In the arm64 failures (lldb and an instrumented handler), the overflow
lands in libc's `read`, called from `io.read_stdin`:
- `pc` is outside kern code;
- `lr` is in `io.read_stdin`, a library function;
- `fp` is stale, about 8 MB above `sp`, because no kern function set it.

So all three lookups fail. At release both architectures name `down` in
8 of 8 runs.

Non-goals:
- freestanding builds, which have no markers or fault-handler lookup;
  their images stay byte-identical;
- a speed measurement. The cost of keeping the frame pointer is measured
  later on a quiet host, as the owner reserved such timing for later.

## 1. Design (informative)

`fnAttrs` adds `"frame-pointer"="all"` to the attribute group of every
hosted build, the same choice `-fno-omit-frame-pointer` makes for the C
runtime. Every kern function then saves and sets the frame pointer, so
the handler's third lookup walks the chain from any frame to the first
user frame.

## 2. Requirements and acceptance criteria

**R1. The attribute.**
- The emitted IR of every hosted build (x86-64 and arm64, Linux and
  macOS) has `"frame-pointer"="all"` in its function attribute group,
  and a freestanding build has no such attribute. A test checks the
  attribute group for each.
- An image check by `cmp` against the baseline shows every
  `tests/freestanding/*.kern` fixture the baseline builds unchanged, on
  both freestanding targets.

**R2. The location.** The program below, built for the host at both
levels, reports a `stack` record that names `down` under each of eight
executable names (`a` to `abcdefgh`), which shift the initial stack:

```kern
module t
import "std/io"
func down(n: u64) -> u64 {
    with r := io.read_stdin() { }
    return down(n + 1u64) + 1u64
}
```

`main` calls `down(0u64)`, with standard input `/dev/null`. The check
runs:
- in `TestNativeTrapLibraryProbe` and as a `TestNativeTrapLocations` case
  on every host (arm64 macOS here; x86-64 and arm64 Linux and macOS in
  CI);
- for x86-64 on this host, by a recorded Rosetta run of
  `x86_64-apple-macosx` builds in the report.

With the attribute removed, `TestNativeTrapLibraryProbe` must fail.

**R3. Nothing else changes.** `go test ./...` passes, including the
existing native trap tests.

**R4. Documents.** The native-trap-loc report's "Not done" entry points
here. The new report records:
- the runs before and after, on arm64 and under Rosetta;
- the freestanding image identity results;
- that hosted images change, every function now carrying the attribute;
- that the speed cost is not yet measured.
