# Interpreter: allocation on the call path

**Date:** 2026-11-04. **Spec:** `specs/interp-call-path/SPEC.md`
(speccritic VALID_WITH_GAPS, no critical finding). **Plan:**
`specs/interp-call-path/PLAN.md` (plancritic
EXECUTABLE_WITH_CLARIFICATIONS, no critical finding; its two contract
warnings are answered by the plan's Contracts section). **Host:** Apple
M4 Pro, busy. Instructions retired (`/usr/bin/time -l`) carry the
comparison; wall-clock timing is left for a quiet host.

## The change

Before this change, a direct call made four allocations: the argument
slice, the frame, the frame's slots and the return slice. It also looked
up the callee's `funcInfo` in the shared map. Now:
- **Arguments** go through a per-thread buffer, `Interp.argBuf`.
  `callWith` copies them into the callee's locals before anything else
  can make a call, so one buffer serves nested and recursive calls. A
  child thread's copy of the interpreter starts with no buffer of its
  own (`child()`).
- **Frame and slots.** An activation with at most 14 locals and values
  (`smallSlots`) gets both in one `smallFrame` allocation. Larger
  activations allocate as before.
- **Returns.** A direct call passes its own frame and destination IDs
  (`frame.retTo`, `frame.retIDs`). The compiled return then writes into
  them and returns no slice. The generic engine (`genericExec`) still
  returns a slice, and the call op stores it.
- **`funcInfo`.** Each direct call op keeps its callee's `funcInfo` in an
  atomic pointer once it is known.

Only calls whose callee is a known kern function that is not an
intrinsic take this path. Calls through function values, intrinsics and
calls by name (`callNamed`) keep theirs.

## Measurements

The spec's workload is `fib(29)`, about 1.6 million direct calls. Three
runs of each compiler, in the same session:

| compiler | instructions retired | median |
|---|---|---|
| b1e9ce0 | 4,740,114,898 · 4,700,049,460 · 4,699,141,637 | 4,700,049,460 |
| this change | 3,185,114,577 · 3,224,010,472 · 3,283,171,011 | 3,224,010,472 |

That is −31.4%, against R3's target of at least −20%. Both print
`514229`.

Five `bench/kern` programs, one run each under `kern run --opt=release`:

| program | b1e9ce0 | this change | change |
|---|---|---|---|
| arena_ast | 60,926,210,566 | 59,527,289,414 | −2.3% |
| btree | 4,974,031,619,161 | 4,923,577,887,798 | −1.0% |
| hash_table | 756,187,981,453 | 749,382,763,382 | −0.9% |
| vector | 320,480,346,981 | 313,244,697,096 | −2.3% |
| packet_parser | 4,615,337,569,474 | 4,567,096,304,838 | −1.0% |

arena_ast prints its own timings, so its output differs between any two
runs; its checksum (`3969867084`) is the same for both compilers. The
bench programs spend their time in loops, indexing and intrinsics (`Vec`,
`Map`) rather than in user calls, so the gain there is small.

**Wall time (2026-11-04, later).** The quiet-host timing was run with
the host given over to it, though Spotlight kept the load near 20. It
interleaved 74d339e, the commit just before this change, with
`dd16b89`, which includes it, in five rounds:
- `fib(29)` took 0.20 s and then 0.14 s (medians), with instructions
  retired going from 4.60 G to 3.12 G;
- `arena_ast`, `vector` and `hash_table` were 0.7% to 1.4% faster.

The figures are in `docs/reports/interp-alloc.md`, which also cuts the
allocations listed below.

## What remains

Profiling `fib` puts about 9% of instructions in the garbage collector.
Here is what is still allocated:
- **Integer boxing.** Each `Int` result is boxed into its `Value`
  interface. It was measured and not taken in
  `docs/reports/interp-speed.md`.
- **`copyValue` on loads** of aggregate values.
- **`Access` objects** for every `view` and `edit` argument.
- **Frames over 14 slots**, which still take two allocations.

The argument buffer keeps the last call's argument values reachable until
the next call overwrites them. Clearing it would cost a loop per call,
and whatever it holds is at most one call's arguments.
