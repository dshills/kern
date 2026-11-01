# Containers over a caller's allocator: `BoxIn[T, A]` and `VecIn[T, A]`

**Version:** 0.1 (2026-11-01). **Scope:** two container types in the core
prelude (`compiler/resolve/prelude/core.kern`, mirrored in
`std/core.kern`); their `Send`/`Share`/`Interior` derivation in the
resolver (`compiler/resolve/decls.go`, `markerOf`); fixtures under
`tests/`; the documents that describe allocators. **Language impact:**
none to the grammar or the §16 schemas; the prelude gains two types and
the marker rule gains their case.
**Related:** `specs/initial/AI_Native_Systems_Language_SPEC.md` §6 ("Every
container that allocates takes `own A` where `A: Allocator`, or `own
Shared[Mutex[A]]`"; the allocator obligations), §5.5 (markers), §5.6
(containers); `docs/decisions/ADR-011-freestanding-runtime-facilities.md`
(freestanding allocation); `specs/fallible-alloc/SPEC.md` (the `try_`
forms whose semantics these mirror).

**Review status.** speccritic (gpt-6.1-sol, with ADR-011 as context),
three rounds: no critical finding; the warnings (the no-`oom` boundary,
which counters stay unchanged, the growth guard's predicate, zero-size
elements, destruction order on a failed `try_new_in`, mechanism in
normative text) are folded in.

**Glossary.** R*n*: requirement *n* of §4. User allocator: a type with an
`impl Allocator` other than `SystemAllocator`. Budget allocator: the
fixtures' user allocator, which serves requests from `SystemAllocator`
until a fixed byte budget is spent and then returns `Err(OutOfMemory)`,
counting live allocations and bytes. Mirrored form: the `Vec` or `Box`
`try_` method of the same name (`specs/fallible-alloc`). Observable
state of a vector: `len()`, `capacity()` and its elements in order. All
four configurations: both engines (interpreter, native) at both opt
levels (`debug`, `release`). Abbreviations: ADR, architecture decision
record; IR, intermediate representation; LLVM, the native backend's
target; QEMU, the emulator the freestanding fixtures boot under.

## 0. Goals

Spec §6 promises containers that allocate through an allocator the
program supplies; the prelude's containers allocate only from
`SystemAllocator`. Freestanding and kernel-style code needs storage from
its own allocators (a page allocator, a per-subsystem budget, a bump
region). This change adds the two most used containers in that form,
fallible throughout, with no change to the existing containers or their
generated code.

Non-goals: allocator forms of `Pool`, `Arena`, `String` or `Map`; an
`Allocator` impl for `Shared[Mutex[A]]`; trapping convenience forms;
in-place constructors (`_with`); a `realloc` in the `Allocator`
interface.

## 1. Background

`interface Allocator { alloc(edit self, size, align) -> Result[RawPtr[u8],
AllocErr]; free(edit self, ptr, size, align) }` exists with its §6
obligations, and `SystemAllocator` implements it. `Box`, `Vec`, `Pool`
and `Arena` call `SystemAllocator` through the prelude's `heap_*`
helpers. A generic struct may hold a field of a type parameter
constrained by an interface and call its methods (checked while
planning: a user-module `VecIn[T, A: Allocator]` resolves and checks).
The resolver derives the markers of the core containers by name: their
`Send`, `Share` and `Interior` are the element's, since a `RawPtr` field
alone would make them neither `Send` nor `Share`. Only `SystemAllocator`'s
methods are enumerated unsafe operations; a user allocator's `alloc` and
`free` are ordinary calls whose own bodies use `unsafe`.

## 2. Design

**Types.** Both hold their allocator inline and own it:

```kern
struct BoxIn[T, A: Allocator] nocopy { p: RawPtr[T]; a: A }
struct VecIn[T, A: Allocator] nocopy { data: RawPtr[T]; size: usize; cap: usize; a: A }
```

**Methods.**

```kern
impl[T, A: Allocator] BoxIn[T, A] {
    func try_new_in(v: own T, a: own A) -> Result[BoxIn[T, A], AllocErr]
    func get(view self) -> view[T]
    func at(edit self) -> edit[T]
    func allocator(view self) -> view[A]
    func release(edit self)
    destructor(edit self)
}
impl[T, A: Allocator] VecIn[T, A] {
    func new_in(a: own A) -> VecIn[T, A]
    func len(view self) -> usize
    func capacity(view self) -> usize
    func allocator(view self) -> view[A]
    func try_reserve(edit self, n: usize) -> Result[(), AllocErr]
    func try_push(edit self, v: own T) -> Result[(), AllocErr]
    func try_extend(edit self, src: view[]T) -> Result[(), AllocErr] where T: Copy
    func pop(edit self) -> Option[T]
    func get(view self, i: usize) -> view[T]
    func at(edit self, i: usize) -> edit[T]
    func as_view(view self) -> view[]T
    func as_edit(edit self) -> edit[]T
    func clear(edit self)
    func release(edit self)
    destructor(edit self)
    func seed_len(edit self, n: usize)   // test hook (below)
}
```

`VecIn.seed_len` is a test hook like `Pool.seed_generation`: it requires
`--test-hooks` (`testing.require_hooks()`), traps `explicit` unless
`size_of[T]() == 0` and `n <= capacity()`, and sets `len()` to `n`, so a
fixture can reach the growth guard without 2^63 pushes.

**Semantics.**

- **Allocation goes through `a`.** Every allocation of the container's
  storage is one `a.alloc(size, align)` with `align = align_of[T]()` and
  `size` a multiple of it (`n * size_of[T]()`), and every free is one
  `a.free` with exactly the pointer, size and alignment of one earlier
  successful `alloc` of the same container, once. The containers call no
  allocator but `a` (what `a`'s own code does internally, for example a
  budget allocator drawing on `SystemAllocator`, is `a`'s business). A
  zero-size `T` is supported: its requests are 0 bytes and reach `a.alloc`
  like any other (spec §6 allows `size == 0`), and are freed the same way.
- **Growth.** The `Allocator` interface has no `realloc`, so growing
  allocates the new buffer, moves the live elements into it in order
  (each element's ownership passes to its new slot; no element is
  copied, destroyed or run through its own code), and then frees the old
  buffer through `a`; on a failed `alloc` the old buffer and the vector
  are untouched. *Implementation note (non-normative):* the move is one
  `ptr_copy` of the live elements.
- **Capacities and failures** are those of the mirrored `Vec` forms
  (`specs/fallible-alloc/SPEC.md` §2): `try_reserve(n)` sets the capacity
  to exactly `n` when `n > capacity()` and does nothing otherwise;
  `try_push` on a full vector grows to 4 from 0 and doubles otherwise;
  `try_extend` grows to the larger of double (4 from 0) and
  `len() + len(src)` when the sum exceeds the capacity; a byte size or a
  length sum past `usize`, the growth guard, or `a.alloc` returning
  `Err` yields `Err(OutOfMemory)` with the observable state unchanged,
  and `try_push` destroys its argument on `Err`. The growth guard is the
  one `Vec` has: when `try_push` finds the vector full, or `try_extend`
  needs to grow, and `capacity() > 2^63 - 1` (strictly), the call
  yields `Err(OutOfMemory)` before computing a doubled capacity;
  `try_reserve(n)` has no guard beyond the byte-size check. No method of
  §2 traps `oom`.
- **`BoxIn.try_new_in(v, a)`** allocates one `T` from `a`; on `Err`, `v`
  is destroyed, then `a`, and `Err(OutOfMemory)` is returned. A
  program that must not lose its allocator uses a handle type for `A`
  (a small value that refers to allocator state held elsewhere).
- **`get`, `at`, `as_view`, `as_edit`, `pop`, `clear`** are those of `Vec`
  and `Box` (an index past `len()` traps `bounds`; a `BoxIn` emptied by
  `release` traps `explicit` on `get` and `at`).
- **`allocator()`** returns a view of the held allocator, so a program
  can read a budget allocator's counters.
- **Destruction.** `release` destroys the elements (a vector from last
  to first), frees the storage through `a`, and leaves an empty
  container that still holds `a`; the destructor calls `release`, after
  which the allocator `a` is destroyed as an ordinary field.
- **Markers.** `BoxIn[T, A]` and `VecIn[T, A]` are never `Copy`; `Send`
  and `Share` hold iff they hold for both `T` and `A`; `Interior` holds iff
  it holds for `T` or for `A` (the allocator is an inline field, and a
  type is `Interior` when any field is, Rule 5.4.6).
- **Unsafe** (a project constraint, Rule 6.4, not behavior): the raw
  operations sit in `unsafe` blocks inside methods with contract blocks,
  as in the existing containers, so `docs/unsafe-audit.md` can list them.

## 3. Out of scope

- `PoolIn`, `ArenaIn`, an allocator-generic `String` or `Map`.
- `impl Allocator for Shared[Mutex[A]]` (sharing one allocator between
  containers); a program shares one today through a handle type.
- Trapping forms (`push`, `reserve`, `extend` on `VecIn`); `into` and
  `into_allocator`.
- Changing `Box`, `Vec`, `Pool` or `Arena`, or rewriting them over the
  new types.
- `show-allocations` sites for user allocators: a call into a
  `VecIn[T, A]` with a user `A` is a site only if `A`'s own code
  allocates from the system allocator, as the analysis already decides.

## 4. Requirements

Fixtures follow the existing header conventions; "all four
configurations" is a fixture with no `config:` line.

**R1. Declarations.** The types and methods of §2 are declared in the
core prelude, mirrored in `std/core.kern`. *Verified by:* the fixtures
below; `TestPreludeSync`.

**R2. Mirrors `Vec` with the system allocator.** For `A =
SystemAllocator`, every `VecIn` method leaves the same observable state
as the `Vec` form of the same name on equal inputs, capacities
included. *Verified by:* `tests/run/vecin_system.kern`, which runs each
pair (`try_reserve` below and above the capacity, `try_push` on empty,
full and spare-capacity vectors, `try_extend` that fits, that doubles
and that needs more than double, `pop`, `clear`) and prints `len()`,
`capacity()` and a checksum for each side, the lines of a pair equal, in
all four configurations.

**R3. Every allocation through the allocator, freed once.** *Verified
by:* `tests/run/vecin_budget.kern` (all four configurations): a budget
allocator records each `alloc` and `free` (size, alignment, live count,
live bytes) and checks that each `free` names a live allocation of the
same size and alignment (an `explicit` trap otherwise); a `VecIn` grown
through several reallocations, cleared, refilled and released, and a
`BoxIn` created and released, end with live count 0 and live bytes 0,
read through `allocator()`, and the program prints the allocation
sequence.

**R4. Exhaustion is a value.** *Verified by:* `tests/run/vecin_exhaust.kern`
(all four configurations): with a budget smaller than the growth
needs, `try_reserve`, `try_push` (with an element whose destructor
prints, which appears once) and `try_extend` yield `Err(OutOfMemory)`
with the vector's observable state unchanged and the allocator's live
count and live bytes unchanged (its count of `alloc` calls includes the
failed call); `BoxIn.try_new_in` with an exhausted budget yields
`Err` and destroys its value once; a byte size past `usize` yields `Err`
without calling `alloc` (the allocator's count of `alloc` calls is
unchanged).

**R5. No `oom` trap.** No method of §2, nor any library function it
calls, contains a trap of kind `oom`. The allocator's own code and the
element type's destructor are the program's, outside this requirement
(a user `alloc` that calls a trapping convenience operation traps as it
would anywhere). *Verified by:*
`compiler/analysis/try_alloc_test.go` extended with every `BoxIn` and
`VecIn` method at `T = u64`, `A = SystemAllocator` (the walk follows
the monomorphized `alloc` and `free` calls).

**R6. Markers.** `VecIn[T, A]` and `BoxIn[T, A]` have the markers of §2
and are never `Copy`. *Verified by:* a negative fixture
`tests/negative/vecin_send.kern` (`SEND_NOT_SEND`: a `VecIn[u64, H]`
whose allocator `H` holds a `RawPtr` is passed to `thread.spawn`) with
an accepting twin (`H` with `unsafe assert Send for H`), and a resolver
test asserting the `Share` and `Copy` cases.

**R7. Freestanding.** *Verified by:* `tests/freestanding/vecin.kern`
(`//! env: freestanding`): a budget allocator over the freestanding
`SystemAllocator` serves a `VecIn` that grows, fails once its budget is
spent, and is released to zero live bytes; the emulation and both QEMU
targets print the same.

**R8. Existing programs unchanged.** The LLVM IR of every program in
the `scripts/capture-ll.sh` corpus at the revision before this change
is byte-identical after it, at both opt levels, with the same programs
failing to build. *Verified by:* a baseline capture before any prelude
edit and a file-by-file `cmp`; new fixtures listed apart.

**R9. Engines agree.** Every fixture of R2–R7 passes the fixture
harness in each configuration it runs in. *Verified by:* `go test ./...`.

**R10. Documents.** `docs/ownership.md` §5.6 gains a rule for the two
types (allocation through `a`, growth without `realloc`, failure state,
markers); spec §6's allocator paragraph says which containers take an
allocator today; `docs/unsafe-audit.md` lists the new functions with
unsafe blocks and its counts match the prelude; ADR-011 gains a dated
note; `docs/reports/alloc-containers.md` records the design, R8, the
reviews and §3's open items. *Verified by:* review; `go test ./tests`.

## 5. Acceptance criteria

`golangci-lint run ./...` and `KERN_REQUIRE_QEMU=1 go test ./...` are
clean; R8's comparison is byte-identical with the same failure list; the
documents of R10 are updated.

## 6. Risks

- **Monomorphized interface calls in the walk.** R5's walk needs the
  call of `a.alloc` to be a statically resolved call in the lowered IR;
  if it is lowered through an interface table, the test follows that
  form instead, and the report says how.
- **Marker derivation.** The new rule is by name in `markerOf`; a
  program with its own type named `BoxIn` is in another module and does
  not match `core.BoxIn`.
- **Bitwise moves on growth** are sound because every kern value is
  movable by copying its bytes (no self-referential values in safe code);
  `Vec` relies on the same through `realloc`.
