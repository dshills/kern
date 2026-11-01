# Plan: nested cursor loops

**Spec:** `specs/nested-cursor-loops/SPEC.md` v0.1. **Profile:** davin-go.
One phase, one commit on `main`, not pushed.

1. Baseline: R3's capture with a compiler built from cf9c50a (the
   commit this work starts from; the checker change is already in the
   working tree, so the baseline compiler is built from a worktree at
   cf9c50a), into `~/.cache/kern-nested-cursor/ll-base`.
2. Resolver: `Info.CursorCalls` and `Info.CursorViews`, filled by
   `cursorFor`. Checker: `access.cursor` (set on a cursor call's argument
   accesses and its result access) and `access.cursorView` (set on a
   view-getter binding's freeze); `finish` skips a cursor view access
   against a view-getter freeze.
3. Fixtures of R1 and R2 (the old `for_cursor_nested` pair rewritten to
   the edit case).
4. R3 comparison; documents.
5. Review: `golangci-lint run ./...`, `KERN_REQUIRE_QEMU=1 go test ./...`,
   `prism review staged --paths 'compiler/**,tests/**' --reasoning-effort
   medium`, `realitycheck check --max-tokens 65536`, `verifier analyze
   -mode offline`; clarion skipped. If a tool cannot run, the commit
   waits and the owner is told.
6. Commit `feat(check): nested cursor loops over one place`.
