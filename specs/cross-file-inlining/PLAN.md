# Plan: cross-file inlining, a file on every copied block

**Spec:** `specs/cross-file-inlining/SPEC.md` v0.1. **Profile:** davin-go.
**Order:** the phases depend on each other in order. Phases 1 and 2
form one correctness gate: Phase 1 ends with its package tests green
(`go test ./compiler/ir ./compiler/opt ./compiler/lower`), Phase 2 with
the complete suite green with no skips (`golangci-lint run ./...`, `go
test ./...`), and nothing is committed between them; Phases 3 and 4 each
end with the complete suite green. The change lands as one commit after
Phase 4 unless the owner asks for more.

**Before Phase 1 (done, 2026-10-30):** the LLVM IR of every single-file
program (`bench/kern/*/*.kern`, `tests/run/*.kern`, `tests/trap/*.kern`,
`tests/positive/*.kern`) in both configurations was captured with the
compiler at 396ff89 into the session's scratch directory
(`ll-before/`, 218 modules; 12 programs that do not build with the hosted
target are listed in `ll-before/failed.txt`), by `capture-ll.sh`, for R11's comparison in Phase 3. The script is kept
in the repository as `scripts/capture-ll.sh` (usage: `scripts/capture-ll.sh
<kern-binary> <out-dir>`; it runs `kern build --opt=<debug|release>
--emit-llvm=<out>/<program>.<opt>.ll` for the host target and lists the
programs that fail to build in `<out>/failed.txt`), so the comparison can
be repeated: build the baseline compiler from the pre-change revision in
a separate worktree (`git worktree add <dir> 396ff89 && (cd <dir> && go
build -o <kern-base> ./cmd/kern)`), capture with it, build the current
compiler, capture again, and `diff -r` the two directories. The report
records the revision, the module count and the failure list.

## Phase 1: the block's file and the inliner (R1, R2, R3, R4, R9, R13)

**Goal.** Give `ir.Block` its `File`, print and validate it, and make the
inliner judge a callee in another file by the remaining rules and stamp
the copied blocks.

