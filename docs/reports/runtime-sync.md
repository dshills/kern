# Runtime, phase 5: synchronization

**Date:** 2026-11-09. **Spec:** `specs/runtime/SPEC.md` v0.6, area D (RD1 to
RD5). **Plan:** `specs/runtime/PLAN.md`, phase 5. **Hosts:** macOS 27.2 on
arm64 and Ubuntu 24.04 on arm64 in the `kern-linux-check` container.

## What changed

- **Park is a prelude operation (RD1).** `park(a, expect)` blocks while an
  `Atomic[u32]` holds `expect`; `park_ns(a, expect, ns)` also returns after
  `ns` nanoseconds, false only when the time elapsed; `unpark_one` and
  `unpark_all` wake the threads parked on the atomic. They are the calls
  the runtime's own `Mutex` and `Condvar` were already built on
  (`os_sync_wait_on_address` on macOS, a futex on Linux), now with entries
  of their own. The system compares the value and sleeps in one step, so a
  wake between a caller's check and its sleep is not lost. They are safe
  and hosted only. A time of 0 returns without a system call; the longest
  time there is sleeps until the wake.
- **A timed wait (RD2).** `Condvar.wait_ns(g, ns)` is `wait` that also
  returns after `ns` nanoseconds, with the lock held again either way. The
  checker holds its guard argument to the rule `wait` has: the edit a
  `lock()` returned, unprojected. Its traps are `wait`'s.
- **A try lock (RD3).** `Mutex.try_with(f)` takes the lock only if it is
  free, calls `f` with the guarded value, releases and returns true; it
  returns false, without calling `f`, when any thread holds the lock, the
  caller included. It is prelude code over two unsafe operations,
  `try_acquire` and `held`, which a program may also use in an `unsafe`
  block with its own pairing.
- **Conditional marker assertions (RD4, a language change).**
  `unsafe assert Share for Channel where T: Send` gives the type the
  marker only for arguments that satisfy the clause. `Send` and `Share`
  are constraints there; `Interior` takes no clause. Parser, formatter,
  the self-hosted parser, the grammar and rule 5.5.1 of
  `docs/ownership.md` carry it, and a module's interface fingerprint
  includes the conditions.
- **`std/sync` (RD5)**, written in kern over park:
  - `RwLock[T]`: any number of readers or one writer; a waiting writer
    blocks new readers.
  - `Once`: a closure run exactly once; every caller returns after it has.
  - `WaitGroup`: `add`, `done`, `wait`.
  - `Channel[T]`: a bounded queue for any number of senders and receivers,
    with blocking, non-blocking and timed forms and `close`.

  `RwLock` and `Channel` count the threads parked on each wait word and
  skip the wake when the count is zero, so an operation nobody waits for
  makes no system call. A channel's receivers and senders park on separate
  words, and a send wakes one receiver, a receive one sender: a pool of
  workers blocked on one channel is not woken as a herd for each value.

## Found on the way

- **An owned `match` on a union refused payloads with destructors.**
  `OWN_DESTRUCTURE_DESTRUCTOR` fired when any payload's type had a
  destructor; the rule (and the reference) refuse only a union that
  declares a destructor itself. `Channel.recv`'s `Option[T]` could not be
  matched for a `T` with a destructor. The check now follows the rule;
  `tests/run/match_owned_payload.kern` runs the case and
  `tests/negative/destructure_destructor.kern` holds the refusal.

## Requirement to test

