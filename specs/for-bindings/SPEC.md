# Loop variables of Copy elements

**Version:** 0.5 (2026-11-11). **Scope:** Rule 5.3.1 of `docs/ownership.md`
(forms 2 and 3); `spec/types.md` §7 (the cursor protocol); the resolver's
treatment of a `for` loop's variables (`compiler/resolve`); the amendment of
`specs/match-bindings/SPEC.md` to v0.5; fixtures under `tests/run` and
`tests/negative`; the fixture `tests/run/release_accessor.kern`;
`CHANGELOG.md`; the report `docs/reports/for-bindings.md`.
**Language impact:** yes. A program that writes through a loop variable of a
`Copy` element changes meaning or stops compiling (§5). A function that does
not is compiled to the same code as before (F12). No diagnostic code is
added, removed or moved to another rule; `spec/rules.lock` is unchanged.
**Follows:** `specs/match-bindings/SPEC.md` (v0.4, released in v0.2.2),
whose rule for a `match` arm's binding this gives to a loop variable, and
whose terms it uses.
**Decided by the owner (2026-11-11):** the rule of §2, "an edit when
written", over the two alternatives of §6.

## 0. The defect

Rule 5.3.1 says that `for x in <place or projection>` "yields a copy of
each element when the element type is `Copy`, else a per-iteration `view`
(or `edit`, when iterating an `edit`)". The copy is a writable local, so
this function is accepted and changes nothing in `data`, in the
interpreter and natively:

```kern
func translate(data: edit[]u8, table: view[[256]u8]) {
    for b in data { b = table[usize(b)] }
}
```

It is the loss `specs/match-bindings` removed from `match`, in the other
binding form, and it is documented, so it was left there for the owner to
decide. Measured on 2026-11-11 over the 2,127 kern files of `tests`,
`examples`, `bench` and `std`: three files write through a loop variable of
a `Copy` element. One is the fixture `tests/run/release_accessor.kern`. The
other two are programs agents wrote in recorded runs of the agent suites
(`bench/agent/runs/20261102-134710/t10_buffer_review-plain-3376311375/buf.kern`,
which is the function above, and
`bench/agent/runs/20261101-165447/t04_image_ops-structured-808147416/image.kern`),
and both mean to change the sequence. None writes through a variable of a
cursor loop.

Unlike a `match`, a `for` keeps its sequence frozen for the whole loop
whatever its variable is, so making the variable an access costs no
program its freedom to reach the sequence: it had none.

## 1. Terms

- **Element loop:** a `for` over an array, a sequence, or a type with the
  cursor protocol (`spec/types.md` §7). A `for` over a range is not one.
- **Loop variable:** a name an element loop binds to an element: the one
  name of `for x in xs`; the second name of `for i, x in xs` over an array
  or a sequence (the first is the index and is not a loop variable); each
  of the two names of `for k, v in c` over a type with the cursor protocol.
- **Copy variable:** a loop variable whose element type is `Copy` where the
  loop is written. In a generic body a type parameter is `Copy` only when
  it is constrained `Copy`.
- **Editable for a variable:** the loop gives a loop variable of an element
  that is not `Copy` the `edit` permission today. That is: over an array or
  a sequence, the iterated expression is an `edit` access or a mutable
  place; over a type with the cursor protocol, the iterated place is
  mutable (or an `edit`), the type has the `_mut` getter
  (`cursor_get_mut`, `cursor_entry_mut`), and the access that getter
  returns for that name is an `edit`. (For `map.Map` the value is editable
  and the key is not: `cursor_entry_mut` returns `(view[K], edit[V])`.)
- **Edit use** of a Copy variable `x`: an edit use as
  `specs/match-bindings/SPEC.md` §1 defines it (items 1 to 6), read with
  `x` in the place of the arm binding `n` and the loop's body in the place
  of the arm's.

## 2. Requirements

- **F1.** A Copy variable with no edit use is a copy: at the start of each
  iteration the element's value is copied into a local. This is today's
  behaviour, unchanged (F12).
- **F2.** A Copy variable with an edit use, in a loop that is editable for
  it, is an `edit` of the element for each iteration, from the start of the
  body. Every read and every write of a path of the variable is a read or a
  write of the element. After the loop, the sequence holds what the body
  wrote.