**Files.** `compiler/ir/ir.go` (the field), `compiler/ir/print.go` (the
label line), `compiler/ir/validate.go` (the module-level check),
`compiler/opt/inline.go` (eligibility, `rewrite`), `compiler/opt/inline_test.go`,
`compiler/ir/validate_test.go` (new, or the existing IR test file if one
exists), `compiler/lower/lower_test.go` (R1's lowering assertion).

**Steps.**

1. `ir.Block` gains `File string` with a comment stating the effective-file
   rule (R1) and that the inliner is its only writer. Add
   `func (b *Block) FileIn(f *Func) string` returning `b.File` when
   non-empty, else `f.File`: the one definition of "effective file" every
   consumer calls.
2. `ir.Print`: when `b.File != ""`, the label line is
   `bb<ID>.<name> [file <File>]:` (R9). No IR parser exists, so nothing
   reads it back.
3. `ir.Validate`: before the per-function loop, collect the declaring
   files of the module's functions; for every block with a non-empty
   `File` not in that set, append `fmt.Errorf("%s: block %s: file %q is
   not declared by any function of the module", f.Name, blockName,
   b.File)` (R4). The check is module-level, so it is in `Validate`, not
   in `validator.run`.
4. `inline.go`: delete the `g.File != f.File` case from `eligible` and the
   "same file" words from the comment on `InlineSingleSite` and from the
   package comment (R2). In `rewrite`, when creating each copied block set
   `File` to `blk.FileIn(g)` if it differs from `f.File`, else `""` (R3,
   including the normalization of a carried override); the continuation
   block gets `File: b.File`.
5. Tests (`inline_test.go`; the validator and lowering tests live in
   `compiler/opt` too, since its helpers build multi-file modules, rather
   than in new test files under `compiler/ir` and `compiler/lower`):
   replace `TestInlineCrossFileStaysCall` with
   `TestInlineCrossFile` (call gone, callee `Inlined`, the copied
   `CheckOverflow`'s block has `File == "b.kern"`, the entry and
   `inline.join` blocks have `File == ""`); `TestInlineImportedModule`
   (`module lib` in `lib/lib.kern`, `lib.twice`; same assertions);
   `TestInlineCrossFileOutParamStaysCall`; `TestInlineCrossFileLibraryStaysCall`
   (a `std/*` callee from a second file stays a call);
   `TestInlineCrossFileChain` (three files, `a`'s check ends in a block
   naming `a`'s file inside `c`); `TestInlineCrossFileRoundTrip` (`a` calls
   `b` in another file, `b` calls a helper declared in `a`'s file; after
   both copies the helper's check is in a block with `File == ""`);
   `TestInlineSameFileNoBlockFile` (one file: every block `File == ""`).
   The existing helper `lowerFiles` builds multi-file programs; check
   whether it supports a sub-directory path such as `lib/lib.kern` and a
   second module, and extend it if not (the project loader in
   `compiler/project` is the reference for how imports map to files).
   Validator tests: `TestValidateBlockFile` (an undeclared file errors,
   another function's file does not). Lowering test: every block of a
   two-file program has `File == ""` after `lower.Lower` (R1).
   Determinism: `TestInlineCrossFileDeterministic` prints the module
   after two independent lower-and-inline runs and compares (R13).
   Printer: `TestPrintBlockFile` asserts the label line of a copied
   block reads `bb<ID>.<name> [file <path>]:` and a same-file block's
   does not (R9).

**Acceptance.** The tests above pass and `go test ./compiler/ir
./compiler/opt ./compiler/lower` is green. The full suite is not a gate
here: `tests/trap/import_trap`'s `Pick` is now inlined in release and
its record names the right file only once Phase 2's consumers read the
block, so the full-suite gate is Phase 2's (see **Order**).

**Risks.** `lowerFiles` may not model imported modules; if extending it
is more than a few lines, drive those tests through the project loader
as `tests/project_test.go` does.

## Phase 2: the consumers (R5, R6, R7, R8)

**Goal.** Every consumer of an instruction's file reads the block's
effective file.

**Files.** `compiler/backend/llvm/calls.go` (`file`, `spanFile`,
`withSpan`), `compiler/backend/llvm/emit.go` (the trap landing block, the
block loop), `compiler/backend/llvm/debug.go` (`location`),
`compiler/backend/llvm/emit_test.go`, `compiler/interp/interp.go`
(`frame`, `callAt`, `trap`), `compiler/analysis/analysis.go` (`loc`),
`compiler/analysis/analysis_test.go` (new if absent).

**Steps.**

1. Backend, file operand: `fnEmit.file()` returns `%span.f` inside a
   library function, else the file symbol of `fe.cur.FileIn(fe.f)` (null
   when that is empty). `fe.cur` is already set per block before any
   instruction is emitted; the stack check in the prologue is emitted
   before the block loop and keeps the function's own file (use `f.File`
   there explicitly, not `fe.cur`). Library calls (`withSpan`) and
   `ir.Trap` (`spanFile`) pick the change up through `file()` (R5).
2. Backend, trap landing block: `trapEdge` gains `file string` (the
   operand `file()` rendered when the edge was recorded). The baseline
   operand is `funcFile()`: `%span.f` inside a library function (the
   file its caller passed), else the function's declaring file's symbol
   (`null` without one). After the kind, start and end phis, if any
   edge's file differs from the baseline, emit `%trap.file = phi ptr
   [...]` and pass it to `kern_trap`; otherwise pass the baseline as
   today, so that single-file output is byte-identical and a library
   function (whose blocks are never copies) keeps reporting `%span.f`
   (R5, R11). The backend test asserts both: one phi in the two-file
   module (main's) and `ptr %span.f` in a library function's landing
   block. Find every site
   that appends to `fe.traps` (`rg "fe.traps = append"`) and record the
   file there.
3. Backend, debug: `location(f, p)` becomes `location(f, b, p)`; when
   `b.FileIn(f) != f.File`, the scope is `!DILexicalBlockFile(scope:
   <subprogram>, file: <DIFile of b's file>, discriminator: 0)` (cached
   per subprogram and file in a new map), and `lineCol` runs against
   `b`'s file; the key of the location cache includes the scope (R7).
   The caller in `emit.go`'s block loop passes `b` for the block's
   instructions. The terminator's location is synthetic (`f.Pos`, a
   position in the function's declaring file) and stays paired with
   that file: it is emitted with `location(f, nil, f.Pos)`, which uses
   the subprogram scope, whatever block it ends. The same rule holds for
   an instruction whose position is empty (today `location` substitutes
   `f.Pos`): an empty position is paired with the function's file and
   scope, not the block's. Check
   `d.sources` holds every file of the program, including an imported
   module's (`opts.Sources` is built by `native.Build` from `srcs`; the
   project loader's file set includes imports).
4. Interpreter: `frame` gains `block *ir.Block`; `callAt`'s loop sets
   `fr.block = b` before executing the block's instructions and before
   the terminator; `trap` uses `it.cur.block.FileIn(it.cur.f)` when
   `it.cur` is not a library activation and the block is set, else the
   existing rule (`it.cur.file`, then `it.file`) (R6). The stack trap
   raised before the loop has no block and keeps the function's file.
   A library activation entered from a user frame inherits, with the
   call's span, the *effective file of the caller's current block*
   (`prev.block.FileIn(prev.f)`), not `prev.file`: natively the hidden
   file argument of a library callee is `file()`, the block's, so a
   `Vec.get` out of range inside a copied helper names the helper's file
   in both engines. A library activation entered from a library frame
   keeps inheriting `prev.file`, as today.
5. Analysis: `loc(f, p)` becomes `loc(f, b, p)` with `b.FileIn(f)`; the
   four call sites are inside `for _, b := range f.Blocks` loops and
   pass `b` (R8).
6. Tests. `emit_test.go`: a two-file release program whose imported
   helper has a bounds check, with the helper's file laid out so that
   the check's line differs from any line of the caller's file (a run
   of blank lines before the helper); assert the emitted module contains
   `DILexicalBlockFile` referencing the helper's `DIFile`, a
   `DILocation` in that scope whose line is the check's line in the
   helper's file, and a `%trap.file = phi ptr` whose entries name both
   file symbols, and that a one-file program's module contains neither
   (R5, R7, R11); build the two-file module with clang (`native.Build`
   on the files with `Debug: true`, as other backend tests do) to show
   the toolchain accepts the scope. Clang rejecting the module fails
   this gate; there is no fallback representation (a different scope
   form would be a spec change, R7). An
   interpreter test is unnecessary beyond R10's fixture: the harness
   compares the file between engines. `analysis_test.go`:
   `show-bounds-checks` over the two-file program under
   `opt.OptionsFor("release")` reports the copied check at
   `lib/lib.kern` with the callee's span (R8).

**Acceptance.** `tests/trap/import_trap` passes again in both
configurations; the tests above pass; `go test ./...` green.

**Risks.** The debug scope: `DILexicalBlockFile` is the form clang
itself emits for code from an included file, so rejection would mean a
malformed node, fixed in place; a wrong-file location is not an
acceptable outcome (R7, acceptance criteria).

## Phase 3: fixtures and measurements (R10, R11, R12)

**Goal.** The end-to-end evidence.

**Files.** `tests/trap/inlined_import_trap/app.kern`, `lib/lib.kern`;
`tests/run/inlined_import/app.kern`, `lib/lib.kern`, `app.out` (the
harness's convention for a directory fixture's expected output: check
`tests/run/` and `tests/harness_test.go` for how a directory fixture
names its `.out`); scratch: `ll-after/` and the two-file packet parser.

**Steps.**

1. `inlined_import_trap`: `lib.Pick(xs: view[[]u64], i: usize) -> u64`
   returning `xs[i]`, called once in bounds then once out of bounds,
   with a sentinel line after the trap and a `nocopy` guard with a
   destructor that must not run (the shape of `tests/trap/import_trap`);
   and `inlined_import_libtrap`, the same shape with the helper's trap
   raised by a library call it makes (`Vec[u64].get` past the length),
   which exercises the hidden file argument of a library callee from a
   copied block natively and the library activation's inherited file in
   the interpreter; both with the header `//! phase: 3`, `//! expect: trap bounds`, `//! trap-file:
   lib/lib.kern`, no `config:`. A CLI test in `cmd/kern/project_test.go`
   (which already drives multi-file programs) runs `emit-ir
   --opt=release` on the fixture and asserts no call to `lib.Pick` and a
   `[file ` label, and `emit-ir --opt=debug` and asserts the call (R9,
   R10); it runs `emit-ir --opt=release` twice and asserts identical
   output (R13).
2. `inlined_import`: `lib` exports a `nocopy` struct with a destructor
   that prints, and one helper `Use(t: own Token, k: view[u64]) -> u64`
   that takes the struct by `own`, reads a field of it and the `view`
   argument, and returns on one of two paths (an early `return` under a
   condition on `k`, else a fall-through), dropping the `own` argument on
   both; `app` calls it twice (each path), with a `view` of a struct
   field as `k`, and prints; the harness compares the four runs, and the CLI test of step 1 also asserts on this fixture that `emit-ir
   --opt=release` has no call to any `lib.` function (so output equality
   is not passing with the calls retained): every function `lib` exports
   here (`Make`, `MakePair`, `Use`) is eligible under the unchanged rules
   (straight-line, far under the size limit, by-value, `own` and `view`
   parameters, no `out`, no returned access, not recursive), so all three
   calls disappear and the assertion names no other function (R10).
3. R11: build the compiler, run `capture-ll.sh` into `ll-after/`, `diff
   -r ll-before ll-after` (expect no output), and `diff failed.txt`.
4. R12 (a gate: the change is not accepted while the two-file variant
   is more than 3% slower than the single-file build, or while its
   release `emit-ir` shows a `rd.` call): in scratch, `pp2/main.kern` is `bench/kern/packet_parser/packet_parser.kern`
   with `rd16`/`rd32` (and whatever small readers it has) moved into
   `pp2/rd/rd.kern` as `module rd` and called as `rd.rd16`; build both
   with `kern build --opt=release`, check `emit-ir` shows no `rd.` call;
   run `cmp.sh`-style interleaved rounds (five or more) of the single-file
   and two-file binaries and take the median same-round ratio; repeat with the baseline compiler (`kern-base`) for the before number,
   and confirm with the baseline compiler's `emit-ir` that the two-file
   variant shows the `rd.` calls. Record the host's one-minute load with
   the numbers. Respect the bench slot
   (`pgrep` for a running harness first).
5. R7: `kern build --opt=release` with debug info on
   `inlined_import_trap` (`kern debug` builds with `Debug: true`; find the
   CLI flag or call `native.Build` with it from a one-off Go test) and `llvm-dwarfdump --debug-line` on the binary: record, inside the
   address range of `app.main`, rows for both `app.kern` and
   `lib/lib.kern`, with the `lib/lib.kern` row's line being the check's
   line in that file.

**Acceptance.** `go test ./...` green including the new fixtures; R11
diff empty; R12 and R7 numbers in hand.

**Risks.** The readers (`rd16`, `rd32`) are straight-line and well
   under the size limit, and the loop rule only keeps a callee *with a
   loop* out of a caller's loop, so they qualify; if `emit-ir` shows a
   `rd.` call anyway, that is a defect in this change to find and fix,
   not a reason to pick other readers.

## Phase 4: documents and review (R14)

**Goal.** Record and review.

**Files.** `docs/reports/cross-file-inlining.md` (new),
`docs/reports/inliner.md` (deferred list), `specs/initial/PLAN.md` 6.6
(a one-line note that cross-file inlining landed, with the spec's path),
`specs/cross-file-inlining/SPEC.md` (review status), `README.md` if it
describes the inliner's file rule (`rg "same file" README.md docs/`).

**Steps.**

1. Write the report: design, what changed per package, the R7 dwarfdump
   rows, the R11 identity, the R12 numbers with load, the review
   findings and how each was handled.
2. Update the deferred lists and the spec's review status.
3. Review pipeline: `golangci-lint run ./...`, `go test ./...`, `prism
   review staged --provider openai --model gpt-6.1-sol` (fix high and
   medium or justify), `realitycheck check . --spec
   specs/cross-file-inlining/SPEC.md --plan specs/cross-file-inlining/PLAN.md
   --provider openai --model gpt-6.1-sol --ignore 'bench,runtime,std,examples,tools,scripts,docs' --format json`,
   `verifier analyze -root . -mode offline -spec ... -plan ... -include
   'compiler/opt/**,compiler/ir/**,compiler/backend/llvm/**,compiler/interp/**,compiler/analysis/**' -format text`;
   clarion skipped (no `docs/clarion-meta.json`).
4. One conventional commit on `main`:
   `feat(opt): inline callees from other files, with the file on every
   copied block`, body naming the spec and report, ending with the
   attribution line. Push only if the owner asks.

**Acceptance.** Reviews clean or justified in the report; commit made.
