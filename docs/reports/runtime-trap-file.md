# Runtime-raised traps name the user-level call's file

**Date:** 2026-10-30. **Spec:** `specs/runtime-trap-file/SPEC.md` v0.1.
**Plan:** `specs/runtime-trap-file/PLAN.md`. **Item:** the ninth of the
ten-item list ("stack, mutex, condvar and allocation traps still carry
the program's file because their intrinsics take a span but no file").
**Finishes:** the "Not done" of `docs/reports/trap-file.md`.

## Before

A check's trap has named the trapping function's file since c4a8aca. The
traps the runtime raises inside its own entry points did not: natively,
`Mutex.lock` and `Condvar.wait` passed a span and no file, and
`Shared.new`, `Shared.clone`, `Mutex.new`, `Condvar.new`, the three
spawn entries and the four `SystemAllocator` entries passed neither. So
their records named the program's file and, for all but the first two,
the span 0–0. The interpreter reported every one at the user-level call
with its file, so the engines disagreed. The baseline's native record
for a `Shared` count overflow was `"span":{"start":0,"end":0}`; the
interpreter's was the `clone` call. They disagreed on one more thing:
natively an armed `testing.fail_next_alloc()` also failed the next
runtime-object allocation, while the interpreter let it through.

## What changed

- **One location argument.** Every runtime entry point that can raise a
  trap takes `const kern_loc *at` last, a pointer to `{ start, end, file
  }`, in both runtimes (the freestanding one has only the four allocator
  entries), and raises its traps there through `kern_trap_loc`; a null
  record is no location (span 0–0, the program's file), and a null file
  in a record is the program's file with the record's span, as for
  `kern_trap`. The backend emits one
  private constant record per distinct location in user code, and inside
  a library function fills a stack slot from the span and file its caller
  passed.
- **The interpreter** consumes the allocation-failure hook at
  `Shared.new`, `Mutex.new`, `Condvar.new`, `thread.scope`,
  `thread.spawn` and `scope.spawn`, and traps `oom` at the call, as
  natively.
- **Stack records.** The native fault handler knows no location
  (ADR-012), so a `stack` record natively keeps the span 0–0 and the
  program's file, while the interpreter names the overflowing call. The
  fixture harness and `kern test` now compare neither the span (as
  before) nor the file of a `stack` record.

## The first version cost four instructions per lock

The first implementation passed the location as three trailing register
arguments. Counting instructions retired on an uncontended loop of
10,000,000 `Mutex.lock` and unlock, it cost 4 instructions per
iteration: +7%, for information only the trap path reads. One pointer
to a constant record costs no more than the two span immediates
`kern_mutex_lock` already took, and LLVM hoists the constant's address
out of the loop, so the shipped version is cheaper than the baseline.

## Measured

**Instructions retired** (`/usr/bin/time -l`), seven interleaved runs
each of release builds made with the compiler at 54e0be6 and with this
change; medians:

| Program | before | three arguments | location record | ratio (record) |
|---|---|---|---|---|
| uncontended lock loop | 564.7 M | 604.8 M | 544.7 M | 0.965 |
| `lock_protected_map` | 1,811.8 M | 2,133.2 M¹ | 1,805.6 M | 0.997 |
| `thread_pool` | 475.8 M | 504.6 M¹ | 467.4 M | 0.982 |

¹ Measured in a separate round against its own baseline (2,126.5 M and
494.2 M); the two workloads' counts vary by about 20% between runs with
contention, which is why only same-round medians are compared. Each
R8 bound holds: the lock loop retires fewer instructions (0.965 ≤ 1.00),
and both workloads are within 0.97–1.03.

**Wall time** could not be measured: the host's one-minute load was 20
to 75 from processes outside this work, and eleven alternating
same-round runs gave time ratios from 0.88 to 2.76 (medians 1.07 and
1.04, on the three-argument version), which decide nothing. They are
recorded here only as an observation; the instruction counts are R8's
measure. The thirteen workloads' checksums are unchanged (`go test
./...`).

**Fixtures** (both engines; all four configurations unless test hooks
restrict them to debug):

| Fixture | Record now |
|---|---|
| `trap/import_mutex_recursive/` | `explicit` in `lib/lib.kern` at the inner `lock` |
| `trap/import_condvar_mismatch/` | `explicit` in `lib/lib.kern` at the second-Mutex `wait` |
| `trap/import_oom/` | `oom` in `lib/lib.kern` at `Mutex.new` (test hooks) |
| `trap/import_stack/` | `stack`; interpreter `lib/lib.kern`, native `app.kern` 0–0: agree under the new rule |
| `trap/oom_{mutex,shared,condvar}_new`, `oom_scope`, `oom_spawn`, `oom_scope_spawn` | `oom` at the call (test hooks) |
| `trap/shared_overflow` | `overflow` at the `clone` call (natively 0–0 before) |
| `trap/sysalloc_obligation` | `explicit` at the `alloc` call (natively 0–0 before) |

The freestanding fixtures, which link the freestanding allocator with
its new signatures, pass with `KERN_REQUIRE_QEMU=1`, so they ran under
QEMU rather than being skipped. A backend test checks the records
emitted for an imported module's `Mutex.new` and `lock` (spans in
`lib/lib.kern`, that file's constant) and the slot a library function
fills for its allocator call.

## Review

- **speccritic** (gpt-6.1-sol, profile general, `docs/reports/trap-file.md`
  as context): VALID_WITH_GAPS, no critical finding; its one
  clarification (the backend test's program, function and expected
  file) folded in. R4's verification was then corrected to the fixtures
  written, and §2, R1, R2 and R8 were revised for the location record
  and the instruction-count measure.
- **plancritic** (davin-go): EXECUTABLE_WITH_CLARIFICATIONS twice (91,
  86), no critical finding. Folded in: Phase 1 is gated by a standalone
  compile of both runtimes; the freestanding runtime's narrower list is
  stated; the drop-glue and function-value fallbacks are named; R8's
  bounds are mandatory, with the lock loop's baseline build recorded;
  fixtures for `thread.spawn` and `scope.spawn` added.
- **prism** (gpt-6.1-sol, staged diff): two medium findings on the first run, one per
  runtime, both on `kern_trap_loc`: the comments and spec §2 said a null
  file inside a record meant no location, while the code kept the
  record's span. The code follows the rule `kern_trap` has always had (a
  null file is the program's file, the span kept), and the interpreter
  reports the same, so the comments and §2 were corrected to it rather
  than the behaviour changed. The second run is clean.
- **realitycheck**: PARTIALLY_ALIGNED, score 100, no violation (the
  partial statuses are the tool's truncated code index).
- **verifier** (offline): 34 recommendations, all "exported symbol has no
  corresponding test" by name for functions that predate this change;
  the change adds no exported Go function.
- `golangci-lint run ./...` 0 issues; `KERN_REQUIRE_QEMU=1 go test ./...`
  green.

## Not done

- A location for native `stack` records, and for `unreachable` from a
  fault in safe code (ADR-012).
