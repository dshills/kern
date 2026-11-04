# Interpreter: allocation on the call path

**Version:** 0.2 (2026-11-04). **Review status.** speccritic
(gpt-6.1-sol): VALID_WITH_GAPS, no critical finding; the workload and
the baseline are now pinned. **Scope:** `compiler/interp` (`interp.go`,
`compile.go`); the report `docs/reports/interp-call-path.md`; a note in
`docs/reports/interp-compile.md` ("What remains"). **Language impact:**
none: the interpreter's results are unchanged. **Follows:**
`docs/reports/interp-compile.md`, which names the call path
(`callAt`'s per-call setup) as a remaining cost.

**Glossary.**
- R*n*: requirement *n* of §3.
- *A direct call*: a compiled call op whose callee is a known kern
  function, not an intrinsic and not a call through a function value.
- *Instructions retired*: the count `/usr/bin/time -l` reports for a
  process. It is used because it is stable on a loaded host, and the
  owner reserved wall-clock timing for a quiet one.

## 0. Goal and non-goals

A direct call in the interpreter must allocate less. Today every one
makes four allocations:
- the argument slice;
- the frame;
- the frame's slots;
- the return-value slice.

The call itself also looks the callee's `funcInfo` up in a shared map.

Non-goals:
- boxing integers (measured and not taken, `docs/reports/interp-speed.md`);
- calls through function values;
- intrinsics;
- wall-clock measurement, which is left for a quiet host.

## 1. Design (informative)

- **Arguments.** A per-thread buffer (`Interp.argBuf`) carries a direct
  call's arguments. `callAt` copies them into the callee's locals before
  anything else runs, so one buffer serves every call, recursive ones
  included. A child thread starts with its own.
- **Frame and slots.** An activation with at most 14 locals and values
  gets its frame and slots in one allocation (`smallFrame`).
- **Returns.** A direct call passes its frame and destination slot IDs
  (`retTo`, `retIDs`). The compiled return then writes the values there
  and returns no slice. The generic path (`genericExec`) still returns a
  slice, which the call op stores.
- **`funcInfo`.** A direct call op holds its callee's `funcInfo` once
  known, in an atomic pointer, so later calls skip the map.

## 2. Requirements and acceptance criteria

**R1. The same results.** The interpreter's tests pass, including the
compiled-against-generic comparisons (`TestCompiledMatchesGeneric*`),
and so does every fixture in `tests` in both engines.

**R2. Threads.** `go test -race ./compiler/interp` passes. A child
thread's interpreter copy does not share the parent's argument buffer.

**R3. Fewer instructions.** The workload is this program, run by
`kern run --opt=release`, on the same host and in the same session for
both compilers:

```kern
module t
func fib(n: u64) -> u64 {
    if n < 2u64 { return n }
    return fib(n - 1u64) + fib(n - 2u64)
}
func main() -> i32 {
    print_u64(fib(29u64))
    println()
    return 0i32
}
```

It makes 1.6 million direct calls. Instructions retired, the median of
three runs, must fall by at least 20% against the baseline compiler
built from commit b1e9ce0, and the output (`514229`) must be the same. The report also
records the change on five `bench/kern` programs, whichever way it goes.

**R4. Documents.** The report records the measurements and the costs
left (integer boxing, `copyValue`, `Access` objects), and
`interp-compile.md`'s "What remains" points to it.
