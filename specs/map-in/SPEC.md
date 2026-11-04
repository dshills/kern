# Map: reservation and an allocator-generic form

**Version:** 0.3 (2026-11-04): §2 gains the markers, found missing while
implementing (v0.2 was reviewed). **Review status.** speccritic
(gpt-6.1-sol): VALID_WITH_GAPS, no critical finding. v0.1's critical
finding (`find`'s slot validity across a reserve) is answered in §1. The
remaining model warning asks to make §2's storage layout informative.
It stays normative on purpose: R2 checks each allocation's size and
alignment against it, as the other `In` containers' specs do.
**Scope:**
- `std/map.kern` and its identical prelude copy
  `compiler/resolve/prelude/map.kern`: `Map.reserve` and
  `Map.try_reserve`, and the type `MapIn[K, V, A: Allocator]`;
- fixtures under `tests/run` and `tests/freestanding`;
- `docs/ownership.md` rule 5.6.5 and the fallible-forms paragraph;
- the open items of `docs/reports/alloc-containers.md` and
  `docs/reports/effect-trap-kinds.md`;
- the generated inventory in `docs/reports/freestanding-availability.md`;
- the report `docs/reports/map-in.md`.

**Language impact:** library additions written in kern. The resolver's
marker rules gain `MapIn`, `ArenaIn` and `PoolIn` (§2, Markers); the
runtimes and the wire schema are unchanged. **Follows:**
- `specs/alloc-containers/SPEC.md` and
  `specs/alloc-containers-more/SPEC.md`, which leave "an
  allocator-generic `Map`" open;
- `specs/effect-trap-kinds/SPEC.md`, which leaves `Map.try_reserve`
  open.

**Glossary.**
- R*n*: requirement *n* of §3.
- *An absent insert*: an insert of a key the map does not hold.
- *The table*: a map's slots. There are 2^bits of them (*cap*); *count*
  is the live entries and *used* is the slots that are not empty (live
  or deleted).
- *The growth rule*: before an insert, a map whose
  `(used + 1) * 4 >= cap * 3` is rebuilt, dropping tombstones. The new
  table is twice the size when `(count + 1) * 2 > cap`, and the same
  size otherwise (`std/map.kern`, `Map.insert`).
- *Slot order*: the order of `each` and of `for k, v in m`, which is
  increasing slot index.

## 0. Goal and non-goals

1. A `Map` user must be able to reserve room for *n* entries so that the
   next *n* absent inserts cannot fail or trap `oom`. `Vec` and `String`
   already offer this (rule 5.6.4, "reserve, then append").
2. A map must be able to draw its storage from a caller's allocator,
   like `VecIn`, `BoxIn`, `ArenaIn` and `PoolIn`. A freestanding program
   with its own allocator then gets a hash map.

Non-goals:
- **Changes to `Map`'s behaviour.** Hashing, probing, the growth rule,
  slot order and every existing method are unchanged. The one exception
  is §1's amendment of the slot-validity contract, which the new
  `reserve` forms need.
- **An allocator-generic `String`,** or `String` keys with a `MapIn`'s
  allocator. A `MapIn` key may be a `String`, which allocates from the
  system allocator as always.
- **Sharing one allocator between containers** (a separate open item).
- **Infallible forms on `MapIn`.** Like the other `In` containers it is
  fallible throughout. No method traps `oom`.
- **A `realloc` in `Allocator`** (the owner's decision, left open).

## 1. `Map.reserve` and `Map.try_reserve` (normative)

```kern
func reserve(edit self, n: usize) where K: Hashable
func try_reserve(edit self, n: usize) -> Result[(), AllocErr] where K: Hashable
```

- **Nothing to do.** When `(used + n) * 4 < cap * 3`, with checked
  arithmetic, the map is unchanged and no allocation is made.
- **Otherwise the table is rebuilt.** Its new `bits` is the smallest
  `b >= bits` with `(count + n) * 4 < 3 * 2^b`, and live entries move as
  in a growth rebuild.
- **Failure.** A sum or product past `usize`, a `b` above 58, or a
  failed allocation is reported as follows, and in every case the map is
  unchanged:
  - `try_reserve` returns `Err(OutOfMemory)`;
  - `reserve` traps `oom`.
- **The guarantee.** After either returns successfully, the next *n*
  absent inserts (`insert` or `try_insert`) make no allocation call and
  never rebuild. This holds with any number of removes and present
  inserts in between, because neither increases `used`.
- **Slot validity (amended).** `find`'s slot stays valid until the next
  insert, remove, `reserve` or `try_reserve` of the map. Today's
  contract names insert and remove only. A reserve that returns early
  moves nothing, but callers may not rely on telling the two apart.
- **User code is outside the guarantees.** "No allocation call" and "no
  `oom` trap", here and in §2, describe the map's own code. A key's
  `hash` or `eq` and a key's or value's destructor may allocate or trap,
  and run when the map calls them. A rebuild calls `hash` on every live
  key. R6's effects requirement is read for `K` and `V` types whose
  `hash`, `eq` and destructors allocate nothing, as in its fixtures'
  `u64` keys and values.

## 2. `MapIn[K, V, A: Allocator]` (normative)

`MapIn` holds its allocator inline and owns it. It allocates only
through it, and frees each allocation once, with that allocation's
pointer, size and alignment (rule 5.6.5).

| Method | Behaviour |
|---|---|
| `new_in(a: own A) -> MapIn[K, V, A]` | an empty map with no table; no allocation call |
| `len(view self) -> usize` | the live entries |
| `allocator(view self) -> view[A]` | the allocator |
| `try_reserve(edit self, n: usize) -> Result[(), AllocErr]` | §1's rule; a map with no table gets the smallest `b >= 4`, and `n = 0` does nothing |
| `try_insert(edit self, key: own K, val: own V) -> Result[Option[V], AllocErr]` | `Map.try_insert`; a map with no table gets `bits = 4` first |
| `contains`, `get`, `at`, `find`, `key_at`, `value_at`, `value_at_mut`, `remove` | as `Map`'s, with the same traps; with no table, `contains` is false, `find` is `None`, `remove` is `None`, and `get`, `at` and the slot accessors trap as on an absent key; a slot from `find` stays valid until the next `try_insert`, `remove`, `try_reserve` or `release` |
| `each`, `each_mut`, and the cursor protocol (`for k, v in m`) | as `Map`'s, in slot order |
| `release(edit self)` | destroys every live entry (key, then value, in slot order) and frees the table, leaving an empty map with no table that still holds `a` |
| destructor | `release`, then `a` is destroyed as a field |

Every method that hashes or compares keys has `where K: Hashable`.

**Storage.** A table is three allocations from `a`, each with its own
size and alignment:
- the states, `cap` bytes at alignment 1;
- the keys, `cap * size_of[K]()` bytes at `align_of[K]()`;
- the values, `cap * size_of[V]()` bytes at `align_of[V]()`.

A slot's key and value are initialized exactly when its state is full.

**Rebuilds are transactional.**
- The new table's three allocations are requested in the order states,
  keys, values.
- If one is refused, the ones already granted are freed in reverse
  order, `Err` is returned, and the map is unchanged.
- Once all three exist, the live entries are moved in slot order with
  no further allocation call. Then the old table's three allocations are
  freed.

**Markers.** `MapIn` is never `Copy`. It is `Send` (`Share`) iff `K`, `V`
and `A` all are, and `Interior` iff any is, as `VecIn` is for its
element and allocator. A raw-pointer field alone would make it neither
`Send` nor `Share`, so the resolver states the rule, as it does for the
other containers. `ArenaIn` and `PoolIn` had been left out of that rule,
and are given `VecIn`'s.

**Equivalence.** For the same sequence of `try_insert` and `remove`
calls, every one succeeding, a `MapIn` and a `Map` have the same `len`,
the same entries and the same slot order. A `Map` starts with 16 slots
and a `MapIn` with none, but a `MapIn`'s first insert creates the same
16-slot table.

## 3. Requirements and acceptance criteria

**R1. `Map.reserve`.** A run fixture (`tests/run/map_reserve.kern`, with
`--test-hooks`) shows:
- after `try_reserve(n)`, `n` absent inserts succeed with
  `testing.fail_next_alloc()` armed before each, for `n` crossing at
  least two growths (n = 40 from an empty map);
- `try_reserve` on a map that has room, with the hook armed, returns
  `Ok` and the hook is still armed (the next allocating insert fails);
- `try_reserve` with the hook armed on a map that must rebuild returns
  `Err(OutOfMemory)`, and the map's `len` and entries are unchanged;
- `try_reserve(2^62)` returns `Err(OutOfMemory)`, and the map is
  unchanged;
- `reserve` leaves the same table as `try_reserve`, shown by both maps
  printing the same slot order after the same inserts.

**R2. `MapIn` through its allocator.** A run fixture
(`tests/run/map_in.kern`) uses the counting `Budget` allocator of
`tests/run/arenain_poolin.kern`, which traps on any free that does not
match a live allocation. It shows:
- every table is three allocations with §2's sizes and alignments
  (traced);
- growth through at least three rebuilds;
- removes, including tombstone reuse and a same-size rebuild;
- `release`, and a map dropped while holding entries, end with no live
  allocation (`live 0 bytes 0`);
- key and value destructors run once each: on `remove` the key, with
  the value returned; on `release`, key then value.

**R3. `MapIn` failure.** The same fixture shows:
- a `try_insert` whose rebuild is refused at each of the three
  allocations (the budget set to refuse the states, then the keys, then
  the values). Each returns `Err(OutOfMemory)` after destroying the key,
  then the value. The granted allocations are freed, and `len`, the
  entries and the live allocation count are unchanged;
- a `try_reserve` refused the same way, with the same result;
- `try_reserve(2^62)` returns `Err(OutOfMemory)` with no allocation
  call.

**R4. Equivalence.** A run fixture (`tests/run/map_in_diff.kern`) drives
a `Map[u64, u64]` and a `MapIn[u64, u64, SystemAllocator]` with the same
2,000 pseudo-random inserts and removes, over at most 300 distinct keys.
After every operation it compares the return values and `len`. At the
end it compares the full slot order, and it prints `equal` together
with a checksum.

**R4a. Markers.** A resolver test checks `MapIn`'s `Send`, `Share` and
`Interior` for each of the eight combinations of its arguments having
the marker. The existing `BoxIn`/`VecIn` marker test also covers
`ArenaIn` and `PoolIn`. R4's fixture moves its `MapIn` to a thread.

**R5. Both engines and freestanding.**
- R1, R2 and R4 give identical output in the interpreter and natively,
  at debug and release (the run suite's existing comparison).
- A freestanding fixture (`tests/freestanding/map_in.kern`) runs a
  `MapIn` over a counting allocator that wraps the freestanding
  `SystemAllocator`. It inserts, removes and releases, and must check
  with no `ENV_HOSTED_ONLY`.

**R6. Effects.** For `MapIn[u64, u64, A]` and `Map[u64, u64]`
(`hash`, `eq` and destructors allocate nothing), `show-effects` lists:
- no `oom` trap kind for any of `MapIn`'s methods, nor for
  `Map.try_reserve`. The exceptions are `each` and `each_mut`, which call
  the caller's closure: like the `_with` constructors' summaries, theirs
  cover that code and are not part of this requirement;
- `oom` for `Map.reserve`.

The fixture's `A` is an allocator whose own methods do not trap `oom`.

**R7. Nothing else changes.**
- The existing `std/map` fixtures (`std_map`, `std_map_borrow`,
  `map_try`, `map_try_oom` and the cursor and `each` fixtures) pass
  unchanged.
- The agent suites' starting and reference programs give the same
  `kern check` result as at the baseline, comparing exit status and
  each diagnostic's code and span.

**R8. Documents.**
- `docs/ownership.md` names `Map.reserve`, `Map.try_reserve` and `MapIn`
  where it lists the fallible forms and the `In` containers.
- `Map.find`'s comment in `std/map.kern` states §1's amended
  slot-validity contract.
- The open items in the alloc-containers and effect-trap-kinds reports
  point here.
- `docs/reports/map-in.md` records the fixtures and the effects.