- **F3.** An edit use of a Copy variable in a loop that is not editable for
  it is an error, reported at the edit use with the code and rule
  `specs/match-bindings` M3 names for the same use: `ASSIGN_IMMUTABLE` for
  an assignment, `ACC_PERMISSION_ESCALATION` for an `edit` parameter, an
  `edit self` receiver, `take`, `replace` and a returned edit.
- **F4.** A variable under F2 is an access binding in every respect:
  `move x`, `drop(x)` and `x` as an `out` argument are refused wherever in
  the body they stand, with `NAME_UNRESOLVED` for `move` and `drop` and
  `PLACE_REQUIRED` for `out`, as `specs/match-bindings` M4 has it for an
  arm's binding.
- **F5.** F1 or F2 is decided per variable and per loop: the two names of a
  cursor loop, and the variables of nested loops, are decided
  independently, subject to F6.
- **F6.** Derived bindings, amending `specs/match-bindings` §1 (D4):
  - a Copy variable of a loop over a path of a Copy arm binding, or of
    another Copy variable, is derived from it (so in
    `for row in grid { for c in row { c = 0u64 } }` the write to `c` is an
    edit use of `row`, and reaches `grid`);
  - a binding that a `match` or a `with` makes from a path of a Copy
    variable is derived from it.

  Only a Copy variable is derived this way. A loop variable of an element
  that is not `Copy` is the access its getter returns, as today, and what
  the loop's own getter call does to the binding it is called on
  (`cursor_get_mut` takes `edit self`, which is an edit use by
  `specs/match-bindings` §1 item 3) is as in v0.2.2. So a function gains
  an access binding from this specification only through an edit use of a
  Copy variable.
- **F7.** A cursor loop with a variable under F2 uses the type's `_mut`
  getter, and is in every respect the loop it would be today if that
  variable's element were not `Copy`. In particular it binds an edit, so a
  loop nested in it over the same place is `ACC_CONFLICT`
  (`spec/types.md` §7), where a loop whose variables are all copies or
  views lets one nest.
- **F8.** The freeze of the iterated place is unchanged: it holds for the
  whole loop in every element loop, as today.
- **F9.** Unchanged: a `for` over a range, whose variable is a local
  holding the counter's value; the index of `for i, x in xs`; a loop
  variable of an element that is not `Copy`.
- **F10.** In a generic body, a loop variable whose element type is a type
  parameter not constrained `Copy` is an access binding, as today, for
  every instantiation. With F2, a write through a loop variable therefore
  reaches the sequence in every program that compiles, generic or not.
- **F11.** The interpreter, a native debug build and a native release build
  agree on every program of §4.
- **F12.** A function in which no Copy variable has an edit use (in its
  own loops or in a closure written in it) is compiled to the code it is
  compiled to today, in both configurations. A function in which one has
  is outside this requirement as a whole.

## 3. The reference

- **D1.** In `docs/ownership.md` Rule 5.3.1, form (3) is replaced by:

  > (3) `for x in <place or projection> { ... }` yields a per-iteration
  > `view` of each element (an `edit`, when iterating an `edit`). A variable
  > of a `Copy` element that the body only reads is instead a copy of the
  > element. It is an edit of the element when the body uses it as one, in
  > the ways form (2) lists for an arm's binding, and that use is an error
  > when the loop gives that variable no edit: it iterates a view, or it
  > iterates a type with the cursor protocol whose `_mut` getter is missing
  > or returns a view for that name (`spec/types.md` §7).

- **D2.** In form (2) of the same rule, "through a binding that a nested
  `match` or a `with` makes from it" becomes "through a binding that a
  nested `match` or a `with` makes from it, or through the variable of a
  `for` over it when that variable's element is `Copy`".
- **D3.** In `spec/types.md` §7, the sentence "A `Copy` element is bound as
  a copy, as in a `for` over a sequence." becomes: "A `Copy` element is
  bound as a copy when the body only reads it, as in a `for` over a
  sequence; when the body uses it as an edit, the loop uses the `_mut`
  getter as for an element that is not `Copy`, and that use is an error
  where the place is not mutable, the type has no `_mut` getter, or the
  getter returns a view for that name."
- **D4.** `specs/match-bindings/SPEC.md` becomes v0.5: in §1, "derived from
  `n`" also covers the Copy variable (and only a Copy variable, F6) of a
  `for` over a path of `n`; the
  list of what is not an edit use no longer names `for`; M9 no longer lists
  `for` as unchanged, and points here; and its §7 non-goal about the `for`
  variable is replaced by a sentence that points here.
- **D5.** `CHANGELOG.md`, under `[Unreleased]`, "Language": one "Fixed"
  entry that names the ways a program changes (§5).
