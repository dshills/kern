# Inlining library bodies in kern's IR

**Date:** 2026-10-31. **Spec:** `specs/inline-library/SPEC.md`.
**Finishes:** PLAN.md 6.6, first deferred item ("library callees").

## What changed

- **A pass after the proof pass.** `opt.InlineLibrary` copies small
  library bodies into their callers, callees first. For example,
  `Vec.push` goes into `main`, with its cold `Vec.grow` left a call.
- **Positions in user code.** Every instruction of a copy into a user
  function takes the call site's position, and the copy's blocks take
  the call's file. So a trap in the copy reports the user-level call,
  as the library function's own trap did through its span parameters.
- **Where it runs.** Only release execution uses it:
  `opt.ExecOptionsFor` in `kern run`, native builds and `kern test`. The
  `show-*` commands and `emit-ir` keep `opt.OptionsFor`, so their reports
  are unchanged by construction.
  - Those reports depend on library calls staying calls: the allocation
    table, `Pool`'s generation checks, clone detection and the effect
    summaries.
  - Labels come from the proof pass, which ran before the copies, so what
    `show-bounds-checks` reports is still what runs.
- **Shared cold list.** `ir.ColdLibrary` is the backend's former
  `coldLibrary` list, now shared with the pass.

Not copied: functions that return an access (`Vec.get`, `as_view`), as
for user functions, since the returned projection is tied to the
caller's `access_end`.

## Effect

- **Interpreter.** A release run of a container-heavy program (200,000
  `Vec.push`, 20,000 `Map.insert`, 50,000 `String.push`) took 0.69 s of
  user time against 0.76 s with the pass off. That is about 9%, the same
  in each of five interleaved rounds, with load near 18.
- **Native.** clang already inlined these bodies, so the native code is
  not expected to change materially. The harness re-run that follows
  this change measures it.

## Verification

- **`TestInlineLibrary`:** `Vec.new`, `Vec.push`, `Vec.len` and `expect`
  are copied into `main`, `Vec.grow` stays a call, and the copied trap is
  at the `expect` call's span. **`TestLibraryInlinerOptions`** pins where
  the pass runs.
- **End to end:** a failing `expect` gives the same record (kind, file,
  span) in the interpreter and natively, in debug and release. This holds
  in one file and in an imported module, where the record names
  `lib/lib.kern`.
- **The full suite** passes. Native release builds of every fixture run
  the copies and match the interpreter's debug output and records.
- **Agent suites.** The dry runs of all four exit 0. The fourth suite's
  natural-solution counts are unchanged.
- **Gates.** `golangci-lint run ./...` reports 0 issues. `go test ./...`
  passes except the ThreadSanitizer fixture `race_raw_ptr`, whose
  symbolizer hangs on this host (it fails the same way on 7e5f638).
