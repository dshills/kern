# Compiler notes from writing bench/kern

Observations made while writing the seven benchmark programs (PLAN.md 2.6),
with their resolution in the same phase.

## 1. Interpreter panicked when a module-level `const` was read — fixed

`const K: u64 = 5u64` followed by `x := 1u64 & K` crashed the interpreter
(the constant's initializer was never typed, so the lowering saw an untyped
literal; the native build happened to work). The resolver now types every
constant initializer against its declared type (`tests/run/consts.kern`,
`tests/negative/const_type_mismatch.kern`).

## 2. `with` binding held across a recursive call — by rule, not a bug

```kern
with n := g.nodes.get(id) { return n.value + walk(g, n.next) }
```

is rejected with `ACC_CONFLICT` (rule 5.4.4) for `g: view[G]`, while the
same walk written with `match g.nodes.get(id) { Link(v, next) => return v + walk(g, next) }`
is accepted. Rule 5.4.2 says that while a binding is live its root must not
be accessed except through the binding; `n` is a live access binding rooted
at `g`, so the call `walk(g, …)` conflicts. The `match` form binds `v` and
`next` by value (Copy fields), so no access binding is live during the call.
The benchmark keeps the `match`/per-statement form.

## 3. Wrapping arithmetic — intrinsics added

The prelude had no wrapping forms, so the first versions emulated them from
32-bit halves. `wrapping_add`, `wrapping_sub` and `wrapping_mul` (spec §4.1)
are now prelude intrinsics lowered to wrapping binary operations in both
engines (`tests/run/wrapping.kern`), and every benchmark uses them.

## Notes (not bugs)

- `<<` traps when a set bit is shifted out (spec §4.1), so hashes mask before
  shifting; `truncate[u8]`/`truncate[u16]`/`truncate[u32]` take low bytes.
- `now_ns()` uses `CLOCK_MONOTONIC_RAW` on macOS (`CLOCK_MONOTONIC` has
  microsecond resolution there); per-op samples sit on the ~42 ns tick.

# Compiler notes from writing the Phase 3 benchmarks (PLAN.md 3.4)

Observations made while writing buffer_pool, page_frame_allocator,
intrusive_run_queue, thread_pool, lock_protected_map, lock_free_queue and
packet_parser_simd. The compiler was not modified; each has a workaround.

## 4. Returning a field projection as an access is rejected — open

The accepted sketch of `docs/ownership.md` Rule 5.3.4,
`func bump(c: edit[Cell]) -> edit[u64] { return c.v }`, is rejected with
`TYPE_MISMATCH rule T.5: expected edit[u64], found u64`. The same happens
with an `edit self` receiver, with a `release` clause, and when the field
type is a `nocopy` struct (so it is not the Copy-read rule): only a call
projection (`Vec.at`, a sub-slice) is accepted as a returned access.

```kern
module m
struct Cell { v: u64 }
func bump(c: edit[Cell]) -> edit[u64] { return c.v }   // TYPE_MISMATCH: expected edit[u64], found u64
```

Workaround in `buffer_pool.kern`: the release accessor is on the pool,
`pin(edit self, f: usize) -> edit[Frame] release unpin_last`, returns the
call projection `self.frames.at(f)` and records `f` in `self.last_pinned`
for the release function (which, per §5.3, receives the receiver only).
A frame-level `Frame.pin(edit self) -> edit[Page] release unpin` would be
the natural form once field projections can be returned.

## 5. A field of a returned projection is not accepted as a `view` argument — open

Rule 5.3.3 lets a returned projection be consumed as an argument in the
same statement; a field projection of it is rejected:

```kern
module m
struct Pool { words: Vec[u64]; n: u64 }
func total(words: view[Vec[u64]]) -> u64 { return u64(words.len()) }
func f(p: view[Shared[Pool]]) -> u64 { return total(p.get().words) }   // PLACE_REQUIRED rule T.5: view parameter requires a place
```

Binding the projection first is accepted:
`with pl := p.get() { return total(pl.words) }`, which is what
`thread_pool.kern` does around `run_job`. A method call through the same
chain, `p.get().q.lock()`, is accepted as a `with` subject.

## Notes (not bugs)

- `match thread.spawn(...)` and `match ts.pop()` on `Result[Thread[R], E]`
  / `Option[Thread[R]]` are `OWN_DESTRUCTURE_DESTRUCTOR` (Rule 5.3.1: the
  payload has a destructor), as is `match run()` on a `Result` carrying a
  `Sampler`. The multi-threaded programs spawn with `?` inside a helper that
  returns `Result[i32, SpawnErr]`, join named locals, and report inside the
  helper.
- Views passed to `Scope.spawn` are live until the scope block ends, and
  `Mutex.lock` roots its `edit` at the mutex, so `m.lock()` inside the block
  after the joins is `ACC_CONFLICT`; `lock_protected_map.kern` keeps the
  joined results in `var` locals and locks after the block.
- A whole-value read of a Copy field through a projection
  (`self.frames.get(i).pins`) is the way to read container elements while
  the container is edited elsewhere in the function; a 4 KiB `Page` copied
  as `fr.page = self.store.get(p)` inside `with fr := self.frames.at(v)` is
  accepted because `self.frames` and `self.store` are disjoint paths.
- `const FAIL_MODE: bool = false` selects the failure-path program at
  compile time; kern has no `getenv`, so `<w>_fail.kern` is the same file
  with the constant flipped (the C/Rust rows read `KERN_BENCH_FAIL=1`).
- There is no condition variable and no spin hint, so `thread_pool.kern`
  spins on the mutex when the ring is empty or full and the SPSC threads
  spin without `pause`/`isb`; the C version uses condvars and a spin hint,
  which is where its lower p99 comes from.

### Resolutions (same phase)

- Issue 4 (returning a field projection as an access) — fixed: `return c.v` with a
  return type `edit[u64]` is accepted when `c.v` is a place of the element type rooted
  at the single access parameter (Rule 5.3.4); `tests/positive/elision_field_return.kern`.
- Issue 5 (a field of a returned projection as a view argument) — fixed: a path through
  a call projection (`p.get().words`) is a place whose permission is the projection's;
  same fixture.
