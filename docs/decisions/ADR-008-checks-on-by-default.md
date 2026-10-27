# ADR-008: Bounds and generation checks on by default, with provenance of eliminated checks

**Status:** Decided (Phase 0, per PLAN.md 0.2 and Phase 0 key decisions)
**Date:** 2026-10-26
**Spec:** §4.1 (Bounds, Arithmetic), §5.6 (dereference check order), §7 (checked lowering,
IR invariant 3, labels), §16 (`show-bounds-checks`); PLAN.md §0 (`--opt debug|release`),
2.2 (debug configuration), 2.5. Spec §10 lists this record as open; PLAN.md closes it in
Phase 0 because the checker prototype and the IR provenance tags depend on it.
**Depends on:** ADR-002, ADR-006.

## Problem

Spec §5 permits runtime checks where stated, and §5.6 makes handle validity a runtime
property. The question is whether those checks may be disabled by a build configuration,
and how an agent learns which checks remain. Disabling checks in release builds (the Zig
`ReleaseFast` and C model) makes program behavior configuration-dependent, which breaks
differential testing (§7) and makes "the program traps here" an unstable fact. Leaving every
check in place costs a compare and branch per index, which the 10% target must absorb.

## Alternatives

- **No checks (C, C++)** [iso-cpp20]. Rejected: undefined behavior reachable from safe code
  violates §4.1.
- **Checks disabled by build mode (Zig `ReleaseFast`)** [zig-ref]. Rejected: a program's
  trap set would differ between `--opt debug` and `--opt release`, so expected-output files
  and the differential harness would need a configuration axis for semantics, and an agent
  could not be told whether a check exists without knowing the build.
- **Rust: checks always on, `get_unchecked` in unsafe** [rust-reference]. Adopted in
  substance: checked by default, an unsafe unchecked form, and elimination only by the
  optimizer's proof.
- **Ada run-time checks with `pragma Suppress`; SPARK proof of absence** [ada-rm]
  [spark-rm]. The proof route is the long-term direction for `proven` facts; the
  suppression pragma is the configuration switch rejected above.
- **Cyclone fat pointers** [cyclone-2002] and **Vale generational references**
  [vale-genrefs]: both check on every access; kern checks only at projections and handle
  dereferences, which is the same set of sites once accesses are second-class.
- **Identity tags always on.** Rejected: the identity check is a logic-error aid, not a
  memory-safety check (§5.6), so it is the one check `--opt release` disables.

## Invariants

1. Every index and slice operation on arrays, slices and views is checked and traps
   (`bounds`) unless the compiler records a proof fact that eliminates the check or the
   operation is the unsafe unchecked form (§4.1).
2. Every handle dereference and `Pool.free` performs the §5.6 checks in order: `bounds`,
   `identity` (debug builds, both tags nonzero), `vacant`, `generation`. Multi-access
   accessors add `disjoint` (ADR-004). Integer `+ - *`, negation, division, shifts and
   conversions trap in every build configuration (§4.1).
3. `--opt release` disables only container identity tags; every other check is on in both
   configurations (PLAN.md §0). The trap set of a program is otherwise configuration-independent.
4. IR invariant 3: every `check_bounds`, `check_generation`, `check_disjoint`,
   `check_overflow` either remains or is replaced by a recorded proof fact
   `proven_by: loop_bound | dominated_by_check | constant`, asserted after every pass.
5. `show-bounds-checks` reports every check with kind, label `runtime-checked` or `proven`,
   and `proven_by` non-null iff `proven`. Labels derive only from IR provenance tags, never
   from heuristics (§7).

## Hard counterexample

A loop whose first index is provable and whose second is not. Under a check-disabling build
mode the second access would be undefined behavior for some inputs; under kern it traps in
every configuration, and the tooling says which is which:

```text
func strided_sum(data: view[]u8, stride: usize) -> u64 {
    total := 0u64
    i := 0usize
    while i < len(data) {
        total = total + u64(data[i])              // check_bounds: proven_by dominated_by_check
        total = total + u64(data[i + stride])     // check_bounds: runtime-checked; traps `bounds` when i + stride >= len(data)
        i = i + stride                            // check_overflow: runtime-checked
    }
    return total
}

func stale(p: edit[Pool[Node]], id: PoolId[Node]) {
    p.free(id)
    with n := p.at(id) {                          // traps `vacant`; `generation` once the slot is reused
        n.leaf = true
    }
}
```

`show-bounds-checks` on `strided_sum` returns three entries: one `proven`
(`dominated_by_check`), two `runtime-checked`. A program whose correctness depends on the
second index being in range is still accepted; it is the trap, not the compiler, that
reports the violation.

## Expressiveness cost

None in safe code. Code that has proven an index out of band and cannot afford the check
uses the unsafe unchecked form inside `unsafe` under a contract block, which is also where
the audit will look for it.

## Performance cost

One compare-and-branch per unproven index or slice, one generation compare per handle
dereference, one identity compare per dereference in `--opt debug` only. The acceptance
target (spec §2.3, within 10% of C/Rust) must be met with checks on; the benchmark matrix
measures `--opt release` as the primary row and `--opt debug` as a second row so the
identity-tag cost is visible separately (PLAN.md 2.6). The three proof facts cover the
common loop shapes (`for i in 0..len(v)`, a dominating `if i < len(v)`, constant indices).

## Complexity

A provenance tag on every `check_*` instruction and a validator rule that forbids a pass
from deleting a check without recording a fact. The optimizer is permitted only the three
named proof facts, which keeps `proven_by` explainable. No separate "safe build" and
"fast build" semantics exist to document or test.

## Experiments

- PLAN.md 1.4: trap fixtures `bounds`, `overflow`, `div_zero`, `shift`, `conversion`,
  `generation`, `vacant`, `identity` (restricted to `opt=debug`, with an `opt=release` accept
  twin whose output shows the foreign-handle access completing memory-safely), `disjoint`,
  each with a completing twin, run in both hosted configurations.
- PLAN.md 1.1 and 1.4: the IR validator's invariant 3 with a deliberately corrupted IR file
  under `tests/ir/` that deletes a check without a proof fact.
- PLAN.md 2.3: generation-wrap retirement via `kern_test_pool_seed_generation`.
- PLAN.md 2.5: `show-bounds-checks` conformance to §16, with `proven_by` from IR provenance.
- PLAN.md 2.6: every benchmark row in `--opt release` and `--opt debug`, with the §2.3
  10% throughput and RSS targets reported per workload.

## Conclusion

All memory- and arithmetic-safety checks are on in every build configuration; only the
identity tag is a debug-only aid. Checks are eliminated only by recorded proof facts, which
the validator enforces and `show-bounds-checks` reports with labels derived from provenance.

## Implementation note (2026-10-27)

The three proof facts exist: `loop_bound` from the lowering of `for` loops (Phase 1), and
`dominated_by_check` and `constant` from the proof pass `compiler/opt/prove.go`, which runs on
every lowered function after the release inliner (`compiler/opt/inline.go`, PLAN.md Phase 6)
in the pipeline `lower → validate → inline → validate → prove → validate` (`opt.Run`). The
backend leaves a proven check out; the interpreter executes it, so a wrong proof is a
disagreement in the differential harness rather than an unchecked access. The reports
`docs/reports/bounds-check-elimination.md` and `docs/reports/inliner.md` record what each
pass changed in the measurements.