- **D6.** `docs/reports/for-bindings.md` records the measurement of §0, the
  decision of §6, the result of acceptance checks A6 to A8, and that the
  two recorded agent programs of §0 would behave differently if they were
  run again (their recorded results stand as measured).

## 4. Acceptance criteria

Each fixture runs in the three configurations of F11 through the fixture
harness, and the `tests/run` fixtures also under the sanitized corpus.

- **A1.** `tests/run/for_binding_edit.kern` with its `.out`. Each case
  prints the sequence after its loop: the function of §0 applied to a
  slice; an array local; `for i, x in xs` writing `x`; a `Vec` through
  `as_edit()`; a field of a `Copy` struct element; an `edit` parameter; an
  `edit self` method; `replace` and `take`; a write inside a closure the
  body calls; a write on one path and a `continue` or a `break` on
  another; nested arrays (F6's example); a `match` on an `Option[u64]`
  element that writes its binding; a loop over an arm's array binding that
  writes its variable, after which the matched place is printed; a
  `map.Map[u64, u64]` whose loop writes the value; and a generic function
  with an unconstrained type parameter beside its non-generic twin (F10).
- **A2.** `tests/run/for_binding_copy.kern` with its `.out`, for what does
  not change: a loop that reads; a range loop that assigns to its variable
  (the iteration count is unaffected); two nested loops over one map that
  only read.
- **A3.** Negative fixtures, each `expect: reject` with the code and rule
  named: an assignment to the variable of a loop over a view slice
  (`ASSIGN_IMMUTABLE`); an `edit` argument in such a loop
  (`ACC_PERMISSION_ESCALATION`); an assignment to the key variable of a map
  loop, and to the value variable when the map is a view
  (`ASSIGN_IMMUTABLE`); an assignment in a loop over a mutable value of a
  type that has no `_mut` getter (`ASSIGN_IMMUTABLE`); `move x` after, and
  `drop(x)` before, a write of `x` (`NAME_UNRESOLVED`); `x` as an `out`
  argument in a body that writes `x` (`PLACE_REQUIRED`); a map loop that
  writes its value variable and has a loop over the same map nested in it
  (`ACC_CONFLICT`).
- **A4.** `tests/run/release_accessor.kern`: its loop
  `for x in a.all() { x = x * 2u64 }` now doubles the guarded items. The
  fixture is changed to print them after the loop, and its `.out` shows
  the doubled values.
- **A5.** For `for x in xs { x = x + 1u64 }` and `for x in xs { print_u64(x) }`
  over a local `[3]u64`, the language server's hover on `x` begins
  `binding edit x: u64` in the first loop and `local x: u64` in the second.
- **A6.** Corpus invariance: `kern check` over every `.kern` file of
  `tests`, `examples`, `bench` and `std` gives the same diagnostics before
  and after the change, except for the files this spec adds or changes.
- **A7.** F12: the LLVM IR that `scripts/capture-ll.sh` emits, in both
  configurations, for the single-file programs of `bench/kern`,
  `tests/run`, `tests/trap` and `tests/positive` is byte-identical before
  and after the change for every program in which no Copy variable has an
  edit use. Among those programs the ones with an edit use are the
  fixtures this spec adds or changes; the recorded agent programs of §0
  are not among the captured programs.
- **A8.** `go test ./...` passes on macOS; the Linux gate passes for
  `./compiler/...` and `./tests`; `golangci-lint run ./...` reports nothing.

## 5. Effect on existing programs

Only a program with an edit use of a Copy variable is affected:

1. In a loop that is editable for the variable, the write now reaches the
   element, where it changed a copy that the next iteration replaced. When
   the variable is derived from an arm's Copy binding (F6), that binding
   is then an edit of the matched place, with the effects
   `specs/match-bindings` §5 lists.
2. In a loop that is not, the program no longer compiles (F3). To keep the
   old meaning, copy the variable into a local and write that.
3. A cursor loop that writes its variable and has a loop over the same
   place nested in it no longer compiles (F7).

## 6. Alternatives the owner declined

- **Copies that must not be written:** the variable is always a copy and an
  edit use is an error. `match` and `for` would then answer the same write
  differently, and a generic loop would keep writing through.
- **No change:** the reference documents the copy; the loss stays.

## 7. Non-goals

- A range loop's variable stays a writable local (F9).
- No new syntax.
- The recorded agent runs are not re-scored.
