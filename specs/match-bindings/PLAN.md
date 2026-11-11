# Plan: match arm bindings of Copy payload fields

**Review status.** plancritic (with the spec as context) round 1:
EXECUTABLE_WITH_CLARIFICATIONS, score 91, no critical finding. Its warning
(a closure resolved before a later write promotes the binding) is answered
under "Whole-arm finality" in phase 1 with a regression case; its note on
the order of the report and the gates is answered in phase 3. Round 2
(score 93): the first edit use is now the minimum source offset over every
edit demand, not the first the resolver meets. Round 3:
EXECUTABLE_AS_IS, score 100; its question (a binding derived from one that
was already promoted) is answered where `Derived` is set.

Implements `specs/match-bindings/SPEC.md` v0.4 in three phases. The
resolver decides what each Copy binding is (phase 1), the checker freezes
the subject arm by arm (phase 2), and the reference, the changelog, the
report and the gates follow (phase 3). Nothing is committed by this plan;
the owner asks for commits.

Conventions that hold in every phase: a fixture's expected output is
written from the spec before the fixture is run; every fixture runs through
the harness, which compares the interpreter, a native debug build and a
native release build (M10); `gofmt -l compiler cmd tests runtime` prints
nothing and `golangci-lint run ./...` reports nothing before a phase ends.

## Phase 1: the resolver decides (M1 to M5, M8, M9; A1, A3 in part, A4, A5)

**Files.**
- `compiler/resolve/decls.go`, `Symbol`: four fields.
  - `ArmCopy bool`: the symbol is a Copy binding of a place match with no
    edit use so far (spec §1). It is a `SymLocal` while this is set.
  - `ArmEdit bool`: that match has an editable subject.
  - `Derived *Symbol`: the binding this one is derived from (spec §1,
    item 6), or nil.
  - `EditUse *ast.Pos`: for a Copy binding that became an access binding,
    the span of its edit use with the smallest start offset, which is the
    first in source order; nil otherwise. Phase 2 reads it.
  The comment on `SymLocal` and `SymAccess` says which arm bindings are
  which.
- `compiler/resolve/bodies.go`, `match`:
  - The root symbol of the subject is looked up once. When it has
    `ArmCopy` set, the nested match's subject is editable exactly when the
    root's `ArmEdit` is set; this case is tested before the existing
    `SymLocal` case, which would call any local editable.
  - A binding of a `Copy` field in a match that is not owned is declared
    as today (`SymLocal`, value convention) and then marked: `ArmCopy`
    true, `ArmEdit` the match's editability, and `Derived` the subject's
    root when that root is or was a Copy binding or derives from one: it
    has `ArmCopy` set, a non-nil `EditUse` (it was promoted earlier in the
    arm) or a non-nil `Derived`. An owned match's bindings are not marked
    (M9).
- `compiler/resolve/bodies.go`, `with`: in the access case, when the
  subject has a root symbol (`placeRoot`, so a slice of a path, not a
  call) with `ArmCopy` set, a non-nil `EditUse` or a non-nil `Derived`,
  the declared binding's `Derived` is that root. The link is made whether
  or not the root has already been promoted, so that an edit use met later
  through the derived binding still takes part in the root's `EditUse`
  minimum.