| Clause | Test |
|---|---|
| RD1: `park_ns` returns true for a wake within the time, false after the time, and has waited that long | `tests/concurrency/sync_timeouts.kern` (`parking`), both engines and under the thread sanitizer |
| RD1: the longest time sleeps until the wake (the caller does not spin) | same (`forever`) |
| RD1: a time of 0 polls; `unpark` with no waiter is harmless | `tests/run/sync_basics.kern` (`prelude`) |
| RD1: no lost wake | `tests/concurrency/sync_types.kern` and `sync_channel_pool.kern`, whose every waiter depends on it; a lost wake is a hang |
| RD1: hosted only | `TestHostedOnlyInventory` (the four entries are in the generated list) |
| RD2: `wait_ns` returns true with the lock held for a notify in time, false after the time | `sync_timeouts.kern` (`condvar`) |
| RD2: the guard rule | `tests/negative/condvar_wait_ns_guard.kern` and its `_ok` twin |
| RD2: `wait`'s traps | `tests/trap/condvar_wait_ns_mismatch.kern` (a second `Mutex` on the `Condvar`) and its twin |
| RD3: true and the closure's effect when free; false, closure not called, when held, by the caller included | `sync_basics.kern` (`prelude`) |
| RD3: `try_acquire` and `held` are unsafe | `tests/negative/mutex_try_acquire_unsafe.kern` and twin |
| RD4: the marker holds only for arguments that satisfy the clause | `tests/negative/assert_where_share.kern`, `assert_where_send.kern` and twins |
| RD4: a clause must name the type's parameters and `Send` or `Share`; `Interior` takes none | `assert_where_param.kern`, `assert_where_marker.kern`, `assert_where_interior.kern` |
| RD4: syntax and formatting | `compiler/ast` goldens (`all_nodes`), the formatter's round trip over the corpus, `TestSelfhostParser` (the self-hosted parser's dump equals the compiler's) |
| RD5: each operation's result in each state, on one thread | `sync_basics.kern` |
| RD5: several threads, both engines, thread sanitizer | `sync_types.kern` (four writers and four readers of an `RwLock`; a `Once` raced by eight threads; a `WaitGroup`; three producers and two consumers), `sync_channel_pool.kern` (one slot, six producers, six consumers, two of them timed) |
| RD5: `recv_ns` returns a value sent within the time, `None` after it | `sync_timeouts.kern` (`channel`, `forever`) |
| RD5: the traps (capacity 0, `done` below zero, `add` past the limit) | `tests/trap/sync_channel_zero_capacity.kern`, `sync_waitgroup_done_below_zero.kern`, `sync_waitgroup_add_overflow.kern`; `tests/run/sync_bounds_ok.kern` is their twin |
| RD5: `Share` and `Send` only for `T: Send` | `tests/negative/sync_channel_not_send.kern` and twin |
| RD5: queued values are destroyed with the channel | `sync_basics.kern` (a counted destructor), and the sanitized run, which would report the leak |

## Deviations and open points

- **One binding cannot hold two guards.** The checker freezes a lock's
  binding while its guard is live, so "a second read through the same
  variable" is refused at compile time; the fixtures reach the lock from
  several threads or through a second `Shared` clone.
- **No poisoned state.** A trap ends the program, so a closure that traps
  inside `Once.call` or `try_with` takes the waiters down with it.
  `Once.call` from inside its own closure waits for itself.
- **`RwLock` wakes every waiter** when the lock comes free (readers can
  all proceed, and a writer must be among those woken). Only the channel
  wakes one.
- **`Channel.capacity`** is an addition to the spec's list.
- **The interpreter's wake is a broadcast**: `unpark_one` wakes every
  parked thread there, which the park contract permits (any return is
  spurious until the caller re-checks).
- **The unsafe audit** (`docs/unsafe-audit.md`) is regenerated in phase 11
  (RK2) with the other documents.

## Verification

`go test ./...` passes on macOS; the phase's fixtures, the area-A checks,
the sanitized corpus and the self-hosted parser pass in the Linux
container, where park is the futex path; `golangci-lint run ./...` reports
no issue. A stress run of the channel pool (one slot, six producers of
5,000 values, six consumers) completed 300 times, debug and release,
without a hang. The internal review's findings were answered: the
sender-receiver herd and the missing zero-time case on Linux were fixed,
the timed fixture no longer fails on a permitted spurious wake, and the
shared entry check of `wait` and `wait_ns` is one function.
