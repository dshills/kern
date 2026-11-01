# Plan: interpreter destroy plans

**Spec:** `specs/interp-destroy-plans/SPEC.md` v0.1. **Profile:** davin-go.
One phase, one commit on `main`, not pushed. No code generation change,
so no IR baseline; R2's "before" counts were taken at 8885e74 (arena_ast
75.22G, vector 322.57G, hash_table 758.95G instructions) on this host.

1. `compiler/interp/destroy.go`: `type destroyPlan` and
   `(*Interp).planOf(t resolve.Type) *destroyPlan` with the bounded
   per-thread map; `destroy` rewritten over plans with the same order of
   destructors and fields; the map field on `Interp`, created lazily.
2. Unit tests: the cap (R3), and that a struct with a destructor-bearing
   field and a union variant destroy in the old order (a destructor that
   records into a list).
3. Measure R2; profile again to confirm `IsCopy` is gone from `destroy`.
4. Report; review: `golangci-lint run ./...`, `KERN_REQUIRE_QEMU=1 go
   test ./...`, `prism review staged --paths 'compiler/**'
   --reasoning-effort medium`, `realitycheck check --max-tokens 65536`,
   `verifier analyze -mode offline`; clarion skipped. If a tool cannot
   run, the commit waits and the owner is told.
5. Commit `perf(interp): cache destroy plans per type`.
