# ADR-005: The `out` convention with static per-field cleanup on error

**Status:** Decided
**Date:** 2026-10-26
**Spec:** §5.1 (whole-value initialization), §5.2 (`out`, failure rule, caller-side state),
§5.4 (`init` access class), §5.7, §5.8; evaluation §2 (P8), §4.2.
**Depends on:** ADR-001, ADR-003. **Interacts with:** ADR-006 (no cleanup on trap).

## Problem

Evaluation P8: `Page { data: [4096]u8 }` built on the stack and moved into a heap slot is a
4 KiB memcpy, and `show-copies` can report it but nothing can remove it. Spec §3 principle 2
forbids hidden copies, and ADR-003 forbids partially initialized ordinary locals. The language
needs a convention for constructing a value directly in its final storage, with definite
initialization still static, and a rule for what the caller sees when construction fails
half-way.

## Alternatives

- **Return by value and rely on copy elision** (C++17 guaranteed elision, NRVO) [iso-cpp20].
  Rejected: elision into a container slot or through a `Result` is not guaranteed, and an
  optimization the language does not promise cannot be reported as `proven`.
- **Rust `MaybeUninit` plus unsafe `ptr::write`** [rustonomicon]. Rejected as the user-facing
  form: in-place construction of a safe value should not require `unsafe`; and Rust's own
  placement-new proposals were withdrawn for lack of a sound, static initialization story.
- **Hylo `set` convention** [hylo-tour] [hylo-mvs]. Adopted in substance under the name
  `out`: a parameter that denotes uninitialized storage the callee must initialize.
- **Zero-initialize then mutate** (Zig `undefined` and C-style construction) [zig-ref].
  Rejected: a zeroed value is a hidden write of the full size and, for move-only fields, a
  value with no destructor semantics; it also makes "initialized" a runtime notion.
- **Dynamic initialization flags per field** (the drop-flag answer). Rejected by ADR-003:
  cleanup must be static.
- **Constructor closures** (`Box.new_with(|slot| ...)`). Needs closures, which ADR-010
  defers; the spec's `Box.new_with(init: func(out T) -> Result[(), E])` uses a function
  pointer with an `out` parameter instead.

## Invariants

1. `out T` is a parameter modifier, not a type (`spec/types.md` §2). The caller passes
   `out x` naming an uninitialized local, or a fresh slot provided by a container. Passing a
   field of an initialized value is not an `out` argument.
2. Inside the callee, initialization of the `out` storage is tracked per field, statically.
   Every normal-return path must have initialized the whole value (`INIT_OUT_NOT_SET`).
3. On an error-return path (`return Err(...)` or `?`), the compiler inserts destruction of
   every initialized field in reverse initialization order; no type destructor runs for a
   partial value; the caller observes the storage as uninitialized. A fully initialized value
   followed by an error is destroyed by the ordinary §5.7 rule.
4. A fallible `out` call is consumed in the same statement by `?` or `match`
   (`INIT_OUT_RESULT_UNHANDLED`). After `f(out x)?`, `x` is initialized; in a `match`, `x` is
   initialized in the `Ok` arm and uninitialized in the `Err` arm; a mixed join is
   `INIT_CONDITIONAL` with a machine-applicable fix. An infallible `out` call initializes
   unconditionally.
5. Passing a place as `out` creates an `init` access: exclusive from argument evaluation to
   return, conflicting with every overlapping live access including a second `init` of the
   same place in the same call (`ACC_CONFLICT`).
6. Per-field initialization exists only inside a callee for its `out` storage; an ordinary
   local is initialized only as a whole (`INIT_FIELD_OF_UNINITIALIZED`).

## Hard counterexample

A connection built in place from a move-only name and a socket. The rejected version leaves
the value partial on a normal return; the accepted version shows the inserted cleanup:

