# Interpreter support for `out` parameters

**Date:** 2026-10-27. **Scope:** `compiler/interp/interp.go` and four new
executable fixtures under `tests/run/`. **Starting point:** `d5d1bdc`.

## Problem

Spec §5.2 `out` parameters (`func fill(o: out Cell) { o.x = 7u64 }` called
as `fill(out v)`) initialize the caller's storage. The lowering passes an
edit access to the caller's local and marks the callee's parameter local
`ast.ConvOut`; the LLVM backend treats that local's slot as a pointer and
dereferences it for every place rooted there. The checker enforces the
rules, and the positive fixtures (`nested_out_fields`, the `init_*` and
`out_*` twins) accept them, but every one of those is `expect: accept` and
is never executed. The interpreter path had never run an out call.

Two things were wrong in `(*Interp).resolve` and `callAt`:

- **The out slot was not dereferenced.** `resolve` unwrapped the `*Access`
  held in a parameter slot only for `ConvView` and `ConvEdit` locals. A
  place rooted at a `ConvOut` local therefore resolved to the slot holding
  the access itself: a field write panicked with "interface conversion:
  interp.Value is *interp.Access, not *interp.Struct", and a whole-value
  write (`o = 7u64`) overwrote the access in the callee's slot, so the
  caller's local stayed nil and the next intrinsic that read it panicked
  with "interp.Value is nil, not interp.Int".
- **Uninitialized storage had no value to write into.** Natively the
  caller's `var v: Cell` is an alloca whose fields the callee stores into.
  In the interpreter an uninitialized local is a nil slot, so even with the
  access dereferenced a field write had no `*Struct` to descend into.

## Changes

- `resolve` dereferences the access for `ConvOut` parameter locals exactly
  as for view and edit parameters, so loads, stores, moves, drops, nested
  `AccessBegin` (forwarding `out o` to another out call) and `Len` all reach
  the caller's storage.
- `callAt` materializes zeroed storage (`zeroOf(p.Type)`) through an out
  parameter's access when the referent is nil at entry. The checker already
  guarantees every field is written before any read and that nothing is
  destroyed until the storage is initialized, so the zero fill is not
  observable; it only gives field writes a place to land, as the alloca does
  natively. A whole-value store replaces it without a `Drop` (the checker
  records no drop for an uninitialized out root), so no destructor runs on
  the zero value.
- `Init` stays a no-op in the interpreter: it is static bookkeeping, and
  the lowering emits it after infallible out calls only. The `Result`
  returning form needs nothing extra: the Ok path initializes through the
  same access, and on the Err path the checker's `OutFieldDrops` destroy
  whatever fields were already written.

## Fixtures (`tests/run`, run in both engines by the harness)

- `out_struct`: field-by-field initialization of a struct, with a nested
  struct, a field read through the out parameter after it is written, and
  an array field; a second struct type initialized by a separate call.
- `out_scalar`: a whole-value write to a `u64` out parameter, two out
  parameters in one call, and an out parameter forwarded to another out
  call.
- `out_result`: a fallible out call. `init_p` fails before writing anything
  and `init_late` fails after writing one field of a struct that holds a
  value with a destructor, so the output shows the partial field being
  destroyed on the Err path and the whole value on the Ok path, both via
  `match` and via `?`.
- `out_destructor`: an out parameter of a type with a destructor and a
  struct of two of them, initialized once and destroyed once each at scope
  end or on reassignment.

Each fixture was also built natively with `kern build` and its output
compared to the interpreter's by hand before being added; the harness
repeats that comparison for every `tests/run` fixture.

## Verification

`golangci-lint run ./...` clean, `go test ./...` green, Prism
(`gpt-6.1-sol`) on the staged diff: no high or medium findings.
