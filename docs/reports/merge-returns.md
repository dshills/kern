# Merging returns: arena_ast against C and Rust

**Date:** 2026-10-31. **Spec:** `specs/merge-returns/SPEC.md`. **Host:**
Apple silicon (arm64, macOS), busy throughout. That is why cycles and
instructions retired (`/usr/bin/time -l`) carry the comparison, with
interleaved runs and medians. Timed `ns` is given for the headline only.

## The gap

arena_ast had the worst release throughput of the suite against its
counterparts: 0.87 of Rust's in `bench/REPORT.md`. Kern retired fewer
instructions than Rust (106M against 118M) but took more cycles (38.0M
against 34.6M).

Timing the build and the evaluation separately located the difference.
These scratch copies of the two programs are not committed:

| phase | kern | Rust |
|---|---|---|
| build (1M allocations) | 4.8 ms | 3.7 ms |
| eval (recursive walk) | 3.4 ms | 3.5 ms |

The machine code of `eval` is the same in both, apart from kern's
overflow checks. Things ruled out in `build`:
- **Layout:** `Expr` is 16 bytes and `ArenaId` 4 bytes, as in Rust.
- **Growth:** both double through `realloc`, with the same page faults
  and RSS.
- **Clock:** the 8,192 clock reads of the sampler cost about 0.1 ms.
- **Load pairing:** the `len`/`cap` pair loaded by one `ldp` after an
  8-byte store. Compiling without the pairing pass changed nothing.

What remained was the source's shape:

```
if (b.x >> 63u32) != 0u64 { return alloc(b, Add(left, right)) }
return alloc(b, Mul(left, right))
```

The user inliner copied `alloc` into both returns. The machine code
therefore branched on the top bit of the generator, a value no predictor
can learn, on each of the 500K internal nodes. Rust's program builds the
node first and calls `alloc` once, and LLVM selects the tag without a
branch (`cinc`).

These checks confirmed it:
- A scratch copy of the kern program written that way built in 3.1 ms.
- Keeping `alloc` out of kern's inliner did not help (5.2 ms). LLVM
  leaves the two calls apart and keeps the branch.

So the merge has to happen in kern's IR before the inliner runs. The
benchmark source stays as it is (bench/PROTOCOL.md).

## The pass

`opt.MergeReturns` merges ret blocks that differ only in a union tag or
a constant. The differing value reaches one shared block through a Copy
temporary, which LLVM turns into a select. Every position a trap reports
must agree between the merged blocks. The pass runs only where the
program runs, as the library inliner does. In arena_ast's `build` the
Add and Mul returns become one, and the one copy of `alloc` stores a
selected tag.

## Effect

Thirteen workloads, release, built by HEAD (7637fe4) and with the pass,
9 interleaved runs each:

| workload | instructions | cycles | text bytes |
|---|---|---|---|
| arena_ast | 1.0284 | 0.8463 | 14836 → 14728 |

The other twelve programs compile to the same machine code (an empty
diff of their disassembly). Their measured ratios (0.95 to 1.09 in
cycles) are the host's noise.

The arena_ast headline, with 15 interleaved runs of each binary and
median timed `ns`:

| binary | ns | cycles | throughput vs Rust | vs C |
|---|---|---|---|---|
| kern, HEAD | 8,090,375 | 37.96M | 0.875 | 1.045 |
| kern, merged | 6,509,166 | 32.08M | 1.088 | 1.299 |
| Rust | 7,082,792 | 34.56M | 1 | |
| C (`clang -O2`) | 8,452,625 | 38.64M | | 1 |

arena_ast now meets the 10% target against both counterparts on this
host. The committed `bench/REPORT.md` changes only with the next full
harness run on a quiet machine.

## Phase review (2026-10-31, later)

- **realitycheck** (gpt-6.1-sol, the spec as its own plan, against
  `compiler/opt`): PARTIALLY_ALIGNED, score 100, no drift, no
  violations. Its items read UNCLEAR because it sees symbols, not
  function bodies.
- **verifier analyze** (offline): three medium, low-confidence (0.1)
  property-test recommendations for `MergeReturns`. Added in answer:
  - `TestMergeReturnsGenerated`: 40 seeded programs whose functions end
    in two or three returns differing in a constant or a union tag,
    under random guards, with callees that can overflow. The
    interpreter's output and trap record agree with and without the
    pass, and the generator must produce both merges and traps.
  - `TestExecOptions`: the exec pipelines' option sets.

  verifier's offline matching still lists the recommendations; it does
  not tie tests to targets whose names differ.
