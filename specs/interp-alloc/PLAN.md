# Plan: interpreter allocation in aggregates, library calls and pointer intrinsics

Implements `specs/interp-alloc/SPEC.md` in one phase and one commit. The
four items touch one package and are measured together; each is
measured on its own as well (step 2), so a regression can be traced to
one item.

## Phase 1

**Goals.** R1 to R4.

**Files.**
- `compiler/interp/values.go`: `newStruct(n)` and `newUnion(tag, n)`,
  one allocation for at most four fields, `Fields` capped at `n`. Used by
  `copyValue` and `zeroOf`.
- `compiler/interp/rawmem.go`: `decode` builds tuples, structs and unions
  with them and assigns fields by index; `putInt` and `getInt` use
  `encoding/binary` for sizes 1, 2, 4 and 8.
- `compiler/interp/interp.go`: `exec`'s `MakeStruct` and `MakeUnion` use
  the helpers. `frame.spanAt(pos)` returns the caller's span or `pos`;
  `noSpan` replaces the per-call `&ast.Pos{}`; the generic call passes
  `fr.spanAt(&i.Pos)`. `Interp.intrBuf`, cleared in `child()`.
- `compiler/interp/compile.go`: the library call op passes
  `fr.spanAt(site)`, `site` being the op's own copy of the position. A
  call to a fast intrinsic copies its arguments into `intrBuf` (grown
  to the call's arity when smaller, so there is no maximum), calls it,
  clears the buffer, and stores the result into the call's first
  destination when it has one. A fast intrinsic with no result returns
  nil and the call stores nothing.
- `compiler/interp/alloc_test.go` (new): the unit tests spec §3 lists.
- `compiler/interp/intrinsics.go`: `fastIntrinsic`, its `slow()`
  wrapper and the `fastIntrinsics` table; the twelve entries move there
  from the ordinary table, and `init` registers each wrapped.
  `deepClone` uses the helpers.
- `docs/reports/interp-alloc.md` (new).

**Key decisions.**
- **The frame keeps a span pointer.** Holding the span by value was
  tried first: `fib(29)` then retired about 3% more instructions,
  because the larger frame moved `smallFrame` into the next allocation
  size class. Stable pointers remove the allocation without growing the
  frame.
- **The buffer releases what it held.** After each fast call the buffer
  is cleared, so it keeps no argument alive. A fast intrinsic that traps
  leaves its arguments until the next call, which overwrites them; that
  is at most one call's arguments, as `argBuf` holds.
- **Larger aggregates** (five fields or more) keep the two allocations
  they had: the value and a `Fields` slice of exactly their size.
- **Only twelve intrinsics are fast.** Each was read to confirm that it
  keeps no reference to its arguments and runs no kern code. Spawning
  intrinsics keep their argument slice (a goroutine reads it later), and
  `ptr_init` runs kern code, so neither may be fast.
- **No `Access` reuse,** for the reason spec §3 gives.

**Steps and verification.**
1. Make the changes. `go build`, `go vet` and `gofmt -l` on the package.
2. Measure instructions retired (R3) against a compiler built at
   `dd16b89`: `fib(29)`, `arena_ast` and `vector`, two or more runs each,
   interleaved, after each of the four items. Record the final figures
   with the load.
3. R1 and R2: `go test ./compiler/interp` (the compiled-versus-generic
   tests and the unit tests), then `go test ./...`, whose `tests` package
   runs every fixture in the interpreter and natively. Then the
   two-level `kern run` differential of spec §3 against the `dd16b89`
   compiler. A difference is investigated; one that also appears
   between two runs of the same compiler is nondeterminism and is
   reported as such.
3a. R6: the allocation counts, from a throwaway test (not committed) in
   this tree and in a worktree at `dd16b89`.
4. R4: `git diff --stat` lists only `compiler/interp`, the report and
   this directory.
5. Review: an internal code review of the diff, findings fixed or
   recorded, then the gates (`golangci-lint run ./...`, `gofmt -l`,
   `go test ./...`) and one commit.

**Acceptance.** R3's numbers met, every test passing, lint clean, and
the review's findings handled.

**Risks.**
- **A fast intrinsic that re-enters.** If a later change makes one of
  them run kern code, nested calls would overwrite the shared buffer.
  The type's comment states the rule next to the table.
- **A capped `Fields` slice.** Code that appends to a built aggregate's
  `Fields` would reallocate, and a `slotRef` into the old array would
  then go stale. `decode` was the only such code, and it now assigns by
  index; a search for `Fields = append` in the package finds none.

## Gate policy

Every gate is mandatory. The one named exception is the `race_raw_ptr`
TSan fixture, which times out on this host because the symbolizer
(`atos`) hangs. If `go test ./tests` fails only there, and only by
timing out, the same fixture is run at `dd16b89` with
`go test ./tests -run 'TestFixtures/concurrency/race_raw_ptr' -timeout 40m`.
If that also times out, the commit may proceed, and the report records
both commands and outcomes. Any other failure blocks the commit.