- `compiler/resolve/bodies.go`, two new functions and one changed:
  - `promote(sym, at)`: when `sym.ArmCopy` is set, clears it, sets the kind
    to `SymAccess`, the convention to `edit` or `view` by `ArmEdit`, and
    `EditUse` to `at`, and reports the uses recorded by `ownedUse` below.
    Otherwise it does nothing.
  - `demandEdit(sc, e)`: calls `promote` on the root symbol of the place
    `e` and then on each symbol of its `Derived` chain, in that order. For
    each of these symbols that has a non-nil `EditUse` after the call
    (promoted now or earlier), `EditUse` is replaced by `e`'s span when
    that span starts earlier. So the recorded span is the minimum over
    every edit use, whatever order the resolver meets them in (a closure
    body, an argument resolved before its call, a derived binding's use)
    and does not rely on the resolver's traversal order.
  - `placeIsMutable`: a `SymLocal` with `ArmCopy` set is mutable exactly
    when `ArmEdit` is set. With a view subject every existing caller then
    reports the code M3 names (`ASSIGN_IMMUTABLE`, "assignment through a
    read-only place"; `ACC_PERMISSION_ESCALATION` for an `edit` parameter,
    an `edit self` receiver and a returned edit), and a slice of the
    binding is a view, so a `with` over it binds a view and a write through
    that binding is refused as a write through a view is today.
- The edit uses of spec §1 call `demandEdit` before the permission check
  they already make, so that the check sees the symbol's final kind:
  - item 1: the `*ast.Assign` case of `stmt` (`bodies.go`), on the target;
  - item 2: the `edit` parameter case of `checkArgs` (`calls.go`), on the
    argument, which also serves a scoped spawn's `edit` arguments;
  - item 3: the `edit self` receiver case (`calls.go`), on the receiver;
  - item 4: the `*ast.Take` and `*ast.Replace` cases of `expr`
    (`exprs.go`), on the place. The resolver makes no permission check
    here; the checker's existing "take through a view" and "replace
    through a view" (`ACC_PERMISSION_ESCALATION`) then apply, because the
    promoted binding is a view binding when the subject is a view;
  - item 5: `ret` (`bodies.go`), on the returned place, in the branch
    that returns a place as an `edit` access;
  - item 6 needs no site of its own: a derived binding's edit use reaches
    the arm binding through `demandEdit`'s walk of the `Derived` chain.
  A closure's body is resolved in place with the arm's scope visible, so an
  edit use inside it is seen by the same sites.
- Whole-arm finality (M2: an edit "from the opening brace"). Nothing that
  the resolver records about a Copy binding while it is still a local may
  fix its kind: every record holds the `*Symbol` itself, and its kind is
  read when the function has been resolved to its end.
  - A closure's capture list (`Info.Closures`) holds `Capture{Sym, Ident,
    Edit}`; `Edit` says what the closure body does with the name and does
    not depend on the symbol's kind. The lowering opens each capture from
    `symPlace(cp.Sym)`, which it computes when it lowers the closure, after
    resolution: for a binding promoted by a later statement of the arm that
    place is the payload field, not a local.
  - A closure cannot outlive the statement it is written in (closure types
    are second-class, `spec/types.md`), so no closure made before a write
    runs after it; the regression below still fixes the order of reading,
    writing and reading again through closures.
  - `Info.Idents` and `Info.Decls` map to the same `*Symbol`; no copy of a
    symbol is made anywhere in the resolver (checked by reading every
    `Symbol{` literal: `declare`, `declareParams`, `globalSym` and the
    match's own are the only ones).
- M4, `ownedUse`: the `*ast.Move` and `*ast.Drop` cases (`exprs.go`) and
  the `out` argument case (`calls.go`) accept a `SymLocal`. When the symbol
  they accept has `ArmCopy` set they record on it the diagnostic an access
  binding would have got there (code, span, message: `NAME_UNRESOLVED`
  "move requires an owned local, found n", the same for `drop`,
  `PLACE_REQUIRED` "out argument must name an uninitialized local or out
  parameter"). `promote` reports the recorded diagnostics. A use after the
  promotion finds a `SymAccess` and is refused by the existing check.
- Fixtures and tests:
  - `tests/run/match_binding_edit.kern` + `.out`: every case of A1, and
    one for whole-arm finality: an arm in which a closure reads the
    binding, the next statement writes it, and a second closure reads it
    again prints the old value, then the new one, and the subject after
    the match holds the new one.
  - `tests/negative/match_binding_*.kern`, one file per case of A3 except
    the `ACC_CONFLICT` case (phase 2): `_assign_view`, `_edit_arg_view`,
    `_edit_self_view`, `_take_view`, `_replace_view`, `_move_before`,
    `_move_after`, `_drop`, `_out`, `_nested_view`, `_with_slice_view`. Each has
    `//! expect: reject CODE rule=R` with the rule `spec/rules.md` gives
    the code.
  - `tests/run/release_accessor.kern` + `.out` (A4): after the match the
    fixture prints the guarded value, which the arm incremented.
  - `compiler/lsp`: a test that hovers `n` in both matches of the spec's §0
    program and checks the two first lines of A5.
  - `compiler/resolve/resolve_test.go`: one test that a binding stays a
    `SymLocal` when only read and that two bindings of one pattern are
    decided independently (M5); it supports the fixtures and is not an
    acceptance criterion.

**Acceptance.** `go test ./compiler/resolve ./compiler/lsp` passes;
`go test ./tests -run 'TestFixtures$/run/(match_binding_edit|release_accessor)'`
and `go test ./tests -run 'TestFixtures$/negative/match_binding'` pass;
`go test ./tests -run 'TestFixtures$'` passes as a whole, which shows that
no existing fixture changed.

**Risks.**
- A resolver decision made while the binding was still a local and not
  revisited after promotion. The decisions that read a symbol's kind are
  `move`, `drop`, `out` (covered by `ownedUse`), `placeIsMutable` (changed),
  a nested match's editability (changed), `placeRoot` and the closure's
  outer set (both treat the two kinds alike). The fixture's "write after a
  read", "write inside a closure" and "nested match before the outer write"
  cases exercise the order.
- The lowering and the checker run after the whole function is resolved
  and read the symbol through `Info.Decls` and `Info.Idents`, which hold
  the same `*Symbol`; if either had copied a kind earlier, the nested and
  closure cases of A1 would fail in all three configurations.

## Phase 2: the checker freezes arm by arm (M6, M7; A2, the rest of A3)

**Files.**
- `compiler/check/stmts.go`, `match`: the freeze of the subject is no
  longer added once for the match. For each arm, after the arm's scope is
  pushed, the freeze is added when the arm has at least one binding whose
  symbol is a `SymAccess`, under the conditions that hold today (the
  subject has a root place and the match is not `match move`). The arm's
  scope already records the length of the enclosing list on entry and
  cuts it back on exit, so the freeze ends with the arm. Nothing else of
  the subject's access changes: it is closed where it is closed today, and
  a release function runs where it runs today (M6's last sentences).
- `compiler/check/state.go`, `access`: a `note` (label and span). The arm's
  freeze gets one when every `SymAccess` binding of the arm has a non-nil
  `EditUse`: the span is the `EditUse` with the smallest start offset, the
  label M7's text with that binding's name.
- `compiler/check/check.go`: `errorfRelated`, which is `errorf` with
  `diag.Related` entries; `compiler/check/stmts.go`, the conflict report:
  when either access of the reported pair carries a note, the
  `ACC_CONFLICT` diagnostic gets it as its one related span.
- Fixtures and tests:
  - `tests/run/match_binding_free.kern` + `.out`: the cases of A2,
    including the two release counts.
  - `tests/negative/match_binding_conflict.kern`: an arm that writes its
    Copy binding and reassigns the subject, `expect: reject ACC_CONFLICT
    rule=5.4.4`.
  - `compiler/check`: a unit test that the diagnostic of that program has
    exactly one related span, at the first edit use, with M7's label; that
    with several edit uses of one binding (an assignment after an `edit`
    argument nested in a call, one inside a closure, one through a nested
    match) and with two written bindings the span is the one that starts
    first in the source; and that an arm with a non-`Copy` binding gets
    none.

**Acceptance.** `go test ./compiler/check` passes;
`go test ./tests -run 'TestFixtures$/run/match_binding_free'` and
`-run 'TestFixtures$/negative/match_binding_conflict'` pass;
`go test ./tests -run 'TestFixtures$'` passes as a whole.

**Risk.** An arm that is no longer frozen writes the subject while the
subject's own access is still to be ended at the end of the match. That is
the situation of a match with no access binding today (the list walk, and
the arena example), so no new case reaches the lowering; the fixture's
`Pool.at` and release-accessor cases run it under the sanitizers.

## Phase 3: the reference, the record and the gates (D1 to D4; A6, A7)

**Steps.**
1. `docs/ownership.md`: D1's replacement text in Rule 5.3.1 and D2's
   comment in the §8 example.
2. `CHANGELOG.md`, `[Unreleased]`, "Language": D3's two entries.
3. A6: `kern check` over every `.kern` file of `tests`, `examples`, `bench`
   and `std` with a compiler built from the commit before this work and
   with the new one; the two sets of diagnostics are compared file by
   file. Any file that differs, other than the fixtures this plan adds or
   changes, is a defect to fix before going on.
4. `gofmt`, `golangci-lint run ./...`.
5. A7: `go test ./... -timeout 90m` on macOS; the sanitized corpus
   (`KERN_SANITIZE=1 go test ./tests -run '^(TestFixturesSanitized|TestSanitizerDetects)$'`);
   the Linux gate, `KERN_SANITIZE=1 scripts/linux-check.sh --run "./compiler/... -timeout 40m" --run "./tests -timeout 120m"`.
6. `docs/reports/match-bindings.md` (D4), written when steps 3 and 5 have
   their results: the measurement of spec §0, the decision, and the
   outcome of steps 3 and 5 with the commands used. Then
   `go test ./tests -run TestCitations`, because documents changed.
7. Project practice, not a requirement of the spec: `prism review staged`;
   its findings are read and answered before the work is reported, and
   steps 4 and 5 are run again for whatever a finding changes.

**Acceptance.** Step 3 reports no unexpected difference; steps 4 and 5
pass; the report of step 6 exists and `TestCitations` passes;
`spec/rules.lock` is unchanged (`git diff --stat spec/` is empty).

**Risk.** The Linux gate needs the container runtime, which the owner
sometimes has stopped; it is started for the run and stopped afterwards,
as on 2026-11-11.
