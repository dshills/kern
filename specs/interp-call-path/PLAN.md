# Plan: interpreter allocation on the call path

Implements `specs/interp-call-path/SPEC.md` v0.2 in one phase and one
commit.

## Phase 1

**Files.**
- `compiler/interp/interp.go`:
  - `Interp.argBuf`, reset in `child()`;
  - `frame.retTo` and `frame.retIDs`;
  - `smallFrame` and `smallSlots = 14`;
  - `callAt`, which calls `callInto`, which calls `callWith(f, fi, …)`:
    - `fi` is looked up when it is nil;
    - small frames come from one allocation;
    - `retTo` and `retIDs` are set from the caller.
- `compiler/interp/compile.go`:
  - `compileCall`: for a callee that is not an intrinsic, the arguments
    go through `argBuf`, the callee's `funcInfo` is held in an
    `atomic.Pointer`, and the call is `callWith` with the caller's frame
    and destination IDs;
  - the compiled `termRet` writes into `retTo` when it is set.

**Contracts.**
- **Eligibility.** `compileCall` returns early for a call through a
  function value (`i.Fn != nil`), and the intrinsic branch comes first.
  The new path therefore applies only to a statically known kern
  function that is not an intrinsic, whose `funcInfo` cannot change.
- **Arguments.** `callWith` reads `args` only in its parameter loop.
  That loop runs after the frame is set up and before the body, and
  nothing between the call op building the buffer and that loop runs
  kern code or makes another call; a stack trap ends the thread. The
  slice is not kept.
- **Returns.** `callWith` returns `nil` when the compiled return has
  written the values into `retTo`. In `genericExec` mode the generic
  loop ignores `retTo` and returns a slice. Calls without `retTo` (every
  other caller of `callAt`) get a slice as before. The call op always
  passes the result to `store`, which writes a slice into the
  destinations and does nothing for `nil`.

**Acceptance.**
- `go test ./compiler/interp ./tests` passes (R1), and so does
  `go test -race ./compiler/interp` (R2).
- **fib(29):** instructions retired against the previous commit, three
  runs each, with the output compared (R3).
- **Bench:** five `bench/kern` programs, measured by instructions only,
  because their output includes timings (R3).
- `go test ./...`, `golangci-lint run ./...` and `gofmt -l` are clean.

**Risks.**
- **The argument buffer.** A callee that read its argument slice after
  starting another call would see it overwritten. `callAt` copies the
  arguments first, and the buffer is used only for callees that are not
  intrinsics, which might keep the slice.
- **Returns into the caller.** A return written into the caller's frame
  must happen before the caller reads its destination slots. It does,
  since the call op stores right after the call returns.
