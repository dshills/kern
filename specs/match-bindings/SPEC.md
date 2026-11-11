# Match arm bindings of Copy payload fields

**Version:** 0.4 (2026-11-11). **Scope:** Rule 5.3.1 of `docs/ownership.md`
(the `match` form) and the comment of its §8 arena example; the resolver's
treatment of a `match` arm's bindings (`compiler/resolve`); the checker's
freeze of a `match` subject (`compiler/check`); fixtures under `tests/run`,
`tests/negative` and `tests/positive`; the fixture
`tests/run/release_accessor.kern`; `CHANGELOG.md`; the report
`docs/reports/match-bindings.md`.
**Language impact:** yes. Rule 5.3.1 is reworded. A program that writes
through an arm binding of a `Copy` payload field changes meaning or stops
compiling (§5). A program that does not is compiled as before, except that
some programs refused today are accepted (M6). No diagnostic code is added,
removed or moved to another rule; `spec/rules.lock` is unchanged.
**Implementation note (not a requirement):** the lowering
(`compiler/lower`) already follows the kind the resolver gives a binding,
so the change is expected in the resolver and the checker alone.
**Decided by the owner (2026-11-11):** the rule of §2, "an edit only when
written", over the two alternatives of §6.

## 0. The defect

Rule 5.3.1 says of `match <place or projection> { Variant(a, b) => ... }`
that "arm bindings are views (edits, when the subject is an edit) of the
matched place for the arm". The compiler does that only for a payload field
whose type is not `Copy`. A binding of a `Copy` field is a copy, made on
entering the arm, in an arm-owned local. The copy is writable, so this
program is accepted and prints 5, in the interpreter and natively:

```kern
var o: Option[u64] = Some(5u64)
match o {
    Some(n) => { n = n + 1u64 }      // changes the copy
    None => { }
}
match o {
    Some(n) => { print_u64(n) }      // 5
    None => { }
}
```

The same holds for a field of a `Copy` struct payload (`p.x = 1u64`), for a
binding passed to an `edit` parameter, and for a nested `match` on a `Copy`
binding. In a generic body the field's type is a type parameter, which is
not `Copy` unless constrained so, and the binding is an edit: the same text
writes to the subject when it is generic and to a copy when it is not.

The copy is not an accident to be removed. Because a copy is not an access,
an arm that binds only `Copy` fields leaves its subject free, which programs
rely on: walking a list by reassigning the matched variable
(`match cur { Some(id) => { cur = next_of(id) } ... }`), and calling a
function with the container whose element is matched
(`match a.nodes.get(id) { Add(l, r) => return eval(a, l) + eval(a, r) }`,
the reference's own §8 example). Measured on 2026-11-11 over the 2,113 kern
files of `tests`, `examples`, `bench` and `std`: making every binding an
access, as Rule 5.3.1 reads, makes 88 of them fail with `ACC_CONFLICT`,
among them `std/net.kern`; and exactly one file, `tests/run/release_accessor.kern`,
writes through a `Copy` arm binding at all.

## 1. Terms

- **Place match:** a `match` whose subject is a place or a projection, that
  is, not an owned value (a temporary, or `match move x`). Only place
  matches are in scope; M9 covers the rest.
- **Editable subject:** a place match's subject through which the compiler
  today gives a non-`Copy` field's binding the `edit` permission (an `edit`
  access or a path rooted in an `edit` binding or parameter, in an owned
  local, or in an `own` or `out` parameter). The determination is unchanged.
  Every other place match has a **view subject**.
- **Copy binding:** a name a pattern of a place match binds to a payload
  field whose type is `Copy` where the `match` is written. In a generic
  body a type parameter is `Copy` only when it is constrained `Copy`.
- **Edit use** of a Copy binding `n`: an occurrence, anywhere in the body of
  the arm that binds `n`, a closure written there included, of one of the
  following, in which *a path of `n`* is `n` itself, a field or an element
  of a path of `n`, or a slice of a path of `n`:
  1. an assignment whose target is a path of `n`;
  2. a path of `n` as the argument of an `edit` parameter;
  3. a path of `n` as the receiver of a method whose receiver is `edit self`;
  4. `take(p)` or `replace(p, v)` where `p` is a path of `n`;
  5. `return p`, where `p` is a path of `n`, from a function whose return
     type is an `edit` access;
  6. an edit use, by items 1 to 5, of a binding derived from `n`.

  A binding is **derived from `n`** when it is bound by a `with` whose
  subject is a path of `n`, or by a pattern of a place match whose subject
  is a path of `n`, or when it is derived in one of these two ways from a
  binding derived from `n`; items 1 to 5 are then read with the derived
  binding in the place of `n`. (Of the paths of `n`, today's compiler
  accepts only a slice as the subject of a `with`; the definition covers
  every path, so that it holds unchanged if more are accepted.)

  Nothing else is an edit use. In particular these are not: reading a path
  of `n`; passing it by value or to a `view` parameter; calling a
  `view self` method on it; slicing it and reading the slice, bound by a
  `with` or not; matching it without item 6; and iterating it with `for`,
  whose variable is a copy of a `Copy` element (M9), so that a write to
  the variable changes that copy and is not a write through `n`.

