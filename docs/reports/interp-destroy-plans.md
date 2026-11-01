# Interpreter: cached destroy plans

**Date:** 2026-11-01. **Spec:** `specs/interp-destroy-plans/SPEC.md`
v0.1. **Plan:** `specs/interp-destroy-plans/PLAN.md`. **Item:** the
eighth of the ten-item list ("more interpreter speed: allocation, call
setup and `copyValue` remain").

## Profile

`kern run --opt=release bench/kern/arena_ast` under `pprof` (a throwaway
test calling `runProgram`, not committed): 13.5% of the run was
`resolve.IsCopy` called from `Interp.destroy`, which re-derives a type's
markers through a visiting map keyed by the printed type name on every
drop; more went to string concatenation (`Named.String()` + `.destructor`
on every destructor call) and to `resolve.Subst` of each field's type,
which yields a fresh type value each time and so defeats any cache keyed
by identity further down.

## Change

`destroy` now looks up a per-thread plan for the type (`destroy.go`):
Copy or not, a library-opaque type or not, the array element type, the
destructor's name, and the substituted field types of a struct or of
each union variant. Recursion into fields uses the plan's field types,
which are the same values on every drop, so they hit the cache too. Only
arrays and named types get plans (for any other type destroy has nothing
to do, so it returns without asking whether the type is Copy). The cache
is capped at 65,536 plans; `child()` gives each new thread its own (the
thread copy would otherwise share the map).

## Effect

Instructions retired (`/usr/bin/time -l`, one run each, on a loaded host,
load average 12-24):

| program | 8885e74 | after | change |
|---|---|---|---|
| arena_ast | 75.22G | 61.20G | -18.6% |
| vector | 322.57G | 320.51G | -0.6% |
| hash_table | 758.95G | 756.25G | -0.4% |

The checksums are unchanged. A second profile has no `IsCopy` or
`namedMarker` in it, and string concatenation fell from 5.5% to 1.4%.
`vector` and `hash_table` drop few values with destructors, so they gain
little; their remaining costs are the ones the last report named.

## Verification

R1: `TestCompiledMatchesGeneric*`, the fixture harness, the
interpreter-versus-native differential tests and the full suite pass;
R3: `TestDestroyPlanCap` (a reused plan, the cap, a child thread's own
cache).

## Review

- **speccritic** (gpt-6.1-sol): its one critical finding was the
  preflight's missing out-of-scope section (added); the baseline revision
  pinned; mechanism marked as guidance.
- **prism** (default settings, medium effort): one low finding (no tests
  of the array, union, destructor and opaque shapes), adopted:
  `TestBuildPlanShapes`.
- **realitycheck** PARTIALLY_ALIGNED, score 100, no warning;
  **verifier** offline: 12 low findings, none on this change; clarion
  skipped. `golangci-lint` and `KERN_REQUIRE_QEMU=1 go test ./...` clean.
