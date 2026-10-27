# kern language reference — Ownership

## Purpose
This chapter is the normative restatement of the ownership model (specification §5, with the §4.1, §6 and §7 rules that interact with it), numbered so that every diagnostic code in `compiler/diag/codes.go` points at exactly one rule. The specification takes precedence where the two differ.

## Non-goals
Grammar, name resolution and the type system (`spec/types.md`), execution semantics other than the trap kinds named here, the machine API, and library container internals are out of scope.

## Acceptance criteria and testing
Every rule with a `Diagnostics:` line has a rejecting fixture under `tests/negative/` or `tests/adversarial/` that produces exactly that code and rule number, and an accepting twin; `go test ./tests/` enforces this. Rules without a code are exercised by the accepting fixtures and the five example programs under `examples/`.

## Terms
- **ABI** — application binary interface.
- **ADR** — architecture decision record.
- **CAS** — compare-and-swap.
- **FFI** — foreign function interface.
- **IR** — intermediate representation.
- **MMIO** — memory-mapped input/output.
- **TLS** — thread-local storage.

## Requirements (normative rules)

This chapter restates spec §5 (and the §4.1, §6 and §7 rules that interact with it) rule by rule, in the spec's order. **Rule S.s.b** is bullet (or paragraph) *b* of spec §S.s; the number is stable and is the `rule` field of every diagnostic (`compiler/diag/codes.go`). Each diagnostic code appears under exactly one rule; where the spec mentions a code in a second place, that place says *reported under Rule X* instead of listing the code again. A rule without a Diagnostics line has no code of its own. Sketches follow `spec/grammar.ebnf`: `Box[T]`, `Pool[T]`, `PoolId[T]`, `Arena[T]`, `ArenaId[T]`, `Mutex[T]`, `Atomic[T]`, `Join[R]`, `Thread[R]`, `SpawnErr`, `thread` and `trap` come from the implicit `std/core`; `Box[T]` is move-only and `u64` is `Copy`. Accepted and rejected sketches of one rule differ by as little as possible; the comment on the offending line names the code.

### Rule 4.1.2 — Arithmetic
`+ - *` and unary negation on integers trap on overflow (kind `overflow`) in every build configuration; division and remainder by zero trap (`div_zero`); dividing the minimum value of a signed type by `-1` traps (`overflow`). **Accepted decision (spec §13, item 2):** the remainder of the minimum signed value by `-1` also traps with kind `overflow`, matching division. The explicit alternatives are exactly the `wrapping_*`, `saturating_*` and `checked_*` forms of §4.1.

### Rule 4.1.5 — Layout
Struct fields are laid out in declaration order with the target C ABI alignment and padding. A union's discriminant is at offset 0, is `u8` when the union has at most 256 variants and `u16` otherwise, and numbers variants from 0 in declaration order; a union with more than 65,536 variants is rejected. The payload begins at the next offset aligned to the maximum alignment of all variant payloads. `[N]T` is `N` contiguous elements; a sequence access is a (pointer, length) pair of two `usize`; a scalar access is one pointer. Reordering is never performed implicitly.
Diagnostics: `TYPE_TOO_MANY_VARIANTS`
Accepted:
```kern
module m
union Small { A; B; C }          // discriminant u8, variants numbered 0, 1, 2
```
Rejected:
```kern
module m
union Huge { V0; V1; V2 }        // TYPE_TOO_MANY_VARIANTS: sketch stands for a union declaring 65,537 variants
```

### Rule 4.1.6 — SIMD vectors
A fixed array `[N]E` with `N` in 2, 4, 8, 16 and a numeric `E` is a SIMD vector for the intrinsics `simd_add`, `simd_sub`, `simd_mul` (integer lanes wrap), `simd_and`, `simd_or`, `simd_xor` (integer lanes only), `simd_shuffle(a, b, idx: [N]u32)` (lane `i` of the result is lane `idx[i]` of the concatenation of `a` and `b`; an index of `2N` or more traps with kind `bounds`), and `simd_reduce_add(v) -> E` (the wrapping sum of the lanes; floats are summed in lane order). Any other type is `TYPE_MISMATCH`. Array literals `[e1, e2, ...]` build vectors and arrays alike.

## 5.1 Unique ownership and moves
### Rule 5.1.1 — One owner, one destruction
Every value has exactly one owner: a variable, a field, an array element, or a slot in a library container. Destruction occurs exactly once, at scope exit of the owner, unless ownership is transferred.

### Rule 5.1.2 — `Copy` derivation
Integer, float and `bool` types, raw pointers, function pointers, and container handles (Rule 5.6.1) are `Copy`. A struct, union or array type is `Copy` iff it declares no destructor (Rule 5.7.1), does not opt out of `Copy`, and every field or element type is `Copy`. Every other type is **move-only**. A field is an **owning field** iff its type is move-only; an aggregate is move-only iff it has a destructor, opts out, or has an owning field. `Copy` values are duplicated bitwise on use.

### Rule 5.1.3 — Explicit deep copies
Deep copies are explicit: `copy(x)` or a `clone` method. There is no implicit deep copy and no hidden heap promotion.

### Rule 5.1.4 — Moves
Moves are implicit at any by-value use of a move-only value: passing to an `own` parameter, assignment, return, or storing into a field or container; `move x` names the move explicitly. A move invalidates the source, and any later access to a moved variable is an error. (A conflict between a `consume` of a variable and another live access, Rule 5.4.4, is reported here too.)
Diagnostics: `OWN_USE_AFTER_MOVE`
Accepted:
```kern
module m
func sink(b: own Box[u64]) { drop(b) }
func f(b: own Box[u64]) { sink(move b) }
```
Rejected:
```kern
module m
func sink(b: own Box[u64]) { drop(b) }
func f(b: own Box[u64]) { sink(move b); sink(move b) }    // OWN_USE_AFTER_MOVE: b was moved by the first call
```

