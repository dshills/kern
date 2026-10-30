# Runtime-raised traps name the user-level call's file

**Version:** 0.1 (2026-10-30). **Scope:** the hosted runtime
(`runtime/hosted/src/kern_rt.c`) and the freestanding runtime
(`runtime/freestanding/src/rt.c`): the entry points that can raise a
trap; the LLVM backend's calls to them (`compiler/backend/llvm`); the
interpreter's runtime-object intrinsics (`compiler/interp`); the trap
record comparison of the fixture harness (`tests/harness_test.go`) and of
`kern test` (`compiler/testrun`); fixtures under `tests/`; the documents
that state the record's contract. **Language impact:** none: the record's
shape (`{"trap": {"kind", "file", "span", "thread"}}`) and the trap kinds
are unchanged. **Related:** `specs/initial/AI_Native_Systems_Language_SPEC.md`
§6 (trap records); `docs/reports/trap-file.md` (records name the trapping
function's file; its "Not done" is this spec's subject); ADR-012 (the
native stack-fault record has no span); `specs/test-command/SPEC.md` R9
(agreement rules).

**Review status.** speccritic (OpenAI gpt-6.1-sol, profile general,
with `docs/reports/trap-file.md` as context): VALID_WITH_GAPS, no
critical finding; its one clarification (R2's backend test names the
program, the function and the expected file constant) folded in; the
remaining warnings are the preflight's flags on R*n* labels and code
identifiers. R4's verification was then corrected to the fixtures
written (`Mutex.new` in an imported module, not a `Vec.push`, whose
`oom` is a library trap that already named the right file).

