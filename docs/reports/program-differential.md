# Whole-program differential test of the interpreter and the backend

**Date:** 2026-10-28. **Follows:** `docs/reports/proof-differential.md`
(random indexing programs for the proof pass and the inliner) and the
integer-semantics differential test; the thesis that the interpreter is the
native backend's behavioural oracle had no test that drew from the rest of
the language.

## What was added

`tests/program_diff_test.go`, `TestProgramDifferential`: a generator of
random programs from fourteen block templates, each printing what it
computed, over a fixed set of helpers and types: a struct and a struct
holding a `Vec`, a union matched through a view, an owned `Option[Box]`
consumed by a `with` binding, `Vec` pushes, pops, edits through `at`,
clones and sums, `Box` edited in place, closures with view and edit
captures, a fold over an array view, a nested closure, byte-order writes
and reads, a generic identity, divisions and conversions that trap on
about one block in ten, checked and saturating arithmetic, and the
`std/str` and `std/map` library code. Each program runs in the
interpreter and then natively in both optimisation levels; output, exit
status and the trap record must agree (`nativeRunOpt`). `KERN_DIFF_PROGRAMS`
(default 24), `KERN_DIFF_SEED` and `KERN_DIFF_DUMP` apply as for the other
two differential tests.

The generator stays inside what the language accepts (a view parameter
needs a place, an owned value with a destructor is consumed through a
`with` binding, not destructured by `match move`), so a program that
fails to resolve or check is a generator bug, reported with its text.

## Result

The default 24 programs and a sweep of 60 further seeds (100 to 159) agree
in all three builds. The two engine bugs found while writing the standard
library (`docs/reports/std-library.md`: a `match move` that never consumed
its subject, and interface-constrained calls without a lowering) predate
this test and are the kind of disagreement it now catches.
