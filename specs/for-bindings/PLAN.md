# Plan: loop variables of Copy elements

**Review status.** plancritic (with the spec as context) round 1:
EXECUTABLE_WITH_CLARIFICATIONS, score 79, no critical finding. Answered in
place: what stays fixed is `HEAD`, and the baseline compiler is built
before any compiler file is edited (conventions); `drop`, `out`, a type
without the `_mut` getter and a two-name cursor with neither, either and
both names written are tested (phases 1 and 2); the cursor rebuild has a
record-by-record contract (phase 2); a cursor loop's Copy variables are
derived from a Copy binding they iterate (phase 2). Round 2: score 93, one
warning, answered by the fixture case and unit-test assertion for a cursor
loop over a path of a Copy arm binding (phase 2).

Implements `specs/for-bindings/SPEC.md` v0.5 in three phases, on the
mechanism `specs/match-bindings` built: a Copy binding is declared as the
local it was and marked, and every check that requires a writable place
first tells the binding at the root of that place (`demandEdit`), which
then becomes an access binding. Phase 1 gives that to the variable of a
loop over an array or a sequence, phase 2 to the variables of a cursor
loop, phase 3 is the reference, the record and the gates. Nothing is
committed by this plan; the owner asks for commits.

Conventions that hold in every phase: a fixture's expected output is
written from the spec before the fixture is run; every fixture runs through
the harness in the interpreter, a native debug build and a native release
build (F11); `gofmt -l compiler cmd tests runtime` prints nothing and
`golangci-lint run ./...` reports nothing before a phase ends. The work is
done in the working tree of the main checkout; `HEAD` stays at `1ac1914`
until the owner asks for a commit, and the v0.2.2 tag names that commit
explicitly, so uncommitted work does not reach it. The baseline compiler
for the comparisons of phase 3 is built from `1ac1914` before any compiler
file is edited (the tree then holds only this spec and plan besides the
commit's files) and kept in the scratchpad as `kern_base`; the changed
compiler is built to `kern_for`, and the two write to separate
directories.

## Phase 1: arrays and sequences (F1 to F6, F8 to F10; A1 in part, A2 in part, A3 in part, A4, A5)

**Files.**
- `compiler/resolve/decls.go`: the fields `ArmCopy` and `ArmEdit` of
  `Symbol` now serve loop variables too, so they are renamed `CopyBound`
  and `CopyEdit`, and the method `armBound` becomes `copyBound`, with the
  comment saying both uses. `Derived` and `EditUse` keep their names. The
  rename is mechanical (`compiler/resolve`, `compiler/check`, their tests).
- `compiler/resolve/bodies.go`, `forStmt`, the array and sequence branch:
  a variable of a `Copy` element is declared as today (`SymLocal`, value
  convention) and marked `CopyBound`, with `CopyEdit` the loop's `edit`
  (already computed there: the access's permission, or `placeIsMutable` of
  the iterated place, which for a place rooted at a Copy binding answers
  by that binding's `CopyEdit`), and `Derived` the root symbol of the
  iterated expression when that root is `copyBound()` (F6, first bullet).
  The index variable of `for i, x in xs` and a range loop's variable are
  not marked (F9).
- Nothing else in the resolver changes for these loops: `demandEdit`,
  `promote`, `placeIsMutable` and the held-back `move`/`drop`/`out`
  diagnostics already act on any symbol with the mark, and a `match` or a
  `with` whose subject is a path of the variable already links its
  bindings to a `copyBound()` root (F6, second bullet).
- `compiler/lower/stmts.go` and `compiler/check/stmts.go`: no change. Both
  already bind the variable as a local or as an access by the symbol's
  kind, read after the function is resolved, and the lowering opens the
  sequence's access by the place's permission, not by the variable's kind,
  so a promoted variable's element access is an edit of an edit.
- Fixtures and tests:
  - `tests/run/for_binding_edit.kern` + `.out`: the cases of A1 except the
    map case (phase 2).
  - `tests/run/for_binding_copy.kern` + `.out`: A2's reading loop and range
    loop.
  - `tests/negative/for_binding_*.kern`: `_assign_view`, `_edit_arg_view`,
    `_move` (A3), each with `//! expect: reject CODE rule=R`.
  - `tests/run/release_accessor.kern` + `.out` (A4).
  - `compiler/lsp`: the hover test of A5.
  - `compiler/resolve`: a unit test that the variable is a local when
    read, an edit binding when written, a view binding (with the error)
    over a view, and that the inner variable of a nested array loop is
    derived from the outer one;
  - `tests/negative/for_binding_drop.kern` and `for_binding_out.kern`
    (F4: `drop(x)` and `x` as an `out` argument, each before the write
    that makes `x` an access binding; `_move` has `move x` after it).

**Acceptance.** `go test ./compiler/...` passes;
`go test ./tests -run 'TestFixtures$/run/(for_binding_edit|for_binding_copy|release_accessor)'`
and `-run 'TestFixtures$/negative/for_binding'` pass;
`go test ./tests -run 'TestFixtures$'` passes as a whole.

**Risk.** A loop over a slice that a `with` made from an arm's Copy
binding: the `with` binding has `Derived` set, the loop variable's
`Derived` is the `with` binding, and a write walks the chain to the arm
binding. The fixture's "loop over an arm's array binding" case and a
variant through a `with` slice cover the chain in both orders.

## Phase 2: cursor loops (F2, F3, F5, F7; the map cases of A1, A2, A3)

A cursor loop is desugared by the resolver before its body is resolved
(`cursorFor`): a `with` binds the getter's accesses, and a `Copy` element is
copied out of its access by a generated `x := #eN` in front of the body.
Which getter the loop uses must therefore follow from the body, which is
known only once the body has been resolved.

**Files.**
- `compiler/resolve/bodies.go`, `cursorFor`:
  - For each name whose element is `Copy`, whether the loop is editable for
    it is computed where the getter is chosen: the iterated place is
    mutable, the type has the `_mut` getter, and the access that getter
    returns for that name is an `edit` (spec §1). The generated copy
    statement is recorded in a table of the function context
    (`cursorCopies`: statement to that boolean and to the root symbol of
    the iterated expression when that root is `copyBound()`).
  - `stmt`, the short-declaration case: when the statement is in that
    table, the symbol it declares is marked `CopyBound` with `CopyEdit`
    and `Derived` from the table (F6: a cursor loop over a path of a Copy
    binding derives its Copy variables from it, as an array loop does). So
    an edit use in the body promotes it, or is refused when the loop is
    not editable for it (F3), by the code of phase 1.
  - After the desugared block is resolved, for each recorded copy whose
    symbol was promoted in an editable loop (`EditUse` non-nil and
    `CopyEdit`): the loop is rebuilt in place as the `_mut` loop it would
    have been for an element that is not `Copy` (F7):
    1. the copy statement is removed from the `with`'s body;
    2. the `with` binds the promoted symbol itself for that name:
       `Info.Decls` of the `with` (one name) or of that name's position
       (two names) is set to it, so the checker and the lowering, which
       read the `with`'s bindings from there, bind it to the getter's
       access;
    3. the getter call's method name becomes the `_mut` one, the `with`
       leaves `Info.CursorViews`, and the call expression is resolved
       again, in a scope that holds the symbols its two generated names
       (`#cur`, and `#it` when the iterated expression was wrapped) were
       resolved to the first time (`Info.Idents` of those identifier
       nodes), so that `Info.Calls` and `Info.Types` describe the `_mut`
       call.
    A name of the same loop that was not promoted keeps its copy statement,
    which copies from the `_mut` getter's access for that name.
  - What the rebuild leaves true, record by record (the contract the
    checker and the lowering rely on, each asserted by the unit test
    below):
    - `Info.Decls`: no entry for a removed copy statement; the `with`'s
      entry for a promoted name is the promoted symbol (`SymAccess`,
      `edit`); the entry for an unpromoted name is its `#eN` symbol, whose
      permission is set to that of the `_mut` getter's access for that
      name (view for a `map.Map` key; edit where a type's `_mut` getter
      returns an edit for it), so every `with` symbol agrees with the
      access it is bound to.
    - `Info.Idents`: untouched. Every identifier of the body that named
      the variable already maps to the promoted symbol, and the generated
      identifiers of the getter call map to the same `#cur` and `#it`
      symbols after it is resolved again.
    - `Info.Calls` and `Info.Types` of the getter call: those of the `_mut`
      method (an `edit self` receiver; a tuple or a single access with the
      `_mut` permissions), written by resolving the call again.
    - `Info.CursorViews`: no entry for the `with`. `Info.CursorCalls`:
      the entry for the call stays, as for any `_mut` cursor loop.
    - The desugared block kept by `SetCursor` is the same block, changed
      in place, so the checker and the lowering see the rebuilt loop.
  - The step is taken once per loop, after its body, so a nested loop is
    already in its final form when an outer one is rebuilt.
- Fixtures and tests:
  - the map case of `tests/run/for_binding_edit.kern`; the nested reading
    loops of `tests/run/for_binding_copy.kern`;
  - `tests/negative/for_binding_map_key.kern`,
    `for_binding_map_view.kern` (`ASSIGN_IMMUTABLE`) and
    `for_binding_map_nested.kern` (`ACC_CONFLICT`);
  - a case in `tests/run/for_binding_edit.kern` over a type of the
    fixture's own with `cursor_get` and `cursor_get_mut`, one name, written;
    and one over a type with only `cursor_get`, read;
  - `compiler/resolve`: a table-driven unit test over a type of the test's
    own whose two-name `_mut` getter returns an edit for both names, with
    neither, the first, the second and both names written, and over
    `map.Map`: it asserts each record of the contract above (which getter
    is called, what each `with` name is bound to and with which
    permission, that the copy statement is gone or kept), and that a loop
    whose variables are only read is unchanged;
  - `tests/run/for_binding_edit.kern` runs that two-name type with each of
    the four combinations, so the rebuilt loops go through the checker and
    the lowering in all three configurations;
  - a case in `tests/run/for_binding_edit.kern` for a cursor loop over a
    path of a Copy arm binding (a `Copy` struct with `cursor_get` and
    `cursor_get_mut` as an arm's payload): the body writes the loop
    variable and the matched place is printed after the match; the
    resolver unit test asserts that the variable's `Derived` is the arm's
    binding and that both were promoted;
  - `tests/negative/for_binding_no_mut.kern`: a write to the variable of a
    loop over a mutable value of a type that has only the view getter
    (`ASSIGN_IMMUTABLE`).

**Acceptance.** `go test ./compiler/...` passes; the fixtures above pass;
`go test ./tests -run 'TestFixtures$'` passes as a whole;
`tests/run/for_cursor*` are unchanged and pass.

**Risks.**
- The rebuilt `with` holds a symbol declared by a statement that is no
  longer in the tree. Nothing reads a symbol's declaring statement after
  resolution except through `Info.Decls`, whose entry for the removed
  statement is deleted in step 1.
- The key's `with` symbol was declared with the view getter's permission.
  After the rebuild the `_mut` getter's access for the key is still a view
  for `map.Map`; for a type whose `_mut` getter returns an edit for both
  names, an unpromoted name's `with` symbol is updated to that permission
  when the call is resolved again, so symbol and access agree.
- Should the rebuild prove unsound in review or in the sanitized corpus,
  the fallback is to refuse an edit use of a cursor loop's Copy variable
  (`ASSIGN_IMMUTABLE` and the other codes of F3) and to tell the owner
  that F2 holds for arrays and sequences only; that would be a change to
  the spec, made with the owner, not silently.

## Phase 3: the reference, the record and the gates (D1 to D6; A6 to A8)

**Steps.**
1. `docs/ownership.md` (D1, D2), `spec/types.md` §7 (D3),
   `specs/match-bindings/SPEC.md` v0.5 (D4), `CHANGELOG.md` (D5).
2. A6: `kern check` over every `.kern` file of `tests`, `examples`, `bench`
   and `std`, with the compiler of commit `1ac1914` and the new one;
   diagnostics compared file by file. Any difference outside the fixtures
   this plan adds or changes is a defect to fix before going on.
3. A7: `scripts/capture-ll.sh` with both compilers; the two directories
   are compared. Any difference outside the fixtures this plan adds or
   changes is a defect to fix before going on.
4. `gofmt`, `golangci-lint run ./...`.
5. A8: `go test ./... -timeout 90m` on macOS, with the generated inventory
   regenerated first (`-update-hosted-inventory`: the new fixtures change
   its count); the sanitized corpus; the Linux gate,
   `KERN_SANITIZE=1 scripts/linux-check.sh --run "./compiler/... -timeout 40m" --run "./tests -timeout 120m"`.
   These wait for the benchmark job that holds the host's timing slot to
   finish or give up, so that they do not load the host under it.
6. `docs/reports/for-bindings.md` (D6), written when steps 2, 3 and 5 have
   their results; then `go test ./tests -run TestCitations`.
7. Project practice, not a requirement of the spec: `prism review staged`;
   its findings are read and answered, and steps 4 and 5 are run again for
   whatever a finding changes.

**Acceptance.** Steps 2 and 3 report no unexpected difference; steps 4 and
5 pass; the report exists and `TestCitations` passes; `spec/rules.lock` is
unchanged.