## 2. Requirements

- **M1.** A Copy binding with no edit use is a copy: on entering the arm
  the field's value is copied into a local the arm owns. It is not an
  access, and it does not keep the subject frozen (M6). This is today's
  behaviour, unchanged.
- **M2.** A Copy binding with an edit use, in a place match with an
  editable subject, is an `edit` of the payload field, for the whole arm
  (from its opening brace, not from the first edit use). Every read and
  every write of a path of the binding is a read or a write of the field in
  the subject. After the arm, the subject holds what the arm wrote.
- **M3.** An edit use of a Copy binding in a place match with a view
  subject is an error, reported at the edit use with the code and rule that
  the same use of a view binding of a non-`Copy` field has today:
  `ASSIGN_IMMUTABLE` for item 1, `ACC_PERMISSION_ESCALATION` for items 2 to
  5. Under item 6 it is reported at the edit use of the derived binding,
  with the code that use has.
- **M4.** A binding under M2 is an access binding in every respect.
  In particular `move n`, `drop(n)` and `n` as an `out` argument are refused,
  wherever in the arm they stand relative to the edit use, with the code a
  non-`Copy` field's binding gets for the same expression today
  (`NAME_UNRESOLVED`, rule T.1, for `move` and `drop`; `PLACE_REQUIRED` for
  `out`, whose argument must name an uninitialized local or an `out`
  parameter).
- **M5.** Whether a Copy binding falls under M1 or M2 is decided per binding
  and per arm: two bindings of one pattern, and the same field bound in two
  arms, are decided independently.
- **M6.** The subject of a place match is frozen (Rule 5.4.2: its root place
  must not be accessed except through the bindings) within an arm if and
  only if that arm has at least one access binding: a binding of a
  non-`Copy` field, or a Copy binding under M2. In every other arm (one
  that binds nothing, a wildcard arm, an arm all of whose bindings fall
  under M1) the subject is not frozen: the arm is checked exactly as an arm
  is checked today in a match none of whose arms has an access binding, so
  it is accepted when it reads, writes or reassigns the subject. Today the
  subject is frozen in every arm when any arm has an access binding; M6
  accepts programs that were refused and refuses none that was accepted,
  other than through M2. M6 is about the freeze alone. The access the
  subject expression itself yields, and a release function that comes with
  it (`match m.lock() { ... }`, Rule 5.3.5), keep the lifetime they have
  today in every arm: the release function runs once, when the `match`
  ends, and what an arm that is not frozen then does with the place the
  access came from (locking `m` again, say) is accepted or refused, and
  behaves at run time, as it does today in a match with no access binding.
- **M7.** An arm under M6's freeze that accesses the subject other than
  through its bindings is refused with `ACC_CONFLICT` (Rule 5.4.4) at that
  access, as today. When the freeze exists only because of M2 (the arm has
  no binding of a non-`Copy` field), the diagnostic carries one related
  span, at the first edit use in source order of a Copy binding of that
  arm, labelled `the arm writes NAME here, which makes NAME an edit of the
  matched place` with the binding's name for NAME.
- **M8.** In a generic body, a binding of a field whose type is a type
  parameter not constrained `Copy` is an access binding, as today, for
  every instantiation. With M2, a write through an arm binding of a place
  match, or through a binding derived from one, therefore reaches the
  subject in every program that compiles, generic or not.
- **M9.** Unchanged: a `match` with an owned subject (its payload fields are
  moved or copied into arm-owned locals, which are writable; nothing of
  the subject remains); bindings of non-`Copy` fields; `with`; and `for`,
  whose variable is a copy for a `Copy` element (Rule 5.3.1 (3)), so that
  assigning to it changes the copy.
- **M10.** The interpreter, a native debug build and a native release build
  agree on every program of §4.

## 3. The reference

- **D1.** In `docs/ownership.md` Rule 5.3.1, the sentence on form (2) up to
  "for the arm." is replaced by:

  > (2) `match <place or projection> { Variant(a, b) => ... }` — arm
  > bindings are views (edits, when the subject is an edit) of the matched
  > place for the arm, and while an arm has such a binding the matched place
  > is reached only through the arm's bindings (Rule 5.4.2); an arm without
  > one leaves it free. A binding of a `Copy` payload field that the arm
  > only reads is instead a copy made on entering the arm, which is not an
  > access. It is an edit of the field when the arm uses it as one (assigns
  > to it or to a part of it, passes it or a part of it to an `edit`
  > parameter or an `edit self` method, `take`s or `replace`s it, returns it
  > as an edit, or does one of these through a binding that a nested `match`
  > or a `with` makes from it), and that use is an error when the subject is
  > not an edit.

  The rest of the rule is unchanged.
- **D2.** The comment of the §8 arena example
  (`// arm bindings are views; l, r are Copy handles`) becomes
  `// l and r are Copy handles the arm only reads: copies, so a is free for the calls`.
