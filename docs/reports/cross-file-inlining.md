# Cross-file inlining: a file on every copied block

**Date:** 2026-10-30. **Spec:** `specs/cross-file-inlining/SPEC.md` v0.1.
**Plan:** `specs/cross-file-inlining/PLAN.md`. **Lifts:** PLAN.md 6.6's
second deferral ("cross-file inlining needs a file on every position or
on every instruction"). **Follows:** `docs/reports/trap-file.md` (a file
on every function, c4a8aca) and `docs/reports/module-discovery.md`
(programs of several files found from their imports, 93b0428).

## Before

The inliner refused any callee declared in another file (`g.File !=
f.File`): a position is a byte offset within one file, and the backend
and the interpreter attributed every instruction of a function to the
function's file, so a copied check would have reported the caller's file
with the callee's span. After module discovery a program is routinely
several files, and every small helper an imported module exported stayed
a real call in release, whatever its size, while the same helper in the
main file was inlined.

## Design

The file lives on the block. `ir.Block` gained `File string`, empty on
every block the lowering creates; `Block.FileIn(f)` is the effective
file (the block's own when set, else the function's declaring file). The
inliner is the only writer: a copied block carries its source block's
effective file when that differs from the caller's declaring file, and
nothing otherwise, which also normalizes an override carried from an
earlier copy when a chain returns to its own file. The prologue carries
no positions (parameter stores and initializations), and the
continuation holds the rest of the call's block in the call's block's
file. Every consumer that paired an instruction's position with
`f.File` now reads the block:

- **Native trap records.** `fnEmit.file()` renders the file operand of
  the block being written, which the `kern_trap` of an `ir.Trap` and the
  hidden file argument of a library callee take. The shared trap landing
  block takes the file by a fourth phi only when some check sits in a
  block of another file; otherwise it passes the function's file as a
  constant, as before. The edges' file operands are rendered at the
  landing block, after the function's own, so that a one-file program
  interns its file constant at the same point and its output is
  byte-identical (R11; the first version interned it at the check and
  moved one declaration line in 48 of the 218 captured modules).
- **Debug locations.** An instruction in a copied block of another file
  gets a `DILocation` scoped in a `DILexicalBlockFile` of the
  function's subprogram for that file, with line and column computed
  against that file's source; terminators and empty positions keep the
  function's own file and scope.
- **Interpreter.** A frame records the block being executed; a trap
  reports the block's effective file, and a library activation entered
  from a user frame inherits the caller's *block's* file with the span,
  as the hidden argument does natively (plancritic's second question
  found this case: a `Vec.get` past the length inside a copied helper).
- **Machine-API reports.** `show-copies`, `show-allocations` and
  `show-bounds-checks` locate an instruction by its block's file.
- **Validator and printer.** `ir.Validate` rejects a block file no
  function of the module declares; `ir.Print` writes `[file <path>]` on
  a copied block's label, so `emit-ir` shows where a block came from.

The library rule is unchanged: `std/*` bodies are still never copied,
for the reason PLAN.md 6.6 gives (their traps report the user-level call
through hidden parameters). This change does not inline `std/str`,
`std/map` or `std/io` into their callers.

## What changed, by package

| Package | Change |
|---|---|
| `compiler/ir` | `Block.File`, `Block.FileIn`; the module-level file check in `Validate`; the label annotation in `Print` |
| `compiler/opt` | the same-file case removed from `eligible`; `rewrite` stamps copied blocks and the continuation |
| `compiler/backend/llvm` | `file`, `blockFile`, `funcFile`, `fileOperand` (calls.go); the trap edge's file and the conditional file phi; `location(f, b, p)` with the lexical-block-file scope |
| `compiler/interp` | `frame.block`; `trap` and the library activation's inherited file |
| `compiler/analysis` | `loc(f, b, p)` at the four report sites |
| `cmd/kern` | `TestCrossFileInliningIR` (emit-ir on the three fixtures, both configurations, twice) |
| `tests` | `trap/inlined_import_trap`, `trap/inlined_import_libtrap`, `run/inlined_import` |
| `scripts` | `capture-ll.sh`, the byte-identity capture |

