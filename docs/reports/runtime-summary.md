# The runtime work: summary

**Date:** 2026-11-09. **Spec:** `specs/runtime/SPEC.md` v0.6. **Plan:**
`specs/runtime/PLAN.md`, eleven phases. **Ledger:**
`specs/runtime/STATUS.md`, one row per requirement. **Reference:**
`docs/runtime.md`. **Hosts:** macOS 27.2 on arm64 and Ubuntu 24.04 on
arm64 in the `kern-linux-check` container.

kern v0.1.0 had a runtime sized to the language's own needs. This work
made it one a program can be shipped on: files, sockets, signals,
synchronization, randomness, memory diagnostics, failure diagnostics and
a way to bring the bare-metal runtime to a new board, each specified,
tested in both engines and on both hosts, and documented.

## The phases

| | Area | Report | Commit |
|---|---|---|---|
| 1 | Structure and the interface: one unit per area, a header with a contract per entry, an interface version, warning-clean and analyzer-clean sources, the sanitized corpus | `runtime-structure-abi.md` | `c45f745` |
| 2 | Lifecycle and the standard streams: the flush policy, robust writes, configuration by environment, exit | `runtime-streams.md` | `c62a4d7` |
| 3 | Threads: detached threads that clean up, stack sizes, spawns that fail without leaking | `runtime-threads.md` | `4dd62e2` |
| 4 | Failure diagnostics: line and column in the trap record, backtraces | `runtime-diagnostics.md` | `e3f7ef2` |
| 5 | Synchronization: park, timed waits, a try lock, `std/sync` | `runtime-sync.md` | `5d37494` |
| 6 | Memory: the checked allocator, the allocation report, `std/mem` | `runtime-memory.md` | `baf1818` |
| 7 | Files and streams: `File`, metadata, readers and writers, error kinds | `runtime-files.md` | `b764316` |
| 8 | Networking: `std/net` | `runtime-net.md` | `407c1e7` |
| 9 | Signals and randomness: `std/signal`, `std/rand` | `runtime-signal-rand.md` | `6296fee` |
| 10 | Freestanding: the board interface, the heap's size, a double-free check | `runtime-board.md` | `a176db2` |
| 11 | Documentation, the performance check, the final gates | this report | |

Every one of the forty-one requirements has a row in the ledger with
the commit that met it and its deviations from the spec's text.

## What the work found in the compiler

The runtime could not be made honest without fixing what stood under it.

| Finding | Found by | Fixed in |
|---|---|---|
| Assignment through an access, to a field or to an element did not destroy the previous value (rule 5.1.9): every `self = bigger` leaked | the sanitized corpus | `41b5593` |
| A temporary used as a method receiver was never destroyed (rule 5.7.2) | the sanitized corpus | `41b5593` |
| Small integer arguments reached the runtime without the extension the C side assumes | the interface conformance test | `c45f745` |
| The interpreter decremented a `Shared` count without the lock that guards its increment | a stress fixture | `4dd62e2` |
| An owned `match` on a union refused payloads whose types have destructors | writing `Channel.recv` | `5d37494` |
| The IR validator gave up on a long, correct, inlined function | a release build of a new fixture | `b764316` |
| The interpreter's `print_str` crashed on a whole array | a new fixture | `b764316` |

And seven places where the language stood in the library's way, each
changed with fixtures and reference text: conditional marker assertions,
three forms of owned `match`, variant names shared by unions, the string
escapes `\r` and `\xHH`, and `swap_at`.

## Gate 8: the sanitized corpus

`KERN_SANITIZE=1 go test ./tests -run TestFixturesSanitized` builds every
runnable fixture with AddressSanitizer and UndefinedBehaviorSanitizer and
runs it; a report fails the test, and the list of known reports is empty.
It passes on macOS and in the Linux container. `TestSanitizerDetects`
checks that the mode would see a defect: a deliberate one is reported.

## Gate 9: performance (R-P1)

Instructions retired by each of the thirteen benchmark workloads, built at
release by the compiler of v0.1.0 (`a78f7c3`) and by the compiler at the
head, the same sources, five interleaved runs each on macOS arm64, the
median of each.

