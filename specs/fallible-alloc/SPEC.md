# Fallible allocation: `try_` forms of `Box`, `Vec` and `String`

**Version:** 0.1 (2026-11-01). **Scope:** library methods in the core
prelude (`compiler/resolve/prelude/core.kern`, mirrored in
`std/core.kern`) and in `std/str` (`compiler/resolve/prelude/str.kern`,
mirrored in `std/str.kern`); fixtures under `tests/`; the documents
that describe allocation failure. **Language impact:** none to the
grammar, the checker's rules, the trap kinds or the §16 schemas; the two
prelude modules gain the declarations of §2.
**Related:** `specs/initial/AI_Native_Systems_Language_SPEC.md` §6 (the
allocator interface, `union AllocErr { OutOfMemory }`, and the OOM rule
"convenience constructors (`Box.new`) trap on it with kind `oom`"),
§5.6 (the containers); `docs/decisions/ADR-006-panic-is-a-trap.md`
(errors a program is expected to handle are values);
`docs/ownership.md` rule 6.8 (freestanding: allocator exhaustion returns
`Err(OutOfMemory)` and never traps); `specs/in-place-init/SPEC.md` (the
`_with` and `_with_ctx` constructors and `AllocOrInit[E]`).

**Review status.** speccritic (OpenAI gpt-6.1-sol, profile general,
with ADR-006 as context), five rounds: the first found two critical
issues (the "no initializer has run" rule contradicted `Init(e)`; R4's
transitive guarantee covered user code), both fixed, and later rounds'
warnings (the extend guard, element code in R5, mechanism in normative
text, the glossary) are folded in. The fifth round has no model finding;
the remaining warnings are the preflight's flags on R*n* labels,
glossary-defined abbreviations and two "may" phrases in §3 and §6.

