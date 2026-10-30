# Cross-file inlining: a file on every copied block

**Version:** 0.1 (2026-10-30). **Scope:** the inliner (`compiler/opt/inline.go`),
the IR's block type (`compiler/ir/ir.go`, `print.go`, `validate.go`), the
two consumers of an instruction's file (the LLVM backend's trap-record and
debug-location emission in `compiler/backend/llvm/`, the interpreter's
trap in `compiler/interp/interp.go`), the machine-API reports that locate
instructions (`compiler/analysis/analysis.go`), the fixture suite under
`tests/` and the documents that describe the inliner. **Language impact:**
none: no change to the language spec, the checker, the lowering or the
shape of a trap record. **Related:** `specs/initial/PLAN.md` 6.2 (the
eligibility rules) and 6.6 (the deferral this spec lifts: "cross-file
inlining needs a file on every position or on every instruction");
`docs/reports/trap-file.md` (trap records name the trapping function's
file, the work that put a file on every function); `docs/reports/inliner.md`,
`inliner-loops.md`, `inliner-sizing.md` (the rules that stay);
`docs/reports/module-discovery.md` (imported user modules).

**Review status.** speccritic (OpenAI gpt-6.1-sol, profile general,
with `docs/reports/trap-file.md` and `compiler/opt/run.go` as context):
VALID_WITH_GAPS, no critical finding. The warnings are the preflight's
flags on the R*n* labels and on code identifiers read as acronyms, one
scope note that §2 prescribes a representation (intended, see
"Normative mechanisms"), one on a measurable requirement (R7's single
`llvm-dwarfdump` check, which is a recorded observation, not a
threshold), and one contradiction in R3's statement of which block a
copied instruction lands in when the files agree, folded in.

**Normative mechanisms.** The mechanism §2 prescribes (the file on the
block, not on the instruction or the position) is normative: R3, R5 and
R7 are stated in its terms, and the acceptance criterion R11 (single-file
programs emit byte-identical LLVM IR) depends on the block carrying the
file only when it differs from the function's.

**Glossary.** R*n*: requirement *n* of §4. File: the path of a kern
source file as the compiler names it (a user file by the path it was
loaded from, relative to the discovery root, such as `lib/lib.kern`; a
library file as `std/<module>.kern`). Declaring file: `ir.Func.File`, the
file the function's body was lowered against. Effective file of a block:
its `File` when non-empty, else its function's declaring file (§2).
Position: `ast.Pos`, a byte-offset span within one file; which file is
not part of the position. Trap record: the JSON line
`{"trap": {"kind", "file", "span", "thread"}}` a trap writes, compared
between the interpreter and the native build by the fixture harness.
User function: a function with a body that is not a library function
(`ir.Func.Library`, set for every function of a `std/*` module), not
extern and not an intrinsic. Imported user module: a module of the
program other than the main module whose files the project loader found
from an `import` (not a `std/*` module). Call site: one direct `ir.Call`
to a function by name. Copy: the inliner's rewrite of a call site into
copies of the callee's blocks. Caller: the function the copy is made
into. Prologue: the instructions the copy places before the callee's
entry (parameter stores and aliases). Continuation: the block that
receives the rest of the call's block after the copy (`inline.join`).
Same-file rule: the eligibility condition `g.File != f.File → not
inlined` that this spec removes.

## 0. Goals

The goal is that a small user function is inlined by the same rules
whether its declaration is in the caller's file, another file of the
main module or an imported user module, and that every record, location
and report that names a file for a copied instruction names the file the
instruction came from. The non-goal is any change to what is inlined
within one file.

## 1. Background

The inliner (PLAN.md Phase 6) copies small user functions into their
callers in `--opt release` so that the lowering's proof facts reach
across calls. One of its eligibility rules refuses a callee declared in
another file, because a position is an offset within one file and the
copied instruction would carry the callee's offsets into a function the
backend and the interpreter attribute to the caller's file: a trap in the
copy would name the caller's file with the callee's span.

Since 93b0428 (module discovery) a program is routinely several files,
and since c4a8aca (`docs/reports/trap-file.md`) every function carries
its declaring file and both backends name it in trap records. With the
file on the function, the rule costs every multi-module program a real
call for each small helper an imported module exports, whatever its
size, while the same helper in the main module's file is inlined.

The library's functions (`std/*`, `ir.Func.Library`) are excluded by a
different rule (PLAN.md 6.2, 6.6: their traps report the user-level call
that entered the library, through hidden span and file parameters), and
that rule is not changed here: this spec does not make `std/str`,
`std/map` or `std/io` bodies inlinable.

## 2. Design

**The file lives on the block.** `ir.Block` gains a field `File string`.
Its effective file is `File` when non-empty, else the function's
declaring file. Lowering never sets it (every block of a lowered function
is in its declaring file). The inliner is the only writer: when it copies
a callee's blocks into a caller whose declaring file differs from the
callee block's effective file, each copied block carries that effective
file; when the files agree, the copied block's `File` stays empty, so the
IR of a program whose functions all share one file is unchanged in every
field. A block is never split across files: the prologue is parameter stores
and initializations, which carry no position, so it stays at the head of
the copied entry block as today, and the continuation (whose positions
are the call's and the rest of the call's block) keeps the call's block's
`File`.

**Consumers read the block.** Every place that today pairs an
instruction's position with `f.File` pairs it with the block's effective
file instead: the native trap record (the file pointer `kern_trap` takes,
and the hidden file argument a library callee takes), the native debug
location, the interpreter's trap, and the machine-API reports that locate
copies, allocation sites and bounds checks. Inside a library function
(which the inliner never copies into) the file stays the one the caller
passed, as today.

**Why the block and not the instruction or the position.** A field on
every position-bearing instruction (nine types) or on `ast.Pos` (used by
every compiler stage) would touch far more code and would have to be
maintained by every creator of an instruction; the inliner copies whole
blocks and nothing after lowering splits or merges blocks across
functions, so the block is the smallest unit that is always in one file.

**Failure mode.** A block whose `File` names a file no function of the
module declares is an IR invariant violation: `ir.Validate` rejects it,
and `opt.Run` reports the pass after which it appeared as an internal
error, as for every other violation (PLAN.md 6.1). The runtimes, the
interpreter and the reports therefore never see a file the program did
not load.

## 3. Out of scope

- Inlining library callees (PLAN.md 6.6, first item): unchanged, for the
  reason §1 gives.
- Inlining through function values and `thread.spawn`: unchanged.
- The runtime-raised traps (stack, mutex, condition variable, allocation)
  that name the program's file: unchanged (`docs/reports/trap-file.md`,
  "Not done").
