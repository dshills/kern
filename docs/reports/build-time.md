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

## The proof pass

`BenchmarkProveLexer` measures 1.8 ms per run and
`BenchmarkProveSequentialGuards` 2.6 ms, against the gate of twice the
pre-join baseline (0.5 ms and 1.0 ms, `docs/reports/prove-guards.md`).
The pass is under 1% of a build.

**Measuring it on this host.** The benchmarks' `ns/op` is wall time,
which a loaded host inflates. Go's CPU profiles on macOS put most samples
in `madvise` and the scheduler, so they can't attribute the pass. Two
methods do work:
- **Instructions retired** for 3,000 runs on one lowered module
  (scratch test), with `GOGC=off` to separate the collector. The pass
  costs 23.5M instructions per run on the lexer, and the collector adds
  about 10% on top.
- **The system's `sample` tool** on the same loop. It attributes nearly
  all of the pass's time to `meet`, in map lookups and iteration. Each
  key either branch touched is looked up through both chains of fact
  layers (up to the depth limit) and through their common ancestor.

**Changed (2026-10-31, second batch):**
- `meet`'s five key sets are kept on the prover and cleared between
  meets, instead of allocated per meet.
- The fact-chain depth limit (`maxDepth`) is 32 instead of 16.
  Instructions per run by limit:

  | limit | 2 | 4 | 8 | 16 | 32 | 64 |
  |---|---|---|---|---|---|---|
  | lexer | 25.2M | 26.9M | 25.9M | 23.5M | 22.5M | 22.5M |
  | guards | 113.5M | 71.2M | 50.9M | 40.7M | 40.0M | 46.1M |

Both are representation only: the facts, and so every proof, are the
same.

| benchmark | before | after |
|---|---|---|
| lexer | 1.81 ms, 1.28 MB, 14.0K allocs | 1.72 ms, 1.06 MB, 12.6K allocs |
| guards | 2.57 ms, 2.21 MB, 16.2K allocs | 2.31 ms, 1.53 MB, 15.5K allocs |

**Not reached here:** the gate needed about 3.5x less work, and no
representation tweak measured here gave more than a few percent. The
persistent-map state that followed (`docs/reports/prove-state.md`,
`specs/prove-state`) took the lexer to the gate and the guards program
to within 30% of it.
