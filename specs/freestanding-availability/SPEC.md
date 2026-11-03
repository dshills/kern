# Hosted-only runtime entries in a freestanding program: a diagnostic

**Version:** 0.1 (2026-11-03). **Scope:** a new diagnostic code under
Rule 6.8 (`docs/ownership.md`); its evaluation by `check`, `build` and
`run` in freestanding mode (`cmd/kern`); the analysis that finds the
runtime entries a program reaches (`compiler/analysis`, with recording in
the LLVM emitter, `compiler/backend/llvm`); the code registry
(`compiler/diag/codes.go`, the generated `spec/rules.md`,
`spec/rules.lock`); fixtures under `tests/`; the fixture harness's
support for a freestanding reject fixture; ADR-011's table; the report
`docs/reports/freestanding-availability.md`.
**Language impact:** a program that reaches a hosted-only runtime entry in
freestanding mode is rejected with a diagnostic before it is linked or
run. The rule reads the compiler's own IR, not what LLVM's optimizer
keeps, so a program that links today only because the optimizer deleted
every such call (dead code) is rejected too. That choice makes the
diagnostic the same at both levels and independent of the optimizer.
R2 checks that no existing program is rejected.
**Follows:** `docs/reports/std-time.md`, "Open items", third item.

## 0. Goals and non-goals

Today a freestanding program that calls a function whose runtime entry
only the hosted runtime provides passes `check`. `build` then fails with
an `INTERNAL` diagnostic quoting the linker, and `run` executes it,
because the interpreter emulates the entry. For example, `time.now()`
reaches `kern_now_ns`:

```
internal: ld.lld: exit status 1: ld.lld: error: undefined symbol: kern_now_ns
```

Goals:
- the same program is rejected by `check`, `build` and `run` in
  freestanding mode, with one diagnostic at each call in the program's
  own source that reaches such an entry;
- the set of entries the freestanding runtime provides has one source,
  the runtime's own C source, so the diagnostic and the linker cannot
  disagree.

Non-goals:
- making more of the library available in freestanding mode;
- `kern test`, which already refuses freestanding mode;
- any diagnostic in hosted mode, where every runtime entry exists.

## 1. Definitions

**Runtime entry.** A symbol the emitted LLVM IR of a program declares
(`declare … @kern_…`) and does not define: the entries of a runtime
called by the code the compiler generates for intrinsics, traps, output
and the prologue. The LLVM emitter is the only component that knows the
mapping from an IR instruction to the entries it uses.

**Provided set** (a constraint, so that the diagnostic and the image
are built from the same runtime). The external symbols `runtime/freestanding/src/rt.c`
defines: functions and objects at file scope that are not `static`. The
set is read from the C source embedded in the compiler
(`runtime/freestanding.RuntimeC`). The startup code's symbols (`_start`,
`kern_stack_limit`) are defined by the assembly startup files, embedded
beside it, and belong to the set.

**Hosted-only entry.** A runtime entry the freestanding emission of a
program declares that is not in the provided set.

