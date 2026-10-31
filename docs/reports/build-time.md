# Build time: where `kern build` spends it

**Date:** 2026-10-31. **Host:** Apple silicon (arm64, macOS), busy.
Instructions retired (`/usr/bin/time -l`) carry the comparison. Wall
times are medians of interleaved runs.

## Where the time goes

`kern build --opt=release` of `bench/kern/btree` (242 lines) took about
315 ms. The front end, everything kern does before clang, retires about
27M instructions on `examples/selfhost/lexer.kern`; `kern emit-ir` there
returns in under 10 ms. The rest is the one clang invocation, split
apart by hand:

| step | instructions | wall |
|---|---|---|
| compile the program's LLVM text (lexer, 3,669 lines) | 1.03B | 80 ms |
| compile the hosted runtime, `kern_rt.c` (833 lines) | 1.25B | 110 ms |
| link | 0.50B | 80 ms |

The runtime was compiled from source on every build. It changes only
with the compiler.

## The change

`native.BuildModule` takes the runtime as an object from the user cache
directory (`kern/runtime/`), and compiles it there on a miss.

- **The key** covers the runtime source, every flag that reaches the
  runtime (opt level, `KERN_RELEASE`, test hooks, TSan, debug, target),
  the clang binary (its resolved path, size and modification time) and
  the working directory. The compile runs in the working directory, as
  the combined compile did, so relative options resolve the same.
- **The fingerprint:** the manifest under the key records a hash of what
  `clang -E -v` prints for the runtime:
  - the preprocessed source;
  - the driver's account of the job: its version, the configuration
    files it loaded (Homebrew's sets the sysroot), and the `-cc1`
    command with every option they expand to.

  Every build takes the fingerprint again with one preprocessing run
  (about 40 ms, a third of a compile), and compiles afresh when it
  changed. So the cache follows every header change: an edited header,
  a new one that shadows another, a `__has_include` probe that now
  succeeds. It also follows another SDK, another environment
  (`SDKROOT`, `CPATH`, a deployment target) and a created or edited
  configuration file.
- **Files:** the manifest also records the size and modification time
  of the compiler executable behind any shim and of the configuration
  files. A compiler upgraded in place changes them when it leaves the
  fingerprint unchanged.
- **Publishing:** objects are named by their content and never
  rewritten. The manifest names its object, and on a miss it is
  published last by one rename. A concurrent build therefore sees the
  old entry or the new one, whole. The fingerprint and the compile name
  a copy of the source kept under the key, so their paths stay fixed.
- **Fallback:** with no usable cache directory, a failed compile, or
  `KERN_NO_RUNTIME_CACHE` set, the source is compiled with the program
  as before.

The program and the runtime were already separate translation units, so
the linked code is the same. btree's disassembly is identical and its
output unchanged.

Two earlier designs were reviewed and set aside. The first checked the
headers the compile read (`-MD`) and the paths that would shadow them.
That missed `__has_include` probes and quoted includes next to a system
header. The second compared the job listing (`-###`), which misses
header content. The preprocessed fingerprint covers both, and its code
is the simplest of the three.

## Effect

Interleaved builds, 5 or 3 of each:

| build | before | with the cache (after the one miss) |
|---|---|---|
| btree, release | 312–331 ms | 247–256 ms (miss: 507 ms) |
| arena_ast, debug | 255–256 ms | 207–209 ms (miss: 312 ms) |

A build is about 20% faster. `kern test` and the test suite's native
builds take the same path.

**Limitation.** Old objects are not pruned; each is 25–100 KB.

## Not changed: the proof pass

`BenchmarkProveLexer` measures 1.9 ms per run (14,000 allocations) and
`BenchmarkProveSequentialGuards` 2.7 ms (16,200), against the gate of
twice the pre-join baseline (0.5 ms and 1.0 ms,
`docs/reports/prove-guards.md`). Half of the pass is the meet. A
persistent map could get under the gate, but that is a rewrite of the
state for under 1% of a build, measured against the 60 ms the runtime
cache saves. It stays the next lever, not taken here.
