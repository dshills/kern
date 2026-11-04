# Plan: `ArenaIn` and `PoolIn`

Implements `specs/alloc-containers-more/SPEC.md` v0.2 in one phase and
one commit.

## Phase 1

**Goals.** R1 to R5.

**Files.**
- `compiler/resolve/prelude/core.kern` and `std/core.kern` (identical),
  before `Shared`:
  - **`ArenaIn`.** The fields are `Arena`'s plus `a: A`. `try_grow`
    allocates double the buffer through `a`, moves the live elements
    with `ptr_copy`, and frees the old buffer. `try_alloc` destroys `v`
    on `Err`. `check`, `get` and `at` are `Arena`'s. `release` frees
    through `a` and renews the identity.
  - **`PoolIn`.** The fields are `Pool`'s plus `a: A`, with the same
    slot layout helpers. `try_grow` allocates the free list, then the
    slot buffer; if the second is refused, it frees the first. It then
    moves the initialized slots and the free list and frees the old
    buffers. `try_alloc`, `check`, `get`, `at`, `free` and `release` are
    `Pool`'s over `a`.
  - **Hoisting.** Method reads of `self` (`slot_align`, `stride`) are
    hoisted out of calls that edit `self.a`. The checker rejects such a
    call otherwise (5.4.4).
- Fixtures:
  - `tests/run/arenain_poolin.kern` (R1, R3, and R2's reuse order),
    with `.out`;
  - `tests/trap/poolin_stale.kern` (R2's generation trap).
- Documents: `docs/reports/alloc-containers-more.md`, and a note in the
  open items of `docs/reports/alloc-containers.md`.

**Key decisions.**
- **Handles are shared.** The containers issue `Arena`'s and `Pool`'s
  handle types, so code that holds handles is generic over the
  container's allocator. The debug identity tag keeps a handle from
  being used with another container.
- **Only the try forms.** These containers are fallible throughout, as
  `VecIn` is, so there are no trapping forms.

**Acceptance.**
- The fixtures pass in the interpreter and natively at debug and
  release.
- The existing container fixtures, including `vecin_*`, pass unchanged.
- `go test ./...`, `golangci-lint run ./...` and `gofmt -l` are clean.

**Risks.**
- **Duplication.** `PoolIn` repeats `Pool`'s slot logic, so a fix to one
  must be made in both. The report says so.
