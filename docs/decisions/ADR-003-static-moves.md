# ADR-003: Static moves, no partial moves, no drop flags

**Status:** Decided
**Date:** 2026-10-26
**Spec:** §4 (evaluation order), §5.1, §5.3 (owned `match` subject), §5.7, §7 (IR invariant 1);
evaluation §4.1, §4.10.
**Depends on:** ADR-001. **Interacts with:** ADR-005 (`out`), ADR-006 (trap paths).

## Problem

Deterministic destruction with no garbage collector requires the compiler to know, at every
program point, whether each owner still holds its value. Rust answers this with dynamic drop
flags for conditionally moved locals and with per-path tracking of partial moves out of
fields; both make the destructor call site a runtime question and complicate the IR, the
unsafe-code obligations, and the explanation an agent receives. kern needs the rule that keeps
every destructor call site static while keeping moves implicit enough that ordinary code does
not read like a linear calculus.

## Alternatives

- **Rust: implicit moves, drop flags, partial moves** [rust-reference] [rustonomicon].
  Rejected: drop flags are hidden runtime state the IR validator cannot check statically,
  and partial moves make move tracking per path instead of per variable.
- **Austral: strict linear types** [austral-spec]. Every value must be consumed exactly once;
  there is no implicit drop. Rejected: verbose, and every early return must thread every
  live value by hand. kern values are affine: dropping at scope exit is implicit.
- **C++: copy by default, moved-from objects stay valid** [iso-cpp20]. Rejected: implicit
  deep copies violate spec §3 principle 2, and a valid-but-unspecified moved-from state is
  exactly the use-after-move the safety contract forbids.
- **Hylo `sink` with explicit copy** [hylo-tour] [hylo-mvs]. Adopted in substance: by-value
  use of a non-`Copy` value moves it; copies are explicit (`copy(x)` or `clone`).
- **Explicit move keyword at every by-value use** (as in Mojo's `^` transfer sigil)
  [mojo-origins]. Rejected as the general rule: it adds a keyword at every `own` argument for
  no checker benefit. kern keeps `move x` only where the grammar needs an owned subject that
  is a named local (`match move x`, `move ident` as a primary).

## Invariants

1. `Copy` is derived (spec §5.1): primitives, raw pointers, function pointers and container
   handles; an aggregate iff it declares no destructor, does not opt out, and every field or
   element is `Copy`. Everything else is move-only. `Copy` values are duplicated bitwise on
   use; deep copies are explicit.
2. Moves are implicit at any by-value use of a move-only value: passing to `own`, assignment,
   return, storing into a field or container. A move invalidates the source
   (`OWN_USE_AFTER_MOVE` on any later access of the root place).
3. Moved-ness is static. A join where one predecessor moved a variable and another did not is
   `OWN_CONDITIONAL_MOVE` unless the variable is never used again on any path including scope
   exit; the machine-applicable fix inserts `drop(x)` on the non-moving path.
4. No partial moves: moving out of a field or element is `OWN_PARTIAL_MOVE`; use
   `take(place)` (leaves the type's declared default) or `replace(place, v)`. Move tracking is
   per variable. `consume` through a binding is never permitted.
5. Definite initialization is flow-sensitive; an ordinary local is initialized only as a
   whole (`INIT_UNINITIALIZED_READ`, `INIT_FIELD_OF_UNINITIALIZED`).
6. IR invariant 1: every ownership instance performs exactly one of `drop`, `move` out, or
   trap on every path from initialization to function end; no use follows its `drop` or
   `move`. No drop flags exist (implementation note in §5.1, non-normative).

## Hard counterexample

The rule rejects the conditionally moved local that Rust accepts with a drop flag, and the
partial move that Rust accepts with per-path tracking:

```text
func route(flag: bool, own msg: Box[Msg]) {
    if flag {
        deliver(msg)                     // own parameter: msg is moved on this path only
    }
}                                        // OWN_CONDITIONAL_MOVE: scope exit must destroy msg on the other path

struct Pair { a: Option[Box[u8]]; b: Option[Box[u8]] }

func split(own p: Pair) -> Option[Box[u8]] {
    return p.a                           // OWN_PARTIAL_MOVE: moving out of a field
}
```

The accepted forms make the destructor site static:

```text
func route(flag: bool, own msg: Box[Msg]) {
    if flag {
        deliver(msg)
    } else {
        drop(msg)                        // the machine-applicable fix; both paths end moved
    }
}

func split(own p: Pair) -> Option[Box[u8]] {
    return take(p.a)                     // p.a is left as None; p is destroyed whole at exit
}
```

An owned `match move p { ... }` destructures the whole value instead, moving bound payload
fields into arm-owned locals and destroying the rest (spec §5.3), except for types with a
destructor (`OWN_DESTRUCTURE_DESTRUCTOR`).

## Expressiveness cost

`if c { consume(x) }` must be written with an explicit `drop(x)` on the other path. Field
extraction needs `take` or `replace`, so a type whose field is extracted must declare a
default for that field's type (or wrap it in `Option`). A type with a destructor cannot be
destructured by an owned `match`. These are local rewrites with machine-applicable fixes;
no program becomes inexpressible.

## Performance cost

None at runtime, and a saving: no drop flags, no runtime moved-state tests, no conditional
destructor calls. `take` costs one write of the default value. Static destructor sites are
what makes ADR-006's "no landing pads" possible and what the Phase 2 backend relies on
(PLAN.md 2.1 risk: the guarantee must survive monomorphization, guarded by the validator).

## Complexity

Move state is one bit per variable per program point, computed by the same flow-sensitive
pass as definite initialization. There is no per-path or per-field move lattice. The IR
validator checks invariant 1 mechanically after every pass, so an optimization that
duplicates or loses a `drop` is an internal error, not a silent leak.

## Experiments

- PLAN.md 0.4: reject fixtures `OWN_USE_AFTER_MOVE`, `OWN_CONDITIONAL_MOVE`,
  `OWN_PARTIAL_MOVE`, `OWN_DESTRUCTURE_DESTRUCTOR`, `INIT_UNINITIALIZED_READ`,
  `INIT_FIELD_OF_UNINITIALIZED`, `INIT_CONDITIONAL`, each with an accepting twin, and the
  `ACC_CONFLICT` consume/view variant.
- PLAN.md 0.5: `examples/vector/vector.kern` (owned buffer, `take`/`replace` on elements).
- PLAN.md 1.1 and 1.4: the IR validator's invariant 1, with a deliberately corrupted IR file
  under `tests/ir/` (duplicated and missing `drop`); accept fixtures whose `.out` files
  record destruction order on early return and `?` propagation (§5.1, §5.7).
- PLAN.md 2.5: `show-copies` reporting every bitwise and explicit-deep copy, confirming no
  implicit deep copy exists in the §8 containers.
- Evaluation §4.1 marks the rule as relaxable by adding drop flags if the fixtures show it
  is too annoying; the Phase 0 report records whether any required program needed the
  `drop(x)` fix more than locally.

## Conclusion

Implicit moves at by-value use, explicit deep copies, static moved-ness with
`OWN_CONDITIONAL_MOVE` and its `drop(x)` fix, no partial moves (`take`/`replace`), and
therefore no drop flags and static destructor sites in the IR.
