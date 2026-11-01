# Plan: completion through constraints and loop scopes

**Spec:** `specs/editor-complete-gaps/SPEC.md` v0.1. **Profile:** davin-go.
**Review status.** plancritic (davin-go): EXECUTABLE_WITH_CLARIFICATIONS,
93, no critical finding; its one warning (collision detail, unresolved
receiver, brace probes) folded into step 4.
**Order:** Phase 1 then Phase 2; each ends with `golangci-lint run
./...` and `go test ./compiler/lsp/...` green. One commit, on `main`, not
pushed. No IR baseline: only `compiler/lsp` and documents change (spec §4).

## Phase 1: completion (R1–R5)

**Files.** `compiler/lsp/complete.go`, `compiler/lsp/complete_test.go`.

1. R1, R2: in `memberItems`, after the named-type case returns nothing,
   strip accesses from `info.Types[fe.X]` (the same loop as `ownerOf`)
   and switch on the result:
   - `*resolve.TypeParamType`: for each constraint in order with a
     non-nil `Iface`, add each method with a `self` parameter whose name
     is not yet added (a local `seen` set, so the first constraint's
     signature wins); markers are skipped;
   - `*resolve.Prim`: `prog.Core.Types["prim_"+name]`, when present,
     contributes its `self` methods as for a named type (no fields);
   - anything else, or no recorded type: nothing (as before).
   Methods are added in `Order` for interfaces; the list is sorted by
   label by the caller in any case (spec editor-complete R1).
2. Where-clause bounds: the R5 case is written first; if the recorded
   type of `self.a` lacks the `Hashable` bound, the server adds the
   enclosing `FuncDecl`'s `Where` bounds for a parameter with the same
   name and owner (spec §5 guidance); the report records which held.
3. R3: in `localItems`, collect the header bindings of the function's
   body with `ast.Walk`: for an `*ast.For`, `VarPos` and `Var2Pos` (when
   `Var2 != ""`) map to its `Body`; for an `*ast.With`, each `NamePos`
   to its `Body`; for an `*ast.Arm`, each `BindPos` to its `Body`. A
   declaration symbol whose `Pos.Start` equals a collected position is
   kept only when `Body.Start <= at && at <= Body.End`; other locals
   keep the innermost-block rule.
4. R5 tests in `complete_test.go`: a new `TestCompletionConstraints`
   (one generic function with `T: Hashable`, one with two interface
   constraints, one with `T: Copy`, the `Pair[T]` where-clause method,
   and a `u64` local) and `TestCompletionHeaderScope` (a `for` variable,
   a `with` name, a `match` arm binding, each probed inside and after
   its body, and a local declared before the loop probed in both). The
   two-constraint case uses `Hashable` and a test interface declared in
   the test source that also declares `hash` with a different signature,
   and asserts the item's detail is `Hashable`'s; an undeclared receiver
   (`zz.`) offers no member, its resolution error expected. The header
   cases probe also just after the body's `{` and just before its `}`. Each test's source is checked to resolve without
   error diagnostics before probing, so a wrong probe source cannot pass
   by offering nothing.
5. R4: `TestCompletion` and the other `compiler/lsp` tests pass
   unchanged.

**Acceptance.** The new tests pass; the existing ones pass unchanged.

## Phase 2: documents, review, commit (R6)

1. `docs/reports/editor-complete.md` "Not done": drop the two closed
   entries, add a pointer to the new report. `docs/editors.md`'s
   completion paragraph: type-parameter and primitive members.
   `docs/reports/editor-complete-gaps.md`: design, tests, the
   where-clause finding, review.
2. Review: stage; `golangci-lint run ./...`, `KERN_REQUIRE_QEMU=1 go
   test ./...`, `prism review staged --paths 'compiler/**'
   --reasoning-effort medium`, `realitycheck check --max-tokens 65536`,
   `verifier analyze -mode offline`; clarion skipped (no
   `docs/clarion-meta.json`). Findings are fixed or justified in the
   report; if a tool cannot run, the commit waits and the owner is told.
3. Commit `feat(lsp): complete through constraints, scope loop bindings`.

**Acceptance.** Documents updated; reviews clean or justified; committed.