**Reaching call.** A call instruction in a function of the program's own
files whose callee is either an intrinsic whose emission declares a
hosted-only entry, or a library function from which such an intrinsic
is reachable through library calls. A function reference (`funcref`,
which makes a function value or a closure's code pointer) to a library
function counts as a call to it, both as a reaching call and as an edge
between library functions. A reference to one of the program's own
functions or closures is never a reaching call: a reaching call inside
its body is reported there, once. A call through a function value adds no edge of its
own: the function it may call was referenced somewhere, and that
reference counts. Library functions are the
standard library's (`std/` and the prelude). Its position is the call's
source span, as `show-allocations` attributes a library call that
allocates to its caller.

## 2. The rule

**F1.** In freestanding mode, each reaching call of the program is an
error `ENV_HOSTED_ONLY` (Rule 6.8):
- **span:** the call's span. A call the compiler generates without a
  source position, such as an implicit drop that calls a library
  destructor, takes the declaration span of the function containing
  it;
- **message:** ``call to `N` needs the hosted runtime (`E`), which a
  freestanding image does not have``, where N is the callee's source-level
  name (`time.now`, `now_ns`). Calls merged at one span are one call
  expression or reference, so they have one N. E the first hosted-only entry it
  reaches, in byte-wise lexicographic order of the full symbol names; when it reaches several, E is followed by
  `` and K more`` with K their count less one;
- **related locations:** none;
- **fix:** null.

Calls at the same span, from instantiations or inlined copies, are one
diagnostic. Its entries are the union of the hosted-only entries every
such call reaches, and E and K are taken from that union.

**F2.** The entries a call reaches are the ones the compiler's
freestanding emission of the program, at the command's optimization
level and target, declares for that call's callee and the library
functions it reaches. Library functions count as calls, not as code
inlined into the caller, so the attribution is the same at both levels.
The roots are the lowering's: every non-generic function and method of
the program's own files and every destructor of a non-generic type, so a
reaching call in a function of the program that nothing calls is
reported too. Library functions are compiled only when reached from a
root, so a library function no reaching path calls is never reported. A call is
reaching whether or not a later optimization would remove it: the rule
is decided on the program as the compiler lowers it.

*Implementation note (not normative):* the module the show queries
lower, which has no library inlining, is emitted by the LLVM emitter in
freestanding mode with recording of each declared runtime entry and the
IR call that declared it.

**F3.** `check`, `build` and `run` evaluate it in freestanding mode (from
`--env freestanding` or the manifest's `env`). Each fails with exit code 2
on an `ENV_HOSTED_ONLY`, after resolution and checking succeed and before
anything is compiled to native code, linked, written to an output file or
executed. Lowering and the in-memory emission the rule reads are part of
evaluating it, and write nothing. This includes a
`build` without `ld.lld`, which writes an archive instead of an image:
it is diagnosed the same way, before the archive is written. `ENV_HOSTED_ONLY`
diagnostics appear together, sorted as other diagnostics are, and never
beside a resolution or checking error. In hosted mode no command
evaluates it, and nothing in hosted mode changes. `check` lowers a
program in freestanding mode for this; in hosted mode it lowers only as
it does today, for budgets.

## 3. Requirements and acceptance criteria

Each requirement is an acceptance criterion; the change is accepted when
all hold. The *baseline compiler* is the one built from this change's
parent commit on `main` (7071ca3 when this was written), and the
*baseline corpus* is that commit's tree. Every comparison against it
uses the same target, optimization level and flags.

**R1 — The rule.** A program calling `time.now()`, one calling the
prelude's `now_ns()` directly, and one calling a function of its own that
calls `time.sleep` are each rejected by `check`, `build` and `run` with
`--env freestanding`, with one `ENV_HOSTED_ONLY` at each call in its own
source that reaches the entry (for the third, the call to `time.sleep`
inside its own function), and exit code 2. Their hosted builds are
unchanged.

**R2 — No false rejection.** Every program under `tests/freestanding/`
in the baseline corpus is accepted by `check --env freestanding`
and builds and runs as before. Every fixture and test that passes with the baseline compiler passes,
and the freestanding workloads' images are byte-identical to the
baseline compiler's.

**R3 — One source.** The provided set has one authority, the freestanding
runtime's sources embedded in the compiler. A test checks that the set
contains every symbol of ADR-011's table, and, where `clang` and `llvm-nm` are available, that it equals the
defined external symbols of the compiled `rt.c` and startup objects for
both targets.

**R4 — Agreement with the linker.** For each of R1's three programs, the
set of hosted-only entries the analysis finds equals the set of symbols
`ld.lld` reports undefined when the image is linked with the
diagnostic bypassed (a test-only option, or the previous compiler's
build), on both targets, at `--opt=debug`, where `ld.lld` is available.
Each of the three programs prints the value its hosted call returns, or
passes it on, so that no optimization can delete the call and the
comparison is exact.

**R5 — Registry.** `ENV_HOSTED_ONLY` is registered with rule 6.8 and
category `lang`, in `compiler/diag/codes.go`, the generated
`spec/rules.md` and `spec/rules.lock`. Rule 6.8 of `docs/ownership.md`
states F1 to F3. A reject fixture in `tests/freestanding/` (header
`//! env: freestanding`) with an accepting twin covers it, and the
fixture harness checks a reject fixture in the environment its header
names.

**R6 — Cost.** `check` on the budget-free hosted files under `tests/`
is byte-identical to the baseline compiler's output, and the
freestanding evaluation is not entered (a counting hook, as the budget
tests use, shows zero entries).

**R7 — Documents.** ADR-011's table notes that the provided set is read
from `rt.c` for the diagnostic. `docs/reports/std-time.md` marks the
open item resolved. The report lists the hosted-only entries the
standard library's functions reach, by function. The list is not
exhaustive: it covers the library functions the programs under
`bench/kern/`, `examples/`, `tests/run/`, `tests/trap/`,
`tests/positive/` and `bench/agent/tasks*/t*/reference/` compile. A test
generates it from the analysis over those programs, at
`--opt=debug`, for each of the two freestanding targets, reported per
target (identical if no entry differs by target).

## 4. Risks

- **The emitter as the oracle.** Recording runtime entries while
  emitting ties the analysis to the emitter's choices. That is the point,
  since the emitter decides what is linked. R4 checks it against the
  linker.
- **Library attribution.** A reaching call into a library function that
  reaches a hosted entry only on some paths is still reported. The
  unoptimized link needs the entry whatever the path, and so does the
  rule, even where the optimizer would delete the path (§0, Language
  impact).