- Any change to the size limits, the growth budget, the single-site rule
  or the loop rule.
- `explain-ownership`, which reads the lowered IR before any pass.

## 4. Requirements

Each requirement names how it is verified. "Both engines" means the
interpreter (`kern run`) and the native build; "both configurations"
means `--opt debug` (no inlining) and `--opt release`.

**R1. Block file.** `ir.Block` has a field `File string`. The effective
file of a block is `File` when non-empty, else the declaring file of the
function that contains it. The lowering leaves `File` empty on every
block it creates. *Verified by:* `go vet`; a lowering test over a
two-file program asserting every block's `File` is empty.

**R2. Eligibility.** The same-file rule is removed from the inliner's
eligibility: a user function declared in another file of the program,
in the main module or an imported user module, is judged by exactly the
remaining rules of PLAN.md 6.2 (not library, not extern, not intrinsic,
no release accessor, not recursive, no `out` or closure parameter, not
itself a closure, no returned access, the size, growth, single-site and
loop rules). A library function stays ineligible whatever its file.
*Verified by:* `compiler/opt/inline_test.go`: the existing
`TestInlineCrossFileStaysCall` is replaced by a test asserting the call
is gone and the callee is marked `Inlined`; a test over a callee in an
imported user module (`module lib`, called as `lib.twice`) asserting the
same; a test that a callee in another file which fails one other rule
(an `out` parameter) still stays a call; a test that a `std/*` callee
stays a call when the caller is in another file.