Tests added: eleven in `compiler/opt` (cross-file, imported module, the
`out`-parameter and library exclusions across files, a three-file chain,
an a-to-b-to-a round trip, same-file blocks unlabelled, lowering leaves
`File` empty, the validator's check, determinism, the printer), two in
`compiler/backend/llvm` (the trap-file phi and the library baseline; the
debug scope, line 12 of `lib/lib.kern`, and clang accepting the module)
and one in `compiler/analysis` (`show-bounds-checks` locates the copied
check in `lib/lib.kern`).

## Measured

**Fixtures (R10).** The three new multi-file fixtures pass in both
engines and both configurations; the two trap fixtures' records agree on
kind, span and file (`lib/lib.kern`) across all four runs. `emit-ir
--opt=release` shows no call into `lib` and one `[file lib/lib.kern]`
label on each; `--opt=debug` shows the calls and no label.

**Byte identity (R11).** `scripts/capture-ll.sh` emitted the LLVM IR of
every single-file program under `bench/kern`, `tests/run`, `tests/trap`
and `tests/positive` in both configurations with the compiler at 396ff89
and with this change: 218 modules, `diff -r` empty, and the same twelve
programs failing to build with the hosted target in both captures
(`tests/trap/oom.kern`, and `tests/positive/{expanding_generic,
nested_generic_markers, nested_out_fields, projection_field_path,
recursive_type_markers}.kern`, each in both configurations). The
`bench/REPORT.md` checksums are unchanged (`go test ./...`).

**Debug line table (R7).** `tests/trap/inlined_import_trap` built with
`kern build --opt=release --debug --emit-llvm=app.ll`, compiled with
`clang -c -O0 -g` and dumped with `llvm-dwarfdump --debug-line`: the
unit lists `app.kern` (file 1) and `lib.kern` in directory `lib` (file
2), and inside `app.main` the rows alternate between file 1 (lines 11,
14–19 of `app.kern`) and file 2 (line 12, `Make`'s struct literal, and
line 15, `Pick`'s `xs[i]`, of `lib/lib.kern`). At `-O2` both calls fold
to constants and only `app.kern` rows remain, which is LLVM's doing, not
the scope's. The emitted module holds one `DILexicalBlockFile(scope:
!3, file: !5)` with `!5 = DIFile("lib.kern", "lib")`, and clang accepts
it (the backend test compiles it to an object on every run).

**Two-file packet parser (R12).** `bench/kern/packet_parser` with `rd16`
and `rd32` moved to an imported module `rd` (as `rd.Rd16`, `rd.Rd32`,
eleven call sites), built with the baseline compiler and with this
change, alongside the single-file program built with each.

| | proven checks | runtime checks | `rd.` calls in release IR |
|---|---|---|---|
| single file, either compiler | 12 | 35 | n/a |
| two files, baseline | 10 | 37 | 11 |
| two files, this change | 12 | 35 | 0 |

The two checks the baseline lost are the readers' own, proven at the
call site once the body is copied, as in the single-file build. Same-round
throughput, interleaved rounds of the four binaries (`ops=10000000`,
one host, `cmp2.sh`):

| run | load | rounds | single/new | two/new | two/base | single/base | two/new ÷ single/new |
|---|---|---|---|---|---|---|---|
| 1 (07:10) | 7.9 | 7 | 65.98 ms | 66.54 ms | 67.34 ms | 66.22 ms | 1.008 |
| 3 (07:12) | 7.5 (system daemons, no build or test of this project running) | 9 | 66.48 ms | 66.65 ms | 66.25 ms | 66.27 ms | 1.003 |

(A second run of 9 rounds at 07:10, while `go test ./...` ran in the
background, had maxima of 106–152 ms and medians 71–83 ms; it is not
used.)

Within the 3% of R12 in every run. The baseline's two-file build is only
1–2% slower than its single-file build because clang's own inliner
copies the readers at `-O2` whatever kern did; what kern's inliner adds
across files is the proof facts (the two checks above), not the call.
That is also why the single-file benchmarks could not show this change:
every workload is one file (R11 proves they emit the same IR).

## Review

- **speccritic** (gpt-6.1-sol, profile general, with
  `docs/reports/trap-file.md` and `compiler/opt/run.go` as context): the
  first draft was INVALID on the preflight's two missing sections (goals,
  acceptance criteria), added; the second VALID_WITH_GAPS with no
  critical finding; one contradiction (R3 claimed no copied instruction
  lies in a block of the caller's file, false when the files agree) was
  reworded; the scope-leak note is intended (the block representation is
  normative).
- **plancritic** (profile davin-go, with the spec): three rounds,
  EXECUTABLE_WITH_CLARIFICATIONS each (79, 93, 86), no critical finding,
  full requirement coverage from the second round. Folded in: Phases 1
  and 2 are one correctness gate (the full suite is not green between
  them: `tests/trap/import_trap` traps inside a now-inlined helper);
  terminator locations stay paired with the function's file; no
  wrong-file fallback for the debug scope; the interpreter's library
  activation inherits the block's file (a real gap, see Design); the
  trap-file phi's baseline named (`%span.f` in a library function); the
  run fixture's helper exercises `own`, a field view and two return
  paths in one function; the baseline assets' provenance recorded (this
  report and `scripts/capture-ll.sh`).
- **prism** (gpt-6.1-sol, staged diff): one medium finding, on `scripts/capture-ll.sh`
  (a relative output directory or compiler path was resolved after the
  script changed to the repository root), fixed by resolving both
  against the invocation directory first and verified by a capture from
  another directory that matched the in-tree one; the compiler change
  itself drew no finding. The second run, on the final diff, had one low
  finding on the same script (a reused output directory kept an earlier
  run's modules), fixed by clearing the directory's captures first.
- **realitycheck** (spec and plan of this change, `compiler/` and
  `cmd/`, `tests/`): PARTIALLY_ALIGNED, score 100, no
  violation: every requirement is matched to its evidence (the tests
  and symbols named above), and the "partial" statuses say only that the
  tool's code index truncated the function bodies (939 symbols past its
  40,000-character limit) and could not read them, not that anything
  drifted.
- **verifier** (offline, the five packages): 69 recommendations on the real tree (it also
  scanned two stale worktrees under `.claude/`), all of the form
  "exported symbol has no corresponding test" for symbols that predate
  this change (`Interp.Run`, `Module.Func`, `Validate`, `opt.Run`,
  `PrintFunc`), plus `Block.FileIn`, which every cross-file test and all
  four consumers exercise; no new negative-path gap specific to this
  change.
- `golangci-lint run ./...` 0 issues; `go test ./...` green.

## Not done

- Library callees (PLAN.md 6.6, first item) stay calls in kern's IR;
  LLVM inlines them natively, the interpreter calls them.
- The runtime-raised traps (stack, mutex, condition variable,
  allocation) still name the program's file (`docs/reports/trap-file.md`,
  "Not done"); a copied block does not change that.
- A future pass that creates blocks after lowering must copy `File`
  (spec §6, first risk); the validator cannot tell a dropped file from
  an intended empty one, the trap fixtures can.
