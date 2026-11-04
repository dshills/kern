# Plan: Map reservation and MapIn

Implements `specs/map-in/SPEC.md` v0.2 in two phases and one commit each.
Phase 1 adds `Map`'s reservation. Phase 2 adds `MapIn`. Both edit
`compiler/resolve/prelude/map.kern`, and `std/map.kern` is copied from
it after every edit; the mirror test fails if the two differ.

## Phase 1: `Map.reserve` and `Map.try_reserve`

**Goals.** Spec §1, R1, and R6 and R7 for `Map`.

**Files.**
- `compiler/resolve/prelude/map.kern` (then `std/map.kern`):
  - `reserve_room(cap, used, n) -> bool`: `(used + n) * 4 < cap * 3`,
    with `checked_add` and `checked_mul`; an overflow means no room.
  - `reserve_bits(from, count, n) -> Option[u32]`: the smallest
    `b` in `from..=58` with `(count + n) * 4 < 3 * 2^b`; `None` past 58
    bits or on overflow.
  - `Map.reserve(n)`: returns when there is room; otherwise
    `rebuild(b)`, with `trap_kind(oom)` when `reserve_bits` is `None`.
    `rebuild` already traps `oom` through `with_bits` when an
    allocation fails.
  - `Map.try_reserve(n)`: the same, with `try_rebuild(b)` and
    `Err(OutOfMemory)`. `try_rebuild` is already transactional: on
    `Err`, nothing has changed.
  - `Map.find`'s comment: a slot stays valid until the next insert,
    remove, `reserve` or `try_reserve`.
- `tests/run/map_reserve.kern` and `.out` (`//! test-hooks: true`): R1's
  cases.
  - The guarantee is shown by arming `testing.fail_next_alloc()` before
    each of 40 inserts after `try_reserve(40)`. Any rebuild would trap,
    because `insert` is the convenience form.
  - "Room, nothing done" is shown by a later allocating `try_insert`
    failing, which proves the hook was still armed.
  - `reserve` and `try_reserve` are compared by printing both maps'
    slot order after the same inserts.

**Key decisions.**
- **A rebuild's size never shrinks** (`b >= bits`), so `reserve(0)` and
  small requests never move entries out of a larger table.
- **The cases that do nothing are decided before any allocation,** so
  a reserve with room makes no call. The fixture's armed hook proves it.

**Acceptance.**
- R1's fixture output is identical in the interpreter and natively, at
  debug and release.
- `map_try`, `map_try_oom`, `std_map` and `std_map_borrow` are
  unchanged.

**Risks.** `1usize << b` is computed only for `b <= 58`, so it cannot
overflow, and `3 * 2^58` fits in `usize`.

## Phase 2: `MapIn[K, V, A: Allocator]`

**Goals.** Spec §2, R2 to R8.

**Files.**
- `compiler/resolve/prelude/map.kern` (then `std/map.kern`):
  - `struct MapIn[K, V, A: Allocator] nocopy { states: RawPtr[u8];
    keys: RawPtr[K]; vals: RawPtr[V]; cap; count; used; bits; a: A }`.
  - `probe_in[K: Hashable](states, keys, bits, key: view[K]) -> usize`:
    `Map.slot`'s probe over raw buffers, unsafe with a contract (the
    buffers address `2^bits` slots, full slots hold initialized keys, and
    there is an empty slot).
  - `impl[K, V, A: Allocator] MapIn[K, V, A]` with:
    - `new_in`, `len`, `allocator`;
    - the private helpers `state(i)`, `key_ptr(i)` and `val_ptr(i)`;
    - `try_rebuild(bits)`. It returns `Err` when `bits > 58` or a byte
      size overflows (`elems_bytes`). It allocates states, keys and
      values in that order, freeing the granted ones in reverse on a
      refusal. It then zeroes the states and moves the live entries in
      slot order (`ptr_take`, `probe_in` on the new table, `ptr_store`).
      Last, it frees the old three allocations with their own sizes and
      alignments, and sets `used = count`.
    - `try_reserve` with phase 1's helpers, starting from 4 bits when
      there is no table;
    - `try_insert` (`Map.try_insert`'s rule, with 4 bits when there is
      no table; on `Err`, `drop(key)` then `drop(val)`) and `place`;
    - `contains`, `get`, `at`, `find`, `key_at`, `value_at`,
      `value_at_mut`, `remove`, each with an empty-table case first;
    - `each`, `each_mut`, and the cursor protocol (`cursor_first`,
      `cursor_next`, `cursor_end`, `cursor_entry`, `cursor_entry_mut`
      through `ptr_view2` and `ptr_view_edit`);
    - `release`, and `destructor(edit self) { self.release() }`.
  - Every method that reads raw memory has a contract, as `PoolIn`'s do.