- **D3.** `CHANGELOG.md`, under `[Unreleased]`, "Language": one "Fixed" entry
  for M2 and M3 that names the three ways a program changes (§5), and one
  entry for M6.
- **D4.** `docs/reports/match-bindings.md` records the measurement of §0, the
  decision of §6, and the result of acceptance checks A6 and A7.

## 4. Acceptance criteria

Each fixture runs in the three configurations of M10 through the fixture
harness, and the `tests/run` fixtures also under the sanitized corpus.

- **A1.** `tests/run/match_binding_edit.kern` with its `.out`. Each case
  prints the subject after the match: a write by assignment
  (`Some(n) => { n = n + 1u64 }` on a local `Option[u64]` holding 5 prints
  6); a field of a `Copy` struct payload; an element of an array payload;
  an `edit` parameter; an `edit self` method of a `Copy` struct; `replace`
  and `take`; a write inside a closure the arm calls; a nested match
  (`Option[Option[u64]]`); a pattern with two Copy bindings of which one is
  written (M5); the subject reached through an `edit` parameter, through a
  field of an owned local, and through a `Pool.at` projection; a function
  that returns an edit of a Copy binding's field; a `with` over a slice of
  an array payload that writes an element (item 6), a nested match inside
  such a `with` that writes its binding, and a `with` over a slice that
  only reads, after which the subject is reassigned in the same arm; and a
  generic function
  with an unconstrained type parameter instantiated at `u64`, which prints
  the same as its non-generic twin (M8).
- **A2.** `tests/run/match_binding_free.kern` with its `.out`, for M1 and
  M6: a list walk that reassigns the matched variable in an arm that only
  reads its binding; `match o { Some(n) => { n = n + 1u64 } None => { o = Some(0u64) } }`
  applied to `Some(1)` and to `None`; a union with a non-`Copy` payload
  whose payload-free arm reassigns the subject; and a subject that is an
  accessor with a release function (a counter of releases prints 1 after a
  match whose arm writes its Copy binding, and 1 after a match whose arms
  bind nothing).
- **A3.** Negative fixtures, one per line, each `expect: reject` with the
  code and rule named: assignment with a view subject (`ASSIGN_IMMUTABLE`);
  an `edit` argument, an `edit self` receiver, `take` and `replace` with a
  view subject (`ACC_PERMISSION_ESCALATION`); an arm that writes its Copy
  binding and reassigns the subject (`ACC_CONFLICT`, with the related span
  of M7 checked by a unit test of the checker); `move n` before and after
  an edit use of `n`, and `drop(n)` before one (`NAME_UNRESOLVED`); `n` as
  an `out` argument in an arm
  that writes `n` (`PLACE_REQUIRED`); a nested match that writes with a
  view outer subject (`ASSIGN_IMMUTABLE`); a write through a `with` over a
  slice with a view subject (`ASSIGN_IMMUTABLE`).
- **A4.** `tests/run/release_accessor.kern`: its arm
  `Some(n) => { n = n + 1u64 }` now writes the guarded value. The fixture is
  changed to print that value after the match, and its `.out` shows the
  incremented value.
- **A5.** Tools report what the rule decides. For the program of §0 the
  language server's hover on `n` begins `binding edit n: u64` in the first
  `match` and `local n: u64` in the second.
- **A6.** Corpus invariance: `kern check` over every `.kern` file of
  `tests`, `examples`, `bench` and `std` gives the same diagnostics before
  and after the change, except for the files this spec adds or changes.
- **A7.** `go test ./...` passes on macOS; the Linux gate passes for
  `./compiler/...` and `./tests`; `golangci-lint run ./...` reports nothing.

## 5. Effect on existing programs

Only a program with an edit use of a Copy binding in a place match is
affected by M2 to M4:

1. With an editable subject, the write now reaches the subject, where it
   changed a copy that was then discarded. A read of the binding after a
   write made through the binding itself gives the value written, as
   before; what differs is the subject after the arm. A write made through
   a derived binding (a nested match's, or a `with` over a slice) changed a
   second copy and left the binding as it was; it now changes the binding,
   so a later read of the binding in the same arm gives the value written.
2. With a view subject, the program no longer compiles (M3). To keep the
   old meaning, copy the binding into a local and write that.
3. If the arm also accesses the subject other than through its bindings,
   it no longer compiles (M7), with the same remedy.

M6 changes no accepted program.

## 6. Alternatives the owner declined

- **Every binding an access** (Rule 5.3.1 as written): 88 of 2,113 files
  fail, the idioms of §0 with them.
- **Copies that must not be written:** a Copy binding is always a copy and
  an edit use is an error. One rule fewer, but a write through an arm
  binding would reach the subject in generic code only.

## 7. Non-goals

- A `for` variable of a `Copy` element stays a writable copy (M9), although
  assigning to it is lost in the same way. It is documented behaviour;
  changing it is a separate decision.
- No new syntax (a marker on a pattern to ask for an edit or a copy).
- Which subjects are editable (§1) is not revisited, a module-level
  variable's included.
- `examples/klisp` keeps replacing whole cells in `set_car` and `set_cdr`.
