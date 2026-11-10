# ADR-017: The runtime's architecture

**Status:** Accepted (2026-11-09), implemented as `specs/runtime/SPEC.md`
(reports `docs/reports/runtime-*.md`, summary
`docs/reports/runtime-summary.md`).
**Date:** 2026-11-09
**Spec:** `specs/runtime/SPEC.md` v0.6; reference `docs/runtime.md`.
**Interacts with:** ADR-011 (the freestanding runtime), ADR-012 (trap
handlers), ADR-015 (freestanding trap containment).

## Context

kern v0.1.0 had a runtime sized to the language's own needs: traps,
standard output, `Shared`, `Mutex`, `Condvar`, threads, the system
allocator, and enough of the system for `std/io` and `std/process`. A
program that wanted a socket, a signal, a read-write lock or a seekable
file had to leave the language. The work this ADR records made the
runtime one a program can be shipped on. Four decisions shaped it; each
had a real alternative.

## Decision 1: blocking calls on system threads, no scheduler

A thread is a system thread, and a call that waits blocks its thread.
There is no event loop in the runtime, no task type in the language, and
no second kind of function.

**Alternative: an asynchronous runtime** (tasks multiplexed over a few
threads, non-blocking I/O behind a reactor). It scales to more
connections than threads allow. It was not chosen because of what it
costs the language: kern's access rules are lexical, and a task that
suspends in the middle of a function holds its accesses across the
suspension, which either forbids borrowing across a wait or needs the
rules to reason about suspended frames. It also splits the library in
two, and makes every stack a heap allocation whose size the program
must guess. kern's first users write systems code where a thread per
connection or a small pool is the ordinary shape.

**What keeps the door open.** Sockets are non-blocking inside the
runtime already, and every waiting operation is a loop over `poll` with
a deadline. A reactor could replace the `poll` without changing an
entry's contract.

**Consequences.** Timeouts are part of every call that waits
(`accept_timeout`, `read_timeout`, `wait_ns`, `recv_ns`), since there is
no outer mechanism to cancel one. Stack size is a setting (RC2).

**Readiness without a scheduler (phase 14).** A program that wants ten
thousand idle connections was not served well by a thread for each.
`net.Poller` serves it within this decision: it is the system's
readiness queue (epoll, kqueue) as a value, one thread asks it which
sockets can be read or written without waiting, and that thread then
makes the ordinary calls. Nothing suspends: there is still no task type,
no second kind of function and no access held across a wait, so the
language pays none of what the alternative above costs. An event loop is
a `while` in the program.

## Decision 2: park is the one blocking primitive

`park(a, expect)` sleeps while an `Atomic[u32]` holds a value; `unpark`
wakes. The runtime's `Mutex` and `Condvar` are built on it, and so is
everything in `std/sync`. It is a futex on Linux and
`os_sync_wait_on_address` on macOS.

**Alternative: the system's mutex and condition variable** under each
type. Measured earlier (`docs/reports/perf-gaps.md`), the system's mutex
cost more on the uncontended path than three states in one word, and
gave no recursion check. With one primitive, a new synchronization type
needs nothing from the runtime.

**Consequences.** macOS 14.4 is the oldest supported release. Every use
must tolerate a spurious return, which the pattern "read the word, check
the condition, park on the value read" does by construction.

## Decision 3: synchronization types and policy in kern

`RwLock`, `Once`, `WaitGroup` and `Channel` are kern code over `Atomic`
and park. The files, sockets and signals of the standard modules are
kern code over runtime entries that are one system call each: option
validation, buffering, line reading, address parsing and error kinds are
all in kern.

**Alternative: write them in the runtime's C**, as `Mutex` is. That
would have been quicker to write and easier to make fast. It was not
chosen because code in kern is checked by kern: the channel's ring
buffer has six `unsafe` blocks with contracts, where the same code in C
would be all unchecked, and the interpreter runs it without a second
implementation. It also showed what the language lacked. This work
changed the language seven times for code the library needed to write:
conditional marker assertions (`unsafe assert Share for Channel where T:
Send`), an owned `match` on a payload with a destructor, on what `take`
and `replace` move out, and on what `?` unwraps; a variant name shared
by two unions; the string escapes `\r` and `\xHH`; and `swap_at`.

**What stays in the runtime:** `Mutex`, `Condvar` and `Shared`, whose
representation the compiler knows, and whatever a signal handler or a
fault handler touches.

**Consequences.** The standard modules are a body of kern code that
every compiler change must keep compiling: a regression suite of its
own.

## Decision 4: checks in debug builds, obligations in release builds

A debug build of a hosted program records every live allocation and
traps on a `free` or `realloc` that names none, or the wrong size. A
release build does not. The rule is the one the language already had for
container identity tags and arithmetic.

**Alternative: always check.** The table costs a lock and a probe per
allocation, about a fifth more time on an allocation-heavy loop. A
release build's allocator is the system's with nothing added, which is
what the benchmarks compare against.

**Alternative: never check, and lean on the sanitizers.** The sanitized
corpus is a gate of this work (RA5) and found three compiler defects.
But a sanitizer is a build a user must know to make; the debug build is
the one they already run.

**Consequences.** `kern test` runs a program in four configurations, two
of them debug, so a wrong free is found by the tests of any program that
has them.

## What was decided against, briefly

- **A runtime library linked whole.** The runtime is one translation
  unit, but the link drops what the program does not reach: an import
  nobody calls still costs time at every launch.
- **Signal handlers written in kern.** A handler would run at any
  instruction, on any thread, where no kern rule holds. A caught signal
  is recorded and waited for instead (RI1).
- **A device tree or a hardware abstraction layer for boards.** Four
  functions are what the runtime needs (RJ1); a board that needs more
  writes it in its board file.
