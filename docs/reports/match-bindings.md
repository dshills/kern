# Match arm bindings of Copy payload fields

**Date:** 2026-11-11. **Spec:** `specs/match-bindings/SPEC.md` v0.4.
**Plan:** `specs/match-bindings/PLAN.md`, three phases. **Hosts:** macOS
27.2 on arm64 and Ubuntu 24.04 on arm64 in the `kern-linux-check`
container.

## The defect

Rule 5.3.1 said that a `match` arm's bindings are views of the matched
place, or edits when the subject is an edit. For a payload field whose
type is `Copy` the compiler made a copy instead, in a local the arm
owned, and the local was writable. So

```kern
match o {
    Some(n) => { n = n + 1u64 }
    None => { }
}
```

was accepted and changed nothing in `o`, in the interpreter and natively.
The same went for a field of a `Copy` struct payload, an `edit` argument,
`take` and `replace`, and a nested `match`. In a generic function the
field's type is a type parameter, which is not `Copy`, so the same text
did write to the subject. It was found while writing `examples/klisp`,
whose `set-car!` did nothing until the interpreter replaced whole cells.

## What was measured before deciding

The copy has a use. A copy is not an access, so an arm that binds only
`Copy` fields leaves its subject free, and programs depend on that: a list
is walked by reassigning the matched variable inside the arm, and a tree
in a pool is evaluated by calling back into the pool with a handle taken
from the matched node, which is the reference's own §8 example. On
2026-11-11, over the 2,113 kern files of `tests`, `examples`, `bench` and
`std`:

| Rule tried | Files whose diagnostics change |
|---|---|
| Every binding an access, as Rule 5.3.1 read | 88, all new `ACC_CONFLICT`s, `std/net.kern` among them |
| Any write through a `Copy` arm binding flagged | 1 (`tests/run/release_accessor.kern`) |

Each count is a comparison of `kern check` on every file between the
compiler as it was and one changed for the trial.

## The decision

The owner chose, on 2026-11-11, "an edit only when written" over the two
alternatives (every binding an access; copies that must not be written):

- A `Copy` binding the arm only reads is a copy made on entering the
  arm. This is what the compiler did, and the reference now says it.
- A `Copy` binding the arm uses as an edit is an edit of the payload
  field for the whole arm: it assigns to the binding or a part of it,
  passes it to an `edit` parameter or an `edit self` method, `take`s or
  `replace`s it, returns it as an edit, or does one of these through a
  binding that a nested `match` or a `with` makes from it. With a subject
  that is only a view, that use is an error.
- The subject is frozen in the arms that hold an access to it and in no
  other. Before, one access binding in any arm froze it in every arm, so
  `match o { Some(n) => { n = n + 1u64 } None => { o = Some(0u64) } }`
  would have been refused once the first arm's write became real.

## How it is built

- **Resolver.** A `Copy` binding of a place match is declared as the
  local it was and marked. Every place that requires a writable place
  (an assignment's target, an `edit` argument or receiver, `take`,
  `replace`, a place returned as an edit) first tells the binding at the
  root of that place, which becomes an access binding: an edit when the
  subject is editable, a view when it is not, so that the permission
  check that follows refuses the use with the code a view binding gets.
  A binding that a nested `match` or a `with` over a slice makes from it
  is linked to it, and its edit uses reach it through the link. A `move`,
  a `drop` or an `out` argument that named the binding while it was still
  a local is reported when it turns out to be an access binding.
- **Checker.** The freeze of the subject is added for each arm that has
  an access binding and ends with that arm. When the arm's only access
  bindings are `Copy` bindings it wrote, the freeze carries the span of
  the first such write in the source, and a conflict with it is reported
  with that span as a related location.
- **Lowering.** Unchanged: it already opened an access for an access
  binding and made a local for the rest.
- No diagnostic code was added; `spec/rules.lock` is unchanged.

## Effect on existing programs

Three kinds of program change, each with an edit use of a `Copy` arm
binding:

1. With a subject that is an edit, the write now reaches the subject.
2. With a subject that is a view, the program no longer compiles.
3. An arm that writes its binding and also reaches the matched place by
   name no longer compiles.

One existing file has such a use, the fixture `tests/run/release_accessor.kern`,
which now prints the value its arm incremented. Returning an edit of a
`Copy` binding's field, which was refused (`ACC_ESCAPE_RETURN`: the copy
was a local), is now accepted, as it was for a field that is not `Copy`.

## Verification

- **Fixtures.** `tests/run/match_binding_edit` (26 lines of output: every
  way of writing through a binding, each printing the subject after its
  match, with a generic function beside its twin), `tests/run/match_binding_free`
  (the arms that leave the subject free, and the release function of an
  accessor running once), twelve `tests/negative/match_binding_*` (one
  diagnostic each: `ASSIGN_IMMUTABLE`, `ACC_PERMISSION_ESCALATION`,
  `ACC_CONFLICT`, `NAME_UNRESOLVED`, `PLACE_REQUIRED`), and
  `tests/run/release_accessor`, which prints the incremented value. The
  expected output of the two new run fixtures was written from the spec
  before they were run, and both matched on the first run, in the
  interpreter, a native debug build and a native release build.
- **Unit tests.** The resolver's decision for each binding and the
  earliest edit use (`compiler/resolve`), the related span in five
  shapes and the freeze arm by arm (`compiler/check`), and the hover text
  (`compiler/lsp`).
- **Corpus invariance** (spec A6). `kern check` on every `.kern` file of
  `tests`, `examples`, `bench` and `std`, 2,126 files, with the compiler
  as it was and as it is: the diagnostics differ for 13 files, the 13
  fixtures this work added before the comparison (the twelfth negative
  fixture came after it, from the review). No other file changed.
- **macOS.** `go test ./... -timeout 90m`: every package passed except
  `compiler/analysis`, whose generated inventory counts the corpus
  programs and was stale by the new fixtures; regenerated
  (`-update-hosted-inventory`), after which the package passes. The
  sanitized corpus
  (`KERN_SANITIZE=1 go test ./tests -run '^(TestFixturesSanitized|TestSanitizerDetects)$'`)
  passed. `golangci-lint run ./...` reports nothing.
- **Linux.** `KERN_SANITIZE=1 scripts/linux-check.sh --run "./compiler/... -timeout 40m" --run "./tests -timeout 120m"`,
  with the new fixtures and unit tests named: 2,890 tests passed, none
  failed.
- **Review.** `prism review staged` on the change: twelve findings, none
  high. Taken: a negative fixture for `drop` (M4), two test tidyings, and
  comments stating that a caller which requires a writable place calls
  `demandEdit` first. Not taken, with the reason: a return of several
  accesses and a `with` that binds several names both take a call as
  their subject, whose `edit` arguments are already edit uses; the two
  "recorded in the error branch" findings misread the code, which records
  after the check has passed.
- `spec/rules.lock` is unchanged.

## Left as it is

- A `for` variable over `Copy` elements is a copy, as Rule 5.3.1 (3)
  says, and assigning to it changes the copy. The loss is the same as
  the one fixed here, but it is documented behaviour and was not in the
  owner's decision.
- `with` accepts a slice or a call as its subject and refuses a field or
  an element (`with x := p.x`), although Rule 5.3.3 calls those
  projections too. Found while listing the ways an arm's binding can be
  aliased; not changed.
- `examples/klisp` still replaces whole cells in `set_car` and `set_cdr`.
