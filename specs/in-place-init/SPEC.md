# In-place initializers: `Box.new_with`, `Pool.alloc_with`, `Arena.alloc_with`

**Version:** 0.1 (2026-10-30). **Scope:** one unsafe library intrinsic
(`ptr_init`) in the core prelude and its implementation in the
interpreter (`compiler/interp`), the LLVM backend
(`compiler/backend/llvm`) and the effect analysis
(`compiler/analysis`); three library methods in the core prelude
(`compiler/resolve/prelude/core.kern`, mirrored in `std/core.kern`);
fixtures under `tests/`; the documents that describe the containers.
**Language impact:** none to the grammar, the checker's rules or the
§16 schemas; the prelude gains the four declarations below.
**Related:** `specs/initial/AI_Native_Systems_Language_SPEC.md` §5.2
(the `out` convention, its failure rule, "a fresh slot provided by a
container", and `Box.new_with(init: func(out T) -> Result[(), E])`),
§5.6 (arenas and pools), §6 (unsafe operations, trap kinds);
`specs/initial/PLAN.md` Phase 1 (the fixed signature of `alloc_with`
and `AllocOrInit[E]`, and its two failure fixtures);
`docs/decisions/ADR-005-out-convention.md` (the experiments it lists);
`docs/reports/closures.md` (a closure parameter cannot be `out`).

**Review status.** speccritic (OpenAI gpt-6.1-sol, profile general,
with ADR-005 as context): VALID_WITH_GAPS, no critical finding; its two
clarifications (capacity grown before a failed initializer is kept; the
methods being kern code is a project constraint) are folded in, and the
remaining warnings are the preflight's flags on R*n* labels and code
identifiers. R10 was added after the gate, for a pre-existing defect
the fixtures exposed.

**Glossary.** R*n*: requirement *n* of §4. Initializer: a function
value of type `func(out T) -> Result[(), E]`. Slot: storage for one `T`
that a container provides and that holds no live value. In place: the
value is constructed in its final storage, with no `T` value built
elsewhere and copied or moved there. Engine: the interpreter or the
native build. Opt level: `debug` or `release`. Out failure rule: spec
§5.2's rule that on an error return the callee destroys the fields of
its `out` storage it had initialized (or the whole value, by §5.7, when
it had initialized all of it) and the caller observes the storage as
uninitialized. Test hooks: the `testing.*` functions available with
`--test-hooks` in debug builds, among them `testing.fail_next_alloc()`.

## 0. Goals

Large or move-only values can be constructed directly in a heap box or
a container slot, as spec §5.2 promises and the plan's Phase 1 fixed,
so that construction costs no copy of the value and failure part-way is
cleaned up by the existing static rule. The non-goals: initializers
that carry per-call context (see §3), new trap kinds, and rewriting any
benchmark program to use the new methods.

## 1. Background

`out` parameters work for uninitialized locals: a caller writes `f(out
x)`, and the callee initializes `x` field by field, with the failure
rule on error paths (ADR-005). Spec §5.2 also allows "a fresh slot
provided by a container", and the plan fixed `alloc_with(edit self,
init: func(out T) -> Result[(), E]) -> Result[Id, AllocOrInit[E]]` with
`union AllocOrInit[E] { Alloc(AllocErr); Init(E) }` (already declared in
the prelude). Nothing in the library could pass a container's raw slot
as `out` storage, so none of the three methods exists, and the
adversarial note "large-object construction without copies" can be met
only with a local. A call through a function value whose parameter is
`out` already works in both engines.

## 2. Design

**The intrinsic.** The prelude declares

```kern
func ptr_init[T, E](p: RawPtr[T], init: func(out T) -> Result[(), E]) -> Result[(), E]
```

It calls `init` with the storage at `p` as its `out` parameter and
returns `init`'s result. Its name begins with `ptr_`, so, like every raw
pointer operation, it is an enumerated unsafe operation (spec §6) and
must appear inside `unsafe` (the resolver's existing rule for `ptr_`
intrinsics). Its obligation, stated on its declaration: `p` addresses
storage for one `T` that holds no live value. On `Ok`, the storage holds
an initialized `T`; on `Err`, the out failure rule has already run
inside `init`, and the storage is uninitialized. Natively the `out`
parameter is a pointer, so the call passes `p` itself; in the
interpreter the `out` parameter is an access on raw memory, as
`ptr_edit` opens. *Implementation guidance (non-normative):* the
lowering keeps it an intrinsic call; the backend emits an indirect call
through `init` with `p` as the argument.

**The methods.** A project constraint, not a behavioral requirement:
the containers are kern code in the prelude since the containers rewrite
(`docs/reports/` on the containers), so the methods are too, each with a
contract block because it contains `unsafe`.

```kern
impl Box[T]   { func new_with[E](init: func(out T) -> Result[(), E]) -> Result[Box[T], E] }
impl Pool[T]  { func alloc_with[E](edit self, init: func(out T) -> Result[(), E]) -> Result[PoolId[T], AllocOrInit[E]] }
impl Arena[T] { func alloc_with[E](edit self, init: func(out T) -> Result[(), E]) -> Result[ArenaId[T], AllocOrInit[E]] }
```

- `Box.new_with` allocates as `Box.new` does (a failed allocation traps
  `oom`), initializes the allocation through `init`, and returns the box;
  on `Err(e)` it frees the allocation and returns `Err(e)`.
- `Pool.alloc_with` obtains a slot as `Pool.alloc` does (the free list
  first, else the next slot, growing the buffers); if growing fails it
  returns `Err(Alloc(OutOfMemory))` without calling `init`. It then
  calls `init` on the slot. On `Ok` the slot becomes live and the handle
  is returned, exactly as `alloc` would return it for that slot. On
  `Err(e)` the slot goes back to the free list, vacant, with its
  generation unchanged, and `Err(Init(e))` is returned.
- `Arena.alloc_with` grows as `Arena.alloc` does (`Err(Alloc(OutOfMemory))`
  without calling `init` on failure), calls `init` on the next slot, and
  on `Ok` makes it the arena's newest element and returns its handle; on
`Err(e)` the arena's elements and `len()` are unchanged and
`Err(Init(e))` is returned.
- **Capacity after a failure.** In both containers, capacity the call
  grew before `init` failed is kept (it is not observable through the
  API); a pool slot appended by the call is kept too, on the free list.
  What R4 and R5 require unchanged is the live elements and `len()`.

## 3. Out of scope

- **Initializers with context.** An initializer is a function value,
  which cannot capture; a closure parameter cannot be `out`
  (`docs/reports/closures.md`), so `closure(out T) -> Result[(), E]` is
  not an initializer type. Per-call data (an identifier, a buffer) must
  come from somewhere the function can read (a thread-local, a global
  constant). Allowing `out` closure parameters, or an explicit context
  argument, is a language decision left to the owner and recorded in the
  report.
- `Vec` in-place growth, `Shared`, `Mutex` construction.
- Rewriting `bench/kern/btree` to use `alloc_with` (`docs/workloads/btree.md`
  suggests it); benchmark programs are not rewritten by this change.
- A trap kind or diagnostic code of its own.

## 4. Requirements

Each requirement names how it is verified. "All four configurations"
means the interpreter and the native build, each in `debug` and
`release`, as the fixture harness runs a fixture with no `config:` line.

**R1. `ptr_init`.** The prelude declares `ptr_init` as in §2 with a
comment stating its obligation. A call outside `unsafe` is rejected by
the existing rule for raw-pointer operations; inside `unsafe` it calls
the initializer with the storage at `p` as `out` and yields its result
in all four configurations. Its type arguments are inferred from the
arguments (`T` from `p`, `E` from the initializer's result). *Verified
by:* a negative fixture (a call outside `unsafe`, with its accepting
twin inside `unsafe`); the fixtures of R3–R5, which reach it through the
methods.

**R2. Effects.** `show-effects` labels a function that calls `ptr_init`
(directly, or through one of the methods) `unknown` with every effect
possible, as for a call through a function value. *Verified by:* an
analysis test on a function calling `Box.new_with`.

**R3. `Box.new_with`.** Declared and behaving as in §2. A trap inside
the initializer reports the initializer's own file and span. *Verified
by:* `tests/run/box_new_with.kern` (an initializer that succeeds, whose
value is read back; one that fails after initializing a field with a
destructor, whose destructor runs once inside the initializer and whose
outer value's type destructor does not run; one that initializes the
whole value and then fails, whose value is destroyed by §5.7 once), all
four configurations, with the expected output; and
`tests/trap/box_new_with_trap.kern`, a bounds trap inside an
initializer reported at the initializer's span.

**R4. `Pool.alloc_with`.** Declared and behaving as in §2. After an
`Err(Init(e))`, the pool's `len()` is what it was before the call and
the next `alloc` or `alloc_with` reuses the slot; after
`Err(Alloc(OutOfMemory))` the initializer has not run. *Verified by:*
`tests/run/pool_alloc_with.kern` (success, read back through `get`;
failure after partial initialization, the field destructor once and no
type destructor; `len()` unchanged; a following `alloc` succeeds and
`len()` is 1; a slot freed and reused by `alloc_with`, with an old
handle to that slot trapping `generation` in a separate trap fixture
`tests/trap/pool_alloc_with_stale.kern`), all four configurations; and
`tests/run/pool_alloc_with_oom.kern` (test hooks, debug only:
`testing.fail_next_alloc()` then `alloc_with` on an empty pool yields
`Alloc`, and the initializer's print does not appear), in both engines.

**R5. `Arena.alloc_with`.** Declared and behaving as in §2.
*Verified by:* `tests/run/arena_alloc_with.kern` (success; failure,
after which `len()` is unchanged and the next `alloc` takes the same
index, observed through `get` of the returned handle), all four
configurations; and an OOM case in `pool_alloc_with_oom.kern` for the
arena.

**R6. No copy.** For a program that constructs a 4,096-byte `Page`
with `Box.new_with` and with `Pool.alloc_with`, `show-copies` reports no
copy whose type is `Page`. *Verified by:* the adversarial fixture
`tests/adversarial/large_object_out.kern` extended with both calls, and
an analysis test asserting no `Page` entry in its `show-copies` result.

**R7. Interpreter and native agree.** Every fixture of R1–R6 passes the
fixture harness, which compares output, status and trap record between
the engines in every configuration the fixture is valid in. *Verified
by:* `go test ./...`.

**R8. Programs that do not use them unchanged.** The LLVM IR of every
single-file program captured at the revision before this change (the
corpus of `scripts/capture-ll.sh`: `bench/kern`, `tests/run`,
`tests/trap`, `tests/positive`) is byte-identical after it, in both opt
levels, with the same programs failing to build; `bench/REPORT.md`'s
checksums are unchanged. *Verified by:* the capture and a file-by-file
comparison of the baseline corpus; `go test ./...`.

**R10. A whole `out` value destroyed on error.** When a function has
initialized every field of its `out` parameter and then returns an
error, spec §5.2 destroys the whole value by §5.7 (the type's destructor,
then its fields). The lowering emits that drop; before this change the
IR validator rejected it ("drop of %c which is uninitialized", an
internal error on any such program), since it tracks the parameter only
as a whole. The lowering marks the parameter initialized before the drop
(no runtime effect). *Verified by:* `tests/run/out_whole_then_err.kern`
(an ordinary local caller; a `Cell` with a destructor and a `Tok`
field: the output is the cell's destructor, then the field's), all four
configurations; `tests/run/box_new_with.kern`'s third case and
`tests/run/arena_alloc_with.kern`'s failure, which take this path.

**R9. Documents.** `docs/ownership.md` rule 5.6.1 names the three
methods and the slot outcome of each failure; ADR-005's experiments list
records which fixtures cover them; `tests/adversarial/NOTES.md` §9
matches the fixture; `docs/reports/multi-module-sweep.md`'s finding is
marked resolved; `docs/reports/in-place-init.md` records the design,
the measurements and the review, and the open question of §3.
*Verified by:* review.

## 5. Acceptance criteria

`golangci-lint run ./...` and `go test ./...` are clean (R1–R7 and R8's
checksums); R8's capture comparison is byte-identical with the same
failure list; the documents of R9 are updated.

## 6. Risks

- **Raw storage in the interpreter.** The interpreter's out access on
  raw memory decodes and re-encodes the slot on each field write; a slot
  reused from the free list holds the old value's bytes until
  overwritten, which the out rule makes unobservable (every normal
  return initializes the whole value, and an error leaves it vacant).
- **Initializer traps.** A trap inside an initializer terminates the
  program with the slot obtained but not live; no cleanup runs on a trap
  (ADR-006), as for every trap.
- **Generic inference.** `E` is inferred from the initializer's
  function type; if inference cannot see through a function type, the
  methods need an explicit type argument, which the fixtures would show.
