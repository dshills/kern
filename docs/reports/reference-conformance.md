# Reference conformance sweep

**Date:** 2026-11-10. **Subject:** `docs/ownership.md`, the language
reference, against the fixture corpus under `tests/`. **Hosts:** macOS
27.2 on arm64 and Ubuntu 24.04 on arm64 in the `kern-linux-check`
container.

## Why

On 2026-11-10 a program written for `examples/` used the handle of a
scoped thread after its block, which Rule 5.5.3 allows. The native
runtime read freed memory, and no fixture had ever done it. The
reference's rules with a `Diagnostics:` line are each tied to a
rejecting fixture by `go test ./tests`; a statement about what a
program does when it runs is tied to nothing. This sweep read the
reference for such statements, looked for a fixture that exercises
each, and wrote a program for every one that had none.

## Method

Each statement became a program whose expected output was written from
the reference's text, before the program was run. The program ran in
the interpreter, natively at `--opt=debug` and at `--opt=release`, and
the three outputs were compared with the expectation and with one
another. A program that agreed became a fixture; one that did not was a
finding.

## Findings

| | Rule | What the reference says | What happened | Outcome |
|---|---|---|---|---|
| 1 | 5.5.3 | A `Join` may be asked for its result after its block, and one destroyed inside the block leaves the result to the scope | natively a use after free; in both engines the destruction waited for the thread | fixed before the sweep, and the reason for it (`scoped_handles.kern`, `scoped_handles_moved.kern`) |
| 2 | 5.3.5 | A release function runs when its access ends, also when the access is consumed inside a statement | in both engines an access passed as an argument ended without it: `bump(m.lock())` left the `Mutex` locked for good and the next lock trapped `explicit`; a user's release accessor was entered and never left | fixed in lowering: an argument access ends through the path that runs its release function, and one the call's result may be derived from waits for that result (`release_accessor.kern`) |
| 3 | 5.3.1 | An owned `match` destroys the payload fields its pattern leaves unbound | a pattern must name every field of its variant (`CALL_ARITY` otherwise), so the case cannot arise | the reference corrected |

Finding 2 had the same cause as finding 1 in the corpus: every lock in
`tests/`, `examples/` and `bench/` was taken with `with`, and the only
user-defined release accessor in the repository was the buffer pool
benchmark's, which binds it with `with` too.

## Statements that held

Each row is a statement that had no fixture, or one covering a single
case of several, and now has the fixture named.

| Rule | Statement | Fixture |
|---|---|---|
| 4.1.2 | `-`, `*` and negation trap `overflow`; a signed sum does; `%` by zero and a signed `/` by zero trap `div_zero`; the minimum divided by, and its remainder by, `-1` trap `overflow`; in every build | `trap/overflow_sub`, `overflow_mul`, `overflow_neg`, `overflow_add_signed`, `div_zero_rem`, `div_zero_signed`, `overflow_min_div`, `overflow_min_rem` |
| 4.1.6 | `simd_shuffle` traps `bounds` on a lane index of 2N or more | `trap/simd_shuffle_bounds` |
| 5.1.9, 5.7.2 | A scope left by its end, `return`, `?`, `break` or `continue` destroys its locals newest first, with those of the scopes left with it; a destructor runs before the fields, which go last first; an array goes last element first | `run/scope_exits` |
| 5.3.1 | A `with` over an owned value destroys it where the block is left, early exits included; an owned `match` moves the payload into the arm and a wildcard arm destroys the whole value; `take` leaves the default and `replace` hands back what was there | `run/scope_exits` |
| 5.3.5 | A release function runs once at each kind of exit, in reverse order of creation, and not on a trap | `run/release_accessor`, `trap/release_not_on_trap` |
| 5.6.2 | A dereference checks occupancy before generation; `Pool.free` makes a dereference's checks; `get2` validates its first handle, then its second, and only then compares them; a handle past another container's slots traps `bounds` in every build; a slot at the last generation is retired and never reused | `trap/pool_get_freed`, `pool_free_reused`, `pool_get2_first_vacant`, `pool_get2_second_stale`, `pool_get2_same_vacant`, `pool_foreign_bounds`, `arena_foreign_bounds`, `pool_slot_retired` |
| 5.6.1, 5.6.5 | A `Map` hands back the value an insert replaced and a remove held, and destroys the rest; `BoxIn.try_new_in` that cannot allocate destroys the value, then the allocator; `VecIn.try_push` that cannot allocate destroys its value | `run/held_values` |
| 5.1.1, 5.5.2, 5.5.4 | A value sent to a closed channel comes back; values left in a channel are destroyed with it; a `Shared` value is destroyed by its last owner on whichever thread; a `Mutex` destroys what it guards; a thread's result is the joiner's | `concurrency/owners_across_threads` |
| 5.7.4 | A trap inside a destructor terminates | `trap/destructor_traps` |
| Trap handlers | `trap.set_handler` returns the handler it replaced, and a handler runs once | `trap/handler_previous` |
| `docs/runtime.md` §2 | `exit` flushes standard output and runs no destructor | `run/exit_runs_no_destructors` |

Already exercised, and left as they were: spawn failures and what they
destroy (`spawn_fail_drop`), `out` storage on a failed initialization
(`out_*`), temporaries and operands (`temp_receiver_drop`,
`try_in_arguments`), assignment (`assign_destroys_previous`), the
fallible allocation forms (`*_try`, `*_try_oom`), the locks' and the
condition variable's traps, thread-local storage, the allocator's
obligations, and `std/sync` across threads.

## What changed besides fixtures

- **A trap fixture can state its output.** The harness compared a trap
  fixture's standard output between the engines and with nothing else.
  A file named after the fixture is now compared with what the program
  wrote before it trapped, so what a trap must not run shows by its
  absence (`release_not_on_trap`, `destructor_traps`, `handler_previous`,
  `pool_slot_retired`).
- **The examples are sanitized.** `TestFixturesSanitized` covered
  `tests/run` and `tests/concurrency`. It now runs every example that
  has a `main`, nine programs, the five applications among them.

## What the sweep did not cover

- Statements about what the checker accepts and rejects: those have
  their fixtures by construction (`TestRegistryCoverage`).
- Layout (Rule 4.1.5), the FFI subset (Rule 7.2) and freestanding mode
  (Rule 6.8), which the differential, FFI and freestanding suites own.
- The standard library's own references (`docs/runtime.md` beyond `exit`,
  and the comments at the head of each `std/` module). Finding 1 came
  from the language reference, but the same gap, a stated behaviour
  with no test, is as likely there.
