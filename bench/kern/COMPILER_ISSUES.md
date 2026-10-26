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