**R3. Files on copied blocks.** For every copied block, its `File` is the
callee block's effective file when that differs from the caller's
declaring file, and empty otherwise. This applies uniformly to a callee
block that already carries a `File` from an earlier copy into the
callee: the earlier override is carried when it differs from the new
caller's declaring file and dropped (normalized to empty) when the copy
returns to that file. The prologue carries no positions and the continuation has the call's
block's `File`, so every instruction whose position is the call's lies
in a block whose effective file is the call's block's, and every copied
instruction lies in a block whose effective file equals its source
block's effective file in the callee (which may be the caller's declaring
file, in which case `File` is empty). *Verified by:* `inline_test.go`:
after inlining a two-file program, every block of the caller that holds
a copied check or trap has `File` equal to the callee's file, the entry
and continuation blocks have `File` empty, and a three-file chain (`c`
calls `b` calls `a`, each in its own file, `b` inlined into `c` after `a`
into `b`) leaves `a`'s check in a block naming `a`'s file; an `a`-to-`b`-to-`a`
chain (`a` calls `b` in another file, which calls back into a helper
declared in `a`'s file, both copies made) leaves the helper's check in a
block whose `File` is empty; a one-file program after inlining has
`File` empty on every block.

**R4. Validation.** `ir.Validate` reports an error for a block whose
`File` is non-empty and is not the declaring file of any function of the
module. *Verified by:* a validator test that sets a block's `File` to a
name no function declares and expects exactly that error, and one that
sets it to another function's file and expects none.

**R5. Trap records, native.** The LLVM backend passes the block's
effective file as the file argument of `kern_trap` for a trap or check in
that block, and as the hidden file argument of a library callee called
from that block; inside a library function the file is the caller's, as
today. When every trap edge of a function is in the function's declaring
file the emitted trap landing block is unchanged (no extra phi); when
they differ the file arrives by phi like the kind and span. *Verified by:*
R10's fixture; R11.

**R6. Trap records, interpreter.** The interpreter reports, for a trap or
check executed in a block, the block's effective file, with the library
activation rule unchanged. *Verified by:* R10's fixture (the harness
compares the file between engines).

**R7. Debug locations.** With debug info on, an instruction in a block
whose effective file differs from the function's declaring file gets a
`DILocation` whose scope is a `DILexicalBlockFile` of the function's
subprogram for that file, and whose line and column are computed against
that file's source; instructions in the function's own file are unchanged.
*Verified by:* a backend test that the emitted module for a two-file
release build contains a `DILexicalBlockFile` naming the callee's
`DIFile` and that clang accepts the module; the report records one
`llvm-dwarfdump --debug-line` check showing rows for both files inside
the caller.

**R8. Machine-API reports.** `show-copies`, `show-allocations` and
`show-bounds-checks` locate an instruction by its block's effective file.
A check copied from another file is reported at the callee's file and
span, with its provenance as proven or runtime-checked by the proof pass
as for a same-file copy. *Verified by:* an analysis test over a two-file
program under `opt.OptionsFor("release")` asserting the copied check's
`Loc.File` is the callee's file and its span is the callee's.

**R9. IR text.** `ir.Print` writes a block's `File`, when non-empty, on
the block's label line (`bb3.entry [file lib/lib.kern]`), so `emit-ir`
shows which file a copied block came from. *Verified by:* an `ir`
printer test; R10's `emit-ir` check.