```text
struct Conn { name: Box[Str]; sock: Socket }           // Socket has a destructor

func open_bad(cfg: view[Config], c: out Conn) -> Result[(), NetErr] {
    c.name = Box.new(copy(cfg.name))
    if cfg.lazy { return Ok(()) }                      // INIT_OUT_NOT_SET: c.sock uninitialized on a normal return
    c.sock = connect(cfg.addr)?
    return Ok(())
}

func open(cfg: view[Config], c: out Conn) -> Result[(), NetErr] {
    c.name = Box.new(copy(cfg.name))                   // field 1 initialized
    c.sock = connect(cfg.addr)?                        // on Err: c.name is destroyed by the inserted cleanup,
    return Ok(())                                      //   Conn's destructor does not run, caller sees c uninitialized
}

func dial(cfg: view[Config]) -> Result[(), NetErr] {
    var c: Conn
    r := open(cfg, out c)                              // INIT_OUT_RESULT_UNHANDLED
    match open(cfg, out c) {
        Ok(u)  => send_hello(c)                        // c initialized here
        Err(e) => log_err(e)                           // c uninitialized here
    }                                                  // INIT_CONDITIONAL at the join: fix inserts drop(c) in the Ok arm
    open(cfg, out c)?                                  // accepted: c initialized from here on
    return Ok(())
}
```

Neither C++ nor Rust can express `open` without either a copy of `Conn` or unsafe code; a
region system (ADR-002) does not address construction at all.

## Expressiveness cost

A callee cannot hand back a fully constructed value together with an error; it must return
the value inside the error. `out` targets are limited to uninitialized locals and
container-provided slots, so a field of an existing value is not rebuilt in place (use
`replace`). Fallible `out` calls must be consumed immediately, which forbids storing the
`Result` for later. Construction functions take an `out` parameter rather than returning the
value, which is a visible calling-convention difference from the rest of the language.

## Performance cost

Negative: the 4 KiB `Page` copy disappears, and `Box.new_with`-style APIs construct directly
in the destination. Error paths carry statically placed field destructors, which is code
size proportional to the number of move-only fields initialized before each fallible point,
with no runtime flags. `show-copies` must show no bitwise copy at an `out` site.

## Complexity

Per-field definite initialization inside the callee, which the Phase 0 checker already needs
for whole locals, extended to the fields of one parameter. The caller-side rule is a
syntactic check on the consuming statement plus the existing join rule. The `init` access
class is a fourth kind in the §5.4 conflict table. The lowering inserts static destructors
exactly as it does for scope exit.

## Experiments

- PLAN.md 0.4: reject fixtures `INIT_OUT_NOT_SET`, `INIT_OUT_RESULT_UNHANDLED`,
  `INIT_CONDITIONAL`, `INIT_FIELD_OF_UNINITIALIZED`, and the `ACC_CONFLICT` cases for
  `init` (including two `init` of one place in one call), each with an accepting twin;
  adversarial fixtures "partially initialized objects" and "large-object construction
  without copies".
- PLAN.md 1.2: `out` partial cleanup on error in the interpreter; `alloc_with(edit self,
  init: func(out T) -> Result[(), E])` with `AllocOrInit[E]`, and the two failure fixtures:
  OOM before initialization, and initializer error after partial initialization verifying
  that no destructor runs for the partial value and the slot is vacant afterwards.
  *Done 2026-10-30* (`specs/in-place-init/SPEC.md`, `docs/reports/in-place-init.md`):
  `Box.new_with`, `Pool.alloc_with` and `Arena.alloc_with` over the unsafe `ptr_init`;
  fixtures `tests/run/{box_new_with,pool_alloc_with,arena_alloc_with,pool_alloc_with_oom,
  out_whole_then_err}`, `tests/trap/{box_new_with_trap,pool_alloc_with_stale}`, and
  `tests/adversarial/large_object_out` (no `Page` copy in `show-copies`).
  *Extended 2026-10-30* (`specs/init-context/SPEC.md`, `docs/reports/init-context.md`):
  `Box.new_with_ctx`, `Pool.alloc_with_ctx` and `Arena.alloc_with_ctx` take an initializer
  `func(out T, view[C]) -> Result[(), E]` and its context, over the unsafe
  `ptr_init_ctx`, so per-call data reaches an initializer.
- PLAN.md 2.1 and 2.5: static lowering of `out` cleanup with no landing pads; `show-copies`
  on the `Page` construction path; `show-layout` against C `offsetof` for `out` storage.
- PLAN.md 2.6 and 3.4: vector and buffer-pool benchmark rows (copies and heap allocation
  counts).

## Conclusion

`out T` is the fourth parameter convention. Initialization is tracked per field inside the
callee and must be complete on every normal return; error returns destroy the initialized
fields statically and leave the caller's storage uninitialized; the caller consumes a
fallible `out` call immediately with `?` or `match`.