| Workload | v0.1.0 | Head | Ratio | Spread of the runs (v0.1.0 / head) |
|---|---|---|---|---|
| `arena_ast` | 112,477,680 | 111,100,258 | 0.9878 | 0.3% / 0.5% |
| `btree` | 1,424,616,853 | 1,423,345,300 | 0.9991 | 0.1% / 0.1% |
| `buffer_pool` | 2,512,471,498 | 2,511,266,251 | 0.9995 | 0.0% / 0.0% |
| `hash_table` | 1,423,769,775 | 1,453,203,032 | 1.0207 | 0.4% / 0.3% |
| `intrusive_run_queue` | 1,140,845,547 | 1,139,477,997 | 0.9988 | 0.0% / 0.1% |
| `lock_free_queue` | 332,206,430 | 331,040,801 | 0.9965 | 0.1% / 0.5% |
| `lock_protected_map` | 1,736,141,868 | 1,757,224,066 | 1.0121 | 16.9% / 11.9% |
| `packet_parser` | 1,864,809,030 | 1,863,441,042 | 0.9993 | 0.0% / 0.0% |
| `page_frame_allocator` | 724,107,715 | 722,780,511 | 0.9982 | 0.1% / 0.1% |
| `ring_buffer` | 1,269,003,368 | 1,267,639,036 | 0.9989 | 0.0% / 0.0% |
| `thread_pool` | 509,227,439 | 512,854,862 | 1.0071 | 12.8% / 16.0% |
| `vector` | 356,800,975 | 355,445,365 | 0.9962 | 0.1% / 0.1% |
| `wal` | 2,186,035,932 | 2,183,691,932 | 0.9989 | 1.3% / 0.1% |

Ten workloads retire fewer instructions than at v0.1.0. Three do not:

- **`hash_table`, +2.1%, and `lock_protected_map`, +1.2% here and +8% as
  the median of fifteen runs.** Both have a map whose `grow` ends in
  `self = bigger`. At v0.1.0 that assignment did not destroy the old
  table: `leaks` counts 57 leaked blocks (143 MB) in `hash_table` and 45
  (9 MB) in `lock_protected_map`, and none at the head. Freeing them is
  the difference; in `lock_protected_map` it happens inside the lock,
  which lengthens the hold and so the waiting of the other three threads.
  This is the fix of rule 5.1.9 (phase 1, required by RA5). `hash_table`'s
  peak memory fell from 287 MB to 272 MB.
- **`thread_pool`, +0.7% here and +1.2% over fifteen runs**, inside a
  spread of 10% and more between runs of one binary: its instruction
  count depends on how often its threads find the queue's lock taken. Its
  generated code is unchanged and it leaks nothing at either commit.

**The lock.** `kern_mutex_lock` and `kern_mutex_unlock`, disassembled
from the release builds of `lock_protected_map` at both commits: the
uncontended path of `lock` (19 instructions to its return) and all of
`unlock` (8) are the same instructions. `lock`'s trap tail, which runs
only to end the program, calls `kern_trap_loc` where it had the call
inlined twice.

### Two regressions the measurement found, and their fixes

The first measurement had `ring_buffer` 20% and `wal` 12% over, and every
workload carrying 2.4 million instructions more.

- **`main` was no longer optimized twice.** At v0.1.0 the program's
  `main` was inlined into the C entry point, and LLVM ran its
  simplification passes over it a second time there, which removed two
  checks from `ring_buffer`'s loop. Phase 4 called `main` from a thunk
  with `noinline`, to give it a frame of its own for backtraces. The
  thunk now carries `main`'s location marker and calls it plainly: LLVM
  inlines it as before, a fault in it is still found in `main`, and when
  `main` does keep a frame the backtrace drops the thunk's behind it. A
  debug build made with `--backtrace` keeps the `noinline`.
- **Every program paid for the whole runtime's imports.** The dynamic
  loader binds every imported function at launch, and the functions of
  sockets, files and signals cost an empty program 2.4 million
  instructions on macOS. The link now drops the runtime code the program
  does not reach (`-dead_strip`; a section per function and
  `--gc-sections` on Linux). An empty program retires 15.4 million
  instructions where v0.1.0's retired 16.9 million, and its file is
  54 KB where it was 76 KB.

## Gate 10: the ledger

`specs/runtime/STATUS.md` lists the forty-one requirements, each with
its commit and its deviations, and what was found along the way.

## What is not done

Stated in the phase reports and the ledger; the ones a user would meet:

- **No asynchronous I/O.** A call that waits blocks its thread
  (ADR-017). Sockets are non-blocking inside the runtime, which leaves
  room for a reactor later.
- **No TLS, no Unix-domain sockets, no IPv6 scope identifiers, no socket
  options beyond `TCP_NODELAY`.**
- **No directory iteration beyond `list_dir`**, no file locking, no
  memory-mapped files.
- **The interpreter is not the system** for signals at their default
  disposition, and it writes no allocation report.
- **A freestanding image has one stack of 64 KiB**, fixed by the start-up
  code, and its start-up code is not replaceable by a flag.
- **`OpenOptions.trunc`**, not `truncate`: a reserved word cannot name a
  field.
- **The Linux container has no QEMU**, so freestanding images are booted
  on macOS and in CI, and linked but not booted there.

## Verification

At the head: `go test ./...` passes on macOS, with every freestanding
image booted under QEMU on both architectures and every concurrency
fixture marked for it run under the thread sanitizer; the whole fixture
corpus, the sanitized corpus, the area-A checks and the runtime's Go tests
pass in the Linux container; `golangci-lint run ./...` reports no issue
and `gofmt -l` lists nothing; `std/*.kern` and their prelude copies are
identical.