**R10. Fixtures.** A new multi-file trap fixture
`tests/trap/inlined_import_trap/` (an `app.kern` importing `lib`, whose
small exported helper traps on a bounds check; no `config:` line, so it
runs in both configurations and both engines; `//! trap-file:
lib/lib.kern`) produces the same trap record (kind, span, file) in all
four runs, and under `--opt release` the helper's call is gone from
`emit-ir` and present under `--opt debug`. A new run fixture
`tests/run/inlined_import/` exercises an imported helper with an `own`
parameter with a destructor, a `view` of a struct field and two return
paths, inlined in release, with identical output in all four runs. Every
existing fixture passes unchanged. *Verified by:* `go test ./...`.

**R11. Single-file programs unchanged.** For every program whose functions
share one declaring file, the LLVM IR the backend emits is byte-identical
before and after this change, in both configurations. *Verified by:*
`kern build --emit-llvm` of the thirteen `bench/kern` workloads and of
every single-file fixture under `tests/run`, `tests/trap` and
`tests/positive`, captured before the change and compared after it; the
`bench/REPORT.md` checksums are unchanged (`go test ./...` checks them).

**R12. Multi-module benefit, measured.** On a two-file variant of
`bench/kern/packet_parser` with its field readers in an imported module
(kept outside `bench/`, in the report's scratch), `emit-ir` under release
shows no call to the readers, and its native throughput is within 3% of the
single-file build's, as the median over at least five same-round pairs of
interleaved runs of the two binaries on one host (the method of
`docs/reports/thread-pool-job-path.md`); before the change the two-file
variant shows the calls in `emit-ir`, and its same-round throughput
relative to the single-file build is recorded as the baseline. *Verified by:* the
numbers in `docs/reports/cross-file-inlining.md`.

**R13. Determinism.** The pass remains a function of the module alone:
with the file rule gone, functions are still visited in name order and
sites in block then instruction order, and the `Inlined` marking applies
across files (a callee in another file whose every site was copied and
that nothing references is not emitted, run or reported). *Verified by:*
running `emit-ir` twice on the two-file fixtures and comparing; the
`Inlined` assertion in R2's tests.

**R14. Documents.** `docs/reports/cross-file-inlining.md` records the
design, the measurements of R7, R11 and R12, and the review;
`docs/reports/inliner.md`'s deferred list and PLAN.md 6.6 note the lift;
the comment on `InlineSingleSite` and the pass's header no longer list
"same file". *Verified by:* review.

## 5. Acceptance criteria

The change is accepted when all of the following hold: `golangci-lint
run ./...` and `go test ./...` are clean, which covers R1–R4, R6, R8–R10
and the checksums of R11; the captured LLVM IR of R11 is byte-identical
for every single-file program that built before the change (the set of
programs that fail to build is the same before and after); R7's
`llvm-dwarfdump` check and R12's interleaved measurement are recorded in
`docs/reports/cross-file-inlining.md` with their numbers; and the
documents of R14 are updated.

## 6. Risks

- **A block created after lowering by a later pass.** Today only the
  inliner creates blocks after lowering (`compiler/opt`); a future pass
  that splits a block must copy its `File`. The validator (R4) does not
  catch a dropped `File` (an empty one is valid); the trap fixtures of
  R10 would, for a trap in the dropped region.
- **Prologue positions.** The prologue is attributed to the callee's
  file by its block; this is sound only while its instructions carry no
  position. A future prologue instruction with the call's position must
  go in the call's block. R11 is the gate for any layout change: if the
  emitted IR of a single-file program differs, the change is wrong, not
  accepted.
- **Debug scopes.** `DILexicalBlockFile` is the LLVM form for code from
  another file in one function (as `#include`d code); if a toolchain
  rejects it, R7's test fails at clang, not at run time.
- **Growth.** Removing the rule lets more sites qualify; the growth
  budget and the size limit bound each caller as before (R2), so a
  multi-module program cannot grow more than a one-file program with the
  same calls.