- `tests/run/map_in.kern` and `.out`: R2 and R3. It copies the `Budget`
  allocator of `arenain_poolin.kern`, and the refusals are set through
  its byte budget:
  - a budget that covers the states but not the keys;
  - then the keys but not the values;
  - then nothing.

  `Tok`-style keys and values print their destruction.
- `tests/run/map_in_diff.kern` and `.out`: R4.
  - An LCG drives 2,000 operations over 300 keys, about 2/3 inserts and
    1/3 removes.
  - After each step it compares the returns and `len`, trapping on a
    difference.
  - At the end it compares the slot order of both maps with
    `cursor_first`/`cursor_next` and prints `equal` and a checksum.
- `tests/freestanding/map_in.kern` and its expected output: R5's
  freestanding run. It uses a counting allocator over the freestanding
  `SystemAllocator`, and its expected output is written in the existing
  freestanding fixtures' format.
- Effects (R6): a test in `cmd/kern` runs `show-effects` on a fixture
  instantiating `MapIn[u64, u64, Budget]` and `Map[u64, u64]`. It
  asserts that no `MapIn` method lists `oom`, that `Map.try_reserve`
  does not, and that `Map.reserve` does. It follows the existing
  effect-kind test in `specs/effect-trap-kinds` (the one asserting that
  no `try_` form lists `oom`), extended there if that test enumerates
  methods.
- Documents:
  - `docs/ownership.md`'s fallible-forms paragraph and rule 5.6.5;
  - the alloc-containers and effect-trap-kinds reports' open items;
  - the regenerated inventory (the corpus count grows);
  - `docs/reports/map-in.md`.

**Key decisions.**
- **Three allocations per table,** as §2 specifies. A failure part way
  through frees the granted ones in reverse order.
- **`MapIn` has no `Hashable` bound on the type,** only on the methods
  that hash. This matches `Map`'s `where K: Hashable`.
- **R4's comparison makes `MapIn`'s slot order a tested property,** not
  just a claim. It holds because both maps use the same probe, growth
  rule and rebuild order.

**Acceptance.**
- R2 to R5:
  - the fixtures pass in both engines at both levels;
  - the freestanding fixture passes in the freestanding emulation, with
    no `ENV_HOSTED_ONLY`;
  - `Budget` reports `live 0 bytes 0` at every end.
- R6: the effects test passes.
- R7:
  - the existing map fixtures are unchanged;
  - the agent-suite `kern check` comparison (exit status, codes, spans)
    against the baseline 103e5e3 shows no difference.
- The full gates (`go build`, `gofmt`, `golangci-lint`, `go test ./...`)
  are clean.

**Risks.**
- **Generic instantiation of an unsafe-heavy generic type over three
  parameters** may expose compiler gaps; `PoolIn` and `ArenaIn` are the
  precedent. Any compiler defect found is fixed with its own fixture, or
  recorded in the report if it falls outside this change.
- **A `hash` that traps during a rebuild** leaves entries half moved, as
  it does in `Map` today. This is out of scope (spec §1, user code), and
  recorded in the report.