**Glossary.** R*n*: requirement *n* of §4. Runtime-raised trap: a trap
the runtime raises inside one of its own entry points rather than
generated code's `kern_trap` call for a check: allocation failure (`oom`),
reference-count overflow of `Shared` (`overflow`), misuse of `Mutex` or
`Condvar` (`explicit`), a violated `SystemAllocator` obligation
(`explicit`). Fault trap: a trap the hosted runtime's signal handler
raises (`stack`, `unreachable`), which has no source location. Location:
a span and the file it is in. User-level call: the call in the
program's code whose execution raised the trap; inside a library
function, the call that entered the library (the span and file its
callers pass along, `docs/reports/trap-file.md`). Program's file: the
file `kern_set_file` names (the file declaring `main`, or under `kern
test` the test's file). Engine: the interpreter or the native build.
Opt level: `debug` or `release`. Test hooks: the `testing.*` functions
of `--test-hooks` debug builds. All four configurations: both engines in
both opt levels.

## 0. Goals

Every trap record whose kind the runtime raises at a call names that
call's file and span, natively as the interpreter already does, so that
the record means the same thing in both engines whichever module the
call is in. The non-goals: a location for fault traps (ADR-012), new
trap kinds, a change to the record's shape.

## 1. Background

Since c4a8aca a check's trap names the file of the function it is in.
The runtime-raised traps did not change: `kern_mutex_lock` and
`kern_condvar_wait` take a span but no file, and `kern_shared_new`,
`kern_shared_clone`, `kern_mutex_new`, `kern_condvar_new`,
`kern_thread_spawn`, `kern_scope_new`, `kern_scope_spawn` and the four
`kern_sysalloc_*` functions take neither, so their records name the
program's file and, for all but the first two, the span 0–0. The
interpreter reports every one at the user-level call, with its file. The
two engines therefore disagree on the span of a `Shared.clone` overflow,
a runtime object's `oom` and a `SystemAllocator` obligation, and on the
file of every runtime-raised trap whose call is not in the program's
file. They also disagree on whether `testing.fail_next_alloc()` fails
the next runtime-object allocation: natively it does (`kern_alloc`
consumes it), in the interpreter only a `SystemAllocator` allocation
does.

## 2. Design

**A location argument.** Every runtime entry point that can raise a
trap takes the location of the user-level call as one trailing argument,
`const kern_loc *at`, a pointer to a record `{ int64_t start, end; const
char *file; }`, and raises every trap there (directly or through
`kern_alloc`). The backend emits the record once per distinct location
in user code, as a private constant holding the call's span and the file
operand of the block being written; inside a library function, whose
location is the one its caller passed (`%span.s`, `%span.e`,
`%span.f`), it fills a stack slot at the call. A null record means no location: the span 0–0 and the program's file.
A null file in a record means the program's file with the record's span,
exactly as a null file does for `kern_trap` (a function the lowering
gave no file; the interpreter reports the same). *Rationale:* three trailing register
arguments (the first version) cost the uncontended `Mutex.lock` four
instructions per lock and unlock (+7% on a lock loop); one pointer to a
constant costs no more than the two span immediates `kern_mutex_lock`
already took, and LLVM hoists the constant's address out of a loop. The entry points: `kern_shared_new`,
`kern_shared_clone`, `kern_mutex_new`, `kern_mutex_lock`,
`kern_condvar_new`, `kern_condvar_wait`, `kern_thread_spawn`,
`kern_scope_new`, `kern_scope_spawn`, `kern_sysalloc_alloc`,
`kern_sysalloc_alloc_zeroed`, `kern_sysalloc_realloc` and
`kern_sysalloc_free` (hosted `kern_sysalloc_free` raises no trap, but
the freestanding one does, and both runtimes share the backend's
declarations). Entry points that raise no trap are unchanged.

**The interpreter.** `Shared.new`, `Mutex.new`, `Condvar.new`,
`thread.scope`, `thread.spawn` and `scope.spawn` consume an armed
`testing.fail_next_alloc()` and trap `oom` at the call, as natively the
first allocation of the corresponding entry point does. Nothing else in
the interpreter changes: its runtime-raised traps already name the
user-level call.

**Fault traps.** The hosted signal handler knows neither a span nor a
file; its records keep the span 0–0 and the program's file (ADR-012).
The interpreter's `stack` record names the call that overflowed. The
fixture harness and `kern test` therefore compare neither the span (as
today) nor the file of a `stack` record.

## 3. Out of scope

- A location for fault traps (`stack`, and `unreachable` from a fault in
  safe code) natively.
- The thread identifier of a record.
- Runtime entry points that raise no trap.
- Performance work: the extra arguments are three registers on calls
  that already cross into the runtime (§4 R8 bounds the cost).

## 4. Requirements

Each requirement names how it is verified.

**R1. Signatures.** Each entry point listed in §2 takes the trailing
location record in both runtimes (the freestanding runtime has only the
four `kern_sysalloc_*` of the list: it implements no `Shared`, `Mutex`,
`Condvar` or threads), and every trap it raises uses it (`kern_alloc`
takes and forwards it). No runtime-
raised trap passes a constant 0–0 span or a null file except when the
location it was given is that. *Verified by:* review of both runtime
sources; the fixtures of R3–R6.

**R2. Backend.** Every call the backend emits to those entry points
passes the user-level call's location: a constant record of the call's
span and its block's file operand in user code; a slot filled from the
passed span and file inside a library function, which are 0–0 and null
where the library function was entered with no call (drop glue, a
library function taken as a function value, by the existing hidden
parameters of `docs/reports/trap-file.md`). *Verified by:* a backend test over a two-file program, `app.kern`
importing `lib/lib.kern`, whose function `lib.Locked` calls
`Mutex.new` and `lock`, built in `debug` (no inlining): the emitted
calls to `kern_mutex_new` and `kern_mutex_lock` inside `lib.Locked`'s
body pass location records whose spans are those calls' spans in that
file and whose file is the constant whose bytes are `lib/lib.kern`; and
the emitted call to `kern_sysalloc_alloc` inside the library function
`heap_try_alloc` passes a slot filled from `%span.s`, `%span.e` and
`%span.f`.

**R3. Mutex and Condvar.** A recursive `Mutex.lock` and a `Condvar.wait`
whose guard is not held, each called from an imported module's function,
produce the same record (kind `explicit`, the imported file, the call's
span) in all four configurations. *Verified by:* multi-file trap
fixtures `tests/trap/import_mutex_recursive/` and
`tests/trap/import_condvar_mismatch/` with `//! trap-file:` naming the
imported file.

**R4. Allocation.** With test hooks (debug, both engines), an armed
`testing.fail_next_alloc()` followed by `Mutex.new` traps `oom` at the
`Mutex.new` call; the same through `Shared.new`, `Condvar.new` and `thread.scope`; and a `Mutex.new` called in an imported module's
function after the hook is armed traps `oom` with that module's file and
the call's span. *Verified by:* the trap fixtures
`tests/trap/oom_mutex_new.kern`, `oom_shared_new.kern`,
`oom_condvar_new.kern`, `oom_scope.kern`, `oom_spawn.kern` (a
`thread.spawn`) and `oom_scope_spawn.kern` (a `spawn` on a scope created
before the hook is armed) (each `//! test-hooks: true`), and `tests/trap/import_oom/` with `//! trap-file:
lib/lib.kern`.

**R5. `Shared` overflow.** A `Shared.clone` whose count is at the
maximum (test hooks: `testing.seed_shared_count`) traps `overflow` at
the `clone` call's span in both engines. *Verified by:*
`tests/trap/shared_overflow.kern` (test hooks, debug, both engines).

**R6. Allocator obligations.** A `SystemAllocator.alloc` with an
alignment that is not a power of two traps `explicit` at the call in both
engines and both opt levels. *Verified by:*
`tests/trap/sysalloc_obligation.kern` (in `unsafe`, with a contract).

**R7. Fault traps.** The fixture harness and `kern test` compare neither
the span nor the file of a `stack` record; every other field and every
other kind is compared as before. *Verified by:* a multi-file fixture
`tests/trap/import_stack/` (unbounded recursion in an imported
module's function) passing in both engines; a `testrun` unit test that
two `stack` runs differing in file agree and two `bounds` runs differing
in file do not.

**R8. No added work.** Measured as instructions retired (macOS
`/usr/bin/time -l`), which does not depend on the host's load, over at
least seven interleaved runs of release builds with and without this
change: an uncontended loop of 10,000,000 `Mutex.lock` and unlock does
not retire more instructions with the change (median ratio at most
1.00), and on the two lock-bound workloads (`bench/kern/lock_protected_map`,
`bench/kern/thread_pool`) the median ratio is between 0.97 and 1.03.
The thirteen workloads' checksums in `bench/REPORT.md` are unchanged.
*Verified by:* the numbers in the report; `go test ./...`. (Wall time
was the first measure chosen; on this host, at one-minute loads of 20
to 75 from processes outside this work, same-round time ratios ranged
from 0.88 to 2.76 and could not decide anything, so the report records
them only as an observation.)

**R9. Documents.** `docs/reports/trap-file.md`'s "Not done" points to
this change; `specs/test-command/SPEC.md` R9 and `spec/types.md` §6 state
that a `stack` record's file is not compared; `docs/reports/runtime-trap-file.md`
records the design, the fixtures, the measurement and the review.
*Verified by:* review.

## 5. Acceptance criteria

`golangci-lint run ./...` and `go test ./...` are clean (R2–R7, R8's
checksums); the freestanding fixtures pass under QEMU when it is
installed (they link the freestanding runtime, whose allocator entry
points change); R8's measurement is recorded; the documents of R9 are
updated.

## 6. Risks

- **ABI drift.** The backend's declarations and both runtimes must agree;
  a mismatch is undefined behaviour at the call, not a link error. Every
  changed entry point is exercised by a fixture in all the
  configurations it runs in (R3–R6), and the freestanding fixtures cover
  the freestanding allocator.
- **Test-hook semantics in the interpreter.** Consuming the hook at
  runtime-object creation changes what an interpreter run of a test-hook
  program does when the hook is armed before such a call; natively that
  program already trapped there, so a fixture that relied on the old
  interpreter behaviour would already have failed the harness.