**Glossary.** R*n*: requirement *n* of §4. Convenience operation: a
library operation that allocates through the system allocator and traps
with kind `oom` when the allocation fails (`Box.new`, `Vec.push`,
`Vec.reserve`, and the others listed in §1). `try_` form: the method of
§2 that performs the same operation and returns `Err(OutOfMemory)`
instead of trapping. Allocation call: a call of `SystemAllocator.alloc`,
`SystemAllocator.alloc_zeroed` or `SystemAllocator.realloc`. Test hooks:
the `testing.*` functions available with `--test-hooks` in debug builds;
`testing.fail_next_alloc()` makes the next allocation call return
`Err(OutOfMemory)` (natively the runtime's `kern_fail_next` flag, in the
interpreter `failNextAlloc`), and it stays armed until an allocation
call consumes it. Engine: the interpreter or the native build. Opt
level: `debug` or `release`. All four configurations: both engines, each
at both opt levels, as the fixture harness runs a fixture with no
`config:` line. Observable state of a vector or string: its `len()`, its
`capacity()` (R2) and its elements in order. Element code: code of the
element or argument type `T` that a container operation runs, namely
`T`'s destructor, `copy` of a `T` that is not `Copy` (a clone, which
allocates for a container element), and an initializer passed by the
caller. Abbreviations:
OOM, out of memory (the `oom` trap kind); ADR, architecture decision
record (`docs/decisions/`); IR, intermediate representation (kern's
`compiler/ir`, or LLVM's where named); LLVM, the compiler backend the
native build targets; ABI, the calling convention between the
generated code and the C runtime; QEMU, the machine emulator the
freestanding fixtures boot under.

## 0. Goals

A program that must survive allocation failure is written with the
standard containers and without a trap path for `oom`: every operation
of `Box`, `Vec` and `String` that allocates has a form that reports the
failure as a value and leaves the container as it was. Spec §6
already treats allocation failure as an error (`alloc` returns
`Err(OutOfMemory)`; `Pool.alloc`, `Arena.alloc` and the `alloc_with`
forms return it), and ADR-006 makes errors a program is expected to
handle values; the convenience operations are the remaining gap. The
existing operations keep their behavior and their generated code.

Non-goals: changing what any existing operation does, a new trap kind,
recoverable traps, and fallible forms for the types listed in §3.

## 1. Background

The allocator returns `Err(OutOfMemory)`, and the prelude's raw helpers
come in pairs: `heap_try_alloc` and `heap_try_realloc` yield a null
pointer on failure, and `heap_alloc`, `heap_realloc` and
`heap_alloc_zeroed` trap `oom`. The convenience operations use the
trapping helpers, and `Vec` also traps `oom` when a byte size is
unrepresentable (`elems_bytes` returns `None`) and when doubling the
capacity would exceed half the address space. The trapping operations
in scope are:

| Type | Operation | Trapping path |
|---|---|---|
| `Box[T]` | `new`, `new_with`, `new_with_ctx` | `heap_alloc` |
| `Vec[T]` | `reserve`, `with_capacity` | `elems_bytes` `None`; `heap_alloc` / `heap_realloc` |
| `Vec[T]` | `push`, `extend` | `grow` / `grow_to` guard; through `reserve` |
| `Vec[T]` | `resize` | `elems_bytes` `None`; `heap_alloc_zeroed`; through `reserve` |
| `str.String` | `from`, `push`, `push_str` and the `push_*` formatters | through `Vec[u8]` |

Today a program avoids them only by building its own container over the
unsafe allocator. Separately, `Vec.push` allocates only when
`len() == capacity`, and `Vec.extend` only when `len() + len(src)`
exceeds the capacity, so an append into reserved capacity makes no
allocation call. Nothing states that as a guarantee, and the capacity is
not observable, so a program cannot rely on it.

## 2. Design

**Error type.** Every `try_` form reports allocation failure as
`AllocErr.OutOfMemory`, the variant `Pool.alloc` and `Arena.alloc`
already return. An unrepresentable byte size, and a capacity that would
exceed the existing growth guard, are reported the same way. No `try_`
form traps `oom`.

**Failure leaves the container as it was.** On `Err(OutOfMemory)` (for
the `_with` forms, `Err(Alloc(OutOfMemory))`), the observable state of
the receiver is what it was before the call, an owned argument of the
call has been destroyed exactly once (as `Pool.alloc` destroys its
value), and no initializer has been called. `Err(Init(e))` is the one
error reported after an initializer ran: it is the initializer's own
failure, handled as `new_with` handles it. The value is destroyed, not
returned, to match `Pool.alloc` and `Arena.alloc`; a program that must
keep the value reserves first (below) and then appends.

**Reserve, then append.** An append of *k* elements to a vector whose
`capacity() - len() >= k` makes no allocation call for the vector's
buffer, applies no growth guard, and cannot trap `oom`. Element code is
outside the guarantee: `resize` of a non-`Copy` `x` makes its copies by
`copy(x)`, which for a container element allocates as `copy` does. That makes `try_reserve` followed by the infallible operations a
complete fallible idiom, including for the `String` formatters, which
append at most a known number of bytes (`push_u64` at most 20,
`push_i64` 20, `push_hex` 16, `push_rune` 4, `push_padded`
`max(width, 20)`).

**Declarations.** All are kern code; a method containing `unsafe`
carries a contract block (the existing project rule).

```kern
impl Box[T] {
    func try_new(v: own T) -> Result[Box[T], AllocErr]
    func try_new_with[E](init: func(out T) -> Result[(), E]) -> Result[Box[T], AllocOrInit[E]]
    func try_new_with_ctx[C, E](init: func(out T, view[C]) -> Result[(), E], ctx: view[C]) -> Result[Box[T], AllocOrInit[E]]
}
impl Vec[T] {
    func capacity(view self) -> usize
    func try_with_capacity(n: usize) -> Result[Vec[T], AllocErr]
    func try_reserve(edit self, n: usize) -> Result[(), AllocErr]
    func try_push(edit self, v: own T) -> Result[(), AllocErr]
    func try_extend(edit self, src: view[]T) -> Result[(), AllocErr] where T: Copy
    func try_resize(edit self, n: usize, x: own T) -> Result[(), AllocErr] where T: Copy
}
// std/str
impl String {
    func capacity(view self) -> usize
    func try_from(s: view[]u8) -> Result[String, AllocErr]
    func try_reserve(edit self, n: usize) -> Result[(), AllocErr]
    func try_push(edit self, b: u8) -> Result[(), AllocErr]
    func try_push_str(edit self, s: view[]u8) -> Result[(), AllocErr]
}
```

**Semantics of each form on success** are those of the operation it
mirrors: `try_new` / `new`, `try_new_with` / `new_with` (with `Init(e)`
for the initializer's error), `try_new_with_ctx` / `new_with_ctx`,
`try_with_capacity` / `with_capacity`, `try_reserve` / `reserve`,
`try_push` / `push`, `try_extend` / `extend`, `try_resize` / `resize`,
`try_from` / `from`, `try_push` / `push`, `try_push_str` / `push_str`.
In particular the resulting capacity is the one the mirrored operation
produces: `reserve(n)` and `try_reserve(n)` leave the capacity
unchanged when `n <= capacity()` and set it to exactly `n` otherwise;
`push` and `try_push` on a full vector set it to 4 when it was 0 and
double it otherwise; `extend` and `try_extend` set it to the larger of
double the old capacity (4 when it was 0) and `len() + len(src)` when
that exceeds the capacity; `resize(n, x)` and `try_resize(n, x)` with
`n > capacity()` set it to exactly `n`. `capacity()` of a vector or
string with no allocation is 0. `String.try_reserve(n)` and
`String.capacity()` are those of its byte vector.

**Failures of each form.**

- `Box.try_new(v)`: the allocation fails → `v` destroyed,
  `Err(OutOfMemory)`.
- `Box.try_new_with(init)` and `try_new_with_ctx(init, ctx)`: the
  allocation fails → `Err(Alloc(OutOfMemory))` before `init` is called;
  `init` fails with `e` → the allocation is freed and `Err(Init(e))` is
  returned, `init`'s own failure rule having destroyed what it had
  initialized (as `new_with`).
- `Vec.try_reserve(n)`: `n <= capacity()` → `Ok` with no allocation
  call; `n * size_of[T]()` unrepresentable or the allocation fails →
  `Err(OutOfMemory)`, vector unchanged. A failed reallocation leaves the
  old buffer in place (`SystemAllocator.realloc`'s contract), so the
  elements are untouched.
- `Vec.try_with_capacity(n)`: as `try_reserve(n)` on an empty vector;
  on `Err` no vector is returned and nothing is leaked.
- `Vec.try_push(v)`: the vector is full and the capacity exceeds
  `2^63 - 1` (the existing guard), or the growth fails → `v` destroyed,
  `Err(OutOfMemory)`, vector unchanged.
- `Vec.try_extend(src)`: `len() + len(src)` unrepresentable in `usize`
  → `Err(OutOfMemory)`, with no `overflow` trap. When the sum exceeds `capacity()` (and only then),
  the capacity exceeding the `2^63 - 1` guard or the growth failing →
  `Err(OutOfMemory)`. The vector is unchanged on every `Err`.
- `Vec.try_resize(n, x)`: restricted to `T: Copy`
  (`CONSTRAINT_UNSATISFIED` otherwise), so the copies of `x` are loads
  and run no element code that could allocate; `resize` keeps its
  unrestricted signature. `n <= len()` never allocates and always
  succeeds (elements past `n` destroyed, `x` destroyed, as `resize`).
  For `n > len()`, `n * size_of[T]()` unrepresentable or the allocation
  fails → `x` destroyed, `Err(OutOfMemory)`, vector unchanged.
- `String.try_from(s)`, `try_reserve(n)`, `try_push(b)`,
  `try_push_str(s)`: those of the underlying `Vec[u8]` form.

**The existing operations are not changed.** Their signatures, behavior
and contracts stay as they are, and the generated code of a program that
does not call a `try_` form is unchanged (R8).
*Implementation guidance (non-normative):* the new methods sit beside
the existing ones; a non-trapping helper the prelude lacks
(`heap_try_alloc_zeroed`, a growth step that reports failure) is added
as a new function rather than by rewriting a trapping one over it, which
keeps R8's comparison trivially byte-identical; `try_extend` computes
the sum with `checked_add`; `try_resize` keeps `resize`'s
zeroed-storage path for a vector with no allocation (fresh zero pages,
untouched until used) over a non-trapping zeroed allocation;
`try_reserve` is `reserve`
with `heap_try_alloc` / `heap_try_realloc` and an early `Err` for a null
result; `try_push` calls a `try_grow` that mirrors `grow`; the trapping
forms could later be rewritten over the `try_` forms if R8's comparison
shows no change, which is outside this spec.

## 3. Out of scope

- `map.Map` (its insertion rehashes into a new table; a `try_insert`
  needs a fallible rehash, a separate design), `Shared.new`,
  `Mutex.new`, `Condvar.new`, `thread.spawn` and `scope.spawn` (their
  allocation is the runtime's, `kern_alloc`, a C ABI change), and the
  allocation inside `std/io`, `std/json` and `std/process`.
- `Vec.clone`, `Box.clone`, `String.clone` and `copy(x)` of a container:
  copying is a language operation with no `Result` in its type.
- The effect analysis. `show-effects` reports a `try_` form as
  `traps: true`, because `SystemAllocator.alloc` checks its §6
  obligations with an `explicit` trap; to separate "may trap `oom`" from
  other traps is a change to the §16 schema and is recorded in the
  report as an open question.
- Rewriting any benchmark, workload or std module to use the new forms.
- Recoverable traps or a trap handler that resumes (ADR-006, ADR-012).

## 4. Requirements

Each requirement names how it is verified. Fixtures in `tests/run/` and
`tests/trap/` follow the existing header conventions (`//! expect:`,
`//! test-hooks: true`, `//! twin:`); a fixture with test hooks runs in
the configurations the harness gives such fixtures (debug, both
engines).

**R1. Box.** `Box.try_new`, `try_new_with` and `try_new_with_ctx` are
declared and behave as in §2. *Verified by:*
`tests/run/box_try_new.kern` (success of each, the value read back;
`try_new_with` whose initializer fails yields `Init`), all four
configurations; `tests/run/box_try_new_oom.kern` (test hooks: after
`testing.fail_next_alloc()`, `try_new` of a value with a destructor
yields `Err` and the destructor's output appears once; `try_new_with`
yields `Alloc` and the initializer's output does not appear; the same
for `try_new_with_ctx`), both engines.

**R2. Vec success.** `Vec.capacity`, `try_with_capacity`, `try_reserve`,
`try_push`, `try_extend` and `try_resize` are declared and, on success,
produce the same observable state as the mirrored operation, including
the capacities of §2. *Verified by:* `tests/run/vec_try.kern`, which
runs each mirrored pair on equal inputs (empty vector, full vector,
vector with spare capacity; `try_resize` growing from no allocation,
growing, shrinking) and prints `len()`, `capacity()` and a checksum of
the elements after each, the two lines of each pair equal, in all four
configurations.

**R3. Vec failure.** On every failure of §2 the vector's observable
state is unchanged and an owned argument is destroyed exactly once.
*Verified by:* `tests/run/vec_try_oom.kern` (test hooks): for
`try_reserve` on an empty vector and on a vector holding elements (the
reallocation path), `try_push` on a full vector of a type with a
destructor, `try_extend`, `try_resize` from no allocation (the zeroed
path) and from an allocated vector, and `try_with_capacity`: arm the
hook, call, print the result and the state before and after; the
destructor output of a destroyed argument appears once; both engines.
`tests/run/vec_try_size.kern` (no hooks, all four configurations):
`try_reserve(n)` and `try_with_capacity(n)` with `n * size_of[T]()`
past `usize`, `try_resize` with such an `n`, `try_extend` onto a vector
whose `len()` is large enough for the sum to exceed `usize`, and
`try_push` onto a full vector whose capacity exceeds the `2^63 - 1`
guard each yield `Err(OutOfMemory)` without trapping. The last two use
a zero-size element type: its byte sizes are 0, so any capacity needs
no memory, and `resize`'s zeroed-storage path sets `len()` to `n`
without a per-element loop when the element's bytes are all zero.

**R4. No `try_` form traps `oom`.** No `try_` method of §2, nor any
library function it calls, contains a trap of kind `oom`. Element code is
outside this requirement: an initializer is the caller's function, and
`T`'s destructor is `T`'s (a destructor that allocates through a
convenience operation traps as it would anywhere). *Verified by:* a Go
test that lowers a program calling every `try_` method with `T = u64`
(no destructor), `E = ()` and `C = u64`, walks the call graph from each
`try_` method over statically resolved calls, stops at calls through a
function value (the initializer), and fails on a `Trap` instruction of
kind `oom` (the trapping helpers `heap_alloc`, `heap_realloc` and
`heap_alloc_zeroed` contain one, so a call of them is caught by the
walk).

**R5. Reserve, then append.** An append of *k* elements (`push`,
`try_push`, `extend`, `try_extend`, `resize`, `try_resize`, and the
`String` `push*` methods) to a vector or string with
`capacity() - len() >= k` makes no allocation call for the buffer
(element code excluded, as in §2). *Verified by:*
`tests/run/vec_reserved_append.kern` (test hooks): reserve, arm the
hook, perform appends that fit, with `Copy` elements (including `push_u64(u64 max)`,
`push_i64(i64 min)`, `push_hex`, `push_rune` of a 4-byte scalar and
`push_padded` after `String.try_reserve` of their bounds), then show the
hook is still armed by a `Box.try_new` that yields `Err`; both engines.

**R6. String.** `String.capacity`, `try_from`, `try_reserve`,
`try_push` and `try_push_str` are declared in `std/str` and behave as
in §2. *Verified by:* `tests/run/string_try.kern` (success, mirrored
against `from`, `push`, `push_str`, all four configurations) and
`tests/run/string_try_oom.kern` (each failure leaves the string's
bytes and `len()` unchanged; test hooks, both engines).

**R7. Freestanding.** The `try_` forms work in freestanding mode.
*Verified by:* `tests/freestanding/try_alloc.kern` (`//! env:
freestanding`): an unrepresentable `try_reserve` yields `Err`, a
`try_reserve` of 4,096 elements succeeds, the reserved appends of R5
succeed, and a checksum goes to the console, the interpreter's emulation
and both QEMU targets printing the same where QEMU is installed. Arena
exhaustion is not used: the emulation's heap is unbounded (rule 6.8),
so the point of exhaustion differs between engines.

**R8. Existing programs unchanged.** The LLVM IR of every program in
the `scripts/capture-ll.sh` corpus, captured at the revision before
this change, is byte-identical after it, at both opt levels, with the
same programs failing to build; `bench/REPORT.md`'s checksums are
unchanged. *Verified by:* the capture and a file-by-file comparison of
the two corpora; `go test ./...`. If a corpus program is changed by this
work (it is not meant to be), the comparison excludes it and the report
says why.

**R9. Engines agree.** Every fixture of R1–R7 passes the fixture
harness, which compares output, status and trap record between the
engines in each configuration the fixture runs in; the prelude mirror
test passes for `core.kern` and `str.kern`. *Verified by:*
`go test ./...`.

**R10. Documents.** The OOM sentence of spec §6 in
`specs/initial/AI_Native_Systems_Language_SPEC.md` names the `try_`
forms as the non-trapping counterparts of the convenience operations;
`docs/ownership.md` §5.6 lists them with their failure state and the
reserve-then-append guarantee; ADR-006 gains a dated amendment line
pointing here; `docs/reports/fallible-alloc.md` records the design, the
R8 comparison, the review, and the open questions of §3 (the effect
schema; `Map`; the runtime objects). *Verified by:* review;
`go test ./tests` (the citation lint covers ADR-006).

## 5. Acceptance criteria

`golangci-lint run ./...` and `go test ./...` are clean, which covers
R1–R7 and R9 (R7's QEMU runs only where QEMU is installed, the
emulation run everywhere) and R8's checksums; R8's capture comparison is
byte-identical with the same failure list; the documents of R10 are
updated.

## 6. Risks

- **Duplicated bodies.** Each `try_` form repeats the logic of its
  mirrored operation, so a later fix to one may miss the other. R2's
  paired fixture is the guard: a divergence shows as unequal lines.
- **Zero-size element types.** `elems_bytes` gives 0 bytes for any
  count, so a vector of a zero-size type allocates 0 bytes; R3's
  `try_extend` and `try_push` guard cases rely on that to reach a large
  `len()` and capacity without memory. If a zero-size struct is not
  expressible in safe kern, those two cases are dropped and the report
  says so; their paths are then covered by the R4 IR test alone.
- **Generic inference.** `E` and `C` of `try_new_with_ctx` are inferred
  as for `new_with_ctx`; the fixtures show whether an explicit type
  argument is needed.
