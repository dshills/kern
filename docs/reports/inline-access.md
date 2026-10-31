# Inlining functions that return a view or edit

**Date:** 2026-10-31. **Spec:** `specs/inline-access/SPEC.md`.

## What changed

Both inliners copied no function that returns an access, because "a
returned projection is tied to the caller's `access_end`". The tie
holds only when the copy cannot define the call's destination. Now:
- **Copied.** A callee whose single `ret` returns an access its own
  instructions defined (an `access_begin` of a field, or a raw-pointer
  view in the library) is copied, and the copy defines the destination.
- **Re-parenting.** The caller's later `access_end` ends the copy. When
  the argument access ends first, as it does after every projection
  call, the validator's existing re-parenting keeps the projection
  derived from the argument's ancestors.
- **Still a call.** A callee with two returns of an access, which would
  need a result local, stays a call. `accessRetOK` (`compiler/opt/inline.go`)
  decides this for both passes.

Returning a parameter itself (`return x`) is also copied: the lowering
returns a fresh reborrow, which the copy can define.

## Effect

- **Library calls.** In release execution, two programs checked after
  both passes:
  - copied: `Vec.get`, `Vec.as_view`, `Map.find`, `Map.key_at` and
    `Pool.get`;
  - still calls: `Map.insert`, `Map.slot`, `Pool.alloc`, `Vec.resize`
    (over the size limit) and the cold `Vec.grow`.
- **Interpreter.** A program reading 100,000 elements five times through
  `Vec.get` and `as_view` took 0.35 s of user time against 0.43 s at the
  previous commit, about 19%, the same in five interleaved rounds (load
  near 35).
- **Labels.** User inlining runs before the proof pass, so labels can
  change. Over `bench/kern/`, `examples/` and `tests/run/` one check
  changes, in release only: `examples/packet_parser/parser.kern`'s slice
  `kt[6usize : 6usize + usize(h.len)]` is now proven. Debug labels are
  unchanged (102 runtime-checked).
- **Suite 4.** The dry run passes, and the natural solutions' counts are
  unchanged (9, 6, 7, 14, 8, 7, 9, 9, 8, 6).
- **Native.** Not measured: the machine was loaded all day. The harness
  re-run is still owed.

## Verification

- **`TestInlineAccessReturn`.** `at` (view) and `at_mut` (edit) are
  copied into `main`, and the output equals the uninlined program's.
- **`TestInlineStaysCall`.** Its access case is now a function returning
  one of two projections, which stays a call. The old case, a single
  projection, is what this change copies.
- **Gates.** `go test ./...` passes; `golangci-lint run ./...` reports 0
  issues.