### Rule 5.1.5 — Moved-ness is static
At every program point the checker knows whether each variable is owned or moved. A control-flow join where one predecessor moved a variable and another did not is an error unless the variable is never used again on any path, scope exit included (scope exit destroys an owned variable, so it counts as a use). The machine-applicable fix inserts `drop(x)` on the non-moving path. Consequence: every destructor call site is static in the IR.
Diagnostics: `OWN_CONDITIONAL_MOVE`
Accepted:
```kern
module m
func sink(b: own Box[u64]) { drop(b) }
func f(b: own Box[u64], c: bool) { if c { sink(move b) } else { drop(b) } }
```
Rejected:
```kern
module m
func sink(b: own Box[u64]) { drop(b) }
func f(b: own Box[u64], c: bool) { if c { sink(move b) } }    // OWN_CONDITIONAL_MOVE: b moved on one path, still owned on the other
```

### Rule 5.1.6 — No partial moves
Moving out of a field or element is an error; move tracking is per variable. Use `take(place)` (moves the value out and leaves the type's declared default, which the type must define) or `replace(place, v)`. The same code is reported for a `consume` through a bound access name (Rule 5.4.2) and for moving `self` or a field of `self` out inside a destructor (Rule 5.7.3).
Diagnostics: `OWN_PARTIAL_MOVE`
Accepted:
```kern
module m
struct Pair { a: Box[u64]; b: Box[u64] }
func sink(x: own Box[u64]) { drop(x) }
func f(p: own Pair) { sink(replace(p.a, Box.new(0u64))) }
```
Rejected:
```kern
module m
struct Pair { a: Box[u64]; b: Box[u64] }
func sink(x: own Box[u64]) { drop(x) }
func f(p: own Pair) { sink(p.a) }       // OWN_PARTIAL_MOVE: by-value use of a field moves out of it
```

### Rule 5.1.7 — Definite initialization
Definite initialization is flow-sensitive. Reading a variable or field before it is initialized on any path is an error.
Diagnostics: `INIT_UNINITIALIZED_READ`
Accepted:
```kern
module m
func f(c: bool) -> u64 { var x: u64; if c { x = 1u64 } else { x = 2u64 }; return x }
```
Rejected:
```kern
module m
func f(c: bool) -> u64 { var x: u64; if c { x = 1u64 }; return x }    // INIT_UNINITIALIZED_READ: x is uninitialized when c is false
```

### Rule 5.1.8 — Whole-value initialization of locals
An ordinary local is initialized only as a whole: by a whole-value expression, or by being passed as an `out` argument (Rule 5.2.1). Assigning a field of an uninitialized local is an error. Per-field initialization exists only inside a callee for its `out` storage. Consequently a partially initialized ordinary local never exists, and an uninitialized local leaving scope is destroyed as a no-op.
Diagnostics: `INIT_FIELD_OF_UNINITIALIZED`
Accepted:
```kern
module m
struct P { x: u64; y: u64 }
func f() -> u64 { var p: P; p = P{ x: 1u64, y: 2u64 }; return p.x }
```
Rejected:
```kern
module m
struct P { x: u64; y: u64 }
func f() -> u64 { var p: P; p.x = 1u64; p.y = 2u64; return p.x }    // INIT_FIELD_OF_UNINITIALIZED: p.x assigned on an uninitialized local
```

### Rule 5.1.9 — Reassignment and scope exit
Reassignment of an owned variable destroys the previous value first, in the order of §4: `expr` is evaluated, then the previous value of `place` is destroyed, then the new value is stored. Early return and `?` propagation destroy all owned locals of exited scopes in reverse declaration order. Recursion creates one owner per activation.

## 5.2 Parameter conventions
### Rule 5.2.1 — The four conventions
A convention is written on the parameter and chosen by the callee. A parameter with neither a modifier nor an access type is passed by value: `Copy` values are copied; a move-only type must be declared `own` so that transfer is visible.

| Convention | Meaning | Caller passes | Callee obligations |
|---|---|---|---|
| `view[T]` | read-only, non-escaping | any place of type `T`, or an existing view | only reads; is permitted to pass it down as `view` and return a projection of it (Rule 5.3.4) |
| `edit[T]` | exclusive mutable, non-escaping | a mutable owned place, or an existing edit | is permitted to read and write in place, pass it down as `view` or `edit`, and return a projection of it (Rule 5.3.4) |
| `own T` | ownership transfer | an owned value (moved) or `copy(x)` | must destroy or move the value on every path |
| `out T` | initialize uninitialized storage in place | an uninitialized local, or a fresh slot provided by a container | must initialize the whole value on every normal-return path; failure paths per Rule 5.2.3 |

Diagnostics: `INIT_OUT_NOT_SET`, `PARAM_OWN_REQUIRED`
Accepted:
```kern
module m
func make(o: out Box[u64], c: bool) { if c { o = Box.new(1u64) } else { o = Box.new(2u64) } }
func sink(b: own Box[u64]) { drop(b) }
```
Rejected:
```kern
module m
func make(o: out Box[u64], c: bool) { if c { o = Box.new(1u64) } }    // INIT_OUT_NOT_SET: o uninitialized on the normal return when c is false
func sink(b: Box[u64]) { drop(b) }                                    // PARAM_OWN_REQUIRED: Box[u64] is move-only
```

### Rule 5.2.2 — Second-class restrictions
`view` and `edit` are second-class. An access must not be stored in a struct, union, array or container; must not be returned except under the elision rule (Rule 5.3.4), and in particular never when derived from a local; must not be captured; must not be live across a thread spawn except a scoped spawn (Rule 5.5.3); and must not be live across any future suspension point. A `view struct` field (Rule 5.6.3) is rejected with the store code unless the extension is enabled.
Diagnostics: `ACC_ESCAPE_STORE`, `ACC_ESCAPE_RETURN`
Accepted:
```kern
module m
struct Cell { v: u64 }
struct Holder { c: Cell }
func fresh() -> Cell { c := Cell{ v: 0u64 }; return c }
```
Rejected:
```kern
module m
struct Cell { v: u64 }
struct Holder { c: view[Cell] }                                  // ACC_ESCAPE_STORE: an access cannot be a field
func fresh() -> view[Cell] { c := Cell{ v: 0u64 }; return c }    // ACC_ESCAPE_RETURN: access derived from a local
```

### Rule 5.2.3 — `out` failure rule and caller-side state
*Callee:* initialization of `out` storage is tracked per field, statically. On an error-return path (`return Err(...)` or `?`), the compiler inserts destruction of every initialized field of the `out` storage in reverse initialization order, and the caller then observes the storage as uninitialized. If the whole value is initialized and the callee then returns an error, the whole value is destroyed by Rule 5.7.2; no type destructor runs for a partial value. A callee that must hand back a fully constructed value together with an error returns it inside the error value. *Caller:* when a function with an `out` parameter returns `Result[_, E]`, the call must be consumed in the same statement by `?` or `match`; storing, ignoring or otherwise using the result is an error. After `f(out x)?`, `x` is initialized. In `match f(out x) { Ok(v) => A, Err(e) => B }`, `x` is initialized throughout `A` and uninitialized throughout `B`; at the join after the `match`, `x` is initialized iff it is initialized on every incoming path, and a mixed join is an error whose fix inserts `drop(x)` on the initialized path or initializes `x` on the other. An infallible `out` call initializes `x` unconditionally.
Diagnostics: `INIT_OUT_RESULT_UNHANDLED`, `INIT_CONDITIONAL`
Accepted:
```kern
module m
union E { Bad }
func make(o: out Box[u64]) -> Result[(), E] { o = Box.new(1u64); return Ok(()) }
func f() -> Result[(), E] { var b: Box[u64]; make(out b)?; return Ok(()) }
func g() { var b: Box[u64]; match make(out b) { Ok(_) => {}; Err(_) => return; } }
```
Rejected:
```kern
module m
union E { Bad }
func make(o: out Box[u64]) -> Result[(), E] { o = Box.new(1u64); return Ok(()) }
func f() -> Result[(), E] { var b: Box[u64]; r := make(out b); return Ok(()) }    // INIT_OUT_RESULT_UNHANDLED: result stored, not consumed by ? or match
func g() { var b: Box[u64]; match make(out b) { Ok(_) => {}; Err(_) => {}; } }    // INIT_CONDITIONAL: b initialized on the Ok path only
```

### Rule 5.2.4 — Scalar and sequence access
`view[T]` and `edit[T]` denote access to exactly one value of type `T`. `[]T` is the slice type: an unsized contiguous sequence of `T` with a length. `view[]T` and `edit[]T` are shorthand for `view[[]T]` and `edit[[]T]`: access to a sequence, with `len(v)` defined, element projection `v[i]` and sub-slice projection `v[i:j]`. An array place `[N]T` coerces to a sequence access at a call site. A scalar access is one pointer; a sequence access is a (pointer, length) pair.

## 5.3 Named access, projections, and the elision rule
### Rule 5.3.1 — The three binding forms
Only three forms bind an access to a name; all are lexically scoped and the access ends at the closing brace. (1) `with n1, ..., nk := <projection> { ... }` binds one name per access returned by the projection. Alternatively the subject is a move-only owned value: the block then owns it, `n1` is an `edit` of it for the block, and it is destroyed at block exit, including early exit (`thread.scope() -> own Scope` is used this way; `Scope`'s destructor joins every thread it spawned). (2) `match <place or projection> { Variant(a, b) => ... }` — arm bindings are views (edits, when the subject is an edit) of the matched place for the arm. Alternatively the subject is an owned value (a temporary, or `move x` for an owned local): the `match` consumes it; on entering an arm the bound payload fields are moved into arm-owned locals, unbound payload fields are destroyed, and nothing of the subject remains. An owned subject whose type declares a destructor must not be destructured this way; match it as a place and use `take` on its fields. (3) `for x in <place or projection> { ... }` yields a copy of each element when the element type is `Copy`, else a per-iteration `view` (or `edit`, when iterating an `edit`).
Diagnostics: `OWN_DESTRUCTURE_DESTRUCTOR`
Accepted:
```kern
module m
union Slot { Empty; Full(Box[u64]) }
func f(s: own Slot) { match move s { Full(b) => drop(b); Empty => {}; } }
```
Rejected:
```kern
module m
union Slot { Empty; Full(Box[u64]) }
impl Slot { destructor(edit self) {} }
func f(s: own Slot) { match move s { Full(b) => drop(b); Empty => {}; } }    // OWN_DESTRUCTURE_DESTRUCTOR: Slot declares a destructor
```

### Rule 5.3.2 — No reference locals
There are no reference-typed `:=` locals. Binding a projection with `:=` is an error; write `with`, or consume the projection in the same statement.
Diagnostics: `ACC_UNBOUND_PROJECTION`
Accepted:
```kern
module m
struct Node { n: u64 }
func f(p: edit[Pool[Node]], id: PoolId[Node]) { with e := p.at(id) { e.n = 1u64 } }
```
Rejected:
```kern
module m
struct Node { n: u64 }
func f(p: edit[Pool[Node]], id: PoolId[Node]) { e := p.at(id); e.n = 1u64 }    // ACC_UNBOUND_PROJECTION: at returns an access
```

### Rule 5.3.3 — Projections
A projection is an expression denoting a sub-place: field access `s.f`, indexing `a[i]`, slicing `a[i:j]`, or a call to a function whose return type is `view[U]`, `edit[U]` or a tuple of those. A returned projection is expression-scoped: it must be consumed in the same statement as an argument, an assignment target, a `with`/`match`/`for` subject, or a read of a `Copy` field. (Zero-copy packet parser, evaluation §8.3.)
Accepted:
```kern
module packet
struct Header { src: u16; dst: u16; len: u16 }
union ParseErr { Short }
func parse_header(pkt: view[]u8) -> Result[Header, ParseErr] { if len(pkt) < 8 { return Err(Short) }; return Ok(Header{ src: u16(pkt[0]), dst: u16(pkt[1]), len: u16(pkt[2]) }) }
func payload(pkt: view[]u8, h: Header) -> view[]u8 { return pkt[8 : 8 + usize(h.len)] }
func deliver(dst: u16, body: view[]u8) {}
func handle(pkt: view[]u8) -> Result[(), ParseErr] {
    h := parse_header(pkt)?                                  // returned value: no access attached
    with body := payload(pkt, h) { deliver(h.dst, body) }    // returned projection: block-scoped
    return Ok(())
}
```

### Rule 5.3.4 — Elision, permission, release-escape and multi-access rules
*Elision.* A function is permitted to return `view[U]`, `edit[U]` or a tuple of those only if it has exactly one `view`/`edit` parameter (a receiver counts); every returned access is derived from that parameter and carries its root place. Two or more candidate parameters is an error; returning an access derived from a local is reported under Rule 5.2.2; an access derived from multiple sources is not expressible. *Permission.* A returned access must not be stronger than its source: a function whose single access parameter is `view` must not return `edit`. *Release-escape.* Returning an access that carries a pending release function (Rule 5.3.5), directly or through a binding, is an error: release obligations never transfer across a return, so a release-bearing access is consumed only within the function that obtained it. *Multi-access.* A function returning a tuple of two or more accesses must be implemented inside `unsafe`, and its contract block must state that the returned accesses are pairwise disjoint, enforced by a runtime check that traps (`get2(a, b)` traps when `a == b`). This reference reads the spec's "or compose only such functions" as describing what the `unsafe` body is permitted to consist of (a call to another multi-access function, with no raw-pointer work); the `unsafe` marker and the contract are still required.
Diagnostics: `ACC_AMBIGUOUS_SOURCE`, `ACC_PERMISSION_ESCALATION`, `ACC_RELEASE_ESCAPE`, `ACC_MULTI_ACCESS_SAFE`
Accepted:
```kern
module m
struct Cell { v: u64 }
func pick(xs: view[]Cell, i: usize) -> view[Cell] { return xs[i] }
func bump(c: edit[Cell]) -> edit[u64] { return c.v }
func held(m: edit[Mutex[Cell]]) -> u64 { with g := m.lock() { return g.v } }
contract { ensures "the returned accesses are disjoint: traps with kind disjoint when a == b" }
func pair(p: edit[Pool[Cell]], a: PoolId[Cell], b: PoolId[Cell]) -> (edit[Cell], edit[Cell]) { unsafe { return p.get2(a, b) } }
```
Rejected:
```kern
module m
struct Cell { v: u64 }
func pick(xs: view[]Cell, ys: view[]Cell) -> view[Cell] { return xs[0] }   // ACC_AMBIGUOUS_SOURCE: xs and ys are both candidate sources
func bump(c: view[Cell]) -> edit[u64] { return c.v }                      // ACC_PERMISSION_ESCALATION: edit returned from a view source
func held(m: edit[Mutex[Cell]]) -> edit[Cell] { return m.lock() }         // ACC_RELEASE_ESCAPE: the access carries a pending unlock
func pair(p: edit[Pool[Cell]], a: PoolId[Cell], b: PoolId[Cell]) -> (edit[Cell], edit[Cell]) { return p.get2(a, b) }   // ACC_MULTI_ACCESS_SAFE: not inside unsafe
```

### Rule 5.3.5 — Accessor release
A function returning an access is permitted to declare a release function: `func lock(view self) -> edit[T] release unlock`. The release function receives the same receiver and runs exactly once when the returned access ends: at the end of the consuming statement, at the closing brace of the binding `with`/`match`/`for` block, or on early exit from that block by `return`, `?`, `break` or `continue`. A trap (§6) runs no release functions. Release functions run in reverse order of access creation.

### Rule 5.3.6 — Runtime-exclusive escalation
`Mutex[T].lock(view self) -> edit[T] release unlock` returns an access stronger than its `view` receiver. This escalation is an enumerated unsafe operation (Rule 6.4): it is permitted only inside `unsafe`, and the implementation's documented contract must state the runtime mechanism (here, the lock) that guarantees exclusivity for as long as the access is live. The checker treats the returned `edit` as rooted at the `Mutex` value, so the mutex itself is frozen for the block; this is why a `Shared[Mutex[T]]` is viewable from many threads while each lock holder has an exclusive `edit`. Buffer-pool `pin(view self, id) -> edit[Page] release unpin` follows the same pattern. **Accepted decision (spec §13, item 4):** recursive acquisition of a `Mutex` already held by the calling thread traps with kind `explicit`.

## 5.4 Aliasing check
### Rule 5.4.1 — Access classes
Every operation on a place is one of: `view` (read); `edit` (write-capable, leaves the place initialized: in-place mutation, whole-place reassignment `place = v`, `take(place)`, `replace(place, v)`); `consume` (leaves the place uninitialized or moved: a by-value use of a move-only place, including passing to `own`, returning, storing elsewhere, and `drop(place)`); or `init`, created by passing a place as an `out` argument: it is exclusive, begins when the argument is evaluated, ends when the call returns, and conflicts with every other simultaneously live access to an overlapping path, including a second `init` of the same place in the same call (reported under Rule 5.4.4). `consume` and `init` are the strongest accesses.

### Rule 5.4.2 — Derived accesses
An operation that goes through a bound access name (a `with`/`match`/`for` binding, or an `edit`/`view` parameter) creates a derived access whose root is that binding, not the binding's own root place. A derived access must not be stronger than the binding (reported under Rule 5.3.4); `consume` through a binding is never permitted (reported under Rule 5.1.6), so moving a value out through an `edit` is done with `take` or `replace`, which are `edit` accesses. While a binding is live, its underlying root place must not be accessed except through the binding (reported under Rule 5.4.4); this is how a bound name continues, rather than conflicts with, its enclosing access.

### Rule 5.4.3 — Access lifetimes within a statement
Accesses are checked for conflict only while simultaneously live, and liveness follows the evaluation order of §4: a read of a `Copy` value is an access that begins and ends at the moment of the read, producing a value with no access attached; an argument access begins when that argument is evaluated and ends when the call returns; a returned projection's access lasts until the consuming operation completes, or for the whole block when it is a `with`/`match`/`for` subject; the `edit` access of an assignment target begins only after the right-hand side has been fully evaluated and ends when the store completes. Every live enclosing binding is live throughout the statement. Consequently `total = total + u64(b)` in a loop is accepted: the read of `total` ends before the write begins.

### Rule 5.4.4 — Conflict
Two simultaneously live accesses with the same root conflict iff their paths overlap and at least one is `edit`, `consume` or `init`. Paths overlap iff one is a prefix of the other; or they index the same array or sequence with indices that are not both compile-time constants with different values; or either is a sub-slice `a[i:j]` of the same array or sequence, unless both the slice bounds and the other index or bounds are compile-time constants and the ranges are disjoint. Accesses with different roots never conflict. A conflict involving a `consume` of a variable is reported under Rule 5.1.4; every other conflict is reported here. Any access to a root place that is already moved is reported under Rule 5.1.4. The check is syntactic path disjointness plus lexical scope: there is no non-lexical lifetime inference, the constant cases above are the only static disjointness facts, and every other pair of indices or slices into one array or sequence is treated as overlapping, so disjointness is otherwise established only at runtime by a multi-access function (Rule 5.3.4), whose check `show-bounds-checks` reports. (B-tree split, evaluation §8.1; the rejected form is its deliberate negative case.)
Diagnostics: `ACC_CONFLICT`
Accepted:
```kern
module btree
struct Node { keys: [5]u64; count: u32; children: [6]PoolId[Node] }
func insert_child(p: edit[Node], i: usize, c: PoolId[Node], k: u64) {}
func split_child(nodes: edit[Pool[Node]], parent: PoolId[Node], i: usize, right: PoolId[Node]) {
    left := nodes.get(parent).children[i]                                  // projection consumed in-statement: a Copy handle
    with l, r := nodes.get2(left, right) { r.count = 2u32; l.count = 2u32 } // runtime check: left != right; both accesses end at }
    median := nodes.get(left).keys[2]                                      // read before the next block opens
    with p := nodes.at(parent) { insert_child(p, i + 1, right, median) }
}
```
Rejected:
```kern
module btree
struct Node { keys: [5]u64; count: u32; children: [6]PoolId[Node] }
func insert_child(p: edit[Node], i: usize, c: PoolId[Node], k: u64) {}
func split_child(nodes: edit[Pool[Node]], parent: PoolId[Node], i: usize, right: PoolId[Node]) {
    left := nodes.get(parent).children[i]
    with l, r := nodes.get2(left, right) { r.count = 2u32; l.count = 2u32 }
    with p := nodes.at(parent) { insert_child(p, i + 1, right, nodes.get(left).keys[2]) }   // ACC_CONFLICT: p is a live edit into nodes; get(left) views the same root
}
```

### Rule 5.4.5 — Multi-access bindings
The names bound by a single `with` from one tuple-returning projection are distinct roots and therefore never conflict with each other; their pairwise disjointness is the responsibility of the callee's contract (Rule 5.3.4), which is why such functions are unsafe-implemented. The projection's own root place is frozen for the block as in Rule 5.4.2.

### Rule 5.4.6 — `Interior` and the aliasing guarantees
A type is `Interior` iff it or any field is a type declared with `unsafe assert Interior for T` (the standard library declares it for `Mutex`, `Atomic` and `Shared`, whose runtime state changes through a `view`). For the duration of a call: an `edit` parameter is the only live access to its place; a `view` parameter of a non-`Interior` type is one of possibly many live `view`s and no write to the place occurs; a `view` parameter of an `Interior` type is one of possibly many live `view`s and writes to the place's interior state are permitted from the declaring module's unsafe code. Generated code must preserve these guarantees. `unsafe assert Interior for T` is an enumerated unsafe operation whose obligation is that every write through a `view` of `T` is made data-race-free by the type's own synchronization.

## 5.5 Concurrency
### Rule 5.5.1 — `Send` and `Share` derivation
`Send` (the value is permitted to move to another thread) and `Share` (a `view` of the value is permitted to be read concurrently from several threads) are compiler-derived marker properties computed structurally from fields. Raw pointers, non-atomic interior-mutable cells and thread-affine handles are neither. `unsafe assert Send for T` and `unsafe assert Share for T` opt in with documented obligations. Data-race freedom follows from: within a thread `edit` is exclusive (Rule 5.4.4); across threads only `Share` values are viewed and only `Send` values are moved; cross-thread mutation happens only through an exclusive scoped `edit` argument (Rule 5.5.3), inside `Mutex`, or through atomics. Lock-free structures is permitted to use audited unsafe internals behind a safe API.

### Rule 5.5.2 — Transfer
`thread.spawn(f: func(own args...) -> R, own args...) -> Result[Thread[R], SpawnErr]`. Every argument type and `R` must be `Send`. On `Err`, the arguments have been destroyed and no thread exists. `Thread[R].join(own self) -> R` blocks until the thread completes and returns its result; destroying a `Thread[R]` without joining detaches it, and a detached thread destroys its own result when its function returns.
Diagnostics: `SEND_NOT_SEND`
Accepted:
```kern
module m
func work(b: own Box[u64]) -> u64 { return 0u64 }
func f(b: own Box[u64]) -> Result[u64, SpawnErr] { t := thread.spawn(work, move b)?; return Ok(t.join()) }
```
Rejected:
```kern
module m
struct Raw { p: RawPtr[u8] }
func work(r: Raw) -> u64 { return 0u64 }
func f(r: Raw) -> Result[u64, SpawnErr] { t := thread.spawn(work, r)?; return Ok(t.join()) }    // SEND_NOT_SEND: Raw holds a RawPtr
```

### Rule 5.5.3 — Scoped sharing
`with s := thread.scope() { ... s.spawn(f, args...) ... }`: `s.spawn` returns `Result[Join[R], SpawnErr]` and `R` must be `Send`; on `Err`, `own` arguments have been destroyed, `view`/`edit` arguments are released, and no thread exists. Arguments is permitted to be `view` (requires `Share`), `edit` (requires `Send`) or `own` (requires `Send`). Every access passed to `s.spawn` is treated as live until the end of the enclosing `with s` block, which joins every thread it spawned before the block exits, including when a later `s.spawn` returns `Err`. Therefore each such access must be rooted in a variable or binding whose scope encloses the `with s` block; an access rooted in a variable or binding declared inside the block (including one from an inner `with`/`match`/`for`, whose release function would run before the join) is an error. This block-long extension is the only cross-thread borrow in the language. `Join[R].get(own self) -> R` blocks until that thread completes, inside or after the block; `Join[R]` is `Send`. At block exit the scope joins each thread; a live `Join[R]` stores the result for a later `get` or destroys it when destroyed, and a `Join[R]` destroyed earlier makes the scope destroy the result at the join. (Scoped parallel sum, evaluation §8.4.)
Diagnostics: `ACC_LIVE_ACROSS_SPAWN`, `SHARE_NOT_SHARE`
Accepted:
```kern
module par
func sum_part(data: view[]u64) -> u64 { total := 0u64; for b in data { total = total + b }; return total }
func par_sum(data: view[]u64) -> Result[u64, SpawnErr] {
    with s := thread.scope() {
        a := s.spawn(sum_part, data[0:2])?      // view rooted in the parameter, which encloses the block
        b := s.spawn(sum_part, data[2:4])?
        return Ok(a.get() + b.get())
    }
}
```
Rejected:
```kern
module par
struct Raw { p: RawPtr[u8] }
func peek(r: view[Raw]) {}
func sum_part(data: view[]u64) -> u64 { total := 0u64; for b in data { total = total + b }; return total }
func par_sum(data: view[]u64, raw: Raw) -> Result[u64, SpawnErr] {
    with s := thread.scope() {
        j := s.spawn(peek, raw)?                                 // SHARE_NOT_SHARE: a view of Raw crosses threads
        var a: Join[u64]
        with lo := data[0:2] { a = s.spawn(sum_part, lo)? }      // ACC_LIVE_ACROSS_SPAWN: lo ends before the scope joins
        b := s.spawn(sum_part, data[2:4])?
        return Ok(a.get() + b.get())
    }
}
```

### Rule 5.5.4 — Shared ownership and `Mutex`
`Shared[T]` is an atomically reference-counted immutable value; it is `Share` iff `T: Share`, and `Send` iff `T: Send` and `T: Share` (the last owner, on any thread, destroys `T`). The count is a `usize`; `clone` when the count equals the maximum `usize` traps with kind `overflow`, so the count never wraps and destruction is never premature. `Mutex[T]` is `Send` and `Share` iff `T: Send`; `with g := m.lock() { ... }` binds `g: edit[T]` for the block, and the lock is released when the bound `edit` ends, including on every early exit of the `with` block (Rules 5.3.5, 5.3.6). An allocator shared between threads is `Shared[Mutex[A]]` or an `Interior` allocator with internal synchronization (§6).

### Rule 5.5.5 — Atomics
`Atomic[T]` for `T` in `u8 u16 u32 u64 usize i8 i16 i32 i64 isize bool` is an `Interior` type. Every ordering argument is a compile-time constant of `Ordering { relaxed, acquire, release, acq_rel, seq_cst }`: `load(view self, ord)` with `ord` in `relaxed | acquire | seq_cst`; `store(view self, v, ord)` with `ord` in `relaxed | release | seq_cst`; `swap`, `fetch_add`, `fetch_sub`, `fetch_and`, `fetch_or`, `fetch_xor` (integer `T` only, wrapping) accept every ordering; `compare_exchange` (strong) and `compare_exchange_weak` `(view self, expected, desired, success, failure) -> Result[T, T]` permit `failure` orderings of `relaxed` for `success = relaxed`; `relaxed, acquire` for `acquire`; `relaxed` for `release`; `relaxed, acquire` for `acq_rel`; `relaxed, acquire, seq_cst` for `seq_cst`; `fence(ord)` with `ord` in `acquire | release | acq_rel | seq_cst`. Any other ordering argument is an error. The memory model is the ISO C++ 2020 memory model. Volatile MMIO is a separate unsafe operation and is never conflated with atomics.
Diagnostics: `ATOMIC_INVALID_ORDERING`
Accepted:
```kern
module m
func f(a: view[Atomic[u64]]) -> u64 { return a.load(Ordering.acquire) }
```
Rejected:
```kern
module m
func f(a: view[Atomic[u64]]) -> u64 { return a.load(Ordering.release) }   // ATOMIC_INVALID_ORDERING: release is not a load ordering
```

## 5.6 Arenas and handles (library, not type system)
### Rule 5.6.1 — Arenas, pools and handles
There are no regions in the type system. `Arena[T]` (bump, freed as a whole), `Pool[T]` (free list with generation counters) and `Slab[T]` are standard-library owned containers with audited unsafe internals. A handle (`ArenaId[T]`, `PoolId[T]`) is a `Copy` integer-like value that is permitted to be stored anywhere, including inside objects in the same container; this is how trees, graphs, parent pointers and cycles are expressed in safe code. Dereference is a projection: `get(id) -> view[T]`, `at(id) -> edit[T]`, `get2(a, b) -> (edit[T], edit[T])` (traps when `a == b`). A container destroys its live contents when destroyed. (Arena AST, evaluation §8.2.)
Accepted:
```kern
module ast
union Expr { Num(i64); Add(lhs: ArenaId[Expr], rhs: ArenaId[Expr]) }
struct Ast { nodes: Arena[Expr] }
func eval(a: view[Ast], id: ArenaId[Expr]) -> i64 {
    match a.nodes.get(id) { Num(n) => return n; Add(l, r) => return eval(a, l) + eval(a, r); }   // arm bindings are views; l, r are Copy handles
}
```

### Rule 5.6.2 — Validity
A successful dereference requires, in this order: the slot index is within bounds (else trap kind `bounds`); in debug builds, when both the handle's and the container's identity tags are nonzero, they are equal (else `identity`); the slot is occupied by a live, fully initialized element (else `vacant`); the slot's generation equals the handle's generation (else `generation`). **Accepted decision (spec §13, item 3):** `get2(a, b)` validates `a`, then validates `b`, each by the sequence above, and only then compares them, trapping when `a == b`. A handle is not bound to a container: using it with a different container of the same element type is a logic error, memory-safe because bounds and generation are checked against the container actually used; in debug builds every container carries a 32-bit identity tag, drawn from a process-global atomic counter starting at 1 and never reused, stored in the handles it issues and checked on dereference; tag 0 means unchecked, and once the counter is exhausted every further container receives tag 0. Generations are 32-bit; when a slot's generation would wrap, the slot is retired and never reused. `Pool.free(id)` performs the same checks in the same order as dereference (a repeated free of a not-yet-reused slot traps `vacant`; a free whose slot was reused traps `generation`; a foreign handle traps `identity`, `vacant` or `generation` as those checks dictate), then runs the element destructor and increments the slot's generation. Logical validity (runtime) and memory safety (static) are distinct guarantees.

### Rule 5.6.3 — Reserved extension: view-kinded aggregates *(resolved: not adopted)*
*Resolution (Phase 5):* the Phase 0 classification found no §8 workload that needs a reference-holding aggregate (`docs/reports/phase-0-workload-summary.md`, count 0 of 13), and Phases 2–4 implemented all thirteen workloads without it, so the extension is not part of the language. The text below is kept as the record of what was reserved.
A struct declared `view struct` is permitted to contain `view`/`edit` fields and is itself second-class: producible only by Rule 5.3.3, bindable only by `with`/`match`/`for`, passable only as `view`/`edit`, never stored or otherwise returned. It is not in the core: Phase 0 rejects it under Rule 5.2.2, and it is added iff Phase 0 shows that at least one §8 workload requires it (Phase 0 passes only if that count is at most two).

## 5.7 Destructors
### Rule 5.7.1 — Declaration
A type declares at most one destructor, `destructor(edit self) { ... }`, in the type's own module. A type with a destructor, or with any field whose type has a destructor, is move-only (Rule 5.1.2).

### Rule 5.7.2 — Destruction order
When an owner is destroyed (scope exit, reassignment, `drop(x)`, container destruction, `Pool.free`), the runtime first runs the type's destructor with `self` bound as an `edit` access, then destroys the fields in reverse declaration order, each recursively by this rule. Array elements are destroyed from the last index to the first.

### Rule 5.7.3 — Inside the destructor
`self` is reachable only through the `edit self` binding; moving `self` or a field out is reported under Rule 5.1.6 (use `take`/`replace`). The destructor must not return a value and must not be called explicitly; `drop(x)` is the only explicit destruction operation and consumes `x`.

### Rule 5.7.4 — Non-reentrancy
Because a value has exactly one owner and destruction consumes that owner, no other access path to the value exists while its destructor runs, so the destructor of a given value runs at most once and never observes itself running. A trap inside a destructor terminates (§6).

### Rule 5.7.5 — `Copy` types
`Copy` types have no destructor by definition, and destruction of a `Copy` value is a no-op.

### Rule 6.4 — Unsafe: enumerated operations and contracts
The unsafe operations are exactly: raw pointers, pointer arithmetic, memory-mapped registers, volatile access, packed/unaligned access, inline assembly, allocator internals, `assert Send/Share/Interior`, multi-access returns and runtime-exclusive escalation (Rules 5.3.4, 5.3.6), writes through a `view` of an `Interior` type inside its declaring module, and the FFI operations of Rule 7.2 (calling an imported `extern "C"` function). `unsafe` authorizes exactly these; conventions, moves, initialization and types are still checked inside `unsafe`. Each safe wrapper over unsafe internals must carry a contract block on its declaration, `contract { invariant "..."; requires "..."; ensures "..." }`, with zero or more of each clause. A function containing `unsafe` without a contract block is an error.
Diagnostics: `UNSAFE_OP_OUTSIDE_UNSAFE`, `UNSAFE_MISSING_CONTRACT`
Accepted:
```kern
module m
extern "C" func getpid() -> i32
contract { ensures "returns the calling process id" }
func pid() -> i32 { unsafe { return getpid() } }
```
Rejected:
```kern
module m
extern "C" func getpid() -> i32
func pid() -> i32 { return getpid() }                // UNSAFE_OP_OUTSIDE_UNSAFE: imported extern "C" call outside unsafe
func pid2() -> i32 { unsafe { return getpid() } }    // UNSAFE_MISSING_CONTRACT: no contract block on a function containing unsafe
```

### Rule 6.5 — Thread-local storage
A module-level `thread_local name: T = init` declares one instance of `name` per thread, including the main thread, initialized to `init` when the thread starts. `T` must be `Copy` and `init` a literal of type `T` or `zero()`; the variable is a mutable place in every function of its module and is never moved or destroyed. Hosted builds lower it to the target's TLS; freestanding TLS is decided by ADR-011.
Diagnostics: `TLS_INVALID_DECL`
Accepted:
```kern
module m
thread_local depth: u32 = 0u32
func enter() -> u32 { depth = depth + 1u32; return depth }
```
Rejected:
```kern
module m
thread_local buf: Vec[u64] = Vec[u64].new()   // TLS_INVALID_DECL: not Copy, not a literal
```

### Rule 6.6 — Raw pointers
`RawPtr[T]` is a `Copy` address with no ownership and no `Send`/`Share`. Its operations are unsafe and require an `unsafe` block: `ptr_null[T]()`, `ptr_from_addr[T](a: usize)`, `ptr_addr(p)`, `ptr_cast[U, T](p)`, `ptr_offset(p, n: isize)` (`n` elements of `T`), `ptr_load(p) -> T` for `Copy` `T`, `ptr_store(p, own T)` (the pointee owns the stored value until it is taken), `ptr_take(p) -> T` (moves the value out), `ptr_load_volatile`/`ptr_store_volatile` for `Copy` `T` (device access; never reordered or elided), and `ptr_copy(dst, src, n)` (overlapping ranges permitted). `SystemAllocator.alloc`/`realloc`/`free` are unsafe operations with the §6 obligations (alignment a power of two at most 4096, size a multiple of it; `free` with exactly the values of one prior `alloc` or `realloc`; `realloc` keeps the common prefix and leaves the old allocation untouched on failure), trapping with kind `explicit` when violated. The pointee's representation is the §4.1 layout. The interpreter addresses a byte heap of its own; natively a raw pointer is the machine address. `size_of[T]()` and `align_of[T]()` give that layout's size and alignment (safe operations). `ptr_view(p) -> view[T]`, `ptr_edit(p) -> edit[T]`, `ptr_view_seq(p, n) -> view[]T`, `ptr_edit_seq(p, n) -> edit[]T` and `ptr_edit2(a, b) -> (edit[T], edit[T])` open accesses on raw memory; they are unsafe operations, and a function may return such an access only from inside `unsafe` (its contract then states the obligation that the memory holds a live value for the access's lifetime, in place of the elision rule's derivation from a parameter). The library containers (`Box`, `Vec`, `Pool`, `Arena`) are kern code built this way in `std/core.kern`; a trap raised inside the library reports the span of the user-level call that entered it.
Diagnostics: `UNSAFE_OP_OUTSIDE_UNSAFE`

### Rule 6.7 — Packed layout, volatile access and inline assembly
`struct S packed { ... }` lays its fields out without padding (alignment 1); every field access is an unaligned access and an unsafe operation. `ptr_load_volatile`/`ptr_store_volatile` (Rule 6.6) are the volatile MMIO operations. `asm("template")` executes an operand-free inline assembly template that may clobber memory; it is unsafe, target-specific, and traps with kind `explicit` in the interpreter. A write through a `view` of an `Interior` type is permitted only inside its declaring module and inside `unsafe` (the module's own synchronization). Each of these is enumerated by Rule 6.4.
Diagnostics: `UNSAFE_OP_OUTSIDE_UNSAFE`, `ASSIGN_IMMUTABLE`

### Rule 6.8 — Freestanding mode
`--env freestanding` builds for a bare-metal target: no operating system, no default allocator (`SystemAllocator.new` is unavailable; the interpreter's emulation traps with kind `explicit`), no threads, no library containers natively, output through the console device at the target's address (the PL011 at `0x0900_0000` on AArch64 `virt`, port `0x3F8` on x86-64 `q35`), a compiler-inserted stack-limit check in every function prologue trapping with kind `stack` (disabled only by `--unsafe-no-stack-check`, which makes stack overflow a trusted-boundary obligation), traps that write their record to the console and halt, and `exit(code)`/the end of `main` writing `{"halt": {"code": n}}` before powering the machine off. The mandatory runtime symbol set is recorded in ADR-011.

### Rule 7.2 — FFI: C ABI subset
A function declared `extern "C"` (exported or imported) uses the target's C calling convention and an unmangled symbol equal to its name. Its parameter and return types must be integer types, `f32`, `f64`, `bool`, raw pointers, or `Copy` structs whose fields are recursively of these types (laid out per Rule 4.1.5); any other type, including `own`/`view`/`edit`/`out` parameters, slices, unions and generics, is an error. Variadic functions are not supported. Calling an imported `extern "C"` function is an unsafe operation (Rule 6.4); exporting one is safe, and a trap inside it terminates per §6.
Diagnostics: `FFI_UNSUPPORTED_TYPE`
Accepted:
```kern
module m
extern "C" func write(fd: i32, buf: RawPtr[u8], n: usize) -> isize
```
Rejected:
```kern
module m
extern "C" func write(fd: i32, buf: view[]u8) -> isize    // FFI_UNSUPPORTED_TYPE: a sequence access is not a C ABI type
```

## Trap handlers
A trap (§6) runs no destructors and no release functions, unwinds no frames, and must not be caught or resumed. `trap.set_handler(f: func(view TrapInfo)) -> func(view TrapInfo)` installs `f` process-wide as a single atomic `seq_cst` exchange and returns the previously installed handler; a trapping thread selects the handler by one `seq_cst` load at the moment of the trap, runs it at most once, and then terminates (halt in freestanding mode, process abort in hosted mode). A trap inside the handler terminates immediately. **Handler obligation (ADR-012):** a custom handler is a trusted boundary. It may run in signal context on an interrupted thread (a hosted stack trap is a guard-page fault), so it must not allocate, take a lock, spawn, perform buffered I/O, or call FFI or a custom allocator that does any of these; the guaranteed-safe operations are the runtime's unbuffered `print_*` output, atomics, and computation on `TrapInfo` and pre-existing data. The obligation applies to every trap kind and the runtime performs no checks for it: a violation may deadlock the process or trap a second time, in which case the nested-trap path terminates it by `SIGABRT`; memory safety is unaffected.

**Obligation on custom handlers (ADR-012).** A custom trap handler is a documented trusted-boundary obligation, not a runtime-enforced restriction. The handler is permitted to run in signal context on an interrupted thread (for example after a stack trap taken while the thread holds a `Mutex` or is in the middle of buffered output), so it must not allocate, take a lock, spawn, perform buffered I/O, or call FFI or a custom allocator that does any of these. The only guaranteed-safe operations are `write(2)`-based unbuffered output, atomics, and computation on `TrapInfo` and pre-existing data. The obligation applies uniformly to every trap kind (`bounds`, `overflow`, `div_zero`, `shift`, `conversion`, `generation`, `vacant`, `identity`, `disjoint`, `oom`, `stack`, `explicit`, `unreachable`), and the runtime performs no checks for it. Violating it is permitted to deadlock the process or trap a second time, in which case the nested-trap path terminates the process; memory safety is unaffected. The default handler and `std/testing.console_trap_handler` satisfy the obligation by construction. The trap handler is the only cleanup hook: programs that must flush or release external resources on failure do so there, within this obligation, without relying on destructors.
